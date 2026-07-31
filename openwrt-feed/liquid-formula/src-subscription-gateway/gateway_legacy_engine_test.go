package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyProviderIsNeverConsultedForInvalidCurrent(t *testing.T) {
	const configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	url := "https://legacy-current.invalid/list"

	for _, testCase := range []struct {
		name        string
		observation currentObservation
		selection   currentSelection
	}{
		{
			name:        "invalid request-start observation",
			observation: currentObservation{Kind: currentInvalid},
			selection:   currentSelection{Kind: currentAbsent},
		},
		{
			name: "invalid lock-acquisition current",
			observation: currentObservation{
				Kind:         currentPresent,
				GenerationID: strings.Repeat("0", 64),
				Validated:    true,
			},
			selection: currentSelection{Kind: currentInvalid},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			provider := &legacyTestProvider{}
			store := &scriptedGenerationStore{
				observation: testCase.observation,
				selection:   testCase.selection,
			}
			outcome := legacyTestEngine(
				configDigest,
				[]string{url},
				store,
				&recordingSourceFetcher{results: map[string]sourceFetchResult{
					url: {Code: fetchCodeTransport},
				}},
				&bodySourceNormalizer{},
				provider,
			).Aggregate(context.Background())
			if outcome.Code != aggregateCodeStateInvalid ||
				provider.probeCalls != 0 ||
				provider.loadCalls != 0 ||
				store.commitCalls != 0 {
				t.Fatalf(
					"invalid-current outcome=%#v probe=%d load=%d commit=%d",
					outcome,
					provider.probeCalls,
					provider.loadCalls,
					store.commitCalls,
				)
			}
		})
	}
}

func TestLegacyMarkerConsumptionIsRecordedWithoutReadingNode(
	t *testing.T,
) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	url := "https://legacy-consume.invalid/list"
	urlDigest := testSHA256(url)
	cached := []byte(
		`{"outbounds":[{"type":"direct","tag":"Current Cache"}]}`,
	)

	for _, testCase := range []struct {
		name       string
		selection  currentSelection
		fetch      sourceFetchResult
		wantResult string
		wantObject []byte
	}{
		{
			name:      "fresh success consumes marker",
			selection: currentSelection{Kind: currentAbsent},
			fetch: sourceFetchResult{
				Body: []byte("fresh-node"), Code: fetchCodeOK,
			},
			wantResult: sourceResultFresh,
		},
		{
			name: "current fallback consumes marker and wins over legacy node",
			selection: currentSelection{
				Kind: currentPresent,
				Generation: legacyTestGeneration(
					"",
					[]generationSource{
						testGenerationSource(1, url, cached),
					},
				),
			},
			fetch:      sourceFetchResult{Code: fetchCodeTimeout},
			wantResult: sourceResultFallback,
			wantObject: cached,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			observation := currentObservation{Kind: currentAbsent}
			if testCase.selection.Kind == currentPresent {
				observation = currentObservation{
					Kind: currentPresent,
					GenerationID: testCase.selection.
						Generation.GenerationID,
					Validated: true,
				}
			}
			store := &scriptedGenerationStore{
				observation: observation,
				selection:   testCase.selection,
				commitResult: legacyTestCommittedResult(
					generation1, configDigest,
				),
			}
			provider := &legacyTestProvider{
				probeResult: legacyProbeResult{
					MatchingURLDigest: urlDigest,
					Eligible:          true,
				},
				loadErr: errors.New(
					"LEGACY_NODE_MUST_NOT_BE_READ_SECRET_CANARY",
				),
			}
			outcome := legacyTestEngine(
				configDigest,
				[]string{url},
				store,
				&recordingSourceFetcher{
					results: map[string]sourceFetchResult{
						url: testCase.fetch,
					},
				},
				&bodySourceNormalizer{},
				provider,
			).Aggregate(context.Background())
			if outcome.Code != "" || store.commitCalls != 1 {
				t.Fatalf(
					"successful consume outcome=%#v commits=%d",
					outcome, store.commitCalls,
				)
			}
			if provider.probeCalls != 1 || provider.loadCalls != 0 {
				t.Fatalf(
					"legacy calls probe=%d load=%d",
					provider.probeCalls, provider.loadCalls,
				)
			}
			candidate := store.lastCandidate
			if candidate.LegacyConsumedURLDigest != urlDigest ||
				candidate.legacyMarkerReceipt == nil {
				t.Fatalf("candidate legacy metadata = %#v", candidate)
			}
			if len(candidate.Sources) != 1 ||
				candidate.Sources[0].Result != testCase.wantResult {
				t.Fatalf("candidate sources = %#v", candidate.Sources)
			}
			if testCase.wantObject != nil &&
				string(candidateObject(
					candidate, candidate.Sources[0],
				)) != string(testCase.wantObject) {
				t.Fatalf(
					"current cache did not win: object=%q want=%q",
					candidateObject(candidate, candidate.Sources[0]),
					testCase.wantObject,
				)
			}
		})
	}
}

