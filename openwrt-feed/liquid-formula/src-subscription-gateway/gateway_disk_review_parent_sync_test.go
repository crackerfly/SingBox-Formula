package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type diskReviewParentSyncFilesystem struct {
	delegate *commitTestFilesystem
	parent   string

	mutex         sync.Mutex
	calls         int
	failRemaining int
}

func (filesystem *diskReviewParentSyncFilesystem) Sync(
	file *os.File,
) error {
	filesystem.mutex.Lock()
	isParent := filepath.Clean(file.Name()) ==
		filepath.Clean(filesystem.parent)
	if isParent {
		filesystem.calls++
		if filesystem.failRemaining > 0 {
			filesystem.failRemaining--
			filesystem.mutex.Unlock()
			return errCommitTestInjected
		}
	}
	filesystem.mutex.Unlock()
	return filesystem.delegate.Sync(file)
}

func (filesystem *diskReviewParentSyncFilesystem) RenameNoReplace(
	oldDirectory *os.File,
	oldName string,
	newDirectory *os.File,
	newName string,
) error {
	return filesystem.delegate.RenameNoReplace(
		oldDirectory, oldName, newDirectory, newName,
	)
}

func (filesystem *diskReviewParentSyncFilesystem) RenameReplace(
	oldDirectory *os.File,
	oldName string,
	newDirectory *os.File,
	newName string,
) error {
	return filesystem.delegate.RenameReplace(
		oldDirectory, oldName, newDirectory, newName,
	)
}

func (filesystem *diskReviewParentSyncFilesystem) callCount() int {
	filesystem.mutex.Lock()
	defer filesystem.mutex.Unlock()
	return filesystem.calls
}

func TestDiskCommitSyncsNewStateParentBeforeCurrentRename(
	t *testing.T,
) {
	parent := t.TempDir()
	root := filepath.Join(parent, "subscriptions")
	delegate := newCommitTestFilesystem()
	filesystem := &diskReviewParentSyncFilesystem{
		delegate: delegate,
		parent:   parent,
	}
	hook := &diskReviewMutationHook{}
	store := newDiskGenerationStoreWithDependencies(
		root,
		diskGenerationStoreDependencies{
			GenerationRandom: commitTestRandom(0x71),
			FS:               filesystem,
			FaultHook:        hook.invoke,
		},
	)
	result, err, _ := commitTestCommit(
		t,
		store,
		commitTestCandidate(
			"", commitTestObject("Parent Sync"),
		),
	)
	commitTestRequireCommitted(t, result, err)
	operations := delegate.snapshot()
	parentSync := commitTestFindOperation(
		operations,
		func(operation commitTestFSOperation) bool {
			return operation.Kind == "sync" &&
				operation.IsDirectory &&
				filepath.Clean(operation.Path) ==
					filepath.Clean(parent)
		},
	)
	currentRename := commitTestFindOperation(
		operations,
		func(operation commitTestFSOperation) bool {
			return operation.Kind == "rename_replace" &&
				operation.NewName == "current"
		},
	)
	if parentSync < 0 ||
		currentRename < 0 ||
		parentSync >= currentRename {
		t.Fatalf(
			"new root parent sync=%d current rename=%d operations=%#v",
			parentSync, currentRename, operations,
		)
	}
}

func TestDiskCommitParentSyncFailureDoesNotPublishAndRetrySucceeds(
	t *testing.T,
) {
	parent := t.TempDir()
	root := filepath.Join(parent, "subscriptions")
	delegate := newCommitTestFilesystem()
	filesystem := &diskReviewParentSyncFilesystem{
		delegate:      delegate,
		parent:        parent,
		failRemaining: 1,
	}
	hook := &diskReviewMutationHook{}
	store := newDiskGenerationStoreWithDependencies(
		root,
		diskGenerationStoreDependencies{
			GenerationRandom: commitTestRandom(0x81, 0x82),
			FS:               filesystem,
			FaultHook:        hook.invoke,
		},
	)
	candidate := commitTestCandidate(
		"", commitTestObject("PARENT_SYNC_SECRET_CANARY"),
	)
	firstResult, firstErr, firstGate := commitTestCommit(
		t, store, candidate,
	)
	diskReviewRequireGenericPreCurrentFailure(
		t,
		firstResult,
		firstErr,
		firstGate,
		"PARENT_SYNC_SECRET_CANARY",
	)
	if _, statErr := os.Lstat(
		filepath.Join(root, "current"),
	); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("parent sync failure published current: %v", statErr)
	}
	if filesystem.callCount() != 1 {
		t.Fatalf(
			"failed parent sync calls = %d, want 1",
			filesystem.callCount(),
		)
	}

	delegate.reset()
	secondResult, secondErr, secondGate := commitTestCommit(
		t, store, candidate,
	)
	selection := commitTestRequireCommitted(
		t, secondResult, secondErr,
	)
	if !secondGate.begun() {
		t.Fatal("successful retry did not reach commit gate")
	}
	if filesystem.callCount() < 2 {
		t.Fatalf(
			"retry did not resync parent: calls=%d",
			filesystem.callCount(),
		)
	}
	if got := string(commitTestReadCurrent(t, root)); got !=
		selection.Generation.GenerationID+"\n" {
		t.Fatalf("retry current = %q", got)
	}
}
