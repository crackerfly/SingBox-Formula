package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func diskReviewSHA256(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func TestDiskLoadRecomputesOrderedAggregateFromStoredSources(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &diskReviewMutationHook{}
	store := diskReviewStore(
		root, []byte{0x61}, filesystem, hook,
	)
	result, err, _ := commitTestCommit(
		t, store, commitTestTwoObjectCandidate(),
	)
	selection := commitTestRequireCommitted(t, result, err)
	generationDirectory := filepath.Join(
		root,
		"generations",
		selection.Generation.GenerationID,
	)
	aggregatePath := filepath.Join(
		generationDirectory, "aggregate.json",
	)
	manifestPath := filepath.Join(
		generationDirectory, "manifest.json",
	)

	reorderedAggregate := []byte(
		`{"outbounds":[{"tag":"Beta","type":"direct"},{"tag":"Alpha","type":"direct"}]}`,
	)
	manifestRaw, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var manifest diskManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Aggregate.SHA256 = diskReviewSHA256(
		reorderedAggregate,
	)
	manifest.Aggregate.Bytes = len(reorderedAggregate)
	manifest.Aggregate.Outbounds = 2
	manifestRaw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		aggregatePath, reorderedAggregate, 0600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		manifestPath, manifestRaw, 0600,
	); err != nil {
		t.Fatal(err)
	}

	loaded, loadErr := store.LoadCurrent(context.Background())
	if loadErr == nil || loaded.Kind == currentPresent {
		t.Fatalf(
			"source-order-inconsistent aggregate loaded: selection=%#v err=%v",
			loaded, loadErr,
		)
	}
	if !errors.Is(loadErr, errDiskStateInvalid) ||
		loadErr.Error() != "subscription state invalid" {
		t.Fatalf("aggregate mismatch error = %v", loadErr)
	}
	if strings.Contains(
		loadErr.Error(), "Alpha",
	) || strings.Contains(loadErr.Error(), "Beta") {
		t.Fatalf("aggregate mismatch leaked a tag: %v", loadErr)
	}
}
