package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

type commitTestAsyncResult struct {
	result generationCommitResult
	err    error
}

func TestDiskCommitEveryPreCurrentFilesystemFailurePreservesOldCurrent(
	t *testing.T,
) {
	probeRoot := filepath.Join(t.TempDir(), "probe")
	probeFilesystem := newCommitTestFilesystem()
	probeHook := &commitTestFaultHook{}
	probeStore := newCommitTestStore(
		probeRoot,
		commitTestRandom(0x61, 0x62),
		probeFilesystem,
		probeHook,
	)
	firstResult, firstErr, _ := commitTestCommit(
		t,
		probeStore,
		commitTestCandidate("", commitTestObject("Old")),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	probeFilesystem.reset()
	probeHook.reset()
	secondResult, secondErr, _ := commitTestCommit(
		t,
		probeStore,
		commitTestCandidate(
			first.Generation.GenerationID,
			commitTestObject("New"),
		),
	)
	commitTestRequireCommitted(t, secondResult, secondErr)

	operations := probeFilesystem.snapshot()
	currentRename := -1
	for index, operation := range operations {
		if operation.Kind == "rename_replace" &&
			operation.NewName == "current" {
			currentRename = index
			break
		}
	}
	if currentRename < 1 {
		t.Fatalf("healthy trace has no pre-current work: %#v", operations)
	}
	preCurrent := operations[:currentRename]
	var fileSyncCount, directorySyncCount, noReplaceCount int
	for _, operation := range preCurrent {
		switch operation.Kind {
		case "sync":
			if operation.IsDirectory {
				directorySyncCount++
			} else {
				fileSyncCount++
			}
		case "rename_no_replace":
			noReplaceCount++
		default:
			t.Fatalf(
				"unexpected pre-current filesystem operation: %#v",
				operation,
			)
		}
	}
	if fileSyncCount < 1 ||
		directorySyncCount < 1 ||
		noReplaceCount < 2 {
		t.Fatalf(
			"incomplete healthy pre-current trace: file-sync=%d directory-sync=%d no-replace=%d ops=%#v",
			fileSyncCount,
			directorySyncCount,
			noReplaceCount,
			preCurrent,
		)
	}

	for operationIndex, operation := range preCurrent {
		testName := fmt.Sprintf(
			"%02d_%s_%s",
			operationIndex+1,
			operation.Kind,
			operation.NewName,
		)
		t.Run(testName, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "subscriptions")
			filesystem := newCommitTestFilesystem()
			hook := &commitTestFaultHook{}
			store := newCommitTestStore(
				root,
				commitTestRandom(0x61, 0x62),
				filesystem,
				hook,
			)
			seedResult, seedErr, _ := commitTestCommit(
				t,
				store,
				commitTestCandidate("", commitTestObject("Old")),
			)
			seed := commitTestRequireCommitted(
				t, seedResult, seedErr,
			)
			oldCurrent := commitTestReadCurrent(t, root)
			filesystem.reset()
			hook.reset()
			filesystem.failOperation(operationIndex)

			result, _, gate := commitTestCommit(
				t,
				store,
				commitTestCandidate(
					seed.Generation.GenerationID,
					commitTestObject("New"),
				),
			)
			if result.Committed || gate.begun() {
				t.Fatalf(
					"pre-current failure committed: result=%#v begun=%t",
					result, gate.begun(),
				)
			}
			commitTestRequireOldCurrent(
				t,
				root,
				store,
				oldCurrent,
				seed.Generation.GenerationID,
			)
		})
	}
}

