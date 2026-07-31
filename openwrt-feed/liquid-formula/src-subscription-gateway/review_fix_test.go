package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClashSemanticFieldsAreEitherMappedOrSkipped(t *testing.T) {
	cases := []struct {
		name  string
		proxy string
	}{
		{
			name:  "unknown common field",
			proxy: `{name: bad, type: ss, server: 192.0.2.2, port: 8388, cipher: aes-128-gcm, password: p, future-semantic: secret-value}`,
		},
		{
			name:  "Shadowsocks udp-over-tcp",
			proxy: `{name: bad, type: ss, server: 192.0.2.2, port: 8388, cipher: aes-128-gcm, password: p, udp-over-tcp: true}`,
		},
		{
			name:  "VMess packet encoding",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, packet-encoding: xudp}`,
		},
		{
			name:  "VMess global padding",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, global-padding: true}`,
		},
		{
			name:  "VMess authenticated length",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, authenticated-length: true}`,
		},
		{
			name:  "VMess invalid cipher",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: secret-cipher}`,
		},
		{
			name:  "VLESS packet encoding",
			proxy: `{name: bad, type: vless, server: vless.example, port: 443, uuid: bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee, packet-encoding: xudp}`,
		},
		{
			name:  "Hysteria2 bandwidth",
			proxy: `{name: bad, type: hy2, server: hy2.example, port: 443, password: p, up: 100 Mbps, down: 100 Mbps}`,
		},
		{
			name:  "Hysteria2 hop choice",
			proxy: `{name: bad, type: hy2, server: hy2.example, port: 443, password: p, ports: "443,8443"}`,
		},
		{
			name:  "TUIC reduce rtt",
			proxy: `{name: bad, type: tuic, server: tuic.example, port: 443, uuid: cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, reduce-rtt: true}`,
		},
		{
			name:  "TUIC heartbeat",
			proxy: `{name: bad, type: tuic, server: tuic.example, port: 443, uuid: cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, heartbeat-interval: 10s}`,
		},
		{
			name:  "AnyTLS idle session",
			proxy: `{name: bad, type: anytls, server: anytls.example, port: 443, password: p, idle-session-check-interval: 30s}`,
		},
		{
			name:  "TLS name cert verify",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, tls: true, name-cert-verify: secret-name}`,
		},
		{
			name:  "TLS private key",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, tls: true, private-key: secret-key}`,
		},
		{
			name:  "TLS post quantum toggle",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, tls: true, support-x25519mlkem768: true}`,
		},
		{
			name:  "TLS ECH",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, tls: true, ech-opts: {enable: true}}`,
		},
		{
			name:  "TLS shadow tls",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, tls: true, shadow-tls: {password: secret}}`,
		},
		{
			name:  "TLS restls",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, tls: true, restls: {password: secret}}`,
		},
		{
			name:  "TLS jls",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, tls: true, jls: {password: secret}}`,
		},
		{
			name:  "TLS mirror",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, tls: true, tlsmirror: {password: secret}}`,
		},
		{
			name:  "WS v2ray HTTP upgrade",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, network: ws, ws-opts: {path: /ws, v2ray-http-upgrade: true}}`,
		},
		{
			name:  "QUIC option payload",
			proxy: `{name: bad, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, network: quic, quic-opts: {security: secret}}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			raw := "proxies:\n  - " + testCase.proxy + "\n" +
				"  - {name: valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}\n"
			nodes, info := decodedOutbounds(t, []byte(raw))
			if len(nodes) != 1 || nodes[0]["tag"] != "valid" || info.Skipped != 1 {
				t.Fatalf("semantic field was silently lost: %#v %#v", info, nodes)
			}
			encoded, _ := json.Marshal(info)
			for _, secret := range []string{"secret-value", "secret-cipher", "secret-name", "secret-key", "password"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("Info leaked %q: %s", secret, encoded)
				}
			}
		})
	}
}

