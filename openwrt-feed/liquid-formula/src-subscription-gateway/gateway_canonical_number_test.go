package main

import (
	"strings"
	"testing"
)

func TestCanonicalAggregateUsesJSONNumbersWithoutFloatConversion(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain integer", raw: "1", want: "1"},
		{name: "decimal integer", raw: "1.0", want: "1"},
		{name: "scientific integer", raw: "1e0", want: "1"},
		{name: "negative exponent still integral", raw: "10e-1", want: "1"},
		{name: "decimal and exponent normalize", raw: "1.2300e2", want: "123"},
		{name: "negative integer", raw: "-10e-1", want: "-1"},
		{name: "negative zero", raw: "-0", want: "0"},
		{name: "negative decimal zero", raw: "-0.000", want: "0"},
		{name: "negative scientific zero", raw: "-0e999", want: "0"},
		{
			name: "integer beyond exact float range",
			raw:  "9007199254740993",
			want: "9007199254740993",
		},
		{
			name: "arbitrary precision integer",
			raw:  "12345678901234567890123456789012345678901234567890",
			want: "12345678901234567890123456789012345678901234567890",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := mergeCanonicalTestObjects(
				[]string{canonicalNumberDocument(test.raw)}, []int{1},
			)
			if err != nil {
				t.Fatalf("integral number %q rejected: %v", test.raw, err)
			}
			want := `{"outbounds":[{"tag":"number","type":"direct","value":` +
				test.want + `}]}`
			if string(got) != want {
				t.Fatalf("canonical aggregate = %q, want %q", got, want)
			}
		})
	}
}

func TestCanonicalAggregateRejectsNonintegralJSONNumbers(t *testing.T) {
	for _, raw := range []string{
		"1.5",
		"1e-1",
		"15e-1",
		"-0.0001",
		"123456789012345678901234567890.000000000000000000000000000001",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := mergeCanonicalTestObjects(
				[]string{canonicalNumberDocument(raw)}, []int{1},
			); err == nil {
				t.Fatalf("nonintegral number %q was accepted", raw)
			}
		})
	}
}

func TestCanonicalIntegerRawAndExpandedLimitsAreInclusive(t *testing.T) {
	const integerLimit = 64 * 1024

	t.Run("raw and expanded exact limit", func(t *testing.T) {
		number := "1" + strings.Repeat("0", integerLimit-1)
		got, err := mergeCanonicalTestObjects(
			[]string{canonicalNumberDocument(number)}, []int{1},
		)
		if err != nil {
			t.Fatalf("exact 64 KiB integer rejected: %v", err)
		}
		want := `{"outbounds":[{"tag":"number","type":"direct","value":` +
			number + `}]}`
		if string(got) != want {
			t.Fatal("exact-limit integer was not preserved canonically")
		}
	})

	t.Run("raw over limit", func(t *testing.T) {
		// The exact value is only 1, so this must be rejected by the raw-token
		// bound rather than incidentally by the expanded-integer bound.
		number := "1." + strings.Repeat("0", integerLimit-1)
		if _, err := mergeCanonicalTestObjects(
			[]string{canonicalNumberDocument(number)}, []int{1},
		); err == nil {
			t.Fatal("64 KiB + 1 raw integer was accepted")
		}
	})

	t.Run("expanded exact limit", func(t *testing.T) {
		got, err := mergeCanonicalTestObjects(
			[]string{canonicalNumberDocument("1e65535")}, []int{1},
		)
		if err != nil {
			t.Fatalf("exact 64 KiB expanded integer rejected: %v", err)
		}
		number := "1" + strings.Repeat("0", integerLimit-1)
		want := `{"outbounds":[{"tag":"number","type":"direct","value":` +
			number + `}]}`
		if string(got) != want {
			t.Fatal("exact-limit exponent was not expanded canonically")
		}
	})

	t.Run("expanded over limit is rejected before expansion", func(t *testing.T) {
		if _, err := mergeCanonicalTestObjects(
			[]string{canonicalNumberDocument("1e65536")}, []int{1},
		); err == nil {
			t.Fatal("an exponent expanding past 64 KiB was accepted")
		}
	})

	t.Run("unrepresentably large exponent is rejected", func(t *testing.T) {
		raw := "1e" + strings.Repeat("9", 128)
		if _, err := mergeCanonicalTestObjects(
			[]string{canonicalNumberDocument(raw)}, []int{1},
		); err == nil {
			t.Fatal("an exponent requiring unbounded expansion was accepted")
		}
	})
}

func canonicalNumberDocument(number string) string {
	return `{"outbounds":[{"value":` + number +
		`,"type":"direct","tag":"number"}]}`
}

func mergeCanonicalTestObjects(
	objects []string,
	orderedObjectIndexes []int,
) ([]byte, error) {
	candidate := generationCandidate{
		Objects: make([][]byte, len(objects)),
		Sources: make(
			[]generationCandidateSource, len(orderedObjectIndexes),
		),
	}
	for index, object := range objects {
		candidate.Objects[index] = []byte(object)
	}
	for index, objectIndex := range orderedObjectIndexes {
		candidate.Sources[index] = generationCandidateSource{
			Index:       index + 1,
			ObjectIndex: objectIndex,
		}
	}
	return mergeCanonicalAggregate(candidate)
}
