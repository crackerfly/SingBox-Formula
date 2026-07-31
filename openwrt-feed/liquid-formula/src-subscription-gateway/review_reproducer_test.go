package main

import (
	"context"
	"testing"
)

func TestReviewLegacyConsumedDigestMustDisableReadoption(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root, nodePath := legacyTestPrepareProviderRoot(t)
	legacyTestWriteMarker(t, root, digest+"\n")
	provider := newFileLegacySourceProvider(root, nodePath)

	probe, err := provider.Probe(
		context.Background(),
		legacyProbeRequest{
			Selection: currentSelection{
				Kind: currentPresent,
				Generation: legacyTestGeneration(
					digest,
					nil,
				),
			},
			FirstURLDigest: digest,
		},
	)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if probe.Eligible {
		t.Fatalf(
			"already-consumed digest was eligible for legacy re-adoption: %#v",
			probe,
		)
	}
}

func TestReviewEngineNeverReadoptsConsumedLegacyDigest(t *testing.T) {
	const configDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	url := "https://legacy-consumed.invalid/list"
	urlDigest := testSHA256(url)
	parent := legacyTestGeneration(urlDigest, nil)
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
		&recordingSourceFetcher{
			results: map[string]sourceFetchResult{
				url: {Code: fetchCodeTransport},
			},
		},
		&bodySourceNormalizer{},
		provider,
	).Aggregate(context.Background())
	if outcome.Code != aggregateCodeSourceUnavailable ||
		outcome.SourceIndex != 1 || !outcome.Preserved ||
		provider.loadCalls != 0 || store.commitCalls != 0 {
		t.Fatalf(
			"consumed re-adoption outcome=%#v load=%d commit=%d",
			outcome,
			provider.loadCalls,
			store.commitCalls,
		)
	}
}
