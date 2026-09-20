// file: internal/plugins/maintenance/author_path_link_guards_test.go
// version: 1.0.1
// guid: 8f21c5a7-4d63-4b90-a1e8-6c07f2d95b31
// last-edited: 2026-09-19

package maintenance

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
)

// The SAFETY BARS of maintenance.author-path-link, in their own file because
// each of them was written against a row the 2026-09-19 prod dry run found and
// each of them failed on that row. The fixture lives in
// author_path_link_test.go.

// 🔴 THE THIN BAR READS THE ROW'S OWN COUNT, NOT JUST THE SCALAR ONE.
//
// The Freedom's Dawn shape, measured on the 2026-09-19 prod dry run: author row
// 43771 is a title, 110 live books already carry it as their SCALAR author id,
// and GET /api/v1/authors/43771 reports book_count 0 because those books' join
// rows credit somebody else. The old bar read only the scalar count, so the row
// read as fat (110 > 1), sailed past the bar its own comment cites it as the
// reason for, and all 110 path matches classified as would_link -- an apply
// would have doubled the row. Before this fix the outcome here is "would_link".
func TestAuthorPathLink_ThinRowReadsTheRowsOwnCount(t *testing.T) {
	f := &pathLinkFixture{}
	f.author(43771, "Freedoms Dawn") // the possessive is exercised separately below
	f.filler(43771, 110)             // scalar reach: 110 books
	f.ownCount(43771, 0)             // the row's own count, as the authors endpoint serves it
	f.book("fd", "/mnt/bigdata/books/audiobook-organizer/Freedoms Dawn/Disc 1/x.m4b", nil)

	res := runPathLink(t, New(&fakeDeps{store: f.store(t)}), `{"dry_run":true}`)
	got := outcomeOf(t, res, "fd")
	if got.Outcome != authorPathLinkSuspectThin {
		t.Fatalf("outcome %q, want %q (outcomes=%v)", got.Outcome, authorPathLinkSuspectThin, res.Outcomes)
	}
	// Both numbers are reported, so a reader of the dry run sees the divergence
	// and not only the verdict.
	if got.TargetBookCount != 110 || got.TargetRowBookCount != 0 {
		t.Fatalf("counts scalar=%d own=%d, want 110 and 0", got.TargetBookCount, got.TargetRowBookCount)
	}
}

// 🔴 AND THE BAR STILL HOLDS THE OTHER WAY ROUND. A row whose own count is
// healthy but whose scalar reach is one book is thin too: EITHER count being
// thin holds the row, because a row is only fat when both quantities agree.
func TestAuthorPathLink_ThinRowHoldsOnEitherCount(t *testing.T) {
	f := &pathLinkFixture{}
	f.author(1, "Thin Scalar")
	f.filler(1, 1)   // scalar reach: 1
	f.ownCount(1, 9) // the row's own count says it is fat
	f.book("ts", "/mnt/bigdata/books/audiobook-organizer/Thin Scalar/A Title/x.m4b", nil)

	res := runPathLink(t, New(&fakeDeps{store: f.store(t)}), `{"dry_run":true}`)
	if got := outcomeOf(t, res, "ts"); got.Outcome != authorPathLinkSuspectThin {
		t.Fatalf("outcome %q, want %q (outcomes=%v)", got.Outcome, authorPathLinkSuspectThin, res.Outcomes)
	}
}

// 🔴 AND A GENUINE AUTHOR STILL LINKS. Both counts agree that the row is fat,
// which is the only way a link is written -- the guard must not have turned the
// op into one that refuses everything.
func TestAuthorPathLink_GenuineAuthorStillLinks(t *testing.T) {
	f := &pathLinkFixture{}
	f.author(38701, "Ryk Brown")
	f.filler(38701, 101)
	f.ownCount(38701, 101)
	f.book("rb", "/mnt/bigdata/books/abooks/imported/Ryk Brown/05_Rise of the Corinari/x.mp3", nil)

	res := runPathLink(t, New(&fakeDeps{store: f.store(t)}), `{"dry_run":true}`)
	got := outcomeOf(t, res, "rb")
	if got.Outcome != authorPathLinkWouldLink || got.AuthorID != 38701 {
		t.Fatalf("outcome %q author %d, want %q and 38701 (outcomes=%v)",
			got.Outcome, got.AuthorID, authorPathLinkWouldLink, res.Outcomes)
	}
}

