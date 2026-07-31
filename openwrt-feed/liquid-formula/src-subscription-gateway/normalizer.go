package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/haierkeys/singbox-subscribe-convert/internal/subscription"
)

var (
	strictStdBase64    = base64.StdEncoding.Strict()
	strictRawStdBase64 = base64.RawStdEncoding.Strict()
	strictURLBase64    = base64.URLEncoding.Strict()
	strictRawURLBase64 = base64.RawURLEncoding.Strict()
)

func NormalizeDocument(raw []byte) ([]byte, NormalizeInfo, error) {
	info := NormalizeInfo{Format: FormatUnknown}
	if len(raw) > MaxInputBytes {
		return nil, info, normalizeError("input_too_large", FormatUnknown)
	}
	raw = stripBOM(raw)
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, info, normalizeError("empty_input", FormatUnknown)
	}
	if !utf8.Valid(trimmed) {
		return nil, info, normalizeError("unknown_format", FormatUnknown)
	}

	if trimmed[0] == '{' || trimmed[0] == '[' {
		return normalizeNativeJSON(trimmed)
	}

	if decoded, ok := decodeBase64Payload(trimmed); ok {
		decoded = stripBOM(decoded)
		decodedTrimmed := bytes.TrimSpace(decoded)
		if len(decodedTrimmed) > 0 &&
			(decodedTrimmed[0] == '{' || decodedTrimmed[0] == '[') {
			return normalizeNativeJSON(decodedTrimmed)
		}
		body := string(decoded)
		containsURI, scanErr := containsURIScheme(body, FormatBase64URI)
		if scanErr != nil {
			return nil, NormalizeInfo{Format: FormatBase64URI}, scanErr
		}
		if containsURI {
			return normalizeURIList(body, FormatBase64URI)
		}
	}

	text := string(trimmed)
	if looksLikeClash(text) {
		return normalizeClashYAML(trimmed)
	}
	if strings.Contains(text, "://") {
		return normalizeURIList(string(raw), FormatPlainURI)
	}

	// A valid YAML scalar or a control-plane error page is not a subscription.
	return nil, info, normalizeError("unknown_format", FormatUnknown)
}

func stripBOM(raw []byte) []byte {
	return bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
}

func encodeOutbounds(outbounds []map[string]any, info NormalizeInfo) ([]byte, NormalizeInfo, error) {
	if len(outbounds) == 0 {
		return nil, info, noValidNodeError(info.Format)
	}
	if len(outbounds) > MaxNormalizedNodes {
		return nil, info, normalizeError("too_many_outbounds", info.Format)
	}
	info.Accepted = len(outbounds)
	encoded, err := json.MarshalIndent(map[string]any{"outbounds": outbounds}, "", "  ")
	if err != nil {
		return nil, info, normalizeError("output_encode_failed", info.Format)
	}
	return append(encoded, '\n'), info, nil
}

func normalizeNativeJSON(raw []byte) ([]byte, NormalizeInfo, error) {
	info := NormalizeInfo{Format: FormatSingBoxJSON}
	if err := validateJSONBounds(raw); err != nil {
		if normalizeErr, ok := err.(*NormalizeError); ok {
			normalizeErr.Format = info.Format
		}
		return nil, info, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, info, normalizeError("json_invalid", info.Format)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, info, normalizeError("json_invalid", info.Format)
	}
	object, ok := root.(map[string]any)
	if !ok {
		return nil, info, normalizeError("json_root_invalid", info.Format)
	}
	values, ok := object["outbounds"].([]any)
	if !ok || len(values) == 0 {
		return nil, info, normalizeError("json_outbounds_invalid", info.Format)
	}
	if len(values) > MaxNormalizedNodes {
		return nil, info, normalizeError("too_many_outbounds", info.Format)
	}
	outbounds := make([]map[string]any, 0, len(values))
	for index, value := range values {
		node, ok := value.(map[string]any)
		if !ok {
			return nil, info, &NormalizeError{
				Code: "native_outbound_invalid", Format: info.Format,
				NodeIndex: index + 1, Field: "outbounds",
			}
		}
		nodeType, typeOK := node["type"].(string)
		tag, tagPresent := node["tag"]
		if !tagPresent {
			node["tag"] = ""
		} else if _, tagOK := tag.(string); !tagOK {
			return nil, info, &NormalizeError{
				Code: "native_outbound_invalid", Format: info.Format,
				NodeIndex: index + 1, Type: safeType(nodeType), Field: "tag",
			}
		}
		if !typeOK || strings.TrimSpace(nodeType) == "" {
			return nil, info, &NormalizeError{
				Code: "native_outbound_invalid", Format: info.Format,
				NodeIndex: index + 1, Type: safeType(nodeType), Field: "type",
			}
		}
		if nativeHasUnsupportedReference(node) {
			return nil, info, &NormalizeError{
				Code: "native_reference_unsupported", Format: info.Format,
				NodeIndex: index + 1, Type: safeType(nodeType), Field: "references",
			}
		}
		outbounds = append(outbounds, node)
	}
	return encodeOutbounds(outbounds, info)
}