func TestLegacyNodeIsNormalizedOnlyWhenOccurrenceOneNeedsIt(
	t *testing.T,
) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	url := "https://legacy-needed.invalid/list"
	urlDigest := testSHA256(url)
	rawLegacy := []byte(
		`{"outbounds":[{"type":"direct","tag":"Raw Legacy"}]}`,
	)
	normalizedLegacy := []byte(
		`{"outbounds":[{"tag":"Raw Legacy","type":"direct"}]}`,
	)

	t.Run("needed native node becomes degraded fallback", func(t *testing.T) {
		store := &scriptedGenerationStore{
			observation: currentObservation{Kind: currentAbsent},
			selection:   currentSelection{Kind: currentAbsent},
			commitResult: legacyTestCommittedResult(
				generation1, configDigest,
			),
		}
		provider := &legacyTestProvider{
			probeResult: legacyProbeResult{
				MatchingURLDigest: urlDigest,
				Eligible:          true,
			},
			loadBody: rawLegacy,
		}
		normalizer := &legacyTestNormalizer{
			output: normalizedLegacy,
			info: NormalizeInfo{
				Format: FormatSingBoxJSON, Accepted: 1,
			},
		}
		outcome := legacyTestEngine(
			configDigest,
			[]string{url},
			store,
			&recordingSourceFetcher{results: map[string]sourceFetchResult{
				url: {Code: fetchCodeTimeout},
			}},
			normalizer,
			provider,
		).Aggregate(context.Background())
		if outcome.Code != "" ||
			provider.loadCalls != 1 ||
			normalizer.calls != 1 ||
			string(normalizer.inputs[0]) != string(rawLegacy) ||
			store.commitCalls != 1 {
			t.Fatalf(
				"legacy fallback outcome=%#v load=%d normalize=%d commits=%d inputs=%q",
				outcome,
				provider.loadCalls,
				normalizer.calls,
				store.commitCalls,
				normalizer.inputs,
			)
		}
		candidate := store.lastCandidate
		if candidate.State != generationStateDegraded ||
			len(candidate.Sources) != 1 ||
			candidate.Sources[0].Result != sourceResultFallback ||
			candidate.Sources[0].FetchCode != fetchCodeTimeout ||
			candidate.Sources[0].Info.Format != FormatSingBoxJSON ||
			string(candidateObject(
				candidate, candidate.Sources[0],
			)) != string(normalizedLegacy) ||
			candidate.LegacyConsumedURLDigest != urlDigest {
			t.Fatalf("legacy candidate = %#v", candidate)
		}
	})

	t.Run("duplicate URL cannot extend legacy beyond occurrence one", func(t *testing.T) {
		store := &scriptedGenerationStore{
			observation: currentObservation{Kind: currentAbsent},
			selection:   currentSelection{Kind: currentAbsent},
		}
		provider := &legacyTestProvider{
			probeResult: legacyProbeResult{
				MatchingURLDigest: urlDigest,
				Eligible:          true,
			},
			loadBody: rawLegacy,
		}
		normalizer := &legacyTestNormalizer{
			output: normalizedLegacy,
			info: NormalizeInfo{
				Format: FormatSingBoxJSON, Accepted: 1,
			},
		}
		fetcher := &recordingSourceFetcher{
			results: map[string]sourceFetchResult{
				url: {Code: fetchCodeTransport},
			},
		}
		outcome := legacyTestEngine(
			configDigest,
			[]string{url, url},
			store,
			fetcher,
			normalizer,
			provider,
		).Aggregate(context.Background())
		if outcome.Code != aggregateCodeSourceUnavailable ||
			outcome.SourceIndex != 2 ||
			len(fetcher.calls) != 1 ||
			provider.loadCalls != 1 ||
			normalizer.calls != 1 ||
			store.commitCalls != 0 {
			t.Fatalf(
				"occurrence-bound outcome=%#v fetch=%v load=%d normalize=%d commit=%d",
				outcome,
				fetcher.calls,
				provider.loadCalls,
				normalizer.calls,
				store.commitCalls,
			)
		}
	})
}

