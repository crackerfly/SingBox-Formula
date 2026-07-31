package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestDiskCommitAfterCurrentHookFailureRemainsCommittedAndSkipsGC(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	store := newCommitTestStore(
		root, commitTestRandom(0xb1, 0xb2), filesystem, hook,
	)
	firstResult, firstErr, _ := commitTestCommit(
		t, store, commitTestCandidate("", commitTestObject("Old")),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	filesystem.reset()
	hook.reset()
	hook.fail("after_current_rename_before_root_sync")

	result, err, gate := commitTestCommit(
		t,
		store,
		commitTestCandidate(
			first.Generation.GenerationID,
			commitTestObject("New"),
		),
	)
	second := commitTestRequireCommitted(t, result, err)
	if !gate.begun() {
		t.Fatal("post-rename failure did not retain begun gate")
	}
	if result.WarningCode != "current_dir_sync_failed" {
		t.Fatalf(
			"post-rename warning = %q, want current_dir_sync_failed",
			result.WarningCode,
		)
	}
	if got := commitTestReadCurrent(t, root); !bytes.Equal(
		got, []byte(second.Generation.GenerationID+"\n"),
	) {
		t.Fatalf("post-rename current = %q", got)
	}
	stages := hook.snapshot()
	if !containsCommitTestString(
		stages, "after_current_rename_before_root_sync",
	) {
		t.Fatalf("post-rename hook was not reached: %#v", stages)
	}
	if containsCommitTestString(stages, "before_gc") {
		t.Fatalf("GC ran after post-rename hook failure: %#v", stages)
	}
}

func TestDiskCommitRootSyncFailureRemainsCommittedAndSkipsGC(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	store := newCommitTestStore(
		root, commitTestRandom(0xc1, 0xc2), filesystem, hook,
	)
	firstResult, firstErr, _ := commitTestCommit(
		t, store, commitTestCandidate("", commitTestObject("Old")),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	filesystem.reset()
	hook.reset()
	filesystem.failPostCurrentRootSync(root)

	result, err, gate := commitTestCommit(
		t,
		store,
		commitTestCandidate(
			first.Generation.GenerationID,
			commitTestObject("New"),
		),
	)
	second := commitTestRequireCommitted(t, result, err)
	if !gate.begun() {
		t.Fatal("root-sync failure did not retain begun gate")
	}
	if result.WarningCode != "current_dir_sync_failed" {
		t.Fatalf(
			"root-sync warning = %q, want current_dir_sync_failed",
			result.WarningCode,
		)
	}
	if got := commitTestReadCurrent(t, root); !bytes.Equal(
		got, []byte(second.Generation.GenerationID+"\n"),
	) {
		t.Fatalf("root-sync current = %q", got)
	}
	if stages := hook.snapshot(); containsCommitTestString(
		stages, "before_gc",
	) {
		t.Fatalf("GC ran after root-sync failure: %#v", stages)
	}
}

func containsCommitTestString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