func TestClashRealityIsRejectedOnUnconfirmedProtocolFamilies(t *testing.T) {
	raw := `proxies:
  - {name: hy2, type: hy2, server: hy2.example, port: 443, password: p, reality-opts: {public-key: secret-key, short-id: ab12}}
  - {name: tuic, type: tuic, server: tuic.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, reality-opts: {public-key: secret-key, short-id: ab12}}
  - {name: anytls, type: anytls, server: anytls.example, port: 443, password: p, reality-opts: {public-key: secret-key, short-id: ab12}}
  - {name: valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if len(nodes) != 1 || info.Skipped != 3 {
		t.Fatalf("unconfirmed Reality combinations were accepted: %#v %#v", info, nodes)
	}
	encoded, _ := json.Marshal(info)
	if strings.Contains(string(encoded), "secret-key") {
		t.Fatalf("Info leaked Reality key: %s", encoded)
	}
}

func TestClashMapsSingleHTTPPathAndCanonicalizesUTLS(t *testing.T) {
	raw := `proxies:
  - name: HTTP
    type: vmess
    server: vmess.example
    port: 443
    uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
    alterId: 0
    cipher: auto
    tls: true
    client-fingerprint: iOS
    network: http
    http-opts:
      path: [/single]
      headers:
        Host: front.example
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 1 || info.Skipped != 0 {
		t.Fatalf("confirmed mapping failed: %#v", info)
	}
	transport := nodes[0]["transport"].(map[string]any)
	if transport["path"] != "/single" {
		t.Fatalf("single HTTP path was not mapped: %#v", transport)
	}
	if hosts, ok := transport["host"].([]any); !ok || len(hosts) != 1 || hosts[0] != "front.example" {
		t.Fatalf("Mihomo Host header was not mapped to sing-box HTTP host: %#v", transport)
	}
	if headers, ok := transport["headers"].(map[string]any); ok {
		if _, exists := headers["Host"]; exists {
			t.Fatalf("Mihomo Host was left as an ineffective ordinary header: %#v", transport)
		}
	}
	tls := nodes[0]["tls"].(map[string]any)
	utls := tls["utls"].(map[string]any)
	if utls["fingerprint"] != "ios" {
		t.Fatalf("uTLS fingerprint was not canonicalized: %#v", utls)
	}
}

