package main

import (
	"context"
	"fmt"
	"testing"
)

func TestSubscriptionPolicyBStates(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	urlA := "https://policy.invalid/A"
	urlB := "https://policy.invalid/B"
	normalizedA := []byte(`{"outbounds":[{"type":"socks","tag":"A"}]}`)
	normalizedB := []byte(`{"outbounds":[{"type":"socks","tag":"B"}]}`)
	oldGeneration := validatedGeneration{
		GenerationID: generation0,
		ConfigDigest: configDigest,
		Aggregate:    []byte(`{"old":true}`),
		Sources: []generationSource{
			testGenerationSource(1, urlA, normalizedA),
			testGenerationSource(2, urlB, normalizedB),
		},
	}

	cases := []struct {
		name          string
		selection     currentSelection
		results       map[string]sourceFetchResult
		wantState     string
		wantResults   []string
		wantCode      aggregateFailureCode
		wantIndex     int
		wantPreserved bool
		wantCommits   int
	}{
		{
			name:      "all fresh",
			selection: currentSelection{Kind: currentAbsent},
			results: map[string]sourceFetchResult{
				urlA: {Body: []byte("A"), Code: fetchCodeOK},
				urlB: {Body: []byte("B"), Code: fetchCodeOK},
			},
			wantState:   generationStateFresh,
			wantResults: []string{sourceResultFresh, sourceResultFresh},
			wantCommits: 1,
		},
		{
			name: "mixed fresh and fallback",
			selection: currentSelection{
				Kind: currentPresent, Generation: oldGeneration,
			},
			results: map[string]sourceFetchResult{
				urlA: {Body: []byte("A"), Code: fetchCodeOK},
				urlB: {Code: fetchCodeTimeout},
			},
			wantState:   generationStateDegraded,
			wantResults: []string{sourceResultFresh, sourceResultFallback},
			wantCommits: 1,
		},
		{
			name: "all fallback still commits degraded",
			selection: currentSelection{
				Kind: currentPresent, Generation: oldGeneration,
			},
			results: map[string]sourceFetchResult{
				urlA: {Code: fetchCodeTransport},
				urlB: {Code: fetchCodeNormalize},
			},
			wantState: generationStateDegraded,
			wantResults: []string{
				sourceResultFallback, sourceResultFallback,
			},
			wantCommits: 1,
		},
		{
			name: "one failed source without cache aborts all fresh work",
			selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: generation0,
					ConfigDigest: configDigest,
					Aggregate:    []byte(`{"old":true}`),
					Sources: []generationSource{
						testGenerationSource(1, urlA, normalizedA),
					},
				},
			},
			results: map[string]sourceFetchResult{
				urlA: {Body: []byte("A"), Code: fetchCodeOK},
				urlB: {Code: fetchCodeHTTPStatus},
			},
			wantCode:      aggregateCodeSourceUnavailable,
			wantIndex:     2,
			wantPreserved: true,
		},
		{
			name:      "absent current has no fallback",
			selection: currentSelection{Kind: currentAbsent},
			results: map[string]sourceFetchResult{
				urlA: {Body: []byte("A"), Code: fetchCodeOK},
				urlB: {Code: fetchCodeBodyTooLarge},
			},
			wantCode:  aggregateCodeSourceUnavailable,
			wantIndex: 2,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			observation := currentObservation{Kind: currentAbsent}
			if testCase.selection.Kind == currentPresent {
				observation = currentObservation{
					Kind:         currentPresent,
					GenerationID: testCase.selection.Generation.GenerationID,
				}
			}
			store := &scriptedGenerationStore{
				observation: observation,
				selection:   testCase.selection,
				commitResult: generationCommitResult{
					Committed: true,
					Selection: currentSelection{
						Kind: currentPresent,
						Generation: validatedGeneration{
							GenerationID: generation1,
							ConfigDigest: configDigest,
							Aggregate:    []byte(`{"new":true}`),
						},
					},
				},
			}
			engine := newPolicyTestEngine(
				configDigest,
				[]string{urlA, urlB},
				store,
				testCase.results,
			)
			outcome := engine.Aggregate(context.Background())
			if outcome.Code != testCase.wantCode ||
				outcome.SourceIndex != testCase.wantIndex ||
				outcome.Preserved != testCase.wantPreserved {
				t.Fatalf("outcome = %#v, want code=%q index=%d preserved=%v",
					outcome, testCase.wantCode, testCase.wantIndex,
					testCase.wantPreserved)
			}
			if store.commitCalls != testCase.wantCommits {
				t.Fatalf("commit calls = %d, want %d",
					store.commitCalls, testCase.wantCommits)
			}
			if testCase.wantCommits == 0 {
				if len(outcome.Bytes) != 0 {
					t.Fatalf("failure returned old bytes: %q", outcome.Bytes)
				}
				return
			}
			if store.lastCandidate.State != testCase.wantState {
				t.Fatalf("candidate state = %q, want %q",
					store.lastCandidate.State, testCase.wantState)
			}
			var gotResults []string
			for _, source := range store.lastCandidate.Sources {
				if candidateObject(
					store.lastCandidate, source,
				) == nil {
					t.Fatalf("source has invalid object reference: %#v",
						source)
				}
				gotResults = append(gotResults, source.Result)
			}
			if fmt.Sprint(gotResults) != fmt.Sprint(testCase.wantResults) {
				t.Fatalf("source results = %v, want %v",
					gotResults, testCase.wantResults)
			}
		})
	}
}

