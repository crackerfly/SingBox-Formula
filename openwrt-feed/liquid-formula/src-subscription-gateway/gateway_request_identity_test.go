package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// Slice 2 RED-A specifies request-start/current identity before any fetching,
// locking, HTTP, or disk transaction implementation is added.
func TestRequestIdentityStateMachine(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		oldDigest    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
		generation2  = "2222222222222222222222222222222222222222222222222222222222222222"
	)

	url := "https://request-identity.invalid/subscription"
	urlDigest := testSHA256(url)
	freshBody := []byte("fresh-source")
	freshNormalized := []byte(`{"outbounds":[{"type":"socks","tag":"fresh"}]}`)
	committedAggregate := []byte(`{"outbounds":[{"type":"socks","tag":"committed"}]}`)
	reusedAggregate := []byte(`{"outbounds":[{"type":"socks","tag":"reused"}]}`)

	absent := currentSelection{Kind: currentAbsent}
	invalid := currentSelection{Kind: currentInvalid}
	generation := func(id, digest string, aggregate []byte) currentSelection {
		return currentSelection{
			Kind: currentPresent,
			Generation: validatedGeneration{
				GenerationID: id,
				ConfigDigest: digest,
				Aggregate:    append([]byte(nil), aggregate...),
			},
		}
	}

	cases := []struct {
		name             string
		start            currentObservation
		acquired         currentSelection
		wantCode         aggregateFailureCode
		wantPreserved    bool
		wantBytes        []byte
		wantLockCalls    int
		wantFetchCalls   int
		wantCommitCalls  int
		wantParent       string
		wantLoadCalls    int
		wantObserveCalls int
	}{
		{
			name:             "absent remains absent may fetch",
			start:            currentObservation{Kind: currentAbsent},
			acquired:         absent,
			wantBytes:        committedAggregate,
			wantLockCalls:    1,
			wantFetchCalls:   1,
			wantCommitCalls:  1,
			wantLoadCalls:    1,
			wantObserveCalls: 1,
		},
		{
			name:  "appeared same config after absent aborts",
			start: currentObservation{Kind: currentAbsent},
			acquired: generation(
				generation1, configDigest, reusedAggregate,
			),
			wantCode:         aggregateCodeStateInvalid,
			wantPreserved:    true,
			wantLockCalls:    1,
			wantLoadCalls:    1,
			wantObserveCalls: 1,
		},
		{
			name:  "appeared different config after absent aborts",
			start: currentObservation{Kind: currentAbsent},
			acquired: generation(
				generation1, oldDigest, reusedAggregate,
			),
			wantCode:         aggregateCodeStateInvalid,
			wantPreserved:    true,
			wantLockCalls:    1,
			wantLoadCalls:    1,
			wantObserveCalls: 1,
		},
		{
			name: "present generation disappeared",
			start: currentObservation{
				Kind: currentPresent, GenerationID: generation0,
			},
			acquired:         absent,
			wantCode:         aggregateCodeStateInvalid,
			wantLockCalls:    1,
			wantLoadCalls:    1,
			wantObserveCalls: 1,
		},
		{
			name: "present generation became invalid",
			start: currentObservation{
				Kind: currentPresent, GenerationID: generation0,
			},
			acquired:         invalid,
			wantCode:         aggregateCodeStateInvalid,
			wantLockCalls:    1,
			wantLoadCalls:    1,
			wantObserveCalls: 1,
		},
		{
			name: "changed same config reuses committed generation",
			start: currentObservation{
				Kind: currentPresent, GenerationID: generation0,
			},
			acquired: generation(
				generation1, configDigest, reusedAggregate,
			),
			wantBytes:        reusedAggregate,
			wantPreserved:    false,
			wantLockCalls:    1,
			wantLoadCalls:    1,
			wantObserveCalls: 1,
		},
		{
			name: "changed different config aborts",
			start: currentObservation{
				Kind: currentPresent, GenerationID: generation0,
			},
			acquired: generation(
				generation1, oldDigest, reusedAggregate,
			),
			wantCode:         aggregateCodeStateInvalid,
			wantPreserved:    true,
			wantLockCalls:    1,
			wantLoadCalls:    1,
			wantObserveCalls: 1,
		},
		{
			name: "unchanged old config may become parent",
			start: currentObservation{
				Kind: currentPresent, GenerationID: generation0,
			},
			acquired: generation(
				generation0, oldDigest, []byte(`{"outbounds":[]}`),
			),
			wantBytes:        committedAggregate,
			wantLockCalls:    1,
			wantFetchCalls:   1,
			wantCommitCalls:  1,
			wantParent:       generation0,
			wantLoadCalls:    1,
			wantObserveCalls: 1,
		},
		{
			name:             "malformed start token fails closed",
			start:            currentObservation{Kind: currentInvalid},
			acquired:         absent,
			wantCode:         aggregateCodeStateInvalid,
			wantObserveCalls: 1,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &scriptedGenerationStore{
				observation: testCase.start,
				selection:   testCase.acquired,
				commitResult: generationCommitResult{
					Committed: true,
					Selection: generation(
						generation2, configDigest, committedAggregate,
					),
				},
			}
			locker := &recordingSubscriptionLocker{}
			fetcher := &recordingSourceFetcher{
				results: map[string]sourceFetchResult{
					url: {Body: freshBody, Code: fetchCodeOK},
				},
			}
			normalizer := &recordingSourceNormalizer{
				output: freshNormalized,
				info: NormalizeInfo{
					Format: FormatSingBoxJSON, Accepted: 1,
				},
			}
			engine := newSubscriptionAggregateEngine(
				gatewayConfig{
					ConfigDigest:         configDigest,
					SourceTimeoutSeconds: 5,
					Sources: []gatewaySource{{
						URL: url, URLDigest: urlDigest,
					}},
				},
				subscriptionEngineDependencies{
					Locker:     locker,
					Store:      store,
					Fetcher:    fetcher,
					Normalizer: normalizer,
				},
			)

			outcome := engine.Aggregate(context.Background())
			if outcome.Code != testCase.wantCode ||
				outcome.Preserved != testCase.wantPreserved ||
				string(outcome.Bytes) != string(testCase.wantBytes) {
				t.Fatalf("outcome = %#v, want code=%q preserved=%v bytes=%q",
					outcome, testCase.wantCode, testCase.wantPreserved,
					testCase.wantBytes)
			}
			if locker.acquireCalls != testCase.wantLockCalls {
				t.Fatalf("lock calls = %d, want %d",
					locker.acquireCalls, testCase.wantLockCalls)
			}
			if locker.releaseCalls != testCase.wantLockCalls {
				t.Fatalf("release calls = %d, want %d",
					locker.releaseCalls, testCase.wantLockCalls)
			}
			if len(fetcher.calls) != testCase.wantFetchCalls {
				t.Fatalf("fetch calls = %v, want %d",
					fetcher.calls, testCase.wantFetchCalls)
			}
			if store.commitCalls != testCase.wantCommitCalls {
				t.Fatalf("commit calls = %d, want %d",
					store.commitCalls, testCase.wantCommitCalls)
			}
			if store.loadCalls != testCase.wantLoadCalls ||
				store.observeCalls != testCase.wantObserveCalls {
				t.Fatalf("store observe/load = %d/%d, want %d/%d",
					store.observeCalls, store.loadCalls,
					testCase.wantObserveCalls, testCase.wantLoadCalls)
			}
			if testCase.wantCommitCalls != 0 &&
				store.lastCandidate.ParentGenerationID != testCase.wantParent {
				t.Fatalf("parent = %q, want %q",
					store.lastCandidate.ParentGenerationID,
					testCase.wantParent)
			}
		})
	}
}

