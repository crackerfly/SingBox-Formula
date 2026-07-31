package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type diskReviewMutationHook struct {
	target string
	mutate func() error
}

func (hook *diskReviewMutationHook) invoke(stage string) error {
	if stage == hook.target && hook.mutate != nil {
		return hook.mutate()
	}
	return nil
}

func diskReviewStagingDirectory(
	root string,
) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	staging := ""
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".staging-") {
			if staging != "" {
				return "", errors.New("multiple staging directories")
			}
			staging = filepath.Join(root, entry.Name())
		}
	}
	if staging == "" {
		return "", errors.New("staging directory missing")
	}
	return staging, nil
}

func diskReviewStore(
	root string,
	randomValues []byte,
	filesystem *commitTestFilesystem,
	hook *diskReviewMutationHook,
) generationStore {
	return newDiskGenerationStoreWithDependencies(
		root,
		diskGenerationStoreDependencies{
			GenerationRandom: commitTestRandom(randomValues...),
			FS:               filesystem,
			FaultHook:        hook.invoke,
		},
	)
}

func diskReviewRequireGenericPreCurrentFailure(
	t *testing.T,
	result generationCommitResult,
	err error,
	gate *currentCommitGate,
	secret string,
) {
	t.Helper()
	if result.Committed || gate.begun() {
		t.Fatalf(
			"invalid staged state committed: result=%#v begun=%t",
			result, gate.begun(),
		)
	}
	if !errors.Is(err, errDiskStateInvalid) ||
		err.Error() != "subscription state invalid" {
		t.Fatalf("pre-current error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("pre-current error leaked canary %q: %v", secret, err)
	}
}

func TestDiskCommitExistingObjectCleanupFailureCannotReachCommitGate(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &diskReviewMutationHook{}
	store := diskReviewStore(
		root, []byte{0x11, 0x12}, filesystem, hook,
	)
	object := commitTestObject("EEXIST_CLEANUP_SECRET_CANARY")
	firstResult, firstErr, _ := commitTestCommit(
		t, store, commitTestCandidate("", object),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	oldCurrent := commitTestReadCurrent(t, root)
	digest := commitTestObjectDigest(object)

	hook.target = "before_object_rename:" + digest
	hook.mutate = func() error {
		staging, err := diskReviewStagingDirectory(root)
		if err != nil {
			return err
		}
		stagedObject := filepath.Join(
			staging, ".object-"+digest+".json",
		)
		if err := os.Remove(stagedObject); err != nil {
			return err
		}
		return os.Mkdir(stagedObject, 0700)
	}
	result, err, gate := commitTestCommit(
		t,
		store,
		commitTestCandidate(first.Generation.GenerationID, object),
	)
	diskReviewRequireGenericPreCurrentFailure(
		t,
		result,
		err,
		gate,
		"EEXIST_CLEANUP_SECRET_CANARY",
	)
	commitTestRequireOldCurrent(
		t,
		root,
		store,
		oldCurrent,
		first.Generation.GenerationID,
	)
}

func TestDiskCommitGenerationIsExactBeforePublishing(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(string) error
	}{
		{
			name: "extra entry",
			mutate: func(staging string) error {
				return os.WriteFile(
					filepath.Join(
						staging,
						"GENERATION_EXTRA_SECRET_CANARY",
					),
					[]byte("unexpected"),
					0600,
				)
			},
		},
		{
			name: "missing status",
			mutate: func(staging string) error {
				return os.Remove(
					filepath.Join(staging, "status.json"),
				)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "subscriptions")
			filesystem := newCommitTestFilesystem()
			hook := &diskReviewMutationHook{}
			store := diskReviewStore(
				root, []byte{0x21, 0x22}, filesystem, hook,
			)
			firstResult, firstErr, _ := commitTestCommit(
				t,
				store,
				commitTestCandidate(
					"", commitTestObject("Old"),
				),
			)
			first := commitTestRequireCommitted(
				t, firstResult, firstErr,
			)
			oldCurrent := commitTestReadCurrent(t, root)
			hook.target = "before_generation_rename"
			hook.mutate = func() error {
				staging, err := diskReviewStagingDirectory(root)
				if err != nil {
					return err
				}
				return testCase.mutate(staging)
			}

			result, err, gate := commitTestCommit(
				t,
				store,
				commitTestCandidate(
					first.Generation.GenerationID,
					commitTestObject(
						"GENERATION_SECRET_CANARY",
					),
				),
			)
			diskReviewRequireGenericPreCurrentFailure(
				t,
				result,
				err,
				gate,
				"GENERATION_SECRET_CANARY",
			)
			commitTestRequireOldCurrent(
				t,
				root,
				store,
				oldCurrent,
				first.Generation.GenerationID,
			)
		})
	}
}