func TestDiskCommitEveryPreCurrentFaultHookPreservesOldCurrent(
	t *testing.T,
) {
	newObject := commitTestObject("New")
	for _, stage := range []string{
		"before_object_rename:" + commitTestObjectDigest(newObject),
		"before_generation_rename",
		"before_current_rename",
	} {
		t.Run(stage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "subscriptions")
			filesystem := newCommitTestFilesystem()
			hook := &commitTestFaultHook{}
			store := newCommitTestStore(
				root,
				commitTestRandom(0x71, 0x72),
				filesystem,
				hook,
			)
			firstResult, firstErr, _ := commitTestCommit(
				t,
				store,
				commitTestCandidate("", commitTestObject("Old")),
			)
			first := commitTestRequireCommitted(
				t, firstResult, firstErr,
			)
			oldCurrent := commitTestReadCurrent(t, root)
			filesystem.reset()
			hook.reset()
			hook.fail(stage)

			result, _, gate := commitTestCommit(
				t,
				store,
				commitTestCandidate(
					first.Generation.GenerationID, newObject,
				),
			)
			if result.Committed || gate.begun() {
				t.Fatalf(
					"hook %q committed: result=%#v begun=%t",
					stage, result, gate.begun(),
				)
			}
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

func TestDiskCommitDeadlineBeforeBeginNeverPublishesCurrent(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	store := newCommitTestStore(
		root, commitTestRandom(0x81, 0x82), filesystem, hook,
	)
	firstResult, firstErr, _ := commitTestCommit(
		t, store, commitTestCandidate("", commitTestObject("Old")),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	oldCurrent := commitTestReadCurrent(t, root)
	filesystem.reset()
	hook.reset()

	ready := make(chan struct{})
	release := make(chan struct{})
	hook.block("before_current_rename", ready, release)
	baseContext, cancel := context.WithCancel(context.Background())
	ctx, gate := withNewCurrentCommitGate(baseContext)
	resultChannel := make(chan commitTestAsyncResult, 1)
	go func() {
		result, err := store.Commit(
			ctx,
			commitTestCandidate(
				first.Generation.GenerationID,
				commitTestObject("New"),
			),
		)
		resultChannel <- commitTestAsyncResult{result: result, err: err}
	}()

	commitTestAwait(t, ready, "before-current hook")
	if gate.begun() {
		t.Fatal("commit gate began before before_current_rename returned")
	}
	cancel()
	close(release)
	completed := commitTestAwait(t, resultChannel, "denied commit result")
	if completed.result.Committed || gate.begun() {
		t.Fatalf(
			"deadline-before-begin result=%#v err=%v begun=%t",
			completed.result, completed.err, gate.begun(),
		)
	}
	for _, operation := range filesystem.snapshot() {
		if operation.Kind == "rename_replace" &&
			operation.NewName == "current" {
			t.Fatalf("current rename ran after denied begin: %#v", operation)
		}
	}
	commitTestRequireOldCurrent(
		t, root, store, oldCurrent, first.Generation.GenerationID,
	)
}

func TestDiskCommitBeginsImmediatelyBeforeCurrentRenameAndFinishes(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	store := newCommitTestStore(
		root, commitTestRandom(0x91, 0x92), filesystem, hook,
	)
	firstResult, firstErr, _ := commitTestCommit(
		t, store, commitTestCandidate("", commitTestObject("Old")),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	filesystem.reset()
	hook.reset()

	ready := make(chan struct{})
	release := make(chan struct{})
	filesystem.blockCurrent(ready, release)
	baseContext, cancel := context.WithCancel(context.Background())
	ctx, gate := withNewCurrentCommitGate(baseContext)
	resultChannel := make(chan commitTestAsyncResult, 1)
	go func() {
		result, err := store.Commit(
			ctx,
			commitTestCandidate(
				first.Generation.GenerationID,
				commitTestObject("New"),
			),
		)
		resultChannel <- commitTestAsyncResult{result: result, err: err}
	}()

	commitTestAwait(t, ready, "current rename")
	if !gate.begun() {
		t.Fatal("current rename was entered before commit gate began")
	}
	cancel()
	select {
	case completed := <-resultChannel:
		t.Fatalf(
			"commit returned while the begun rename was blocked: %#v",
			completed,
		)
	default:
	}
	close(release)
	completed := commitTestAwait(t, resultChannel, "begun commit result")
	second := commitTestRequireCommitted(
		t, completed.result, completed.err,
	)
	if !gate.begun() ||
		second.Generation.GenerationID != commitTestID(0x92) {
		t.Fatalf(
			"begun commit did not finish: selection=%#v begun=%t",
			second, gate.begun(),
		)
	}
	if got := commitTestReadCurrent(t, root); !bytes.Equal(
		got,
		[]byte(second.Generation.GenerationID+"\n"),
	) {
		t.Fatalf("current after begun commit = %q", got)
	}
}

func TestDiskCommitUsesOnlyApprovedPreCurrentHookNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	store := newCommitTestStore(
		root, commitTestRandom(0xa1), filesystem, hook,
	)
	object := commitTestObject("Hooks")
	result, err, _ := commitTestCommit(
		t, store, commitTestCandidate("", object),
	)
	commitTestRequireCommitted(t, result, err)
	got := hook.snapshot()
	want := []string{
		"before_object_rename:" + commitTestObjectDigest(object),
		"before_generation_rename",
		"before_current_rename",
		"after_current_rename_before_root_sync",
		"before_gc",
	}
	if !equalCommitTestStrings(got, want) {
		t.Fatalf("fault-hook sequence = %#v, want %#v", got, want)
	}
}

func equalCommitTestStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