func TestRequestIdentityLaterSequentialRequestRefetches(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
		generation2  = "2222222222222222222222222222222222222222222222222222222222222222"
	)
	url := "https://later-request.invalid/list"
	urlDigest := testSHA256(url)
	store := &advancingGenerationStore{
		current: validatedGeneration{
			GenerationID: generation0,
			ConfigDigest: configDigest,
			Aggregate:    []byte(`{"generation":0}`),
		},
		commitIDs: []string{generation1, generation2},
	}
	fetcher := &recordingSourceFetcher{
		results: map[string]sourceFetchResult{
			url: {Body: []byte("body"), Code: fetchCodeOK},
		},
	}
	engine := newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:         configDigest,
			SourceTimeoutSeconds: 5,
			Sources: []gatewaySource{{
				URL: url, URLDigest: urlDigest,
			}},
		},
		subscriptionEngineDependencies{
			Locker:  &recordingSubscriptionLocker{},
			Store:   store,
			Fetcher: fetcher,
			Normalizer: &recordingSourceNormalizer{
				output: []byte(`{"outbounds":[{"type":"socks"}]}`),
				info: NormalizeInfo{
					Format: FormatSingBoxJSON, Accepted: 1,
				},
			},
		},
	)

	first := engine.Aggregate(context.Background())
	second := engine.Aggregate(context.Background())
	if first.Code != "" || second.Code != "" {
		t.Fatalf("sequential outcomes = %#v / %#v", first, second)
	}
	if len(fetcher.calls) != 2 || store.commitCalls != 2 {
		t.Fatalf("later request reused: fetches=%v commits=%d",
			fetcher.calls, store.commitCalls)
	}
}

