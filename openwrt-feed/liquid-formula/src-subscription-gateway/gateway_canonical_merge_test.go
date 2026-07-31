package main

import "testing"

func TestCanonicalAggregateUsesOccurrenceThenNodeOrder(t *testing.T) {
	objects := []string{
		`{"outbounds":[` +
			`{"type":"direct","tag":"A1","marker":"a1"},` +
			`{"marker":"a2","tag":"A2","type":"direct"}]}`,
		`{"outbounds":[` +
			`{"tag":"B1","type":"direct","marker":"b1"},` +
			`{"type":"direct","marker":"b2","tag":"B2"}]}`,
	}
	got, err := mergeCanonicalTestObjects(objects, []int{2, 1})
	if err != nil {
		t.Fatalf("ordered merge failed: %v", err)
	}
	want := `{"outbounds":[` +
		`{"marker":"b1","tag":"B1","type":"direct"},` +
		`{"marker":"b2","tag":"B2","type":"direct"},` +
		`{"marker":"a1","tag":"A1","type":"direct"},` +
		`{"marker":"a2","tag":"A2","type":"direct"}]}`
	if string(got) != want {
		t.Fatalf("ordered aggregate = %q, want %q", got, want)
	}
}

func TestCanonicalAggregateRejectsInvalidObjectReferences(t *testing.T) {
	tests := []struct {
		name        string
		objects     []string
		objectIndex int
	}{
		{
			name:        "zero is not a one based reference",
			objects:     []string{`{"outbounds":[{"type":"direct"}]}`},
			objectIndex: 0,
		},
		{
			name:        "negative reference",
			objects:     []string{`{"outbounds":[{"type":"direct"}]}`},
			objectIndex: -1,
		},
		{
			name:        "reference past object slice",
			objects:     []string{`{"outbounds":[{"type":"direct"}]}`},
			objectIndex: 2,
		},
		{
			name:        "reference into empty object slice",
			objects:     nil,
			objectIndex: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := mergeCanonicalTestObjects(
				test.objects, []int{test.objectIndex},
			); err == nil {
				t.Fatalf("invalid ObjectIndex %d was accepted",
					test.objectIndex)
			}
		})
	}
}

func TestCanonicalAggregateSortsUTF8KeysAndDisablesHTMLEscaping(t *testing.T) {
	object := `{"outbounds":[{` +
		`"😀":"emoji","é":"accent","z":"zed","type":"direct",` +
		`"tag":"keys","array":[3,2,1],"A":"upper","<html>":"<>&"` +
		`}]}`
	got, err := mergeCanonicalTestObjects([]string{object}, []int{1})
	if err != nil {
		t.Fatalf("canonical UTF-8 object rejected: %v", err)
	}
	want := `{"outbounds":[{` +
		`"<html>":"<>&","A":"upper","array":[3,2,1],` +
		`"tag":"keys","type":"direct","z":"zed",` +
		`"é":"accent","😀":"emoji"}]}`
	if string(got) != want {
		t.Fatalf("canonical UTF-8 aggregate = %q, want %q", got, want)
	}
}

func TestCanonicalAggregateDeduplicatesExactNodesWithoutTopLevelTag(t *testing.T) {
	objects := []string{
		`{"outbounds":[{` +
			`"type":"direct","tag":"first","nested":{"tag":"inner-a"},` +
			`"value":1}]}`,
		`{"outbounds":[` +
			`{"value":1.0,"nested":{"tag":"inner-a"},"tag":"duplicate",` +
			`"type":"direct"},` +
			`{"value":1e0,"nested":{"tag":"inner-b"},"tag":"nested-diff",` +
			`"type":"direct"}]}`,
	}
	got, err := mergeCanonicalTestObjects(objects, []int{1, 2})
	if err != nil {
		t.Fatalf("cross-source exact dedupe failed: %v", err)
	}
	want := `{"outbounds":[` +
		`{"nested":{"tag":"inner-a"},"tag":"first","type":"direct","value":1},` +
		`{"nested":{"tag":"inner-b"},"tag":"nested-diff",` +
		`"type":"direct","value":1}]}`
	if string(got) != want {
		t.Fatalf("deduplicated aggregate = %q, want %q", got, want)
	}
}

func TestCanonicalTagAllocationReservesFutureNamesAndTreatsSuffixesLiterally(
	t *testing.T,
) {
	object := `{"outbounds":[` +
		`{"type":"direct","tag":"X","id":1},` +
		`{"type":"direct","tag":"X","id":2},` +
		`{"type":"direct","tag":"X #2","id":3},` +
		`{"type":"direct","tag":"X","id":4},` +
		`{"type":"direct","tag":"X #2","id":5},` +
		`{"type":"direct","tag":"X #2 #2","id":6}` +
		`]}`
	got, err := mergeCanonicalTestObjects([]string{object}, []int{1})
	if err != nil {
		t.Fatalf("tag collision allocation failed: %v", err)
	}
	want := `{"outbounds":[` +
		`{"id":1,"tag":"X","type":"direct"},` +
		`{"id":2,"tag":"X #3","type":"direct"},` +
		`{"id":3,"tag":"X #2","type":"direct"},` +
		`{"id":4,"tag":"X #4","type":"direct"},` +
		`{"id":5,"tag":"X #2 #3","type":"direct"},` +
		`{"id":6,"tag":"X #2 #2","type":"direct"}]}`
	if string(got) != want {
		t.Fatalf("collision tags = %q, want %q", got, want)
	}
}

func TestCanonicalTagAllocationUsesUnnamedForMissingEmptyAndWhitespace(
	t *testing.T,
) {
	t.Run("Unnamed is used when free", func(t *testing.T) {
		got, err := mergeCanonicalTestObjects([]string{
			`{"outbounds":[{"type":"direct","id":1}]}`,
		}, []int{1})
		if err != nil {
			t.Fatalf("missing tag rejected: %v", err)
		}
		want := `{"outbounds":[{"id":1,"tag":"Unnamed","type":"direct"}]}`
		if string(got) != want {
			t.Fatalf("missing-tag aggregate = %q, want %q", got, want)
		}
	})

	t.Run("future explicit names stay available", func(t *testing.T) {
		object := `{"outbounds":[` +
			`{"type":"direct","id":1},` +
			`{"type":"direct","tag":"","id":2},` +
			`{"type":"direct","tag":" \t ","id":3},` +
			`{"type":"direct","tag":"Unnamed #2","id":4},` +
			`{"type":"direct","tag":"Unnamed","id":5}` +
			`]}`
		got, err := mergeCanonicalTestObjects([]string{object}, []int{1})
		if err != nil {
			t.Fatalf("Unnamed reservation failed: %v", err)
		}
		want := `{"outbounds":[` +
			`{"id":1,"tag":"Unnamed #3","type":"direct"},` +
			`{"id":2,"tag":"Unnamed #4","type":"direct"},` +
			`{"id":3,"tag":"Unnamed #5","type":"direct"},` +
			`{"id":4,"tag":"Unnamed #2","type":"direct"},` +
			`{"id":5,"tag":"Unnamed","type":"direct"}]}`
		if string(got) != want {
			t.Fatalf("Unnamed tags = %q, want %q", got, want)
		}
	})
}
