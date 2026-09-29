// file: internal/plugins/maintenance/junk_author_fixer_review_test.go
// version: 1.6.0
// guid: 0f4f7d0e-5a2b-4d63-9a51-3c9b8e2f6a14
// last-edited: 2026-09-29

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Tests ported from the PR #3613 review (scratch cases S1-S9, R1-R2) plus
// the medium/low items. Each failed against the fixer as first opened.

// row returns the plan row of (author name, book id), or nil.
func (f *junkFixture) row(plan *repairs.PlanResult, author, bookID string) *repairs.Row {
	f.t.Helper()
	id, ok := f.authors[author]
	require.True(f.t, ok, "no author %q in the fixture", author)
	return rowByID(plan, junkAuthorRowID(id, bookID))
}

// noRowsFor fails when the plan holds any row for author.
func noRowsFor(t *testing.T, f *junkFixture, plan *repairs.PlanResult, author string) {
	t.Helper()
	id := f.authors[author]
	for _, r := range plan.Rows {
		require.NotEqual(t, id, mustAuthorOfRow(t, r.RowID), "%q must not be flagged: %s / %s", author, r.Reason, r.SkipReason)
	}
}

func (f *junkFixture) setProvider(bookID, title, author string) {
	f.t.Helper()
	for field, v := range map[string]string{"title": title, "author_name": author} {
		enc, err := metastate.Encode(v)
		require.NoError(f.t, err)
		require.NoError(f.t, f.s.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: bookID, Field: field, FetchedValue: enc}))
	}
}

// ---- H2: a narrator is never the real author ----

// S1/S1b: an artist tag naming only the book's narrator, who has no author
// row of their own, is not an answer -- whichever of the three narrator
// sources names them -- and the row is held, never unlinked. (A narrator-role
// credit's own row is not "an author row" for this book.)
func TestJunkAuthorFixer_NarratorOnlyTagIsHeld(t *testing.T) {
	cases := []struct {
		name string
		spec junkBookSpec
	}{
		{"book narrator string", junkBookSpec{narrator: "Michael Kramer, Kate Reading",
			tags: map[string]string{"artist": "MICHAEL KRAMER"}}},
		{"book_narrators row", junkBookSpec{bookNarrators: []string{"Michael Kramer"},
			tags: map[string]string{"artist": "Michael Kramer"}}},
		{"narrator-role credit", junkBookSpec{narratorCredit: "Michael Kramer",
			tags: map[string]string{"artist": "Michael Kramer"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newJunkFixture(t)
			spec := c.spec
			spec.title, spec.path, spec.author = "The Final Empire", "/lib/n/fe", "GraphicAudio"
			id := f.book(spec)
			plan := f.plan()
			r := f.row(plan, "GraphicAudio", id)
			require.NotNil(t, r)
			require.NotEqual(t, "Michael Kramer", r.Proposed["author"], "narrator proposed as author: %s", r.Reason)
			require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "held, not unlinked: %s", r.Reason)
			require.Contains(t, r.SkipReason, "narrator")
		})
	}
}

// #3616 review F2: the album_artist tag naming the book's narrator is no
// more evidence than the artist tag -- rips put the reader in either. The row
// is held unless the name resolves to an author row with books of its own or
// the provider names them. (Round 2 accepted album_artist alone at review
// risk; that created an author for the reader, or reopened L1.)
func TestJunkAuthorFixer_NarratorByAlbumArtistIsHeld(t *testing.T) {
	f := newJunkFixture(t)
	id := f.book(junkBookSpec{title: "The Final Empire", path: "/lib/n/fe", author: "GraphicAudio", narrator: "Michael Kramer",
		tags: map[string]string{"album_artist": "Michael Kramer"}})
	r := f.row(f.plan(), "GraphicAudio", id)
	require.NotNil(t, r)
	require.NotEqual(t, "Michael Kramer", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "%v %s", r.Proposed, r.Reason)
}

// F2 A1: album_artist and artist both name the narrator, and no author row
// exists: held, and no author is created for the reader.
func TestJunkAuthorFixer_NarratorByAlbumArtistNeverCreates(t *testing.T) {
	f := newJunkFixture(t)
	id := f.book(junkBookSpec{title: "The Final Empire", path: "/lib/a/fe", author: "GraphicAudio", narrator: "Michael Kramer",
		tags: map[string]string{"album_artist": "Michael Kramer", "artist": "Michael Kramer"}})
	plan := f.plan()
	r := f.row(plan, "GraphicAudio", id)
	require.NotNil(t, r)
	require.NotEqual(t, junkAuthorDecCreate, r.Proposed["decision"], r.Reason)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "%v %s", r.Proposed, r.Reason)
	kramer, err := f.s.GetAuthorByName("Michael Kramer")
	require.NoError(t, err)
	require.Nil(t, kramer, "planning made an author row for the narrator")
}

// F2 A2: album_artist names the narrator, who has an author row with no
// books: held (L1 through the album-artist leg).
func TestJunkAuthorFixer_NarratorByAlbumArtistEmptyRowIsHeld(t *testing.T) {
	f := newJunkFixture(t)
	f.author("Michael Kramer")
	id := f.book(junkBookSpec{title: "The Final Empire", path: "/lib/a/fe", author: "GraphicAudio", bookNarrators: []string{"Michael Kramer"},
		tags: map[string]string{"album_artist": "Michael Kramer"}})
	r := f.row(f.plan(), "GraphicAudio", id)
	require.NotNil(t, r)
	require.NotEqual(t, "Michael Kramer", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "%v %s", r.Proposed, r.Reason)
}

// F2: the provider naming the narrator as the author is still an answer
// (authors read their own books), at review risk.
func TestJunkAuthorFixer_NarratorByProviderIsReview(t *testing.T) {
	f := newJunkFixture(t)
	id := f.book(junkBookSpec{title: "Bag of Bones", path: "/lib/n/bob", author: "GraphicAudio", narrator: "Stephen King"})
	f.setProvider(id, "Bag of Bones", "Stephen King")
	r := f.row(f.plan(), "GraphicAudio", id)
	require.NotNil(t, r)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Equal(t, "Stephen King", r.Proposed["author"], r.Reason)
	require.Equal(t, repairs.RiskReview, r.Risk)
	require.Contains(t, r.Reason, "narrator")
}