func TestRequestIdentityObservePrecedesLockAndLoad(t *testing.T) {
	const configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	url := "https://call-order.invalid/list"
	var calls []string
	var mutex sync.Mutex
	record := func(value string) {
		mutex.Lock()
		defer mutex.Unlock()
		calls = append(calls, value)
	}
	store := &scriptedGenerationStore{
		observation: currentObservation{Kind: currentAbsent},
		selection:   currentSelection{Kind: currentAbsent},
		commitResult: generationCommitResult{
			Committed: true,
			Selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: "1111111111111111111111111111111111111111111111111111111111111111",
					ConfigDigest: configDigest,
					Aggregate:    []byte(`{"ok":true}`),
				},
			},
		},
		record: record,
	}
	locker := &recordingSubscriptionLocker{record: record}
	fetcher := &recordingSourceFetcher{
		results: map[string]sourceFetchResult{
			url: {Body: []byte("body"), Code: fetchCodeOK},
		},
		record: record,
	}
	engine := newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:         configDigest,
			SourceTimeoutSeconds: 5,
			Sources: []gatewaySource{{
				URL: url, URLDigest: testSHA256(url),
			}},
		},
		subscriptionEngineDependencies{
			Locker:  locker,
			Store:   store,
			Fetcher: fetcher,
			Normalizer: &recordingSourceNormalizer{
				output: []byte(`{"outbounds":[{"type":"socks"}]}`),
				info: NormalizeInfo{
					Format: FormatSingBoxJSON, Accepted: 1,
				},
				record: record,
			},
		},
	)
	if outcome := engine.Aggregate(context.Background()); outcome.Code != "" {
		t.Fatalf("outcome = %#v", outcome)
	}
	want := []string{
		"observe", "lock", "load", "fetch", "normalize", "commit", "unlock",
	}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("call order = %v, want %v", calls, want)
	}
}

func TestSubscriptionEngineRejectsSourceTimeoutOutsideFrozenRange(t *testing.T) {
	const configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	url := "https://timeout-range.invalid/list"
	for _, timeout := range []int64{1, 4, 601} {
		t.Run(fmt.Sprintf("%d", timeout), func(t *testing.T) {
			store := &scriptedGenerationStore{}
			locker := &recordingSubscriptionLocker{}
			outcome := newSubscriptionAggregateEngine(
				gatewayConfig{
					ConfigDigest:         configDigest,
					SourceTimeoutSeconds: timeout,
					Sources: []gatewaySource{{
						URL: url, URLDigest: testSHA256(url),
					}},
				},
				subscriptionEngineDependencies{
					Locker:     locker,
					Store:      store,
					Fetcher:    &recordingSourceFetcher{},
					Normalizer: &recordingSourceNormalizer{},
				},
			).Aggregate(context.Background())
			if outcome.Code != aggregateCodeStateInvalid ||
				store.observeCalls != 0 ||
				locker.acquireCalls != 0 {
				t.Fatalf("timeout %d outcome=%#v observe=%d lock=%d",
					timeout, outcome, store.observeCalls,
					locker.acquireCalls)
			}
		})
	}
}

