package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	canonicalAggregateByteLimit = 32 * 1024 * 1024
	canonicalIntegerByteLimit   = 64 * 1024
	canonicalSpoolBufferBytes   = 64 * 1024
)

var errCanonicalAggregateInvalid = errors.New("canonical aggregate invalid")

type canonicalFragment struct {
	offset int64
	length int64
}

type canonicalRopeKind uint8

const (
	canonicalRopeSpool canonicalRopeKind = iota
	canonicalRopeLiteral
	canonicalRopeConcat
)

type canonicalRope struct {
	kind        canonicalRopeKind
	length      int64
	fragment    canonicalFragment
	literal     string
	childOffset int
	childCount  int
}

type canonicalSyntaxRopes struct {
	empty       int
	objectOpen  int
	objectClose int
	arrayOpen   int
	arrayClose  int
	comma       int
	colon       int
	trueValue   int
	falseValue  int
	nullValue   int
}

type canonicalParsedValue struct {
	rope     int
	text     string
	isString bool
}

type canonicalField struct {
	key       string
	keyRope   int
	valueRope int
}

type canonicalOutbound struct {
	identity    int
	prefix      int
	suffix      int
	originalTag string
	tagSeen     bool
	named       bool
	assignedTag string
}

type canonicalStoredOutbound struct {
	identity    canonicalFragment
	prefix      canonicalFragment
	suffix      canonicalFragment
	digest      [sha256.Size]byte
	originalTag string
	tagSeen     bool
	named       bool
	assignedTag string
}

type canonicalSpool struct {
	file     *os.File
	size     int64
	buffer   []byte
	ropes    []canonicalRope
	children []int
	fields   []canonicalField
	values   []int
	syntax   canonicalSyntaxRopes
}

func newCanonicalSpool() (*canonicalSpool, error) {
	file, err := os.CreateTemp("", "liquid-formula-canonical-*")
	if err != nil {
		return nil, errCanonicalAggregateInvalid
	}
	name := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(name)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return nil, errCanonicalAggregateInvalid
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() ||
		info.Mode().Perm() != 0o600 {
		cleanup()
		return nil, errCanonicalAggregateInvalid
	}
	// The open descriptor is sufficient for the whole merge. Unlinking the
	// random mode-0600 file immediately makes cleanup automatic on every exit.
	if err := os.Remove(name); err != nil {
		cleanup()
		return nil, errCanonicalAggregateInvalid
	}
	spool := &canonicalSpool{
		file:   file,
		buffer: make([]byte, canonicalSpoolBufferBytes),
		ropes:  make([]canonicalRope, 0, 128),
	}
	spool.syntax = canonicalSyntaxRopes{
		empty:       spool.addLiteralRope(""),
		objectOpen:  spool.addLiteralRope("{"),
		objectClose: spool.addLiteralRope("}"),
		arrayOpen:   spool.addLiteralRope("["),
		arrayClose:  spool.addLiteralRope("]"),
		comma:       spool.addLiteralRope(","),
		colon:       spool.addLiteralRope(":"),
		trueValue:   spool.addLiteralRope("true"),
		falseValue:  spool.addLiteralRope("false"),
		nullValue:   spool.addLiteralRope("null"),
	}
	return spool, nil
}

func (spool *canonicalSpool) close() {
	if spool != nil && spool.file != nil {
		_ = spool.file.Close()
	}
}

func (spool *canonicalSpool) reset() error {
	if spool == nil || spool.file == nil {
		return errCanonicalAggregateInvalid
	}
	if err := spool.file.Truncate(0); err != nil {
		return errCanonicalAggregateInvalid
	}
	if _, err := spool.file.Seek(0, io.SeekStart); err != nil {
		return errCanonicalAggregateInvalid
	}
	spool.size = 0
	clear(spool.ropes)
	spool.ropes = spool.ropes[:0]
	clear(spool.children)
	spool.children = spool.children[:0]
	clear(spool.fields)
	spool.fields = spool.fields[:0]
	clear(spool.values)
	spool.values = spool.values[:0]
	spool.syntax = canonicalSyntaxRopes{
		empty:       spool.addLiteralRope(""),
		objectOpen:  spool.addLiteralRope("{"),
		objectClose: spool.addLiteralRope("}"),
		arrayOpen:   spool.addLiteralRope("["),
		arrayClose:  spool.addLiteralRope("]"),
		comma:       spool.addLiteralRope(","),
		colon:       spool.addLiteralRope(":"),
		trueValue:   spool.addLiteralRope("true"),
		falseValue:  spool.addLiteralRope("false"),
		nullValue:   spool.addLiteralRope("null"),
	}
	return nil
}

func (spool *canonicalSpool) appendString(value string) error {
	for len(value) != 0 {
		written, err := spool.file.WriteString(value)
		if written > 0 {
			spool.size += int64(written)
			value = value[written:]
		}
		if err != nil || written == 0 {
			return errCanonicalAggregateInvalid
		}
	}
	return nil
}

func (spool *canonicalSpool) appendBytes(value []byte) error {
	for len(value) != 0 {
		written, err := spool.file.Write(value)
		if written > 0 {
			spool.size += int64(written)
			value = value[written:]
		}
		if err != nil || written == 0 {
			return errCanonicalAggregateInvalid
		}
	}
	return nil
}

func (spool *canonicalSpool) addLiteralRope(value string) int {
	ropeID := len(spool.ropes)
	spool.ropes = append(spool.ropes, canonicalRope{
		kind: canonicalRopeLiteral, length: int64(len(value)),
		literal: value,
	})
	return ropeID
}

func (spool *canonicalSpool) addFragmentRope(
	fragment canonicalFragment,
) (int, error) {
	if fragment.offset < 0 || fragment.length < 0 ||
		fragment.offset > spool.size ||
		fragment.length > spool.size-fragment.offset {
		return 0, errCanonicalAggregateInvalid
	}
	ropeID := len(spool.ropes)
	spool.ropes = append(spool.ropes, canonicalRope{
		kind: canonicalRopeSpool, length: fragment.length,
		fragment: fragment,
	})
	return ropeID, nil
}

