package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDiskCommitRetriesRandomGenerationCollisionWithoutOverwrite(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	commitTestPrepareRoot(t, root)
	collisionID := commitTestID(0x11)
	collisionDirectory := filepath.Join(
		root, "generations", collisionID,
	)
	if err := os.Mkdir(collisionDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(collisionDirectory, "sentinel")
	sentinel := []byte("DO_NOT_OVERWRITE_GENERATION")
	if err := os.WriteFile(sentinelPath, sentinel, 0600); err != nil {
		t.Fatal(err)
	}

	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	hook.fail("before_gc")
	store := newCommitTestStore(
		root, commitTestRandom(0x11, 0x22), filesystem, hook,
	)
	result, err, gate := commitTestCommit(
		t, store, commitTestCandidate("", commitTestObject("Collision")),
	)
	selection := commitTestRequireCommitted(t, result, err)
	if !gate.begun() {
		t.Fatal("commit gate did not begin")
	}
	if selection.Generation.GenerationID != commitTestID(0x22) {
		t.Fatalf(
			"generation = %q, want collision retry %q",
			selection.Generation.GenerationID, commitTestID(0x22),
		)
	}
	if got, readErr := os.ReadFile(sentinelPath); readErr != nil ||
		!bytes.Equal(got, sentinel) {
		t.Fatalf(
			"collision target changed: contents=%q err=%v",
			got, readErr,
		)
	}
}

func TestDiskCommitReusesExactObjectWithoutOverwrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	store := newCommitTestStore(
		root, commitTestRandom(0x31, 0x32), filesystem, hook,
	)
	object := commitTestObject("Shared")

	firstResult, firstErr, _ := commitTestCommit(
		t, store, commitTestCandidate("", object),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	objectPath := filepath.Join(
		root, "objects", commitTestObjectDigest(object)+".json",
	)
	firstInfo, err := os.Stat(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	firstStat, ok := firstInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("object stat type = %T", firstInfo.Sys())
	}

	secondResult, secondErr, _ := commitTestCommit(
		t,
		store,
		commitTestCandidate(first.Generation.GenerationID, object),
	)
	commitTestRequireCommitted(t, secondResult, secondErr)
	secondInfo, err := os.Stat(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	secondStat, ok := secondInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("object stat type = %T", secondInfo.Sys())
	}
	if firstStat.Dev != secondStat.Dev ||
		firstStat.Ino != secondStat.Ino {
		t.Fatalf(
			"existing object was replaced: first=(%d,%d) second=(%d,%d)",
			firstStat.Dev, firstStat.Ino,
			secondStat.Dev, secondStat.Ino,
		)
	}
	entries, err := os.ReadDir(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 ||
		entries[0].Name() != commitTestObjectDigest(object)+".json" {
		t.Fatalf("object entries = %#v", entries)
	}
}

func TestDiskCommitRejectsExistingSameDigestPathWithInvalidBytes(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	store := newCommitTestStore(
		root, commitTestRandom(0x41, 0x42), filesystem, hook,
	)
	firstResult, firstErr, _ := commitTestCommit(
		t, store, commitTestCandidate("", commitTestObject("Old")),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	oldCurrent := commitTestReadCurrent(t, root)

	newObject := commitTestObject("New")
	objectPath := filepath.Join(
		root, "objects", commitTestObjectDigest(newObject)+".json",
	)
	invalid := []byte(`{"outbounds":[{"tag":"WRONG","type":"block"}]}`)
	if err := os.WriteFile(objectPath, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	result, _, gate := commitTestCommit(
		t,
		store,
		commitTestCandidate(first.Generation.GenerationID, newObject),
	)
	if result.Committed || gate.begun() {
		t.Fatalf(
			"invalid existing object committed: result=%#v begun=%t",
			result, gate.begun(),
		)
	}
	commitTestRequireOldCurrent(
		t, root, store, oldCurrent, first.Generation.GenerationID,
	)
	if got, err := os.ReadFile(objectPath); err != nil ||
		!bytes.Equal(got, invalid) {
		t.Fatalf("invalid collision target changed: got=%q err=%v", got, err)
	}
}

func TestDiskCommitSameContentAlwaysPublishesNewGeneration(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	store := newCommitTestStore(
		root, commitTestRandom(0x51, 0x52), filesystem, hook,
	)
	candidate := commitTestCandidate("", commitTestObject("Stable"))
	firstResult, firstErr, _ := commitTestCommit(t, store, candidate)
	first := commitTestRequireCommitted(t, firstResult, firstErr)

	candidate.ParentGenerationID = first.Generation.GenerationID
	secondResult, secondErr, _ := commitTestCommit(t, store, candidate)
	second := commitTestRequireCommitted(t, secondResult, secondErr)
	if second.Generation.GenerationID ==
		first.Generation.GenerationID {
		t.Fatalf(
			"same content reused generation %q",
			second.Generation.GenerationID,
		)
	}
	if !bytes.Equal(
		second.Generation.Aggregate, first.Generation.Aggregate,
	) {
		t.Fatalf(
			"same input aggregates differ: first=%q second=%q",
			first.Generation.Aggregate, second.Generation.Aggregate,
		)
	}
	wantCurrent := second.Generation.GenerationID + "\n"
	if got := string(commitTestReadCurrent(t, root)); got != wantCurrent {
		t.Fatalf("current = %q, want %q", got, wantCurrent)
	}

	manifestPath := filepath.Join(
		root, "generations", second.Generation.GenerationID,
		"manifest.json",
	)
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	wantParent := `"parent":"` +
		first.Generation.GenerationID + `"`
	if !strings.Contains(string(manifest), wantParent) {
		t.Fatalf("manifest lacks exact parent %s: %s", wantParent, manifest)
	}

	selection, err := store.LoadCurrent(context.Background())
	if err != nil ||
		selection.Kind != currentPresent ||
		selection.Generation.GenerationID !=
			second.Generation.GenerationID {
		t.Fatalf("selected latest generation = %#v err=%v", selection, err)
	}
}
