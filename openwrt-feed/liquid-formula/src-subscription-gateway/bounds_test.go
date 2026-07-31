package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const validProxyYAML = "proxies:\n" +
	"  - name: valid\n" +
	"    type: ss\n" +
	"    server: 192.0.2.1\n" +
	"    port: 8388\n" +
	"    cipher: aes-128-gcm\n" +
	"    password: p\n"

func TestInclusiveInputScalarDepthAndOutboundLimits(t *testing.T) {
	t.Run("input equal accepted", func(t *testing.T) {
		raw := append([]byte(validProxyYAML), bytes.Repeat(
			[]byte{' '}, 32*1024*1024-len(validProxyYAML))...)
		_, _, err := NormalizeDocument(raw)
		if err != nil {
			t.Fatalf("exact 32 MiB input rejected: %v", err)
		}
	})

	t.Run("scalar equal accepted", func(t *testing.T) {
		raw := []byte(validProxyYAML + "padding: " + strings.Repeat("x", 64*1024) + "\n")
		_, _, err := NormalizeDocument(raw)
		if err != nil {
			t.Fatalf("exact 64 KiB scalar rejected: %v", err)
		}
	})

	t.Run("depth equal accepted", func(t *testing.T) {
		// Document is depth 0, the root mapping depth 1 and this scalar depth 64.
		raw := []byte(validProxyYAML + "padding: " +
			strings.Repeat("[", 62) + "x" + strings.Repeat("]", 62) + "\n")
		_, _, err := NormalizeDocument(raw)
		if err != nil {
			t.Fatalf("depth 64 rejected: %v", err)
		}
	})

	t.Run("8192 outbounds accepted", func(t *testing.T) {
		raw := repeatedValidProxies(8192)
		output, info, err := NormalizeDocument([]byte(raw))
		if err != nil {
			t.Fatalf("8192 outbounds rejected: %v", err)
		}
		if info.Accepted != 8192 || len(output) == 0 {
			t.Fatalf("unexpected result: %#v", info)
		}
	})
}

func TestRawYAMLNodeLimitIsInclusive(t *testing.T) {
	const rawNodeLimit = 131072
	atLimit := rawNodeLimitDocument(rawNodeLimit)
	if got := countRawNodesForTest(t, atLimit); got != rawNodeLimit {
		t.Fatalf("test fixture has %d raw nodes, want %d", got, rawNodeLimit)
	}
	if _, _, err := NormalizeDocument([]byte(atLimit)); err != nil {
		t.Fatalf("exact raw-node limit rejected: %v", err)
	}

	overLimit := rawNodeLimitDocument(rawNodeLimit + 1)
	if got := countRawNodesForTest(t, overLimit); got != rawNodeLimit+1 {
		t.Fatalf("test fixture has %d raw nodes, want %d", got, rawNodeLimit+1)
	}
	_, _, err := NormalizeDocument([]byte(overLimit))
	requireErrorCode(t, err, "too_many_document_nodes")
}

func TestExpandedYAMLNodeLimitIsInclusive(t *testing.T) {
	const expandedNodeLimit = 131072
	atLimit := expandedNodeLimitDocument(236)
	if got := countExpandedNodesForTest(t, atLimit); got != expandedNodeLimit {
		t.Fatalf("test fixture has %d expanded nodes, want %d", got, expandedNodeLimit)
	}
	_, info, err := NormalizeDocument([]byte(atLimit))
	if err != nil {
		t.Fatalf("exact expanded-node limit rejected: %v", err)
	}
	if info.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1", info.Accepted)
	}

	overLimit := expandedNodeLimitDocument(237)
	if got := countExpandedNodesForTest(t, overLimit); got != expandedNodeLimit+1 {
		t.Fatalf("test fixture has %d expanded nodes, want %d", got, expandedNodeLimit+1)
	}
	_, _, err = NormalizeDocument([]byte(overLimit))
	requireErrorCode(t, err, "too_many_expanded_nodes")
}