func validateJSONBounds(raw []byte) error {
	if !validJSONUnicodeEscapes(raw) {
		return normalizeError("json_invalid", FormatSingBoxJSON)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := jsonValidationState{}
	if err := validateJSONValue(decoder, 0, &state); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return normalizeError("json_invalid", FormatSingBoxJSON)
	}
	return nil
}

// encoding/json replaces unpaired UTF-16 surrogate escapes with U+FFFD instead
// of rejecting them. Validate escapes in raw JSON strings first so credentials,
// paths, tags and object keys cannot be changed silently during decoding.
func validJSONUnicodeEscapes(raw []byte) bool {
	inString := false
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			index++
			if index >= len(raw) {
				return false
			}
			if raw[index] != 'u' {
				continue
			}
			if index+4 >= len(raw) {
				return false
			}
			codeUnit, ok := parseJSONHexUnit(raw[index+1 : index+5])
			if !ok {
				return false
			}
			index += 4
			switch {
			case codeUnit >= 0xd800 && codeUnit <= 0xdbff:
				if index+6 >= len(raw) ||
					raw[index+1] != '\\' ||
					raw[index+2] != 'u' {
					return false
				}
				low, validLow := parseJSONHexUnit(raw[index+3 : index+7])
				if !validLow || low < 0xdc00 || low > 0xdfff {
					return false
				}
				index += 6
			case codeUnit >= 0xdc00 && codeUnit <= 0xdfff:
				return false
			}
		}
	}
	return true
}

func parseJSONHexUnit(raw []byte) (uint16, bool) {
	if len(raw) != 4 {
		return 0, false
	}
	var value uint16
	for _, character := range raw {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

type jsonValidationState struct {
	nodes int
}

func validateJSONValue(decoder *json.Decoder, depth int, state *jsonValidationState) error {
	token, err := decoder.Token()
	if err != nil {
		return normalizeError("json_invalid", FormatSingBoxJSON)
	}
	if err := validateJSONToken(token, state); err != nil {
		return err
	}

	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	if delim != '{' && delim != '[' {
		return normalizeError("json_invalid", FormatSingBoxJSON)
	}
	depth++
	if depth > MaxDocumentDepth {
		return normalizeError("document_too_deep", FormatSingBoxJSON)
	}

	switch delim {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return normalizeError("json_invalid", FormatSingBoxJSON)
			}
			if err := validateJSONToken(keyToken, state); err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return normalizeError("json_invalid", FormatSingBoxJSON)
			}
			seen[key] = true
			if err := validateJSONValue(decoder, depth, state); err != nil {
				return err
			}
		}
		closeToken, closeErr := decoder.Token()
		if closeErr != nil || closeToken != json.Delim('}') {
			return normalizeError("json_invalid", FormatSingBoxJSON)
		}
		return validateJSONToken(closeToken, state)
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth, state); err != nil {
				return err
			}
		}
		closeToken, closeErr := decoder.Token()
		if closeErr != nil || closeToken != json.Delim(']') {
			return normalizeError("json_invalid", FormatSingBoxJSON)
		}
		return validateJSONToken(closeToken, state)
	default:
		return normalizeError("json_invalid", FormatSingBoxJSON)
	}
}

func validateJSONToken(token any, state *jsonValidationState) error {
	state.nodes++
	if state.nodes > MaxYAMLNodes {
		return normalizeError("too_many_document_nodes", FormatSingBoxJSON)
	}
	switch value := token.(type) {
	case string:
		if len(value) > MaxScalarBytes {
			return normalizeError("scalar_too_large", FormatSingBoxJSON)
		}
	case json.Number:
		if len(value.String()) > MaxScalarBytes {
			return normalizeError("scalar_too_large", FormatSingBoxJSON)
		}
	}
	return nil
}

