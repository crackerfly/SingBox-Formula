package main

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var supportedSchemes = map[string]bool{
	"ss": true, "vmess": true, "vless": true, "trojan": true,
	"hysteria2": true, "hy2": true, "tuic": true, "anytls": true,
	"socks": true, "socks5": true,
}

var supportedShadowsocksCiphers = map[string]bool{
	"2022-blake3-aes-128-gcm":       true,
	"2022-blake3-aes-256-gcm":       true,
	"2022-blake3-chacha20-poly1305": true,
	"none":                          true,
	"aes-128-gcm":                   true,
	"aes-192-gcm":                   true,
	"aes-256-gcm":                   true,
	"aes-128-ctr":                   true,
	"aes-192-ctr":                   true,
	"aes-256-ctr":                   true,
	"aes-128-cfb":                   true,
	"aes-192-cfb":                   true,
	"aes-256-cfb":                   true,
	"chacha20-ietf-poly1305":        true,
	"xchacha20-ietf-poly1305":       true,
	"rc4-md5":                       true,
	"chacha20-ietf":                 true,
	"xchacha20":                     true,
}

var supportedVMessCiphers = map[string]bool{
	"auto":              true,
	"aes-128-gcm":       true,
	"chacha20-poly1305": true,
	"none":              true,
	"zero":              true,
}

var supportedUTLSFingerprints = map[string]bool{
	"chrome": true, "firefox": true, "edge": true, "safari": true,
	"360": true, "qq": true, "ios": true, "android": true,
	"random": true, "randomized": true,
}

var supportedTUICCongestion = map[string]bool{
	"cubic": true, "new_reno": true, "bbr": true,
}

var supportedTUICRelay = map[string]bool{
	"native": true, "quic": true,
}

var uriProtocolQueryFields = map[string]map[string]bool{
	"ss": uriFieldSet(
		"plugin", "udp", "dialer-proxy", "dialerProxy",
	),
	"vless": uriFieldSet(
		"security", "sni", "servername", "server-name", "peer",
		"client-fingerprint", "fp", "alpn", "allowInsecure", "insecure",
		"pbk", "sid", "type", "headerType", "path", "host", "serviceName",
		"encryption", "flow", "packetEncoding", "udp",
		"dialer-proxy", "dialerProxy",
	),
	"trojan": uriFieldSet(
		"security", "sni", "servername", "server-name", "peer",
		"client-fingerprint", "fp", "alpn", "allowInsecure", "insecure",
		"type", "headerType", "path", "host", "serviceName",
		"ss-opts", "ss_opts", "udp", "dialer-proxy", "dialerProxy",
	),
	"hy2": uriFieldSet(
		"security", "sni", "peer", "alpn", "insecure", "allowInsecure",
		"password", "obfs", "obfs-password", "obfsParam",
		"realm", "fingerprint", "hop-interval", "hop_interval",
		"mport", "ports", "udp", "dialer-proxy", "dialerProxy",
	),
	"tuic": uriFieldSet(
		"security", "sni", "peer", "alpn", "insecure", "allow_insecure",
		"congestion_control", "udp_relay_mode", "token", "udp",
		"dialer-proxy", "dialerProxy",
	),
	"anytls": uriFieldSet(
		"security", "sni", "peer", "alpn", "insecure", "allowInsecure",
		"client-fingerprint", "fp", "udp", "dialer-proxy", "dialerProxy",
	),
	"socks": uriFieldSet(
		"tls", "udp", "dialer-proxy", "dialerProxy",
	),
}

var vmessJSONFields = uriFieldSet(
	"v", "ps", "add", "port", "id", "aid", "scy", "security",
	"net", "type", "host", "path", "serviceName",
	"tls", "sni", "allowInsecure", "skip-cert-verify", "alpn", "fp",
	"dialer-proxy", "dialerProxy", "fingerprint", "certificate", "ech",
	"grpc-user-agent", "grpcUserAgent", "pbk", "sid",
)

func uriFieldSet(fields ...string) map[string]bool {
	result := make(map[string]bool, len(fields))
	for _, field := range fields {
		result[field] = true
	}
	return result
}

