package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxGatewayBudget = int64(2147483647)

const (
	defaultLegacyCacheDirectory = "/var/lib/liquid-formula/cache"
	legacyNodeFileName          = "node.json"
)

type gatewaySource struct {
	URL       string
	URLDigest string
}

type gatewayConfig struct {
	ConfigDigest            string
	ListenAddress           string
	ListenPort              int
	SourceTimeoutSeconds    int64
	AggregateTimeoutSeconds int64
	WriteTimeoutSeconds     int64
	UserAgent               string
	EnabledTemplates        int64
	LegacyNodePath          string
	Sources                 []gatewaySource
}

type gatewayConfigError struct {
	code string
}

func (err *gatewayConfigError) Error() string {
	return "gateway config: code=" + err.code
}

func newGatewayConfigError(code string) *gatewayConfigError {
	return &gatewayConfigError{code: code}
}

func readGatewayConfig(
	path string,
	expectedDigest string,
	readFile func(string) ([]byte, error),
) (gatewayConfig, error) {
	if !isLowerHexDigest(expectedDigest) {
		return gatewayConfig{}, newGatewayConfigError("expected_digest_invalid")
	}
	if readFile == nil {
		return gatewayConfig{}, newGatewayConfigError("config_read_failed")
	}
	raw, err := readFile(path)
	if err != nil {
		return gatewayConfig{}, newGatewayConfigError("config_read_failed")
	}
	sum := sha256.Sum256(raw)
	digest := fmt.Sprintf("%x", sum[:])
	if digest != expectedDigest {
		return gatewayConfig{}, newGatewayConfigError("config_digest_mismatch")
	}
	config, err := parseGatewayConfig(raw)
	if err != nil {
		return gatewayConfig{}, newGatewayConfigError("config_invalid")
	}
	config.ConfigDigest = digest
	return config, nil
}

func parseGatewayConfig(raw []byte) (gatewayConfig, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return gatewayConfig{}, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return gatewayConfig{}, newGatewayConfigError("multiple_documents")
		}
		return gatewayConfig{}, err
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return gatewayConfig{}, newGatewayConfigError("document_invalid")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return gatewayConfig{}, newGatewayConfigError("root_invalid")
	}
	if err := validateYAMLTree(root); err != nil {
		return gatewayConfig{}, err
	}

	server, ok := mappingValue(root, "server")
	if !ok || server.Kind != yaml.MappingNode {
		return gatewayConfig{}, newGatewayConfigError("server_invalid")
	}
	subscription, ok := mappingValue(root, "subscription")
	if !ok || subscription.Kind != yaml.MappingNode {
		return gatewayConfig{}, newGatewayConfigError("subscription_invalid")
	}
	gateway, ok := mappingValue(root, "liquid_formula_gateway")
	if !ok || gateway.Kind != yaml.MappingNode {
		return gatewayConfig{}, newGatewayConfigError("gateway_missing")
	}

	converterPort, err := requiredDecimal(server, "port")
	if err != nil || converterPort < 1 || converterPort > 65535 {
		return gatewayConfig{}, newGatewayConfigError("converter_port_invalid")
	}
	writeTimeout, err := requiredDecimal(server, "write_timeout")
	if err != nil {
		return gatewayConfig{}, newGatewayConfigError("write_timeout_invalid")
	}
	subscriptionURL, err := requiredString(subscription, "url")
	if err != nil {
		return gatewayConfig{}, newGatewayConfigError("subscription_url_invalid")
	}
	subscriptionUserAgent, err := requiredString(subscription, "user_agent")
	if err != nil || !validGatewayUserAgent(subscriptionUserAgent) {
		return gatewayConfig{}, newGatewayConfigError("subscription_user_agent_invalid")
	}
	subscriptionTimeout, err := requiredDecimal(subscription, "timeout")
	if err != nil {
		return gatewayConfig{}, newGatewayConfigError("subscription_timeout_invalid")
	}

	enabledTemplates, err := countEnabledTemplates(root)
	if err != nil {
		return gatewayConfig{}, err
	}
	gatewayFields, err := exactGatewayFields(gateway)
	if err != nil {
		return gatewayConfig{}, err
	}
	listenAddress, err := gatewayScalarString(gatewayFields["listen_address"])
	if err != nil || listenAddress != "127.0.0.1" {
		return gatewayConfig{}, newGatewayConfigError("listen_address_invalid")
	}
	listenPort, err := gatewayScalarDecimal(gatewayFields["listen_port"])
	if err != nil || listenPort < 1 || listenPort > 65535 {
		return gatewayConfig{}, newGatewayConfigError("listen_port_invalid")
	}
	sourceTimeout, err := gatewayScalarDecimal(gatewayFields["source_timeout"])
	if err != nil {
		return gatewayConfig{}, newGatewayConfigError("source_timeout_invalid")
	}
	aggregateTimeout, err := gatewayScalarDecimal(gatewayFields["aggregate_timeout"])
	if err != nil {
		return gatewayConfig{}, newGatewayConfigError("aggregate_timeout_invalid")
	}
	gatewayUserAgent, err := gatewayScalarString(gatewayFields["user_agent"])
	if err != nil || !validGatewayUserAgent(gatewayUserAgent) {
		return gatewayConfig{}, newGatewayConfigError("gateway_user_agent_invalid")
	}
	sources, err := parseGatewaySources(gatewayFields["urls"])
	if err != nil {
		return gatewayConfig{}, err
	}
	legacyNodePath, err := gatewayLegacyNodePath(root)
	if err != nil {
		return gatewayConfig{}, err
	}

	wantAggregate, wantRequest, err := calculateGatewayBudgets(
		int64(len(sources)),
		sourceTimeout,
		enabledTemplates,
	)
	if err != nil {
		return gatewayConfig{}, err
	}
	wantPort := converterPort + 1
	if converterPort == 65535 {
		wantPort = 65534
	}
	wantURL := fmt.Sprintf("http://127.0.0.1:%d/v1/aggregate", wantPort)
	if listenPort != wantPort ||
		subscriptionURL != wantURL ||
		subscriptionTimeout != wantAggregate ||
		aggregateTimeout != wantAggregate ||
		writeTimeout != wantRequest ||
		gatewayUserAgent != subscriptionUserAgent {
		return gatewayConfig{}, newGatewayConfigError("config_disagreement")
	}

	return gatewayConfig{
		ListenAddress:           listenAddress,
		ListenPort:              int(listenPort),
		SourceTimeoutSeconds:    sourceTimeout,
		AggregateTimeoutSeconds: aggregateTimeout,
		WriteTimeoutSeconds:     writeTimeout,
		UserAgent:               gatewayUserAgent,
		EnabledTemplates:        enabledTemplates,
		LegacyNodePath:          legacyNodePath,
		Sources:                 sources,
	}, nil
}