func TestLegacyFailureClassificationAndNativeOnlyFormat(t *testing.T) {
	const configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	url := "https://legacy-failure.invalid/list"
	urlDigest := testSHA256(url)

	t.Run("probe structural error is state invalid", func(t *testing.T) {
		provider := &legacyTestProvider{
			probeErr: errors.New("PROBE_IDENTITY_SECRET_CANARY"),
		}
		fetcher := &recordingSourceFetcher{
			results: map[string]sourceFetchResult{
				url: {Code: fetchCodeTransport},
			},
		}
		store := &scriptedGenerationStore{
			observation: currentObservation{Kind: currentAbsent},
			selection:   currentSelection{Kind: currentAbsent},
		}
		outcome := legacyTestEngine(
			configDigest,
			[]string{url},
			store,
			fetcher,
			&bodySourceNormalizer{},
			provider,
		).Aggregate(context.Background())
		if outcome.Code != aggregateCodeStateInvalid ||
			outcome.SourceIndex != 0 ||
			len(fetcher.calls) != 0 ||
			provider.loadCalls != 0 ||
			store.commitCalls != 0 {
			t.Fatalf(
				"probe error outcome=%#v fetch=%v load=%d commit=%d",
				outcome, fetcher.calls, provider.loadCalls, store.commitCalls,
			)
		}
	})

	for _, testCase := range []struct {
		name      string
		loadErr   error
		normalize *legacyTestNormalizer
		wantCalls int
	}{
		{
			name:      "missing legacy node",
			loadErr:   errors.New("LEGACY_NODE_MISSING_SECRET_CANARY"),
			normalize: &legacyTestNormalizer{},
		},
		{
			name: "legacy normalize failure",
			normalize: &legacyTestNormalizer{
				err: errors.New("LEGACY_NORMALIZE_SECRET_CANARY"),
			},
			wantCalls: 1,
		},
		{
			name: "plain URI result is rejected",
			normalize: &legacyTestNormalizer{
				output: []byte(`{"outbounds":[{"type":"direct"}]}`),
				info: NormalizeInfo{
					Format: FormatPlainURI, Accepted: 1,
				},
			},
			wantCalls: 1,
		},
		{
			name: "base64 URI result is rejected",
			normalize: &legacyTestNormalizer{
				output: []byte(`{"outbounds":[{"type":"direct"}]}`),
				info: NormalizeInfo{
					Format: FormatBase64URI, Accepted: 1,
				},
			},
			wantCalls: 1,
		},
		{
			name: "Clash YAML result is rejected",
			normalize: &legacyTestNormalizer{
				output: []byte(`{"outbounds":[{"type":"direct"}]}`),
				info: NormalizeInfo{
					Format: FormatClashYAML, Accepted: 1,
				},
			},
			wantCalls: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			provider := &legacyTestProvider{
				probeResult: legacyProbeResult{
					MatchingURLDigest: urlDigest,
					Eligible:          true,
				},
				loadBody: []byte(
					`{"outbounds":[{"type":"direct"}]}`,
				),
				loadErr: testCase.loadErr,
			}
			store := &scriptedGenerationStore{
				observation: currentObservation{Kind: currentAbsent},
				selection:   currentSelection{Kind: currentAbsent},
			}
			outcome := legacyTestEngine(
				configDigest,
				[]string{url},
				store,
				&recordingSourceFetcher{
					results: map[string]sourceFetchResult{
						url: {Code: fetchCodeTransport},
					},
				},
				testCase.normalize,
				provider,
			).Aggregate(context.Background())
			if outcome.Code != aggregateCodeSourceUnavailable ||
				outcome.SourceIndex != 1 ||
				provider.loadCalls != 1 ||
				testCase.normalize.calls != testCase.wantCalls ||
				store.commitCalls != 0 {
				t.Fatalf(
					"legacy failure outcome=%#v load=%d normalize=%d commit=%d",
					outcome,
					provider.loadCalls,
					testCase.normalize.calls,
					store.commitCalls,
				)
			}
		})
	}
}