func (spool *canonicalSpool) concatRopes(
	children []int,
) (int, error) {
	start := len(spool.children)
	spool.children = append(spool.children, children...)
	return spool.concatChildRange(start)
}

func (spool *canonicalSpool) concatChildRange(
	start int,
) (int, error) {
	if start < 0 || start > len(spool.children) {
		return 0, errCanonicalAggregateInvalid
	}
	end := len(spool.children)
	write := start
	var length int64
	for _, child := range spool.children[start:end] {
		if child < 0 || child >= len(spool.ropes) {
			return 0, errCanonicalAggregateInvalid
		}
		childLength := spool.ropes[child].length
		if childLength == 0 {
			continue
		}
		if childLength >
			canonicalAggregateByteLimit-length {
			return 0, errCanonicalAggregateInvalid
		}
		length += childLength
		spool.children[write] = child
		write++
	}
	count := write - start
	switch count {
	case 0:
		spool.children = spool.children[:start]
		return spool.syntax.empty, nil
	case 1:
		child := spool.children[start]
		spool.children = spool.children[:start]
		return child, nil
	}
	spool.children = spool.children[:write]
	ropeID := len(spool.ropes)
	spool.ropes = append(spool.ropes, canonicalRope{
		kind: canonicalRopeConcat, length: length,
		childOffset: start, childCount: count,
	})
	return ropeID, nil
}

func (spool *canonicalSpool) ropeLength(ropeID int) (int64, error) {
	if ropeID < 0 || ropeID >= len(spool.ropes) {
		return 0, errCanonicalAggregateInvalid
	}
	return spool.ropes[ropeID].length, nil
}

func (spool *canonicalSpool) appendStringRope(
	value string,
) (int, error) {
	start := spool.size
	if err := spool.appendString(value); err != nil {
		return 0, err
	}
	return spool.addFragmentRope(canonicalFragment{
		offset: start, length: spool.size - start,
	})
}

func (spool *canonicalSpool) appendJSONStringRope(
	value string,
) (int, error) {
	start := spool.size
	if err := spool.appendJSONString(value); err != nil {
		return 0, err
	}
	return spool.addFragmentRope(canonicalFragment{
		offset: start, length: spool.size - start,
	})
}

type canonicalRopeCursor struct {
	ropeID     int
	childIndex int
}

type canonicalRopeReader struct {
	spool          *canonicalSpool
	stack          []canonicalRopeCursor
	literal        string
	literalOffset  int
	fragment       canonicalFragment
	fragmentOffset int64
	hasFragment    bool
}

func newCanonicalRopeReader(
	spool *canonicalSpool,
	ropeID int,
) (*canonicalRopeReader, error) {
	if _, err := spool.ropeLength(ropeID); err != nil {
		return nil, err
	}
	return &canonicalRopeReader{
		spool: spool,
		stack: []canonicalRopeCursor{{ropeID: ropeID}},
	}, nil
}

func (reader *canonicalRopeReader) Read(destination []byte) (
	int,
	error,
) {
	written := 0
	for written < len(destination) {
		if reader.literalOffset < len(reader.literal) {
			count := copy(
				destination[written:],
				reader.literal[reader.literalOffset:],
			)
			reader.literalOffset += count
			written += count
			continue
		}
		reader.literal = ""
		reader.literalOffset = 0

		if reader.hasFragment {
			remaining := reader.fragment.length -
				reader.fragmentOffset
			if remaining > 0 {
				count := len(destination) - written
				if int64(count) > remaining {
					count = int(remaining)
				}
				if err := readCanonicalAt(
					reader.spool.file,
					destination[written:written+count],
					reader.fragment.offset+reader.fragmentOffset,
				); err != nil {
					return written, err
				}
				reader.fragmentOffset += int64(count)
				written += count
				continue
			}
			reader.hasFragment = false
			reader.fragmentOffset = 0
		}

		if len(reader.stack) == 0 {
			if written == 0 {
				return 0, io.EOF
			}
			return written, nil
		}
		cursor := &reader.stack[len(reader.stack)-1]
		if cursor.ropeID < 0 ||
			cursor.ropeID >= len(reader.spool.ropes) {
			return written, errCanonicalAggregateInvalid
		}
		rope := &reader.spool.ropes[cursor.ropeID]
		switch rope.kind {
		case canonicalRopeLiteral:
			reader.stack = reader.stack[:len(reader.stack)-1]
			reader.literal = rope.literal
		case canonicalRopeSpool:
			reader.stack = reader.stack[:len(reader.stack)-1]
			reader.fragment = rope.fragment
			reader.fragmentOffset = 0
			reader.hasFragment = true
		case canonicalRopeConcat:
			if rope.childOffset < 0 || rope.childCount < 0 ||
				rope.childOffset > len(reader.spool.children) ||
				rope.childCount >
					len(reader.spool.children)-rope.childOffset {
				return written, errCanonicalAggregateInvalid
			}
			if cursor.childIndex >= rope.childCount {
				reader.stack = reader.stack[:len(reader.stack)-1]
				continue
			}
			child := reader.spool.children[rope.childOffset+cursor.childIndex]
			cursor.childIndex++
			reader.stack = append(
				reader.stack,
				canonicalRopeCursor{ropeID: child},
			)
		default:
			return written, errCanonicalAggregateInvalid
		}
	}
	return written, nil
}

func (spool *canonicalSpool) hashRope(
	ropeID int,
) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	reader, err := newCanonicalRopeReader(spool, ropeID)
	if err != nil {
		return result, err
	}
	digest := sha256.New()
	if _, err := io.CopyBuffer(
		digest, reader, spool.buffer[:len(spool.buffer)/2],
	); err != nil {
		return result, errCanonicalAggregateInvalid
	}
	sum := digest.Sum(result[:0])
	if len(sum) != sha256.Size {
		return result, errCanonicalAggregateInvalid
	}
	return result, nil
}

