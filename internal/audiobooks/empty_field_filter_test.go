// file: internal/audiobooks/empty_field_filter_test.go
// version: 1.0.0
// guid: 4e8b1a37-9d52-4c06-b3f8-27a1d0e5c964
// last-edited: 2026-08-13

package audiobooks

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// emptyFilterTestBook builds a book with a known title/narrator so the tests
// below can distinguish "matched because the filter is satisfied" from "matched
// because the filter constrained nothing".
func emptyFilterTestBook(title string) database.Book {
	n := "Some Narrator"
	return database.Book{
		ID:       "b1",
		Title:    title,
		Narrator: &n,
	}
}

// TestEmptyFilterValueDoesNotMatchEveryBook is the regression for the defect.
//
// strings.Contains(anything, "") is true in Go, so before the fix an empty
// filter value silently matched every book. Measured on production 2026-08-13:
// title="" returned all 63,870 books while title="zzqqxx" returned 0 — the
// filter worked, and only the empty value widened it to everything.
func TestEmptyFilterValueDoesNotMatchEveryBook(t *testing.T) {
	book := emptyFilterTestBook("The Hobbit")

	if matchesFieldFilters(book, []FieldFilter{{Field: "title", Value: ""}}) {
		t.Error("an empty filter value matched a book; it must fail closed, because " +
			"strings.Contains(x, \"\") is always true and would match the whole library")
	}
}

// TestEmptyFilterValueFailsClosedEvenWhenNegated pins the negated case
// separately. `title != ""` is no more a real constraint than `title == ""`, and
// leaving Negated to fall through would give the two an inconsistent answer.
func TestEmptyFilterValueFailsClosedEvenWhenNegated(t *testing.T) {
	book := emptyFilterTestBook("The Hobbit")

	if matchesFieldFilters(book, []FieldFilter{{Field: "title", Value: "", Negated: true}}) {
		t.Error("a negated empty filter value matched a book; neither == \"\" nor != \"\" " +
			"is a constraint a caller can have meant")
	}
}

// TestNonEmptyFiltersStillWork is the discriminating control. Without it, the
// two tests above would pass just as happily against a matcher that rejected
// EVERYTHING — which would be a far worse bug than the one being fixed.
func TestNonEmptyFiltersStillWork(t *testing.T) {
	book := emptyFilterTestBook("The Hobbit")

	cases := []struct {
		name   string
		filter FieldFilter
		want   bool
	}{
		{"substring match", FieldFilter{Field: "title", Value: "Hobbit"}, true},
		{"case-insensitive", FieldFilter{Field: "title", Value: "hobbit"}, true},
		{"non-matching value", FieldFilter{Field: "title", Value: "zzqqxx"}, false},
		{"negated non-match keeps the book", FieldFilter{Field: "title", Value: "zzqqxx", Negated: true}, true},
		{"negated match drops the book", FieldFilter{Field: "title", Value: "Hobbit", Negated: true}, false},
		{"other field still matches", FieldFilter{Field: "narrator", Value: "Narrator"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesFieldFilters(book, []FieldFilter{tc.filter}); got != tc.want {
				t.Errorf("matchesFieldFilters(%+v) = %v, want %v", tc.filter, got, tc.want)
			}
		})
	}
}

// TestEmptyValueRejectsTheWholeFilterSet checks the AND semantics: one empty
// value must sink the whole set, not just its own clause. A caller sending
// [title="Hobbit", narrator=""] would otherwise still get everything the first
// clause allowed while believing the second had narrowed it further.
func TestEmptyValueRejectsTheWholeFilterSet(t *testing.T) {
	book := emptyFilterTestBook("The Hobbit")

	filters := []FieldFilter{
		{Field: "title", Value: "Hobbit"}, // would match
		{Field: "narrator", Value: ""},    // must sink the set
	}
	if matchesFieldFilters(book, filters) {
		t.Error("a filter set containing one empty value matched; all filters are ANDed, " +
			"so an unusable clause must reject the set rather than be ignored")
	}
}

// TestFirstEmptyFilterValue covers the helper the two validation layers call.
func TestFirstEmptyFilterValue(t *testing.T) {
	t.Run("reports the offending field", func(t *testing.T) {
		field, empty := FirstEmptyFilterValue([]FieldFilter{
			{Field: "title", Value: "Hobbit"},
			{Field: "narrator", Value: ""},
		})
		if !empty {
			t.Fatal("FirstEmptyFilterValue did not flag an empty value")
		}
		if field != "narrator" {
			t.Errorf("FirstEmptyFilterValue reported field %q, want %q — the message shown "+
				"to the caller names this field", field, "narrator")
		}
	})

	t.Run("clean filter set passes", func(t *testing.T) {
		if _, empty := FirstEmptyFilterValue([]FieldFilter{
			{Field: "title", Value: "Hobbit"},
			{Field: "narrator", Value: "Someone"},
		}); empty {
			t.Error("FirstEmptyFilterValue flagged a filter set with no empty values")
		}
	})

	t.Run("no filters at all is not an empty value", func(t *testing.T) {
		if _, empty := FirstEmptyFilterValue(nil); empty {
			t.Error("FirstEmptyFilterValue flagged an absent filter set; omitting filters " +
				"legitimately means 'no constraint' and must stay allowed")
		}
	})
}
