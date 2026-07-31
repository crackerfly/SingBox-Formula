package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestURIRecordAndScalarLimitsAreInclusive(t *testing.T) {
	valid := "ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#valid"

	t.Run("raw records equal", func(t *testing.T) {
		raw := valid + "\n" + strings.Repeat("\n", MaxYAMLNodes-2) + "# end"
		nodes, info := decodedOutbounds(t, []byte(raw))
		if len(nodes) != 1 || info.Accepted != 1 {
			t.Fatalf("exact URI record limit rejected: %#v %#v", info, nodes)
		}
	})

	t.Run("raw records over", func(t *testing.T) {
		raw := valid + "\n" + strings.Repeat("\n", MaxYAMLNodes-1) + "# end"
		_, _, err := NormalizeDocument([]byte(raw))
		requireErrorCode(t, err, "too_many_document_nodes")
	})

	t.Run("CRLF is one record separator", func(t *testing.T) {
		raw := valid + strings.Repeat("\r\n", MaxYAMLNodes-1) + "# end"
		nodes, info := decodedOutbounds(t, []byte(raw))
		if len(nodes) != 1 || info.Accepted != 1 {
			t.Fatalf("CRLF was counted as two separators: %#v %#v", info, nodes)
		}
	})

	t.Run("standalone CR is a record separator", func(t *testing.T) {
		raw := valid + strings.Repeat("\r", MaxYAMLNodes) + "# end"
		_, _, err := NormalizeDocument([]byte(raw))
		requireErrorCode(t, err, "too_many_document_nodes")
	})

	t.Run("raw scalar equal", func(t *testing.T) {
		raw := valid + "\n#" + strings.Repeat("x", MaxScalarBytes-1)
		if _, _, err := NormalizeDocument([]byte(raw)); err != nil {
			t.Fatalf("exact URI scalar limit rejected: %v", err)
		}
	})

	t.Run("raw scalar over", func(t *testing.T) {
		raw := valid + "\n#" + strings.Repeat("x", MaxScalarBytes)
		_, _, err := NormalizeDocument([]byte(raw))
		requireErrorCode(t, err, "scalar_too_large")
	})
}

func TestManyInvalidURIRecordsStayBoundedAndKeepExactCounts(t *testing.T) {
	var raw strings.Builder
	raw.Grow((MaxYAMLNodes - 1) * 18)
	for index := 0; index < MaxYAMLNodes-2; index++ {
		raw.WriteString("future://invalid\n")
	}
	raw.WriteString("ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#valid")

	nodes, info := decodedOutbounds(t, []byte(raw.String()))
	if len(nodes) != 1 || info.Accepted != 1 || info.Skipped != MaxYAMLNodes-2 {
		t.Fatalf("many invalid URI records changed counts: %#v %#v", info, nodes)
	}
	if len(info.Warnings) != MaxWarningSamples {
		t.Fatalf("warning samples were not bounded: %#v", info.Warnings)
	}
}

func TestYAMLLegacyOctalIntegersAreNotReinterpretedAsDecimal(t *testing.T) {
	raw := `proxies:
  - {name: bad-port, type: ss, server: 192.0.2.1, port: 0123, cipher: aes-128-gcm, password: p}
  - {name: bad-alter, type: vmess, server: vmess.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: 0123, cipher: auto}
  - {name: quoted-port, type: ss, server: 192.0.2.2, port: "0123", cipher: aes-128-gcm, password: p}
  - {name: quoted-alter, type: vmess, server: vmess.example, port: 443, uuid: bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee, alterId: "0123", cipher: auto}
`
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 2 || info.Skipped != 2 {
		t.Fatalf("legacy octal integers were accepted: %#v %#v", info, nodes)
	}
	if nodes[0]["server_port"] != json.Number("123") ||
		nodes[1]["alter_id"] != json.Number("123") {
		t.Fatalf("explicit decimal strings changed: %#v", nodes)
	}
}