func (spool *canonicalSpool) equalRopeFragment(
	ropeID int,
	flat *canonicalSpool,
	fragment canonicalFragment,
) (bool, error) {
	ropeLength, err := spool.ropeLength(ropeID)
	if err != nil {
		return false, err
	}
	if flat == nil || flat.file == nil ||
		fragment.offset < 0 || fragment.length < 0 ||
		fragment.offset > flat.size ||
		fragment.length > flat.size-fragment.offset {
		return false, errCanonicalAggregateInvalid
	}
	if ropeLength != fragment.length {
		return false, nil
	}
	reader, err := newCanonicalRopeReader(spool, ropeID)
	if err != nil {
		return false, err
	}
	half := len(spool.buffer) / 2
	ropeBuffer := spool.buffer[:half]
	flatBuffer := spool.buffer[half:]
	for offset := int64(0); offset < fragment.length; {
		chunk := int64(half)
		if fragment.length-offset < chunk {
			chunk = fragment.length - offset
		}
		ropeChunk := ropeBuffer[:int(chunk)]
		flatChunk := flatBuffer[:int(chunk)]
		if _, err := io.ReadFull(reader, ropeChunk); err != nil {
			return false, errCanonicalAggregateInvalid
		}
		if err := readCanonicalAt(
			flat.file, flatChunk, fragment.offset+offset,
		); err != nil {
			return false, errCanonicalAggregateInvalid
		}
		if !bytes.Equal(ropeChunk, flatChunk) {
			return false, nil
		}
		offset += chunk
	}
	return true, nil
}

func (spool *canonicalSpool) copyRopeTo(
	ropeID int,
	flat *canonicalSpool,
) (canonicalFragment, error) {
	var fragment canonicalFragment
	if flat == nil || flat.file == nil {
		return fragment, errCanonicalAggregateInvalid
	}
	length, err := spool.ropeLength(ropeID)
	if err != nil {
		return fragment, err
	}
	reader, err := newCanonicalRopeReader(spool, ropeID)
	if err != nil {
		return fragment, err
	}
	fragment.offset = flat.size
	fragment.length = length
	for remaining := length; remaining > 0; {
		chunk := int64(len(spool.buffer))
		if remaining < chunk {
			chunk = remaining
		}
		buffer := spool.buffer[:int(chunk)]
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return canonicalFragment{}, errCanonicalAggregateInvalid
		}
		if err := flat.appendBytes(buffer); err != nil {
			return canonicalFragment{}, err
		}
		remaining -= chunk
	}
	return fragment, nil
}

func (spool *canonicalSpool) flattenOutbound(
	outbound canonicalOutbound,
	flat *canonicalSpool,
	digest [sha256.Size]byte,
) (canonicalStoredOutbound, error) {
	var stored canonicalStoredOutbound
	prefixLength, err := spool.ropeLength(outbound.prefix)
	if err != nil {
		return stored, err
	}
	suffixLength, err := spool.ropeLength(outbound.suffix)
	if err != nil {
		return stored, err
	}
	identity, err := spool.copyRopeTo(outbound.identity, flat)
	if err != nil {
		return stored, err
	}
	separatorLength := int64(0)
	if prefixLength != 0 && suffixLength != 0 {
		separatorLength = 1
	}
	if identity.length !=
		2+prefixLength+separatorLength+suffixLength {
		return stored, errCanonicalAggregateInvalid
	}
	stored = canonicalStoredOutbound{
		identity: identity,
		prefix: canonicalFragment{
			offset: identity.offset + 1,
			length: prefixLength,
		},
		suffix: canonicalFragment{
			offset: identity.offset + 1 +
				prefixLength + separatorLength,
			length: suffixLength,
		},
		digest:      digest,
		originalTag: outbound.originalTag,
		tagSeen:     outbound.tagSeen,
		named:       outbound.named,
	}
	return stored, nil
}

func (spool *canonicalSpool) readFragmentInto(
	fragment canonicalFragment,
	destination []byte,
) error {
	if spool == nil || spool.file == nil ||
		fragment.offset < 0 || fragment.length < 0 ||
		fragment.offset > spool.size ||
		fragment.length > spool.size-fragment.offset ||
		fragment.length != int64(len(destination)) {
		return errCanonicalAggregateInvalid
	}
	return readCanonicalAt(spool.file, destination, fragment.offset)
}

func readCanonicalAt(
	file *os.File,
	destination []byte,
	offset int64,
) error {
	for len(destination) != 0 {
		read, err := file.ReadAt(destination, offset)
		if read > 0 {
			offset += int64(read)
			destination = destination[read:]
		}
		if err != nil {
			if err == io.EOF && len(destination) == 0 {
				return nil
			}
			return errCanonicalAggregateInvalid
		}
		if read == 0 {
			return errCanonicalAggregateInvalid
		}
	}
	return nil
}

type canonicalSourceParser struct {
	decoder *json.Decoder
	spool   *canonicalSpool
	tokens  int
}