// N1: self-narrated books. The narrator is an existing author with books of
// his own: relinked, at review risk, never needs_manual.
func TestJunkAuthorFixer_SelfNarratedIsRelinked(t *testing.T) {
	f := newJunkFixture(t)
	king := f.author("Stephen King")
	f.book(junkBookSpec{title: "It", path: "/lib/Stephen King/It", author: "Stephen King"})
	a := f.book(junkBookSpec{title: "Bag of Bones", path: "/lib/n/bob", author: "read by Stephen King", narrator: "Stephen King",
		tags: map[string]string{"artist": "Stephen King", "album_artist": "Stephen King"}})
	b := f.book(junkBookSpec{title: "Bag of Bones", path: "/lib/n/bob2", author: "GraphicAudio", bookNarrators: []string{"Stephen King"},
		tags: map[string]string{"artist": "Stephen King"}})
	plan := f.plan()
	for _, c := range []struct{ author, book string }{{"read by Stephen King", a}, {"GraphicAudio", b}} {
		r := f.row(plan, c.author, c.book)
		require.NotNil(t, r, c.author)
		require.Empty(t, r.Skipped, "%s: %s", c.author, r.SkipReason)
		require.Equal(t, junkAuthorDecRelink, r.Proposed["decision"], "%s: %s", c.author, r.Reason)
		require.Equal(t, "Stephen King", r.Proposed["author"])
		require.Equal(t, repairs.RiskReview, r.Risk, "a narrator answer is review")
	}
	res := f.apply(plan, nil)
	require.Equal(t, 2, res.Applied, "by outcome: %v", res.ByOutcome)
	require.Equal(t, []int{king}, f.credits(a))
	require.Equal(t, []int{king}, f.credits(b))
}

// N2: a self-narrated book never falls through to an unrelated sibling.
func TestJunkAuthorFixer_SelfNarratedNotRelinkedToSibling(t *testing.T) {
	f := newJunkFixture(t)
	f.book(junkBookSpec{title: "American Gods", path: "/lib/Neil Gaiman/American Gods", author: "Neil Gaiman"})
	gb := f.book(junkBookSpec{title: "The Graveyard Book", path: "/lib/flat/gb.m4b", author: "GraphicAudio", narrator: "Neil Gaiman",
		tags: map[string]string{"artist": "Neil Gaiman"}})
	f.book(junkBookSpec{title: "Unrelated", path: "/lib/flat/u.m4b", author: "Tim Dorsey"})
	r := f.row(f.plan(), "GraphicAudio", gb)
	require.NotNil(t, r)
	require.Equal(t, "Neil Gaiman", r.Proposed["author"], r.Reason)
	require.Equal(t, repairs.RiskReview, r.Risk)
}

// Follow-up L1: an existing author row with no books of its own is not
// evidence that the narrator writes. Prod has 65 narrator names that fold to
// such an empty row; an artist tag naming one holds the row.
func TestJunkAuthorFixer_NarratorWithEmptyAuthorRowIsHeld(t *testing.T) {
	f := newJunkFixture(t)
	f.author("Michael Kramer") // an author row, credited on nothing
	id := f.book(junkBookSpec{title: "The Final Empire", path: "/lib/n/fe", author: "GraphicAudio", narrator: "Michael Kramer",
		tags: map[string]string{"artist": "Michael Kramer"}})
	r := f.row(f.plan(), "GraphicAudio", id)
	require.NotNil(t, r)
	require.NotEqual(t, "Michael Kramer", r.Proposed["author"], "narrator relinked to an empty author row: %s", r.Reason)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "held: %v %s", r.Proposed, r.Reason)
}

// A narrator-only tag answer that was dropped holds the row: no sibling, no
// path, no unlink.
func TestJunkAuthorFixer_DroppedNarratorAnswerHoldsTheRow(t *testing.T) {
	f := newJunkFixture(t)
	withSib := f.book(junkBookSpec{title: "Some Book", path: "/lib/flat/sb.m4b", author: "GraphicAudio", narrator: "Mark Boyett",
		tags: map[string]string{"artist": "Mark Boyett"}})
	f.book(junkBookSpec{title: "Unrelated", path: "/lib/flat/u.m4b", author: "Tim Dorsey"})
	byPath := f.book(junkBookSpec{title: "Other Book", path: "/lib/Tim Dorsey/ob", author: "GraphicAudio", narrator: "Mark Boyett",
		tags: map[string]string{"artist": "Mark Boyett"}})
	bare := f.book(junkBookSpec{title: "Third Book", path: "/lib/n/tb", author: "GraphicAudio", narrator: "Mark Boyett",
		tags: map[string]string{"artist": "Mark Boyett"}})
	plan := f.plan()
	for _, id := range []string{withSib, byPath, bare} {
		r := f.row(plan, "GraphicAudio", id)
		require.NotNil(t, r)
		require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "%s: %v %s", id, r.Proposed, r.Reason)
	}
}

