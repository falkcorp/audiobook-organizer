// file: internal/search/per_user_term_test.go
// version: 1.0.0
// guid: 6e2c9a14-d5b7-4f83-a0e6-1b94c7d2f305
// last-edited: 2026-10-26

package search

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

var termNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// The Library list sends a per-user term as a field/value pair. What it accepts
// must be what CheckPerUserValues accepts in a query string: before, -read_status:
// completed was accepted there and returned every book, and progress_pct:[* TO 50]
// was accepted and matched none.
func TestPerUserFilterForTerm_RefusesWhatTheQueryStringRefuses(t *testing.T) {
	refused := []struct {
		field, value string
		quoted       bool
		want         string
	}{
		{"read_status", "completed", false, "read_status must be one of"},
		{"read_status", "in progress", true, "read_status must be one of"},
		{"read_status", "(finished|in-progress)", false, "in-progress"},
		{"read_status", "fin*", false, "patterns"},
		{"read_status", ">finished", false, "not a comparison"},
		{"progress_pct", "[* TO 50]", false, "open end"},
		{"progress_pct", "!=100", false, "!="},
		{"progress_pct", "==50", false, "=="},
		{"progress_pct", ">half", false, "progress_pct takes a number"},
		{"progress_pct", "50*", false, "patterns"},
		{"last_played", ">30d", false, "last_played"},
		{"last_played", "(yesterday)", false, "last_played"},
		{"read_status", "()", false, "lists no values"},
	}
	for _, tc := range refused {
		for _, negated := range []bool{false, true} {
			_, err := PerUserFilterForTerm(tc.field, tc.value, tc.quoted, negated, termNow)
			if err == nil {
				t.Errorf("PerUserFilterForTerm(%s, %q, negated=%v) accepted a value the matcher cannot evaluate", tc.field, tc.value, negated)
				continue
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("PerUserFilterForTerm(%s, %q) = %q, want it to mention %q", tc.field, tc.value, err.Error(), tc.want)
			}
			if !IsPerUserQueryError(err) && !strings.Contains(err.Error(), "unexpected") {
				t.Errorf("PerUserFilterForTerm(%s, %q) = %v: not a per-user query error", tc.field, tc.value, err)
			}
		}
	}
}

func TestPerUserFilterForTerm_AcceptsAndBuildsTheFormsTheMatcherEvaluates(t *testing.T) {
	for _, tc := range []struct{ field, value string }{
		{"read_status", "finished"},
		{"read_status", "FINISHED"},
		{"read_status", "(finished|in_progress)"},
		{"read_status", "(finished | in_progress)"},
		{"progress_pct", ">50"},
		{"progress_pct", "[10 TO 90]"},
		{"progress_pct", "(10|20)"},
		{"last_played", "*"},
		{"last_played", ">-30d"},
		{"last_played", "(2026-01-01|2026-02-01)"},
	} {
		if _, err := PerUserFilterForTerm(tc.field, tc.value, false, false, termNow); err != nil {
			t.Errorf("PerUserFilterForTerm(%s, %q) = %v, want it accepted", tc.field, tc.value, err)
		}
	}

	// An alternation is ONE filter that ORs inside itself, and its negation is
	// "none of them": the same filter the translator builds for the query string.
	f, err := PerUserFilterForTerm("read_status", "(finished|in_progress)", false, false, termNow)
	if err != nil || len(f.Alternatives) != 2 || f.Node != nil {
		t.Fatalf("alternation = %+v, %v; want one filter holding 2 alternatives", f, err)
	}
	finished := &database.UserBookState{Status: database.UserBookStatusFinished}
	reading := &database.UserBookState{Status: database.UserBookStatusInProgress}
	fresh := &database.UserBookState{Status: database.UserBookStatusUnstarted}
	if !MatchPerUserFilters(finished, []PerUserFilter{f}, termNow) || !MatchPerUserFilters(reading, []PerUserFilter{f}, termNow) ||
		MatchPerUserFilters(fresh, []PerUserFilter{f}, termNow) {
		t.Errorf("read_status:(finished|in_progress) must hold for finished and in_progress and not for unstarted")
	}
	neg, err := PerUserFilterForTerm("read_status", "(finished|in_progress)", false, true, termNow)
	if err != nil || MatchPerUserFilters(finished, []PerUserFilter{neg}, termNow) || !MatchPerUserFilters(fresh, []PerUserFilter{neg}, termNow) {
		t.Errorf("-read_status:(finished|in_progress) must hold for none of them, got %+v, %v", neg, err)
	}

	// A single value is a plain node, negation carried.
	one, err := PerUserFilterForTerm("read_status", "finished", false, true, termNow)
	if err != nil || one.Node == nil || !one.Negated || len(one.Alternatives) != 0 {
		t.Errorf("single term = %+v, %v; want a negated Node", one, err)
	}
}