func newCanonicalSourceParser(
	raw []byte,
	spool *canonicalSpool,
) (*canonicalSourceParser, error) {
	if len(raw) == 0 || !utf8.Valid(raw) ||
		!validJSONUnicodeEscapes(raw) {
		return nil, errCanonicalAggregateInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return &canonicalSourceParser{decoder: decoder, spool: spool}, nil
}

func (parser *canonicalSourceParser) token() (any, error) {
	token, err := parser.decoder.Token()
	if err != nil {
		return nil, errCanonicalAggregateInvalid
	}
	parser.tokens++
	if parser.tokens > MaxYAMLNodes {
		return nil, errCanonicalAggregateInvalid
	}
	switch value := token.(type) {
	case string:
		if len(value) > MaxScalarBytes {
			return nil, errCanonicalAggregateInvalid
		}
	case json.Number:
		if len(value.String()) > canonicalIntegerByteLimit {
			return nil, errCanonicalAggregateInvalid
		}
	}
	return token, nil
}

func (parser *canonicalSourceParser) parseSource(
	accept func(canonicalOutbound) error,
) error {
	rootToken, err := parser.token()
	if err != nil || rootToken != json.Delim('{') {
		return errCanonicalAggregateInvalid
	}
	if !parser.decoder.More() {
		return errCanonicalAggregateInvalid
	}
	keyToken, err := parser.token()
	key, ok := keyToken.(string)
	if err != nil || !ok || key != "outbounds" {
		return errCanonicalAggregateInvalid
	}
	arrayToken, err := parser.token()
	if err != nil || arrayToken != json.Delim('[') {
		return errCanonicalAggregateInvalid
	}

	outboundCount := 0
	for parser.decoder.More() {
		outbound, err := parser.parseOutbound(2)
		if err != nil {
			return err
		}
		if err := accept(outbound); err != nil {
			return err
		}
		outboundCount++
	}
	if outboundCount == 0 {
		return errCanonicalAggregateInvalid
	}
	closeArray, err := parser.token()
	if err != nil || closeArray != json.Delim(']') ||
		parser.decoder.More() {
		return errCanonicalAggregateInvalid
	}
	closeRoot, err := parser.token()
	if err != nil || closeRoot != json.Delim('}') {
		return errCanonicalAggregateInvalid
	}
	if _, err := parser.decoder.Token(); err != io.EOF {
		return errCanonicalAggregateInvalid
	}
	return nil
}

func (parser *canonicalSourceParser) parseOutbound(
	parentDepth int,
) (canonicalOutbound, error) {
	var outbound canonicalOutbound
	openToken, err := parser.token()
	if err != nil || openToken != json.Delim('{') ||
		parentDepth+1 > MaxDocumentDepth {
		return outbound, errCanonicalAggregateInvalid
	}

	fieldStart := len(parser.spool.fields)
	defer func() {
		clear(parser.spool.fields[fieldStart:])
		parser.spool.fields = parser.spool.fields[:fieldStart]
	}()
	typeSeen := false
	nodeType := ""
	tagSeen := false
	for parser.decoder.More() {
		keyToken, err := parser.token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return outbound, errCanonicalAggregateInvalid
		}
		if key == "outbounds" {
			return outbound, errCanonicalAggregateInvalid
		}
		value, err := parser.parseValue(parentDepth+1, true)
		if err != nil {
			return outbound, err
		}
		if key == "detour" &&
			(!value.isString || value.text != "") {
			return outbound, errCanonicalAggregateInvalid
		}
		switch key {
		case "tag":
			if tagSeen || !value.isString {
				return outbound, errCanonicalAggregateInvalid
			}
			tagSeen = true
			outbound.tagSeen = true
			outbound.originalTag = value.text
			if strings.TrimSpace(value.text) != "" {
				outbound.named = true
			}
			continue
		case "type":
			if !value.isString {
				return outbound, errCanonicalAggregateInvalid
			}
			typeSeen = true
			nodeType = value.text
		}
		keyRope, err := parser.spool.appendJSONStringRope(key)
		if err != nil {
			return outbound, err
		}
		value.text = ""
		parser.spool.fields = append(
			parser.spool.fields, canonicalField{
				key: key, keyRope: keyRope, valueRope: value.rope,
			},
		)
	}
	closeToken, err := parser.token()
	if err != nil || closeToken != json.Delim('}') ||
		!typeSeen || strings.TrimSpace(nodeType) == "" {
		return outbound, errCanonicalAggregateInvalid
	}
	switch strings.ToLower(nodeType) {
	case "selector", "urltest":
		return outbound, errCanonicalAggregateInvalid
	}

	fields := parser.spool.fields[fieldStart:]
	sort.Slice(fields, func(left, right int) bool {
		return fields[left].key < fields[right].key
	})
	for index := 1; index < len(fields); index++ {
		if fields[index-1].key == fields[index].key {
			return outbound, errCanonicalAggregateInvalid
		}
	}
	identity, prefix, suffix, err :=
		parser.spool.assembleOutboundIdentity(fields)
	if err != nil {
		return outbound, err
	}
	outbound.identity = identity
	outbound.prefix = prefix
	outbound.suffix = suffix
	return outbound, nil
}

