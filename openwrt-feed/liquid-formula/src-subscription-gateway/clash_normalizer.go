package main

import (
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type nodeProblem struct {
	code  string
	field string
}

var (
	clashCommonFields = []string{
		"name", "type", "server", "port", "dialer-proxy", "udp",
	}
	clashTLSFields = []string{
		"tls", "sni", "servername", "server-name", "skip-cert-verify", "alpn",
	}
	clashFingerprintFields = []string{"client-fingerprint"}
	clashRealityFields     = []string{"reality-opts"}
	clashFullTransport     = []string{
		"network", "ws-opts", "grpc-opts", "http-opts", "h2-opts", "quic-opts",
	}
	clashLimitedTransport = []string{"network", "ws-opts", "grpc-opts"}

	clashProtocolFields = map[string]map[string]bool{
		"ss": combinedFieldSet(clashCommonFields, []string{
			"cipher", "password", "plugin", "plugin-opts",
		}),
		"vmess": combinedFieldSet(
			clashCommonFields, clashTLSFields, clashFingerprintFields,
			clashRealityFields, clashFullTransport,
			[]string{"uuid", "alterId", "alter-id", "cipher"},
		),
		"vless": combinedFieldSet(
			clashCommonFields, clashTLSFields, clashFingerprintFields,
			clashRealityFields, clashFullTransport,
			[]string{"uuid", "encryption", "flow"},
		),
		"trojan": combinedFieldSet(
			clashCommonFields, clashTLSFields, clashFingerprintFields,
			clashRealityFields, clashLimitedTransport,
			[]string{"password", "ss-opts"},
		),
		"hy2": combinedFieldSet(clashCommonFields, clashTLSFields, []string{
			"password", "auth", "realm", "fingerprint", "hop-interval",
			"ports", "obfs", "obfs-password",
		}),
		"tuic": combinedFieldSet(clashCommonFields, clashTLSFields, []string{
			"token", "uuid", "password", "congestion-controller",
			"congestion_control", "udp-relay-mode", "udp_relay_mode",
		}),
		"anytls": combinedFieldSet(
			clashCommonFields, clashTLSFields, clashFingerprintFields,
			[]string{"password"},
		),
		"socks": combinedFieldSet(clashCommonFields, []string{
			"tls", "username", "password",
		}),
	}
)

func combinedFieldSet(groups ...[]string) map[string]bool {
	result := make(map[string]bool)
	for _, group := range groups {
		for _, field := range group {
			result[field] = true
		}
	}
	return result
}

func canonicalClashType(value string) string {
	switch value {
	case "ss", "shadowsocks":
		return "ss"
	case "hysteria2", "hy2":
		return "hy2"
	case "socks", "socks5":
		return "socks"
	case "vmess", "vless", "trojan", "tuic", "anytls":
		return value
	default:
		return ""
	}
}

func validateAllowedFields(values map[string]*yaml.Node, allowed map[string]bool) *nodeProblem {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !allowed[key] {
			return problem("unsupported_field", key)
		}
	}
	return nil
}

