package main

import "context"

type currentKind uint8

const (
	currentAbsent currentKind = iota
	currentPresent
	currentInvalid
)

type currentObservation struct {
	Kind         currentKind
	GenerationID string
	// Validated is true only when the observed present generation passed full
	// store validation. Absent and invalid observations must leave it false.
	Validated bool
}

type generationSource struct {
	Index        int
	URLDigest    string
	ObjectDigest string
	Normalized   []byte
	Info         NormalizeInfo
}

type validatedGeneration struct {
	GenerationID            string
	ConfigDigest            string
	Aggregate               []byte
	Sources                 []generationSource
	LegacyConsumedURLDigest string
}

type currentSelection struct {
	Kind       currentKind
	Generation validatedGeneration
}

type sourceFetchCode string

const (
	fetchCodeOK            sourceFetchCode = "ok"
	fetchCodeTimeout       sourceFetchCode = "timeout"
	fetchCodeHTTPStatus    sourceFetchCode = "http_status"
	fetchCodeRedirectLimit sourceFetchCode = "redirect_limit"
	fetchCodeBodyTooLarge  sourceFetchCode = "body_too_large"
	fetchCodeTransport     sourceFetchCode = "transport"
	fetchCodeNormalize     sourceFetchCode = "normalize"
)

const (
	generationStateFresh    = "fresh"
	generationStateDegraded = "degraded"
	sourceResultFresh       = "fresh"
	sourceResultFallback    = "fallback"
)

type sourceFetchResult struct {
	Body []byte
	Code sourceFetchCode
}

type generationCandidateSource struct {
	Index int
	// ObjectIndex is a 1-based reference into generationCandidate.Objects.
	ObjectIndex int
	URLDigest   string
	Result      string
	FetchCode   sourceFetchCode
	Info        NormalizeInfo
}

type generationCandidate struct {
	ParentGenerationID      string
	ConfigDigest            string
	State                   string
	LegacyConsumedURLDigest string
	legacyMarkerReceipt     *legacyReadToken
	// Objects contains unique normalized buffers. Fresh buffers transfer from
	// the normalizer; fallback buffers borrow validated immutable generation
	// storage. Stores must treat every slice as read-only during Commit.
	Objects [][]byte
	Sources []generationCandidateSource
}

type generationCommitResult struct {
	Committed   bool
	Selection   currentSelection
	WarningCode string
}

type generationStore interface {
	// ObserveCurrent returns a cheap request-start observation. Validated may
	// be true only when the reported present generation was fully validated.
	ObserveCurrent(context.Context) (currentObservation, error)
	// LoadCurrent runs while the subscription lock is held and returns either
	// a fully valid present selection or a clean absent selection.
	LoadCurrent(context.Context) (currentSelection, error)
	// Commit may stage all candidate data first. Immediately before the single
	// logical rename that selects the new current generation, it must call
	// beginCurrentCommit(ctx). A false result forbids publication. Once Begin
	// succeeds, Commit must report any completed logical publication with
	// Committed=true even when a later durability operation returns a warning.
	Commit(
		context.Context,
		generationCandidate,
	) (generationCommitResult, error)
}

type heldSubscriptionLock interface {
	Release() error
}

type subscriptionLocker interface {
	Acquire(context.Context) (heldSubscriptionLock, error)
}

type sourceFetcher interface {
	Fetch(context.Context, string, string) sourceFetchResult
}

type sourceNormalizer interface {
	// Normalize transfers a newly allocated output buffer to the caller on
	// success. The caller may retain that immutable buffer without cloning it.
	Normalize([]byte) ([]byte, NormalizeInfo, error)
}

type subscriptionEngineDependencies struct {
	Locker     subscriptionLocker
	Store      generationStore
	Fetcher    sourceFetcher
	Normalizer sourceNormalizer
	Legacy     legacySourceProvider
}
