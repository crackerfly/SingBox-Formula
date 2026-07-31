package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

type subscriptionAggregateEngine struct {
	config     gatewayConfig
	locker     subscriptionLocker
	store      generationStore
	fetcher    sourceFetcher
	normalizer sourceNormalizer
	legacy     legacySourceProvider
}

func newSubscriptionAggregateEngine(
	config gatewayConfig,
	dependencies subscriptionEngineDependencies,
) aggregateEngine {
	return &subscriptionAggregateEngine{
		config:     config,
		locker:     dependencies.Locker,
		store:      dependencies.Store,
		fetcher:    dependencies.Fetcher,
		normalizer: dependencies.Normalizer,
		legacy:     dependencies.Legacy,
	}
}

func (engine *subscriptionAggregateEngine) Aggregate(
	ctx context.Context,
) (outcome aggregateOutcome) {
	activeGeneration := ""
	currentObserved := false
	defer func() {
		if outcome.Code != "" {
			outcome.ActiveGeneration = activeGeneration
			outcome.CurrentObserved = currentObserved
		}
	}()
	if ctx == nil {
		return aggregateOutcome{
			Code: aggregateCodeStateInvalid, FailureStage: failureStageConfiguration,
		}
	}
	ctx, gate := ensureCurrentCommitGate(ctx)
	stopDeadlineGate := context.AfterFunc(ctx, gate.deny)
	defer stopDeadlineGate()
	if ctx.Err() != nil {
		gate.deny()
	}

	if engine == nil ||
		engine.locker == nil ||
		engine.store == nil ||
		engine.fetcher == nil ||
		engine.normalizer == nil ||
		!validTransactionConfig(engine.config) {
		return aggregateOutcome{
			Code: aggregateCodeStateInvalid, FailureStage: failureStageConfiguration,
		}
	}

	observation, err := engine.store.ObserveCurrent(ctx)
	if err != nil || !validCurrentObservation(observation) ||
		observation.Kind == currentInvalid {
		return aggregateOutcome{
			Code: aggregateCodeStateInvalid, FailureStage: failureStageCurrentState,
		}
	}
	currentObserved = true
	if observation.Kind == currentPresent {
		activeGeneration = observation.GenerationID
	}
	if len(engine.config.Sources) == 0 {
		return aggregateOutcome{
			Code:         aggregateCodeNoSources,
			Preserved:    observation.Validated,
			FailureStage: failureStageConfiguration,
		}
	}
	held, err := engine.locker.Acquire(ctx)
	if err != nil {
		if errors.Is(err, errSubscriptionBusy) {
			return aggregateOutcome{
				Code:      aggregateCodeBusy,
				Preserved: observation.Validated,
			}
		}
		return aggregateOutcome{
			Code:         aggregateCodeStateInvalid,
			Preserved:    observation.Validated,
			FailureStage: failureStageCurrentState,
		}
	}
	defer held.Release()

	selection, err := engine.store.LoadCurrent(ctx)
	if err != nil || !validCurrentSelection(selection) {
		return aggregateOutcome{
			Code: aggregateCodeStateInvalid, FailureStage: failureStageCurrentState,
		}
	}
	if selection.Kind == currentPresent {
		activeGeneration = selection.Generation.GenerationID
	}
	switch observation.Kind {
	case currentAbsent:
		switch selection.Kind {
		case currentAbsent:
		case currentPresent:
			return aggregateOutcome{
				Code: aggregateCodeStateInvalid, Preserved: true,
				FailureStage: failureStageCurrentState,
			}
		default:
			return aggregateOutcome{
				Code: aggregateCodeStateInvalid, FailureStage: failureStageCurrentState,
			}
		}
	case currentPresent:
		if selection.Kind != currentPresent {
			return aggregateOutcome{
				Code: aggregateCodeStateInvalid, FailureStage: failureStageCurrentState,
			}
		}
		if selection.Generation.GenerationID != observation.GenerationID {
			if selection.Generation.ConfigDigest == engine.config.ConfigDigest {
				return aggregateOutcome{
					Bytes: append(
						[]byte(nil), selection.Generation.Aggregate...,
					),
				}
			}
			return aggregateOutcome{
				Code: aggregateCodeStateInvalid, Preserved: true,
				FailureStage: failureStageCurrentState,
			}
		}
	default:
		return aggregateOutcome{
			Code: aggregateCodeStateInvalid, FailureStage: failureStageCurrentState,
		}
	}

	var parent *validatedGeneration
	if selection.Kind == currentPresent {
		parent = &selection.Generation
	}
	fallback, ok := fallbackSources(parent)
	if !ok {
		return aggregateOutcome{
			Code: aggregateCodeStateInvalid, FailureStage: failureStageCurrentState,
		}
	}
	legacyConsumedURLDigest := ""
	if parent != nil {
		legacyConsumedURLDigest = parent.LegacyConsumedURLDigest
	}
	var legacyMarkerReceipt *legacyReadToken
	legacyEligible := false
	var legacyToken legacyReadToken
	legacyAlreadyConsumed := parent != nil &&
		parent.LegacyConsumedURLDigest ==
			engine.config.Sources[0].URLDigest
	if engine.legacy != nil {
		probe, probeErr := engine.legacy.Probe(
			ctx,
			legacyProbeRequest{
				Selection:      selection,
				FirstURLDigest: engine.config.Sources[0].URLDigest,
			},
		)
		if probeErr != nil ||
			!validLegacyProbeResult(
				probe, engine.config.Sources[0].URLDigest,
			) {
			return aggregateOutcome{
				Code:         aggregateCodeStateInvalid,
				Preserved:    parent != nil,
				FailureStage: failureStageCurrentState,
			}
		}
		if probe.MatchingURLDigest != "" {
			if legacyConsumedURLDigest != "" &&
				legacyConsumedURLDigest != probe.MatchingURLDigest {
				return aggregateOutcome{
					Code:         aggregateCodeStateInvalid,
					Preserved:    parent != nil,
					FailureStage: failureStageCurrentState,
				}
			}
			legacyConsumedURLDigest = probe.MatchingURLDigest
			legacyToken = probe.Token
			legacyMarkerReceipt = &legacyToken
			legacyEligible = probe.Eligible &&
				!legacyAlreadyConsumed
		}
	}

	// A successful normalizer transfers its output buffer here. Validated
	// fallback buffers are already immutable and are borrowed without copying.
	// The pool retains only the first backing slice for each exact object.
	candidateObjects := make([][]byte, 0, len(engine.config.Sources))
	objectIndexes := make(map[[sha256.Size]byte][]int)
	internObject := func(normalized []byte) int {
		digest := sha256.Sum256(normalized)
		for _, objectIndex := range objectIndexes[digest] {
			if bytes.Equal(
				candidateObjects[objectIndex-1], normalized,
			) {
				return objectIndex
			}
		}
		candidateObjects = append(candidateObjects, normalized)
		objectIndex := len(candidateObjects)
		objectIndexes[digest] = append(
			objectIndexes[digest], objectIndex,
		)
		return objectIndex
	}
	unique := make(map[string]transactionSourceResult)
	for _, source := range engine.config.Sources {
		if _, exists := unique[source.URL]; exists {
			continue
		}
		if ctx.Err() != nil {
			return aggregateOutcome{
				Code:         aggregateCodeStateInvalid,
				Preserved:    parent != nil,
				FailureStage: failureStageCurrentState,
			}
		}
		result := engine.fetchAndNormalize(ctx, source.URL)
		if result.fresh {
			result.objectIndex = internObject(result.normalized)
			result.normalized = nil
		} else if cached, exists := fallback[source.URLDigest]; exists {
			result.objectIndex = internObject(cached.Normalized)
		}
		unique[source.URL] = result
	}

	state := generationStateFresh
	candidateSources := make(
		[]generationCandidateSource, 0, len(engine.config.Sources),
	)
	for index, source := range engine.config.Sources {
		result := unique[source.URL]
		candidate := generationCandidateSource{
			Index:     index + 1,
			URLDigest: source.URLDigest,
			FetchCode: result.fetchCode,
		}
		if result.fresh {
			candidate.ObjectIndex = result.objectIndex
			candidate.Info = cloneNormalizeInfo(result.info)
			candidate.Result = sourceResultFresh
			candidate.FetchCode = fetchCodeOK
		} else if cached, exists := fallback[source.URLDigest]; exists {
			candidate.ObjectIndex = result.objectIndex
			candidate.Info = cloneNormalizeInfo(cached.Info)
			candidate.Result = sourceResultFallback
			state = generationStateDegraded
		} else if index == 0 && legacyEligible {
			normalized, info, adopted := engine.loadLegacySource(
				ctx, legacyToken,
			)
			if !adopted {
				return aggregateOutcome{
					Code:        aggregateCodeSourceUnavailable,
					SourceIndex: index + 1,
					Preserved:   parent != nil,
					FailureStage: defaultFailureStage(aggregateOutcome{
						Code: aggregateCodeSourceUnavailable, FetchCode: result.fetchCode,
					}),
					FetchCode: result.fetchCode,
				}
			}
			candidate.ObjectIndex = internObject(normalized)
			candidate.Info = info
			candidate.Result = sourceResultFallback
			state = generationStateDegraded
		} else {
			return aggregateOutcome{
				Code:        aggregateCodeSourceUnavailable,
				SourceIndex: index + 1,
				Preserved:   parent != nil,
				FailureStage: defaultFailureStage(aggregateOutcome{
					Code: aggregateCodeSourceUnavailable, FetchCode: result.fetchCode,
				}),
				FetchCode: result.fetchCode,
			}
		}
		candidateSources = append(candidateSources, candidate)
	}
	if ctx.Err() != nil {
		return aggregateOutcome{
			Code: aggregateCodeStateInvalid, Preserved: parent != nil,
			FailureStage: failureStageCurrentState,
		}
	}
	parentID := ""
	if parent != nil {
		parentID = parent.GenerationID
	}
	commit, commitErr := engine.store.Commit(ctx, generationCandidate{
		ParentGenerationID:      parentID,
		ConfigDigest:            engine.config.ConfigDigest,
		State:                   state,
		LegacyConsumedURLDigest: legacyConsumedURLDigest,
		legacyMarkerReceipt:     legacyMarkerReceipt,
		Objects:                 candidateObjects,
		Sources:                 candidateSources,
	})
	if commit.Committed {
		if !gate.begun() ||
			!validCurrentSelection(commit.Selection) ||
			commit.Selection.Kind != currentPresent ||
			commit.Selection.Generation.ConfigDigest !=
				engine.config.ConfigDigest ||
			(parent != nil &&
				commit.Selection.Generation.GenerationID ==
					parent.GenerationID) {
			return aggregateOutcome{
				Code:         aggregateCodeStateInvalid,
				Preserved:    parent != nil,
				FailureStage: failureStageCommit,
			}
		}
		return aggregateOutcome{
			Bytes: append(
				[]byte(nil), commit.Selection.Generation.Aggregate...,
			),
		}
	}
	_ = commitErr
	return aggregateOutcome{
		Code: aggregateCodeCommitFailed, Preserved: parent != nil,
		FailureStage: failureStageCommit,
	}
}