func TestDiskCommitRevalidatesPublishedGenerationBeforeCurrent(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &diskReviewMutationHook{}
	store := diskReviewStore(
		root, []byte{0x31, 0x32}, filesystem, hook,
	)
	firstResult, firstErr, _ := commitTestCommit(
		t,
		store,
		commitTestCandidate("", commitTestObject("Old")),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	oldCurrent := commitTestReadCurrent(t, root)
	secondID := commitTestID(0x32)
	hook.target = "before_current_rename"
	hook.mutate = func() error {
		return os.WriteFile(
			filepath.Join(
				root,
				"generations",
				secondID,
				"aggregate.json",
			),
			[]byte(
				`{"outbounds":[{"tag":"CURRENT_REVALIDATE_SECRET_CANARY","type":"block"}]}`,
			),
			0600,
		)
	}

	result, err, gate := commitTestCommit(
		t,
		store,
		commitTestCandidate(
			first.Generation.GenerationID,
			commitTestObject("New"),
		),
	)
	diskReviewRequireGenericPreCurrentFailure(
		t,
		result,
		err,
		gate,
		"CURRENT_REVALIDATE_SECRET_CANARY",
	)
	commitTestRequireOldCurrent(
		t,
		root,
		store,
		oldCurrent,
		first.Generation.GenerationID,
	)

	selection, loadErr := store.LoadCurrent(context.Background())
	if loadErr != nil ||
		selection.Kind != currentPresent ||
		selection.Generation.GenerationID !=
			first.Generation.GenerationID {
		t.Fatalf(
			"old current was not still loadable: selection=%#v err=%v",
			selection, loadErr,
		)
	}
}

func TestDiskCommitCleanupNeverLeavesMalformedGenerationInPool(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &diskReviewMutationHook{}
	store := diskReviewStore(
		root, []byte{0x71, 0x72}, filesystem, hook,
	)
	firstResult, firstErr, _ := commitTestCommit(
		t,
		store,
		commitTestCandidate("", commitTestObject("Old")),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	oldCurrent := commitTestReadCurrent(t, root)
	secondID := commitTestID(0x72)

	// Make the last direct unlink fail after cleanup has already removed the
	// first two files. Production I/O errors at the same boundary must not
	// turn a complete unpublished generation into malformed permanent state.
	hook.target = "before_current_rename"
	hook.mutate = func() error {
		status := filepath.Join(
			root, "generations", secondID, "status.json",
		)
		if err := os.Remove(status); err != nil {
			return err
		}
		return os.Mkdir(status, 0700)
	}
	result, err, gate := commitTestCommit(
		t,
		store,
		commitTestCandidate(
			first.Generation.GenerationID,
			commitTestObject("CLEANUP_SECRET_CANARY"),
		),
	)
	diskReviewRequireGenericPreCurrentFailure(
		t,
		result,
		err,
		gate,
		"CLEANUP_SECRET_CANARY",
	)
	commitTestRequireOldCurrent(
		t,
		root,
		store,
		oldCurrent,
		first.Generation.GenerationID,
	)

	if _, statErr := os.Lstat(
		filepath.Join(root, "generations", secondID),
	); errors.Is(statErr, os.ErrNotExist) {
		return
	} else if statErr != nil {
		t.Fatalf("inspect unpublished generation: %v", statErr)
	}
	state, absent, openErr := diskOpenExistingState(root)
	if openErr != nil || absent {
		t.Fatalf(
			"open state after failed cleanup: absent=%t err=%v",
			absent,
			openErr,
		)
	}
	defer state.close()
	if _, loadErr := diskLoadGeneration(
		context.Background(),
		state.generations,
		state.objects,
		secondID,
	); loadErr != nil {
		t.Fatalf(
			"cleanup left malformed generation %s in generations: %v",
			secondID,
			loadErr,
		)
	}
}