func (parser *canonicalSourceParser) parseValue(
	parentDepth int,
	insideOutbound bool,
) (canonicalParsedValue, error) {
	var parsed canonicalParsedValue
	token, err := parser.token()
	if err != nil {
		return parsed, err
	}
	switch value := token.(type) {
	case json.Delim:
		if parentDepth+1 > MaxDocumentDepth {
			return parsed, errCanonicalAggregateInvalid
		}
		switch value {
		case '{':
			fieldStart := len(parser.spool.fields)
			defer func() {
				clear(parser.spool.fields[fieldStart:])
				parser.spool.fields =
					parser.spool.fields[:fieldStart]
			}()
			for parser.decoder.More() {
				keyToken, err := parser.token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return parsed, errCanonicalAggregateInvalid
				}
				if insideOutbound && key == "outbounds" {
					return parsed, errCanonicalAggregateInvalid
				}
				child, err := parser.parseValue(
					parentDepth+1, insideOutbound,
				)
				if err != nil {
					return parsed, err
				}
				if insideOutbound && key == "detour" &&
					(!child.isString || child.text != "") {
					return parsed, errCanonicalAggregateInvalid
				}
				keyRope, err := parser.spool.appendJSONStringRope(key)
				if err != nil {
					return parsed, err
				}
				child.text = ""
				parser.spool.fields = append(
					parser.spool.fields,
					canonicalField{
						key: key, keyRope: keyRope,
						valueRope: child.rope,
					},
				)
			}
			closeToken, err := parser.token()
			if err != nil || closeToken != json.Delim('}') {
				return parsed, errCanonicalAggregateInvalid
			}
			fields := parser.spool.fields[fieldStart:]
			sort.Slice(fields, func(left, right int) bool {
				return fields[left].key < fields[right].key
			})
			for index := 1; index < len(fields); index++ {
				if fields[index-1].key == fields[index].key {
					return parsed, errCanonicalAggregateInvalid
				}
			}
			rope, err := parser.spool.assembleObject(fields)
			if err != nil {
				return parsed, err
			}
			parsed.rope = rope
			return parsed, nil
		case '[':
			valueStart := len(parser.spool.values)
			defer func() {
				clear(parser.spool.values[valueStart:])
				parser.spool.values =
					parser.spool.values[:valueStart]
			}()
			for parser.decoder.More() {
				child, err := parser.parseValue(
					parentDepth+1, insideOutbound,
				)
				if err != nil {
					return parsed, err
				}
				child.text = ""
				parser.spool.values = append(
					parser.spool.values, child.rope,
				)
			}
			closeToken, err := parser.token()
			if err != nil || closeToken != json.Delim(']') {
				return parsed, errCanonicalAggregateInvalid
			}
			rope, err := parser.spool.assembleArray(
				parser.spool.values[valueStart:],
			)
			if err != nil {
				return parsed, err
			}
			parsed.rope = rope
			return parsed, nil
		default:
			return parsed, errCanonicalAggregateInvalid
		}
	case string:
		rope, err := parser.spool.appendJSONStringRope(value)
		if err != nil {
			return parsed, err
		}
		parsed.rope = rope
		parsed.text = value
		parsed.isString = true
		return parsed, nil
	case json.Number:
		number, err := canonicalIntegralJSONNumber(value.String())
		if err != nil {
			return parsed, err
		}
		rope, err := parser.spool.appendStringRope(number)
		if err != nil {
			return parsed, err
		}
		parsed.rope = rope
		return parsed, nil
	case bool:
		if value {
			parsed.rope = parser.spool.syntax.trueValue
		} else {
			parsed.rope = parser.spool.syntax.falseValue
		}
		return parsed, nil
	case nil:
		parsed.rope = parser.spool.syntax.nullValue
		return parsed, nil
	default:
		return parsed, errCanonicalAggregateInvalid
	}
}

func (spool *canonicalSpool) assembleFieldList(
	fields []canonicalField,
) (int, error) {
	start := len(spool.children)
	for index, field := range fields {
		if index != 0 {
			spool.children = append(
				spool.children, spool.syntax.comma,
			)
		}
		spool.children = append(
			spool.children, field.keyRope, spool.syntax.colon,
			field.valueRope,
		)
	}
	return spool.concatChildRange(start)
}

func (spool *canonicalSpool) assembleArray(
	values []int,
) (int, error) {
	start := len(spool.children)
	spool.children = append(
		spool.children, spool.syntax.arrayOpen,
	)
	for index, value := range values {
		if index != 0 {
			spool.children = append(
				spool.children, spool.syntax.comma,
			)
		}
		spool.children = append(spool.children, value)
	}
	spool.children = append(
		spool.children, spool.syntax.arrayClose,
	)
	return spool.concatChildRange(start)
}

func (spool *canonicalSpool) assembleObject(
	fields []canonicalField,
) (int, error) {
	fieldList, err := spool.assembleFieldList(fields)
	if err != nil {
		return 0, err
	}
	return spool.concatRopes([]int{
		spool.syntax.objectOpen, fieldList, spool.syntax.objectClose,
	})
}

func (spool *canonicalSpool) assembleOutboundIdentity(
	fields []canonicalField,
) (int, int, int, error) {
	split := sort.Search(len(fields), func(index int) bool {
		return fields[index].key > "tag"
	})
	prefix, err := spool.assembleFieldList(fields[:split])
	if err != nil {
		return 0, 0, 0, err
	}
	suffix, err := spool.assembleFieldList(fields[split:])
	if err != nil {
		return 0, 0, 0, err
	}
	children := []int{spool.syntax.objectOpen, prefix}
	prefixLength, err := spool.ropeLength(prefix)
	if err != nil {
		return 0, 0, 0, err
	}
	suffixLength, err := spool.ropeLength(suffix)
	if err != nil {
		return 0, 0, 0, err
	}
	if prefixLength != 0 && suffixLength != 0 {
		children = append(children, spool.syntax.comma)
	}
	children = append(children, suffix, spool.syntax.objectClose)
	identity, err := spool.concatRopes(children)
	if err != nil {
		return 0, 0, 0, err
	}
	return identity, prefix, suffix, nil
}

func (spool *canonicalSpool) appendJSONString(value string) error {
	if !utf8.ValidString(value) {
		return errCanonicalAggregateInvalid
	}
	if err := spool.appendString(`"`); err != nil {
		return err
	}
	start := 0
	for offset := 0; offset < len(value); {
		character := value[offset]
		if character < utf8.RuneSelf {
			escaped := ""
			var unicodeEscape [6]byte
			switch character {
			case '\\':
				escaped = `\\`
			case '"':
				escaped = `\"`
			case '\b':
				escaped = `\b`
			case '\f':
				escaped = `\f`
			case '\n':
				escaped = `\n`
			case '\r':
				escaped = `\r`
			case '\t':
				escaped = `\t`
			default:
				if character < 0x20 {
					const hexadecimal = "0123456789abcdef"
					unicodeEscape = [6]byte{
						'\\', 'u', '0', '0',
						hexadecimal[character>>4],
						hexadecimal[character&0x0f],
					}
				}
			}
			if escaped == "" && unicodeEscape == [6]byte{} {
				offset++
				continue
			}
			if err := spool.appendString(value[start:offset]); err != nil {
				return err
			}
			if escaped != "" {
				if err := spool.appendString(escaped); err != nil {
					return err
				}
			} else if err := spool.appendBytes(unicodeEscape[:]); err != nil {
				return err
			}
			offset++
			start = offset
			continue
		}
		decoded, width := utf8.DecodeRuneInString(value[offset:])
		if decoded == '\u2028' || decoded == '\u2029' {
			if err := spool.appendString(value[start:offset]); err != nil {
				return err
			}
			escaped := `\u2028`
			if decoded == '\u2029' {
				escaped = `\u2029`
			}
			if err := spool.appendString(escaped); err != nil {
				return err
			}
			offset += width
			start = offset
			continue
		}
		offset += width
	}
	if err := spool.appendString(value[start:]); err != nil {
		return err
	}
	return spool.appendString(`"`)
}