// 🔴 A PLACEHOLDER-NAMED TARGET ROW IS NEVER A LINK TARGET.
//
// authorname.IsPlaceholder has always been run on the DERIVED name in
// authorPathLinkCandidates; it was never run on the row that name RESOLVES to,
// so a row literally called "Unknown Author" could gain books. Before this fix
// the outcome here is "would_link".
func TestAuthorPathLink_PlaceholderTargetRefused(t *testing.T) {
	f := &pathLinkFixture{}
	f.author(1, authorname.Placeholder)
	f.filler(1, 40)
	f.ownCount(1, 40)
	// 🔴 THE SEGMENT IS DOUBLE-SPACED, AND THAT IS THE WHOLE POINT. The
	// candidate-side IsPlaceholder check is an EqualFold on the trimmed string,
	// so "Unknown  Author" is not the placeholder to it and the segment sails
	// through the gate. util.NormalizeAuthor collapses internal whitespace, so
	// the same segment then resolves squarely onto the placeholder ROW. Only a
	// check on the resolved row catches this; with the segment spelled exactly
	// "Unknown Author" the candidate check fires first and the test proves
	// nothing about the row-side bar.
	f.book("ph", "/mnt/bigdata/books/audiobook-organizer/Unknown  Author/A Title/x.m4b", nil)

	res := runPathLink(t, New(&fakeDeps{store: f.store(t)}), `{"dry_run":true}`)
	got := outcomeOf(t, res, "ph")
	if got.Outcome != authorPathLinkSuspectPlaceholder {
		t.Fatalf("outcome %q, want %q (outcomes=%v)", got.Outcome, authorPathLinkSuspectPlaceholder, res.Outcomes)
	}
	if res.Outcomes[authorPathLinkWouldLink] != 0 {
		t.Fatalf("would_link=%d, want 0 (outcomes=%v)", res.Outcomes[authorPathLinkWouldLink], res.Outcomes)
	}
}

// 🔴 THE SIX NON-PERSON ROWS THE PROD DRY RUN NAMED, one case each, with the
// row fat on BOTH counts so only the NAME can refuse it.
//
// "Star Trek" is in the table on purpose and is expected to LINK: two ordinary
// capitalised words are structurally indistinguishable from a person here, and
// the only thing that would catch it is a list of franchise titles, which
// misfires on real authors and fails silently years later. It stays an
// owner-review item rather than a heuristic, and this table is where that
// decision is recorded.
//
// "Sanderson, Brandon" is refused in prod by the thin bar (its own count is 0),
// not by its inverted shape. No inverted-name detector is added: with both
// counts fat, an inverted duplicate reads as a person and links. That is the
// documented behaviour, not an oversight -- collapsing the duplicate is
// author-merge's job, not this op's.
func TestAuthorPathLink_NonPersonTargetRows(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Freedom's Dawn", authorPathLinkSuspectNonPerson},          // possessive
		{"The Messenger", authorPathLinkSuspectNonPerson},           // leading article
		{"STAR TREK POWER KLINGON", authorPathLinkSuspectNonPerson}, // all-caps shout
		{"Various Authors", authorPathLinkSuspectNonPerson},         // collective
		{"Star Trek", authorPathLinkWouldLink},                      // NOT refused, by decision
		{"Sanderson, Brandon", authorPathLinkWouldLink},             // refused by the thin bar in prod
		{"Ryk Brown", authorPathLinkWouldLink},                      // control
		{"STEPHEN KING", authorPathLinkWouldLink},                   // two caps words is not a shout
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &pathLinkFixture{}
			f.author(1, tc.name)
			f.filler(1, 12)
			f.ownCount(1, 12)
			f.book("b", "/mnt/bigdata/books/audiobook-organizer/"+tc.name+"/A Title/x.m4b", nil)

			res := runPathLink(t, New(&fakeDeps{store: f.store(t)}), `{"dry_run":true}`)
			got := outcomeOf(t, res, "b")
			if got.Outcome != tc.want {
				t.Fatalf("%q: outcome %q, want %q (outcomes=%v)", tc.name, got.Outcome, tc.want, res.Outcomes)
			}
		})
	}
}

// 🔴 THE PREDICATE ITSELF, including what it must NOT hold. A bar that refuses
// a write has to fail towards letting a real author through.
func TestAuthorPathLinkNonPersonRow(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Freedom's Dawn", true},
		{"Freedom’s Dawn", true},
		{"The Messenger", true},
		{"A Wizard of Earthsea", true},
		{"An Unkindness of Ghosts", true},
		{"STAR TREK POWER KLINGON", true},
		{"Various Authors", true},
		{"Anonymous Contributor", true},
		{"Charles Dickens", false},
		{"Star Trek", false},
		{"Sanderson, Brandon", false},
		{"J. R. R. Tolkien", false},
		{"STEPHEN KING", false},
		{"Ursula K. Le Guin", false},
		{"O'Brien Smith", false},
		// 🔴 AN INITIAL IS NOT AN ARTICLE. "A. Merritt" is a real author, and a
		// leading-article test that strips the period before comparing holds
		// him -- the exact failure direction this predicate must not have.
		{"A. Merritt", false},
		{"A. A. Milne", false},
		{"A.A. Milne", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authorPathLinkNonPersonRow(tc.name); got != tc.want {
				t.Fatalf("authorPathLinkNonPersonRow(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
