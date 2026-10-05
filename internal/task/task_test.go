package task

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCriteriaGetStableIDs(t *testing.T) {
	md := `# Rate-limit /api/login

Failed logins should be throttled.

## Acceptance criteria
- After 5 failed logins, further attempts get HTTP 429.
- [ ] The limit resets after 15 minutes
  of the first failure.
* A successful login clears the count.
1. Numbered items count too.

## Notes
- not a criterion
`
	got := Parse(md)
	if got.Title != "Rate-limit /api/login" {
		t.Fatalf("Title = %q", got.Title)
	}
	want := []Criterion{
		{ID: "AC-1", Text: "After 5 failed logins, further attempts get HTTP 429."},
		{ID: "AC-2", Text: "The limit resets after 15 minutes of the first failure."},
		{ID: "AC-3", Text: "A successful login clears the count."},
		{ID: "AC-4", Text: "Numbered items count too."},
	}
	if !reflect.DeepEqual(got.Criteria, want) {
		t.Fatalf("Criteria = %#v\nwant %#v", got.Criteria, want)
	}
}

func TestParsePlainTextTaskHasTitleAndNoCriteria(t *testing.T) {
	got := Parse("fix the flaky login test\nit fails on CI")
	if got.Title != "fix the flaky login test" {
		t.Fatalf("Title = %q", got.Title)
	}
	if len(got.Criteria) != 0 {
		t.Fatalf("Criteria = %v, want none", got.Criteria)
	}
}

func TestParseIgnoresHeadingsInsideCodeFences(t *testing.T) {
	md := "# T\n\n```md\n## Acceptance criteria\n- fenced\n```\n"
	if got := Parse(md); len(got.Criteria) != 0 {
		t.Fatalf("Criteria = %v, want none", got.Criteria)
	}
}

func TestParseHeadingIsCaseInsensitive(t *testing.T) {
	got := Parse("# T\n## acceptance Criteria\n- one\n")
	if len(got.Criteria) != 1 || got.Criteria[0].Text != "one" {
		t.Fatalf("Criteria = %v", got.Criteria)
	}
}

func TestStripCommentsAndEmpty(t *testing.T) {
	if s := StripComments(EditorTemplate); !IsEmpty(s) {
		t.Fatalf("untouched template is not empty: %q", s)
	}
	s := StripComments("<!-- note -->\n# Do it\n<!--\nmulti\n-->\nbody")
	if s != "\n# Do it\n\nbody" {
		t.Fatalf("StripComments = %q", s)
	}
}

func TestParseCRLF(t *testing.T) {
	md := "# Rate-limit /api/login\r\n\r\n## Acceptance criteria\r\n- After 5 failed logins, get HTTP 429.\r\n- The limit resets\r\n  after 15 minutes.\r\n"
	got := Parse(md)
	if got.Title != "Rate-limit /api/login" {
		t.Fatalf("Title = %q", got.Title)
	}
	want := []Criterion{
		{ID: "AC-1", Text: "After 5 failed logins, get HTTP 429."},
		{ID: "AC-2", Text: "The limit resets after 15 minutes."},
	}
	if !reflect.DeepEqual(got.Criteria, want) {
		t.Fatalf("Criteria = %#v", got.Criteria)
	}
	if strings.Contains(got.Text, "\r") {
		t.Fatalf("Text keeps CR: %q", got.Text)
	}
}
