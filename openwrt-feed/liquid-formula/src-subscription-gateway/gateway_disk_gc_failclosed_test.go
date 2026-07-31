package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type commitTestGCSeed struct {
	root        string
	store       generationStore
	filesystem  *commitTestFilesystem
	hook        *commitTestFaultHook
	objects     [][]byte
	generations []string
}

func newCommitTestGCSeed(t *testing.T) commitTestGCSeed {
	t.Helper()
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	store := newCommitTestStore(
		root,
		commitTestRandom(0x31, 0x32, 0x33, 0x34, 0x35),
		filesystem,
		hook,
	)
	seed := commitTestGCSeed{
		root:       root,
		store:      store,
		filesystem: filesystem,
		hook:       hook,
		objects: [][]byte{
			commitTestObject("A"),
			commitTestObject("B"),
			commitTestObject("C"),
			commitTestObject("D"),
		},
	}
	parent := ""
	for _, object := range seed.objects[:3] {
		result, err, _ := commitTestCommit(
			t, store, commitTestCandidate(parent, object),
		)
		selection := commitTestRequireCommitted(t, result, err)
		parent = selection.Generation.GenerationID
		seed.generations = append(seed.generations, parent)
	}
	return seed
}

func (seed *commitTestGCSeed) commitFourthBlockedAtGC(
	t *testing.T,
	mutate func(currentGeneration string),
) generationCommitResult {
	t.Helper()
	seed.hook.reset()
	ready := make(chan struct{})
	release := make(chan struct{})
	seed.hook.block("before_gc", ready, release)
	parent := seed.generations[len(seed.generations)-1]
	resultChannel := make(chan commitTestAsyncResult, 1)
	ctx, _ := withNewCurrentCommitGate(context.Background())
	go func() {
		result, err := seed.store.Commit(
			ctx,
			commitTestCandidate(parent, seed.objects[3]),
		)
		resultChannel <- commitTestAsyncResult{result: result, err: err}
	}()
	commitTestAwait(t, ready, "before-gc hook")
	currentGeneration := commitTestID(0x34)
	mutate(currentGeneration)
	close(release)
	completed := commitTestAwait(
		t, resultChannel, "post-GC commit result",
	)
	commitTestRequireCommitted(t, completed.result, completed.err)
	seed.generations = append(seed.generations, currentGeneration)
	return completed.result
}

func (seed *commitTestGCSeed) requireAllSeedArtifacts(t *testing.T) {
	t.Helper()
	commitTestRequireEntryNames(
		t,
		filepath.Join(seed.root, "generations"),
		seed.generations,
	)
	objectNames := make([]string, 0, len(seed.objects))
	for _, object := range seed.objects {
		objectNames = append(
			objectNames,
			commitTestObjectDigest(object)+".json",
		)
	}
	commitTestRequireEntryNames(
		t,
		filepath.Join(seed.root, "objects"),
		objectNames,
	)
}

func TestDiskGCSkipsEntireSweepForRepeatedParentIDs(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		parent func(commitTestGCSeed, string) (string, string)
	}{
		{
			name: "self parent",
			parent: func(
				seed commitTestGCSeed,
				_ string,
			) (string, string) {
				generation := seed.generations[2]
				return generation, generation
			},
		},
		{
			name: "cycle back to current",
			parent: func(
				seed commitTestGCSeed,
				current string,
			) (string, string) {
				return seed.generations[2], current
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			seed := newCommitTestGCSeed(t)
			result := seed.commitFourthBlockedAtGC(
				t,
				func(current string) {
					generation, parent := testCase.parent(seed, current)
					commitTestRewriteManifestParent(
						t, seed.root, generation, parent,
					)
				},
			)
			if !result.Committed {
				t.Fatalf("post-current parent fault result = %#v", result)
			}
			seed.requireAllSeedArtifacts(t)
		})
	}
}

func TestDiskGCSkipsEntireSweepForCycleOutsideRetentionWindow(
	t *testing.T,
) {
	seed := newCommitTestGCSeed(t)
	seed.commitFourthBlockedAtGC(
		t,
		func(_ string) {
			oldest := seed.generations[0]
			commitTestRewriteManifestParent(
				t, seed.root, oldest, oldest,
			)
		},
	)
	seed.requireAllSeedArtifacts(t)
}

func TestDiskGCSkipsEntireSweepWhenAnyRetainedManifestIsInvalid(
	t *testing.T,
) {
	for _, retainedOffset := range []int{0, 1, 2} {
		t.Run(
			[]string{"grandparent", "parent", "current"}[retainedOffset],
			func(t *testing.T) {
				seed := newCommitTestGCSeed(t)
				seed.commitFourthBlockedAtGC(
					t,
					func(current string) {
						retained := []string{
							seed.generations[1],
							seed.generations[2],
							current,
						}
						path := filepath.Join(
							seed.root,
							"generations",
							retained[retainedOffset],
							"manifest.json",
						)
						if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
							t.Fatal(err)
						}
					},
				)
				seed.requireAllSeedArtifacts(t)
			},
		)
	}
}

func commitTestRewriteManifestParent(
	t *testing.T,
	root string,
	generation string,
	parent string,
) {
	t.Helper()
	path := filepath.Join(
		root, "generations", generation, "manifest.json",
	)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	manifest["parent"] = parent
	contents, err = json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
}