func TestSubscriptionPolicyDuplicateOccurrencesUseOneBoundResult(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	urlA := "https://duplicates.invalid/A"
	urlB := "https://duplicates.invalid/B"
	normalizedA := []byte(`{"outbounds":[{"type":"socks","tag":"cached-A"}]}`)
	store := &scriptedGenerationStore{
		observation: currentObservation{
			Kind: currentPresent, GenerationID: generation0,
		},
		selection: currentSelection{
			Kind: currentPresent,
			Generation: validatedGeneration{
				GenerationID: generation0,
				ConfigDigest: configDigest,
				Aggregate:    []byte(`{"old":true}`),
				Sources: []generationSource{
					testGenerationSource(1, urlA, normalizedA),
				},
			},
		},
		commitResult: generationCommitResult{
			Committed: true,
			Selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: "1111111111111111111111111111111111111111111111111111111111111111",
					ConfigDigest: configDigest,
					Aggregate:    []byte(`{"new":true}`),
				},
			},
		},
	}
	fetcher := &recordingSourceFetcher{results: map[string]sourceFetchResult{
		urlA: {Code: fetchCodeTimeout},
		urlB: {Code: fetchCodeTransport},
	}}
	engine := newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:         configDigest,
			SourceTimeoutSeconds: 5,
			Sources: []gatewaySource{
				{URL: urlA, URLDigest: testSHA256(urlA)},
				{URL: urlA, URLDigest: testSHA256(urlA)},
				{URL: urlB, URLDigest: testSHA256(urlB)},
			},
		},
		subscriptionEngineDependencies{
			Locker:     &recordingSubscriptionLocker{},
			Store:      store,
			Fetcher:    fetcher,
			Normalizer: &bodySourceNormalizer{},
		},
	)
	outcome := engine.Aggregate(context.Background())
	if outcome.Code != aggregateCodeSourceUnavailable ||
		outcome.SourceIndex != 3 ||
		!outcome.Preserved {
		t.Fatalf("duplicate/no-cache outcome = %#v", outcome)
	}
	if fmt.Sprint(fetcher.calls) != fmt.Sprintf("[%s %s]", urlA, urlB) {
		t.Fatalf("duplicate fetch calls = %v", fetcher.calls)
	}
	if store.commitCalls != 0 {
		t.Fatal("partial duplicate fallback transaction committed")
	}

	fetcher.results[urlB] = sourceFetchResult{
		Body: []byte("fresh-B"), Code: fetchCodeOK,
	}
	outcome = engine.Aggregate(context.Background())
	if outcome.Code != "" || store.commitCalls != 1 {
		t.Fatalf("duplicate fallback success = %#v commits=%d",
			outcome, store.commitCalls)
	}
	if len(store.lastCandidate.Sources) != 3 {
		t.Fatalf("duplicate candidate occurrences = %d",
			len(store.lastCandidate.Sources))
	}
	if len(store.lastCandidate.Objects) != 2 {
		t.Fatalf("duplicate candidate objects = %d, want 2",
			len(store.lastCandidate.Objects))
	}
	for index := 0; index < 2; index++ {
		source := store.lastCandidate.Sources[index]
		if source.Index != index+1 ||
			source.ObjectIndex != 1 ||
			source.Result != sourceResultFallback ||
			source.URLDigest != testSHA256(urlA) {
			t.Fatalf("duplicate occurrence %d = %#v", index+1, source)
		}
	}
	if store.lastCandidate.Sources[2].ObjectIndex != 2 {
		t.Fatalf("fresh occurrence object index = %d, want 2",
			store.lastCandidate.Sources[2].ObjectIndex)
	}
}

