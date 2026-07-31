package main

import (
	"errors"
	"fmt"
)

const (
	MaxInputBytes      = 32 * 1024 * 1024
	MaxDocumentDepth   = 64
	MaxScalarBytes     = 64 * 1024
	MaxYAMLNodes       = 131072
	MaxAliases         = 256
	MaxExpandedNodes   = 131072
	MaxNormalizedNodes = 8192
	MaxWarningSamples  = 8
)

type Format string

const (
	FormatUnknown     Format = "unknown"
	FormatSingBoxJSON Format = "singbox-json"
	FormatBase64URI   Format = "base64-uri-list"
	FormatPlainURI    Format = "plain-uri-list"
	FormatClashYAML   Format = "clash-yaml"
)

type Warning struct {
	Code      string `json:"code"`
	NodeIndex int    `json:"node_index"`
	Type      string `json:"type,omitempty"`
	Field     string `json:"field,omitempty"`
}

type NormalizeInfo struct {
	Format   Format    `json:"format"`
	Accepted int       `json:"accepted"`
	Skipped  int       `json:"skipped"`
	Warnings []Warning `json:"warnings,omitempty"`
}

var ErrNoValidNode = errors.New("no valid node")

type NormalizeError struct {
	Code      string
	Format    Format
	NodeIndex int
	Type      string
	Field     string
	cause     error
}

func (e *NormalizeError) Error() string {
	message := "normalize: code=" + safeCode(e.Code)
	if e.Format != "" && e.Format != FormatUnknown {
		message += " format=" + string(e.Format)
	}
	if e.NodeIndex > 0 {
		message += fmt.Sprintf(" node_index=%d", e.NodeIndex)
	}
	if e.Type != "" {
		message += " type=" + safeType(e.Type)
	}
	if e.Field != "" {
		message += " field=" + safeField(e.Field)
	}
	return message
}

func (e *NormalizeError) Unwrap() error {
	return e.cause
}

func normalizeError(code string, format Format) *NormalizeError {
	return &NormalizeError{Code: safeCode(code), Format: format}
}

func noValidNodeError(format Format) *NormalizeError {
	return &NormalizeError{
		Code:   "no_valid_node",
		Format: format,
		cause:  ErrNoValidNode,
	}
}

func safeCode(value string) string {
	switch value {
	case "empty_input",
		"input_too_large",
		"unknown_format",
		"json_invalid",
		"json_root_invalid",
		"json_outbounds_invalid",
		"native_outbound_invalid",
		"native_reference_unsupported",
		"document_too_deep",
		"scalar_too_large",
		"too_many_document_nodes",
		"too_many_aliases",
		"alias_target_invalid",
		"alias_cycle",
		"too_many_expanded_nodes",
		"multiple_yaml_documents",
		"yaml_invalid",
		"clash_root_invalid",
		"clash_proxies_invalid",
		"too_many_outbounds",
		"no_valid_node",
		"output_encode_failed",
		"input_read_failed",
		"usage_error":
		return value
	default:
		return "normalize_failed"
	}
}

func safeType(value string) string {
	switch value {
	case "shadowsocks", "ss", "vmess", "vless", "trojan",
		"hysteria2", "hy2", "tuic", "anytls", "socks", "socks5",
		"ssr", "wireguard", "direct", "block":
		return value
	default:
		return "unknown"
	}
}

func safeField(value string) string {
	switch value {
	case "document", "outbounds", "proxies", "name", "tag", "type", "server", "port",
		"dialer-proxy", "udp", "cipher", "plugin", "uuid", "alterId",
		"encryption", "flow", "password", "token", "tls", "sni", "servername",
		"fingerprint", "client-fingerprint", "alpn", "reality-opts",
		"public-key", "short-id", "network", "transport", "ws-opts",
		"grpc-opts", "http-opts", "grpc-user-agent", "ss-opts", "obfs",
		"realm", "ports", "hop-interval", "username", "references",
		"alter-id", "auth", "h2-opts", "quic-opts", "path", "host",
		"headers", "method", "service-name", "max-early-data",
		"early-data-header-name", "congestion-controller", "congestion_control",
		"udp-relay-mode", "udp_relay_mode":
		return value
	default:
		return "field"
	}
}

func (info *NormalizeInfo) skip(index int, nodeType, code, field string) {
	info.Skipped++
	if len(info.Warnings) >= MaxWarningSamples {
		return
	}
	info.Warnings = append(info.Warnings, Warning{
		Code:      safeWarningCode(code),
		NodeIndex: index,
		Type:      safeType(nodeType),
		Field:     safeField(field),
	})
}

func safeWarningCode(value string) string {
	switch value {
	case "node_not_mapping",
		"missing_field",
		"invalid_field",
		"unsupported_protocol",
		"unsupported_cipher",
		"unsupported_plugin",
		"unsupported_transport",
		"unsupported_tls_option",
		"unsupported_reference",
		"unsupported_encryption",
		"unsupported_flow",
		"unsupported_tuic_v4",
		"unsupported_socks_tls",
		"unsupported_hysteria2_option",
		"unsupported_field",
		"parse_failed":
		return value
	default:
		return "node_skipped"
	}
}
