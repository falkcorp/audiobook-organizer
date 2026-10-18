// file: internal/search/field_term_test.go
// version: 1.0.0
// guid: 554d8e4d-4fc1-4a8e-a7d4-b817a4325b65
// last-edited: 2026-10-18

package search

import (
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestFieldNodeForTerm_MatchesTheQueryParser(t *testing.T) {
	// A term built from a field/value pair must be the node the parser builds
	// for the same text, or the Library list and a dynamic collection would read
	// one query two ways.
	for _, tc := range []struct{ field, value string }{
		{"last_played", ">-30d"},
		{"last_played", "-30d..now"},
		{"last_played", "2026-01-01"},
		{"last_played", "*"},
		{"date_added", ">=2026-01-01"},
		{"date_modified", "<-1y"},
		{"progress_pct", "[25 TO 75]"},
		{"progress_pct", ">75"},
		{"read_status", "finished"},
	} {
		got, err := FieldNodeForTerm(tc.field, tc.value, false)
		if err != nil {
			t.Errorf("%s:%s: %v", tc.field, tc.value, err)
			continue
		}
		parsed, err := ParseQuery(tc.field + ":" + tc.value)
		if err != nil {
			t.Fatalf("%s:%s does not parse: %v", tc.field, tc.value, err)
		}
		want, ok := parsed.(*FieldNode)
		if !ok {
			t.Fatalf("%s:%s parsed to %T", tc.field, tc.value, parsed)
		}
		if *got != *want {
			t.Errorf("%s:%s: got %+v, parser built %+v", tc.field, tc.value, *got, *want)
		}
	}
}

func TestFieldNodeForTerm_QuotedIsLiteralAndJunkIsRefused(t *testing.T) {
	n, err := FieldNodeForTerm("last_played", ">-30d", true)
	if err != nil || n.Op != "" || n.Value != ">-30d" || !n.Quoted {
		t.Fatalf("a quoted value must stay literal, got %+v, %v", n, err)
	}
	for _, v := range []string{"a b", "(a|b)", ""} {
		if _, err := FieldNodeForTerm("read_status", v, false); err == nil {
			t.Errorf("read_status:%q must be refused", v)
		}
	}
}

func TestCheckDateNode(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, v := range []string{">-30d", "-30d..now", "2026-02-28", "*"} {
		n, _ := FieldNodeForTerm("last_played", v, false)
		if err := CheckDateNode(n, now); err != nil {
			t.Errorf("last_played:%s refused: %v", v, err)
		}
	}
	for v, want := range map[string]string{
		">30d":       "-30d",
		"2026-02-31": `invalid date "2026-02-31"`,
		"-30d..soon": "soon",
		"yesterday":  "yesterday",
	} {
		n, err := FieldNodeForTerm("last_played", v, false)
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		err = CheckDateNode(n, now)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("last_played:%s: got %v, want an error naming %q", v, err, want)
		}
	}
}

func TestMatchPerUserFilters_LastPlayedStarIsPresence(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	n, _ := FieldNodeForTerm("last_played", "*", false)
	f := []PerUserFilter{{Node: n}}
	if !MatchPerUserFilters(&database.UserBookState{LastActivityAt: now.AddDate(-3, 0, 0)}, f, now) {
		t.Error("last_played:* must match a book played at any time")
	}
	if MatchPerUserFilters(&database.UserBookState{}, f, now) || MatchPerUserFilters(nil, f, now) {
		t.Error("last_played:* must not match a book never played")
	}
	f[0].Negated = true
	if !MatchPerUserFilters(nil, f, now) {
		t.Error("-last_played:* must match a book never played")
	}
}