// canonicalizeStoredSource validates one normalized source with the exact
// strict parser used by the aggregate merge, then emits its compact canonical
// bytes without deduplicating nodes or changing tag presence/content.
func canonicalizeStoredSource(raw []byte) ([]byte, int, error) {
	scratch, err := newCanonicalSpool()
	if err != nil {
		return nil, 0, errCanonicalAggregateInvalid
	}
	defer scratch.close()
	flat, err := newCanonicalSpool()
	if err != nil {
		return nil, 0, errCanonicalAggregateInvalid
	}
	defer flat.close()

	outbounds := make([]canonicalStoredOutbound, 0)
	parser, err := newCanonicalSourceParser(raw, scratch)
	if err != nil {
		return nil, 0, errCanonicalAggregateInvalid
	}
	err = parser.parseSource(func(outbound canonicalOutbound) error {
		if len(outbounds) >= MaxNormalizedNodes {
			return errCanonicalAggregateInvalid
		}
		stored, err := scratch.flattenOutbound(
			outbound, flat, [sha256.Size]byte{},
		)
		if err != nil {
			return err
		}
		outbounds = append(outbounds, stored)
		return scratch.reset()
	})
	if err != nil || len(outbounds) == 0 {
		return nil, 0, errCanonicalAggregateInvalid
	}

	totalBytes := int64(len(`{"outbounds":[`)) + int64(len(`]}`))
	for index, outbound := range outbounds {
		prefixLength := outbound.prefix.length
		suffixLength := outbound.suffix.length
		nodeBytes := int64(2) + prefixLength + suffixLength
		fieldCount := 0
		if prefixLength != 0 {
			fieldCount++
		}
		if outbound.tagSeen {
			tagBytes, err := canonicalJSONStringLength(
				outbound.originalTag,
			)
			if err != nil {
				return nil, 0, errCanonicalAggregateInvalid
			}
			nodeBytes += int64(len(`"tag":`) + tagBytes)
			fieldCount++
		}
		if suffixLength != 0 {
			fieldCount++
		}
		if fieldCount > 1 {
			nodeBytes += int64(fieldCount - 1)
		}
		if index != 0 {
			nodeBytes++
		}
		if nodeBytes >
			canonicalAggregateByteLimit-totalBytes {
			return nil, 0, errCanonicalAggregateInvalid
		}
		totalBytes += nodeBytes
	}

	output := make([]byte, int(totalBytes))
	offset := copy(output, `{"outbounds":[`)
	for index, outbound := range outbounds {
		if index != 0 {
			output[offset] = ','
			offset++
		}
		output[offset] = '{'
		offset++
		wroteField := false
		prefixLength := outbound.prefix.length
		if prefixLength != 0 {
			if err := flat.readFragmentInto(
				outbound.prefix,
				output[offset:offset+int(prefixLength)],
			); err != nil {
				return nil, 0, errCanonicalAggregateInvalid
			}
			offset += int(prefixLength)
			wroteField = true
		}
		if outbound.tagSeen {
			if wroteField {
				output[offset] = ','
				offset++
			}
			offset += copy(output[offset:], `"tag":`)
			written, err := writeCanonicalJSONString(
				output[offset:], outbound.originalTag,
			)
			if err != nil {
				return nil, 0, errCanonicalAggregateInvalid
			}
			offset += written
			wroteField = true
		}
		suffixLength := outbound.suffix.length
		if suffixLength != 0 {
			if wroteField {
				output[offset] = ','
				offset++
			}
			if err := flat.readFragmentInto(
				outbound.suffix,
				output[offset:offset+int(suffixLength)],
			); err != nil {
				return nil, 0, errCanonicalAggregateInvalid
			}
			offset += int(suffixLength)
		}
		output[offset] = '}'
		offset++
	}
	offset += copy(output[offset:], `]}`)
	if offset != len(output) {
		return nil, 0, errCanonicalAggregateInvalid
	}
	return output, len(outbounds), nil
}