func validateVMessJSONFields(raw map[string]any) (string, string) {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !vmessJSONFields[key] {
			return "unsupported_field", key
		}
	}
	for _, key := range []string{
		"ps", "add", "id", "scy", "security", "net", "type", "host",
		"path", "serviceName", "tls", "sni", "alpn", "fp",
		"dialer-proxy", "dialerProxy", "fingerprint", "certificate", "ech",
		"grpc-user-agent", "grpcUserAgent", "pbk", "sid",
	} {
		if value, present := raw[key]; present {
			if _, ok := value.(string); !ok {
				return "invalid_field", key
			}
		}
	}
	for _, key := range []string{"v", "port", "aid"} {
		if value, present := raw[key]; present {
			switch value.(type) {
			case string, json.Number:
			default:
				return "invalid_field", key
			}
		}
	}
	for _, key := range []string{"allowInsecure", "skip-cert-verify"} {
		value, present := raw[key]
		if !present {
			continue
		}
		switch typed := value.(type) {
		case bool:
		case string:
			if !validBooleanQueryValue(typed) {
				return "invalid_field", key
			}
		default:
			return "invalid_field", key
		}
	}
	if vmessBooleanAliasesConflict(raw, "allowInsecure", "skip-cert-verify") {
		return "invalid_field", "tls"
	}

	network := strings.ToLower(firstNonEmpty(textValue(raw["net"]), "tcp"))
	if !supportedURITransport("vmess", network) {
		return "unsupported_transport", "transport"
	}
	headerType := strings.ToLower(textValue(raw["type"]))
	if headerType != "" && headerType != "none" {
		if network != "tcp" || headerType != "http" {
			return "unsupported_transport", "transport"
		}
	}
	switch network {
	case "tcp":
		if headerType != "http" &&
			firstNonEmpty(
				textValue(raw["path"]), textValue(raw["host"]),
				textValue(raw["serviceName"]),
			) != "" {
			return "unsupported_transport", "transport"
		}
	case "ws", "websocket":
		if textValue(raw["serviceName"]) != "" {
			return "unsupported_transport", "transport"
		}
		if !validWSPath(textValue(raw["path"])) {
			return "unsupported_transport", "path"
		}
	case "grpc":
		if textValue(raw["host"]) != "" {
			return "unsupported_transport", "transport"
		}
		if textValue(raw["serviceName"]) != "" &&
			textValue(raw["path"]) != "" {
			return "unsupported_transport", "transport"
		}
	case "h2", "http":
		if textValue(raw["serviceName"]) != "" {
			return "unsupported_transport", "transport"
		}
	case "quic":
		if firstNonEmpty(
			textValue(raw["path"]), textValue(raw["host"]),
			textValue(raw["serviceName"]),
		) != "" {
			return "unsupported_transport", "transport"
		}
	default:
		return "unsupported_transport", "transport"
	}
	return "", ""
}

func canonicalURIScheme(scheme string) string {
	switch scheme {
	case "hysteria2", "hy2":
		return "hy2"
	case "socks", "socks5":
		return "socks"
	default:
		return scheme
	}
}

func validateURIQueryFields(scheme string, values url.Values) (string, bool) {
	allowed := uriProtocolQueryFields[canonicalURIScheme(scheme)]
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !allowed[key] || len(values[key]) != 1 {
			return key, false
		}
	}
	for _, aliases := range [][]string{
		{"sni", "servername", "server-name", "peer"},
		{"client-fingerprint", "fp"},
		{"mport", "ports"},
		{"obfs-password", "obfsParam"},
	} {
		if queryAliasesConflict(values, aliases...) {
			return aliases[0], false
		}
	}
	for _, field := range []string{
		"allowInsecure", "allow_insecure", "insecure", "tls", "udp",
	} {
		if value, present := firstPresent(values, field); present &&
			!validBooleanQueryValue(value) {
			return field, false
		}
	}
	if booleanQueryAliasesConflict(values, "allowInsecure", "allow_insecure", "insecure") {
		return "tls", false
	}
	return "", true
}

