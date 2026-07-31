//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const canonicalReviewSpoolSlack = 1024 * 1024

func TestCanonicalDepthBoundaryKeepsPhysicalSpoolLinear(t *testing.T) {
	raw := canonicalReviewDeepSource(4 * 1024 * 1024)
	candidate := generationCandidate{
		Objects: [][]byte{raw},
		Sources: []generationCandidateSource{{
			Index:       1,
			ObjectIndex: 1,
		}},
	}

	output, backingBytes, err :=
		canonicalReviewObservedMerge(t, candidate)
	if err != nil {
		t.Fatalf("legal depth-boundary source rejected: %v", err)
	}
	if !json.Valid(output) ||
		len(output) == 0 ||
		len(output) > canonicalAggregateByteLimit {
		t.Fatalf("invalid compact aggregate size %d", len(output))
	}
	budget := int64(3*len(output) + canonicalReviewSpoolSlack)
	if backingBytes > budget {
		t.Fatalf(
			"depth-boundary merge used %d backing bytes for %d output bytes; linear budget is %d",
			backingBytes, len(output), budget,
		)
	}
}

func TestCanonicalRepeatedObjectOccurrenceDoesNotMultiplyPhysicalSpool(
	t *testing.T,
) {
	object := canonicalProductionBoundarySource(
		t, 4*1024*1024, 64,
	)
	sources := make([]generationCandidateSource, 8)
	for index := range sources {
		sources[index] = generationCandidateSource{
			Index:       index + 1,
			ObjectIndex: 1,
		}
	}
	output, backingBytes, err := canonicalReviewObservedMerge(
		t,
		generationCandidate{
			Objects: [][]byte{object},
			Sources: sources,
		},
	)
	if err != nil {
		t.Fatalf("eight repeated occurrences rejected: %v", err)
	}
	if len(output) != 4*1024*1024 {
		t.Fatalf("deduplicated aggregate has %d bytes, want %d",
			len(output), 4*1024*1024)
	}
	budget := int64(3*len(output) + canonicalReviewSpoolSlack)
	if backingBytes > budget {
		t.Fatalf(
			"repeated-object merge used %d backing bytes for %d output bytes; linear budget is %d",
			backingBytes, len(output), budget,
		)
	}
}

func TestCanonicalDistinctObjectsWithDuplicateIdentityBoundPhysicalSpool(
	t *testing.T,
) {
	candidate, totalInputBytes := canonicalReviewDuplicateIdentityCandidate(
		t, 4*1024*1024, 64, 8,
	)
	output, backingBytes, err := canonicalReviewObservedMerge(t, candidate)
	if err != nil {
		t.Fatalf("eight unique normalized objects rejected: %v", err)
	}
	const expectedInputBytes = 33558816
	const expectedOutputBytes = 4194305
	if totalInputBytes != expectedInputBytes {
		t.Fatalf("normalized input bytes = %d, want %d",
			totalInputBytes, expectedInputBytes)
	}
	if len(output) != expectedOutputBytes {
		t.Fatalf("deduplicated aggregate has %d bytes, want %d",
			len(output), expectedOutputBytes)
	}
	budget := int64(3*len(output) + canonicalReviewSpoolSlack)
	if backingBytes > budget {
		t.Fatalf(
			"unique duplicate objects used %d backing bytes for %d output bytes; linear budget is %d",
			backingBytes, len(output), budget,
		)
	}
}

func canonicalReviewDuplicateIdentityCandidate(
	t *testing.T,
	compactBytes int,
	paddingFields int,
	objectCount int,
) (generationCandidate, int) {
	t.Helper()
	base := canonicalProductionBoundarySource(
		t, compactBytes, paddingFields,
	)
	const suffix = `,"type":"direct"}]}`
	if !strings.HasSuffix(string(base), suffix) {
		t.Fatal("unexpected production boundary fixture")
	}
	prefix := base[:len(base)-len(suffix)]
	candidate := generationCandidate{
		Objects: make([][]byte, objectCount),
		Sources: make([]generationCandidateSource, objectCount),
	}
	totalInputBytes := 0
	for index := range candidate.Objects {
		raw := []byte(fmt.Sprintf(
			`%s,"tag":"source-%d","type":"direct"}]}`,
			prefix, index,
		))
		normalized, info, err := NormalizeDocument(raw)
		if err != nil {
			t.Fatalf("normalize unique object %d: %v", index+1, err)
		}
		if info.Format != FormatSingBoxJSON || info.Accepted != 1 {
			t.Fatalf("unique object %d info = %#v", index+1, info)
		}
		candidate.Objects[index] = normalized
		candidate.Sources[index] = generationCandidateSource{
			Index:       index + 1,
			ObjectIndex: index + 1,
		}
		totalInputBytes += len(normalized)
	}
	return candidate, totalInputBytes
}

func canonicalReviewDeepSource(targetBytes int) []byte {
	const wrappers = MaxDocumentDepth - 4

	var inner strings.Builder
	inner.Grow(targetBytes)
	inner.WriteByte('{')
	field := 0
	for inner.Len() < targetBytes-70*1024 {
		if field != 0 {
			inner.WriteByte(',')
		}
		fmt.Fprintf(&inner, `"p%04d":"`, field)
		remaining := targetBytes - inner.Len() - 2
		valueBytes := MaxScalarBytes
		if remaining < valueBytes {
			valueBytes = remaining
		}
		inner.WriteString(strings.Repeat("x", valueBytes))
		inner.WriteByte('"')
		field++
	}
	inner.WriteByte('}')

	var raw strings.Builder
	raw.Grow(inner.Len() + wrappers*6 + 128)
	raw.WriteString(`{"outbounds":[{"payload":`)
	for index := 0; index < wrappers; index++ {
		raw.WriteString(`{"x":`)
	}
	raw.WriteString(inner.String())
	raw.WriteString(strings.Repeat("}", wrappers))
	raw.WriteString(`,"tag":"deep","type":"direct"}]}`)
	return []byte(raw.String())
}

type canonicalReviewMergeResult struct {
	output []byte
	err    error
}

func canonicalReviewObservedMerge(
	t *testing.T,
	candidate generationCandidate,
) ([]byte, int64, error) {
	t.Helper()
	tempDirectory := t.TempDir()
	t.Setenv("TMPDIR", tempDirectory)

	done := make(chan canonicalReviewMergeResult, 1)
	go func() {
		output, err := mergeCanonicalAggregate(candidate)
		done <- canonicalReviewMergeResult{output: output, err: err}
	}()

	ticker := time.NewTicker(100 * time.Microsecond)
	defer ticker.Stop()
	var maximum int64
	for {
		if current := canonicalReviewOpenSpoolBytes(tempDirectory); current > maximum {
			maximum = current
		}
		select {
		case result := <-done:
			return result.output, maximum, result.err
		case <-ticker.C:
		}
	}
}

func canonicalReviewOpenSpoolBytes(tempDirectory string) int64 {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	prefix := tempDirectory + string(os.PathSeparator) +
		"liquid-formula-canonical-"
	var total int64
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		fdPath := filepath.Join("/proc/self/fd", entry.Name())
		target, err := os.Readlink(fdPath)
		if err != nil || !strings.HasPrefix(target, prefix) {
			continue
		}
		info, err := os.Stat(fdPath)
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}
