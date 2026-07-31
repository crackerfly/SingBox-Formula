package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskGCSameContentStillPublishesDistinctGenerations(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	store := newCommitTestStore(
		root,
		commitTestRandom(0x41, 0x42, 0x43, 0x44),
		newCommitTestFilesystem(),
		&commitTestFaultHook{},
	)
	object := commitTestObject("Stable")
	parent := ""
	var generations []string
	var firstAggregate []byte
	for _, wantID := range []string{
		commitTestID(0x41),
		commitTestID(0x42),
		commitTestID(0x43),
		commitTestID(0x44),
	} {
		result, err, _ := commitTestCommit(
			t, store, commitTestCandidate(parent, object),
		)
		selection := commitTestRequireCommitted(t, result, err)
		if selection.Generation.GenerationID != wantID {
			t.Fatalf(
				"same-content generation = %q, want %q",
				selection.Generation.GenerationID,
				wantID,
			)
		}
		if firstAggregate == nil {
			firstAggregate = append(
				[]byte(nil), selection.Generation.Aggregate...,
			)
		} else if !bytes.Equal(
			selection.Generation.Aggregate, firstAggregate,
		) {
			t.Fatalf(
				"same-content aggregate changed: %q",
				selection.Generation.Aggregate,
			)
		}
		parent = selection.Generation.GenerationID
		generations = append(generations, parent)
	}

	commitTestRequireEntryNames(
		t,
		filepath.Join(root, "generations"),
		generations[1:],
	)
	commitTestRequireEntryNames(
		t,
		filepath.Join(root, "objects"),
		[]string{commitTestObjectDigest(object) + ".json"},
	)
}

func TestDiskStoreNeverUsesPhysicalAncestorForPolicyBFallback(
	t *testing.T,
) {
	const (
		urlX = "https://x.example/subscription"
		urlY = "https://y.example/subscription"
	)
	configX := strings.Repeat("a", 64)
	configY := strings.Repeat("b", 64)
	root := filepath.Join(t.TempDir(), "subscriptions")
	store := newCommitTestStore(
		root,
		commitTestRandom(0x51, 0x52, 0x53),
		newCommitTestFilesystem(),
		&commitTestFaultHook{},
	)

	first := newPolicyTestEngine(
		configX,
		[]string{urlX},
		store,
		map[string]sourceFetchResult{
			urlX: {Body: []byte("X fresh"), Code: fetchCodeOK},
		},
	).Aggregate(context.Background())
	if first.Code != "" || len(first.Bytes) == 0 {
		t.Fatalf("X transaction = %#v", first)
	}
	firstSelection, err := store.LoadCurrent(context.Background())
	if err != nil || firstSelection.Kind != currentPresent {
		t.Fatalf("load X generation = %#v err=%v", firstSelection, err)
	}
	if firstSelection.Generation.GenerationID != commitTestID(0x51) ||
		len(firstSelection.Generation.Sources) != 1 {
		t.Fatalf("X generation = %#v", firstSelection.Generation)
	}
	xObjectDigest := firstSelection.Generation.Sources[0].ObjectDigest

	second := newPolicyTestEngine(
		configY,
		[]string{urlY},
		store,
		map[string]sourceFetchResult{
			urlY: {Body: []byte("Y fresh"), Code: fetchCodeOK},
		},
	).Aggregate(context.Background())
	if second.Code != "" || len(second.Bytes) == 0 {
		t.Fatalf("Y transaction = %#v", second)
	}
	if got := commitTestReadCurrent(t, root); !bytes.Equal(
		got, []byte(commitTestID(0x52)+"\n"),
	) {
		t.Fatalf("Y current = %q", got)
	}
	xGenerationPath := filepath.Join(
		root, "generations", commitTestID(0x51),
	)
	xObjectPath := filepath.Join(
		root, "objects", xObjectDigest+".json",
	)
	for _, path := range []string{xGenerationPath, xObjectPath} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("physical X ancestor missing before retry: %s: %v", path, err)
		}
	}

	third := newPolicyTestEngine(
		configX,
		[]string{urlX},
		store,
		map[string]sourceFetchResult{
			urlX: {Code: fetchCodeTimeout},
		},
	).Aggregate(context.Background())
	if third.Code != aggregateCodeSourceUnavailable ||
		third.SourceIndex != 1 ||
		!third.Preserved ||
		len(third.Bytes) != 0 {
		t.Fatalf("X retry used an ancestor fallback: %#v", third)
	}
	if got := commitTestReadCurrent(t, root); !bytes.Equal(
		got, []byte(commitTestID(0x52)+"\n"),
	) {
		t.Fatalf("failed X retry changed current = %q", got)
	}
	commitTestRequireEntryNames(
		t,
		filepath.Join(root, "generations"),
		[]string{commitTestID(0x51), commitTestID(0x52)},
	)
	for _, path := range []string{xGenerationPath, xObjectPath} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("failed X retry changed physical ancestor: %s: %v", path, err)
		}
	}
}