// A one-value alternation is the single term, as the translator builds it.
func TestPerUserAltFilter_OneValueIsASingleTermAndNoValueIsNoFilter(t *testing.T) {
	one, ok := perUserAltFilter(&ValueAltNode{Field: "read_status", Values: []string{"finished"}}, true)
	if !ok || one.Node == nil || !one.Negated || len(one.Alternatives) != 0 {
		t.Errorf("one value = %+v, %v", one, ok)
	}
	if _, ok := perUserAltFilter(&ValueAltNode{Field: "read_status"}, false); ok {
		t.Errorf("no values must give no filter")
	}
}

func TestNodeForTerm_ReturnsAnAlternationAndFieldNodeForTermRefusesIt(t *testing.T) {
	n, err := NodeForTerm("last_played", "(-30d|-60d)", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.(*ValueAltNode); !ok {
		t.Fatalf("NodeForTerm = %T, want *ValueAltNode", n)
	}
	if !HasRelativeDate(n) {
		t.Errorf("an alternation of relative dates must report a relative date")
	}
	if _, err := FieldNodeForTerm("last_played", "(-30d|-60d)", false); err == nil {
		t.Errorf("FieldNodeForTerm must keep refusing an alternation")
	}
}

// N-k: the long s (U+017F) folds to s under Go's (?i), the rule that makes a
// collection per-viewer, and not under ToLower. A name spelled with it is a
// per-viewer term to the text rule and an unknown field to the translator.
func TestCheckPerUserValues_RefusesAFieldNameThatOnlyCaseFoldingMatches(t *testing.T) {
	for _, field := range []string{"read_statuſ", "READ_STATUS", "Progress_pct", "last_playeD"} {
		ast := &FieldNode{Field: field, Value: "finished"}
		err := CheckPerUserValues(ast, termNow)
		var ve *PerUserValueError
		if !errors.As(err, &ve) || !strings.Contains(err.Error(), "lowercase") {
			t.Errorf("CheckPerUserValues(%q) = %v, want a lowercase refusal", field, err)
		}
	}
	for _, field := range []string{"read_status", "title", "author"} {
		if err := CheckPerUserValues(&FieldNode{Field: field, Value: "finished"}, termNow); err != nil && field != "read_status" {
			t.Errorf("CheckPerUserValues(%q) = %v", field, err)
		}
	}
}

// N-j (empty alternation): the translator drops a per-user alternation with no
// values, so title:Hush -read_status:() was just title:Hush and was accepted.
func TestCheckPerUserValues_RefusesAnEmptyPerUserAlternation(t *testing.T) {
	for _, q := range []string{"title:Hush -read_status:()", "read_status:()", "progress_pct:( | )", "last_played:()"} {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("fixture %q does not parse: %v", q, err)
		}
		err = CheckPerUserValues(ast, termNow)
		if err == nil || !strings.Contains(err.Error(), "lists no values") {
			t.Errorf("CheckPerUserValues(%q) = %v, want an empty-alternation refusal", q, err)
		}
	}
	// An empty alternation on an ordinary field is not this check's business.
	ast, _ := ParseQuery("title:()")
	if err := CheckPerUserValues(ast, termNow); err != nil {
		t.Errorf("title:() = %v", err)
	}
}

// N-5: a comparison inside ( | ) is refused with a message that says so, not with
// "as in progress_pct:>50", which is the form the person just wrote.
func TestCheckPerUserValues_ComparisonInsideAnAlternationSaysWhyAndWhatToWrite(t *testing.T) {
	for q, want := range map[string]string{
		"progress_pct:(>50|<10)":                   "not the comparison",
		"progress_pct:([10 TO 90]|5)":              "not the comparison",
		"last_played:(>-30d|2026-01-01)":           "not the comparison",
		"last_played:(2026-01-01..2026-02-01|-3d)": "not the comparison",
	} {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("fixture %q does not parse: %v", q, err)
		}
		err = CheckPerUserValues(ast, termNow)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "inside ( | )") {
			t.Errorf("CheckPerUserValues(%q) = %v, want %q and 'inside ( | )'", q, err, want)
			continue
		}
		if strings.Contains(err.Error(), "as in progress_pct:>50 or") {
			t.Errorf("CheckPerUserValues(%q) = %q points at the form the person wrote", q, err)
		}
	}
}

func TestPerUserQueryReason_DropsTheWrappersACallerAdds(t *testing.T) {
	ast, _ := ParseQuery("read_status:completed")
	inner := CheckPerUserValues(ast, termNow)
	wrapped := errors.Join(errors.New("search"), inner)
	if got := PerUserQueryReason(wrapped); got != inner.Error() {
		t.Errorf("PerUserQueryReason(wrapped value error) = %q, want %q", got, inner.Error())
	}
	shape := CheckPerUserBoolean(mustParse(t, "read_status:finished OR title:Hush"))
	if got := PerUserQueryReason(errors.Join(errors.New("query"), shape)); got != ErrPerUserBoolean.Error() {
		t.Errorf("PerUserQueryReason(wrapped shape error) = %q", got)
	}
	if got := PerUserQueryReason(errors.New("boom")); got != "boom" {
		t.Errorf("PerUserQueryReason(other) = %q", got)
	}
}
