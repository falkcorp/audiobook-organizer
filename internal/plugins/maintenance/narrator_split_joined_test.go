// file: internal/plugins/maintenance/narrator_split_joined_test.go
// version: 1.0.0
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
// bk4 [Le Guin, Ursula] only (a surname-first name, NOT joined).
func newSplitJoinedFixture(t *testing.T) splitJoinedFixture {
	s := newSeriesPhantomStore(t)
	mk := func(name string) *database.Narrator {
		n, err := s.CreateNarrator(name)
		require.NoError(t, err)
		return n
	}
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
