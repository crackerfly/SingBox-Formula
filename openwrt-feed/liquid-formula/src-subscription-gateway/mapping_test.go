package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBase64WrappedNativeJSONCompatibility(t *testing.T) {
	nodes, info := decodedOutbounds(t, fixture(t, "base64-singbox.txt"))
	if info.Format != FormatSingBoxJSON || info.Accepted != 2 {
		t.Fatalf("unexpected compatibility result: %#v", info)
	}
	if nodes[0]["type"] != "wireguard" || nodes[0]["private_key"] != "fixture-native-private-key" {
		t.Fatalf("base64-wrapped native JSON changed semantics: %#v", nodes[0])
	}
}

func TestNativeJSONRejectsReferencesAndTrailingDocuments(t *testing.T) {
	inputs := []string{
		`{"outbounds":[{"type":"shadowsocks","tag":"a","server":"192.0.2.1","server_port":8388,"method":"aes-128-gcm","password":"p","detour":"secret-reference"}]}`,
		`{"outbounds":[{"type":"selector","tag":"a","outbounds":["secret-reference"]}]}`,
		`{"outbounds":[{"type":"future-selector","tag":"a","outbounds":["secret-reference"]}]}`,
		`{"outbounds":[{"type":"direct","tag":"a"}]} {"outbounds":[{"type":"direct","tag":"b"}]}`,
	}
	for _, raw := range inputs {
		_, _, err := NormalizeDocument([]byte(raw))
		if err == nil {
			t.Fatal("unsafe native document unexpectedly succeeded")
		}
		if strings.Contains(err.Error(), "secret-reference") {
			t.Fatalf("native diagnostic leaked a reference: %v", err)
		}
	}
}

func TestClashMapsEverySupportedProtocolAndConfirmedTransport(t *testing.T) {
	raw := `proxies:
  - {name: SS, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
  - name: VMess HTTP
    type: vmess
    server: vmess.example
    port: 443
    uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
    alterId: 0
    cipher: auto
    tls: true
    sni: vmess.example
    network: http
    http-opts:
      method: GET
      path: [/tunnel]
      headers: {Host: cdn.example, X-Test: yes}
  - name: VLESS Reality
    type: vless
    server: vless.example
    port: 443
    uuid: bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee
    flow: xtls-rprx-vision
    tls: true
    client-fingerprint: chrome
    reality-opts: {public-key: fixture-public-key, short-id: ab12}
  - {name: Trojan, type: trojan, server: trojan.example, port: 443, password: p, sni: trojan.example}
  - {name: Hy2, type: hy2, server: hy2.example, port: 443, password: p, sni: hy2.example, obfs: salamander, obfs-password: ob}
  - {name: TUIC, type: tuic, server: tuic.example, port: 443, uuid: cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, sni: tuic.example, alpn: [h3]}
  - {name: AnyTLS, type: anytls, server: anytls.example, port: 443, password: p, sni: anytls.example}
  - {name: SOCKS, type: socks5, server: socks.example, port: 1080, username: u, password: p}
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 8 || info.Skipped != 0 {
		t.Fatalf("unexpected protocol matrix result: %#v", info)
	}
	gotTypes := make([]any, 0, len(nodes))
	for _, node := range nodes {
		gotTypes = append(gotTypes, node["type"])
	}
	wantTypes := []any{"shadowsocks", "vmess", "vless", "trojan", "hysteria2", "tuic", "anytls", "socks"}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("types = %#v, want %#v", gotTypes, wantTypes)
	}
	httpTransport := nodes[1]["transport"].(map[string]any)
	if httpTransport["type"] != "http" || httpTransport["method"] != "GET" ||
		httpTransport["path"] != "/tunnel" {
		t.Fatalf("HTTP transport lost confirmed fields: %#v", httpTransport)
	}
	reality := nodes[2]["tls"].(map[string]any)["reality"].(map[string]any)
	if reality["public_key"] != "fixture-public-key" || reality["short_id"] != "ab12" {
		t.Fatalf("Reality mapping changed: %#v", reality)
	}
}

func TestDangerousAliasFieldsCannotHideBehindEmptyFirstField(t *testing.T) {
	raw := `proxies:
  - {name: plugin, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p, plugin: "", plugin-opts: {mode: unsafe}}
  - {name: tls, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, tls: true, fingerprint: "", certificate: secret-certificate}
  - {name: hy2, type: hy2, server: hy2.example, port: 443, password: p, realm: "", hop-interval: 10s}
  - {name: valid, type: ss, server: 192.0.2.2, port: 8388, cipher: aes-128-gcm, password: p}
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if len(nodes) != 1 || info.Skipped != 3 || nodes[0]["tag"] != "valid" {
		t.Fatalf("dangerous secondary fields were ignored: %#v %#v", info, nodes)
	}
}