func queryAliasesConflict(values url.Values, names ...string) bool {
	first := ""
	found := false
	for _, name := range names {
		value, present := firstPresent(values, name)
		if !present || value == "" {
			continue
		}
		if !found {
			first = value
			found = true
			continue
		}
		if value != first {
			return true
		}
	}
	return false
}

func validBooleanQueryValue(value string) bool {
	switch value {
	case "", "0", "1", "false", "true", "no", "yes", "off", "on":
		return true
	default:
		return false
	}
}

func booleanQueryAliasesConflict(values url.Values, names ...string) bool {
	first := false
	found := false
	for _, name := range names {
		value, present := firstPresent(values, name)
		if !present {
			continue
		}
		normalized := truthyValue(value)
		if !found {
			first = normalized
			found = true
			continue
		}
		if normalized != first {
			return true
		}
	}
	return false
}

func vmessBooleanAliasesConflict(raw map[string]any, names ...string) bool {
	first := false
	found := false
	for _, name := range names {
		value, present := raw[name]
		if !present {
			continue
		}
		normalized := false
		switch typed := value.(type) {
		case bool:
			normalized = typed
		case string:
			normalized = truthyValue(typed)
		default:
			return true
		}
		if !found {
			first = normalized
			found = true
			continue
		}
		if normalized != first {
			return true
		}
	}
	return false
}

func schemeFromURI(line string) string {
	index := strings.Index(line, "://")
	if index <= 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(line[:index]))
}