func TestClashHTTPTransportOptionsMatchSelectedNetwork(t *testing.T) {
	raw := `proxies:
  - name: wrong-h2-options
    type: vmess
    server: vmess.example
    port: 443
    uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
    alterId: 0
    cipher: auto
    network: h2
    http-opts: {path: /wrong}
  - name: wrong-http-options
    type: vmess
    server: vmess.example
    port: 443
    uuid: bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee
    alterId: 0
    cipher: auto
    network: http
    h2-opts: {path: /wrong}
  - name: valid-h2
    type: vmess
    server: vmess.example
    port: 443
    uuid: cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee
    alterId: 0
    cipher: auto
    network: h2
    h2-opts: {path: /h2, host: [h2.example]}
  - name: valid-http
    type: vmess
    server: vmess.example
    port: 443
    uuid: dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee
    alterId: 0
    cipher: auto
    network: http
    http-opts: {path: [/http]}
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 2 || info.Skipped != 2 {
		t.Fatalf("HTTP option blocks were not selected by network: %#v %#v", info, nodes)
	}
	if nodes[0]["tag"] != "valid-h2" || nodes[1]["tag"] != "valid-http" {
		t.Fatalf("unexpected HTTP transport nodes: %#v", nodes)
	}
}

func TestURISecurityModesFailClosedAndRealityIsNotDropped(t *testing.T) {
	validVMess := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "valid-vmess", "add": "vmess.example", "port": "443",
		"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws", "tls": "tls", "sni": "vmess.example",
		"fp": "iOS",
	})
	realityVMess := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "secret-reality-vmess", "add": "secret.example", "port": "443",
		"id": "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws", "tls": "reality", "sni": "secret.example",
		"fp": "chrome", "pbk": "secret-public-key", "sid": "ab12",
	})
	invalidCipherVMess := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "secret-cipher-vmess", "add": "secret.example", "port": "443",
		"id": "cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "secret-cipher", "net": "ws", "tls": "tls",
	})
	raw := strings.Join([]string{
		"trojan://secret@secret.example:443?security=none#secret-none",
		"trojan://secret@secret.example:443?security=secret-mode#secret-unknown",
		"vless://dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=secret-mode#secret-vless",
		"vmess://" + realityVMess,
		"vmess://" + invalidCipherVMess,
		"hy2://secret@secret.example:443?security=none#secret-hy2",
		"tuic://eeeeeeee-bbbb-cccc-dddd-eeeeeeeeeeee:secret@secret.example:443?security=none#secret-tuic",
		"anytls://secret@secret.example:443?security=none#secret-anytls",
		"vmess://" + validVMess,
		"vless://ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee@vless.example:443?security=reality&sni=vless.example&fp=iOS&pbk=fixture-public-key&sid=ab12#valid-vless",
		"trojan://p@trojan.example:443?security=tls&sni=trojan.example#valid-trojan",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 3 || info.Skipped != 8 {
		t.Fatalf("security modes were not fail closed: %#v %#v", info, nodes)
	}
	vmessTLS := nodes[0]["tls"].(map[string]any)
	if vmessTLS["utls"].(map[string]any)["fingerprint"] != "ios" {
		t.Fatalf("VMess URI uTLS was not canonicalized: %#v", vmessTLS)
	}
	vlessTLS := nodes[1]["tls"].(map[string]any)
	if _, ok := vlessTLS["reality"].(map[string]any); !ok {
		t.Fatalf("valid VLESS Reality was dropped: %#v", vlessTLS)
	}
	if vlessTLS["utls"].(map[string]any)["fingerprint"] != "ios" {
		t.Fatalf("VLESS URI uTLS was not canonicalized: %#v", vlessTLS)
	}
	diagnostic, _ := json.Marshal(info)
	for _, secret := range []string{"secret.example", "secret-public-key", "secret-cipher", "secret-mode"} {
		if strings.Contains(string(diagnostic), secret) {
			t.Fatalf("Info leaked %q: %s", secret, diagnostic)
		}
	}
}

func TestURIFieldsAreExplicitlyConsumed(t *testing.T) {
	unknownVMessField := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "unknown-vmess", "add": "secret.example", "port": "443",
		"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws", "future-semantic": "secret-value",
	})
	conflictingVMessCipher := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "conflicting-vmess", "add": "secret.example", "port": "443",
		"id": "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "security": "none", "net": "ws",
	})
	raw := strings.Join([]string{
		"trojan://p@secret.example:443?security=tls&future-semantic=secret-value#unknown",
		"vless://cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=tls&type=ws&future-option=secret-value#transport",
		"vless://dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=tls&security=none#duplicate",
		"vless://eeeeeeee-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=none&sni=secret.example#disabled",
		"vmess://" + unknownVMessField,
		"vmess://" + conflictingVMessCipher,
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#valid",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 1 || info.Skipped != 6 || nodes[0]["tag"] != "valid" {
		t.Fatalf("URI fields were silently ignored: %#v %#v", info, nodes)
	}
	diagnostic, _ := json.Marshal(info)
	for _, secret := range []string{"secret.example", "secret-value", "unknown-vmess"} {
		if strings.Contains(string(diagnostic), secret) {
			t.Fatalf("Info leaked %q: %s", secret, diagnostic)
		}
	}
}

func TestURITransportOptionsAndBooleanAliasesAreUnambiguous(t *testing.T) {
	vmessTCPHost := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "tcp-host", "add": "secret.example", "port": "443",
		"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "tcp", "host": "secret-host.example", "tls": "tls",
	})
	vmessWSService := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "ws-service", "add": "secret.example", "port": "443",
		"id": "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws", "serviceName": "secret-service",
	})
	vmessBooleanConflict := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "boolean-conflict", "add": "secret.example", "port": "443",
		"id": "cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws", "tls": "tls",
		"allowInsecure": true, "skip-cert-verify": false,
	})
	raw := strings.Join([]string{
		"vless://dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=tls&type=tcp&path=%2Fsecret#tcp-path",
		"vless://eeeeeeee-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=tls&type=tcp&host=secret-host.example#tcp-host",
		"vless://ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=tls&type=ws&serviceName=secret-service#ws-service",
		"trojan://p@secret.example:443?allowInsecure=1&insecure=0#boolean-conflict",
		"tuic://11111111-bbbb-cccc-dddd-eeeeeeeeeeee:p@secret.example:443?insecure=1&allow_insecure=0#tuic-conflict",
		"vmess://" + vmessTCPHost,
		"vmess://" + vmessWSService,
		"vmess://" + vmessBooleanConflict,
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#valid",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 1 || info.Skipped != 8 || nodes[0]["tag"] != "valid" {
		t.Fatalf("ambiguous URI semantics were accepted: %#v %#v", info, nodes)
	}
	diagnostic, _ := json.Marshal(info)
	for _, secret := range []string{"secret.example", "secret-host", "secret-service"} {
		if strings.Contains(string(diagnostic), secret) {
			t.Fatalf("Info leaked %q: %s", secret, diagnostic)
		}
	}
}

func TestQuotedYAMLScalarsPreserveExactBytes(t *testing.T) {
	raw := `proxies:
  - name: "  exact tag  "
    type: vmess
    server: vmess.example
    port: 443
    uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
    alterId: 0
    cipher: auto
    network: ws
    ws-opts:
      path: "  /exact path  "
      headers:
        X-Exact: "  exact header  "
  - name: password
    type: ss
    server: 192.0.2.1
    port: 8388
    cipher: aes-128-gcm
    password: "  preserve me  "
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 2 || info.Skipped != 0 {
		t.Fatalf("exact scalar document failed: %#v", info)
	}
	if nodes[0]["tag"] != "  exact tag  " {
		t.Fatalf("tag bytes changed: %q", nodes[0]["tag"])
	}
	transport := nodes[0]["transport"].(map[string]any)
	if transport["path"] != "  /exact path  " {
		t.Fatalf("path bytes changed: %q", transport["path"])
	}
	headers := transport["headers"].(map[string]any)
	if headers["X-Exact"] != "  exact header  " {
		t.Fatalf("header bytes changed: %q", headers["X-Exact"])
	}
	if nodes[1]["password"] != "  preserve me  " {
		t.Fatalf("password bytes changed: %q", nodes[1]["password"])
	}
}