func validLegacyProbeResult(
	result legacyProbeResult,
	firstURLDigest string,
) bool {
	if result.MatchingURLDigest == "" {
		return !result.Eligible
	}
	return result.MatchingURLDigest == firstURLDigest
}

func (engine *subscriptionAggregateEngine) loadLegacySource(
	ctx context.Context,
	token legacyReadToken,
) ([]byte, NormalizeInfo, bool) {
	raw, err := engine.legacy.Load(ctx, token)
	if err != nil || ctx.Err() != nil {
		return nil, NormalizeInfo{}, false
	}
	normalized, info, err := engine.normalizer.Normalize(raw)
	raw = nil
	if err != nil || ctx.Err() != nil ||
		info.Format != FormatSingBoxJSON ||
		!validNormalizedSource(normalized, info) {
		return nil, NormalizeInfo{}, false
	}
	canonical, count, err := canonicalizeStoredSource(normalized)
	if err != nil || !bytes.Equal(canonical, normalized) ||
		count != info.Accepted {
		return nil, NormalizeInfo{}, false
	}
	return normalized, cloneNormalizeInfo(info), true
}

type transactionSourceResult struct {
	fresh       bool
	normalized  []byte
	objectIndex int
	info        NormalizeInfo
	fetchCode   sourceFetchCode
}

