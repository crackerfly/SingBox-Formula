package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSubscriptionCancellationAfterBlockingNormalizerNeverCommits(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		urlCanary    = "URL_SECRET_CANARY"
		bodyCanary   = "BODY_PASSWORD_SECRET_CANARY"
	)
	url := "https://user:pass@cancel.invalid/list?token=" + urlCanary
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
					Aggregate:    []byte(`{"must-not":"commit"}`),
				},
			},
		},
	}
	locker := newCancellationTestLocker()
	normalizer := &blockingSourceNormalizer{
		started: make(chan struct{}),
		release: make(chan struct{}),
		output: []byte(
			`{"outbounds":[{"password":"` + bodyCanary + `"}]}`,
		),
	}
	engine := newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:            configDigest,
			SourceTimeoutSeconds:    5,
			AggregateTimeoutSeconds: 1,
			Sources: []gatewaySource{{
				URL: url, URLDigest: testSHA256(url),
			}},
		},
		subscriptionEngineDependencies{
			Locker: locker,
			Store:  store,
			Fetcher: &recordingSourceFetcher{
				results: map[string]sourceFetchResult{
					url: {
						Body: []byte(bodyCanary), Code: fetchCodeOK,
					},
				},
			},
			Normalizer: normalizer,
		},
	)
	server := httptest.NewServer(gatewayHTTPHandler{
		config: gatewayConfig{
			ConfigDigest:            configDigest,
			AggregateTimeoutSeconds: 1,
			Sources: []gatewaySource{{
				URL: url, URLDigest: testSHA256(url),
			}},
		},
		engine: engine,
	})
	defer server.Close()

	responseDone := make(chan []byte, 1)
	go func() {
		response, err := http.Get(server.URL + "/v1/aggregate")
		if err != nil {
			responseDone <- []byte("request error: " + err.Error())
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		responseDone <- body
	}()
	<-normalizer.started
	body := <-responseDone
	if store.commitCalls != 0 {
		t.Fatalf("commit occurred before normalizer returned: %d",
			store.commitCalls)
	}
	for _, secret := range []string{urlCanary, bodyCanary, "user:pass"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("deadline body leaked %q: %s", secret, body)
		}
	}

	close(normalizer.release)
	select {
	case <-locker.released:
	case <-time.After(2 * time.Second):
		t.Fatal("background engine did not release lock")
	}
	if store.commitCalls != 0 {
		t.Fatalf("cancelled background engine committed: %d",
			store.commitCalls)
	}
}

func TestSubscriptionFailureResponsesContainNoSourceSecrets(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	urlSecret := "URL_TOKEN_SECRET_CANARY"
	bodySecret := "SERVER_UUID_PASSWORD_SECRET_CANARY"
	errorSecret := "RAW_ERROR_SECRET_CANARY"
	oldSecret := "OLD_AGGREGATE_SECRET_CANARY"
	url := "https://user:password@secret.invalid/list?token=" + urlSecret
	validCommit := generationCommitResult{
		Committed: true,
		Selection: currentSelection{
			Kind: currentPresent,
			Generation: validatedGeneration{
				GenerationID: "1111111111111111111111111111111111111111111111111111111111111111",
				ConfigDigest: configDigest,
				Aggregate:    []byte(`{"new":true}`),
			},
		},
	}

	cases := []struct {
		name       string
		fetch      sourceFetchResult
		normalize  *recordingSourceNormalizer
		selection  currentSelection
		commit     generationCommitResult
		commitErr  error
		wantStatus int
		wantCode   string
	}{
		{
			name:  "transport failure",
			fetch: sourceFetchResult{Code: fetchCodeTransport},
			normalize: &recordingSourceNormalizer{
				output: []byte(`{"outbounds":[]}`),
			},
			selection:  currentSelection{Kind: currentAbsent},
			commit:     validCommit,
			wantStatus: http.StatusBadGateway,
			wantCode:   "source_unavailable",
		},
		{
			name: "normalizer raw error",
			fetch: sourceFetchResult{
				Body: []byte(bodySecret), Code: fetchCodeOK,
			},
			normalize: &recordingSourceNormalizer{
				err: errors.New(errorSecret + " " + bodySecret),
			},
			selection:  currentSelection{Kind: currentAbsent},
			commit:     validCommit,
			wantStatus: http.StatusBadGateway,
			wantCode:   "source_unavailable",
		},
		{
			name: "precommit raw error",
			fetch: sourceFetchResult{
				Body: []byte(bodySecret), Code: fetchCodeOK,
			},
			normalize: &recordingSourceNormalizer{
				output: []byte(
					`{"outbounds":[{"server":"` + bodySecret + `"}]}`,
				),
				info: NormalizeInfo{
					Format: FormatSingBoxJSON, Accepted: 1,
				},
			},
			selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: generation0,
					ConfigDigest: configDigest,
					Aggregate: []byte(
						`{"outbounds":["` + oldSecret + `"]}`,
					),
				},
			},
			commit:     generationCommitResult{Committed: false},
			commitErr:  errors.New(errorSecret),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "commit_failed",
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
				observation:  observation,
				selection:    testCase.selection,
				commitResult: testCase.commit,
				commitErr:    testCase.commitErr,
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
					Locker: &recordingSubscriptionLocker{},
					Store:  store,
					Fetcher: &recordingSourceFetcher{
						results: map[string]sourceFetchResult{
							url: testCase.fetch,
						},
					},
					Normalizer: testCase.normalize,
				},
			)
			recorder := httptest.NewRecorder()
			gatewayHTTPHandler{
				config: gatewayConfig{
					ConfigDigest:            configDigest,
					AggregateTimeoutSeconds: 5,
					Sources: []gatewaySource{{
						URL: url, URLDigest: testSHA256(url),
					}},
				},
				engine: engine,
			}.ServeHTTP(
				recorder,
				httptest.NewRequest(http.MethodGet, "/v1/aggregate", nil),
			)
			body := recorder.Body.String()
			if recorder.Code != testCase.wantStatus ||
				!strings.Contains(body, `"code":"`+testCase.wantCode+`"`) {
				t.Fatalf("response = %d %s", recorder.Code, body)
			}
			if len(body) > 256 {
				t.Fatalf("error body is unbounded: %d bytes", len(body))
			}
			for _, secret := range []string{
				urlSecret, bodySecret, errorSecret, oldSecret,
				"user:password", "secret.invalid",
			} {
				if strings.Contains(body, secret) {
					t.Fatalf("response leaked %q: %s", secret, body)
				}
			}
		})
	}
}

type blockingSourceNormalizer struct {
	startOnce sync.Once
	started   chan struct{}
	release   chan struct{}
	output    []byte
}

func (normalizer *blockingSourceNormalizer) Normalize(
	[]byte,
) ([]byte, NormalizeInfo, error) {
	normalizer.startOnce.Do(func() { close(normalizer.started) })
	<-normalizer.release
	return normalizer.output, NormalizeInfo{
		Format: FormatSingBoxJSON, Accepted: 1,
	}, nil
}

type cancellationTestLocker struct {
	released chan struct{}
	once     sync.Once
}

func newCancellationTestLocker() *cancellationTestLocker {
	return &cancellationTestLocker{released: make(chan struct{})}
}

func (locker *cancellationTestLocker) Acquire(
	context.Context,
) (heldSubscriptionLock, error) {
	return cancellationTestHeldLock{owner: locker}, nil
}

type cancellationTestHeldLock struct {
	owner *cancellationTestLocker
}

func (lock cancellationTestHeldLock) Release() error {
	lock.owner.once.Do(func() { close(lock.owner.released) })
	return nil
}
