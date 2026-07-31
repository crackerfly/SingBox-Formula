package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	root := os.Getenv("LIQUID_FORMULA_NORMALIZER_FIXTURES")
	if root == "" {
		t.Fatal("LIQUID_FORMULA_NORMALIZER_FIXTURES is not set")
	}
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func decodedOutbounds(t *testing.T, raw []byte) ([]map[string]any, NormalizeInfo) {
	t.Helper()
	normalized, info, err := NormalizeDocument(raw)
	if err != nil {
		t.Fatalf("NormalizeDocument: %v", err)
	}
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		t.Fatalf("decode normalized output: %v", err)
	}
	return doc.Outbounds, info
}

func requireSafeDiagnostic(t *testing.T, value string) {
	t.Helper()
	for _, secret := range []string{
		"must-not-be-logged",
		"provider-secret-token",
		"tuic-v4-secret-token",
		"plain-secret-token",
		"fixture-native-private-key",
	} {
		if strings.Contains(value, secret) {
			t.Fatalf("diagnostic leaked %q: %s", secret, value)
		}
	}
	if strings.Contains(value, "://") {
		t.Fatalf("diagnostic leaked a URI: %s", value)
	}
}

func TestNativeSingBoxReencodesWithoutDroppingUnknownOutboundFields(t *testing.T) {
	nodes, info := decodedOutbounds(t, fixture(t, "singbox.json"))
	if info.Format != FormatSingBoxJSON || info.Accepted != 2 || info.Skipped != 0 {
		t.Fatalf("unexpected info: %#v", info)
	}
	if len(nodes) != 2 || nodes[0]["type"] != "wireguard" {
		t.Fatalf("native order/type changed: %#v", nodes)
	}
	if nodes[0]["private_key"] != "fixture-native-private-key" {
		t.Fatalf("native field was dropped: %#v", nodes[0])
	}
	if nodes[0]["server_port"] != json.Number("51820") {
		t.Fatalf("native number changed: %#v", nodes[0]["server_port"])
	}
}

func TestPlainAndBase64URIListsReuseSupportedParserSafely(t *testing.T) {
	plain, plainInfo := decodedOutbounds(t, fixture(t, "plain-uri.txt"))
	if plainInfo.Format != FormatPlainURI || plainInfo.Accepted != 4 || plainInfo.Skipped != 2 {
		t.Fatalf("unexpected plain info: %#v", plainInfo)
	}
	if got := []any{
		plain[0]["type"], plain[1]["type"], plain[2]["type"], plain[3]["type"],
	}; !reflect.DeepEqual(got, []any{"shadowsocks", "anytls", "socks", "vless"}) {
		t.Fatalf("plain node order changed: %#v", got)
	}
	if _, present := plain[3]["encryption"]; present {
		t.Fatalf("VLESS no-op encryption leaked into output: %#v", plain[3])
	}

	encoded, encodedInfo := decodedOutbounds(t, fixture(t, "base64-uri.txt"))
	if encodedInfo.Format != FormatBase64URI || encodedInfo.Accepted != 2 {
		t.Fatalf("unexpected base64 info: %#v", encodedInfo)
	}
	if encoded[0]["tag"] != "Base64-SS" || encoded[1]["tag"] != "Base64-Trojan" {
		t.Fatalf("base64 tags/order changed: %#v", encoded)
	}
}

func TestClashUsesOnlyRootInlineProxiesAndNeverProviders(t *testing.T) {
	nodes, info := decodedOutbounds(t, fixture(t, "clash-inline.yaml"))
	if info.Format != FormatClashYAML || info.Accepted != 3 || info.Skipped != 2 {
		t.Fatalf("unexpected clash info: %#v", info)
	}
	if nodes[0]["network"] != "tcp" {
		t.Fatalf("udp:false was not preserved as tcp-only: %#v", nodes[0])
	}
	tls, _ := nodes[1]["tls"].(map[string]any)
	utls, _ := tls["utls"].(map[string]any)
	if utls["fingerprint"] != "chrome" {
		t.Fatalf("client fingerprint was not mapped to uTLS: %#v", tls)
	}

	inlineOnly, mixedInfo := decodedOutbounds(t, fixture(t, "clash-inline-plus-provider.yaml"))
	if mixedInfo.Accepted != 1 || inlineOnly[0]["tag"] != "Inline only" {
		t.Fatalf("inline-plus-provider used the wrong nodes: %#v %#v", mixedInfo, inlineOnly)
	}

	_, _, err := NormalizeDocument(fixture(t, "clash-provider-only.yaml"))
	if err == nil || !errors.Is(err, ErrNoValidNode) {
		t.Fatalf("provider-only input must fail with ErrNoValidNode, got %v", err)
	}
	requireSafeDiagnostic(t, err.Error())
}