func TestSubscriptionCandidateDeduplicatesEightExactObjects(t *testing.T) {
	const configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	urlA := "https://object-dedupe.invalid/A"
	urlB := "https://object-dedupe.invalid/B"
	normalized := []byte(`{"outbounds":[{"type":"socks","tag":"same"}]}`)
	urls := []string{urlA, urlB, urlA, urlB, urlA, urlB, urlA, urlB}
	sources := make([]gatewaySource, 0, len(urls))
	for _, url := range urls {
		sources = append(sources, gatewaySource{
			URL: url, URLDigest: testSHA256(url),
		})
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
					Aggregate:    []byte(`{"deduplicated":true}`),
				},
			},
		},
	}
	fetcher := &recordingSourceFetcher{
		results: map[string]sourceFetchResult{
			urlA: {Body: []byte("A"), Code: fetchCodeOK},
			urlB: {Body: []byte("B"), Code: fetchCodeOK},
		},
	}
	outcome := newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:         configDigest,
			SourceTimeoutSeconds: 5,
			Sources:              sources,
		},
		subscriptionEngineDependencies{
			Locker:  &recordingSubscriptionLocker{},
			Store:   store,
			Fetcher: fetcher,
			Normalizer: fixedSourceNormalizer{
				normalized: normalized,
			},
		},
	).Aggregate(context.Background())
	if outcome.Code != "" || store.commitCalls != 1 {
		t.Fatalf("dedupe outcome=%#v commits=%d",
			outcome, store.commitCalls)
	}
	if fmt.Sprint(fetcher.calls) != fmt.Sprintf("[%s %s]", urlA, urlB) {
		t.Fatalf("dedupe fetch calls = %v", fetcher.calls)
	}
	if len(store.lastCandidate.Sources) != 8 ||
		len(store.lastCandidate.Objects) != 1 ||
		string(store.lastCandidate.Objects[0]) != string(normalized) {
		t.Fatalf("dedupe candidate sources=%d objects=%q",
			len(store.lastCandidate.Sources),
			store.lastCandidate.Objects)
	}
	for index, source := range store.lastCandidate.Sources {
		if source.Index != index+1 || source.ObjectIndex != 1 {
			t.Fatalf("dedupe occurrence %d = %#v", index+1, source)
		}
	}
}

func TestSubscriptionCandidateTransfersNormalizedObjectBacking(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	validCommit := generationCommitResult{
		Committed: true,
		Selection: currentSelection{
			Kind: currentPresent,
			Generation: validatedGeneration{
				GenerationID: generation1,
				ConfigDigest: configDigest,
				Aggregate:    []byte(`{"ownership":true}`),
			},
		},
	}

	t.Run("fresh normalizer output", func(t *testing.T) {
		url := "https://object-ownership.invalid/fresh"
		normalizer := &ownershipSourceNormalizer{
			normalized: []byte(
				`{"outbounds":[{"type":"socks","tag":"fresh-owned"}]}`,
			),
		}
		store := &scriptedGenerationStore{
			observation:  currentObservation{Kind: currentAbsent},
			selection:    currentSelection{Kind: currentAbsent},
			commitResult: validCommit,
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
				Locker: &recordingSubscriptionLocker{},
				Store:  store,
				Fetcher: &recordingSourceFetcher{
					results: map[string]sourceFetchResult{
						url: {Body: []byte("fresh"), Code: fetchCodeOK},
					},
				},
				Normalizer: normalizer,
			},
		).Aggregate(context.Background())
		if outcome.Code != "" ||
			len(store.lastCandidate.Objects) != 1 {
			t.Fatalf("fresh ownership outcome=%#v objects=%d",
				outcome, len(store.lastCandidate.Objects))
		}
		object := store.lastCandidate.Objects[0]
		if &object[0] != &normalizer.normalized[0] {
			t.Fatal("fresh normalized object backing was cloned")
		}
	})

	t.Run("validated fallback object", func(t *testing.T) {
		url := "https://object-ownership.invalid/fallback"
		store := &scriptedGenerationStore{
			observation: currentObservation{
				Kind: currentPresent, GenerationID: generation0,
			},
			selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: generation0,
					ConfigDigest: configDigest,
					Aggregate:    []byte(`{"old":true}`),
					Sources: []generationSource{
						testGenerationSource(
							1, url,
							[]byte(`{"outbounds":[{"tag":"fallback-owned"}]}`),
						),
					},
				},
			},
			commitResult: validCommit,
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
				Locker: &recordingSubscriptionLocker{},
				Store:  store,
				Fetcher: &recordingSourceFetcher{
					results: map[string]sourceFetchResult{
						url: {Code: fetchCodeTimeout},
					},
				},
				Normalizer: &bodySourceNormalizer{},
			},
		).Aggregate(context.Background())
		if outcome.Code != "" ||
			len(store.lastCandidate.Objects) != 1 {
			t.Fatalf("fallback ownership outcome=%#v objects=%d",
				outcome, len(store.lastCandidate.Objects))
		}
		object := store.lastCandidate.Objects[0]
		cached := store.selection.Generation.Sources[0].Normalized
		if &object[0] != &cached[0] {
			t.Fatal("validated fallback object backing was cloned")
		}
	})
}