func TestTLSRequiredProtocolsRejectExplicitDisableAndUnknownFingerprint(t *testing.T) {
	raw := `proxies:
  - {name: trojan, type: trojan, server: trojan.example, port: 443, password: p, tls: false}
  - {name: hy2, type: hy2, server: hy2.example, port: 443, password: p, tls: false}
  - {name: tuic, type: tuic, server: tuic.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, tls: false}
  - {name: anytls, type: anytls, server: anytls.example, port: 443, password: p, tls: false}
  - {name: fingerprint, type: vmess, server: vmess.example, port: 443, uuid: bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0, cipher: auto, tls: true, client-fingerprint: private-token-fingerprint}
  - {name: valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if len(nodes) != 1 || info.Skipped != 5 {
		t.Fatalf("unsafe TLS choices were accepted: %#v %#v", info, nodes)
	}
	encoded, _ := json.Marshal(info)
	if strings.Contains(string(encoded), "private-token-fingerprint") {
		t.Fatalf("fingerprint leaked through Info: %s", encoded)
	}
}

func TestWarningSamplesAreCappedAtEightButSkippedCountIsExact(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("proxies:\n")
	for index := 0; index < 12; index++ {
		raw.WriteString("  - {name: invalid, type: ssr, server: secret.example, port: 443}\n")
	}
	raw.WriteString("  - {name: valid, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}\n")
	_, info := decodedOutbounds(t, []byte(raw.String()))
	if info.Skipped != 12 || len(info.Warnings) != 8 {
		t.Fatalf("unexpected warning bound: %#v", info)
	}
}

func TestURIRiskChecksDoNotForwardParserPreviewsOrFingerprints(t *testing.T) {
	raw := strings.Join([]string{
		"ss://YWVzLTI1Ni1nY206c2VjcmV0@secret.example:not-a-port#secret-tag",
		"trojan://secret-password@secret.example:443?security=tls&fp=private-token-fingerprint#secret-tag",
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#valid",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if len(nodes) != 1 || info.Skipped != 2 {
		t.Fatalf("unsafe URIs were accepted: %#v %#v", info, nodes)
	}
	diagnostic, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret.example", "secret-password", "secret-tag", "private-token-fingerprint", "://"} {
		if strings.Contains(string(diagnostic), secret) {
			t.Fatalf("Info leaked %q: %s", secret, diagnostic)
		}
	}
}

func TestMalformedURIQueriesAndVMessNumericFieldsFailClosed(t *testing.T) {
	invalidVMess := base64.RawStdEncoding.EncodeToString([]byte(
		`{"v":"2","ps":"secret-tag","add":"secret.example","port":"443","id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","aid":"not-a-number","scy":"auto","net":"ws"}`,
	))
	raw := strings.Join([]string{
		"ss://YWVzLTEyOC1nY206cA@192.0.2.2:8388?plugin=%ZZ#query-secret",
		"vmess://" + invalidVMess,
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#valid",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if len(nodes) != 1 || info.Skipped != 2 || nodes[0]["tag"] != "valid" {
		t.Fatalf("malformed fields were silently defaulted: %#v %#v", info, nodes)
	}
	encoded, _ := json.Marshal(info)
	for _, secret := range []string{"query-secret", "secret.example", "secret-tag", "not-a-number"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("Info leaked %q: %s", secret, encoded)
		}
	}
}

func TestURIWrapperRetainsAllSupportedProtocolFamilies(t *testing.T) {
	vmessPayload := base64.RawStdEncoding.EncodeToString([]byte(
		`{"v":"2","ps":"VMess","add":"vmess.example","port":"443","id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","aid":"0","scy":"auto","net":"ws","path":"/ws","tls":"tls","sni":"vmess.example","fp":"chrome"}`,
	))
	raw := strings.Join([]string{
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#SS",
		"vmess://" + vmessPayload,
		"vless://bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee@vless.example:443?security=reality&sni=vless.example&fp=chrome&pbk=fixture-public-key&sid=ab12&type=grpc&serviceName=svc#VLESS",
		"trojan://p@trojan.example:443?security=tls&sni=trojan.example&type=ws&path=%2Fws#Trojan",
		"hy2://p@hy2.example:443?sni=hy2.example&obfs=salamander&obfs-password=ob#Hy2",
		"tuic://cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee:p@tuic.example:443?sni=tuic.example&alpn=h3#TUIC",
		"anytls://p@anytls.example:443?sni=anytls.example#AnyTLS",
		"socks5://u:p@socks.example:1080#SOCKS",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 8 || info.Skipped != 0 {
		t.Fatalf("unexpected URI protocol result: %#v", info)
	}
	got := make([]any, 0, len(nodes))
	for _, node := range nodes {
		got = append(got, node["type"])
	}
	want := []any{"shadowsocks", "vmess", "vless", "trojan", "hysteria2", "tuic", "anytls", "socks"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("types = %#v, want %#v", got, want)
	}
}

func TestProviderOnlyErrorRetainsErrorsIsContract(t *testing.T) {
	_, _, err := NormalizeDocument(fixture(t, "clash-provider-only.yaml"))
	if !errors.Is(err, ErrNoValidNode) {
		t.Fatalf("errors.Is contract lost: %v", err)
	}
}