func nativeHasUnsupportedReference(node map[string]any) bool {
	nodeType, _ := node["type"].(string)
	switch strings.ToLower(nodeType) {
	case "selector", "urltest":
		return true
	}
	stack := []any{node}
	for len(stack) > 0 {
		value := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "detour" {
					text, ok := child.(string)
					if !ok || text != "" {
						return true
					}
				}
				if key == "outbounds" {
					return true
				}
				stack = append(stack, child)
			}
		case []any:
			stack = append(stack, typed...)
		}
	}
	return false
}

func normalizeURIList(body string, format Format) ([]byte, NormalizeInfo, error) {
	info := NormalizeInfo{Format: format}
	outbounds := make([]map[string]any, 0, 16)
	nodeIndex := 0
	err := scanURIRecords(body, format, func(rawLine string, _ int) error {
		framed := strings.TrimSpace(rawLine)
		if framed == "" || strings.HasPrefix(framed, "#") ||
			strings.HasPrefix(framed, "//") {
			return nil
		}
		line := strings.TrimLeftFunc(rawLine, unicode.IsSpace)
		if !strings.Contains(line, "#") {
			line = strings.TrimSpace(line)
		}
		nodeIndex++
		nodeType, code, field := preflightURI(line)
		if code != "" {
			info.skip(nodeIndex, nodeType, code, field)
			return nil
		}
		node, err := subscription.ParseURI(line)
		if err != nil {
			info.skip(nodeIndex, nodeType, "parse_failed", "document")
			return nil
		}
		if !preserveExactURITag(node, line) {
			info.skip(nodeIndex, nodeType, "parse_failed", "document")
			return nil
		}
		if code, field := validateParsedURI(node, line); code != "" {
			info.skip(nodeIndex, nodeType, code, field)
			return nil
		}
		applyURICompatibility(node, line)
		if !validUTF8Tree(node) {
			info.skip(nodeIndex, nodeType, "parse_failed", "document")
			return nil
		}
		if len(outbounds) >= MaxNormalizedNodes {
			return normalizeError("too_many_outbounds", format)
		}
		if len(outbounds) == cap(outbounds) {
			nextCapacity := cap(outbounds) * 2
			if nextCapacity > MaxNormalizedNodes {
				nextCapacity = MaxNormalizedNodes
			}
			grown := make([]map[string]any, len(outbounds), nextCapacity)
			copy(grown, outbounds)
			outbounds = grown
		}
		outbounds = append(outbounds, node)
		return nil
	})
	if err != nil {
		return nil, info, err
	}
	return encodeOutbounds(outbounds, info)
}

func scanURIRecords(
	body string,
	format Format,
	visit func(record string, recordIndex int) error,
) error {
	recordIndex := 0
	start := 0
	emit := func(end int) error {
		recordIndex++
		if recordIndex > MaxYAMLNodes {
			return normalizeError("too_many_document_nodes", format)
		}
		if end-start > MaxScalarBytes {
			return normalizeError("scalar_too_large", format)
		}
		return visit(body[start:end], recordIndex)
	}

	for offset := 0; offset < len(body); offset++ {
		if body[offset] != '\n' && body[offset] != '\r' {
			continue
		}
		if err := emit(offset); err != nil {
			return err
		}
		if body[offset] == '\r' && offset+1 < len(body) &&
			body[offset+1] == '\n' {
			offset++
		}
		start = offset + 1
	}
	if start < len(body) {
		return emit(len(body))
	}
	return nil
}