// S1 as the reviewer wrote it// S1 as the reviewer wrote it: nothing says the tag's name is a narrator, so
// the answer stands, but an artist tag alone is review risk. A provider or a
// path that agrees makes it low.
func TestJunkAuthorFixer_TagOnlyAnswerIsReview(t *testing.T) {
	f := newJunkFixture(t)
	f.author("Michael Kramer")
	f.author("Brent Weeks")
	tagOnly := f.book(junkBookSpec{title: "The Final Empire", path: "/lib/n/fe", author: "GraphicAudio",
		tags: map[string]string{"artist": "Michael Kramer"}})
	byPath := f.book(junkBookSpec{title: "The Black Prism", path: "/lib/Brent Weeks/tbp", author: "GraphicAudio",
		tags: map[string]string{"artist": "Brent Weeks"}})
	byProv := f.book(junkBookSpec{title: "The Way of Shadows", path: "/lib/n/tws", author: "GraphicAudio",
		tags: map[string]string{"artist": "Brent Weeks"}})
	f.setProvider(byProv, "The Way of Shadows", "Brent Weeks")
	plan := f.plan()

	r := f.row(plan, "GraphicAudio", tagOnly)
	require.Equal(t, "Michael Kramer", r.Proposed["author"], r.Reason)
	require.Equal(t, repairs.RiskReview, r.Risk, "an artist tag alone is review: %s", r.Reason)
	for _, id := range []string{byPath, byProv} {
		r = f.row(plan, "GraphicAudio", id)
		require.Equal(t, "Brent Weeks", r.Proposed["author"], r.Reason)
		require.Equal(t, junkAuthorSrcTags, r.Proposed["source"])
		require.Equal(t, repairs.RiskLow, r.Risk, "corroborated: %s", r.Reason)
	}
}

// ---- H4: weak verdicts ----

// S2: a real author whose name is another author's series; the artist tag on
// her book names her narrator. The narrator is not "someone else".
func TestJunkAuthorFixer_WeakNotConfirmedByNarrator(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Kristen Painter", "Hollis McCarthy")
	f.book(junkBookSpec{title: "Swapped", path: "/lib/s/sw", author: "Hollis McCarthy", series: "Kristen Painter"})
	f.book(junkBookSpec{title: "Blood Rights", path: "/lib/k/b1", author: "Kristen Painter", narrator: "Hollis McCarthy & Kate Rudd",
		tags: map[string]string{"artist": "Hollis McCarthy"}})
	f.book(junkBookSpec{title: "Flesh and Blood", path: "/lib/k/b2", author: "Kristen Painter"})
	noRowsFor(t, f, f.plan(), "Kristen Painter")
}

// S3: a punctuation variant of the name itself names the row, so the weak row
// is not confirmed and no "Bryce O'Connor" duplicate is proposed.
func TestJunkAuthorFixer_WeakSelfVariantIsSelf(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Bryce OConnor", "Somebody Else")
	f.book(junkBookSpec{title: "Swapped", path: "/lib/s/sw", author: "Somebody Else", series: "Bryce OConnor"})
	f.book(junkBookSpec{title: "The Warrior's Path", path: "/lib/k/b1", author: "Bryce OConnor",
		tags: map[string]string{"artist": "Bryce O'Connor"}})
	noRowsFor(t, f, f.plan(), "Bryce OConnor")
}

// S9: siblings and paths never confirm a weak row: a flat folder shared with
// another author's book, or a folder named for someone else, is not evidence
// that the row is not a person.
func TestJunkAuthorFixer_WeakNotConfirmedBySiblingsOrPath(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Rick Gualtieri", "Other Person")
	f.book(junkBookSpec{title: "Swapped", path: "/lib/s/sw", author: "Other Person", series: "Rick Gualtieri"})
	f.book(junkBookSpec{title: "Bill the Vampire", path: "/lib/shared/a.m4b", author: "Rick Gualtieri"})
	f.book(junkBookSpec{title: "Unrelated", path: "/lib/shared/b.m4b", author: "Tim Dorsey"})
	f.book(junkBookSpec{title: "Scary Dead Things", path: "/lib/Tim Dorsey/sdt", author: "Rick Gualtieri"})
	noRowsFor(t, f, f.plan(), "Rick Gualtieri")
}

// A confirmed weak row's book with no evidence of its own keeps its credit:
// a weak verdict never leaves a book with no author.
func TestJunkAuthorFixer_WeakNeverUnlinks(t *testing.T) {
	f := newJunkFixture(t)
	f.author("J. K. Rowling")
	f.mkSeries("Harry Potter", "J. K. Rowling")
	f.book(junkBookSpec{title: "Philosopher's Stone", path: "/lib/r/ps", author: "J. K. Rowling", series: "Harry Potter"})
	tagged := f.book(junkBookSpec{title: "Chamber of Secrets", path: "/lib/h/cs", author: "Harry Potter",
		tags: map[string]string{"artist": "J. K. Rowling"}})
	bare := f.book(junkBookSpec{title: "Prisoner of Azkaban", path: "/lib/h/pa", author: "Harry Potter"})
	plan := f.plan()
	r := f.row(plan, "Harry Potter", tagged)
	require.NotNil(t, r, "confirmed by the tag")
	require.Equal(t, "J. K. Rowling", r.Proposed["author"])
	r = f.row(plan, "Harry Potter", bare)
	require.NotNil(t, r)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "weak row unlinked: %s", r.Reason)
}

// ---- H5: a narrator credit is never promoted to primary ----

