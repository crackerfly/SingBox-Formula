package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestCanonicalAggregateAcceptsProductionReachableExact32MiB(
	t *testing.T,
) {
	const aggregateLimit = 32 * 1024 * 1024
	// The source has 512 bounded padding fields plus the required type field:
	// 513 fields. NormalizeDocument then adds the missing tag.
	raw := canonicalProductionBoundarySource(t, aggregateLimit, 512)
	gotFields := bytes.Count(raw, []byte(`"p`)) +
		bytes.Count(raw, []byte(`"type":`))
	if gotFields != 513 {
		t.Fatalf("native fixture has %d fields, want 513", gotFields)
	}
	if len(raw) > aggregateLimit {
		t.Fatalf("native source has %d bytes, exceeds %d",
			len(raw), aggregateLimit)
	}

	normalized, info, err := NormalizeDocument(raw)
	if err != nil {
		t.Fatalf("production normalizer rejected boundary source: %v", err)
	}
	if info.Format != FormatSingBoxJSON || info.Accepted != 1 {
		t.Fatalf("unexpected boundary normalization info: %#v", info)
	}
	if len(normalized) <= aggregateLimit {
		t.Fatalf("normalized fixture has %d encoded bytes, want more than %d",
			len(normalized), aggregateLimit)
	}

	aggregate, err := mergeCanonicalCandidate(normalized)
	if err != nil {
		t.Fatalf("production-reachable exact aggregate rejected: %v", err)
	}
	if len(aggregate) != aggregateLimit {
		t.Fatalf("aggregate has %d bytes, want %d",
			len(aggregate), aggregateLimit)
	}
	if bytes.HasSuffix(aggregate, []byte{'\n'}) {
		t.Fatal("exact-limit aggregate has a trailing newline")
	}
}

func canonicalProductionBoundarySource(
	t *testing.T,
	compactBytes int,
	paddingFields int,
) []byte {
	t.Helper()
	if paddingFields < 1 {
		t.Fatalf("padding field count = %d, want positive", paddingFields)
	}

	const prefix = `{"outbounds":[{`
	const suffix = `}]}`
	const assignedTag = `"tag":"Unnamed"`
	const outboundType = `"type":"direct"`
	fixedBytes := len(prefix) + len(suffix) +
		1 + len(assignedTag) +
		1 + len(outboundType)
	keys := make([]string, paddingFields)
	for index := range keys {
		keys[index] = fmt.Sprintf("p%03d", index)
		if index != 0 {
			fixedBytes++
		}
		// Quoted key, colon, and quoted string value.
		fixedBytes += len(keys[index]) + 5
	}
	payloadBytes := compactBytes - fixedBytes
	if payloadBytes < 0 ||
		payloadBytes > paddingFields*(64*1024) {
		t.Fatalf("cannot distribute %d payload bytes across %d fields",
			payloadBytes, paddingFields)
	}
	baseValueBytes := payloadBytes / paddingFields
	extraValues := payloadBytes % paddingFields

	var raw strings.Builder
	raw.Grow(compactBytes)
	raw.WriteString(prefix)
	for index, key := range keys {
		if index != 0 {
			raw.WriteByte(',')
		}
		valueBytes := baseValueBytes
		if index < extraValues {
			valueBytes++
		}
		if valueBytes > 64*1024 {
			t.Fatalf("field %d has %d decoded bytes, exceeds 64 KiB",
				index, valueBytes)
		}
		raw.WriteByte('"')
		raw.WriteString(key)
		raw.WriteString(`":"`)
		raw.WriteString(strings.Repeat("x", valueBytes))
		raw.WriteByte('"')
	}
	raw.WriteByte(',')
	raw.WriteString(outboundType)
	raw.WriteString(suffix)
	return []byte(raw.String())
}

func mergeCanonicalCandidate(normalized []byte) ([]byte, error) {
	return mergeCanonicalAggregate(generationCandidate{
		Objects: [][]byte{normalized},
		Sources: []generationCandidateSource{{
			Index:       1,
			ObjectIndex: 1,
		}},
	})
}