func TestSubscriptionPolicyRejectsConflictingObjectsForSameURLDigest(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	url := "https://conflicting-cache.invalid/A"
	first := testGenerationSource(
		1, url, []byte(`{"outbounds":[{"tag":"first"}]}`),
	)
	second := testGenerationSource(
		2, url, []byte(`{"outbounds":[{"tag":"second"}]}`),
	)
	store := &scriptedGenerationStore{
		observation: currentObservation{
			Kind: currentPresent, GenerationID: generation0,
		},
		selection: currentSelection{
			Kind: currentPresent,
			Generation: validatedGeneration{
				GenerationID: generation0,
				ConfigDigest: configDigest,
				Aggregate:    []byte(`{"old":true}`),
				Sources:      []generationSource{first, second},
			},
		},
	}
	fetcher := &recordingSourceFetcher{results: map[string]sourceFetchResult{
		url: {Body: []byte("fresh"), Code: fetchCodeOK},
	}}
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
			Normalizer: &bodySourceNormalizer{},
		},
	).Aggregate(context.Background())
	if outcome.Code != aggregateCodeStateInvalid ||
		outcome.Preserved ||
		len(fetcher.calls) != 0 ||
		store.commitCalls != 0 {
		t.Fatalf("conflicting cache outcome=%#v fetches=%v commits=%d",
			outcome, fetcher.calls, store.commitCalls)
	}
}

func TestSubscriptionPolicyRejectsConflictingInfoForSameURLObject(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	url := "https://conflicting-info.invalid/A"
	normalized := []byte(`{"outbounds":[{"tag":"same"}]}`)
	first := testGenerationSource(1, url, normalized)
	second := testGenerationSource(2, url, normalized)
	second.Info.Accepted = 2
	store := &scriptedGenerationStore{
		observation: currentObservation{
			Kind: currentPresent, GenerationID: generation0,
		},
		selection: currentSelection{
			Kind: currentPresent,
			Generation: validatedGeneration{
				GenerationID: generation0,
				ConfigDigest: configDigest,
				Aggregate:    []byte(`{"old":true}`),
				Sources:      []generationSource{first, second},
			},
		},
	}
	fetcher := &recordingSourceFetcher{results: map[string]sourceFetchResult{
		url: {Body: []byte("fresh"), Code: fetchCodeOK},
	}}
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
			Normalizer: &bodySourceNormalizer{},
		},
	).Aggregate(context.Background())
	if outcome.Code != aggregateCodeStateInvalid ||
		outcome.Preserved ||
		len(fetcher.calls) != 0 ||
		store.commitCalls != 0 {
		t.Fatalf("conflicting info outcome=%#v fetches=%v commits=%d",
			outcome, fetcher.calls, store.commitCalls)
	}
}