// S4: removing the junk primary leaves only a narrator credit: the primary is
// nil. A real co-author left in the junction is promoted.
func TestJunkAuthorFixer_UnlinkNeverPromotesNarrator(t *testing.T) {
	f := newJunkFixture(t)
	onlyNarr := f.book(junkBookSpec{title: "Some Title", path: "/lib/u/b1", author: "GraphicAudio", narratorCredit: "Mark Boyett"})
	withCo := f.book(junkBookSpec{title: "Other Title", path: "/lib/u/b2", author: "GraphicAudio",
		coAuthor: "Jacqueline Simpson", narratorCredit: "Mark Boyett"})
	plan := f.plan()
	for _, id := range []string{onlyNarr, withCo} {
		require.Equal(t, junkAuthorDecUnlink, f.row(plan, "GraphicAudio", id).Proposed["decision"])
	}
	res := f.apply(plan, nil)
	require.Equal(t, 2, res.Applied, "by outcome: %v", res.ByOutcome)
	require.Nil(t, f.primary(onlyNarr), "a narrator is not a primary author")
	require.Equal(t, []int{f.authors["Mark Boyett"]}, f.credits(onlyNarr))
	require.Equal(t, f.authors["Jacqueline Simpson"], *f.primary(withCo))

	rev, err := audiobooks.NewRevertService(f.s).RevertOperation(junkTestOpID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	require.Equal(t, f.authors["GraphicAudio"], *f.primary(onlyNarr))
	require.Equal(t, f.authors["GraphicAudio"], *f.primary(withCo))
}

func TestMoveCredit_NarratorRoleIsNotCarried(t *testing.T) {
	cur := []database.BookAuthor{{BookID: "b", AuthorID: 7, Role: "narrator", Position: 0}, {BookID: "b", AuthorID: 8, Role: "author", Position: 1}}
	got := moveCredit(cur, "b", 7, &database.Author{ID: 9, Name: "Real"})
	require.Equal(t, []database.BookAuthor{{BookID: "b", AuthorID: 9, Role: "author", Position: 0}, {BookID: "b", AuthorID: 8, Role: "author", Position: 1}}, got)
	require.Equal(t, "narrator", cur[0].Role, "input not mutated")
}

// ---- M1: one folded author key ----

// S7 and friends: a case, punctuation or diacritics variant resolves to the
// existing row (never a new one), and the row says the spelling differs.
func TestJunkAuthorFixer_FoldedNameResolvesToExistingRow(t *testing.T) {
	f := newJunkFixture(t)
	pairs := map[string]string{
		"Brandon Sanderson":      "BRANDON SANDERSON",
		"Christopher G. Nuttall": "Christopher G Nuttall",
		"Emma Törzs":             "Emma Torzs",
		"J. N. Chaney":           "J.N. Chaney",
	}
	books := map[string]string{}
	for real, tag := range pairs {
		f.author(real)
		books[real] = f.book(junkBookSpec{title: "Book " + real, path: "/lib/m/" + tag, author: "GraphicAudio",
			tags: map[string]string{"artist": tag}})
	}
	before, err := f.s.GetAllAuthors()
	require.NoError(t, err)
	plan := f.plan()
	for real, id := range books {
		r := f.row(plan, "GraphicAudio", id)
		require.NotNil(t, r)
		require.Equal(t, junkAuthorDecRelink, r.Proposed["decision"], "%s: %s", real, r.Reason)
		require.Equal(t, real, r.Proposed["author"], r.Reason)
		require.Equal(t, repairs.RiskReview, r.Risk)
		require.Contains(t, r.Reason, "spelled", r.Reason)
	}
	res := f.apply(plan, nil)
	require.Equal(t, len(books), res.Applied, "by outcome: %v", res.ByOutcome)
	for real, id := range books {
		require.Equal(t, []int{f.authors[real]}, f.credits(id), real)
	}
	after, err := f.s.GetAllAuthors()
	require.NoError(t, err)
	require.Len(t, after, len(before), "no author row minted")
}

// Apply never moves a book onto the junk row itself, whatever the decision
// says (the store's name lookup is case-insensitive).
func TestJunkAuthorFixer_ApplyRefusesTheJunkRowAsTarget(t *testing.T) {
	f := newJunkFixture(t)
	id := f.book(junkBookSpec{title: "Nothing Known", path: "/lib/j/nk", author: "GraphicAudio"})
	plan := f.plan()
	r := f.row(plan, "GraphicAudio", id)
	fresh, err := f.fixer.Replan(context.Background(), nil, *r, &fakeReporter{})
	require.NoError(t, err)
	d := fresh.Detail.(*junkAuthorBookDecision)
	d.Decision, d.Target = junkAuthorDecCreate, database.Author{Name: "graphicaudio"}
	w := repairs.NewWriter(f.s, f.s, f.fixer.ID(), "bulk_update", "repairs-").WithJournal(f.s, f.s, junkTestOpID).WithCredits(f.s)
	require.Error(t, f.fixer.Apply(context.Background(), w, fresh))
	require.Equal(t, []int{f.authors["GraphicAudio"]}, f.credits(id))
}

// ---- M2: the junk name itself names the real author ----

// The cleaned name only RESOLVES to an existing author row that has books;
// it never creates one, and never an author named like the book's title.
func TestJunkAuthorFixer_CleanedSelfName(t *testing.T) {
	f := newJunkFixture(t)
	f.book(junkBookSpec{title: "Childhood's End", path: "/lib/c/ce", author: "Arthur C. Clarke"})
	f.book(junkBookSpec{title: "Homeland", path: "/lib/c/hl", author: "R. A. Salvatore"})
	f.author("Brent Weeks") // a row with no books
	dash := f.book(junkBookSpec{title: "Rendezvous with Rama", path: "/lib/c/rr", author: "- Arthur C. Clarke"})
	studio := f.book(junkBookSpec{title: "The Crystal Shard", path: "/lib/c/cs", author: "GraphicAudio [R. A. Salvatore]"})
	noBooks := f.book(junkBookSpec{title: "The Black Prism", path: "/lib/c/bp", author: "+Brent Weeks"})
	noRow := f.book(junkBookSpec{title: "The Sword of Shannara", path: "/lib/c/ss", author: "+Terry Brooks"})
	plan := f.plan()

	for _, c := range []struct{ author, book, target string }{
		{"- Arthur C. Clarke", dash, "Arthur C. Clarke"},
		{"GraphicAudio [R. A. Salvatore]", studio, "R. A. Salvatore"},
	} {
		r := f.row(plan, c.author, c.book)
		require.NotNil(t, r, c.author)
		require.Empty(t, r.Skipped, r.SkipReason)
		require.Equal(t, junkAuthorDecRelink, r.Proposed["decision"], "%s: %s", c.author, r.Reason)
		require.Equal(t, c.target, r.Proposed["author"], r.Reason)
		require.Equal(t, junkAuthorSrcCleaned, r.Proposed["source"])
	}
	for _, c := range []struct{ author, book string }{{"+Brent Weeks", noBooks}, {"+Terry Brooks", noRow}} {
		r := f.row(plan, c.author, c.book)
		require.NotNil(t, r, c.author)
		require.NotEqual(t, junkAuthorSrcCleaned, r.Proposed["source"], "%s: %s", c.author, r.Reason)
		require.NotEqual(t, junkAuthorDecCreate, r.Proposed["decision"], "%s: %s", c.author, r.Reason)
	}
	res := f.apply(plan, []string{f.row(plan, "- Arthur C. Clarke", dash).RowID, f.row(plan, "GraphicAudio [R. A. Salvatore]", studio).RowID})
	require.Equal(t, 2, res.Applied, "by outcome: %v", res.ByOutcome)
	require.Equal(t, []int{f.authors["Arthur C. Clarke"]}, f.credits(dash))
	brooks, err := f.s.GetAuthorByName("Terry Brooks")
	require.NoError(t, err)
	require.Nil(t, brooks, "never minted from a cleaned name")
}

// C1 (review 2): titles and chapter labels credited as authors are never
// turned into author rows; and a cleaned name that is the book's title (or
// its head) is refused even when a row of that name has books.
func TestJunkAuthorFixer_CleanedNameIsNeverATitle(t *testing.T) {
	f := newJunkFixture(t)
	f.book(junkBookSpec{title: "Heart of Rust", path: "/lib/c/hor", author: "HOR_ Prologue"})
	f.book(junkBookSpec{title: "Killing Titan (Unabridged)", path: "/lib/c/kt", author: "Killing Titan 01-44"})
	f.book(junkBookSpec{title: "The Colour of Magic: Discworld 1", path: "/lib/c/com", author: "The Colour Of Magic 01"})
	f.book(junkBookSpec{title: "Glow Red: A LitRPG", path: "/lib/c/gr", author: "Glow Red_"})
	f.book(junkBookSpec{title: "Country Mage 2", path: "/lib/c/cm", author: "Country Mage_"})
	f.book(junkBookSpec{title: "Stephen Hawking: A Life", path: "/lib/c/sh0", author: "Stephen Hawking"})
	hawking := f.book(junkBookSpec{title: "Stephen Hawking, His Life and Work", path: "/lib/c/sh", author: "(c) 2001 Stephen Hawking"})
	before, err := f.s.GetAllAuthors()
	require.NoError(t, err)
	plan := f.plan()
	for _, r := range plan.Rows {
		require.NotEqual(t, junkAuthorSrcCleaned, r.Proposed["source"], "%s: %s", r.RowID, r.Reason)
		require.NotEqual(t, junkAuthorDecCreate, r.Proposed["decision"], "%s: %s", r.RowID, r.Reason)
	}
	require.NotNil(t, f.row(plan, "(c) 2001 Stephen Hawking", hawking))
	f.apply(plan, nil)
	after, err := f.s.GetAllAuthors()
	require.NoError(t, err)
	require.Len(t, after, len(before), "no author row minted")
}

// ---- M3 / M4 ----

func TestJunkAuthorFixer_CollectiveCreditsAreNeverUnlinked(t *testing.T) {
	f := newJunkFixture(t)
	var ids []string
	for _, name := range []string{"Various Authors", "Anonymous", "Anthology Editor"} {
		ids = append(ids, f.book(junkBookSpec{title: "Tales " + name, path: "/lib/v/" + name, author: name}))
	}
	plan := f.plan()
	for i, name := range []string{"Various Authors", "Anonymous", "Anthology Editor"} {
		r := f.row(plan, name, ids[i])
		require.NotNil(t, r, name)
		require.NotEqual(t, junkAuthorDecUnlink, r.Proposed["decision"], "%s unlinked: %s", name, r.Reason)
		require.NotEmpty(t, r.Skipped, name)
	}
}

func TestJunkAuthorFixer_UnderscoreCoAuthorsGoToSplit(t *testing.T) {
	f := newJunkFixture(t)
	f.author("Terry Pratchett")
	id := f.book(junkBookSpec{title: "The Folklore of Discworld", path: "/lib/p/fd", author: "Terry Pratchett_ Jacqueline Simpson",
		tags: map[string]string{"artist": "Terry Pratchett"}})
	r := f.row(f.plan(), "Terry Pratchett_ Jacqueline Simpson", id)
	require.NotNil(t, r)
	require.Equal(t, junkAuthorSkipComposite, r.Skipped, "relinked to one co-author: %s", r.Reason)
	require.Contains(t, r.SkipReason, "author-split")
}

// ---- L1: the created author is journaled by id ----

func TestJunkAuthorFixer_CreateJournalsTheAuthorID(t *testing.T) {
	f := newJunkFixture(t)
	id := f.book(junkBookSpec{title: "Assassin's Apprentice", path: "/lib/bk-create", author: "GraphicAudio",
		tags: map[string]string{"artist": "Robin Hobb"}})
	res := f.apply(f.plan(), nil)
	require.Equal(t, 1, res.Applied, "by outcome: %v", res.ByOutcome)
	hobb, err := f.s.GetAuthorByName("Robin Hobb")
	require.NoError(t, err)
	require.NotNil(t, hobb)
	require.Equal(t, []int{hobb.ID}, f.credits(id))
	changes, err := f.s.GetOperationChanges(junkTestOpID)
	require.NoError(t, err)
	var created []undo.JunkAuthorCreate
	for _, c := range changes {
		require.NotEqual(t, undo.ChangeTypeTitleRelinkAuthorCreate, c.ChangeType)
		if c.ChangeType == undo.ChangeTypeJunkAuthorCreate {
			var jc undo.JunkAuthorCreate
			require.NoError(t, json.Unmarshal([]byte(c.NewValue), &jc))
			created = append(created, jc)
		}
	}
	require.Equal(t, []undo.JunkAuthorCreate{{AuthorID: hobb.ID, Name: "Robin Hobb"}}, created)
}

// ---- H3: the owner-manual guard reads the title ----

// S8: a Doctor Who title on a neutral path, no series, credited to a studio.
func TestJunkAuthorFixer_OwnerManualByTitle(t *testing.T) {
	f := newJunkFixture(t)
	f.author("Gary Russell")
	id := f.book(junkBookSpec{title: "Doctor Who: Placebo Effect", path: "/lib/bbc/pe", author: "BBC Audiobooks",
		tags: map[string]string{"artist": "Gary Russell"}})
	r := f.row(f.plan(), "BBC Audiobooks", id)
	require.NotNil(t, r)
	require.Equal(t, repairs.SkipOwnerManual, r.Skipped, r.Reason)
}

// ---- H1: the op revert never undoes a later change ----

// R1: unlink, then the owner sets the real author, then the op revert: the
// owner's fix stands (credits and primary), nothing is half-reverted.
func TestJunkAuthorFixer_RevertKeepsAManualFix(t *testing.T) {
	f := newJunkFixture(t)
	id := f.book(junkBookSpec{title: "Some Title", path: "/lib/u/b1", author: "GraphicAudio"})
	res := f.apply(f.plan(), nil)
	require.Equal(t, 1, res.Applied)
	real := f.author("Real Person")
	require.NoError(t, f.s.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: real, Role: "author"}}))
	_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.AuthorID = &real; return nil })
	require.NoError(t, err)

	_, _ = audiobooks.NewRevertService(f.s).RevertOperation(junkTestOpID)
	require.Equal(t, []int{real}, f.credits(id), "the owner's credit is kept")
	require.Equal(t, real, *f.primary(id), "the owner's primary is kept")
}