func decodeBase64Payload(raw []byte) ([]byte, bool) {
	cleanLength := 0
	padding := 0
	paddingStarted := false
	standardAlphabet := false
	urlAlphabet := false

	for _, character := range raw {
		if isBase64Whitespace(character) {
			continue
		}
		cleanLength++
		switch {
		case character >= 'A' && character <= 'Z',
			character >= 'a' && character <= 'z',
			character >= '0' && character <= '9':
			if paddingStarted {
				return nil, false
			}
		case character == '+' || character == '/':
			if paddingStarted {
				return nil, false
			}
			standardAlphabet = true
		case character == '-' || character == '_':
			if paddingStarted {
				return nil, false
			}
			urlAlphabet = true
		case character == '=':
			paddingStarted = true
			padding++
			if padding > 2 {
				return nil, false
			}
		default:
			return nil, false
		}
	}

	if cleanLength == 0 || standardAlphabet && urlAlphabet {
		return nil, false
	}
	if padding > 0 {
		if cleanLength%4 != 0 {
			return nil, false
		}
	} else if cleanLength%4 == 1 {
		return nil, false
	}

	compact := raw
	if cleanLength != len(raw) {
		compact = make([]byte, 0, cleanLength)
		for _, character := range raw {
			if !isBase64Whitespace(character) {
				compact = append(compact, character)
			}
		}
	}

	var encoding *base64.Encoding
	switch {
	case urlAlphabet && padding > 0:
		encoding = strictURLBase64
	case urlAlphabet:
		encoding = strictRawURLBase64
	case padding > 0:
		encoding = strictStdBase64
	default:
		encoding = strictRawStdBase64
	}
	decoded := make([]byte, encoding.DecodedLen(cleanLength))
	decodedLength, err := encoding.Decode(decoded, compact)
	if err != nil {
		return nil, false
	}
	decoded = decoded[:decodedLength]
	if !utf8.Valid(decoded) {
		return nil, false
	}
	return decoded, true
}

func isBase64Whitespace(character byte) bool {
	switch character {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

func validUTF8Tree(value any) bool {
	return validUTF8Reflect(reflect.ValueOf(value))
}

func validUTF8Reflect(value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Ptr:
		if value.IsNil() {
			return true
		}
		return validUTF8Reflect(value.Elem())
	case reflect.String:
		return utf8.ValidString(value.String())
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if !validUTF8Reflect(iterator.Key()) ||
				!validUTF8Reflect(iterator.Value()) {
				return false
			}
		}
	case reflect.Array, reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			if !validUTF8Reflect(value.Index(index)) {
				return false
			}
		}
	}
	return true
}

func containsURIScheme(body string, format Format) (bool, error) {
	found := false
	err := scanURIRecords(body, format, func(rawLine string, _ int) error {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "//") {
			return nil
		}
		if schemeFromURI(line) != "" {
			found = true
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

func looksLikeClash(text string) bool {
	if firstURIRecord(text) {
		return false
	}
	start := 0
	for offset := 0; offset < len(text); {
		character, size := utf8.DecodeRuneInString(text[offset:])
		if !isYAMLBreak(character) {
			offset += size
			continue
		}
		if isClashRootKeyLine(text[start:offset]) {
			return true
		}
		offset += size
		start = offset
	}
	return isClashRootKeyLine(text[start:])
}

// URI subscription records use CR and LF framing. Keep Unicode LS and PS
// inside a URI fragment rather than reinterpreting fragment text as YAML, but
// do not let URL-looking metadata on later YAML lines hide a root proxies key.
func firstURIRecord(text string) bool {
	start := 0
	for offset := 0; offset <= len(text); offset++ {
		if offset < len(text) && text[offset] != '\n' && text[offset] != '\r' {
			continue
		}
		line := strings.TrimSpace(text[start:offset])
		if line != "" &&
			!strings.HasPrefix(line, "#") &&
			!strings.HasPrefix(line, "//") {
			return startsWithURIScheme(line)
		}
		if offset < len(text) && text[offset] == '\r' &&
			offset+1 < len(text) && text[offset+1] == '\n' {
			offset++
		}
		start = offset + 1
	}
	return false
}

func startsWithURIScheme(line string) bool {
	line = strings.TrimSpace(line)
	delimiter := strings.Index(line, "://")
	if delimiter <= 0 {
		return false
	}
	for index := 0; index < delimiter; index++ {
		character := line[index]
		if index == 0 {
			if character < 'A' || character > 'Z' {
				if character < 'a' || character > 'z' {
					return false
				}
			}
			continue
		}
		if character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			character == '+' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func isClashRootKeyLine(line string) bool {
	for _, key := range []string{
		"proxies", `"proxies"`, `'proxies'`,
		"proxy-providers", `"proxy-providers"`, `'proxy-providers'`,
	} {
		if !strings.HasPrefix(line, key) {
			continue
		}
		offset := len(key)
		for offset < len(line) && (line[offset] == ' ' || line[offset] == '\t') {
			offset++
		}
		if offset < len(line) && line[offset] == ':' {
			return true
		}
	}
	return false
}
