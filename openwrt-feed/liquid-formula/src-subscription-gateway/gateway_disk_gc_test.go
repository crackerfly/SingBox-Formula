package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestDiskGCUsesCurrentParentChainAndMarksAllRetainedObjects(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	store := newCommitTestStore(
		root,
		commitTestRandom(0x11, 0x12, 0x13, 0x14),
		filesystem,
		hook,
	)
	objects := [][]byte{
		commitTestObject("A"),
		commitTestObject("B"),
		commitTestObject("C"),
		commitTestObject("D"),
	}
	generations := make([]string, 0, len(objects))
	parent := ""
	for index, object := range objects[:3] {
		result, err, _ := commitTestCommit(
			t, store, commitTestCandidate(parent, object),
		)
		selection := commitTestRequireCommitted(t, result, err)
		generations = append(
			generations, selection.Generation.GenerationID,
		)
		parent = selection.Generation.GenerationID
		if parent != commitTestID(byte(0x11+index)) {
			t.Fatalf(
				"generation %d = %q", index+1, parent,
			)
		}
	}

	// Make the generation and object that must be collected look newest while
	// retained parents look old. A collector that selects by mtime will fail.
	newest := time.Unix(4102444800, 0)
	oldest := time.Unix(946684800, 0)
	if err := os.Chtimes(
		filepath.Join(root, "generations", generations[0]),
		newest,
		newest,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(
		filepath.Join(
			root, "objects",
			commitTestObjectDigest(objects[0])+".json",
		),
		newest,
		newest,
	); err != nil {
		t.Fatal(err)
	}
	for _, generation := range generations[1:] {
		if err := os.Chtimes(
			filepath.Join(root, "generations", generation),
			oldest,
			oldest,
		); err != nil {
			t.Fatal(err)
		}
	}
	for _, object := range objects[1:3] {
		if err := os.Chtimes(
			filepath.Join(
				root, "objects",
				commitTestObjectDigest(object)+".json",
			),
			oldest,
			oldest,
		); err != nil {
			t.Fatal(err)
		}
	}

	result, err, _ := commitTestCommit(
		t, store, commitTestCandidate(parent, objects[3]),
	)
	current := commitTestRequireCommitted(t, result, err)
	generations = append(
		generations, current.Generation.GenerationID,
	)
	if current.Generation.GenerationID != commitTestID(0x14) {
		t.Fatalf(
			"fourth generation = %q", current.Generation.GenerationID,
		)
	}

	commitTestRequireEntryNames(
		t,
		filepath.Join(root, "generations"),
		[]string{generations[1], generations[2], generations[3]},
	)
	commitTestRequireEntryNames(
		t,
		filepath.Join(root, "objects"),
		[]string{
			commitTestObjectDigest(objects[1]) + ".json",
			commitTestObjectDigest(objects[2]) + ".json",
			commitTestObjectDigest(objects[3]) + ".json",
		},
	)
}

func commitTestRequireEntryNames(
	t *testing.T,
	directory string,
	want []string,
) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read %s: %v", directory, err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if !equalCommitTestStrings(got, want) {
		t.Fatalf("%s entries = %#v, want %#v", directory, got, want)
	}
}