func gatewayLegacyNodePath(root *yaml.Node) (string, error) {
	cache, ok := mappingValue(root, "cache")
	if !ok {
		return filepath.Join(
			defaultLegacyCacheDirectory,
			legacyNodeFileName,
		), nil
	}
	if cache.Kind != yaml.MappingNode {
		return "", newGatewayConfigError("cache_invalid")
	}
	directory, err := requiredString(cache, "directory")
	if err != nil ||
		directory == "" ||
		!filepath.IsAbs(directory) ||
		!validGatewayFilesystemPath(directory) {
		return "", newGatewayConfigError("cache_directory_invalid")
	}
	nodeFile, err := requiredString(cache, "node_file")
	if err != nil || nodeFile != legacyNodeFileName {
		return "", newGatewayConfigError("cache_node_file_invalid")
	}
	return filepath.Join(directory, nodeFile), nil
}

func validGatewayFilesystemPath(path string) bool {
	for _, character := range []byte(path) {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func calculateGatewayBudgets(
	sourceOccurrences int64,
	sourceTimeout int64,
	enabledTemplates int64,
) (int64, int64, error) {
	if sourceOccurrences < 0 || sourceOccurrences > 8 ||
		sourceTimeout < 5 || sourceTimeout > 600 ||
		enabledTemplates < 0 {
		return 0, 0, newGatewayConfigError("budget_input_invalid")
	}
	sources := sourceOccurrences
	if sources == 0 {
		sources = 1
	}
	sourceBudget, err := checkedBudgetMultiply(sources, sourceTimeout)
	if err != nil {
		return 0, 0, err
	}
	aggregateBudget, err := checkedBudgetAdd(sourceBudget, 60)
	if err != nil {
		return 0, 0, err
	}
	templateBudget, err := checkedBudgetMultiply(enabledTemplates, sourceTimeout)
	if err != nil {
		return 0, 0, err
	}
	requestBeforeMargin, err := checkedBudgetAdd(aggregateBudget, templateBudget)
	if err != nil {
		return 0, 0, err
	}
	requestBudget, err := checkedBudgetAdd(requestBeforeMargin, 60)
	if err != nil {
		return 0, 0, err
	}
	return aggregateBudget, requestBudget, nil
}

func checkedBudgetAdd(left, right int64) (int64, error) {
	if left < 0 || right < 0 ||
		left > maxGatewayBudget || right > maxGatewayBudget ||
		left > maxGatewayBudget-right {
		return 0, newGatewayConfigError("budget_overflow")
	}
	return left + right, nil
}

func checkedBudgetMultiply(left, right int64) (int64, error) {
	if left < 0 || right < 0 ||
		left > maxGatewayBudget || right > maxGatewayBudget {
		return 0, newGatewayConfigError("budget_overflow")
	}
	if left != 0 && right > maxGatewayBudget/left {
		return 0, newGatewayConfigError("budget_overflow")
	}
	return left * right, nil
}

func countEnabledTemplates(root *yaml.Node) (int64, error) {
	templates, ok := mappingValue(root, "templates")
	if !ok {
		return 0, nil
	}
	if templates.Kind != yaml.MappingNode {
		return 0, newGatewayConfigError("templates_invalid")
	}
	var enabled int64
	for index := 0; index < len(templates.Content); index += 2 {
		template := templates.Content[index+1]
		if template.Kind != yaml.MappingNode {
			return 0, newGatewayConfigError("template_invalid")
		}
		enabledNode, ok := mappingValue(template, "enabled")
		if !ok {
			continue
		}
		value, err := gatewayScalarBool(enabledNode)
		if err != nil {
			return 0, newGatewayConfigError("template_enabled_invalid")
		}
		if value {
			enabled, err = checkedBudgetAdd(enabled, 1)
			if err != nil {
				return 0, err
			}
		}
	}
	return enabled, nil
}

func exactGatewayFields(gateway *yaml.Node) (map[string]*yaml.Node, error) {
	required := map[string]bool{
		"listen_address":    false,
		"listen_port":       false,
		"source_timeout":    false,
		"aggregate_timeout": false,
		"user_agent":        false,
		"urls":              false,
	}
	fields := make(map[string]*yaml.Node, len(required))
	for index := 0; index < len(gateway.Content); index += 2 {
		key := gateway.Content[index].Value
		if _, ok := required[key]; !ok {
			return nil, newGatewayConfigError("gateway_key_unknown")
		}
		required[key] = true
		fields[key] = gateway.Content[index+1]
	}
	for _, present := range required {
		if !present {
			return nil, newGatewayConfigError("gateway_key_missing")
		}
	}
	return fields, nil
}

func parseGatewaySources(node *yaml.Node) ([]gatewaySource, error) {
	if node.Kind != yaml.SequenceNode || node.Tag != "!!seq" {
		return nil, newGatewayConfigError("urls_invalid")
	}
	if len(node.Content) > 8 {
		return nil, newGatewayConfigError("too_many_urls")
	}
	sources := make([]gatewaySource, 0, len(node.Content))
	for _, item := range node.Content {
		raw, err := gatewayScalarString(item)
		if err != nil || !validHTTPURL(raw) {
			return nil, newGatewayConfigError("url_invalid")
		}
		sum := sha256.Sum256([]byte(raw))
		sources = append(sources, gatewaySource{
			URL:       raw,
			URLDigest: fmt.Sprintf("%x", sum[:]),
		})
	}
	return sources, nil
}

func validHTTPURL(raw string) bool {
	if raw == "" {
		return false
	}
	for _, char := range []byte(raw) {
		if char <= 0x20 || char == 0x7f {
			return false
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http") ||
		strings.EqualFold(parsed.Scheme, "https")
}

func validGatewayUserAgent(value string) bool {
	if len(value) > 200 {
		return false
	}
	for _, char := range []byte(value) {
		if char < 0x20 || char > 0x7e {
			return false
		}
	}
	return true
}

func validateYAMLTree(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return newGatewayConfigError("yaml_alias_invalid")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" ||
				key.Value == "<<" {
				return newGatewayConfigError("yaml_key_invalid")
			}
			if _, exists := seen[key.Value]; exists {
				return newGatewayConfigError("yaml_key_duplicate")
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLTree(child); err != nil {
			return err
		}
	}
	return nil
}

func mappingValue(mapping *yaml.Node, name string) (*yaml.Node, bool) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, false
	}
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == name {
			return mapping.Content[index+1], true
		}
	}
	return nil, false
}

func requiredString(mapping *yaml.Node, name string) (string, error) {
	node, ok := mappingValue(mapping, name)
	if !ok {
		return "", newGatewayConfigError("field_missing")
	}
	return gatewayScalarString(node)
}

func gatewayScalarString(node *yaml.Node) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", newGatewayConfigError("string_invalid")
	}
	return node.Value, nil
}

func requiredDecimal(mapping *yaml.Node, name string) (int64, error) {
	node, ok := mappingValue(mapping, name)
	if !ok {
		return 0, newGatewayConfigError("field_missing")
	}
	return gatewayScalarDecimal(node)
}

func gatewayScalarDecimal(node *yaml.Node) (int64, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!int" ||
		node.Value == "" {
		return 0, newGatewayConfigError("decimal_invalid")
	}
	raw := []byte(node.Value)
	if raw[0] < '1' || raw[0] > '9' {
		return 0, newGatewayConfigError("decimal_invalid")
	}
	for _, char := range raw[1:] {
		if char < '0' || char > '9' {
			return 0, newGatewayConfigError("decimal_invalid")
		}
	}
	value, err := strconv.ParseInt(node.Value, 10, 64)
	if err != nil {
		return 0, newGatewayConfigError("decimal_invalid")
	}
	return value, nil
}

func gatewayScalarBool(node *yaml.Node) (bool, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return false, newGatewayConfigError("boolean_invalid")
	}
	switch node.Value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, newGatewayConfigError("boolean_invalid")
	}
}

func isLowerHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range []byte(value) {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