func TestBOMCRLFAnchorsAliasesAndPartialSkipsStayBounded(t *testing.T) {
	bom, bomInfo := decodedOutbounds(t, fixture(t, "bom-crlf-uri.txt"))
	if bomInfo.Format != FormatPlainURI || len(bom) != 2 {
		t.Fatalf("BOM/CRLF input was not normalized: %#v %#v", bomInfo, bom)
	}

	anchors, anchorInfo := decodedOutbounds(t, fixture(t, "clash-anchors.yaml"))
	if anchorInfo.Accepted != 3 || anchors[0]["tag"] != "Merged anchor" {
		t.Fatalf("anchors/aliases were not expanded: %#v %#v", anchorInfo, anchors)
	}

	partial, partialInfo := decodedOutbounds(t, fixture(t, "clash-partially-invalid.yaml"))
	if partialInfo.Accepted != 1 || partialInfo.Skipped != 4 || partial[0]["type"] != "tuic" {
		t.Fatalf("partially invalid source handling changed: %#v %#v", partialInfo, partial)
	}
	diagnostic, err := json.Marshal(partialInfo.Warnings)
	if err != nil {
		t.Fatal(err)
	}
	requireSafeDiagnostic(t, string(diagnostic))
	if len(partialInfo.Warnings) > MaxWarningSamples {
		t.Fatalf("too many warning samples: %d", len(partialInfo.Warnings))
	}
}

func TestDocumentAndSemanticBoundsAreInclusive(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		raw := bytes.Repeat([]byte{' '}, MaxInputBytes+1)
		_, _, err := NormalizeDocument(raw)
		requireErrorCode(t, err, "input_too_large")
	})
	t.Run("depth", func(t *testing.T) {
		raw := []byte("proxies:\n  - name: deep\n    type: ss\n    extra: " +
			strings.Repeat("[", MaxDocumentDepth+1) + "x" +
			strings.Repeat("]", MaxDocumentDepth+1) + "\n")
		_, _, err := NormalizeDocument(raw)
		requireErrorCode(t, err, "document_too_deep")
	})
	t.Run("scalar", func(t *testing.T) {
		raw := []byte("proxies:\n  - name: " + strings.Repeat("x", MaxScalarBytes+1) + "\n" +
			"    type: ss\n    server: 192.0.2.1\n    port: 8388\n" +
			"    cipher: aes-128-gcm\n    password: p\n")
		_, _, err := NormalizeDocument(raw)
		requireErrorCode(t, err, "scalar_too_large")
	})
	t.Run("aliases", func(t *testing.T) {
		raw := "base: &b { name: alias, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p }\nproxies:\n"
		for i := 0; i < MaxAliases+1; i++ {
			raw += "  - *b\n"
		}
		_, _, err := NormalizeDocument([]byte(raw))
		requireErrorCode(t, err, "too_many_aliases")
	})
	t.Run("normalized nodes", func(t *testing.T) {
		var raw strings.Builder
		raw.WriteString("proxies:\n")
		for i := 0; i < MaxNormalizedNodes+1; i++ {
			raw.WriteString("  - {name: n, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}\n")
		}
		_, _, err := NormalizeDocument([]byte(raw.String()))
		requireErrorCode(t, err, "too_many_outbounds")
	})
}

func TestZeroValidAndMalformedInputsReturnOnlyStableDiagnostics(t *testing.T) {
	for _, name := range []string{"unknown.txt", "clash-provider-only.yaml"} {
		_, info, err := NormalizeDocument(fixture(t, name))
		if err == nil {
			t.Fatalf("%s unexpectedly succeeded", name)
		}
		requireSafeDiagnostic(t, err.Error())
		encoded, marshalErr := json.Marshal(info)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		requireSafeDiagnostic(t, string(encoded))
	}
}

func requireErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %q", code)
	}
	var normalizeErr *NormalizeError
	if !errors.As(err, &normalizeErr) {
		t.Fatalf("expected NormalizeError, got %T: %v", err, err)
	}
	if normalizeErr.Code != code {
		t.Fatalf("error code = %q, want %q (%v)", normalizeErr.Code, code, err)
	}
	requireSafeDiagnostic(t, err.Error())
}
