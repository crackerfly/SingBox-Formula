package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestCanonicalAggregateRevalidatesNormalizedOutboundSemantics(
	t *testing.T,
) {
	tests := []struct {
		name string
		node string
	}{
		{
			name: "missing type",
			node: `{"tag":"missing","server":"canonical-review-secret"}`,
		},
		{
			name: "blank type",
			node: `{"type":" \t ","tag":"blank"}`,
		},
		{
			name: "non-string type",
			node: `{"type":7,"tag":"number"}`,
		},
		{
			name: "selector outbound",
			node: `{"type":"selector","tag":"selector"}`,
		},
		{
			name: "urltest outbound",
			node: `{"type":"urltest","tag":"urltest"}`,
		},
		{
			name: "nested nonempty detour reference",
			node: `{"type":"direct","tag":"detour",` +
				`"transport":{"detour":"canonical-review-secret"}}`,
		},
		{
			name: "nested outbounds reference",
			node: `{"type":"direct","tag":"nested",` +
				`"transport":{"outbounds":["canonical-review-secret"]}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := `{"outbounds":[` + test.node + `]}`
			_, err := mergeCanonicalTestObjects([]string{raw}, []int{1})
			requireGenericCanonicalInvalid(t, err)
		})
	}
}

func TestCanonicalAggregateNormalizedScalarLimitUsesDecodedBytes(
	t *testing.T,
) {
	const scalarLimit = 64 * 1024

	for _, test := range []struct {
		name    string
		scalar  string
		allowed bool
	}{
		{
			name:    "literal exactly 64 KiB",
			scalar:  strings.Repeat("x", scalarLimit),
			allowed: true,
		},
		{
			name:   "literal 64 KiB plus one",
			scalar: strings.Repeat("x", scalarLimit+1),
		},
		{
			name:    "escaped representation decoding to exactly 64 KiB",
			scalar:  strings.Repeat(`\u0078`, scalarLimit),
			allowed: true,
		},
		{
			name:   "escaped representation decoding to 64 KiB plus one",
			scalar: strings.Repeat(`\u0078`, scalarLimit+1),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := `{"outbounds":[{"type":"direct","tag":"scalar",` +
				`"label":"` + test.scalar + `"}]}`
			output, err := mergeCanonicalTestObjects(
				[]string{raw}, []int{1},
			)
			if test.allowed {
				if err != nil {
					t.Fatalf("inclusive scalar boundary rejected: %v", err)
				}
				if len(output) == 0 {
					t.Fatal("inclusive scalar boundary produced no aggregate")
				}
				return
			}
			requireGenericCanonicalInvalid(t, err)
		})
	}
}

func TestCanonicalAggregateNormalizedDocumentNodeLimitIsInclusive(
	t *testing.T,
) {
	const documentNodeLimit = 131072

	atLimit := canonicalDocumentNodeBoundaryFixture(false)
	if got := countCanonicalJSONTokens(t, atLimit); got != documentNodeLimit {
		t.Fatalf("at-limit fixture has %d JSON tokens, want %d",
			got, documentNodeLimit)
	}
	if _, err := mergeCanonicalTestObjects(
		[]string{atLimit}, []int{1},
	); err != nil {
		t.Fatalf("exact document-node limit rejected: %v", err)
	}

	overLimit := canonicalDocumentNodeBoundaryFixture(true)
	if got := countCanonicalJSONTokens(t, overLimit); got != documentNodeLimit+1 {
		t.Fatalf("over-limit fixture has %d JSON tokens, want %d",
			got, documentNodeLimit+1)
	}
	_, err := mergeCanonicalTestObjects([]string{overLimit}, []int{1})
	requireGenericCanonicalInvalid(t, err)
}

func TestCanonicalAggregateDoesNotTreatEncodedObjectBytesAsAggregateBytes(
	t *testing.T,
) {
	const fetchBodyLimit = 32 * 1024 * 1024
	base := `{"outbounds":[{"type":"direct","tag":"encoded-boundary"}]}`
	raw := base + strings.Repeat(" ", fetchBodyLimit+1-len(base))
	if len(raw) != fetchBodyLimit+1 {
		t.Fatalf("fixture has %d encoded bytes, want %d",
			len(raw), fetchBodyLimit+1)
	}

	output, err := mergeCanonicalTestObjects([]string{raw}, []int{1})
	if err != nil {
		t.Fatalf("valid normalized object above fetch-body size rejected: %v",
			err)
	}
	want := `{"outbounds":[{"tag":"encoded-boundary","type":"direct"}]}`
	if string(output) != want {
		t.Fatalf("compact aggregate = %q, want %q", output, want)
	}
}

func TestCanonicalAssignedTagStaysWithinDecodedScalarLimit(t *testing.T) {
	const suffix = " #2"
	t.Run("largest base with direct suffix is accepted", func(t *testing.T) {
		base := strings.Repeat("x", MaxScalarBytes-len(suffix))
		output, err := mergeCanonicalTestObjects(
			[]string{canonicalTagCollisionDocument(base)}, []int{1},
		)
		if err != nil {
			t.Fatalf("legal tag suffix boundary rejected: %v", err)
		}
		tags := canonicalReviewOutputTags(t, output)
		if len(tags) != 2 || tags[0] != base ||
			tags[1] != base+suffix {
			t.Fatalf("boundary tags do not use stable numbering")
		}
		if len(tags[1]) != MaxScalarBytes {
			t.Fatalf("numbered tag has %d bytes, want %d",
				len(tags[1]), MaxScalarBytes)
		}
		if _, _, err := canonicalizeStoredSource(output); err != nil {
			t.Fatalf("boundary aggregate failed self-validation: %v", err)
		}
	})

	for _, size := range []int{
		MaxScalarBytes - len(suffix) + 1,
		MaxScalarBytes,
	} {
		t.Run(fmt.Sprintf("base length %d cannot emit an oversized suffix",
			size), func(t *testing.T) {
			base := strings.Repeat("x", size)
			output, err := mergeCanonicalTestObjects(
				[]string{canonicalTagCollisionDocument(base)}, []int{1},
			)
			if err != nil {
				requireGenericCanonicalInvalid(t, err)
				return
			}
			if _, _, err := canonicalizeStoredSource(output); err != nil {
				t.Fatalf(
					"successful merge emitted an aggregate rejected by its own strict parser: %v",
					err,
				)
			}
			tags := canonicalReviewOutputTags(t, output)
			if len(tags) != 2 || tags[0] != base ||
				tags[0] == tags[1] ||
				len(tags[0]) > MaxScalarBytes ||
				len(tags[1]) > MaxScalarBytes {
				t.Fatalf("successful merge emitted invalid collision tags")
			}
		})
	}
}

func canonicalTagCollisionDocument(tag string) string {
	return fmt.Sprintf(
		`{"outbounds":[`+
			`{"id":1,"tag":%q,"type":"direct"},`+
			`{"id":2,"tag":%q,"type":"direct"}]}`,
		tag, tag,
	)
}

func canonicalReviewOutputTags(t *testing.T, output []byte) []string {
	t.Helper()
	var root struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(output, &root); err != nil {
		t.Fatalf("decode canonical aggregate: %v", err)
	}
	tags := make([]string, len(root.Outbounds))
	for index := range root.Outbounds {
		tags[index] = root.Outbounds[index].Tag
	}
	return tags
}

func canonicalDocumentNodeBoundaryFixture(overLimit bool) string {
	// The framing, required type/tag pairs and empty bucket account for
	// fourteen JSON tokens. 65,529 key/value pairs add 131,058 tokens.
	// Adding one bucket element produces the single over-limit token.
	const paddingPairs = 65529
	var raw strings.Builder
	raw.Grow(1_200_000)
	raw.WriteString(`{"outbounds":[{"type":"direct","tag":"nodes"`)
	for index := 0; index < paddingPairs; index++ {
		fmt.Fprintf(&raw, `,"p%05d":0`, index)
	}
	raw.WriteString(`,"bucket":[`)
	if overLimit {
		raw.WriteByte('0')
	}
	raw.WriteString(`]}]}`)
	return raw.String()
}

func countCanonicalJSONTokens(t *testing.T, raw string) int {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	count := 0
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			return count
		}
		if err != nil {
			t.Fatalf("count generated JSON tokens: %v", err)
		}
		count++
	}
}

func requireGenericCanonicalInvalid(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errCanonicalAggregateInvalid) {
		t.Fatalf("error = %T %v, want generic canonical invalid", err, err)
	}
	diagnostic := err.Error()
	if diagnostic != "canonical aggregate invalid" {
		t.Fatalf("diagnostic = %q, want stable generic error", diagnostic)
	}
	for _, secret := range []string{
		"canonical-review-secret",
		"selector",
		"urltest",
		"detour",
		"outbounds",
	} {
		if strings.Contains(diagnostic, secret) {
			t.Fatalf("diagnostic leaked %q: %q", secret, diagnostic)
		}
	}
}