func TestNoSourcesAndBusyReportValidatedPriorGeneration(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	for _, validated := range []bool{false, true} {
		name := fmt.Sprintf("validated_%v", validated)
		observation := currentObservation{
			Kind:         currentPresent,
			GenerationID: generation0,
			Validated:    validated,
		}
		t.Run("no_sources_"+name, func(t *testing.T) {
			store := &scriptedGenerationStore{observation: observation}
			outcome := newSubscriptionAggregateEngine(
				gatewayConfig{
					ConfigDigest:         configDigest,
					SourceTimeoutSeconds: 5,
				},
				subscriptionEngineDependencies{
					Locker:     &recordingSubscriptionLocker{},
					Store:      store,
					Fetcher:    &recordingSourceFetcher{},
					Normalizer: &recordingSourceNormalizer{},
				},
			).Aggregate(context.Background())
			if outcome.Code != aggregateCodeNoSources ||
				outcome.Preserved != validated ||
				store.observeCalls != 1 {
				t.Fatalf("no-sources outcome=%#v observe=%d",
					outcome, store.observeCalls)
			}
		})
		for _, lockCase := range []struct {
			name string
			err  error
			code aggregateFailureCode
		}{
			{name: "busy", err: errSubscriptionBusy, code: aggregateCodeBusy},
			{
				name: "invalid", err: errSubscriptionLockInvalid,
				code: aggregateCodeStateInvalid,
			},
			{
				name: "other", err: errors.New("lock backend unavailable"),
				code: aggregateCodeStateInvalid,
			},
		} {
			t.Run(lockCase.name+"_"+name, func(t *testing.T) {
				url := "https://lock-preserved.invalid/list"
				store := &scriptedGenerationStore{
					observation: observation,
				}
				outcome := newSubscriptionAggregateEngine(
					gatewayConfig{
						ConfigDigest:         configDigest,
						SourceTimeoutSeconds: 5,
						Sources: []gatewaySource{{
							URL: url, URLDigest: testSHA256(url),
						}},
					},
					subscriptionEngineDependencies{
						Locker: failingSubscriptionLocker{
							err: lockCase.err,
						},
						Store:      store,
						Fetcher:    &recordingSourceFetcher{},
						Normalizer: &recordingSourceNormalizer{},
					},
				).Aggregate(context.Background())
				if outcome.Code != lockCase.code ||
					outcome.Preserved != validated {
					t.Fatalf("%s outcome = %#v",
						lockCase.name, outcome)
				}
			})
		}
	}
}

func TestRequestIdentityRejectsDirtyAbsentSelection(t *testing.T) {
	const configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	url := "https://dirty-absent.invalid/list"
	store := &scriptedGenerationStore{
		observation: currentObservation{Kind: currentAbsent},
		selection: currentSelection{
			Kind: currentAbsent,
			Generation: validatedGeneration{
				Aggregate: []byte(`{"dirty":true}`),
			},
		},
	}
	fetcher := &recordingSourceFetcher{}
	outcome := newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:         configDigest,
			SourceTimeoutSeconds: 5,
			Sources: []gatewaySource{{
				URL: url, URLDigest: testSHA256(url),
			}},
		},
		subscriptionEngineDependencies{
			Locker:     &recordingSubscriptionLocker{},
			Store:      store,
			Fetcher:    fetcher,
			Normalizer: &recordingSourceNormalizer{},
		},
	).Aggregate(context.Background())
	if outcome.Code != aggregateCodeStateInvalid ||
		len(fetcher.calls) != 0 ||
		store.commitCalls != 0 {
		t.Fatalf("dirty absent outcome=%#v fetches=%v commits=%d",
			outcome, fetcher.calls, store.commitCalls)
	}
}

func TestUnvalidatedPresentObservationStillUsesLockedSelection(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	url := "https://unvalidated-observation.invalid/list"
	store := &scriptedGenerationStore{
		observation: currentObservation{
			Kind: currentPresent, GenerationID: generation0,
		},
		selection: currentSelection{
			Kind: currentPresent,
			Generation: validatedGeneration{
				GenerationID: generation1,
				ConfigDigest: configDigest,
				Aggregate:    []byte(`{"same-config":"reused"}`),
			},
		},
	}
	outcome := newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:         configDigest,
			SourceTimeoutSeconds: 5,
			Sources: []gatewaySource{{
				URL: url, URLDigest: testSHA256(url),
			}},
		},
		subscriptionEngineDependencies{
			Locker:     &recordingSubscriptionLocker{},
			Store:      store,
			Fetcher:    &recordingSourceFetcher{},
			Normalizer: &recordingSourceNormalizer{},
		},
	).Aggregate(context.Background())
	if outcome.Code != "" ||
		string(outcome.Bytes) != `{"same-config":"reused"}` {
		t.Fatalf("unvalidated observation outcome = %#v", outcome)
	}
}

func TestObservationValidatedFlagShape(t *testing.T) {
	for _, observation := range []currentObservation{
		{Kind: currentAbsent, Validated: true},
		{Kind: currentInvalid, Validated: true},
	} {
		if validCurrentObservation(observation) {
			t.Fatalf("invalid validated observation accepted: %#v",
				observation)
		}
	}
}

