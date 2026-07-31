package main

import (
	"context"
	"errors"
	"testing"
)

func TestSubscriptionCommitFailurePreservesOnlyValidatedCurrent(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	url := "https://commit-failure.invalid/list"
	cases := []struct {
		name          string
		observation   currentObservation
		selection     currentSelection
		result        generationCommitResult
		err           error
		wantPreserved bool
	}{
		{
			name:        "absent current",
			observation: currentObservation{Kind: currentAbsent},
			selection:   currentSelection{Kind: currentAbsent},
			result:      generationCommitResult{Committed: false},
			err:         errors.New("PRECOMMIT_SECRET_CANARY"),
		},
		{
			name: "validated current",
			observation: currentObservation{
				Kind: currentPresent, GenerationID: generation0,
			},
			selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: generation0,
					ConfigDigest: configDigest,
					Aggregate:    []byte(`{"old":true}`),
				},
			},
			result:        generationCommitResult{Committed: false},
			err:           errors.New("PRECOMMIT_SECRET_CANARY"),
			wantPreserved: true,
		},
		{
			name:        "false result without raw error still fails",
			observation: currentObservation{Kind: currentAbsent},
			selection:   currentSelection{Kind: currentAbsent},
			result:      generationCommitResult{Committed: false},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &scriptedGenerationStore{
				observation:  testCase.observation,
				selection:    testCase.selection,
				commitResult: testCase.result,
				commitErr:    testCase.err,
			}
			outcome := newCommitTestEngine(
				configDigest, url, store,
			).Aggregate(context.Background())
			if outcome.Code != aggregateCodeCommitFailed ||
				outcome.SourceIndex != 0 ||
				outcome.Preserved != testCase.wantPreserved ||
				len(outcome.Bytes) != 0 {
				t.Fatalf("precommit outcome = %#v", outcome)
			}
			if store.commitCalls != 1 {
				t.Fatalf("commit calls = %d, want 1", store.commitCalls)
			}
		})
	}
}

func TestSubscriptionPublishedCommitWarningRemainsSuccessful(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	url := "https://published-warning.invalid/list"
	exactAggregate := []byte(
		`{"outbounds":[{"type":"socks","password":"legitimate-secret"}]}`,
	)
	store := &scriptedGenerationStore{
		observation: currentObservation{Kind: currentAbsent},
		selection:   currentSelection{Kind: currentAbsent},
		commitResult: generationCommitResult{
			Committed:   true,
			WarningCode: "current_dir_sync_failed",
			Selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: generation1,
					ConfigDigest: configDigest,
					Aggregate:    exactAggregate,
				},
			},
		},
		commitErr: errors.New(
			"POST_POINTER_SYNC_SECRET_CANARY must not roll back",
		),
	}
	outcome := newCommitTestEngine(
		configDigest, url, store,
	).Aggregate(context.Background())
	if outcome.Code != "" ||
		outcome.SourceIndex != 0 ||
		outcome.Preserved ||
		string(outcome.Bytes) != string(exactAggregate) {
		t.Fatalf("published warning outcome = %#v", outcome)
	}
	if store.commitCalls != 1 || store.loadCalls != 1 {
		t.Fatalf("published commit was retried/rolled back: loads=%d commits=%d",
			store.loadCalls, store.commitCalls)
	}
}

func TestSubscriptionCommitMustAdvanceParentGeneration(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation0  = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	url := "https://fake-commit.invalid/list"
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
			},
		},
		commitResult: generationCommitResult{
			Committed: true,
			Selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: generation0,
					ConfigDigest: configDigest,
					Aggregate:    []byte(`{"fake-commit":true}`),
				},
			},
		},
	}
	outcome := newCommitTestEngine(
		configDigest, url, store,
	).Aggregate(context.Background())
	if outcome.Code != aggregateCodeStateInvalid ||
		len(outcome.Bytes) != 0 ||
		store.commitCalls != 1 {
		t.Fatalf("non-advancing commit outcome=%#v commits=%d",
			outcome, store.commitCalls)
	}
}

func TestSubscriptionUnknownPostCommitWarningCannotUndoCommit(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	url := "https://unknown-warning.invalid/list"
	exact := []byte(`{"unknown-warning":"still-committed"}`)
	store := &scriptedGenerationStore{
		observation: currentObservation{Kind: currentAbsent},
		selection:   currentSelection{Kind: currentAbsent},
		commitResult: generationCommitResult{
			Committed:   true,
			WarningCode: "UNKNOWN_WARNING_SECRET_CANARY",
			Selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: generation1,
					ConfigDigest: configDigest,
					Aggregate:    exact,
				},
			},
		},
		commitErr: errors.New("UNKNOWN_WARNING_ERROR_SECRET_CANARY"),
	}
	outcome := newCommitTestEngine(
		configDigest, url, store,
	).Aggregate(context.Background())
	if outcome.Code != "" || string(outcome.Bytes) != string(exact) {
		t.Fatalf("unknown postcommit warning outcome = %#v", outcome)
	}
}

func TestSubscriptionCommittedResultWithoutBeginFailsClosed(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		generation1  = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	url := "https://missing-begin.invalid/list"
	store := &scriptedGenerationStore{
		observation: currentObservation{Kind: currentAbsent},
		selection:   currentSelection{Kind: currentAbsent},
		skipBegin:   true,
		commitResult: generationCommitResult{
			Committed: true,
			Selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: generation1,
					ConfigDigest: configDigest,
					Aggregate:    []byte(`{"unauthorized":true}`),
				},
			},
		},
	}
	outcome := newCommitTestEngine(
		configDigest, url, store,
	).Aggregate(context.Background())
	if outcome.Code != aggregateCodeStateInvalid ||
		len(outcome.Bytes) != 0 ||
		store.commitCalls != 1 {
		t.Fatalf("unbegun commit outcome=%#v commits=%d",
			outcome, store.commitCalls)
	}
}

func newCommitTestEngine(
	configDigest string,
	url string,
	store generationStore,
) aggregateEngine {
	return newSubscriptionAggregateEngine(
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
			Normalizer: &bodySourceNormalizer{},
		},
	)
}