// R2: relink, then a co-author is added, then the op revert: refused, the
// co-author is kept.
func TestJunkAuthorFixer_RevertKeepsALaterCoAuthor(t *testing.T) {
	f := newJunkFixture(t)
	brent := f.author("Brent Weeks")
	id := f.book(junkBookSpec{title: "The Way of Shadows", path: "/lib/r/b1", author: "GraphicAudio",
		tags: map[string]string{"artist": "Brent Weeks"}})
	res := f.apply(f.plan(), nil)
	require.Equal(t, 1, res.Applied)
	co := f.author("Added Coauthor")
	cs, err := f.s.GetBookAuthors(id)
	require.NoError(t, err)
	cs = append(cs, database.BookAuthor{BookID: id, AuthorID: co, Role: "author", Position: len(cs)})
	require.NoError(t, f.s.SetBookAuthors(id, cs))

	rev, _ := audiobooks.NewRevertService(f.s).RevertOperation(junkTestOpID)
	require.Zero(t, rev.Restored, "a revert over a later change is refused: %+v", rev)
	require.Equal(t, []int{brent, co}, f.credits(id), "the later co-author is kept")
	require.Equal(t, brent, *f.primary(id))
}

// ---- classes the creation gate refuses run end to end ----

func TestJunkAuthorFixer_GateRefusedNamesPlanApplyRevert(t *testing.T) {
	f := newJunkFixture(t)
	brent := f.author("Brent Weeks")
	books := map[string]string{}
	for _, name := range []string{"read by narrator", "Book 1 (Unabridged)"} {
		books[name] = f.book(junkBookSpec{title: "Title for " + name, path: "/lib/Brent Weeks/" + name, author: name,
			tags: map[string]string{"artist": "Brent Weeks"}})
	}
	plan := f.plan()
	for name, id := range books {
		r := f.row(plan, name, id)
		require.NotNil(t, r, name)
		require.Equal(t, "Brent Weeks", r.Proposed["author"], r.Reason)
	}
	res := f.apply(plan, nil)
	require.Equal(t, len(books), res.Applied, "by outcome: %v", res.ByOutcome)
	for _, id := range books {
		require.Equal(t, []int{brent}, f.credits(id))
	}
	rev, err := audiobooks.NewRevertService(f.s).RevertOperation(junkTestOpID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	for name, id := range books {
		require.Equal(t, []int{f.authors[name]}, f.credits(id), name)
		require.Equal(t, f.authors[name], *f.primary(id), name)
	}
}

// raceStore hides one author from the name lookup, as if another writer made
// it between Apply's pre-check and its create.
type raceStore struct {
	database.Store
	hide string
}

func (r raceStore) GetAuthorByName(name string) (*database.Author, error) {
	if name == r.hide {
		return nil, nil
	}
	return r.Store.GetAuthorByName(name)
}

// N-L1 (review 2): a create that finds the row already there (resolve, not
// mint) journals no create, so the op revert never deletes a row this op did
// not make.
func TestJunkAuthorFixer_CreateJournalsOnlyARowItMinted(t *testing.T) {
	f := newJunkFixture(t)
	bk := f.book(junkBookSpec{title: "Assassin's Apprentice", path: "/lib/bk-create", author: "GraphicAudio",
		tags: map[string]string{"artist": "Robin Hobb"}})
	plan := f.plan()
	id := junkAuthorRowID(f.authors["GraphicAudio"], bk)
	fresh, err := f.fixer.Replan(context.Background(), nil, *rowByID(plan, id), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, junkAuthorDecCreate, fresh.Proposed["decision"])

	hobb, err := f.s.CreateAuthor("Robin Hobb") // made by someone else
	require.NoError(t, err)
	f.fixer.p = &Plugin{deps: &fakeDeps{store: raceStore{Store: f.s, hide: "Robin Hobb"}}}
	w := repairs.NewWriter(f.s, f.s, f.fixer.ID(), "bulk_update", "repairs-").WithJournal(f.s, f.s, junkTestOpID).WithCredits(f.s)
	require.NoError(t, f.fixer.Apply(context.Background(), w, fresh))
	require.Equal(t, []int{hobb.ID}, f.credits(bk))

	changes, err := f.s.GetOperationChanges(junkTestOpID)
	require.NoError(t, err)
	for _, c := range changes {
		require.NotEqual(t, undo.ChangeTypeJunkAuthorCreate, c.ChangeType, "journaled a create for a row it did not mint")
	}
	_, err = audiobooks.NewRevertService(f.s).RevertOperation(junkTestOpID)
	require.NoError(t, err)
	got, err := f.s.GetAuthorByID(hobb.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "the revert deleted a row this op did not make")
}

// The primary-author history batch of a junk-author apply is marked
// apply_op_journaled (#3612's marker): its credit junction lives in the op
// journal, so undo-last-apply refuses the batch instead of putting the
// primary back alone.
func TestJunkAuthorFixer_PrimaryBatchIsMarkedOpJournaled(t *testing.T) {
	f := newJunkFixture(t)
	f.author("Brent Weeks")
	id := f.book(junkBookSpec{title: "The Way of Shadows", path: "/lib/r/b1", author: "GraphicAudio",
		tags: map[string]string{"artist": "Brent Weeks"}})
	res := f.apply(f.plan(), nil)
	require.Equal(t, 1, res.Applied, "by outcome: %v", res.ByOutcome)
	marks, err := f.s.GetMetadataChangeHistory(id, "apply", 10)
	require.NoError(t, err)
	var kinds []string
	for _, m := range marks {
		kinds = append(kinds, m.ChangeType)
	}
	require.Equal(t, []string{repairs.ChangeTypeApplyOpJournaled}, kinds)
}

// creditOnJournal fails the op journal's create-author entry, and just before
// failing it credits the new author to another book: a second worker whose
// MintAuthor resolved the same name and linked it.
type creditOnJournal struct {
	database.Store
	other string
}

func (c creditOnJournal) CreateOperationChange(ch *database.OperationChange) error {
	if ch.ChangeType != undo.ChangeTypeJunkAuthorCreate {
		return c.Store.CreateOperationChange(ch)
	}
	var jc undo.JunkAuthorCreate
	if err := json.Unmarshal([]byte(ch.NewValue), &jc); err != nil {
		return err
	}
	if err := c.SetBookAuthors(c.other, []database.BookAuthor{{BookID: c.other, AuthorID: jc.AuthorID, Role: "author"}}); err != nil {
		return err
	}
	return errors.New("journal down")
}

// Follow-up L2: the rollback of a create whose journal entry failed never
// deletes a row that someone else credited in the meantime.
func TestJunkAuthorFixer_CreateRollbackKeepsACreditedRow(t *testing.T) {
	f := newJunkFixture(t)
	bk := f.book(junkBookSpec{title: "Assassin's Apprentice", path: "/lib/bk-create", author: "GraphicAudio",
		tags: map[string]string{"artist": "Robin Hobb"}})
	other := f.book(junkBookSpec{title: "Royal Assassin", path: "/lib/bk-other", author: "Somebody Else"})
	plan := f.plan()
	fresh, err := f.fixer.Replan(context.Background(), nil, *rowByID(plan, junkAuthorRowID(f.authors["GraphicAudio"], bk)), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, junkAuthorDecCreate, fresh.Proposed["decision"])

	j := creditOnJournal{Store: f.s, other: other}
	w := repairs.NewWriter(f.s, f.s, f.fixer.ID(), "bulk_update", "repairs-").WithJournal(f.s, j, junkTestOpID).WithCredits(f.s)
	require.Error(t, f.fixer.Apply(context.Background(), w, fresh))

	hobb, err := f.s.GetAuthorByName("Robin Hobb")
	require.NoError(t, err)
	require.NotNil(t, hobb, "rollback deleted an author another book credits")
	require.Equal(t, []int{hobb.ID}, f.credits(other))
	require.Equal(t, []int{f.authors["GraphicAudio"]}, f.credits(bk), "the failed row was not applied")
}

// Follow-up L2, the other direction: with nothing crediting it, the row is
// still taken back.
func TestJunkAuthorFixer_CreateRollbackRemovesAnUncreditedRow(t *testing.T) {
	f := newJunkFixture(t)
	bk := f.book(junkBookSpec{title: "Assassin's Apprentice", path: "/lib/bk-create", author: "GraphicAudio",
		tags: map[string]string{"artist": "Robin Hobb"}})
	plan := f.plan()
	fresh, err := f.fixer.Replan(context.Background(), nil, *rowByID(plan, junkAuthorRowID(f.authors["GraphicAudio"], bk)), &fakeReporter{})
	require.NoError(t, err)
	w := repairs.NewWriter(f.s, f.s, f.fixer.ID(), "bulk_update", "repairs-").WithJournal(f.s, failJournal{f.s}, junkTestOpID).WithCredits(f.s)
	require.Error(t, f.fixer.Apply(context.Background(), w, fresh))
	hobb, err := f.s.GetAuthorByName("Robin Hobb")
	require.NoError(t, err)
	require.Nil(t, hobb, "an unjournaled, uncredited row was left behind")
}

// failJournal fails only the create-author entry.
type failJournal struct{ database.Store }

func (j failJournal) CreateOperationChange(ch *database.OperationChange) error {
	if ch.ChangeType == undo.ChangeTypeJunkAuthorCreate {
		return errors.New("journal down")
	}
	return j.Store.CreateOperationChange(ch)
}

// ---- 2026-09-29 trial: targets that are junk themselves ----

// A person with a series in parentheses is junk when the library has that
// series, and resolves to the person; the verdict holds from Plan through
// Replan to Apply. A narrator parenthetical is left alone, and a junk-shaped
// name is never a relink target.
func TestJunkAuthorFixer_SeriesParentheticalResolvesToPerson(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Dragon Born", "Dante King")
	f.book(junkBookSpec{title: "Dragon Born 1", path: "/lib/dk/db1", author: "Dante King", series: "Dragon Born"})
	paren := f.book(junkBookSpec{title: "Shifter Hoard", path: "/lib/dk/db2", author: "Dante King (Dragon Born)"})
	f.book(junkBookSpec{title: "Hounded", path: "/lib/kh/h", author: "Kevin Hearne (Luke Daniels)"})
	// A junk credit whose file names a junk-shaped "author": no target.
	tagged := f.book(junkBookSpec{title: "Some Book", path: "/lib/x/sb", author: "Book 1 (Unabridged)",
		tags: map[string]string{"artist": "Dante King (Dragon Born)"}})
	plan := f.plan()

	r := f.row(plan, "Dante King (Dragon Born)", paren)
	require.NotNil(t, r)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Equal(t, "series_name", r.Current["class"], r.Reason)
	require.Equal(t, junkAuthorDecRelink, r.Proposed["decision"], r.Reason)
	require.Equal(t, "Dante King", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSrcCleaned, r.Proposed["source"])
	// The series is the parenthetical, not the whole junk name.
	require.Equal(t, "Dragon Born", r.Proposed["series"], r.Reason)
	noRowsFor(t, f, plan, "Kevin Hearne (Luke Daniels)")
	if tr := f.row(plan, "Book 1 (Unabridged)", tagged); tr != nil {
		require.NotEqual(t, strconv.Itoa(f.authors["Dante King (Dragon Born)"]), tr.Proposed["author_id"], tr.Reason)
		require.NotEqual(t, "Dante King (Dragon Born)", tr.Proposed["author"], tr.Reason)
	}

	res := f.apply(plan, []string{r.RowID})
	require.Equal(t, 1, res.Applied, "Replan must reach the plan's verdict: %v", res.ByOutcome)
	require.Equal(t, []int{f.authors["Dante King"]}, f.credits(paren))
	b, err := f.s.GetBookByID(paren)
	require.NoError(t, err)
	require.NotNil(t, b.SeriesID)
	require.Equal(t, f.series["Dragon Born"], *b.SeriesID)
}

// The rules added for the trial reach real credits ("The Dalai Lama", "50
// Cent"). With no evidence of another author the credit is kept (skipped as
// ambiguous, never unlinked); with evidence the book is still relinked.
func TestJunkAuthorFixer_RelinkOnlyRulesNeverUnlink(t *testing.T) {
	f := newJunkFixture(t)
	names := []string{"The Dalai Lama", "The Rock", "The Mayo Clinic", "The Washington Post", "The Three Initiates",
		"The Venerable Bede", "The Gawain Poet", "The Beatles", "The Rolling Stones", "The Weeknd", "The Edge",
		"The Prophet Enoch", "50 Cent", "Jackson 5", "Maroon 5", "Blink 182", "Matchbox 20", "abooks", "Star Wars"}
	books := map[string]string{}
	for i, n := range names {
		books[n] = f.book(junkBookSpec{title: "Own Book " + strconv.Itoa(i), path: "/lib/real/" + strconv.Itoa(i), author: n})
	}
	// A series named like a narrator flips a narrator parenthetical to junk;
	// with no evidence it is still never unlinked.
	f.mkSeries("Luke Daniels", "Someone Else")
	kh := f.book(junkBookSpec{title: "Hounded", path: "/lib/kh/h", author: "Kevin Hearne (Luke Daniels)"})
	// Evidence names a real author: relinked.
	f.book(junkBookSpec{title: "The Spook's Apprentice", path: "/lib/jd/sa", author: "Joseph Delaney"})
	tc := f.book(junkBookSpec{title: "The Spook's Curse", path: "/lib/tc/sc", author: "The Complete",
		tags: map[string]string{"artist": "Joseph Delaney"}})
	plan := f.plan()

	for _, n := range append(names, "Kevin Hearne (Luke Daniels)") {
		id := books[n]
		if n == "Kevin Hearne (Luke Daniels)" {
			id = kh
		}
		r := f.row(plan, n, id)
		require.NotNil(t, r, n)
		require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "%s: %s / %s", n, r.Reason, r.SkipReason)
		require.Contains(t, r.SkipReason, "the credit is kept", n)
	}
	r := f.row(plan, "The Complete", tc)
	require.NotNil(t, r)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Equal(t, junkAuthorDecRelink, r.Proposed["decision"], r.Reason)
	require.Equal(t, "Joseph Delaney", r.Proposed["author"], r.Reason)

	f.apply(plan, nil)
	for n, id := range books {
		require.Equal(t, []int{f.authors[n]}, f.credits(id), "%s keeps its credit", n)
	}
	require.Equal(t, []int{f.authors["Kevin Hearne (Luke Daniels)"]}, f.credits(kh))
	require.Equal(t, []int{f.authors["Joseph Delaney"]}, f.credits(tc))
}