func TestSubscriptionPolicyCacheIdentityAcrossReorderAndReplacement(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	urlA := "https://identity.invalid/list?token=%7E"
	urlB := "https://identity.invalid/list?token=~"
	urlC := "https://identity.invalid/list/?token=~"
	normalizedA := []byte(`{"outbounds":[{"tag":"cache-A"}]}`)
	normalizedB := []byte(`{"outbounds":[{"tag":"cache-B"}]}`)
	baseGeneration := validatedGeneration{
		GenerationID: generation0,
		ConfigDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Aggregate:    []byte(`{"old":true}`),
		Sources: []generationSource{
			testGenerationSource(1, urlA, normalizedA),
			testGenerationSource(2, urlB, normalizedB),
		},
	}

	t.Run("reorder finds exact URL digest without cross wiring", func(t *testing.T) {
		store := &scriptedGenerationStore{
			observation: currentObservation{
				Kind: currentPresent, GenerationID: generation0,
			},
			selection: currentSelection{
				Kind: currentPresent, Generation: baseGeneration,
			},
			commitResult: generationCommitResult{
				Committed: true,
				Selection: currentSelection{
					Kind: currentPresent,
					Generation: validatedGeneration{
						GenerationID: generation1,
						ConfigDigest: configDigest,
						Aggregate:    []byte(`{"reordered":true}`),
					},
				},
			},
		}
		outcome := newPolicyTestEngine(
			configDigest,
			[]string{urlB, urlA},
			store,
			map[string]sourceFetchResult{
				urlA: {Code: fetchCodeTransport},
				urlB: {Code: fetchCodeTimeout},
			},
		).Aggregate(context.Background())
		if outcome.Code != "" || store.commitCalls != 1 {
			t.Fatalf("reorder outcome=%#v commits=%d",
				outcome, store.commitCalls)
		}
		sources := store.lastCandidate.Sources
		if len(sources) != 2 ||
			sources[0].URLDigest != testSHA256(urlB) ||
			string(candidateObject(
				store.lastCandidate, sources[0],
			)) != string(normalizedB) ||
			sources[1].URLDigest != testSHA256(urlA) ||
			string(candidateObject(
				store.lastCandidate, sources[1],
			)) != string(normalizedA) {
			t.Fatalf("reordered fallback sources = %#v", sources)
		}
	})

	t.Run("similar replacement cannot borrow another URL cache", func(t *testing.T) {
		store := &scriptedGenerationStore{
			observation: currentObservation{
				Kind: currentPresent, GenerationID: generation0,
			},
			selection: currentSelection{
				Kind: currentPresent, Generation: baseGeneration,
			},
		}
		outcome := newPolicyTestEngine(
			configDigest,
			[]string{urlA, urlC},
			store,
			map[string]sourceFetchResult{
				urlA: {Code: fetchCodeTimeout},
				urlC: {Code: fetchCodeTransport},
			},
		).Aggregate(context.Background())
		if outcome.Code != aggregateCodeSourceUnavailable ||
			outcome.SourceIndex != 2 ||
			!outcome.Preserved ||
			store.commitCalls != 0 {
			t.Fatalf("replacement outcome=%#v commits=%d",
				outcome, store.commitCalls)
		}
	})
}

func TestSubscriptionPolicyDeleteThenReaddCannotUseHistoricalObject(t *testing.T) {
	const (
		configA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		configAB    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		generation0 = "0000000000000000000000000000000000000000000000000000000000000000"
		generation1 = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	urlA := "https://delete-readd.invalid/A"
	urlB := "https://delete-readd.invalid/B"
	store := &policyAdvancingStore{
		current: validatedGeneration{
			GenerationID: generation0,
			ConfigDigest: configAB,
			Aggregate:    []byte(`{"old":true}`),
			Sources: []generationSource{
				testGenerationSource(
					1, urlA, []byte(`{"outbounds":[{"tag":"old-A"}]}`),
				),
				testGenerationSource(
					2, urlB, []byte(`{"outbounds":[{"tag":"old-B"}]}`),
				),
			},
		},
		historical: map[string]validatedGeneration{},
		commitIDs:  []string{generation1},
	}

	deleteB := newPolicyTestEngine(
		configA,
		[]string{urlA},
		store,
		map[string]sourceFetchResult{
			urlA: {Body: []byte("fresh-A"), Code: fetchCodeOK},
		},
	).Aggregate(context.Background())
	if deleteB.Code != "" || store.commitCalls != 1 ||
		len(store.current.Sources) != 1 {
		t.Fatalf("delete transaction=%#v commits=%d sources=%d",
			deleteB, store.commitCalls, len(store.current.Sources))
	}
	if _, retained := store.historical[generation0]; !retained {
		t.Fatal("test fixture did not retain historical generation")
	}

	readdB := newPolicyTestEngine(
		configAB,
		[]string{urlA, urlB},
		store,
		map[string]sourceFetchResult{
			urlA: {Code: fetchCodeTimeout},
			urlB: {Code: fetchCodeTransport},
		},
	).Aggregate(context.Background())
	if readdB.Code != aggregateCodeSourceUnavailable ||
		readdB.SourceIndex != 2 ||
		!readdB.Preserved ||
		store.commitCalls != 1 {
		t.Fatalf("readd transaction=%#v commits=%d",
			readdB, store.commitCalls)
	}
}

func newPolicyTestEngine(
	configDigest string,
	urls []string,
	store generationStore,
	results map[string]sourceFetchResult,
) aggregateEngine {
	sources := make([]gatewaySource, 0, len(urls))
	for _, url := range urls {
		sources = append(sources, gatewaySource{
			URL: url, URLDigest: testSHA256(url),
		})
	}
	return newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:         configDigest,
			SourceTimeoutSeconds: 5,
			Sources:              sources,
		},
		subscriptionEngineDependencies{
			Locker:     &recordingSubscriptionLocker{},
			Store:      store,
			Fetcher:    &recordingSourceFetcher{results: results},
			Normalizer: &bodySourceNormalizer{},
		},
	)
}