func TestStructuralWhitespaceAndControlCharactersAreRejected(t *testing.T) {
	raw := `proxies:
  - {name: server, type: ss, server: " 192.0.2.1 ", port: 8388, cipher: aes-128-gcm, password: p}
  - {name: port, type: ss, server: 192.0.2.1, port: " 8388 ", cipher: aes-128-gcm, password: p}
  - {name: cipher, type: ss, server: 192.0.2.1, port: 8388, cipher: " aes-128-gcm ", password: p}
  - {name: control, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: "line\nbreak"}
  - {name: valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if len(nodes) != 1 || nodes[0]["tag"] != "valid" || info.Skipped != 4 {
		t.Fatalf("invalid structural scalars were accepted: %#v %#v", info, nodes)
	}
}

func TestYAMLStringFieldsRejectImplicitTypeConfusion(t *testing.T) {
	raw := `proxies:
  - {name: true, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
  - {name: type, type: false, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
  - {name: server, type: ss, server: 192001, port: 8388, cipher: aes-128-gcm, password: p}
  - {name: cipher, type: ss, server: 192.0.2.1, port: 8388, cipher: true, password: p}
  - {name: password, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: 123}
  - {name: typed-valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p, udp: false}
  - {name: string-valid, type: ss, server: 192.0.2.1, port: "8388", cipher: aes-128-gcm, password: p, udp: "false"}
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 2 || info.Skipped != 5 {
		t.Fatalf("YAML type confusion was accepted: %#v %#v", info, nodes)
	}
	if nodes[0]["tag"] != "typed-valid" || nodes[1]["tag"] != "string-valid" {
		t.Fatalf("canonical typed scalars were rejected: %#v", nodes)
	}
}

func TestYAMLMergeKeysUseTagsAndRejectAmbiguity(t *testing.T) {
	t.Run("quoted merge key is ordinary and unsupported", func(t *testing.T) {
		raw := `defaults: &defaults {name: injected, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
proxies:
  - "<<": *defaults
`
		_, _, err := NormalizeDocument([]byte(raw))
		if !errors.Is(err, ErrNoValidNode) {
			t.Fatalf("quoted merge key injected a proxy: %v", err)
		}
	})

	t.Run("second true merge key is rejected", func(t *testing.T) {
		raw := `one: &one {type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
two: &two {type: ss, server: 192.0.2.2, port: 8388, cipher: aes-128-gcm, password: p}
proxies:
  - <<: *one
    <<: *two
    name: ambiguous
`
		_, _, err := NormalizeDocument([]byte(raw))
		requireErrorCode(t, err, "yaml_invalid")
	})

	t.Run("merge sequence keeps YAML first-wins precedence", func(t *testing.T) {
		raw := `one: &one {type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
two: &two {type: ss, server: 192.0.2.2, port: 8388, cipher: aes-128-gcm, password: p}
proxies:
  - <<: [*one, *two]
    name: merged
`
		nodes, _ := decodedOutbounds(t, []byte(raw))
		if nodes[0]["server"] != "192.0.2.1" {
			t.Fatalf("merge sequence precedence changed: %#v", nodes[0])
		}
	})

	t.Run("quoted merge alongside proxy cannot be ignored", func(t *testing.T) {
		raw := `defaults: &defaults {server: 192.0.2.2}
proxies:
  - {name: bad, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p, "<<": *defaults}
  - {name: valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
`
		nodes, info := decodedOutbounds(t, []byte(raw))
		if len(nodes) != 1 || nodes[0]["tag"] != "valid" || info.Skipped != 1 {
			t.Fatalf("quoted merge field was ignored: %#v %#v", info, nodes)
		}
	})
}

func TestRawYAMLDuplicateKeysAreRejectedEverywhereWithTagAwareIdentity(t *testing.T) {
	duplicate := `ignored:
  duplicate: one
  duplicate: two
proxies:
  - {name: valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
`
	_, _, err := NormalizeDocument([]byte(duplicate))
	requireErrorCode(t, err, "yaml_invalid")

	tagDistinct := `ignored:
  1: integer
  "1": string
proxies:
  - {name: valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
`
	nodes, info := decodedOutbounds(t, []byte(tagDistinct))
	if len(nodes) != 1 || info.Accepted != 1 {
		t.Fatalf("tag-distinct YAML keys were treated as duplicates: %#v", info)
	}

	complexIgnored := `ignored:
  ? [complex, key]
  : value
proxies:
  - {name: valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
`
	_, _, err = NormalizeDocument([]byte(complexIgnored))
	requireErrorCode(t, err, "yaml_invalid")
}

func TestNativeJSONRejectsDuplicateKeysAtEveryDepth(t *testing.T) {
	cases := []string{
		`{"outbounds":[{"type":"direct","tag":"a"}],"outbounds":[{"type":"direct","tag":"b"}]}`,
		`{"outbounds":[{"type":"direct","type":"wireguard","tag":"a"}]}`,
		`{"outbounds":[{"type":"wireguard","tag":"a","nested":{"secret":"one","secret":"two"}}]}`,
	}
	for index, raw := range cases {
		t.Run(fmt.Sprintf("duplicate-%d", index), func(t *testing.T) {
			_, _, err := NormalizeDocument([]byte(raw))
			requireErrorCode(t, err, "json_invalid")
			requireSafeDiagnostic(t, err.Error())
		})
	}
}

func TestNativeJSONScalarBoundsIncludeStringsAndNumbers(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value func(int) string
	}{
		{name: "string", value: func(size int) string {
			return `"` + strings.Repeat("x", size) + `"`
		}},
		{name: "number", value: func(size int) string {
			return "1" + strings.Repeat("0", size-1)
		}},
	} {
		t.Run(testCase.name+" equal", func(t *testing.T) {
			raw := `{"padding":` + testCase.value(64*1024) +
				`,"outbounds":[{"type":"direct","tag":"valid"}]}`
			if _, _, err := NormalizeDocument([]byte(raw)); err != nil {
				t.Fatalf("exact scalar limit rejected: %v", err)
			}
		})
		t.Run(testCase.name+" over", func(t *testing.T) {
			raw := `{"padding":` + testCase.value(64*1024+1) +
				`,"outbounds":[{"type":"direct","tag":"valid"}]}`
			_, _, err := NormalizeDocument([]byte(raw))
			requireErrorCode(t, err, "scalar_too_large")
		})
	}
}

func TestExplicitEmptyTagsArePreservedForMergeTimeNaming(t *testing.T) {
	native, _ := decodedOutbounds(t, []byte(
		`{"outbounds":[{"type":"direct","tag":""}]}`,
	))
	if tag, ok := native[0]["tag"].(string); !ok || tag != "" {
		t.Fatalf("native empty tag changed: %#v", native[0])
	}

	clash, _ := decodedOutbounds(t, []byte(
		"proxies:\n  - {name: \"\", type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}\n",
	))
	if tag, ok := clash[0]["tag"].(string); !ok || tag != "" {
		t.Fatalf("Clash empty name changed: %#v", clash[0])
	}

	uri, _ := decodedOutbounds(t, []byte(
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#\n",
	))
	if tag, ok := uri[0]["tag"].(string); !ok || tag != "" {
		t.Fatalf("URI empty fragment changed: %#v", uri[0])
	}

	vmess := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "", "add": "vmess.example", "port": "443",
		"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws",
	})
	vmessNodes, _ := decodedOutbounds(t, []byte("vmess://"+vmess+"\n"))
	if tag, ok := vmessNodes[0]["tag"].(string); !ok || tag != "" {
		t.Fatalf("VMess empty ps changed: %#v", vmessNodes[0])
	}

	missingNative, _ := decodedOutbounds(t, []byte(
		`{"outbounds":[{"type":"direct"}]}`,
	))
	if tag, ok := missingNative[0]["tag"].(string); !ok || tag != "" {
		t.Fatalf("missing native tag was not normalized to empty: %#v", missingNative[0])
	}

	noFragment, _ := decodedOutbounds(t, []byte(
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388\n",
	))
	if tag, ok := noFragment[0]["tag"].(string); !ok || tag != "" {
		t.Fatalf("missing URI fragment gained a fallback tag: %#v", noFragment[0])
	}

	spaceFragment, _ := decodedOutbounds(t, []byte(
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#%20%20\n",
	))
	if tag, ok := spaceFragment[0]["tag"].(string); !ok || tag != "  " {
		t.Fatalf("whitespace URI fragment changed: %#v", spaceFragment[0])
	}

	paddedFragment, _ := decodedOutbounds(t, []byte(
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#%20exact%20\n",
	))
	if tag, ok := paddedFragment[0]["tag"].(string); !ok || tag != " exact " {
		t.Fatalf("padded URI fragment changed: %#v", paddedFragment[0])
	}

	for _, testCase := range []struct {
		name   string
		fields map[string]any
		tag    string
	}{
		{
			name: "missing",
			fields: map[string]any{
				"v": "2", "add": "vmess.example", "port": "443",
				"id": "dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
				"scy": "auto", "net": "ws",
			},
			tag: "",
		},
		{
			name: "whitespace",
			fields: map[string]any{
				"v": "2", "ps": "  ", "add": "vmess.example", "port": "443",
				"id": "eeeeeeee-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
				"scy": "auto", "net": "ws",
			},
			tag: "  ",
		},
		{
			name: "padded",
			fields: map[string]any{
				"v": "2", "ps": "  exact  ", "add": "vmess.example", "port": "443",
				"id": "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
				"scy": "auto", "net": "ws",
			},
			tag: "  exact  ",
		},
	} {
		t.Run("VMess "+testCase.name, func(t *testing.T) {
			payload := encodeVMessForTest(testCase.fields)
			nodes, _ := decodedOutbounds(t, []byte("vmess://"+payload+"\n"))
			if tag, ok := nodes[0]["tag"].(string); !ok || tag != testCase.tag {
				t.Fatalf("VMess ps changed: %#v", nodes[0])
			}
		})
	}
}