func preflightURI(line string) (nodeType, code, field string) {
	scheme := schemeFromURI(line)
	if !supportedSchemes[scheme] {
		if scheme == "ssr" {
			return "ssr", "unsupported_protocol", "type"
		}
		return "unknown", "unsupported_protocol", "type"
	}
	if len(line) > MaxScalarBytes {
		return scheme, "invalid_field", "document"
	}
	if scheme == "vmess" {
		return preflightVMessURI(line)
	}

	if scheme == "vless" || scheme == "trojan" || scheme == "anytls" {
		parsed, err := url.Parse(line)
		if err != nil || parsed.User == nil {
			return scheme, "parse_failed", "document"
		}
		if _, hasPasswordComponent := parsed.User.Password(); hasPasswordComponent {
			field := "password"
			if scheme == "vless" {
				field = "uuid"
			}
			return scheme, "invalid_field", field
		}
	}

	values, queryOK := strictQueryValues(line)
	if !queryOK {
		return scheme, "parse_failed", "document"
	}
	if field, valid := validateURIQueryFields(scheme, values); !valid {
		return scheme, "unsupported_field", field
	}
	security, securityOK := singleQueryValue(values, "security")
	if !securityOK {
		return scheme, "invalid_field", "tls"
	}
	security = strings.ToLower(security)
	if hasExactNonEmpty(values, "dialer-proxy", "dialerProxy") {
		return scheme, "unsupported_reference", "dialer-proxy"
	}
	if conflict(values.Get("sni"), values.Get("servername"), values.Get("server-name")) {
		return scheme, "unsupported_tls_option", "servername"
	}
	if hasNonEmpty(values, "fingerprint", "certificate", "certificate-path",
		"ca", "ca-file", "ech", "ech-config", "shadowtls", "restls") {
		return scheme, "unsupported_tls_option", "fingerprint"
	}
	if fingerprint := firstNonEmpty(values.Get("client-fingerprint"), values.Get("fp")); fingerprint != "" &&
		!supportedUTLSFingerprints[strings.ToLower(fingerprint)] {
		return scheme, "unsupported_tls_option", "client-fingerprint"
	}
	network := strings.ToLower(firstNonEmpty(values.Get("type"), values.Get("network"), "tcp"))
	if !supportedURITransport(scheme, network) {
		return scheme, "unsupported_transport", "transport"
	}
	if field, valid := validateURITransportOptions(network, values); !valid {
		return scheme, "unsupported_transport", field
	}
	if network == "grpc" && hasNonEmpty(values, "grpc-user-agent", "grpcUserAgent") {
		return scheme, "unsupported_transport", "grpc-user-agent"
	}

	switch scheme {
	case "ss":
		if hasNonEmpty(values, "plugin") {
			return scheme, "unsupported_plugin", "plugin"
		}
	case "vless":
		switch security {
		case "", "none":
			if hasNonEmpty(values, "sni", "servername", "server-name", "peer",
				"client-fingerprint", "fp", "alpn", "allowInsecure", "insecure",
				"pbk", "sid") ||
				(hasNonEmpty(values, "host") && !uriHostIsTransportOption(network, values)) {
				return scheme, "unsupported_tls_option", "tls"
			}
		case "tls", "xtls":
			if hasNonEmpty(values, "pbk", "sid") {
				return scheme, "unsupported_tls_option", "reality-opts"
			}
		case "reality":
			if values.Get("pbk") == "" || values.Get("sid") == "" {
				return scheme, "unsupported_tls_option", "reality-opts"
			}
		default:
			return scheme, "unsupported_tls_option", "tls"
		}
		if encryption := values.Get("encryption"); encryption != "" &&
			!strings.EqualFold(encryption, "none") {
			return scheme, "unsupported_encryption", "encryption"
		}
		if flow := values.Get("flow"); flow != "" && flow != "xtls-rprx-vision" {
			return scheme, "unsupported_flow", "flow"
		}
	case "trojan":
		if security != "" && security != "tls" {
			return scheme, "unsupported_tls_option", "tls"
		}
		if hasNonEmpty(values, "pbk", "sid") {
			return scheme, "unsupported_tls_option", "reality-opts"
		}
		if hasNonEmpty(values, "ss-opts", "ss_opts") {
			return scheme, "unsupported_tls_option", "ss-opts"
		}
	case "hysteria2", "hy2":
		if security != "" && security != "tls" {
			return scheme, "unsupported_tls_option", "tls"
		}
		if hasNonEmpty(values, "pbk", "sid") {
			return scheme, "unsupported_tls_option", "reality-opts"
		}
		if hasNonEmpty(values, "realm", "fingerprint", "hop-interval", "hop_interval") {
			return scheme, "unsupported_hysteria2_option", "realm"
		}
		if hasNonEmpty(values, "mport", "ports") {
			return scheme, "unsupported_hysteria2_option", "ports"
		}
		if obfs := values.Get("obfs"); obfs != "" && obfs != "salamander" {
			return scheme, "unsupported_hysteria2_option", "obfs"
		}
		if values.Get("obfs") == "" && hasNonEmpty(values, "obfs-password", "obfsParam") {
			return scheme, "unsupported_hysteria2_option", "obfs"
		}
		if hasNonEmpty(values, "password") {
			parsed, err := url.Parse(line)
			if err != nil || (parsed.User != nil && parsed.User.Username() != "") {
				return scheme, "invalid_field", "password"
			}
		}
	case "tuic":
		if security != "" && security != "tls" {
			return scheme, "unsupported_tls_option", "tls"
		}
		if hasNonEmpty(values, "pbk", "sid") {
			return scheme, "unsupported_tls_option", "reality-opts"
		}
		if hasNonEmpty(values, "token") {
			return scheme, "unsupported_tuic_v4", "token"
		}
		if congestion := values.Get("congestion_control"); congestion != "" &&
			!supportedTUICCongestion[congestion] {
			return scheme, "invalid_field", "congestion_control"
		}
		if relay := values.Get("udp_relay_mode"); relay != "" &&
			!supportedTUICRelay[relay] {
			return scheme, "invalid_field", "udp_relay_mode"
		}
	case "anytls":
		if security != "" && security != "tls" {
			return scheme, "unsupported_tls_option", "tls"
		}
		if hasNonEmpty(values, "pbk", "sid") {
			return scheme, "unsupported_tls_option", "reality-opts"
		}
	case "socks", "socks5":
		if truthyValue(values.Get("tls")) {
			return scheme, "unsupported_socks_tls", "tls"
		}
		parsed, err := url.Parse(line)
		if err == nil && parsed.User != nil {
			username := parsed.User.Username()
			password, hasPassword := parsed.User.Password()
			if hasPassword && password != "" && username == "" {
				return scheme, "invalid_field", "username"
			}
		}
	}
	return scheme, "", ""
}

