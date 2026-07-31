package main

import (
	"errors"
	"strings"
	"testing"
)

func TestVLESSURIEncryptionNoneIsOmitted(t *testing.T) {
	base := "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@192.0.2.1:443" +
		"?security=none&encryption="
	for name, value := range map[string]string{
		"empty":      "",
		"lowercase":  "none",
		"mixed case": "NoNe",
	} {
		t.Run(name, func(t *testing.T) {
			nodes, info := decodedOutbounds(t, []byte(base+value+"#vless-only"))
			if info.Format != FormatPlainURI ||
				info.Accepted != 1 || info.Skipped != 0 || len(nodes) != 1 {
				t.Fatalf("VLESS-only URI rejected: %#v %#v", info, nodes)
			}
			if _, present := nodes[0]["encryption"]; present {
				t.Fatalf("no-op encryption leaked into output: %#v", nodes[0])
			}
		})
	}

	raw := strings.Join([]string{
		base + "zero#invalid-value",
		base + "%20none#leading-space",
		base + "none%20#trailing-space",
		base + "none#valid",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 1 || info.Skipped != 3 ||
		len(nodes) != 1 || nodes[0]["tag"] != "valid" {
		t.Fatalf("non-no-op VLESS encryption accepted: %#v %#v", info, nodes)
	}
}

func TestClashVLESSEncryptionNoneIsOmitted(t *testing.T) {
	proxy := func(value string) string {
		return `proxies:
  - {name: vless-only, type: vless, server: 192.0.2.1, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, encryption: ` +
			value + "}\n"
	}
	for name, value := range map[string]string{
		"empty":      `""`,
		"lowercase":  "none",
		"mixed case": "NoNe",
	} {
		t.Run(name, func(t *testing.T) {
			nodes, info := decodedOutbounds(t, []byte(proxy(value)))
			if info.Format != FormatClashYAML ||
				info.Accepted != 1 || info.Skipped != 0 || len(nodes) != 1 {
				t.Fatalf("VLESS-only Clash document rejected: %#v %#v", info, nodes)
			}
			if _, present := nodes[0]["encryption"]; present {
				t.Fatalf("no-op encryption leaked into output: %#v", nodes[0])
			}
		})
	}

	raw := `proxies:
  - {name: invalid-value, type: vless, server: 192.0.2.1, port: 443, uuid: bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee, encryption: zero}
  - {name: invalid-space, type: vless, server: 192.0.2.2, port: 443, uuid: cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee, encryption: " none "}
  - {name: invalid-type, type: vless, server: 192.0.2.3, port: 443, uuid: dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee, encryption: true}
  - {name: valid, type: vless, server: 192.0.2.4, port: 443, uuid: eeeeeeee-bbbb-cccc-dddd-eeeeeeeeeeee, encryption: NONE}
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 1 || info.Skipped != 3 ||
		len(nodes) != 1 || nodes[0]["tag"] != "valid" {
		t.Fatalf("non-no-op Clash encryption accepted: %#v %#v", info, nodes)
	}
}

func TestClashDetectionUsesYAMLv3LineBreaks(t *testing.T) {
	breaks := []struct {
		name  string
		value string
	}{
		{name: "LF", value: "\n"},
		{name: "CR", value: "\r"},
		{name: "CRLF", value: "\r\n"},
		{name: "NEL", value: "\u0085"},
		{name: "LS", value: "\u2028"},
		{name: "PS", value: "\u2029"},
	}
	proxy := "  - {name: detected, type: ss, server: 192.0.2.1, " +
		"port: 8388, cipher: aes-128-gcm, password: p}"
	for _, lineBreak := range breaks {
		t.Run(lineBreak.name, func(t *testing.T) {
			raw := "mixed-port: 7890" + lineBreak.value +
				"proxies:" + lineBreak.value + proxy
			nodes, info := decodedOutbounds(t, []byte(raw))
			if info.Format != FormatClashYAML ||
				info.Accepted != 1 || len(nodes) != 1 ||
				nodes[0]["tag"] != "detected" {
				t.Fatalf("Clash root key after YAML break missed: %#v %#v", info, nodes)
			}
		})
	}
}

func TestClashDetectionAcceptsExactQuotedRootKeys(t *testing.T) {
	proxy := "  - {name: detected, type: ss, server: 192.0.2.1, " +
		"port: 8388, cipher: aes-128-gcm, password: p}"
	for _, key := range []string{"proxies", `"proxies"`, `'proxies'`} {
		t.Run(key, func(t *testing.T) {
			raw := "mixed-port: 7890\n" + key + "  :\n" + proxy
			nodes, info := decodedOutbounds(t, []byte(raw))
			if info.Format != FormatClashYAML ||
				info.Accepted != 1 || len(nodes) != 1 {
				t.Fatalf("exact proxies root key missed: %#v %#v", info, nodes)
			}
		})
	}

	for _, key := range []string{
		"proxy-providers", `"proxy-providers"`, `'proxy-providers'`,
	} {
		t.Run(key, func(t *testing.T) {
			raw := "mixed-port: 7890\n" + key + "  : {}"
			_, info, err := NormalizeDocument([]byte(raw))
			if !errors.Is(err, ErrNoValidNode) || info.Format != FormatClashYAML {
				t.Fatalf("provider-only root key was not recognized: %#v %v", info, err)
			}
		})
	}
}

func TestClashDetectionNearMissesRemainUnknown(t *testing.T) {
	documents := map[string]string{
		"plain scalar":       "service temporarily unavailable",
		"near miss key":      "status: unavailable\nproxies-extra: []",
		"quoted near miss":   "status: unavailable\n\"proxies-extra\": []",
		"provider near miss": "status: unavailable\n'proxy-providers-extra': {}",
		"nested block text":  "status: error\nmessage: |\n  proxies:\n",
	}
	for name, raw := range documents {
		t.Run(name, func(t *testing.T) {
			_, info, err := NormalizeDocument([]byte(raw))
			requireErrorCode(t, err, "unknown_format")
			if info.Format == FormatClashYAML {
				t.Fatalf("near miss was classified as Clash: %#v", info)
			}
		})
	}

	nodes, info := decodedOutbounds(t, []byte(
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#proxies: []",
	))
	if info.Format != FormatPlainURI ||
		len(nodes) != 1 || nodes[0]["tag"] != "proxies: []" {
		t.Fatalf("URI fragment was classified as Clash: %#v %#v", info, nodes)
	}

	for name, separator := range map[string]string{
		"line separator":      "\u2028",
		"paragraph separator": "\u2029",
	} {
		t.Run("URI fragment "+name, func(t *testing.T) {
			tag := "prefix" + separator + "proxies: []"
			nodes, info := decodedOutbounds(t, []byte(
				"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#"+tag,
			))
			if info.Format != FormatPlainURI ||
				len(nodes) != 1 || nodes[0]["tag"] != tag {
				t.Fatalf("Unicode URI fragment was classified as Clash: %#v %#v", info, nodes)
			}
		})
	}
}

func TestClashDetectionAllowsMetadataURLBeforeProxies(t *testing.T) {
	raw := `metadata: |
  https://example.invalid/info
proxies:
  - {name: valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Format != FormatClashYAML ||
		info.Accepted != 1 || info.Skipped != 0 ||
		len(nodes) != 1 || nodes[0]["tag"] != "valid" {
		t.Fatalf("metadata URL hid the proxies root key: %#v %#v", info, nodes)
	}
}

func TestVMessSchemeCaseParityAndExactTags(t *testing.T) {
	payload := encodeVMessForTest(map[string]any{
		"v": "2", "ps": " Exact? ", "add": "192.0.2.1", "port": "443",
		"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws",
	})
	for _, scheme := range []string{"vmess", "VMESS", "VmEsS"} {
		t.Run(scheme, func(t *testing.T) {
			nodes, info := decodedOutbounds(t, []byte(scheme+"://"+payload))
			if info.Accepted != 1 || info.Skipped != 0 || len(nodes) != 1 ||
				nodes[0]["tag"] != " Exact? " {
				t.Fatalf("VMess scheme case changed behavior: %#v %#v", info, nodes)
			}
		})
	}

	raw := "VmEsS://not-a-valid-payload\n" +
		"ss://YWVzLTEyOC1nY206cA@192.0.2.2:8388#valid"
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 1 || info.Skipped != 1 ||
		len(nodes) != 1 || nodes[0]["tag"] != "valid" {
		t.Fatalf("invalid mixed-case VMess payload did not fail closed: %#v %#v", info, nodes)
	}
}