func scalarAliasesConflict(
	values map[string]*yaml.Node,
	extract func(*yaml.Node) (string, bool),
	names ...string,
) bool {
	first := ""
	found := false
	for _, name := range names {
		node, present := values[name]
		if !present {
			continue
		}
		value, ok := extract(node)
		if !ok {
			return true
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

func problem(code, field string) *nodeProblem {
	return &nodeProblem{code: code, field: field}
}

func normalizeClashYAML(raw []byte) ([]byte, NormalizeInfo, error) {
	info := NormalizeInfo{Format: FormatClashYAML}
	document, err := parseBoundedYAML(raw)
	if err != nil {
		return nil, info, err
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, info, normalizeError("clash_root_invalid", info.Format)
	}
	rootValues, err := mappingValues(root)
	if err != nil {
		return nil, info, err
	}
	proxies, exists := rootValues["proxies"]
	if !exists {
		return nil, info, noValidNodeError(info.Format)
	}
	if proxies.Kind != yaml.SequenceNode {
		return nil, info, normalizeError("clash_proxies_invalid", info.Format)
	}

	outbounds := make([]map[string]any, 0, len(proxies.Content))
	for index, proxyNode := range proxies.Content {
		if proxyNode.Kind != yaml.MappingNode {
			info.skip(index+1, "unknown", "node_not_mapping", "proxies")
			continue
		}
		values, err := mappingValues(proxyNode)
		if err != nil {
			info.skip(index+1, "unknown", "invalid_field", "document")
			continue
		}
		nodeType, _ := stringField(values, "type")
		node, nodeErr := mapClashProxy(values)
		if nodeErr != nil {
			info.skip(index+1, strings.ToLower(nodeType), nodeErr.code, nodeErr.field)
			continue
		}
		outbounds = append(outbounds, node)
		if len(outbounds) > MaxNormalizedNodes {
			return nil, info, normalizeError("too_many_outbounds", info.Format)
		}
	}
	return encodeOutbounds(outbounds, info)
}

func mapClashProxy(values map[string]*yaml.Node) (map[string]any, *nodeProblem) {
	name, nameOK := stringField(values, "name")
	if !nameOK {
		return nil, problem("missing_field", "name")
	}
	nodeType, typeOK := tokenField(values, "type")
	nodeType = strings.ToLower(nodeType)
	if !typeOK || nodeType == "" {
		return nil, problem("missing_field", "type")
	}
	canonicalType := canonicalClashType(nodeType)
	if canonicalType == "" {
		return nil, problem("unsupported_protocol", "type")
	}
	if fieldErr := validateAllowedFields(values, clashProtocolFields[canonicalType]); fieldErr != nil {
		return nil, fieldErr
	}
	if nonEmptyField(values, "dialer-proxy") {
		return nil, problem("unsupported_reference", "dialer-proxy")
	}
	server, serverOK := tokenField(values, "server")
	if !serverOK || server == "" {
		return nil, problem("missing_field", "server")
	}
	port, portPresent, portValid := intField(values, "port")
	if !portPresent || !portValid || port < 1 || port > 65535 {
		return nil, problem("invalid_field", "port")
	}

	base := map[string]any{
		"tag":         name,
		"server":      server,
		"server_port": port,
	}
	if udp, present, ok := boolField(values, "udp"); present {
		if !ok {
			return nil, problem("invalid_field", "udp")
		}
		if !udp {
			base["network"] = "tcp"
		}
	}

	switch canonicalType {
	case "ss":
		return mapClashShadowsocks(base, values)
	case "vmess":
		return mapClashVMess(base, values)
	case "vless":
		return mapClashVLESS(base, values)
	case "trojan":
		return mapClashTrojan(base, values)
	case "hy2":
		return mapClashHysteria2(base, values)
	case "tuic":
		return mapClashTUIC(base, values)
	case "anytls":
		return mapClashAnyTLS(base, values)
	case "socks":
		return mapClashSOCKS(base, values)
	}
	return nil, problem("unsupported_protocol", "type")
}

func mapClashShadowsocks(node map[string]any, values map[string]*yaml.Node) (map[string]any, *nodeProblem) {
	cipher, ok := tokenField(values, "cipher")
	if !ok || !supportedShadowsocksCiphers[strings.ToLower(cipher)] {
		return nil, problem("unsupported_cipher", "cipher")
	}
	if nonEmptyField(values, "plugin") || nonEmptyField(values, "plugin-opts") {
		return nil, problem("unsupported_plugin", "plugin")
	}
	password, present := stringField(values, "password")
	if !present {
		return nil, problem("missing_field", "password")
	}
	node["type"] = "shadowsocks"
	node["method"] = strings.ToLower(cipher)
	node["password"] = password
	return node, nil
}

func mapClashVMess(node map[string]any, values map[string]*yaml.Node) (map[string]any, *nodeProblem) {
	uuid, ok := tokenField(values, "uuid")
	if !ok || uuid == "" {
		return nil, problem("missing_field", "uuid")
	}
	if scalarAliasesConflict(values, scalarDecimal, "alterId", "alter-id") {
		return nil, problem("invalid_field", "alterId")
	}
	alterID, present, valid := intField(values, "alterId", "alter-id")
	if !present || !valid || alterID < 0 {
		return nil, problem("invalid_field", "alterId")
	}
	cipher, ok := tokenField(values, "cipher")
	cipher = strings.ToLower(cipher)
	if !ok || !supportedVMessCiphers[cipher] {
		return nil, problem("unsupported_cipher", "cipher")
	}
	node["type"] = "vmess"
	node["uuid"] = uuid
	node["alter_id"] = alterID
	node["security"] = cipher
	if transport, err := mapClashTransport(values, "vmess"); err != nil {
		return nil, err
	} else if transport != nil {
		node["transport"] = transport
	}
	if tls, err := mapClashTLS(values, "vmess", false); err != nil {
		return nil, err
	} else if tls != nil {
		node["tls"] = tls
	}
	return node, nil
}

func mapClashVLESS(node map[string]any, values map[string]*yaml.Node) (map[string]any, *nodeProblem) {
	uuid, ok := tokenField(values, "uuid")
	if !ok || uuid == "" {
		return nil, problem("missing_field", "uuid")
	}
	if encryption, present, valid := tokenFieldStatus(values, "encryption"); present {
		if !valid {
			return nil, problem("invalid_field", "encryption")
		}
		if encryption != "" && !strings.EqualFold(encryption, "none") {
			return nil, problem("unsupported_encryption", "encryption")
		}
	}
	if flow, present, valid := tokenFieldStatus(values, "flow"); present {
		if !valid {
			return nil, problem("invalid_field", "flow")
		}
		if flow != "" {
			if flow != "xtls-rprx-vision" {
				return nil, problem("unsupported_flow", "flow")
			}
			node["flow"] = flow
		}
	}
	node["type"] = "vless"
	node["uuid"] = uuid
	if transport, err := mapClashTransport(values, "vless"); err != nil {
		return nil, err
	} else if transport != nil {
		node["transport"] = transport
	}
	if tls, err := mapClashTLS(values, "vless", false); err != nil {
		return nil, err
	} else if tls != nil {
		node["tls"] = tls
	}
	return node, nil
}

func mapClashTrojan(node map[string]any, values map[string]*yaml.Node) (map[string]any, *nodeProblem) {
	password, ok := stringField(values, "password")
	if !ok || password == "" {
		return nil, problem("missing_field", "password")
	}
	if options, exists := values["ss-opts"]; exists {
		optionValues, err := mappingValues(options)
		if err != nil {
			return nil, problem("invalid_field", "ss-opts")
		}
		if fieldErr := validateAllowedFields(optionValues, combinedFieldSet([]string{"enabled"})); fieldErr != nil {
			return nil, problem(fieldErr.code, "ss-opts")
		}
		enabled, present, valid := boolField(optionValues, "enabled")
		if !valid {
			return nil, problem("invalid_field", "ss-opts")
		}
		if present && enabled {
			return nil, problem("unsupported_tls_option", "ss-opts")
		}
	}
	node["type"] = "trojan"
	node["password"] = password
	if transport, err := mapClashTransport(values, "trojan"); err != nil {
		return nil, err
	} else if transport != nil {
		node["transport"] = transport
	}
	tls, err := mapClashTLS(values, "trojan", true)
	if err != nil {
		return nil, err
	}
	node["tls"] = tls
	return node, nil
}

func mapClashHysteria2(node map[string]any, values map[string]*yaml.Node) (map[string]any, *nodeProblem) {
	if scalarAliasesConflict(values, scalarExact, "password", "auth") {
		return nil, problem("invalid_field", "password")
	}
	password, ok := stringField(values, "password", "auth")
	if !ok || password == "" {
		return nil, problem("missing_field", "password")
	}
	if nonEmptyField(values, "realm") || nonEmptyField(values, "fingerprint") ||
		nonEmptyField(values, "hop-interval") {
		return nil, problem("unsupported_hysteria2_option", "realm")
	}
	node["type"] = "hysteria2"
	node["password"] = password
	if ports, present, valid := tokenFieldStatus(values, "ports"); present {
		if !valid {
			return nil, problem("invalid_field", "ports")
		}
		if ports != "" {
			return nil, problem("unsupported_hysteria2_option", "ports")
		}
	}
	if obfs, present, valid := tokenFieldStatus(values, "obfs"); present {
		if !valid {
			return nil, problem("invalid_field", "obfs")
		}
		if obfs != "" {
			if obfs != "salamander" {
				return nil, problem("unsupported_hysteria2_option", "obfs")
			}
			obfsPassword, ok := stringField(values, "obfs-password")
			if !ok || obfsPassword == "" {
				return nil, problem("missing_field", "obfs")
			}
			node["obfs"] = map[string]any{"type": "salamander", "password": obfsPassword}
		} else if nonEmptyField(values, "obfs-password") {
			return nil, problem("unsupported_hysteria2_option", "obfs")
		}
	} else if nonEmptyField(values, "obfs-password") {
		return nil, problem("unsupported_hysteria2_option", "obfs")
	}
	tls, err := mapClashTLS(values, "hy2", true)
	if err != nil {
		return nil, err
	}
	node["tls"] = tls
	return node, nil
}

func mapClashTUIC(node map[string]any, values map[string]*yaml.Node) (map[string]any, *nodeProblem) {
	if nonEmptyField(values, "token") {
		return nil, problem("unsupported_tuic_v4", "token")
	}
	uuid, uuidOK := tokenField(values, "uuid")
	password, passwordOK := stringField(values, "password")
	if !uuidOK || uuid == "" || !passwordOK || password == "" {
		return nil, problem("missing_field", "password")
	}
	node["type"] = "tuic"
	node["uuid"] = uuid
	node["password"] = password
	if scalarAliasesConflict(values, scalarToken, "congestion-controller", "congestion_control") {
		return nil, problem("invalid_field", "congestion-controller")
	}
	if congestion, present, valid := tokenFieldStatus(values, "congestion-controller", "congestion_control"); present {
		if !valid {
			return nil, problem("invalid_field", "congestion-controller")
		}
		if congestion != "" {
			if !supportedTUICCongestion[congestion] {
				return nil, problem("invalid_field", "congestion-controller")
			}
			node["congestion_control"] = congestion
		}
	}
	if scalarAliasesConflict(values, scalarToken, "udp-relay-mode", "udp_relay_mode") {
		return nil, problem("invalid_field", "udp-relay-mode")
	}
	if relay, present, valid := tokenFieldStatus(values, "udp-relay-mode", "udp_relay_mode"); present {
		if !valid {
			return nil, problem("invalid_field", "udp-relay-mode")
		}
		if relay != "" {
			if !supportedTUICRelay[relay] {
				return nil, problem("invalid_field", "udp-relay-mode")
			}
			node["udp_relay_mode"] = relay
		}
	}
	tls, err := mapClashTLS(values, "tuic", true)
	if err != nil {
		return nil, err
	}
	node["tls"] = tls
	return node, nil
}

func mapClashAnyTLS(node map[string]any, values map[string]*yaml.Node) (map[string]any, *nodeProblem) {
	password, ok := stringField(values, "password")
	if !ok || password == "" {
		return nil, problem("missing_field", "password")
	}
	node["type"] = "anytls"
	node["password"] = password
	tls, err := mapClashTLS(values, "anytls", true)
	if err != nil {
		return nil, err
	}
	node["tls"] = tls
	return node, nil
}

func mapClashSOCKS(node map[string]any, values map[string]*yaml.Node) (map[string]any, *nodeProblem) {
	if tls, present, valid := boolField(values, "tls"); present {
		if !valid {
			return nil, problem("invalid_field", "tls")
		}
		if tls {
			return nil, problem("unsupported_socks_tls", "tls")
		}
	}
	username, usernamePresent, usernameValid := stringFieldStatus(values, "username")
	password, passwordPresent, passwordValid := stringFieldStatus(values, "password")
	if !usernameValid || !passwordValid {
		return nil, problem("invalid_field", "username")
	}
	if passwordPresent && password != "" && (!usernamePresent || username == "") {
		return nil, problem("invalid_field", "username")
	}
	node["type"] = "socks"
	node["version"] = "5"
	if usernamePresent && username != "" {
		node["username"] = username
	}
	if passwordPresent {
		node["password"] = password
	}
	return node, nil
}

func mapClashTLS(values map[string]*yaml.Node, protocol string, required bool) (map[string]any, *nodeProblem) {
	enabled := required
	explicitDisable := false
	if value, present, valid := boolField(values, "tls"); present {
		if !valid {
			return nil, problem("invalid_field", "tls")
		}
		if required && !value {
			return nil, problem("unsupported_tls_option", "tls")
		}
		enabled = value
		explicitDisable = !value
	}
	_, realityPresent := values["reality-opts"]
	realityAllowed := protocol == "vmess" || protocol == "vless" || protocol == "trojan"
	if realityPresent && !realityAllowed {
		return nil, problem("unsupported_tls_option", "reality-opts")
	}
	if realityPresent {
		if explicitDisable {
			return nil, problem("unsupported_tls_option", "tls")
		}
		enabled = true
	}
	if !enabled {
		if nonEmptyField(values, "sni", "servername", "server-name",
			"skip-cert-verify", "alpn", "client-fingerprint", "reality-opts") {
			return nil, problem("unsupported_tls_option", "tls")
		}
		return nil, nil
	}
	if scalarAliasesConflict(values, scalarToken, "sni", "servername", "server-name") {
		return nil, problem("unsupported_tls_option", "servername")
	}
	serverNames := make([]string, 0, 3)
	for _, field := range []string{"sni", "servername", "server-name"} {
		if value, present := tokenField(values, field); present && value != "" {
			serverNames = append(serverNames, value)
		}
	}
	tls := map[string]any{"enabled": true}
	if len(serverNames) > 0 {
		tls["server_name"] = serverNames[0]
	}
	if insecure, present, valid := boolField(values, "skip-cert-verify"); present {
		if !valid {
			return nil, problem("invalid_field", "tls")
		}
		if insecure {
			tls["insecure"] = true
		}
	}
	if alpn, present, valid := stringListField(values, "alpn"); present {
		if !valid {
			return nil, problem("invalid_field", "alpn")
		}
		tls["alpn"] = alpn
	}
	fingerprint, fingerprintPresent, fingerprintValid := tokenFieldStatus(values, "client-fingerprint")
	if fingerprintPresent {
		if !fingerprintValid {
			return nil, problem("invalid_field", "client-fingerprint")
		}
		if fingerprint != "" {
			fingerprint = strings.ToLower(fingerprint)
			if !supportedUTLSFingerprints[fingerprint] {
				return nil, problem("unsupported_tls_option", "client-fingerprint")
			}
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": fingerprint}
		}
	}
	if realityNode, present := values["reality-opts"]; present {
		realityValues, err := mappingValues(realityNode)
		if err != nil {
			return nil, problem("invalid_field", "reality-opts")
		}
		if fieldErr := validateAllowedFields(
			realityValues,
			combinedFieldSet([]string{"public-key", "short-id"}),
		); fieldErr != nil {
			return nil, problem(fieldErr.code, "reality-opts")
		}
		publicKey, publicOK := tokenField(realityValues, "public-key")
		shortID, shortOK := tokenField(realityValues, "short-id")
		if !publicOK || publicKey == "" || !shortOK || shortID == "" {
			return nil, problem("unsupported_tls_option", "reality-opts")
		}
		tls["reality"] = map[string]any{
			"enabled": true, "public_key": publicKey, "short_id": shortID,
		}
		if _, exists := tls["utls"]; !exists {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		}
	}
	return tls, nil
}

func mapClashTransport(values map[string]*yaml.Node, protocol string) (map[string]any, *nodeProblem) {
	network, present, valid := tokenFieldStatus(values, "network")
	if present && !valid {
		return nil, problem("invalid_field", "network")
	}
	if !present || network == "" {
		network = "tcp"
	}
	network = strings.ToLower(network)
	if protocol == "trojan" && network != "tcp" && network != "ws" && network != "grpc" {
		return nil, problem("unsupported_transport", "network")
	}
	selectedOption := ""
	switch network {
	case "ws", "websocket":
		selectedOption = "ws-opts"
	case "grpc":
		selectedOption = "grpc-opts"
	case "http":
		selectedOption = "http-opts"
	case "h2":
		selectedOption = "h2-opts"
	case "quic":
		selectedOption = "quic-opts"
	}
	for _, optionField := range []string{
		"ws-opts", "grpc-opts", "http-opts", "h2-opts", "quic-opts",
	} {
		if _, present := values[optionField]; present && optionField != selectedOption {
			return nil, problem("unsupported_field", optionField)
		}
	}
	switch network {
	case "tcp":
		return nil, nil
	case "ws", "websocket":
		options, err := optionMapping(values, "ws-opts")
		if err != nil {
			return nil, problem("invalid_field", "ws-opts")
		}
		if fieldErr := validateAllowedFields(options, combinedFieldSet([]string{
			"path", "headers", "max-early-data", "early-data-header-name",
		})); fieldErr != nil {
			return nil, problem(fieldErr.code, "ws-opts")
		}
		transport := map[string]any{"type": "ws"}
		if path, present, valid := stringFieldStatus(options, "path"); present {
			if !valid {
				return nil, problem("invalid_field", "ws-opts")
			}
			if path != "" {
				transport["path"] = path
			}
		}
		if headersNode, present := options["headers"]; present {
			headers, ok := stringHeaderMap(headersNode)
			if !ok {
				return nil, problem("invalid_field", "ws-opts")
			}
			transport["headers"] = headers
		}
		if earlyData, present, valid := intField(options, "max-early-data"); present {
			if !valid || earlyData < 0 {
				return nil, problem("invalid_field", "ws-opts")
			}
			transport["max_early_data"] = earlyData
		}
		if header, present, valid := stringFieldStatus(options, "early-data-header-name"); present {
			if !valid {
				return nil, problem("invalid_field", "ws-opts")
			}
			if header != "" {
				transport["early_data_header_name"] = header
			}
		}
		return transport, nil
	case "grpc":
		options, err := optionMapping(values, "grpc-opts")
		if err != nil {
			return nil, problem("invalid_field", "grpc-opts")
		}
		if fieldErr := validateAllowedFields(options, combinedFieldSet([]string{
			"grpc-service-name", "service-name",
		})); fieldErr != nil {
			return nil, problem(fieldErr.code, "grpc-opts")
		}
		if scalarAliasesConflict(options, scalarExact, "grpc-service-name", "service-name") {
			return nil, problem("invalid_field", "grpc-opts")
		}
		transport := map[string]any{"type": "grpc"}
		if service, present, valid := stringFieldStatus(options, "grpc-service-name", "service-name"); present {
			if !valid {
				return nil, problem("invalid_field", "grpc-opts")
			}
			transport["service_name"] = service
		}
		return transport, nil
	case "http":
		options, err := optionMapping(values, "http-opts")
		if err != nil {
			return nil, problem("invalid_field", "http-opts")
		}
		if fieldErr := validateAllowedFields(options, combinedFieldSet([]string{
			"method", "path", "headers",
		})); fieldErr != nil {
			return nil, problem(fieldErr.code, "http-opts")
		}
		transport := map[string]any{"type": "http"}
		if method, present, valid := tokenFieldStatus(options, "method"); present {
			if !valid {
				return nil, problem("invalid_field", "http-opts")
			}
			if method != "" {
				transport["method"] = method
			}
		}
		if path, present, valid := exactSingleStringField(options, "path"); present {
			if !valid {
				return nil, problem("invalid_field", "http-opts")
			}
			transport["path"] = path
		}
		if headersNode, present := options["headers"]; present {
			headers, hosts, ok := httpHeaderMap(headersNode)
			if !ok {
				return nil, problem("invalid_field", "http-opts")
			}
			if len(hosts) > 0 {
				transport["host"] = hosts
			}
			if len(headers) > 0 {
				transport["headers"] = headers
			}
		}
		return transport, nil
	case "h2":
		options, err := optionMapping(values, "h2-opts")
		if err != nil {
			return nil, problem("invalid_field", "h2-opts")
		}
		if fieldErr := validateAllowedFields(options, combinedFieldSet([]string{
			"host", "path",
		})); fieldErr != nil {
			return nil, problem(fieldErr.code, "h2-opts")
		}
		transport := map[string]any{"type": "http"}
		if path, present, valid := stringFieldStatus(options, "path"); present {
			if !valid {
				return nil, problem("invalid_field", "h2-opts")
			}
			if path != "" {
				transport["path"] = path
			}
		}
		if hosts, present, valid := stringListField(options, "host"); present {
			if !valid {
				return nil, problem("invalid_field", "h2-opts")
			}
			transport["host"] = hosts
		}
		return transport, nil
	case "quic":
		if protocol != "vmess" && protocol != "vless" {
			return nil, problem("unsupported_transport", "network")
		}
		options, err := optionMapping(values, "quic-opts")
		if err != nil {
			return nil, problem("invalid_field", "quic-opts")
		}
		if fieldErr := validateAllowedFields(options, map[string]bool{}); fieldErr != nil {
			return nil, problem(fieldErr.code, "quic-opts")
		}
		return map[string]any{"type": "quic"}, nil
	default:
		return nil, problem("unsupported_transport", "network")
	}
}

func optionMapping(values map[string]*yaml.Node, names ...string) (map[string]*yaml.Node, error) {
	for _, name := range names {
		if node, present := values[name]; present {
			return mappingValues(node)
		}
	}
	return map[string]*yaml.Node{}, nil
}

func exactSingleStringField(
	values map[string]*yaml.Node,
	name string,
) (string, bool, bool) {
	node, present := values[name]
	if !present {
		return "", false, true
	}
	if node.Kind == yaml.ScalarNode {
		value, ok := scalarExact(node)
		return value, true, ok && value != ""
	}
	if node.Kind != yaml.SequenceNode || len(node.Content) != 1 {
		return "", true, false
	}
	value, ok := scalarExact(node.Content[0])
	return value, true, ok && value != ""
}

func stringHeaderMap(node *yaml.Node) (map[string]any, bool) {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nil, false
	}
	for index := 0; index < len(node.Content); index += 2 {
		keyNode := node.Content[index]
		if isYAMLMergeKey(keyNode) {
			return nil, false
		}
		if key, ok := scalarToken(keyNode); !ok || key == "" {
			return nil, false
		}
	}
	values, err := mappingValues(node)
	if err != nil {
		return nil, false
	}
	result := make(map[string]any, len(values))
	for key, valueNode := range values {
		if strings.TrimSpace(key) != key || key == "" {
			return nil, false
		}
		if value, ok := scalarExact(valueNode); ok {
			result[key] = value
			continue
		}
		if valueNode.Kind == yaml.SequenceNode {
			items := make([]string, 0, len(valueNode.Content))
			for _, itemNode := range valueNode.Content {
				item, ok := scalarExact(itemNode)
				if !ok {
					return nil, false
				}
				items = append(items, item)
			}
			result[key] = items
			continue
		}
		return nil, false
	}
	return result, true
}

func httpHeaderMap(node *yaml.Node) (map[string]any, []string, bool) {
	headers, ok := stringHeaderMap(node)
	if !ok {
		return nil, nil, false
	}
	var hosts []string
	for key, value := range headers {
		if !strings.EqualFold(key, "Host") {
			continue
		}
		if hosts != nil {
			return nil, nil, false
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != typed || typed == "" {
				return nil, nil, false
			}
			hosts = []string{typed}
		case []string:
			if len(typed) == 0 {
				return nil, nil, false
			}
			for _, host := range typed {
				if strings.TrimSpace(host) != host || host == "" {
					return nil, nil, false
				}
			}
			hosts = typed
		default:
			return nil, nil, false
		}
		delete(headers, key)
	}
	return headers, hosts, true
}
