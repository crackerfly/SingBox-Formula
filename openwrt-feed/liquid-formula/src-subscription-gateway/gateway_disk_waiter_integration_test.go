package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDiskStoreSameConfigWaiterReusesJustCommittedGeneration(
	t *testing.T,
) {
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
		commitTestCandidate("", commitTestObject("Initial")),
	)
	seed := commitTestRequireCommitted(t, seedResult, seedErr)
	if seed.Generation.GenerationID != commitTestID(0x61) {
		t.Fatalf("seed generation = %q", seed.Generation.GenerationID)
	}

	filesystem.reset()
	hook.reset()
	currentReady := make(chan struct{})
	currentGo := make(chan struct{})
	hook.block(
		"before_current_rename", currentReady, currentGo,
	)
	locker := newFileSubscriptionLocker(
		filepath.Join(t.TempDir(), "subscription.lock"),
		time.Millisecond,
	)
	const sourceURL = "https://waiter.example/subscription"
	config := gatewayConfig{
		ConfigDigest:         strings.Repeat("c", 64),
		SourceTimeoutSeconds: 5,
		Sources: []gatewaySource{{
			URL: sourceURL, URLDigest: testSHA256(sourceURL),
		}},
	}
	firstFetcher := &recordingSourceFetcher{
		results: map[string]sourceFetchResult{
			sourceURL: {Body: []byte("first"), Code: fetchCodeOK},
		},
	}
	firstEngine := newSubscriptionAggregateEngine(
		config,
		subscriptionEngineDependencies{
			Locker:     locker,
			Store:      store,
			Fetcher:    firstFetcher,
			Normalizer: &bodySourceNormalizer{},
		},
	)
	firstChannel := make(chan aggregateOutcome, 1)
	go func() {
		firstChannel <- firstEngine.Aggregate(context.Background())
	}()
	commitTestAwait(t, currentReady, "first before-current hook")

	waiterEntered := make(chan struct{})
	waiterFetcher := &recordingSourceFetcher{
		results: map[string]sourceFetchResult{
			sourceURL: {Body: []byte("waiter"), Code: fetchCodeOK},
		},
	}
	waiterEngine := newSubscriptionAggregateEngine(
		config,
		subscriptionEngineDependencies{
			Locker: &commitTestNotifyingLocker{
				inner:   locker,
				entered: waiterEntered,
			},
			Store:      store,
			Fetcher:    waiterFetcher,
			Normalizer: &bodySourceNormalizer{},
		},
	)
	waiterChannel := make(chan aggregateOutcome, 1)
	go func() {
		waiterChannel <- waiterEngine.Aggregate(context.Background())
	}()
	commitTestAwait(t, waiterEntered, "waiter lock acquisition")
	close(currentGo)

	firstOutcome := commitTestAwait(
		t, firstChannel, "first aggregate",
	)
	waiterOutcome := commitTestAwait(
		t, waiterChannel, "waiter aggregate",
	)
	if firstOutcome.Code != "" || len(firstOutcome.Bytes) == 0 {
		t.Fatalf("first aggregate = %#v", firstOutcome)
	}
	if waiterOutcome.Code != "" ||
		!bytes.Equal(waiterOutcome.Bytes, firstOutcome.Bytes) {
		t.Fatalf(
			"waiter=%#v first=%#v", waiterOutcome, firstOutcome,
		)
	}
	if !equalCommitTestStrings(firstFetcher.calls, []string{sourceURL}) {
		t.Fatalf("first fetches = %#v", firstFetcher.calls)
	}
	if len(waiterFetcher.calls) != 0 {
		t.Fatalf("same-config waiter refetched = %#v", waiterFetcher.calls)
	}
	if got := commitTestReadCurrent(t, root); !bytes.Equal(
		got, []byte(commitTestID(0x62)+"\n"),
	) {
		t.Fatalf("waiter current = %q", got)
	}
	commitTestRequireEntryNames(
		t,
		filepath.Join(root, "generations"),
		[]string{commitTestID(0x61), commitTestID(0x62)},
	)
}

type commitTestNotifyingLocker struct {
	inner   subscriptionLocker
	entered chan struct{}
	once    sync.Once
}

func (locker *commitTestNotifyingLocker) Acquire(
	ctx context.Context,
) (heldSubscriptionLock, error) {
	locker.once.Do(func() {
		close(locker.entered)
	})
	return locker.inner.Acquire(ctx)
}
