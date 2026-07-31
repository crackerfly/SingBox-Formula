package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestYAMLv3LineBreaksAreBudgetedConsistently(t *testing.T) {
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

	for _, lineBreak := range breaks {
		t.Run(lineBreak.name, func(t *testing.T) {
			assertYAMLv3RecognizesLineBreak(t, lineBreak.value)

			atEntryLimit := []byte(strings.Repeat(
				"-"+lineBreak.value,
				MaxYAMLNodes,
			))
			if err := validateYAMLPredecodeBudget(atEntryLimit); err != nil {
				t.Fatalf("exact block-entry budget rejected: %v", err)
			}
			overEntryLimit := append(
				append([]byte(nil), atEntryLimit...),
				[]byte("-"+lineBreak.value)...,
			)
			requireErrorCode(
				t,
				validateYAMLPredecodeBudget(overEntryLimit),
				"too_many_document_nodes",
			)

			markerCount := MaxYAMLNodes / 3
			atMarkerLimit := []byte(
				strings.Repeat(lineBreak.value+"---"+lineBreak.value, markerCount) +
					"[]",
			)
			if err := validateYAMLPredecodeBudget(atMarkerLimit); err != nil {
				t.Fatalf("exact document-marker budget rejected: %v", err)
			}
			overMarkerLimit := append(
				append([]byte(nil), atMarkerLimit...),
				[]byte(lineBreak.value+"---"+lineBreak.value)...,
			)
			requireErrorCode(
				t,
				validateYAMLPredecodeBudget(overMarkerLimit),
				"too_many_document_nodes",
			)
		})
	}
}

func assertYAMLv3RecognizesLineBreak(t *testing.T, lineBreak string) {
	t.Helper()

	var sequence yaml.Node
	if err := yaml.NewDecoder(
		strings.NewReader("-" + lineBreak + "- value"),
	).Decode(&sequence); err != nil {
		t.Fatalf("yaml.v3 did not parse block entries across the break: %v", err)
	}
	if len(sequence.Content) != 1 ||
		sequence.Content[0].Kind != yaml.SequenceNode ||
		len(sequence.Content[0].Content) != 2 {
		t.Fatalf("break did not delimit two block entries: %#v", sequence.Content)
	}

	decoder := yaml.NewDecoder(
		strings.NewReader("first" + lineBreak + "---" + lineBreak + "second"),
	)
	var first, second yaml.Node
	if err := decoder.Decode(&first); err != nil {
		t.Fatalf("first document decode failed: %v", err)
	}
	if err := decoder.Decode(&second); err != nil {
		t.Fatalf("break did not establish a document-marker line: %v", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("document marker produced unexpected trailing decode: %v", err)
	}
}

func TestURIFragmentQuestionMarksAreNotQueries(t *testing.T) {
	ss := "ss://YWVzLTEyOC1nY206cA@192.0.2.1:8388"
	raw := strings.Join([]string{
		ss + "#exact?note",
		"vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@192.0.2.2:443#tag?udp=false",
		ss + "?udp=false#mapped?udp=true",
	}, "\n")
	nodes, info := decodedOutbounds(t, []byte(raw))
	if info.Accepted != 3 || info.Skipped != 0 || len(nodes) != 3 {
		t.Fatalf("fragment query text changed acceptance: %#v %#v", info, nodes)
	}
	for index, want := range []string{"exact?note", "tag?udp=false", "mapped?udp=true"} {
		if got := nodes[index]["tag"]; got != want {
			t.Fatalf("node %d tag = %#v, want %#v", index, got, want)
		}
	}
	if network, present := nodes[1]["network"]; present && network == "tcp" {
		t.Fatalf("fragment udp=false changed the network: %#v", nodes[1])
	}
	if nodes[2]["network"] != "tcp" {
		t.Fatalf("real query before fragment was not mapped: %#v", nodes[2])
	}
}

func TestUppercaseLegacyWholeBase64SSTagRemainsExact(t *testing.T) {
	payload := base64.RawStdEncoding.EncodeToString(
		[]byte("aes-128-gcm:p@192.0.2.1:8388# Upper? "),
	)
	nodes, info := decodedOutbounds(t, []byte("SS://"+payload))
	if info.Accepted != 1 || info.Skipped != 0 || len(nodes) != 1 {
		t.Fatalf("uppercase legacy SS was rejected: %#v %#v", info, nodes)
	}
	if nodes[0]["tag"] != " Upper? " {
		t.Fatalf("uppercase legacy SS tag changed: %#v", nodes[0])
	}
}

func TestYAMLPredecodeBudgetConservativeTradeoff(t *testing.T) {
	// MINOR compatibility tradeoff: the predecode proof deliberately counts
	// indicators even where YAML syntax makes them non-structural. This may
	// reject unusually punctuation-heavy metadata while keeping parser memory
	// bounded before yaml.v3 constructs an AST.
	indicators := strings.Repeat("[", MaxYAMLNodes+1)
	documents := map[string][]byte{
		"comment": bytes.Join([][]byte{
			[]byte(validProxyYAML),
			[]byte("# " + indicators + "\n"),
		}, nil),
		"quoted scalar": []byte(validProxyYAML +
			`metadata: "` + indicators + "\"\n"),
		"block scalar": []byte(validProxyYAML +
			"metadata: |\n  " + indicators + "\n"),
	}
	for name, raw := range documents {
		t.Run(name, func(t *testing.T) {
			_, _, err := NormalizeDocument(raw)
			requireErrorCode(t, err, "too_many_document_nodes")
			if strings.Contains(err.Error(), indicators[:64]) {
				t.Fatalf("diagnostic exposed raw input: %v", err)
			}
		})
	}
}