// mergeCanonicalAggregate merges occurrence order then node order. Each node
// is assembled in a reusable private scratch rope and unique identities are
// flattened once into a second private spool; discarded DAGs never persist.
func mergeCanonicalAggregate(candidate generationCandidate) ([]byte, error) {
	if len(candidate.Sources) == 0 {
		return nil, errCanonicalAggregateInvalid
	}
	scratch, err := newCanonicalSpool()
	if err != nil {
		return nil, errCanonicalAggregateInvalid
	}
	defer scratch.close()
	flat, err := newCanonicalSpool()
	if err != nil {
		return nil, errCanonicalAggregateInvalid
	}
	defer flat.close()

	survivors := make([]canonicalStoredOutbound, 0)
	identities := make(map[[sha256.Size]byte][]int)
	lowerBound := int64(len(`{"outbounds":[]}`))
	accept := func(outbound canonicalOutbound) (int, error) {
		digest, err := scratch.hashRope(outbound.identity)
		if err != nil {
			return 0, err
		}
		for _, survivorIndex := range identities[digest] {
			equal, err := scratch.equalRopeFragment(
				outbound.identity,
				flat,
				survivors[survivorIndex].identity,
			)
			if err != nil {
				return 0, err
			}
			if equal {
				return survivorIndex, nil
			}
		}
		if len(survivors) >= MaxNormalizedNodes {
			return 0, errCanonicalAggregateInvalid
		}

		tag := outbound.originalTag
		if !outbound.named {
			tag = "Unnamed"
			outbound.originalTag = ""
		}
		prefixLength, err := scratch.ropeLength(outbound.prefix)
		if err != nil {
			return 0, err
		}
		suffixLength, err := scratch.ropeLength(outbound.suffix)
		if err != nil {
			return 0, err
		}
		nodeLowerBound := int64(2+len(`"tag":`)) +
			prefixLength + suffixLength
		if prefixLength != 0 {
			nodeLowerBound++
		}
		if suffixLength != 0 {
			nodeLowerBound++
		}
		tagLength, err := canonicalJSONStringLength(tag)
		if err != nil {
			return 0, err
		}
		nodeLowerBound += int64(tagLength)
		if len(survivors) != 0 {
			nodeLowerBound++
		}
		if nodeLowerBound >
			canonicalAggregateByteLimit-lowerBound {
			return 0, errCanonicalAggregateInvalid
		}
		lowerBound += nodeLowerBound

		stored, err := scratch.flattenOutbound(
			outbound, flat, digest,
		)
		if err != nil {
			return 0, err
		}
		survivors = append(survivors, stored)
		survivorIndex := len(survivors) - 1
		identities[digest] = append(
			identities[digest], survivorIndex,
		)
		return survivorIndex, nil
	}

	parsedObjects := make([][]int, len(candidate.Objects))
	objectParsed := make([]bool, len(candidate.Objects))
	for sourceOffset, source := range candidate.Sources {
		if source.Index != sourceOffset+1 ||
			source.ObjectIndex < 1 ||
			source.ObjectIndex > len(candidate.Objects) {
			return nil, errCanonicalAggregateInvalid
		}
		objectOffset := source.ObjectIndex - 1
		if objectParsed[objectOffset] {
			for _, survivorIndex := range parsedObjects[objectOffset] {
				if survivorIndex < 0 ||
					survivorIndex >= len(survivors) {
					return nil, errCanonicalAggregateInvalid
				}
				digest := survivors[survivorIndex].digest
				found := false
				for _, existing := range identities[digest] {
					if existing == survivorIndex {
						found = true
						break
					}
				}
				if !found {
					return nil, errCanonicalAggregateInvalid
				}
			}
			continue
		}
		parser, err := newCanonicalSourceParser(
			candidate.Objects[objectOffset], scratch,
		)
		if err != nil {
			return nil, errCanonicalAggregateInvalid
		}
		err = parser.parseSource(func(outbound canonicalOutbound) error {
			survivorIndex, err := accept(outbound)
			if err != nil {
				return err
			}
			parsedObjects[objectOffset] = append(
				parsedObjects[objectOffset], survivorIndex,
			)
			return scratch.reset()
		})
		if err != nil {
			return nil, errCanonicalAggregateInvalid
		}
		objectParsed[objectOffset] = true
	}
	if len(survivors) == 0 {
		return nil, errCanonicalAggregateInvalid
	}

	reserved := make(map[string]bool, len(survivors))
	for _, outbound := range survivors {
		if outbound.named {
			reserved[outbound.originalTag] = true
		}
	}
	assigned := make(map[string]bool, len(survivors))
	for index := range survivors {
		outbound := &survivors[index]
		base := outbound.originalTag
		if !outbound.named {
			base = "Unnamed"
		}
		switch {
		case outbound.named && !assigned[base]:
			outbound.assignedTag = base
		case !outbound.named && !reserved[base] && !assigned[base]:
			outbound.assignedTag = base
		default:
			for suffix := 2; suffix <= MaxNormalizedNodes+1; suffix++ {
				suffixText := " #" + strconv.Itoa(suffix)
				if len(base) > MaxScalarBytes-len(suffixText) {
					return nil, errCanonicalAggregateInvalid
				}
				candidateTag := base + suffixText
				if !reserved[candidateTag] &&
					!assigned[candidateTag] {
					outbound.assignedTag = candidateTag
					break
				}
			}
		}
		if outbound.assignedTag == "" {
			return nil, errCanonicalAggregateInvalid
		}
		assigned[outbound.assignedTag] = true
	}

	totalBytes := int64(len(`{"outbounds":[`)) + int64(len(`]}`))
	for index, outbound := range survivors {
		prefixLength := outbound.prefix.length
		suffixLength := outbound.suffix.length
		nodeBytes := int64(2+len(`"tag":`)) +
			prefixLength + suffixLength
		if prefixLength != 0 {
			nodeBytes++
		}
		if suffixLength != 0 {
			nodeBytes++
		}
		tagBytes, err := canonicalJSONStringLength(outbound.assignedTag)
		if err != nil {
			return nil, errCanonicalAggregateInvalid
		}
		nodeBytes += int64(tagBytes)
		if index != 0 {
			nodeBytes++
		}
		if nodeBytes > canonicalAggregateByteLimit-totalBytes {
			return nil, errCanonicalAggregateInvalid
		}
		totalBytes += nodeBytes
	}

	output := make([]byte, int(totalBytes))
	offset := copy(output, `{"outbounds":[`)
	for index, outbound := range survivors {
		if index != 0 {
			output[offset] = ','
			offset++
		}
		output[offset] = '{'
		offset++
		prefixLength := outbound.prefix.length
		if prefixLength != 0 {
			if err := flat.readFragmentInto(
				outbound.prefix,
				output[offset:offset+int(prefixLength)],
			); err != nil {
				return nil, errCanonicalAggregateInvalid
			}
			offset += int(prefixLength)
			output[offset] = ','
			offset++
		}
		offset += copy(output[offset:], `"tag":`)
		written, err := writeCanonicalJSONString(
			output[offset:], outbound.assignedTag,
		)
		if err != nil {
			return nil, errCanonicalAggregateInvalid
		}
		offset += written
		suffixLength := outbound.suffix.length
		if suffixLength != 0 {
			output[offset] = ','
			offset++
			if err := flat.readFragmentInto(
				outbound.suffix,
				output[offset:offset+int(suffixLength)],
			); err != nil {
				return nil, errCanonicalAggregateInvalid
			}
			offset += int(suffixLength)
		}
		output[offset] = '}'
		offset++
	}
	offset += copy(output[offset:], `]}`)
	if offset != len(output) {
		return nil, errCanonicalAggregateInvalid
	}
	return output, nil
}

