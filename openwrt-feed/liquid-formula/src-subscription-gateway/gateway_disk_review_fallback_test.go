package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func diskReviewFallbackCandidate(
	parent string,
	object []byte,
) generationCandidate {
	candidate := commitTestCandidate(parent, object)
	candidate.State = generationStateDegraded
	candidate.Sources[0].Result = sourceResultFallback
	candidate.Sources[0].FetchCode = fetchCodeTimeout
	return candidate
}

func TestDiskCommitFallbackOccurrenceMustExactlyMatchCurrentParent(
	t *testing.T,
) {
	for _, testCase := range []struct {
		name   string
		mutate func(*generationCandidate)
	}{
		{
			name: "URL digest",
			mutate: func(candidate *generationCandidate) {
				candidate.Sources[0].URLDigest =
					strings.Repeat("e", 64)
			},
		},
		{
			name: "object bytes and digest",
			mutate: func(candidate *generationCandidate) {
				candidate.Objects[0] = commitTestObject(
					"FALLBACK_OBJECT_SECRET_CANARY",
				)
			},
		},
		{
			name: "normalize info",
			mutate: func(candidate *generationCandidate) {
				candidate.Sources[0].Info.Format =
					FormatPlainURI
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "subscriptions")
			filesystem := newCommitTestFilesystem()
			hook := &diskReviewMutationHook{}
			store := diskReviewStore(
				root, []byte{0x41, 0x42}, filesystem, hook,
			)
			parentObject := commitTestObject("Parent")
			firstResult, firstErr, _ := commitTestCommit(
				t,
				store,
				commitTestCandidate("", parentObject),
			)
			first := commitTestRequireCommitted(
				t, firstResult, firstErr,
			)
			loaded, loadErr := store.LoadCurrent(
				context.Background(),
			)
			if loadErr != nil ||
				loaded.Kind != currentPresent ||
				loaded.Generation.GenerationID !=
					first.Generation.GenerationID {
				t.Fatalf(
					"parent is not fully valid: selection=%#v err=%v",
					loaded, loadErr,
				)
			}
			oldCurrent := commitTestReadCurrent(t, root)
			candidate := diskReviewFallbackCandidate(
				first.Generation.GenerationID, parentObject,
			)
			testCase.mutate(&candidate)

			result, err, gate := commitTestCommit(
				t, store, candidate,
			)
			diskReviewRequireGenericPreCurrentFailure(
				t,
				result,
				err,
				gate,
				"FALLBACK_",
			)
			commitTestRequireOldCurrent(
				t,
				root,
				store,
				oldCurrent,
				first.Generation.GenerationID,
			)
		})
	}
}

func TestDiskCommitAbsentParentNeverAcceptsFallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &diskReviewMutationHook{}
	store := diskReviewStore(
		root, []byte{0x51}, filesystem, hook,
	)
	result, err, gate := commitTestCommit(
		t,
		store,
		diskReviewFallbackCandidate(
			"",
			commitTestObject("ABSENT_FALLBACK_SECRET_CANARY"),
		),
	)
	diskReviewRequireGenericPreCurrentFailure(
		t,
		result,
		err,
		gate,
		"ABSENT_FALLBACK_SECRET_CANARY",
	)
	if _, statErr := os.Lstat(
		filepath.Join(root, "current"),
	); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("absent fallback created current: %v", statErr)
	}
}

func TestDiskCommitFallbackRequiresExactCanonicalParentBytes(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &diskReviewMutationHook{}
	store := diskReviewStore(
		root, []byte{0x61, 0x62}, filesystem, hook,
	)
	parentObject := commitTestObject("Parent")
	firstResult, firstErr, _ := commitTestCommit(
		t,
		store,
		commitTestCandidate("", parentObject),
	)
	first := commitTestRequireCommitted(t, firstResult, firstErr)
	oldCurrent := commitTestReadCurrent(t, root)

	byteDistinctEquivalent := []byte(
		"{\n  \"outbounds\" : [ { \"type\" : \"direct\", \"tag\" : \"Parent\" } ]\n}",
	)
	if bytes.Equal(byteDistinctEquivalent, parentObject) {
		t.Fatal("fallback fixture is not byte-distinct")
	}
	canonical, _, canonicalErr := canonicalizeStoredSource(
		byteDistinctEquivalent,
	)
	if canonicalErr != nil || !bytes.Equal(canonical, parentObject) {
		t.Fatalf(
			"fallback fixture is not canonically equivalent: canonical=%q err=%v",
			canonical,
			canonicalErr,
		)
	}
	candidate := diskReviewFallbackCandidate(
		first.Generation.GenerationID,
		byteDistinctEquivalent,
	)
	result, err, gate := commitTestCommit(t, store, candidate)
	diskReviewRequireGenericPreCurrentFailure(
		t,
		result,
		err,
		gate,
		"Parent",
	)
	commitTestRequireOldCurrent(
		t,
		root,
		store,
		oldCurrent,
		first.Generation.GenerationID,
	)
}
