package main

import (
	"bytes"
	"testing"

	convertersubscription "github.com/haierkeys/singbox-subscribe-convert/internal/subscription"
)

func TestGatewayAggregateIsByteStableInConverterCache(t *testing.T) {
	raw := []byte(`{"outbounds":[` +
		`{"type":"socks","tag":"First","server":"192.0.2.1","server_port":1080},` +
		`{"type":"direct","tag":"Second"}` +
		`]}`)
	aggregate, info, err := NormalizeDocument(raw)
	if err != nil {
		t.Fatalf("NormalizeDocument: %v", err)
	}
	if info.Accepted != 2 {
		t.Fatalf("gateway accepted = %d, want 2", info.Accepted)
	}

	cached, cachedInfo, err := convertersubscription.Normalize(aggregate)
	if err != nil {
		t.Fatalf("converter Normalize: %v", err)
	}
	if cachedInfo.NodeCount != info.Accepted {
		t.Fatalf(
			"converter accepted = %d, want %d",
			cachedInfo.NodeCount,
			info.Accepted,
		)
	}
	if !bytes.Equal(cached, aggregate) {
		t.Fatalf(
			"converter cache is not byte-stable\naggregate=%s\ncached=%s",
			aggregate,
			cached,
		)
	}
}