func testGenerationSource(
	index int,
	url string,
	normalized []byte,
) generationSource {
	digest := testSHA256(string(normalized))
	return generationSource{
		Index:        index,
		URLDigest:    testSHA256(url),
		ObjectDigest: digest,
		Normalized:   append([]byte(nil), normalized...),
		Info: NormalizeInfo{
			Format: FormatSingBoxJSON, Accepted: 1,
		},
	}
}

type bodySourceNormalizer struct{}

func (*bodySourceNormalizer) Normalize(
	body []byte,
) ([]byte, NormalizeInfo, error) {
	return []byte(fmt.Sprintf(
		`{"outbounds":[{"tag":%q,"type":"socks"}]}`,
		string(body),
	)), NormalizeInfo{
			Format: FormatSingBoxJSON, Accepted: 1,
		}, nil
}

type fixedSourceNormalizer struct {
	normalized []byte
}

func (normalizer fixedSourceNormalizer) Normalize(
	[]byte,
) ([]byte, NormalizeInfo, error) {
	return append([]byte(nil), normalizer.normalized...), NormalizeInfo{
		Format: FormatSingBoxJSON, Accepted: 1,
	}, nil
}

type ownershipSourceNormalizer struct {
	normalized []byte
}

func (normalizer *ownershipSourceNormalizer) Normalize(
	[]byte,
) ([]byte, NormalizeInfo, error) {
	return normalizer.normalized, NormalizeInfo{
		Format: FormatSingBoxJSON, Accepted: 1,
	}, nil
}

func candidateObject(
	candidate generationCandidate,
	source generationCandidateSource,
) []byte {
	if source.ObjectIndex < 1 ||
		source.ObjectIndex > len(candidate.Objects) {
		return nil
	}
	return candidate.Objects[source.ObjectIndex-1]
}

type policyAdvancingStore struct {
	current     validatedGeneration
	historical  map[string]validatedGeneration
	commitIDs   []string
	commitCalls int
}

func (store *policyAdvancingStore) ObserveCurrent(
	context.Context,
) (currentObservation, error) {
	return currentObservation{
		Kind: currentPresent, GenerationID: store.current.GenerationID,
	}, nil
}

func (store *policyAdvancingStore) LoadCurrent(
	context.Context,
) (currentSelection, error) {
	return currentSelection{
		Kind: currentPresent, Generation: store.current,
	}, nil
}

func (store *policyAdvancingStore) Commit(
	ctx context.Context,
	candidate generationCandidate,
) (generationCommitResult, error) {
	if !beginCurrentCommit(ctx) {
		return generationCommitResult{}, nil
	}
	store.historical[store.current.GenerationID] = store.current
	id := store.commitIDs[store.commitCalls]
	store.commitCalls++
	sources := make([]generationSource, 0, len(candidate.Sources))
	for _, source := range candidate.Sources {
		normalized := candidateObject(candidate, source)
		if normalized == nil {
			return generationCommitResult{}, fmt.Errorf(
				"invalid candidate object index %d",
				source.ObjectIndex,
			)
		}
		sources = append(sources, generationSource{
			Index:        source.Index,
			URLDigest:    source.URLDigest,
			ObjectDigest: testSHA256(string(normalized)),
			Normalized:   append([]byte(nil), normalized...),
			Info:         source.Info,
		})
	}
	store.current = validatedGeneration{
		GenerationID: id,
		ConfigDigest: candidate.ConfigDigest,
		Aggregate: []byte(fmt.Sprintf(
			`{"generation":%d}`, store.commitCalls,
		)),
		Sources: sources,
	}
	return generationCommitResult{
		Committed: true,
		Selection: currentSelection{
			Kind: currentPresent, Generation: store.current,
		},
	}, nil
}
