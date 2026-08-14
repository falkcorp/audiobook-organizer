// file: internal/dedup/author_leading_conjunction_test.go
// version: 1.0.0
// guid: 7c2d9e14-5b83-4a67-9f02-1d8e3a6b4c95
// last-edited: 2026-08-14

package dedup

import "testing"

// TestSplitCompositeAuthorName_OxfordCommaAmpersand pins the defect that created
// 48 author rows beginning with "& " in production (ids 46411-46764, one Big
// Finish import run).
//
// The literal string below is the real album_artist tag read from
// "The Creed of the Kromon - Paul McGann - read by Philip Martin.m4b" on
// 2026-08-13. It is NOT a synthetic example: an Oxford comma before the
// ampersand is the only comma shape that strands the "&" on the final name.
// A non-Oxford "A, B & C" yields "B & C", which is a different (and much more
// visible) corruption.
//
// The comma branch of SplitCompositeAuthorName runs before the " & " branch and
// accepts any part containing a space, so "& Conrad Westmaas" passes validation
// and NormalizeAuthorName never strips the leading conjunction.
func TestSplitCompositeAuthorName_OxfordCommaAmpersand(t *testing.T) {
	const realWorldTag = "Paul McGann, India Fisher, & Conrad Westmaas"

	got := SplitCompositeAuthorName(realWorldTag)
	want := []string{"Paul McGann", "India Fisher", "Conrad Westmaas"}

	if len(got) != len(want) {
		t.Fatalf("SplitCompositeAuthorName(%q) = %v (len %d), want %v (len %d)",
			realWorldTag, got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %q, want %q (leading conjunction not stripped)", i, got[i], want[i])
		}
	}
}

// TestSplitCompositeAuthorName_LeadingConjunctionAllBranches proves the fix is
// at the shared chokepoint rather than patched into the comma branch alone.
// Every delimiter branch funnels through NormalizeAuthorName, and every one of
// them had the same hole: the per-part validity test only asks whether the part
// contains a space, which "& Some Name" satisfies.
func TestSplitCompositeAuthorName_LeadingConjunctionAllBranches(t *testing.T) {
	tests := []struct {
		branch string
		input  string
		want   []string
	}{
		{"comma", "Paul McGann, India Fisher, & Conrad Westmaas",
			[]string{"Paul McGann", "India Fisher", "Conrad Westmaas"}},
		{"slash", "Jane Roe / & John Doe",
			[]string{"Jane Roe", "John Doe"}},
		{"semicolon", "Jane Roe; & John Doe",
			[]string{"Jane Roe", "John Doe"}},
		{"comma-and-word", "Paul McGann, India Fisher, and Conrad Westmaas",
			[]string{"Paul McGann", "India Fisher", "Conrad Westmaas"}},
	}

	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			got := SplitCompositeAuthorName(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("SplitCompositeAuthorName(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("part %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestNormalizeAuthorName_LeadingConjunction guards the chokepoint directly,
// including the two rows that must NOT be rewritten.
//
// "&#169" is a decapitated HTML entity (©) that also reached the author table.
// It is a DIFFERENT defect (copyright text in the artist tag) and a bare "^&"
// strip would mangle it into "#169" — strictly worse than leaving it. Requiring
// whitespace after the conjunction is what keeps the two bugs separate.
func TestNormalizeAuthorName_LeadingConjunction(t *testing.T) {
	tests := []struct{ in, want string }{
		// stripped
		{"& Conrad Westmaas", "Conrad Westmaas"},
		{"&  Lisa Bowerman", "Lisa Bowerman"},
		{"and Sadie Miller", "Sadie Miller"},
		{"And Sadie Miller", "Sadie Miller"},
		{"AND Sadie Miller", "Sadie Miller"},
		// NOT stripped — no trailing whitespace after the conjunction
		{"&#169", "&#169"},
		{"&#169;2013 by HarperCollinsPublishers", "&#169;2013 by HarperCollinsPublishers"},
		// NOT stripped — "and" is a prefix of a real name, not a conjunction
		{"Anders Bergman", "Anders Bergman"},
		{"Andrea Cremer", "Andrea Cremer"},
		// unaffected ordinary names
		{"Paul McGann", "Paul McGann"},
		{"S.A. Corey", "S. A. Corey"},
	}
	for _, tt := range tests {
		if got := NormalizeAuthorName(tt.in); got != tt.want {
			t.Errorf("NormalizeAuthorName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