func TestLegacyConsumedDigestPropagatesMonotonicallyInEngine(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
		oldConsumed  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	url := "https://legacy-monotonic.invalid/list"
	urlDigest := testSHA256(url)
	parent := legacyTestGeneration(oldConsumed, nil)

	t.Run("unchanged parent value propagates without marker", func(t *testing.T) {
		store := &scriptedGenerationStore{
			observation: currentObservation{
				Kind:         currentPresent,
				GenerationID: parent.GenerationID,
				Validated:    true,
			},
			selection: currentSelection{
				Kind: currentPresent, Generation: parent,
			},
			commitResult: legacyTestCommittedResult(
				generation1, configDigest,
			),
		}
		provider := &legacyTestProvider{}
		outcome := legacyTestEngine(
			configDigest,
			[]string{url},
			store,
			&recordingSourceFetcher{results: map[string]sourceFetchResult{
				url: {Body: []byte("fresh"), Code: fetchCodeOK},
			}},
			&bodySourceNormalizer{},
			provider,
		).Aggregate(context.Background())
		if outcome.Code != "" ||
			store.lastCandidate.LegacyConsumedURLDigest != oldConsumed ||
			store.lastCandidate.legacyMarkerReceipt != nil {
			t.Fatalf(
				"monotonic propagation outcome=%#v candidate=%#v",
				outcome, store.lastCandidate,
			)
		}
	})

	t.Run("different matching marker conflicts before fetch", func(t *testing.T) {
		store := &scriptedGenerationStore{
			observation: currentObservation{
				Kind:         currentPresent,
				GenerationID: parent.GenerationID,
				Validated:    true,
			},
			selection: currentSelection{
				Kind: currentPresent, Generation: parent,
			},
		}
		fetcher := &recordingSourceFetcher{
			results: map[string]sourceFetchResult{
				url: {Body: []byte("fresh"), Code: fetchCodeOK},
			},
		}
		provider := &legacyTestProvider{
			probeResult: legacyProbeResult{
				MatchingURLDigest: urlDigest,
				Eligible:          true,
			},
		}
		outcome := legacyTestEngine(
			configDigest,
			[]string{url},
			store,
			fetcher,
			&bodySourceNormalizer{},
			provider,
		).Aggregate(context.Background())
		if outcome.Code != aggregateCodeStateInvalid ||
			!outcome.Preserved ||
			len(fetcher.calls) != 0 ||
			provider.loadCalls != 0 ||
			store.commitCalls != 0 {
			t.Fatalf(
				"conflicting consumption outcome=%#v fetch=%v load=%d commit=%d",
				outcome,
				fetcher.calls,
				provider.loadCalls,
				store.commitCalls,
			)
		}
	})
}

func TestLegacyErrorsNeverEscapeAggregateHTTP(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		errorCanary  = "LEGACY_RAW_ERROR_SECRET_CANARY"
		pathCanary   = "LEGACY_PATH_SECRET_CANARY"
		urlCanary    = "LEGACY_URL_TOKEN_SECRET_CANARY"
	)
	url := "https://user:password@legacy-secret.invalid/" +
		pathCanary + "?token=" + urlCanary
	urlDigest := testSHA256(url)

	for _, testCase := range []struct {
		name       string
		provider   *legacyTestProvider
		wantStatus int
		wantCode   string
	}{
		{
			name: "probe error",
			provider: &legacyTestProvider{
				probeErr: errors.New(errorCanary + " /tmp/" + pathCanary),
			},
			wantStatus: http.StatusInternalServerError,
			wantCode:   `"code":"state_invalid"`,
		},
		{
			name: "needed node load error",
			provider: &legacyTestProvider{
				probeResult: legacyProbeResult{
					MatchingURLDigest: urlDigest,
					Eligible:          true,
				},
				loadErr: errors.New(errorCanary + " /tmp/" + pathCanary),
			},
			wantStatus: http.StatusBadGateway,
			wantCode:   `"code":"source_unavailable"`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := &scriptedGenerationStore{
				observation: currentObservation{Kind: currentAbsent},
				selection:   currentSelection{Kind: currentAbsent},
			}
			config := gatewayConfig{
				ConfigDigest:            configDigest,
				SourceTimeoutSeconds:    5,
				AggregateTimeoutSeconds: 2,
				Sources: []gatewaySource{{
					URL: url, URLDigest: urlDigest,
				}},
			}
			engine := newSubscriptionAggregateEngine(
				config,
				subscriptionEngineDependencies{
					Locker: &recordingSubscriptionLocker{},
					Store:  store,
					Fetcher: &recordingSourceFetcher{
						results: map[string]sourceFetchResult{
							url: {Code: fetchCodeTransport},
						},
					},
					Normalizer: &bodySourceNormalizer{},
					Legacy:     testCase.provider,
				},
			)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodGet, "http://127.0.0.1/v1/aggregate", nil,
			)
			gatewayHTTPHandler{
				config: config, engine: engine,
			}.ServeHTTP(recorder, request)
			body := recorder.Body.String()
			if recorder.Code != testCase.wantStatus ||
				!strings.Contains(body, testCase.wantCode) {
				t.Fatalf(
					"response status=%d body=%s",
					recorder.Code, body,
				)
			}
			for _, canary := range []string{
				errorCanary, pathCanary, urlCanary,
				"user:password", url, urlDigest,
			} {
				if strings.Contains(body, canary) {
					t.Fatalf("response leaked %q: %s", canary, body)
				}
			}
		})
	}
}