func preflightVMessURI(line string) (nodeType, code, field string) {
	payload, bodyOK := uriSchemeBody(line)
	if !bodyOK {
		return "vmess", "parse_failed", "document"
	}
	if index := strings.IndexByte(payload, '#'); index >= 0 {
		payload = payload[:index]
	}
	decoded, ok := decodeBase64Segment(payload)
	if !ok || len(decoded) > MaxScalarBytes {
		return "vmess", "parse_failed", "document"
	}
	if !utf8.ValidString(decoded) {
		return "vmess", "parse_failed", "document"
	}
	if err := validateJSONBounds([]byte(decoded)); err != nil {
		return "vmess", "parse_failed", "document"
	}
	decoder := json.NewDecoder(strings.NewReader(decoded))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return "vmess", "parse_failed", "document"
	}
	if code, field := validateVMessJSONFields(raw); code != "" {
		return "vmess", code, field
	}
	if textValue(raw["id"]) == "" {
		return "vmess", "missing_field", "uuid"
	}
	alterIDRaw, exists := raw["aid"]
	if !exists || !isUnsignedDecimal(textValue(alterIDRaw)) {
		return "vmess", "missing_field", "alterId"
	}
	cipher := textValue(raw["scy"])
	securityCipher := textValue(raw["security"])
	if cipher != "" && securityCipher != "" &&
		!strings.EqualFold(cipher, securityCipher) {
		return "vmess", "unsupported_cipher", "cipher"
	}
	cipher = strings.ToLower(firstNonEmpty(cipher, securityCipher))
	if cipher == "" {
		return "vmess", "missing_field", "cipher"
	}
	if !supportedVMessCiphers[cipher] {
		return "vmess", "unsupported_cipher", "cipher"
	}
	if textValue(raw["add"]) == "" || !isDecimalPort(textValue(raw["port"])) {
		return "vmess", "invalid_field", "server"
	}
	if exactStringFieldNonEmpty(raw, "dialer-proxy") ||
		exactStringFieldNonEmpty(raw, "dialerProxy") {
		return "vmess", "unsupported_reference", "dialer-proxy"
	}
	if conflict(textValue(raw["sni"]), textValue(raw["servername"])) {
		return "vmess", "unsupported_tls_option", "servername"
	}
	if textValue(raw["fingerprint"]) != "" || textValue(raw["certificate"]) != "" ||
		textValue(raw["ech"]) != "" {
		return "vmess", "unsupported_tls_option", "fingerprint"
	}
	if fingerprint := firstNonEmpty(textValue(raw["client-fingerprint"]), textValue(raw["fp"])); fingerprint != "" &&
		!supportedUTLSFingerprints[strings.ToLower(fingerprint)] {
		return "vmess", "unsupported_tls_option", "client-fingerprint"
	}
	tlsMode := strings.ToLower(textValue(raw["tls"]))
	switch tlsMode {
	case "", "none":
		if firstNonEmpty(
			textValue(raw["sni"]), textValue(raw["servername"]),
			textValue(raw["client-fingerprint"]), textValue(raw["fp"]),
			textValue(raw["alpn"]), textValue(raw["allowInsecure"]),
			textValue(raw["skip-cert-verify"]), textValue(raw["pbk"]),
			textValue(raw["sid"]),
		) != "" ||
			(textValue(raw["host"]) != "" &&
				!vmessHostIsTransportOption(
					strings.ToLower(firstNonEmpty(textValue(raw["net"]), "tcp")),
					strings.ToLower(textValue(raw["type"])),
				)) {
			return "vmess", "unsupported_tls_option", "tls"
		}
	case "tls":
		if firstNonEmpty(textValue(raw["pbk"]), textValue(raw["sid"])) != "" {
			return "vmess", "unsupported_tls_option", "reality-opts"
		}
	case "reality":
		return "vmess", "unsupported_tls_option", "reality-opts"
	default:
		return "vmess", "unsupported_tls_option", "tls"
	}
	network := strings.ToLower(firstNonEmpty(textValue(raw["net"]), "tcp"))
	if !supportedURITransport("vmess", network) {
		return "vmess", "unsupported_transport", "transport"
	}
	if network == "grpc" && firstNonEmpty(textValue(raw["grpc-user-agent"]), textValue(raw["grpcUserAgent"])) != "" {
		return "vmess", "unsupported_transport", "grpc-user-agent"
	}
	return "vmess", "", ""
}