func (engine *subscriptionAggregateEngine) fetchAndNormalize(
	ctx context.Context,
	rawURL string,
) transactionSourceResult {
	sourceCtx, cancel := context.WithTimeout(
		ctx,
		time.Duration(engine.config.SourceTimeoutSeconds)*time.Second,
	)
	defer cancel()
	fetched := engine.fetcher.Fetch(
		sourceCtx, rawURL, engine.config.UserAgent,
	)
	if fetched.Code != fetchCodeOK {
		if !validFetchFailureCode(fetched.Code) {
			fetched.Code = fetchCodeTransport
		}
		return transactionSourceResult{fetchCode: fetched.Code}
	}
	normalized, info, err := engine.normalizer.Normalize(fetched.Body)
	fetched.Body = nil
	if sourceCtx.Err() != nil || ctx.Err() != nil {
		return transactionSourceResult{fetchCode: fetchCodeTimeout}
	}
	if err != nil || !validNormalizedSource(normalized, info) {
		return transactionSourceResult{fetchCode: fetchCodeNormalize}
	}
	return transactionSourceResult{
		fresh:      true,
		normalized: normalized,
		info:       cloneNormalizeInfo(info),
		fetchCode:  fetchCodeOK,
	}
}

func validTransactionConfig(config gatewayConfig) bool {
	if !isLowerHexDigest(config.ConfigDigest) ||
		config.SourceTimeoutSeconds < 5 ||
		config.SourceTimeoutSeconds > 600 ||
		len(config.Sources) > 8 {
		return false
	}
	for _, source := range config.Sources {
		sum := sha256.Sum256([]byte(source.URL))
		if source.URL == "" ||
			source.URLDigest != fmt.Sprintf("%x", sum[:]) {
			return false
		}
	}
	return true
}