func TestVMessEmbeddedJSONUsesStrictDocumentValidation(t *testing.T) {
	duplicate := `{"v":"2","ps":"duplicate","add":"vmess.example","port":"443","id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","id":"bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee","aid":"0","scy":"auto","net":"ws"}`
	deep := `{"v":"2","ps":"deep","add":"vmess.example","port":"443","id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","aid":"0","scy":"auto","net":"ws","extra":` +
		strings.Repeat("[", 65) + `"x"` + strings.Repeat("]", 65) + `}`
	trailing := `{"v":"2","ps":"trailing","add":"vmess.example","port":"443","id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","aid":"0","scy":"auto","net":"ws"}{"extra":true}`
	raw := strings.Join([]string{
		"vmess://" + base64.RawStdEncoding.EncodeToString([]byte(duplicate)),
		"vmess://" + base64.RawStdEncoding.EncodeToString([]byte(deep)),
		"vmess://" + base64.RawStdEncoding.EncodeToString([]byte(trailing)),
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#valid",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if len(nodes) != 1 || info.Skipped != 3 || nodes[0]["tag"] != "valid" {
		t.Fatalf("embedded VMess JSON was not strict: %#v %#v", info, nodes)
	}
}

func encodeVMessForTest(fields map[string]any) string {
	encoded, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return base64.RawStdEncoding.EncodeToString(encoded)
}