func validateParsedURI(node map[string]any, line string) (code, field string) {
	nodeType, _ := node["type"].(string)
	_, tagOK := node["tag"].(string)
	server, _ := node["server"].(string)
	port, portOK := numberAsPort(node["server_port"])
	if nodeType == "" || !tagOK || strings.TrimSpace(server) == "" || !portOK || port < 1 {
		return "invalid_field", "server"
	}

	switch nodeType {
	case "shadowsocks":
		method, _ := node["method"].(string)
		if !supportedShadowsocksCiphers[strings.ToLower(method)] {
			return "unsupported_cipher", "cipher"
		}
		if _, exists := node["plugin"]; exists {
			return "unsupported_plugin", "plugin"
		}
	case "vmess":
		if textValue(node["uuid"]) == "" {
			return "missing_field", "uuid"
		}
		cipher := strings.ToLower(textValue(node["security"]))
		if !supportedVMessCiphers[cipher] {
			return "unsupported_cipher", "cipher"
		}
		node["security"] = cipher
		if _, exists := node["alter_id"]; !exists {
			return "missing_field", "alterId"
		}
	case "vless":
		if textValue(node["uuid"]) == "" {
			return "missing_field", "uuid"
		}
		security := strings.ToLower(queryValues(line).Get("security"))
		if security == "reality" {
			tls, tlsOK := node["tls"].(map[string]any)
			if !tlsOK {
				return "unsupported_tls_option", "reality-opts"
			}
			if _, realityOK := tls["reality"].(map[string]any); !realityOK {
				return "unsupported_tls_option", "reality-opts"
			}
		} else if security == "tls" || security == "xtls" {
			if _, tlsOK := node["tls"].(map[string]any); !tlsOK {
				return "unsupported_tls_option", "tls"
			}
		}
	case "trojan":
		if textValue(node["password"]) == "" {
			return "missing_field", "password"
		}
		if _, ok := node["tls"].(map[string]any); !ok {
			return "invalid_field", "tls"
		}
	case "hysteria2":
		if textValue(node["password"]) == "" {
			return "missing_field", "password"
		}
		if _, ok := node["tls"].(map[string]any); !ok {
			return "invalid_field", "tls"
		}
	case "tuic":
		if textValue(node["uuid"]) == "" || textValue(node["password"]) == "" {
			return "missing_field", "password"
		}
		if _, ok := node["tls"].(map[string]any); !ok {
			return "invalid_field", "tls"
		}
	case "anytls":
		if textValue(node["password"]) == "" {
			return "missing_field", "password"
		}
		if _, ok := node["tls"].(map[string]any); !ok {
			return "invalid_field", "tls"
		}
	case "socks":
		username := textValue(node["username"])
		password := textValue(node["password"])
		if password != "" && username == "" {
			return "invalid_field", "username"
		}
	default:
		return "unsupported_protocol", "type"
	}

	if transport, ok := node["transport"].(map[string]any); ok {
		transportType := textValue(transport["type"])
		if !supportedOutputTransport(nodeType, transportType) {
			return "unsupported_transport", "transport"
		}
	}
	_ = line
	return "", ""
}

