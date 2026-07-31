package main

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type yamlFrame struct {
	node  *yaml.Node
	depth int
	leave bool
}

func parseBoundedYAML(raw []byte) (*yaml.Node, error) {
	if err := validateYAMLPredecodeBudget(raw); err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, normalizeError("yaml_invalid", FormatClashYAML)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, normalizeError("multiple_yaml_documents", FormatClashYAML)
		}
		return nil, normalizeError("yaml_invalid", FormatClashYAML)
	}
	if len(document.Content) != 1 {
		return nil, normalizeError("clash_root_invalid", FormatClashYAML)
	}
	rawNodes, err := validateRawYAML(&document)
	if err != nil {
		return nil, err
	}
	if err := validateExpandedYAML(&document, rawNodes); err != nil {
		return nil, err
	}
	cloned, err := cloneAliasFree(&document, make(map[*yaml.Node]bool))
	if err != nil {
		return nil, err
	}
	return cloned, nil
}

// validateYAMLPredecodeBudget limits yaml.v3 parser exposure before it builds
// an AST. It deliberately counts indicators inside comments, quotes and block
// scalars too, so ambiguous lexical context can only overcount.
//
// For a valid nonempty document with I counted indicators, the raw yaml.Node
// graph is bounded by N <= 2 + 3*I. The two uncharged nodes are the document
// and its possible indicator-free root plain scalar. Every collection, alias,
// quoted/block scalar, or implicit collection child requires an indicator; a
// single indicator can introduce at most a collection plus an empty key and
// empty value. Thus I <= MaxYAMLNodes bounds pre-decode AST exposure to
// 2 + 3*MaxYAMLNodes. The exact post-decode MaxYAMLNodes check remains below.
// The scan uses yaml.v3's complete valid-document break set (CR, LF, NEL, LS
// and PS), so block-entry separation, document-marker boundaries and
// line-start tracking charge the same syntax the parser recognizes.
func validateYAMLPredecodeBudget(raw []byte) error {
	indicators := 0
	lineStart := true
	addIndicators := func(count int) error {
		indicators += count
		if indicators > MaxYAMLNodes {
			return normalizeError("too_many_document_nodes", FormatClashYAML)
		}
		return nil
	}

	for index := 0; index < len(raw); {
		character, size := utf8.DecodeRune(raw[index:])
		if lineStart && index+3 <= len(raw) &&
			isYAMLDocumentMarker(raw[index:index+3]) &&
			(index+3 == len(raw) ||
				isYAMLBlankOrBreak(decodeYAMLRune(raw, index+3))) {
			if err := addIndicators(3); err != nil {
				return err
			}
			index += 3
			lineStart = false
			continue
		}

		count := false
		switch character {
		case '?', ':', ',', '[', ']', '{', '}', '#', '&', '*', '!',
			'|', '>', '\'', '"', '%', '@', '`':
			count = true
		case '-':
			count = index+size == len(raw) ||
				isYAMLBlankOrBreak(decodeYAMLRune(raw, index+size))
		}
		if count {
			if err := addIndicators(1); err != nil {
				return err
			}
		}
		lineStart = isYAMLBreak(character)
		index += size
	}
	return nil
}

func isYAMLDocumentMarker(value []byte) bool {
	return bytes.Equal(value, []byte("---")) ||
		bytes.Equal(value, []byte("..."))
}

func decodeYAMLRune(raw []byte, index int) rune {
	character, _ := utf8.DecodeRune(raw[index:])
	return character
}

func isYAMLBlankOrBreak(character rune) bool {
	return character == ' ' || character == '\t' || isYAMLBreak(character)
}

func isYAMLBreak(character rune) bool {
	switch character {
	case '\r', '\n', '\u0085', '\u2028', '\u2029':
		return true
	default:
		return false
	}
}

func validateRawYAML(document *yaml.Node) (map[*yaml.Node]bool, error) {
	nodes := make(map[*yaml.Node]bool)
	stack := []yamlFrame{{node: document, depth: 0}}
	count := 0
	aliases := 0
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node := frame.node
		if node == nil {
			return nil, normalizeError("yaml_invalid", FormatClashYAML)
		}
		count++
		if count > MaxYAMLNodes {
			return nil, normalizeError("too_many_document_nodes", FormatClashYAML)
		}
		if frame.depth > MaxDocumentDepth {
			return nil, normalizeError("document_too_deep", FormatClashYAML)
		}
		nodes[node] = true
		if node.Kind == yaml.ScalarNode && len(node.Value) > MaxScalarBytes {
			return nil, normalizeError("scalar_too_large", FormatClashYAML)
		}
		if node.Kind == yaml.MappingNode {
			if err := validateRawMappingKeys(node); err != nil {
				return nil, err
			}
		}
		if node.Kind == yaml.AliasNode {
			aliases++
			if aliases > MaxAliases {
				return nil, normalizeError("too_many_aliases", FormatClashYAML)
			}
			continue
		}
		for index := len(node.Content) - 1; index >= 0; index-- {
			stack = append(stack, yamlFrame{
				node: node.Content[index], depth: frame.depth + 1,
			})
		}
	}
	for node := range nodes {
		if node.Kind == yaml.AliasNode && (node.Alias == nil || !nodes[node.Alias]) {
			return nil, normalizeError("alias_target_invalid", FormatClashYAML)
		}
	}
	return nodes, nil
}

