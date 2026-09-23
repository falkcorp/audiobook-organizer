// file: internal/plugins/maintenance/narrator_split_joined_test.go
// version: 1.4.0
// guid: 9882c157-c994-478a-89fd-0568ba93a56c
// last-edited: 2026-09-23

package maintenance

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

type splitJoinedFixture struct {
	s                        *database.PebbleStore
	kate, joined1, joined2   *database.Narrator
	leGuin                   *database.Narrator
	book1, book2, book3, bk4 string
}

// book1 [Kate Reading, Michael Kramer] (joined), book2 [Kate Reading] + the
// same joined name, book3 [Dorrie Sacks & Jeff Hays] (joined) + Le Guin,
// bk4 [Le Guin, Ursula] only (a surname-first name, NOT joined). Every book
// is credited to one author who is none of the narrators: a book with no
// linked author is held, not split.
func newSplitJoinedFixture(t *testing.T) splitJoinedFixture {
	s := newSeriesPhantomStore(t)
	mk := func(name string) *database.Narrator {
		n, err := s.CreateNarrator(name)
		require.NoError(t, err)
		return n
	}
	author, err := s.CreateAuthor("Fixture Author")
	require.NoError(t, err)
	f := splitJoinedFixture{s: s}
	f.kate = mk("Kate Reading")
	f.joined1 = mk("Kate Reading, Michael Kramer")
	f.joined2 = mk("Dorrie Sacks & Jeff Hays")
	f.leGuin = mk("Le Guin, Ursula")
	book := func(title string, ns ...*database.Narrator) string {
		b, err := s.CreateBook(&database.Book{Title: title, FilePath: "/splitjoined/" + title})
		require.NoError(t, err)
		var rows []database.BookNarrator
		for i, n := range ns {
			rows = append(rows, database.BookNarrator{NarratorID: n.ID, Role: "narrator", Position: i})
		}
		require.NoError(t, s.SetBookNarrators(b.ID, rows))
		require.NoError(t, s.SetBookAuthors(b.ID, []database.BookAuthor{{AuthorID: author.ID, Role: "author"}}))
		return b.ID
	}
	f.book1 = book("one", f.joined1)
	f.book2 = book("two", f.kate, f.joined1)
	f.book3 = book("three", f.joined2, f.leGuin)
	f.bk4 = book("four", f.leGuin)
	return f
}

func splitNamesOf(t *testing.T, s *database.PebbleStore, bookID string) []string {
	t.Helper()
	rows, err := s.GetBookNarrators(bookID)
	require.NoError(t, err)
	var names []string
	for i, r := range rows {
		require.Equal(t, i, r.Position)
		n, err := s.GetNarratorByID(r.NarratorID)
		require.NoError(t, err)
		require.NotNil(t, n, "book %s credits missing narrator %d", bookID, r.NarratorID)
		names = append(names, n.Name)
	}
	return names
}

func TestSplitJoinedNarrators_DryRunReportsAndWritesNothing(t *testing.T) {
	f := newSplitJoinedFixture(t)
	p := &Plugin{deps: fakeDeps{store: f.s}}
	rep, err := p.splitJoinedNarrators(context.Background(), splitJoinedNarratorsParams{}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 2, rep.Joined, "Le Guin, Ursula is one person")
	require.Equal(t, 3, rep.BooksAffected)
	require.Zero(t, rep.BooksRewritten)
	require.Equal(t, []string{"Kate Reading, Michael Kramer"}, splitNamesOf(t, f.s, f.book1))
	n, err := f.s.GetNarratorByID(f.joined1.ID)
	require.NoError(t, err)
	require.NotNil(t, n, "dry run must not delete")
}

