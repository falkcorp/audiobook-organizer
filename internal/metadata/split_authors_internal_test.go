// file: internal/metadata/split_authors_internal_test.go
// version: 1.0.0
// guid: 8e4b7d20-6f19-4c53-a2b8-3d9e5f7c1a06
// last-edited: 2026-08-14

package metadata

import "testing"

// TestSplitMultipleAuthors_HTMLEntitySemicolons pins the C413 shear fix: an
// HTML entity's terminating ";" is NOT an author separator. Splitting
// "&#169;2013 by HarperCollinsPublishers" on the raw ";" minted an "&#169"
// author row (id 46583 in prod).
func TestSplitMultipleAuthors_HTMLEntitySemicolons(t *testing.T) {
	got := splitMultipleAuthors("&#169;2013 by HarperCollinsPublishers")
	if len(got) != 1 || got[0] != "&#169;2013 by HarperCollinsPublishers" {
		t.Fatalf("entity semicolon treated as separator: %#v", got)
	}

	// A REAL separator still splits, entity or not on either side.
	got = splitMultipleAuthors("Terry Pratchett; Neil Gaiman")
	if len(got) != 2 || got[0] != "Terry Pratchett" || got[1] != "Neil Gaiman" {
		t.Fatalf("plain separator broken: %#v", got)
	}

	// Mixed: entity inside a name survives, separator still works.
	got = splitMultipleAuthors("Johnson &amp; Johnson; Neil Gaiman")
	if len(got) != 2 || got[0] != "Johnson &amp; Johnson" || got[1] != "Neil Gaiman" {
		t.Fatalf("mixed entity + separator: %#v", got)
	}
}