func TestAliasBoundaryCyclesAndTargets(t *testing.T) {
	t.Run("256 aliases accepted", func(t *testing.T) {
		var raw strings.Builder
		raw.WriteString("base: &base {name: alias, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}\n")
		raw.WriteString("proxies:\n")
		for index := 0; index < 256; index++ {
			raw.WriteString("  - *base\n")
		}
		_, info, err := NormalizeDocument([]byte(raw.String()))
		if err != nil {
			t.Fatalf("256 aliases rejected: %v", err)
		}
		if info.Accepted != 256 {
			t.Fatalf("accepted = %d, want 256", info.Accepted)
		}
	})

	t.Run("self cycle rejected", func(t *testing.T) {
		raw := []byte("loop: &loop [*loop]\n" + validProxyYAML)
		_, _, err := NormalizeDocument(raw)
		requireErrorCode(t, err, "alias_cycle")
	})

	t.Run("alias target outside raw tree rejected", func(t *testing.T) {
		external := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "external"}
		alias := &yaml.Node{Kind: yaml.AliasNode, Alias: external}
		document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{alias}}
		_, err := validateRawYAML(document)
		requireErrorCode(t, err, "alias_target_invalid")
	})
}

func TestYAMLDocumentShapeAmbiguitiesAreRejected(t *testing.T) {
	for name, raw := range map[string]string{
		"nonempty second document": validProxyYAML + "---\nproxies: []\n",
		"empty second document":    validProxyYAML + "---\n",
		"duplicate mapping key":    validProxyYAML + "padding: one\npadding: two\n",
		"complex mapping key":      validProxyYAML + "? [complex, key]\n: value\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := NormalizeDocument([]byte(raw))
			if err == nil {
				t.Fatal("ambiguous YAML unexpectedly succeeded")
			}
			var normalizeErr *NormalizeError
			if !strings.Contains(err.Error(), "code=") ||
				!strings.Contains(err.Error(), "format=clash-yaml") {
				t.Fatalf("unsafe or unstable error: %T %v", normalizeErr, err)
			}
			requireSafeDiagnostic(t, err.Error())
		})
	}
}

func repeatedValidProxies(count int) string {
	var raw strings.Builder
	raw.Grow(count * 105)
	raw.WriteString("proxies:\n")
	for index := 0; index < count; index++ {
		fmt.Fprintf(&raw,
			"  - {name: n%d, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}\n",
			index)
	}
	return raw.String()
}

func rawNodeLimitDocument(totalNodes int) string {
	// Document + root mapping + proxies key/sequence + one six-field proxy
	// mapping + padding key/sequence consume 19 nodes.
	paddingNodes := totalNodes - 19
	var raw strings.Builder
	raw.Grow(len(validProxyYAML) + paddingNodes*6)
	raw.WriteString(validProxyYAML)
	raw.WriteString("padding:\n")
	for index := 0; index < paddingNodes; index++ {
		raw.WriteString("  - x\n")
	}
	return raw.String()
}

func expandedNodeLimitDocument(smallPadding int) string {
	var raw strings.Builder
	raw.WriteString("base: &base\n")
	for index := 0; index < 508; index++ {
		raw.WriteString("  - x\n")
	}
	raw.WriteString("padding:\n")
	for index := 0; index < 256; index++ {
		raw.WriteString("  - *base\n")
	}
	raw.WriteString("  -\n")
	for index := 0; index < smallPadding; index++ {
		raw.WriteString("    - x\n")
	}
	raw.WriteString("marker: x\n")
	raw.WriteString(validProxyYAML)
	return raw.String()
}

func countRawNodesForTest(t *testing.T, raw string) int {
	t.Helper()
	document := decodeYAMLForTest(t, raw)
	count := 0
	stack := []*yaml.Node{document}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		count++
		if node.Kind != yaml.AliasNode {
			stack = append(stack, node.Content...)
		}
	}
	return count
}

func countExpandedNodesForTest(t *testing.T, raw string) int {
	t.Helper()
	document := decodeYAMLForTest(t, raw)
	count := 0
	stack := []*yaml.Node{document}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		count++
		stack = append(stack, node.Content...)
	}
	return count
}

func decodeYAMLForTest(t *testing.T, raw string) *yaml.Node {
	t.Helper()
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode generated YAML: %v", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("generated fixture contains another document: %v", err)
	}
	return &document
}