func validateRawMappingKeys(node *yaml.Node) error {
	if len(node.Content)%2 != 0 {
		return normalizeError("yaml_invalid", FormatClashYAML)
	}
	seen := make(map[string]bool)
	mergeSeen := false
	for index := 0; index < len(node.Content); index += 2 {
		keyNode := node.Content[index]
		if keyNode == nil || keyNode.Kind != yaml.ScalarNode {
			return normalizeError("yaml_invalid", FormatClashYAML)
		}
		if isYAMLMergeKey(keyNode) {
			if mergeSeen {
				return normalizeError("yaml_invalid", FormatClashYAML)
			}
			mergeSeen = true
			continue
		}
		identity := keyNode.ShortTag() + "\x00" + keyNode.Value
		if seen[identity] {
			return normalizeError("yaml_invalid", FormatClashYAML)
		}
		seen[identity] = true
	}
	return nil
}

func validateExpandedYAML(document *yaml.Node, rawNodes map[*yaml.Node]bool) error {
	active := make(map[*yaml.Node]bool)
	stack := []yamlFrame{{node: document, depth: 0}}
	count := 0
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if frame.leave {
			delete(active, frame.node)
			continue
		}
		node := frame.node
		if node.Kind == yaml.AliasNode {
			if node.Alias == nil || !rawNodes[node.Alias] {
				return normalizeError("alias_target_invalid", FormatClashYAML)
			}
			node = node.Alias
		}
		if active[node] {
			return normalizeError("alias_cycle", FormatClashYAML)
		}
		if frame.depth > MaxDocumentDepth {
			return normalizeError("document_too_deep", FormatClashYAML)
		}
		count++
		if count > MaxExpandedNodes {
			return normalizeError("too_many_expanded_nodes", FormatClashYAML)
		}
		active[node] = true
		stack = append(stack, yamlFrame{node: node, leave: true})
		for index := len(node.Content) - 1; index >= 0; index-- {
			stack = append(stack, yamlFrame{
				node: node.Content[index], depth: frame.depth + 1,
			})
		}
	}
	return nil
}

func cloneAliasFree(node *yaml.Node, active map[*yaml.Node]bool) (*yaml.Node, error) {
	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node == nil || active[node] {
		return nil, normalizeError("alias_cycle", FormatClashYAML)
	}
	active[node] = true
	defer delete(active, node)

	cloned := &yaml.Node{
		Kind:        node.Kind,
		Style:       node.Style,
		Tag:         node.Tag,
		Value:       node.Value,
		HeadComment: node.HeadComment,
		LineComment: node.LineComment,
		FootComment: node.FootComment,
		Line:        node.Line,
		Column:      node.Column,
	}
	cloned.Content = make([]*yaml.Node, 0, len(node.Content))
	for _, child := range node.Content {
		copyChild, err := cloneAliasFree(child, active)
		if err != nil {
			return nil, err
		}
		cloned.Content = append(cloned.Content, copyChild)
	}
	return cloned, nil
}

func mappingValues(node *yaml.Node) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nil, normalizeError("yaml_invalid", FormatClashYAML)
	}
	result := make(map[string]*yaml.Node)
	explicit := make(map[string]*yaml.Node)
	mergeSeen := false
	for index := 0; index < len(node.Content); index += 2 {
		keyNode := node.Content[index]
		valueNode := node.Content[index+1]
		if keyNode.Kind != yaml.ScalarNode {
			return nil, normalizeError("yaml_invalid", FormatClashYAML)
		}
		key := keyNode.Value
		if isYAMLMergeKey(keyNode) {
			if mergeSeen {
				return nil, normalizeError("yaml_invalid", FormatClashYAML)
			}
			mergeSeen = true
			if err := mergeValues(result, valueNode); err != nil {
				return nil, err
			}
			continue
		}
		if keyNode.ShortTag() != "!!str" {
			return nil, normalizeError("yaml_invalid", FormatClashYAML)
		}
		if _, duplicate := explicit[key]; duplicate {
			return nil, normalizeError("yaml_invalid", FormatClashYAML)
		}
		explicit[key] = valueNode
	}
	for key, value := range explicit {
		result[key] = value
	}
	return result, nil
}