type legacyTestProvider struct {
	probeResult legacyProbeResult
	probeErr    error
	loadBody    []byte
	loadErr     error
	removeErr   error

	probeCalls   int
	loadCalls    int
	removeCalls  int
	probeInputs  []legacyProbeRequest
	removeCtx    context.Context
	removeToken  legacyReadToken
	removeDigest string
	record       func(string)
}

func (provider *legacyTestProvider) Probe(
	_ context.Context,
	request legacyProbeRequest,
) (legacyProbeResult, error) {
	provider.probeCalls++
	provider.probeInputs = append(provider.probeInputs, request)
	if provider.record != nil {
		provider.record("probe")
	}
	return provider.probeResult, provider.probeErr
}

func (provider *legacyTestProvider) Load(
	_ context.Context,
	_ legacyReadToken,
) ([]byte, error) {
	provider.loadCalls++
	if provider.record != nil {
		provider.record("load")
	}
	return append([]byte(nil), provider.loadBody...), provider.loadErr
}

func (provider *legacyTestProvider) RemoveCommittedMarker(
	ctx context.Context,
	token legacyReadToken,
	digest string,
) error {
	provider.removeCalls++
	provider.removeCtx = ctx
	provider.removeToken = token
	provider.removeDigest = digest
	if provider.record != nil {
		provider.record("remove-marker")
	}
	return provider.removeErr
}

type legacyTestNormalizer struct {
	output []byte
	info   NormalizeInfo
	err    error
	calls  int
	inputs [][]byte
}

func (normalizer *legacyTestNormalizer) Normalize(
	input []byte,
) ([]byte, NormalizeInfo, error) {
	normalizer.calls++
	normalizer.inputs = append(
		normalizer.inputs, append([]byte(nil), input...),
	)
	return append([]byte(nil), normalizer.output...),
		cloneNormalizeInfo(normalizer.info),
		normalizer.err
}

func legacyTestEngine(
	configDigest string,
	urls []string,
	store generationStore,
	fetcher sourceFetcher,
	normalizer sourceNormalizer,
	provider legacySourceProvider,
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
			Fetcher:    fetcher,
			Normalizer: normalizer,
			Legacy:     provider,
		},
	)
}

func legacyTestGeneration(
	consumed string,
	sources []generationSource,
) validatedGeneration {
	return validatedGeneration{
		GenerationID:            strings.Repeat("0", 64),
		ConfigDigest:            strings.Repeat("a", 64),
		Aggregate:               []byte(`{"outbounds":[{"type":"direct"}]}`),
		Sources:                 sources,
		LegacyConsumedURLDigest: consumed,
	}
}

func legacyTestCommittedResult(
	generationID string,
	configDigest string,
) generationCommitResult {
	return generationCommitResult{
		Committed: true,
		Selection: currentSelection{
			Kind: currentPresent,
			Generation: validatedGeneration{
				GenerationID: generationID,
				ConfigDigest: configDigest,
				Aggregate: []byte(fmt.Sprintf(
					`{"generation":%q}`, generationID,
				)),
			},
		},
	}
}