// "The Thirteenth Doctor Adventures" is four capitalized words, so the
// person-shape test passed it and the trial minted it as an author for Big
// Finish books. It is neither minted nor applied: the name is junk, and the
// Doctor range is owner-manual. An article-led name is never minted.
func TestJunkAuthorFixer_NeverMintsTitleShapedNames(t *testing.T) {
	f := newJunkFixture(t)
	dw := f.book(junkBookSpec{title: "The Return of the Doctor", path: "/lib/ad/rd", author: "Book 1 (Unabridged)",
		tags: map[string]string{"artist": "The Thirteenth Doctor Adventures"}})
	tt := f.book(junkBookSpec{title: "Dyke 2288", path: "/lib/ad/tt", author: "Opening Credits",
		tags: map[string]string{"artist": "A Time Travel"}})
	series := f.book(junkBookSpec{title: "Lionesses in Winter", path: "/lib/ad/lw", author: "The Thirteenth Doctor Adventures - Series 1",
		tags: map[string]string{"artist": "Jacqueline Rayner"}})
	before, err := f.s.GetAllAuthors()
	require.NoError(t, err)
	plan := f.plan()
	for _, c := range []struct{ author, book string }{{"Book 1 (Unabridged)", dw}, {"Opening Credits", tt}} {
		r := f.row(plan, c.author, c.book)
		require.NotNil(t, r, c.author)
		require.NotEqual(t, junkAuthorDecCreate, r.Proposed["decision"], "%s: %s", c.author, r.Reason)
		require.NotEqual(t, junkAuthorDecRelink, r.Proposed["decision"], "%s: %s", c.author, r.Reason)
	}
	r := f.row(plan, "The Thirteenth Doctor Adventures - Series 1", series)
	require.NotNil(t, r)
	require.Equal(t, repairs.SkipOwnerManual, r.Skipped, r.Reason)
	f.apply(plan, nil)
	after, err := f.s.GetAllAuthors()
	require.NoError(t, err)
	require.Len(t, after, len(before), "no author row minted")
}