func preserveExactURITag(node map[string]any, line string) bool {
	if schemeFromURI(line) == "vmess" {
		payload, bodyOK := uriSchemeBody(line)
		if !bodyOK {
			return false
		}
		if fragment := strings.IndexByte(payload, '#'); fragment >= 0 {
			if payload[fragment+1:] != "" {
				return false
			}
			payload = payload[:fragment]
		}
		decoded, ok := decodeBase64Segment(payload)
		if !ok {
			return false
		}
		decoder := json.NewDecoder(strings.NewReader(decoded))
		decoder.UseNumber()
		var raw map[string]any
		if decoder.Decode(&raw) != nil {
			return false
		}
		displayName, present := raw["ps"]
		if !present {
			node["tag"] = ""
			return true
		}
		tag, ok := displayName.(string)
		if !ok {
			return false
		}
		if !validExactURITag(tag) {
			return false
		}
		node["tag"] = tag
		return true
	}

	fragment := strings.IndexByte(line, '#')
	if fragment < 0 {
		if schemeFromURI(line) == "ss" {
			schemeDelimiter := strings.Index(line, "://")
			if schemeDelimiter < 0 {
				return false
			}
			body := line[schemeDelimiter+3:]
			if query := strings.IndexByte(body, '?'); query >= 0 {
				body = body[:query]
			}
			if !strings.Contains(body, "@") {
				decoded, ok := decodeBase64Segment(body)
				if !ok || !utf8.ValidString(decoded) {
					return false
				}
				if innerFragment := strings.IndexByte(decoded, '#'); innerFragment >= 0 {
					tag, err := url.PathUnescape(decoded[innerFragment+1:])
					if err != nil || !validExactURITag(tag) {
						return false
					}
					node["tag"] = tag
					return true
				}
			}
		}
		node["tag"] = ""
		return true
	}
	tag, err := url.PathUnescape(line[fragment+1:])
	if err != nil {
		return false
	}
	if !validExactURITag(tag) {
		return false
	}
	node["tag"] = tag
	return true
}

func validExactURITag(tag string) bool {
	if !utf8.ValidString(tag) {
		return false
	}
	for _, character := range tag {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func applyURICompatibility(node map[string]any, line string) {
	values := queryValues(line)
	if schemeFromURI(line) == "vless" {
		delete(node, "encryption")
	}
	if value, present := firstPresent(values, "udp"); present && !truthyValue(value) {
		node["network"] = "tcp"
	}
	tls, hasTLS := node["tls"].(map[string]any)
	if hasTLS {
		if utls, ok := tls["utls"].(map[string]any); ok {
			if fingerprint, ok := utls["fingerprint"].(string); ok {
				utls["fingerprint"] = strings.ToLower(fingerprint)
			}
		}
		if serverName := firstNonEmpty(values.Get("sni"), values.Get("servername"), values.Get("server-name")); serverName != "" {
			tls["server_name"] = serverName
		}
		if fingerprint := firstNonEmpty(values.Get("client-fingerprint"), values.Get("fp")); fingerprint != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": strings.ToLower(fingerprint)}
		}
	}
}

func supportedURITransport(scheme, network string) bool {
	switch network {
	case "", "tcp", "ws", "websocket", "grpc":
		return true
	case "h2", "http", "quic":
		return scheme == "vmess" || scheme == "vless"
	default:
		return false
	}
}

func validateURITransportOptions(network string, values url.Values) (string, bool) {
	headerType := strings.ToLower(values.Get("headerType"))
	if headerType != "" && headerType != "none" {
		if network != "tcp" || headerType != "http" {
			return "transport", false
		}
	}
	switch network {
	case "", "tcp":
		if headerType != "http" {
			if hasNonEmpty(values, "path", "host", "serviceName") {
				return "transport", false
			}
		}
	case "ws", "websocket":
		if hasNonEmpty(values, "serviceName") {
			return "transport", false
		}
		if !validWSPath(values.Get("path")) {
			return "path", false
		}
	case "grpc":
		if hasNonEmpty(values, "host") {
			return "transport", false
		}
		if hasNonEmpty(values, "serviceName") &&
			hasNonEmpty(values, "path") {
			return "transport", false
		}
	case "h2", "http":
		if hasNonEmpty(values, "serviceName") {
			return "transport", false
		}
	case "quic":
		if hasNonEmpty(values, "path", "host", "serviceName") {
			return "transport", false
		}
	default:
		return "transport", false
	}
	return "", true
}

func validWSPath(path string) bool {
	if path == "" {
		return true
	}

	decoded := path
	// Repeated decoding is bounded by an input-sized work budget. Excessive
	// nesting is ambiguous and therefore rejected rather than partially parsed.
	decodeWorkBudget := len(path) * 8
	for {
		if len(decoded) > decodeWorkBudget {
			return false
		}
		decodeWorkBudget -= len(decoded)
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return false
		}
		if next == decoded {
			break
		}
		for _, separator := range []byte{'?', '&', '='} {
			if strings.Count(next, string(separator)) >
				strings.Count(decoded, string(separator)) {
				return false
			}
		}
		decoded = next
	}

	queryStart := strings.IndexByte(path, '?')
	if queryStart < 0 {
		return true
	}
	if queryStart == 0 || strings.IndexByte(path[queryStart+1:], '?') >= 0 {
		return false
	}
	query := path[queryStart+1:]
	if !strings.HasPrefix(query, "ed=") {
		return false
	}
	value := strings.TrimPrefix(query, "ed=")
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	parsed, err := strconv.Atoi(value)
	return err == nil && parsed > 0
}

