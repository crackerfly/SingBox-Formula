package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLiteralURIFragmentsAreDistinctFromRecordFraming(t *testing.T) {
	uri := "ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388"
	raw := strings.Join([]string{
		" \t" + uri + " \t",
		uri + "#  ",
		uri + "# exact ",
		uri + "#\t",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 3 || info.Skipped != 1 {
		t.Fatalf("literal fragments were treated as framing: %#v %#v", info, nodes)
	}
	for index, want := range []string{"", "  ", " exact "} {
		if got := nodes[index]["tag"]; got != want {
			t.Fatalf("node %d tag = %#v, want %#v", index, got, want)
		}
	}
}

func TestLegacyWholeBase64SSTagsRemainExact(t *testing.T) {
	legacy := func(payload []byte) string {
		return "ss://" + base64.RawStdEncoding.EncodeToString(payload)
	}
	invalidUTF8 := append(
		[]byte("aes-128-gcm:p@192.0.2.5:8388#bad-"),
		byte(0xff),
	)
	raw := strings.Join([]string{
		legacy([]byte("aes-128-gcm:p@192.0.2.1:8388#inner")),
		legacy([]byte("aes-128-gcm:p@192.0.2.2:8388#")),
		legacy([]byte("aes-128-gcm:p@192.0.2.3:8388")),
		legacy([]byte("aes-128-gcm:p@192.0.2.4:8388#%20inner%20")),
		legacy(invalidUTF8),
		legacy([]byte("aes-128-gcm:p@192.0.2.6:8388#bad\t")),
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 4 || info.Skipped != 2 {
		t.Fatalf("legacy SS tags were lossy: %#v %#v", info, nodes)
	}
	for index, want := range []string{"inner", "", "", " inner "} {
		if got := nodes[index]["tag"]; got != want {
			t.Fatalf("node %d legacy tag = %#v, want %#v", index, got, want)
		}
	}
}

func TestParsedURIStringsMustRemainValidUTF8(t *testing.T) {
	invalidSSUserinfo := append([]byte("aes-128-gcm:p"), byte(0xff))
	raw := strings.Join([]string{
		"ss://" + base64.RawStdEncoding.EncodeToString(invalidSSUserinfo) +
			"@192.0.2.1:8388#invalid-ss",
		"vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@192.0.2.2:443?security=none&type=ws&path=%FF#invalid-path",
		"trojan://p%FF@192.0.2.3:443#invalid-password",
		"ss://YWVzLTEyOC1nY206cA@192.0.2.4:8388#valid",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 1 || info.Skipped != 3 ||
		len(nodes) != 1 || nodes[0]["tag"] != "valid" {
		t.Fatalf("invalid UTF-8 URI fields were accepted: %#v %#v", info, nodes)
	}
}

func TestURIUserinfoShapesFailClosed(t *testing.T) {
	raw := strings.Join([]string{
		"vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:discarded@192.0.2.1:443?security=none#bad-vless",
		"vless://bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee:@192.0.2.2:443?security=none#bad-vless-empty",
		"trojan://kept:discarded@192.0.2.3:443#bad-trojan",
		"trojan://kept:@192.0.2.4:443#bad-trojan-empty",
		"anytls://discarded:kept@192.0.2.5:443#bad-anytls",
		"anytls://kept:@192.0.2.6:443#bad-anytls-empty",
		"vless://cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee@192.0.2.7:443?security=none#vless",
		"trojan://kept%3Aencoded@192.0.2.8:443#trojan",
		"anytls://kept%3Aencoded@192.0.2.9:443#anytls",
		"hy2://user:pass@192.0.2.10:443#hy2",
		"tuic://dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee:p@192.0.2.11:443#tuic",
		"socks5://user:p@192.0.2.12:1080#socks",
		"ss://aes-128-gcm:p@192.0.2.13:8388#ss",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 7 || info.Skipped != 6 {
		t.Fatalf("userinfo shapes were not enforced: %#v %#v", info, nodes)
	}
	if nodes[1]["password"] != "kept:encoded" ||
		nodes[2]["password"] != "kept:encoded" ||
		nodes[3]["password"] != "user:pass" {
		t.Fatalf("intended credential forms changed: %#v", nodes)
	}
}

func TestWhitespaceReferencesNeverDisappear(t *testing.T) {
	t.Run("native", func(t *testing.T) {
		raw := `{"outbounds":[{"type":"direct","tag":" ","options":{"detour":" "}}]}`
		_, _, err := NormalizeDocument([]byte(raw))
		requireErrorCode(t, err, "native_reference_unsupported")

		raw = `{"outbounds":[{"type":"direct","tag":" ","options":{"detour":""}}]}`
		nodes, _ := decodedOutbounds(t, []byte(raw))
		if nodes[0]["tag"] != " " {
			t.Fatalf("whitespace native tag changed: %#v", nodes[0])
		}
	})

	t.Run("URI", func(t *testing.T) {
		raw := strings.Join([]string{
			"vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@192.0.2.1:443?security=none&dialer-proxy=%20#bad-kebab",
			"vless://bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee@192.0.2.2:443?security=none&dialerProxy=%20#bad-camel",
			"vless://cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee@192.0.2.3:443?security=none&dialer-proxy=#",
			"ss://YWVzLTEyOC1nY206cA@192.0.2.4:8388#%20",
		}, "\n")
		nodes, info := decodedOutbounds(t, []byte(raw))
		if info.Accepted != 2 || info.Skipped != 2 {
			t.Fatalf("whitespace URI references disappeared: %#v %#v", info, nodes)
		}
		if nodes[1]["tag"] != " " {
			t.Fatalf("whitespace URI tag changed: %#v", nodes[1])
		}
	})

	t.Run("VMess", func(t *testing.T) {
		base := map[string]any{
			"v": "2", "ps": "valid", "add": "192.0.2.1", "port": "443",
			"id": "dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee", "aid": "0",
			"scy": "auto", "net": "ws",
		}
		withField := func(field, value string) string {
			copyFields := make(map[string]any, len(base)+1)
			for key, fieldValue := range base {
				copyFields[key] = fieldValue
			}
			copyFields[field] = value
			return "vmess://" + encodeVMessForTest(copyFields)
		}
		raw := strings.Join([]string{
			withField("dialer-proxy", " "),
			withField("dialerProxy", " "),
			withField("dialer-proxy", ""),
			"vmess://" + encodeVMessForTest(base),
		}, "\n")
		nodes, info := decodedOutbounds(t, []byte(raw))
		if info.Accepted != 2 || info.Skipped != 2 {
			t.Fatalf("whitespace VMess references disappeared: %#v %#v", info, nodes)
		}
	})
}

func TestNativeNonStringTagReportsTagField(t *testing.T) {
	_, _, err := NormalizeDocument([]byte(
		`{"outbounds":[{"type":"direct","tag":null}]}`,
	))
	var normalizeErr *NormalizeError
	if !errors.As(err, &normalizeErr) {
		t.Fatalf("error type = %T, want *NormalizeError", err)
	}
	if normalizeErr.Code != "native_outbound_invalid" ||
		normalizeErr.NodeIndex != 1 ||
		normalizeErr.Type != "direct" ||
		normalizeErr.Field != "tag" ||
		!strings.Contains(err.Error(), "field=tag") {
		t.Fatalf("wrong native tag diagnostic: %#v %v", normalizeErr, err)
	}
}

func TestBase64PayloadSelectionIsDeterministicAndBounded(t *testing.T) {
	payload := []byte("😀")
	variants := map[string]string{
		"standard padded": base64.StdEncoding.EncodeToString(payload),
		"standard raw":    base64.RawStdEncoding.EncodeToString(payload),
		"URL padded":      base64.URLEncoding.EncodeToString(payload),
		"URL raw":         base64.RawURLEncoding.EncodeToString(payload),
	}
	for name, encoded := range variants {
		t.Run(name, func(t *testing.T) {
			withWhitespace := " \t" + encoded[:2] + " \r\n" + encoded[2:] + "\t"
			decoded, ok := decodeBase64Payload([]byte(withWhitespace))
			if !ok || !bytes.Equal(decoded, payload) {
				t.Fatalf("decode = %q, %v", decoded, ok)
			}
		})
	}

	for _, invalid := range []string{
		"8J+YgA-_",
		"8J+YgA=",
		"8J=YgA==",
		"8J+YgA===",
		"A",
		"AB",
	} {
		if decoded, ok := decodeBase64Payload([]byte(invalid)); ok {
			t.Fatalf("invalid Base64 %q decoded as %x", invalid, decoded)
		}
	}

	largeInvalid := append(bytes.Repeat([]byte{'A'}, 256*1024), ':')
	var decoded []byte
	var ok bool
	if allocs := testing.AllocsPerRun(10, func() {
		decoded, ok = decodeBase64Payload(largeInvalid)
	}); allocs != 0 {
		t.Fatalf("lexically invalid Base64 allocations = %v, want 0", allocs)
	}
	if ok || decoded != nil {
		t.Fatalf("large invalid Base64 decoded: %t %x", ok, decoded)
	}

	invalidUTF8 := base64.StdEncoding.EncodeToString(
		bytes.Repeat([]byte{0xff}, 256*1024),
	)
	invalidUTF8Bytes := []byte(invalidUTF8)
	if allocs := testing.AllocsPerRun(5, func() {
		decoded, ok = decodeBase64Payload(invalidUTF8Bytes)
	}); allocs > 1 {
		t.Fatalf("valid Base64 invalid UTF-8 allocations = %v, want <= 1", allocs)
	}
	if ok || decoded != nil {
		t.Fatalf("invalid UTF-8 Base64 decoded: %t %x", ok, decoded)
	}
}

func TestYAMLLexicalIndicatorBudgetIsInclusive(t *testing.T) {
	const maxIndicators = MaxYAMLNodes
	base := []byte(validProxyYAML + "#")
	baseIndicators := countYAMLIndicatorsForTest(base)
	if baseIndicators >= maxIndicators {
		t.Fatalf("base fixture uses %d indicators", baseIndicators)
	}
	atLimit := append(
		append([]byte(nil), base...),
		bytes.Repeat([]byte{'['}, maxIndicators-baseIndicators)...,
	)
	if got := countYAMLIndicatorsForTest(atLimit); got != maxIndicators {
		t.Fatalf("exact fixture indicators = %d", got)
	}
	if _, _, err := NormalizeDocument(atLimit); err != nil {
		t.Fatalf("exact YAML lexical budget rejected: %v", err)
	}

	overLimit := append(append([]byte(nil), atLimit...), '[')
	_, _, err := NormalizeDocument(overLimit)
	requireErrorCode(t, err, "too_many_document_nodes")
}

func TestYAMLLexicalBudgetPreservesScalarStyles(t *testing.T) {
	raw := validProxyYAML + `
quoted: "[]{}#&*!|>'%@` + "`" + `"
literal: |
  []{}#&*!|>'"%@` + "`" + `
folded: >
  []{}#&*!|>'"%@` + "`" + `
`
	if _, _, err := NormalizeDocument([]byte(raw)); err != nil {
		t.Fatalf("quoted/block scalar styles rejected: %v", err)
	}
}

func TestYAMLIndicatorBoundDominatesRawAST(t *testing.T) {
	documents := map[string]string{
		"plain scalar":     "plain",
		"block sequence":   "-\n- value\n",
		"explicit mapping": "? key\n: value\n",
		"flow collections": "[{}, [], null, value]\n",
		"nested block":     "root:\n  - child:\n      key: value\n",
		"anchor alias":     "base: &base [x]\ncopy: *base\n",
		"quoted block":     "quoted: \"[]\"\nblock: |\n  {}[]\n",
		"valid proxy":      validProxyYAML,
	}
	for name, raw := range documents {
		t.Run(name, func(t *testing.T) {
			indicators := countYAMLIndicatorsForTest([]byte(raw))
			nodes := countRawNodesForTest(t, raw)
			if nodes > 2+3*indicators {
				t.Fatalf("raw nodes %d exceed 2 + 3*%d", nodes, indicators)
			}
		})
	}
}

func countYAMLIndicatorsForTest(raw []byte) int {
	count := 0
	lineStart := true
	for index := 0; index < len(raw); index++ {
		character := raw[index]
		if lineStart && index+3 <= len(raw) &&
			(string(raw[index:index+3]) == "---" ||
				string(raw[index:index+3]) == "...") &&
			(index+3 == len(raw) || isYAMLBlankOrBreakForTest(raw[index+3])) {
			count += 3
			index += 2
			lineStart = false
			continue
		}
		switch character {
		case '?', ':', ',', '[', ']', '{', '}', '#', '&', '*', '!',
			'|', '>', '\'', '"', '%', '@', '`':
			count++
		case '-':
			if index+1 == len(raw) || isYAMLBlankOrBreakForTest(raw[index+1]) {
				count++
			}
		}
		lineStart = character == '\n' || character == '\r'
	}
	return count
}

func isYAMLBlankOrBreakForTest(character byte) bool {
	return character == ' ' || character == '\t' ||
		character == '\r' || character == '\n'
}

func TestRecursiveUTF8TreeValidationIncludesKeys(t *testing.T) {
	invalidKey := string([]byte{'k', 0xff})
	invalidValue := string([]byte{'v', 0xff})
	value := map[string]any{
		"nested": []any{
			map[string]any{invalidKey: "value"},
			[]string{"valid", invalidValue},
		},
	}
	if validUTF8Tree(value) {
		encoded, _ := json.Marshal(value)
		t.Fatalf("invalid UTF-8 tree accepted and would marshal as %s", encoded)
	}
}

func TestJSONLoneSurrogatesFailClosed(t *testing.T) {
	t.Run("native value", func(t *testing.T) {
		raw := `{"outbounds":[{"type":"direct","tag":"\ud800"}]}`
		_, _, err := NormalizeDocument([]byte(raw))
		requireErrorCode(t, err, "json_invalid")
	})

	t.Run("native key", func(t *testing.T) {
		raw := `{"outbounds":[{"type":"direct","tag":"valid","nested":{"\udfff":true}}]}`
		_, _, err := NormalizeDocument([]byte(raw))
		requireErrorCode(t, err, "json_invalid")
	})

	t.Run("valid pair", func(t *testing.T) {
		raw := `{"outbounds":[{"type":"direct","tag":"\ud83d\ude00"}]}`
		nodes, _ := decodedOutbounds(t, []byte(raw))
		if nodes[0]["tag"] != "😀" {
			t.Fatalf("valid surrogate pair changed: %#v", nodes[0])
		}
	})

	t.Run("VMess", func(t *testing.T) {
		rawVMess := `{"v":"2","ps":"\ud800","add":"192.0.2.1","port":"443","id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","aid":"0","scy":"auto","net":"ws"}`
		raw := "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(rawVMess)) +
			"\nss://YWVzLTEyOC1nY206cA@192.0.2.2:8388#valid"
		nodes, info := decodedOutbounds(t, []byte(raw))
		if info.Accepted != 1 || info.Skipped != 1 ||
			len(nodes) != 1 || nodes[0]["tag"] != "valid" {
			t.Fatalf("VMess lone surrogate accepted: %#v %#v", info, nodes)
		}
	})
}
