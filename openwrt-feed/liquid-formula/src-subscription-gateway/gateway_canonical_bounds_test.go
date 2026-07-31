package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCanonicalAggregateOutboundLimitIsInclusiveAfterDeduplication(
	t *testing.T,
) {
	t.Run("8192 surviving nodes accepted", func(t *testing.T) {
		got, err := mergeCanonicalTestObjects(
			[]string{canonicalDistinctNodeDocument(8192)}, []int{1},
		)
		if err != nil {
			t.Fatalf("8192 distinct nodes rejected: %v", err)
		}
		var root struct {
			Outbounds []json.RawMessage `json:"outbounds"`
		}
		if err := json.Unmarshal(got, &root); err != nil {
			t.Fatalf("accepted aggregate is not JSON: %v", err)
		}
		if len(root.Outbounds) != 8192 {
			t.Fatalf("surviving outbounds = %d, want 8192",
				len(root.Outbounds))
		}
	})

	t.Run("8193 surviving nodes rejected", func(t *testing.T) {
		if _, err := mergeCanonicalTestObjects(
			[]string{canonicalDistinctNodeDocument(8193)}, []int{1},
		); err == nil {
			t.Fatal("8193 distinct nodes were accepted")
		}
	})

	t.Run("duplicates do not count toward surviving limit", func(t *testing.T) {
		object := canonicalDistinctNodeDocument(8192)
		got, err := mergeCanonicalTestObjects(
			[]string{object}, []int{1, 1},
		)
		if err != nil {
			t.Fatalf("duplicate occurrence exceeded node limit: %v", err)
		}
		var root struct {
			Outbounds []json.RawMessage `json:"outbounds"`
		}
		if err := json.Unmarshal(got, &root); err != nil {
			t.Fatalf("accepted aggregate is not JSON: %v", err)
		}
		if len(root.Outbounds) != 8192 {
			t.Fatalf("deduplicated outbounds = %d, want 8192",
				len(root.Outbounds))
		}
	})
}

func TestCompactCanonicalAggregateByteLimitIsInclusive(t *testing.T) {
	const aggregateLimit = 32 * 1024 * 1024

	t.Run("exactly 32 MiB accepted without trailing newline",
		func(t *testing.T) {
			objects := canonicalAggregateBoundaryObjects(t, 0)
			got, err := mergeCanonicalTestObjects(objects, []int{1, 2})
			if err != nil {
				t.Fatalf("exact 32 MiB aggregate rejected: %v", err)
			}
			if len(got) != aggregateLimit {
				t.Fatalf("aggregate bytes = %d, want %d",
					len(got), aggregateLimit)
			}
			if bytes.HasSuffix(got, []byte{'\n'}) {
				t.Fatal("canonical aggregate has a trailing newline")
			}
			if !bytes.HasPrefix(got,
				[]byte(`{"outbounds":[{"p000":"`)) ||
				!bytes.HasSuffix(got,
					[]byte(`","tag":"Unnamed","type":"direct"}]}`)) {
				t.Fatal("boundary aggregate does not have canonical framing")
			}
		})

	t.Run("32 MiB plus one rejected", func(t *testing.T) {
		objects := canonicalAggregateBoundaryObjects(t, 1)
		if _, err := mergeCanonicalTestObjects(
			objects, []int{1, 2},
		); err == nil {
			t.Fatal("32 MiB + 1 compact aggregate was accepted")
		}
	})
}

func canonicalDistinctNodeDocument(count int) string {
	var raw strings.Builder
	raw.Grow(count * 48)
	raw.WriteString(`{"outbounds":[`)
	for index := 0; index < count; index++ {
		if index != 0 {
			raw.WriteByte(',')
		}
		fmt.Fprintf(&raw,
			`{"id":%d,"tag":"node-%d","type":"direct"}`,
			index, index)
	}
	raw.WriteString(`]}`)
	return raw.String()
}

func canonicalAggregateBoundaryObjects(t *testing.T, extra int) []string {
	t.Helper()
	const aggregateLimit = 32 * 1024 * 1024
	// Keep every decoded scalar within the production 64 KiB bound. Repeating
	// the same legal object also keeps the original cross-occurrence dedupe
	// coverage while the compact surviving aggregate lands on the byte edge.
	object := string(canonicalProductionBoundarySource(
		t, aggregateLimit+extra, 512,
	))
	return []string{object, object}
}