type scriptedGenerationStore struct {
	observation   currentObservation
	selection     currentSelection
	observeErr    error
	loadErr       error
	commitResult  generationCommitResult
	commitErr     error
	observeCalls  int
	loadCalls     int
	commitCalls   int
	lastCandidate generationCandidate
	record        func(string)
	skipBegin     bool
}

func (store *scriptedGenerationStore) ObserveCurrent(
	context.Context,
) (currentObservation, error) {
	store.observeCalls++
	if store.record != nil {
		store.record("observe")
	}
	return store.observation, store.observeErr
}

func (store *scriptedGenerationStore) LoadCurrent(
	context.Context,
) (currentSelection, error) {
	store.loadCalls++
	if store.record != nil {
		store.record("load")
	}
	return store.selection, store.loadErr
}

func (store *scriptedGenerationStore) Commit(
	ctx context.Context,
	candidate generationCandidate,
) (generationCommitResult, error) {
	store.commitCalls++
	store.lastCandidate = candidate
	if store.record != nil {
		store.record("commit")
	}
	if store.commitResult.Committed &&
		!store.skipBegin &&
		!beginCurrentCommit(ctx) {
		return generationCommitResult{}, nil
	}
	return store.commitResult, store.commitErr
}

type advancingGenerationStore struct {
	current     validatedGeneration
	commitIDs   []string
	commitCalls int
}

func (store *advancingGenerationStore) ObserveCurrent(
	context.Context,
) (currentObservation, error) {
	return currentObservation{
		Kind: currentPresent, GenerationID: store.current.GenerationID,
	}, nil
}

func (store *advancingGenerationStore) LoadCurrent(
	context.Context,
) (currentSelection, error) {
	return currentSelection{
		Kind: currentPresent, Generation: store.current,
	}, nil
}

func (store *advancingGenerationStore) Commit(
	ctx context.Context,
	candidate generationCandidate,
) (generationCommitResult, error) {
	if !beginCurrentCommit(ctx) {
		return generationCommitResult{}, nil
	}
	id := store.commitIDs[store.commitCalls]
	store.commitCalls++
	store.current = validatedGeneration{
		GenerationID: id,
		ConfigDigest: candidate.ConfigDigest,
		Aggregate:    []byte(fmt.Sprintf(`{"generation":%d}`, store.commitCalls)),
	}
	return generationCommitResult{
		Committed: true,
		Selection: currentSelection{
			Kind: currentPresent, Generation: store.current,
		},
	}, nil
}

type recordingSubscriptionLocker struct {
	acquireCalls int
	releaseCalls int
	record       func(string)
}

type failingSubscriptionLocker struct {
	err error
}

func (locker failingSubscriptionLocker) Acquire(
	context.Context,
) (heldSubscriptionLock, error) {
	return nil, locker.err
}

func (locker *recordingSubscriptionLocker) Acquire(
	context.Context,
) (heldSubscriptionLock, error) {
	locker.acquireCalls++
	if locker.record != nil {
		locker.record("lock")
	}
	return &recordingHeldSubscriptionLock{owner: locker}, nil
}

type recordingHeldSubscriptionLock struct {
	owner *recordingSubscriptionLocker
}

func (lock *recordingHeldSubscriptionLock) Release() error {
	lock.owner.releaseCalls++
	if lock.owner.record != nil {
		lock.owner.record("unlock")
	}
	return nil
}

type recordingSourceFetcher struct {
	results map[string]sourceFetchResult
	calls   []string
	record  func(string)
}

func (fetcher *recordingSourceFetcher) Fetch(
	_ context.Context,
	url string,
	_ string,
) sourceFetchResult {
	fetcher.calls = append(fetcher.calls, url)
	if fetcher.record != nil {
		fetcher.record("fetch")
	}
	return fetcher.results[url]
}

type recordingSourceNormalizer struct {
	output []byte
	info   NormalizeInfo
	err    error
	calls  int
	record func(string)
}

func (normalizer *recordingSourceNormalizer) Normalize(
	[]byte,
) ([]byte, NormalizeInfo, error) {
	normalizer.calls++
	if normalizer.record != nil {
		normalizer.record("normalize")
	}
	return append([]byte(nil), normalizer.output...), normalizer.info, normalizer.err
}

func testSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}