func isYAMLMergeKey(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.ShortTag() == "!!merge"
}

func mergeValues(destination map[string]*yaml.Node, node *yaml.Node) error {
	switch node.Kind {
	case yaml.MappingNode:
		values, err := mappingValues(node)
		if err != nil {
			return err
		}
		for key, value := range values {
			if _, exists := destination[key]; !exists {
				destination[key] = value
			}
		}
		return nil
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := mergeValues(destination, child); err != nil {
				return err
			}
		}
		return nil
	default:
		return normalizeError("yaml_invalid", FormatClashYAML)
	}
}

func scalarExact(node *yaml.Node) (string, bool) {
	if node == nil || node.Kind != yaml.ScalarNode || node.ShortTag() != "!!str" {
		return "", false
	}
	for _, character := range node.Value {
		if unicode.IsControl(character) {
			return "", false
		}
	}
	return node.Value, true
}

func scalarToken(node *yaml.Node) (string, bool) {
	value, ok := scalarExact(node)
	if !ok || strings.TrimSpace(value) != value {
		return "", false
	}
	return value, true
}

func scalarDecimal(node *yaml.Node) (string, bool) {
	if node == nil || node.Kind != yaml.ScalarNode {
		return "", false
	}
	switch node.ShortTag() {
	case "!!int":
		value := node.Value
		if len(value) > 1 && value[0] == '0' {
			return "", false
		}
		for _, character := range value {
			if character < '0' || character > '9' {
				return "", false
			}
		}
		return value, value != ""
	case "!!str":
		value, ok := scalarToken(node)
		if !ok || value == "" {
			return "", false
		}
		for _, character := range value {
			if character < '0' || character > '9' {
				return "", false
			}
		}
		return value, true
	default:
		return "", false
	}
}

func scalarBool(node *yaml.Node) (bool, bool) {
	if node == nil || node.Kind != yaml.ScalarNode {
		return false, false
	}
	switch node.ShortTag() {
	case "!!bool":
		switch strings.ToLower(node.Value) {
		case "true":
			return true, true
		case "false":
			return false, true
		default:
			return false, false
		}
	case "!!str":
		value, ok := scalarToken(node)
		if !ok {
			return false, false
		}
		switch value {
		case "true":
			return true, true
		case "false":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func stringField(values map[string]*yaml.Node, names ...string) (string, bool) {
	value, present, valid := stringFieldStatus(values, names...)
	return value, present && valid
}

func stringFieldStatus(
	values map[string]*yaml.Node,
	names ...string,
) (string, bool, bool) {
	for _, name := range names {
		if node, exists := values[name]; exists {
			value, ok := scalarExact(node)
			return value, true, ok
		}
	}
	return "", false, true
}

func tokenField(values map[string]*yaml.Node, names ...string) (string, bool) {
	value, present, valid := tokenFieldStatus(values, names...)
	return value, present && valid
}

func tokenFieldStatus(
	values map[string]*yaml.Node,
	names ...string,
) (string, bool, bool) {
	for _, name := range names {
		if node, exists := values[name]; exists {
			value, ok := scalarToken(node)
			return value, true, ok
		}
	}
	return "", false, true
}

func boolField(values map[string]*yaml.Node, names ...string) (bool, bool, bool) {
	for _, name := range names {
		node, exists := values[name]
		if !exists {
			continue
		}
		value, ok := scalarBool(node)
		if !ok {
			return false, true, false
		}
		return value, true, true
	}
	return false, false, true
}

func intField(values map[string]*yaml.Node, names ...string) (int, bool, bool) {
	for _, name := range names {
		node, present := values[name]
		if !present {
			continue
		}
		value, ok := scalarDecimal(node)
		if !ok {
			return 0, true, false
		}
		parsed, err := strconv.Atoi(value)
		return parsed, true, err == nil
	}
	return 0, false, true
}

func stringListField(values map[string]*yaml.Node, names ...string) ([]string, bool, bool) {
	var node *yaml.Node
	present := false
	for _, name := range names {
		if value, exists := values[name]; exists {
			node = value
			present = true
			break
		}
	}
	if !present {
		return nil, false, true
	}
	if node.Kind != yaml.SequenceNode {
		return nil, true, false
	}
	result := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		value, ok := scalarToken(item)
		if !ok || value == "" {
			return nil, true, false
		}
		result = append(result, value)
	}
	return result, true, true
}

func nonEmptyField(values map[string]*yaml.Node, names ...string) bool {
	for _, name := range names {
		node, present := values[name]
		if !present {
			continue
		}
		value, scalar := scalarExact(node)
		if !scalar || value != "" {
			return true
		}
	}
	return false
}
