// file: internal/personname/author_plausible_test.go
// version: 1.0.0
// guid: 3d0c9a8e-6f41-4b7a-9e25-8c1f4b6d2a70
// last-edited: 2026-09-25

package personname

import (
	"strings"
	"testing"
)

// TestIsPlausibleAuthorName_Junk: every string here was a real author row in
// production on 2026-09-25 (or is the exact shape of one). Each must be refused,
// and by the named rule, so a later edit that moves a string to a different rule
// is a visible, deliberate change.
func TestIsPlausibleAuthorName_Junk(t *testing.T) {
	cases := []struct {
		name string
		why  AuthorNameRejection
	}{
		{"", RejectEmpty},
		{"   ", RejectEmpty},
		{"&#169", RejectHTMLEntity},
		{"Master &amp; Apprentice", RejectHTMLEntity},
		{"&#xA9;2013 HarperCollins", RejectHTMLEntity},
		{"Stephen Hawking/Simon Prebble/(c) 2001 Stephen Hawking", RejectCopyright},
		{"©2013 by HarperCollinsPublishers", RejectCopyright},
		{"Copyright Stephen King", RejectCopyright},
		{"13 short stories", RejectStoryCount},
		{"22 Short Stories", RejectStoryCount},
		{"4 novellas", RejectStoryCount},
		{"3 Books", RejectStoryCount},
		{"14 BBY", RejectEraTag},
		{"0BBY", RejectEraTag},
		{"45 ABY", RejectEraTag},
		{"Lords of the Sith (14 BBY)", RejectEraTag},
		{"- Epigraph", RejectLeadingPunct},
		{"- Peter F. Hamilton", RejectLeadingPunct},
		{"+Brandon Sanderson", RejectLeadingPunct},
		{"[PZG]", RejectLeadingPunct},
		{"_static", RejectLeadingPunct},
		{"(c) 2001 Stephen Hawking", RejectCopyright},
		{"Book 1 (Unabridged)", RejectEditionMarker},
		{"Fresh Beginnings (Unabridged)", RejectEditionMarker},
		{"Unabridged", RejectEditionMarker},
		{"read by narrator", RejectReadBy},
		{"Read by Robin Sachs", RejectReadBy},
		{"Narrated by: Christina Traister", RejectReadBy},
		{"Lords of the Sith_418m_07s_99h__477m_51s_99h", RejectTimecode},
		{"000m_00s__056m_16s_43h", RejectTimecode},
		{"Track01", RejectTimecode},
		{"CD2", RejectTimecode},
		{"Disk 2 -Track 01", RejectTimecode},
		{"02-25", RejectPureNumber},
		{"2", RejectPureNumber},
		{"1-14", RejectPureNumber},
		{"Unknown", RejectPlaceholder},
		{"Unknown Author", RejectPlaceholder},
		{"Various Artists", RejectPlaceholder},
		{"n/a", RejectPlaceholder},
		{"None", RejectPlaceholder},
		{"Audiobook", RejectPlaceholder},
		{"Epigraph", RejectStructural},
		{"Prologue", RejectStructural},
		{"Epilogue", RejectStructural},
		{"Chapter 12", RejectStructural},
		{"Part One", RejectStructural},
		{"Book IV", RejectStructural},
		{"Vol. 01", RejectStructural},
		{"Disc 3", RejectTimecode},
		{"Opening Credits", RejectStructural},
		{strings.Repeat("Listening-to-Web-Of-Worlds-", 4), RejectTooLong},
	}
	for _, tc := range cases {
		ok, why := IsPlausibleAuthorName(tc.name)
		if ok {
			t.Errorf("IsPlausibleAuthorName(%q) = true; want refused (%s)", tc.name, tc.why)
			continue
		}
		if why != tc.why {
			t.Errorf("IsPlausibleAuthorName(%q) refused by %q; want %q", tc.name, why, tc.why)
		}
	}
}

// TestIsPlausibleAuthorName_RealNames: real authors, most of them from the
// production author table, chosen for the shapes a junk rule could catch by
// accident -- single-word pen names, lowercase pen names, digits, initials,
// particles, apostrophes, non-Latin scripts and long multi-author credit lists.
func TestIsPlausibleAuthorName_RealNames(t *testing.T) {
	for _, name := range []string{
		"Zogarth", "pirateaba", "RavensDagger", "Radclyffe", "Jae",
		"SenescentSoul", "Actus", "Monsoon117", "Homer", "Voltaire",
		"J. N. Chaney", "M.E. Thorne", "M. E. Thorne", "J.R.R. Tolkien",
		"R. A. Mejia", "James S. A. Corey", "George R. R. Martin",
		"Ursula K. Le Guin", "Ludwig van Beethoven", "Simone de Beauvoir",
		"Joseph Sheridan Le Fanu", "Fallon O'Dowd", "Aaron Dembski-Bowden",
		"Junot Díaz", "P. Djèlí Clark", "Émile Zola", "村上 春樹",
		"50 Cent", "Anonymous", "Book Group Author Ann Smith",
		"Chapter House Press Staff", "Parker Part", "Bookman Bishop",
		"Big Finish Productions", "Brandon Sanderson", "Terry Pratchett",
		"Stephen King", "Dakota Krout", "Shirtaloon", "Cassius Lange",
		// A long anthology credit: over the length cap, but every part is a
		// real name, so it is a composite for the split tooling, not junk.
		"Greg Bear, Gregory Benford, Ben Bova, David Brin, Neil Gaiman, Harry Harrison, Larry Niven",
	} {
		if ok, why := IsPlausibleAuthorName(name); !ok {
			t.Errorf("IsPlausibleAuthorName(%q) refused (%s); want accepted -- a real author", name, why)
		}
	}
}

// TestPrepareAuthorNameForCreation covers the salvage step: positional
// numbering is stripped, a leading dash is NOT (most "- X" rows are titles), and
// whatever survives must pass the gate.
func TestPrepareAuthorNameForCreation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"001-147 Kevin J Anderson", "Kevin J Anderson"},
		{"  Brandon   Sanderson ", "Brandon Sanderson"},
		{"S.A. Chakraborty", "S. A. Chakraborty"},
		{"Zogarth", "Zogarth"},
		{"- The Green Mile", ""},
		{"001_Celestia", ""},
		{"read by narrator", ""},
		{"14 BBY", ""},
		{"Epigraph", ""},
	} {
		got, why := PrepareAuthorNameForCreation(tc.in)
		if got != tc.want {
			t.Errorf("PrepareAuthorNameForCreation(%q) = %q (%s); want %q", tc.in, got, why, tc.want)
		}
		if (got == "") != (why != "") {
			t.Errorf("PrepareAuthorNameForCreation(%q) = %q with reason %q; a refusal must carry a reason and an accept must not", tc.in, got, why)
		}
	}
}

// TestCleanAuthorNameForCreationNowRefusesTheReportedJunk pins the strings the
// 2026-09-25 probe showed the old CleanAuthorNameForCreation accepting.
func TestCleanAuthorNameForCreationNowRefusesTheReportedJunk(t *testing.T) {
	for _, in := range []string{
		"14 BBY", "13 short stories", "(c) 2001 Stephen Hawking", "- Epigraph",
		"read by narrator", "Book 1 (Unabridged)", "Lords of the Sith (14 BBY)",
	} {
		if got, ok := CleanAuthorNameForCreation(in); ok {
			t.Errorf("CleanAuthorNameForCreation(%q) = %q, true; want refused", in, got)
		}
	}
}