func canonicalJSONStringLength(value string) (int, error) {
	if !utf8.ValidString(value) {
		return 0, errCanonicalAggregateInvalid
	}
	length := 2
	for offset := 0; offset < len(value); {
		character := value[offset]
		if character < utf8.RuneSelf {
			switch character {
			case '\\', '"', '\b', '\f', '\n', '\r', '\t':
				length += 2
			default:
				if character < 0x20 {
					length += 6
				} else {
					length++
				}
			}
			offset++
			continue
		}
		decoded, width := utf8.DecodeRuneInString(value[offset:])
		if decoded == '\u2028' || decoded == '\u2029' {
			length += 6
		} else {
			length += width
		}
		offset += width
	}
	return length, nil
}

func writeCanonicalJSONString(
	destination []byte,
	value string,
) (int, error) {
	length, err := canonicalJSONStringLength(value)
	if err != nil || len(destination) < length {
		return 0, errCanonicalAggregateInvalid
	}
	offset := 0
	destination[offset] = '"'
	offset++
	const hexadecimal = "0123456789abcdef"
	for inputOffset := 0; inputOffset < len(value); {
		character := value[inputOffset]
		if character < utf8.RuneSelf {
			switch character {
			case '\\', '"':
				destination[offset] = '\\'
				destination[offset+1] = character
				offset += 2
			case '\b':
				copy(destination[offset:], `\b`)
				offset += 2
			case '\f':
				copy(destination[offset:], `\f`)
				offset += 2
			case '\n':
				copy(destination[offset:], `\n`)
				offset += 2
			case '\r':
				copy(destination[offset:], `\r`)
				offset += 2
			case '\t':
				copy(destination[offset:], `\t`)
				offset += 2
			default:
				if character < 0x20 {
					copy(destination[offset:], `\u00`)
					destination[offset+4] =
						hexadecimal[character>>4]
					destination[offset+5] =
						hexadecimal[character&0x0f]
					offset += 6
				} else {
					destination[offset] = character
					offset++
				}
			}
			inputOffset++
			continue
		}
		decoded, width := utf8.DecodeRuneInString(value[inputOffset:])
		if decoded == '\u2028' || decoded == '\u2029' {
			copy(destination[offset:], `\u2028`)
			if decoded == '\u2029' {
				destination[offset+5] = '9'
			}
			offset += 6
		} else {
			copy(
				destination[offset:],
				value[inputOffset:inputOffset+width],
			)
			offset += width
		}
		inputOffset += width
	}
	destination[offset] = '"'
	offset++
	if offset != length {
		return 0, errCanonicalAggregateInvalid
	}
	return offset, nil
}

func canonicalIntegralJSONNumber(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > canonicalIntegerByteLimit {
		return "", errCanonicalAggregateInvalid
	}
	negative := raw[0] == '-'
	unsigned := raw
	if negative {
		unsigned = raw[1:]
	}

	exponentText := ""
	if exponentOffset := strings.IndexAny(unsigned, "eE"); exponentOffset >= 0 {
		exponentText = unsigned[exponentOffset+1:]
		unsigned = unsigned[:exponentOffset]
	}
	fractionDigits := 0
	if dotOffset := strings.IndexByte(unsigned, '.'); dotOffset >= 0 {
		fractionDigits = len(unsigned) - dotOffset - 1
		unsigned = unsigned[:dotOffset] + unsigned[dotOffset+1:]
	}

	firstNonzero := 0
	for firstNonzero < len(unsigned) &&
		unsigned[firstNonzero] == '0' {
		firstNonzero++
	}
	if firstNonzero == len(unsigned) {
		return "0", nil
	}
	digits := unsigned[firstNonzero:]

	exponentNegative, exponentMagnitude, exponentOverflow :=
		parseCanonicalExponent(exponentText)
	if exponentOverflow {
		return "", errCanonicalAggregateInvalid
	}

	removeDigits := 0
	appendZeros := 0
	if exponentNegative {
		removeDigits = fractionDigits + exponentMagnitude
	} else if exponentMagnitude < fractionDigits {
		removeDigits = fractionDigits - exponentMagnitude
	} else {
		appendZeros = exponentMagnitude - fractionDigits
	}
	if removeDigits != 0 {
		if removeDigits > len(digits) {
			return "", errCanonicalAggregateInvalid
		}
		for index := len(digits) - removeDigits; index < len(digits); index++ {
			if digits[index] != '0' {
				return "", errCanonicalAggregateInvalid
			}
		}
		digits = digits[:len(digits)-removeDigits]
	}

	signBytes := 0
	if negative {
		signBytes = 1
	}
	if appendZeros >
		canonicalIntegerByteLimit-signBytes-len(digits) {
		return "", errCanonicalAggregateInvalid
	}
	outputLength := signBytes + len(digits) + appendZeros
	if outputLength == 0 ||
		outputLength > canonicalIntegerByteLimit {
		return "", errCanonicalAggregateInvalid
	}
	output := make([]byte, outputLength)
	offset := 0
	if negative {
		output[offset] = '-'
		offset++
	}
	offset += copy(output[offset:], digits)
	for offset < len(output) {
		output[offset] = '0'
		offset++
	}
	return string(output), nil
}

func parseCanonicalExponent(raw string) (
	negative bool,
	magnitude int,
	overflow bool,
) {
	if raw == "" {
		return false, 0, false
	}
	switch raw[0] {
	case '-':
		negative = true
		raw = raw[1:]
	case '+':
		raw = raw[1:]
	}
	if raw == "" {
		return false, 0, true
	}
	const saturation = canonicalIntegerByteLimit*3 + 1
	for _, character := range []byte(raw) {
		if character < '0' || character > '9' {
			return false, 0, true
		}
		digit := int(character - '0')
		if magnitude > (saturation-digit)/10 {
			return negative, 0, true
		}
		magnitude = magnitude*10 + digit
		if magnitude > saturation {
			return negative, 0, true
		}
	}
	return negative, magnitude, false
}