func uriHostIsTransportOption(network string, values url.Values) bool {
	switch network {
	case "ws", "websocket", "h2", "http":
		return true
	case "tcp":
		return strings.EqualFold(values.Get("headerType"), "http")
	default:
		return false
	}
}

func vmessHostIsTransportOption(network, headerType string) bool {
	switch network {
	case "ws", "websocket", "h2", "http":
		return true
	case "tcp":
		return headerType == "http"
	default:
		return false
	}
}

func supportedOutputTransport(nodeType, transport string) bool {
	switch transport {
	case "", "ws", "grpc":
		return true
	case "http", "quic":
		return nodeType == "vmess" || nodeType == "vless"
	default:
		return false
	}
}

func queryValues(line string) url.Values {
	values, _ := strictQueryValues(line)
	return values
}

func uriSchemeBody(line string) (string, bool) {
	delimiter := strings.Index(line, "://")
	if delimiter <= 0 {
		return "", false
	}
	return line[delimiter+3:], true
}

func strictQueryValues(line string) (url.Values, bool) {
	if fragment := strings.IndexByte(line, '#'); fragment >= 0 {
		line = line[:fragment]
	}
	queryStart := strings.IndexByte(line, '?')
	if queryStart < 0 {
		return url.Values{}, true
	}
	query := line[queryStart+1:]
	values, err := url.ParseQuery(query)
	if err != nil {
		return url.Values{}, false
	}
	return values, true
}

func singleQueryValue(values url.Values, name string) (string, bool) {
	items, present := values[name]
	if !present {
		return "", true
	}
	if len(items) != 1 {
		return "", false
	}
	return items[0], true
}

func hasNonEmpty(values url.Values, names ...string) bool {
	for _, name := range names {
		if strings.TrimSpace(values.Get(name)) != "" {
			return true
		}
	}
	return false
}

func hasExactNonEmpty(values url.Values, names ...string) bool {
	for _, name := range names {
		items, present := values[name]
		if present && len(items) > 0 && items[0] != "" {
			return true
		}
	}
	return false
}

func exactStringFieldNonEmpty(values map[string]any, name string) bool {
	value, present := values[name]
	if !present {
		return false
	}
	text, ok := value.(string)
	return !ok || text != ""
}

func firstPresent(values url.Values, names ...string) (string, bool) {
	for _, name := range names {
		items, ok := values[name]
		if ok {
			if len(items) == 0 {
				return "", true
			}
			return items[0], true
		}
	}
	return "", false
}

func conflict(values ...string) bool {
	first := ""
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if first == "" {
			first = value
			continue
		}
		if value != first {
			return true
		}
	}
	return false
}

func decodeBase64Segment(value string) (string, bool) {
	value = strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.StdEncoding,
	} {
		candidate := value
		if encoding == base64.URLEncoding || encoding == base64.StdEncoding {
			if remainder := len(candidate) % 4; remainder != 0 {
				candidate += strings.Repeat("=", 4-remainder)
			}
		}
		decoded, err := encoding.DecodeString(candidate)
		if err == nil {
			return string(decoded), true
		}
	}
	return "", false
}

func textValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return string(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func numberAsPort(value any) (int, bool) {
	text := textValue(value)
	if !isDecimalPort(text) {
		return 0, false
	}
	port, err := strconv.Atoi(text)
	return port, err == nil
}

func isDecimalPort(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	port, err := strconv.Atoi(value)
	return err == nil && port >= 1 && port <= 65535
}

func isUnsignedDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 31)
	return err == nil && parsed <= uint64(^uint32(0)>>1)
}

func truthyValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