func validCurrentObservation(observation currentObservation) bool {
	switch observation.Kind {
	case currentAbsent:
		return observation.GenerationID == "" && !observation.Validated
	case currentPresent:
		return isLowerHexDigest(observation.GenerationID)
	case currentInvalid:
		return observation.GenerationID == "" && !observation.Validated
	default:
		return false
	}
}

func validCurrentSelection(selection currentSelection) bool {
	switch selection.Kind {
	case currentAbsent:
		return selection.Generation.GenerationID == "" &&
			selection.Generation.ConfigDigest == "" &&
			len(selection.Generation.Aggregate) == 0 &&
			len(selection.Generation.Sources) == 0 &&
			selection.Generation.LegacyConsumedURLDigest == ""
	case currentPresent:
		generation := selection.Generation
		if !isLowerHexDigest(generation.GenerationID) ||
			!isLowerHexDigest(generation.ConfigDigest) ||
			len(generation.Aggregate) == 0 ||
			(generation.LegacyConsumedURLDigest != "" &&
				!isLowerHexDigest(
					generation.LegacyConsumedURLDigest,
				)) {
			return false
		}
		_, ok := fallbackSources(&generation)
		return ok
	case currentInvalid:
		return false
	default:
		return false
	}
}

func fallbackSources(
	generation *validatedGeneration,
) (map[string]generationSource, bool) {
	result := make(map[string]generationSource)
	if generation == nil {
		return result, true
	}
	for _, source := range generation.Sources {
		if source.Index < 1 ||
			!isLowerHexDigest(source.URLDigest) ||
			!isLowerHexDigest(source.ObjectDigest) ||
			len(source.Normalized) == 0 ||
			!validCachedNormalizeInfo(source.Info) {
			return nil, false
		}
		sum := sha256.Sum256(source.Normalized)
		if source.ObjectDigest != fmt.Sprintf("%x", sum[:]) {
			return nil, false
		}
		if previous, exists := result[source.URLDigest]; exists {
			if previous.ObjectDigest != source.ObjectDigest ||
				!bytes.Equal(previous.Normalized, source.Normalized) ||
				!equalNormalizeInfo(previous.Info, source.Info) {
				return nil, false
			}
			continue
		}
		copySource := source
		// Validated generation bytes are immutable for the transaction, so the
		// fallback map borrows their backing rather than cloning large objects.
		copySource.Info = cloneNormalizeInfo(source.Info)
		result[source.URLDigest] = copySource
	}
	return result, true
}

func validFetchFailureCode(code sourceFetchCode) bool {
	switch code {
	case fetchCodeTimeout,
		fetchCodeHTTPStatus,
		fetchCodeRedirectLimit,
		fetchCodeBodyTooLarge,
		fetchCodeTransport,
		fetchCodeNormalize:
		return true
	default:
		return false
	}
}

func validNormalizedSource(
	normalized []byte,
	info NormalizeInfo,
) bool {
	return len(normalized) != 0 && validCachedNormalizeInfo(info)
}

func validCachedNormalizeInfo(info NormalizeInfo) bool {
	switch info.Format {
	case FormatSingBoxJSON,
		FormatBase64URI,
		FormatPlainURI,
		FormatClashYAML:
	default:
		return false
	}
	return info.Accepted > 0 &&
		info.Skipped >= 0 &&
		len(info.Warnings) <= MaxWarningSamples
}

func cloneNormalizeInfo(info NormalizeInfo) NormalizeInfo {
	copyInfo := info
	copyInfo.Warnings = append([]Warning(nil), info.Warnings...)
	return copyInfo
}

func equalNormalizeInfo(left, right NormalizeInfo) bool {
	if left.Format != right.Format ||
		left.Accepted != right.Accepted ||
		left.Skipped != right.Skipped ||
		len(left.Warnings) != len(right.Warnings) {
		return false
	}
	for index := range left.Warnings {
		if left.Warnings[index] != right.Warnings[index] {
			return false
		}
	}
	return true
}
