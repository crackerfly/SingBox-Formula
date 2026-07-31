//go:build linux && !race

package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestCanonicalDuplicateWideObjectsBoundMergeHeap(t *testing.T) {
	base := canonicalDocumentNodeBoundaryFixture(false)
	candidate := generationCandidate{
		Objects: make([][]byte, 8),
		Sources: make([]generationCandidateSource, 8),
	}
	totalInputBytes := 0
	for index := range candidate.Objects {
		raw := []byte(strings.Replace(
			base, `"tag":"nodes"`,
			fmt.Sprintf(`"tag":"nodes-%d"`, index), 1,
		))
		normalized, info, err := NormalizeDocument(raw)
		if err != nil {
			t.Fatalf("normalize wide object %d: %v", index+1, err)
		}
		if info.Format != FormatSingBoxJSON || info.Accepted != 1 {
			t.Fatalf("wide object %d info = %#v", index+1, info)
		}
		candidate.Objects[index] = normalized
		candidate.Sources[index] = generationCandidateSource{
			Index:       index + 1,
			ObjectIndex: index + 1,
		}
		totalInputBytes += len(normalized)
	}
	base = ""

	previousGCPercent := debug.SetGCPercent(100)
	defer debug.SetGCPercent(previousGCPercent)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	stop := make(chan struct{})
	peakResult := make(chan uint64, 1)
	go func() {
		ticker := time.NewTicker(250 * time.Microsecond)
		defer ticker.Stop()
		peak := before.HeapAlloc
		for {
			var current runtime.MemStats
			runtime.ReadMemStats(&current)
			if current.HeapAlloc > peak {
				peak = current.HeapAlloc
			}
			select {
			case <-stop:
				peakResult <- peak
				return
			case <-ticker.C:
			}
		}
	}()

	output, err := mergeCanonicalAggregate(candidate)
	close(stop)
	peak := <-peakResult
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(candidate)
	runtime.KeepAlive(output)
	if err != nil {
		t.Fatalf("merge eight wide objects: %v", err)
	}

	const expectedInputBytes = 9961240
	const expectedOutputBytes = 720880
	if totalInputBytes != expectedInputBytes {
		t.Fatalf("normalized input bytes = %d, want %d",
			totalInputBytes, expectedInputBytes)
	}
	if len(output) != expectedOutputBytes {
		t.Fatalf("deduplicated aggregate has %d bytes, want %d",
			len(output), expectedOutputBytes)
	}
	liveDelta := peak - before.HeapAlloc
	allocated := after.TotalAlloc - before.TotalAlloc
	scale := uint64(totalInputBytes + len(output))
	liveBudget := 4*scale + 16*1024*1024
	allocationBudget := 16*scale + 32*1024*1024
	if liveDelta > liveBudget {
		t.Errorf(
			"merge peak heap grew by %d bytes for %d input and %d output bytes; budget is %d",
			liveDelta, totalInputBytes, len(output), liveBudget,
		)
	}
	if allocated > allocationBudget {
		t.Errorf(
			"merge allocated %d bytes for %d input and %d output bytes; budget is %d",
			allocated, totalInputBytes, len(output), allocationBudget,
		)
	}
}
