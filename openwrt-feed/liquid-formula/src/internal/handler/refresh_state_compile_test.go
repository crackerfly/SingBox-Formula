package handler

import "testing"

func TestInjectNotesFilterContextRewritesOnlyActualFilters(t *testing.T) {
	input := `{
  "literal": "| NotesName",
  "a": {{ ""|NotesName }},
  "b": {{ "HK" |  NotesName }},
  "c": {{ "| NotesName" | escape }},
  "d": {{ "SG" | NotesName:existing }},
  "e": {{ "JP" | NotesNameExtra }}
}
{# | NotesName #}`
	want := `{
  "literal": "| NotesName",
  "a": {{ ""|NotesName:__refresh_filter }},
  "b": {{ "HK" |  NotesName:__refresh_filter }},
  "c": {{ "| NotesName" | escape }},
  "d": {{ "SG" | NotesName:existing }},
  "e": {{ "JP" | NotesNameExtra }}
}
{# | NotesName #}`
	if got := injectNotesFilterContext(input); got != want {
		t.Fatalf("injectNotesFilterContext() =\n%s\nwant:\n%s", got, want)
	}
}

func TestCompileTemplateAcceptsNotesNameWhitespaceVariants(t *testing.T) {
	ensureNotesFilter()
	for _, source := range []string{
		`{{ ""|NotesName }}`,
		"{{ \"HK\" |\tNotesName }}",
		`{{ "SG" |  NotesName }}`,
	} {
		if _, err := compileTemplate([]byte(source)); err != nil {
			t.Fatalf("compileTemplate(%q) error = %v", source, err)
		}
	}
}
