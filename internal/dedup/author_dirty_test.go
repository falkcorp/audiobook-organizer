// file: internal/dedup/author_dirty_test.go
// version: 1.3.0
// guid: 5c8f2e91-3a67-4b04-9d2e-7f1a6c3b8d50
// last-edited: 2026-10-21

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestIsDirtyAuthorName_CopyrightAndEntityShrapnel pins the C413 creation-gate
// rules. The dirty side is drawn from REAL author rows minted from artist
// tags; the clean side guards against overreach (years inside names, real
// people, the ampersand that survives entity decoding).
func TestIsDirtyAuthorName_CopyrightAndEntityShrapnel(t *testing.T) {
	dirty := []string{
		"&#169",                                 // row 46583 — entity shrapnel
		"&#169;2013 by HarperCollinsPublishers", // row 51870 — whole rights line
		"©2013 by HarperCollinsPublishers",      // decoded form is just as dirty
		"© Big Finish",
		"2013 by HarperCollinsPublishers", // leading standalone year
		"BBC Audio",                       // existing publisher rule still holds
	}
	for _, name := range dirty {
		if !IsDirtyAuthorName(name) {
			t.Errorf("IsDirtyAuthorName(%q) = false, want true", name)
		}
	}
	clean := []string{
		"Brandon Sanderson",
		"J. R. R. Tolkien",
		"Agent 47 Fan",       // year-like digits NOT leading
		"Simone de Beauvoir", // lowercase particle mid-name
	}
	for _, name := range clean {
		if IsDirtyAuthorName(name) {
			t.Errorf("IsDirtyAuthorName(%q) = true, want false", name)
		}
	}
}

// TestFindDuplicateAuthors_ExcludesPartTrackMarkers: filename shrapnel such as
// "Charles Dickens CD2" is not a person, so rows of it are never grouped
// (owner-approved 2026-10-21). Every pair below is one the code BEFORE this
// rule grouped (identical or case-only twins), so the test fails against the
// old newAuthorPrecomputed; the last pair is the positive control that real
// duplicates are still found.
func TestFindDuplicateAuthors_ExcludesPartTrackMarkers(t *testing.T) {
	authors := []database.Author{
		{ID: 1, Name: "Charles Dickens CD2"},
		{ID: 2, Name: "Charles Dickens CD2"},
		{ID: 3, Name: "Maroon 5"},
		{ID: 4, Name: "maroon 5"},
		{ID: 5, Name: "Pip Estella 2 (Unabridged)"},
		{ID: 6, Name: "Pip Estella 2 (Unabridged)"},
		{ID: 7, Name: "Charles Dickens"},
		{ID: 8, Name: "Charles  Dickens"},
	}
	groups := FindDuplicateAuthors(authors, 0.9, func(int) int { return 1 })
	control := false
	for _, g := range groups {
		ids := []int{g.Canonical.ID}
		for _, v := range g.Variants {
			ids = append(ids, v.ID)
		}
		for _, id := range ids {
			if id <= 6 {
				t.Errorf("junk author id %d grouped: canonical=%d ids=%v", id, g.Canonical.ID, ids)
			}
		}
		if g.Canonical.ID == 7 || g.Canonical.ID == 8 {
			control = true
		}
	}
	if !control {
		t.Errorf("positive control: real duplicate pair 7/8 not grouped: %+v", groups)
	}
}
