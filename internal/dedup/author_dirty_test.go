// file: internal/dedup/author_dirty_test.go
// version: 1.0.0
// guid: 5c8f2e91-3a67-4b04-9d2e-7f1a6c3b8d50
// last-edited: 2026-08-14

package dedup

import "testing"

// TestIsDirtyAuthorName_CopyrightAndEntityShrapnel pins the C413 creation-gate
// rules. The dirty side is drawn from REAL author rows minted from artist
// tags; the clean side guards against overreach (years inside names, real
// people, the ampersand that survives entity decoding).
func TestIsDirtyAuthorName_CopyrightAndEntityShrapnel(t *testing.T) {
	dirty := []string{
		"&#169",                                // row 46583 — entity shrapnel
		"&#169;2013 by HarperCollinsPublishers", // row 51870 — whole rights line
		"©2013 by HarperCollinsPublishers",     // decoded form is just as dirty
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
