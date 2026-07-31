//go:build !race

package main

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func TestCanonicalAggregateTotalAllocationStaysLinear(t *testing.T) {
	const compactBytes = 6 * 1024 * 1024
	raw := canonicalProductionBoundarySource(t, compactBytes, 96)
	normalized, info, err := NormalizeDocument(raw)
	if err != nil {
		t.Fatalf("normalize memory fixture: %v", err)
	}
	if info.Format != FormatSingBoxJSON || info.Accepted != 1 {
		t.Fatalf("unexpected memory-fixture info: %#v", info)
	}
	raw = nil

	runtime.GC()
	previousGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(previousGCPercent)

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	aggregate, err := mergeCanonicalCandidate(normalized)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(normalized)
	runtime.KeepAlive(aggregate)

	if err != nil {
		t.Fatalf("merge memory fixture: %v", err)
	}
	if len(aggregate) != compactBytes {
		t.Fatalf("aggregate has %d bytes, want %d",
			len(aggregate), compactBytes)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	budget := uint64(4*len(normalized) + 8*1024*1024)
	if allocated > budget {
		t.Fatalf(
			"merge allocated %d bytes for %d input bytes; budget is %d",
			allocated, len(normalized), budget,
		)
	}
}