func TestSplitJoinedNarrators_ApplySplitsInPlaceAndDeletesJoined(t *testing.T) {
	f := newSplitJoinedFixture(t)
	p := &Plugin{deps: fakeDeps{store: f.s}}
	rep, err := p.splitJoinedNarrators(context.Background(), splitJoinedNarratorsParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 3, rep.BooksRewritten)
	require.Equal(t, 2, rep.NarratorsDeleted)

	require.Equal(t, []string{"Kate Reading", "Michael Kramer"}, splitNamesOf(t, f.s, f.book1))
	require.Equal(t, []string{"Kate Reading", "Michael Kramer"}, splitNamesOf(t, f.s, f.book2),
		"a person credited directly and inside the cast is kept once")
	require.Equal(t, []string{"Dorrie Sacks", "Jeff Hays", "Le Guin, Ursula"}, splitNamesOf(t, f.s, f.book3))
	require.Equal(t, []string{"Le Guin, Ursula"}, splitNamesOf(t, f.s, f.bk4))

	rows, err := f.s.GetBookNarrators(f.book1)
	require.NoError(t, err)
	require.Equal(t, f.kate.ID, rows[0].NarratorID, "the existing Kate Reading entity is reused")
	require.Equal(t, "co-narrator", rows[1].Role)

	for _, id := range []int{f.joined1.ID, f.joined2.ID} {
		n, err := f.s.GetNarratorByID(id)
		require.NoError(t, err)
		require.Nil(t, n, "joined narrator %d should be deleted", id)
	}

	again, err := p.splitJoinedNarrators(context.Background(), splitJoinedNarratorsParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Zero(t, again.Joined, "idempotent")
}

// A limited run leaves the joined name linked by unprocessed books, so it
// must be held, not deleted.
func TestSplitJoinedNarrators_LimitHoldsStillLinkedJoined(t *testing.T) {
	f := newSplitJoinedFixture(t)
	p := &Plugin{deps: fakeDeps{store: f.s}}
	rep, err := p.splitJoinedNarrators(context.Background(), splitJoinedNarratorsParams{Apply: true, Limit: 1}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, rep.BooksRewritten)
	require.Equal(t, 2, rep.NarratorsHeld+rep.NarratorsDeleted)
	require.GreaterOrEqual(t, rep.NarratorsHeld, 1)
	// Every credit, rewritten or not, must still resolve to a live narrator;
	// splitNamesOf fails the test on a dangling one.
	for _, b := range []string{f.book1, f.book2, f.book3} {
		splitNamesOf(t, f.s, b)
	}
}

// Owner rules 2026-09-23: the book's author is dropped from an author+narrator
// credit; an authors-only credit and a URL are held for review, unsplit, and
// their joined entities stay linked, so they are not deleted.
func TestSplitJoinedNarrators_AppliesCreditRulesPerBook(t *testing.T) {
	s := newSeriesPhantomStore(t)
	mkN := func(name string) *database.Narrator {
		n, err := s.CreateNarrator(name)
		require.NoError(t, err)
		return n
	}
	mkBook := func(title string, n *database.Narrator, authors ...string) string {
		b, err := s.CreateBook(&database.Book{Title: title, FilePath: "/splitrules/" + title})
		require.NoError(t, err)
		var links []database.BookAuthor
		for i, name := range authors {
			a, err := s.CreateAuthor(name)
			require.NoError(t, err)
			links = append(links, database.BookAuthor{AuthorID: a.ID, Role: "author", Position: i})
		}
		if len(links) > 0 {
			require.NoError(t, s.SetBookAuthors(b.ID, links))
		}
		require.NoError(t, s.SetBookNarrators(b.ID, []database.BookNarrator{{NarratorID: n.ID, Role: "narrator"}}))
		return b.ID
	}
	mixed := mkN("Adrian Tchaikovsky, Ben Allen")
	authorsOnly := mkN("Craig Martelle, Michael Anderle")
	junk := mkN("https://kickass.to/user/Morrogoth/")
	withTranslator := mkN("By: Rick Partlow, Zachary J. Lorang - translator")

	bMixed := mkBook("mixed", mixed, "Adrian Tchaikovsky")
	bAuthors := mkBook("authors", authorsOnly, "Craig Martelle", "Michael Anderle")
	bJunk := mkBook("junk", junk)
	bTrans := mkBook("trans", withTranslator, "Some Author")

	p := &Plugin{deps: fakeDeps{store: s}}
	dry, err := p.splitJoinedNarrators(context.Background(), splitJoinedNarratorsParams{}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 2, dry.HeldForReview, "authors-only and URL credits are held in the dry run")
	require.Equal(t, 2, dry.BooksWithDrops, "the author and the translator/By: pieces are dropped")
	preview := make(map[string]splitBookPreview, len(dry.BooksPreview))
	for _, pv := range dry.BooksPreview {
		preview[pv.BookID] = pv
	}
	require.Len(t, preview, 4, "every affected book is previewed")
	require.Equal(t, splitBookPreview{
		BookID: bMixed, Title: "mixed", Authors: []string{"Adrian Tchaikovsky"},
		Before: []string{"Adrian Tchaikovsky, Ben Allen"}, After: []string{"Ben Allen"},
		Dropped: []string{"Adrian Tchaikovsky"},
	}, preview[bMixed])
	require.Equal(t, []string{"Rick Partlow"}, preview[bTrans].After)
	require.Equal(t, []string{"By: Rick Partlow", "Zachary J. Lorang - translator"}, preview[bTrans].Dropped)
	require.Equal(t, []string{"Craig Martelle, Michael Anderle"}, preview[bAuthors].After, "held credits preview unchanged")
	require.Empty(t, preview[bAuthors].Dropped)

	rep, err := p.splitJoinedNarrators(context.Background(), splitJoinedNarratorsParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 2, rep.HeldForReview)
	require.Equal(t, 2, rep.BooksRewritten)

	require.Equal(t, []string{"Ben Allen"}, splitNamesOf(t, s, bMixed))
	require.Equal(t, []string{"Rick Partlow"}, splitNamesOf(t, s, bTrans))
	require.Equal(t, []string{"Craig Martelle, Michael Anderle"}, splitNamesOf(t, s, bAuthors), "held, unsplit")
	require.Equal(t, []string{"https://kickass.to/user/Morrogoth/"}, splitNamesOf(t, s, bJunk), "held, unsplit")
	for _, id := range []string{bMixed, bTrans, bAuthors, bJunk} {
		require.Equal(t, preview[id].After, splitNamesOf(t, s, id), "the dry-run preview must be what apply writes")
	}

	for _, n := range []*database.Narrator{authorsOnly, junk} {
		got, err := s.GetNarratorByID(n.ID)
		require.NoError(t, err)
		require.NotNil(t, got, "a held joined narrator %q is still linked and must not be deleted", n.Name)
	}
	for _, n := range []*database.Narrator{mixed, withTranslator} {
		got, err := s.GetNarratorByID(n.ID)
		require.NoError(t, err)
		require.Nil(t, got, "split joined narrator %q should be deleted", n.Name)
	}
	author, err := s.GetNarratorByName("Adrian Tchaikovsky")
	require.NoError(t, err)
	require.Nil(t, author, "the author must not be minted as a narrator")
}

func TestSplitJoinedNarrators_RunPersistsReportAsResult(t *testing.T) {
	f := newSplitJoinedFixture(t)
	p := &Plugin{deps: fakeDeps{store: f.s}}
	rep := &resultReporter{}
	require.NoError(t, p.runSplitJoinedNarrators(context.Background(), nil, rep))
	got, ok := rep.result.(splitJoinedReport)
	require.True(t, ok, "the run must persist its report, got %T", rep.result)
	require.Equal(t, 3, got.BooksAffected)
	require.Len(t, got.BooksPreview, 3, "the dry-run preview is what the owner reads from the result")
}

// A junction row whose book is gone is an orphan, not a book: it is counted
// but never previewed or split, and apply clears it so the joined entity it
// held can be deleted.
func TestSplitJoinedNarrators_OrphanLinksAreClearedNotSplit(t *testing.T) {
	f := newSplitJoinedFixture(t)
	gone := "01KJBN4S75DFPD56DKY9VBMAPH"
	require.NoError(t, f.s.SetBookNarrators(gone, []database.BookNarrator{{NarratorID: f.joined2.ID, Role: "narrator"}}))
	p := &Plugin{deps: fakeDeps{store: f.s}}

	dry, err := p.splitJoinedNarrators(context.Background(), splitJoinedNarratorsParams{}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, dry.OrphanLinks)
	require.Equal(t, []string{gone}, dry.OrphanLinkSample)
	require.Equal(t, 3, dry.BooksAffected, "the orphan is not a book")
	for _, pv := range dry.BooksPreview {
		require.NotEqual(t, gone, pv.BookID, "an orphan must not be previewed")
		require.NotEmpty(t, pv.Title, "every previewed book exists")
	}
	for _, js := range dry.JoinedSample {
		if js.NarratorID == f.joined2.ID {
			require.Equal(t, 1, js.Books, "book counts exclude orphans")
		}
	}
	rows, err := f.s.GetBookNarrators(gone)
	require.NoError(t, err)
	require.Len(t, rows, 1, "dry run writes nothing")

	rep, err := p.splitJoinedNarrators(context.Background(), splitJoinedNarratorsParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, rep.OrphanLinksCleared)
	require.Equal(t, 3, rep.BooksRewritten)
	rows, err = f.s.GetBookNarrators(gone)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Equal(t, 2, rep.NarratorsDeleted, "the orphan no longer holds the joined entity")
	gotBook, err := f.s.GetBookByID(gone)
	require.NoError(t, err)
	require.Nil(t, gotBook, "clearing an orphan must not create a book")
}

// With no linked author the author-drop rule has nothing to check against,
// so the credit is held rather than split (a Witcher book credited
// "Andrzej Sapkowski, ..." would otherwise keep the author as narrator).
func TestSplitJoinedNarrators_BookWithNoAuthorsIsHeld(t *testing.T) {
	s := newSeriesPhantomStore(t)
	n, err := s.CreateNarrator("Andrzej Sapkowski, Peter Kenny")
	require.NoError(t, err)
	b, err := s.CreateBook(&database.Book{Title: "Blood of Elves", FilePath: "/splitnoauth/elves"})
	require.NoError(t, err)
	require.NoError(t, s.SetBookNarrators(b.ID, []database.BookNarrator{{NarratorID: n.ID, Role: "narrator"}}))
	p := &Plugin{deps: fakeDeps{store: s}}

	rep, err := p.splitJoinedNarrators(context.Background(), splitJoinedNarratorsParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, rep.HeldForReview)
	require.Equal(t, splitReasonNoAuthors, rep.HeldForReviewSample[0].Reason)
	require.Zero(t, rep.BooksRewritten)
	require.Equal(t, []string{"Andrzej Sapkowski, Peter Kenny"}, splitNamesOf(t, s, b.ID), "held, unsplit")
	got, err := s.GetNarratorByID(n.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "still linked, so not deleted")
}