func TestURITransportFieldsAreLossless(t *testing.T) {
	vmessGRPCDual := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "grpc-dual", "add": "secret.example", "port": "443",
		"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "grpc", "serviceName": "svc", "path": "/svc",
	})
	vmessWSQuery := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "ws-query", "add": "secret.example", "port": "443",
		"id": "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws", "path": "/ws?foo=bar",
	})
	vmessEncodedQuery := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "ws-encoded-query", "add": "secret.example", "port": "443",
		"id": "99999999-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws", "path": "/ws%3Fed=2048",
	})
	vmessTripleEncodedQuery := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "ws-triple-encoded-query", "add": "secret.example", "port": "443",
		"id": "88888888-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws", "path": "/ws%25253Fed=2048",
	})
	validVMess := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "valid-vmess", "add": "vmess.example", "port": "443",
		"id": "cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws", "path": "/ws?ed=2048",
	})
	raw := strings.Join([]string{
		"vless://dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=none&type=grpc&serviceName=svc&path=%2Fsvc#grpc-dual",
		"vless://eeeeeeee-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=none&type=ws&path=%2Fws%3Ffoo%3Dbar#ws-query",
		"vless://ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=none&type=ws&path=%2Fws%3Fed%3D0#ws-zero",
		"vless://11111111-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=none&type=ws&path=%2Fws%3Fed%3Dabc#ws-invalid",
		"vless://22222222-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=none&type=ws&path=%2Fws%3Fed%3D1%26ed%3D2#ws-multiple",
		"vless://33333333-bbbb-cccc-dddd-eeeeeeeeeeee@secret.example:443?security=none&type=ws&path=%252Fws%253Fed%253D2048#ws-double-encoded",
		"vmess://" + vmessGRPCDual,
		"vmess://" + vmessWSQuery,
		"vmess://" + vmessEncodedQuery,
		"vmess://" + vmessTripleEncodedQuery,
		"vless://44444444-bbbb-cccc-dddd-eeeeeeeeeeee@vless.example:443?security=none&type=ws&path=%2Fws%3Fed%3D2048#valid-uri",
		"vmess://" + validVMess,
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 2 || info.Skipped != 10 {
		t.Fatalf("lossy URI transports were accepted: %#v %#v", info, nodes)
	}
	for _, node := range nodes {
		transport := node["transport"].(map[string]any)
		if transport["path"] != "/ws" ||
			transport["max_early_data"] != json.Number("2048") {
			t.Fatalf("confirmed early data mapping changed: %#v", transport)
		}
	}
}

func TestHysteria2AlternatePortsFailClosed(t *testing.T) {
	clashRaw := `proxies:
  - {name: bad, type: hy2, server: hy2.example, port: 443, ports: "8443", password: p}
  - {name: valid, type: hy2, server: hy2.example, port: 443, password: p}
`
	clash, clashInfo := decodedOutbounds(t, []byte(clashRaw))
	if clashInfo.Accepted != 1 || clashInfo.Skipped != 1 || clash[0]["tag"] != "valid" {
		t.Fatalf("Clash Hy2 alternate port was accepted: %#v %#v", clashInfo, clash)
	}
	if _, exists := clash[0]["server_ports"]; exists {
		t.Fatalf("valid single-endpoint Hy2 gained server_ports: %#v", clash[0])
	}

	uriRaw := strings.Join([]string{
		"hy2://p@secret.example:443?ports=8443#ports",
		"hy2://p@secret.example:443?mport=8443#mport",
		"hy2://p@hy2.example:443#valid",
	}, "\n")
	uri, uriInfo := decodedOutbounds(t, []byte(uriRaw))
	if uriInfo.Accepted != 1 || uriInfo.Skipped != 2 || uri[0]["tag"] != "valid" {
		t.Fatalf("URI Hy2 alternate port was accepted: %#v %#v", uriInfo, uri)
	}
	if _, exists := uri[0]["server_ports"]; exists {
		t.Fatalf("valid URI Hy2 gained server_ports: %#v", uri[0])
	}
}

func TestURITagsRejectLossyOrControlBearingSources(t *testing.T) {
	invalidVMessJSON := append([]byte(
		`{"v":"2","ps":"invalid-`,
	), byte(0xff))
	invalidVMessJSON = append(invalidVMessJSON, []byte(
		`","add":"secret.example","port":"443","id":"dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee","aid":"0","scy":"auto","net":"ws"}`,
	)...)
	invalidVMessUTF8 := base64.RawStdEncoding.EncodeToString(invalidVMessJSON)
	vmessControl := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "\u0000", "add": "secret.example", "port": "443",
		"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws",
	})
	vmessOuterFragment := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "inner", "add": "secret.example", "port": "443",
		"id": "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws",
	})
	vmessEmptyOuterFragment := encodeVMessForTest(map[string]any{
		"v": "2", "ps": "kept", "add": "vmess.example", "port": "443",
		"id": "cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
		"scy": "auto", "net": "ws",
	})
	raw := strings.Join([]string{
		"ss://YWVzLTEyOC1nY206cA@secret.example:8388#%00",
		"ss://YWVzLTEyOC1nY206cA@secret.example:8388#%FF",
		"vmess://" + invalidVMessUTF8,
		"vmess://" + vmessControl,
		"vmess://" + vmessOuterFragment + "#ignored",
		"vmess://" + vmessEmptyOuterFragment + "#",
		"ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388#valid",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 2 || info.Skipped != 5 {
		t.Fatalf("lossy/control-bearing URI tags were accepted: %#v %#v", info, nodes)
	}
	if nodes[0]["tag"] != "kept" || nodes[1]["tag"] != "valid" {
		t.Fatalf("valid exact tags changed: %#v", nodes)
	}
}

func TestTUICEnumsAreExplicit(t *testing.T) {
	clashRaw := `proxies:
  - {name: bad-congestion, type: tuic, server: tuic.example, port: 443, uuid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, congestion-controller: reno}
  - {name: bad-relay, type: tuic, server: tuic.example, port: 443, uuid: bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, udp-relay-mode: udp}
  - {name: bad-alias, type: tuic, server: tuic.example, port: 443, uuid: cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, congestion-controller: cubic, congestion_control: bbr}
  - {name: cubic, type: tuic, server: tuic.example, port: 443, uuid: dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, congestion-controller: cubic, udp-relay-mode: native}
  - {name: reno, type: tuic, server: tuic.example, port: 443, uuid: eeeeeeee-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, congestion-controller: new_reno, udp-relay-mode: quic}
  - {name: bbr, type: tuic, server: tuic.example, port: 443, uuid: ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee, password: p, congestion-controller: bbr}
`
	clash, clashInfo := decodedOutbounds(t, []byte(clashRaw))
	if clashInfo.Accepted != 3 || clashInfo.Skipped != 3 {
		t.Fatalf("Clash TUIC enums were not enforced: %#v %#v", clashInfo, clash)
	}

	uriRaw := strings.Join([]string{
		"tuic://11111111-bbbb-cccc-dddd-eeeeeeeeeeee:p@secret.example:443?congestion_control=reno#bad-congestion",
		"tuic://22222222-bbbb-cccc-dddd-eeeeeeeeeeee:p@secret.example:443?udp_relay_mode=udp#bad-relay",
		"tuic://33333333-bbbb-cccc-dddd-eeeeeeeeeeee:p@tuic.example:443?congestion_control=cubic&udp_relay_mode=quic#valid",
		"tuic://44444444-bbbb-cccc-dddd-eeeeeeeeeeee:p@tuic.example:443?congestion_control=new_reno&udp_relay_mode=native#reno",
		"tuic://55555555-bbbb-cccc-dddd-eeeeeeeeeeee:p@tuic.example:443#default",
	}, "\n")
	uri, uriInfo := decodedOutbounds(t, []byte(uriRaw))
	if uriInfo.Accepted != 3 || uriInfo.Skipped != 2 {
		t.Fatalf("URI TUIC enums were not enforced: %#v %#v", uriInfo, uri)
	}
}
