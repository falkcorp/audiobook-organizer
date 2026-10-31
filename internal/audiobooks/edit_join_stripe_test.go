// file: internal/audiobooks/edit_join_stripe_test.go
// version: 1.0.0
// guid: 4b9d2e60-7a31-4c85-9f1e-d3a6c0b58e27
// last-edited: 2026-10-30

package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// narratorEdit is the PUT a narrator-only save sends.
func narratorEdit(name string) *UpdateAudiobookRequest {
	raw, _ := json.Marshal(name)
	return &UpdateAudiobookRequest{
		Updates:    &AudiobookUpdate{Book: &database.Book{Narrator: &name}},
		RawPayload: map[string]json.RawMessage{"narrator": raw},
	}
}

func joinNames(t *testing.T, ps *database.PebbleStore, bookID string) []string {
	t.Helper()
	rows, err := ps.GetBookNarrators(bookID)
	require.NoError(t, err)
	var names []string
	for _, r := range rows {
		n, err := ps.GetNarratorByID(r.NarratorID)
		require.NoError(t, err)
		require.NotNil(t, n)
		names = append(names, n.Name)
	}
	return names
}

func stripeFixture(t *testing.T) (*database.PebbleStore, *database.Book) {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	book, err := ps.CreateBook(&database.Book{Title: "Year One", FilePath: "/library/year-one.m4b", Format: "m4b"})
	require.NoError(t, err)
	return ps, book
}

// The edit's narrator list and the book row commit together: with a concurrent
// writer that sets the narrator COLUMN (whose junction sync is the "narrator
// sync path"), the row and the junction always agree on one writer's cast.
// Before the edit wrote its joins under the book's stripe, an edit's junction
// write could land after the other writer's row write and leave the column on
// one narrator and the junction on the other.
func TestUpdateAudiobook_NarratorRowAndJoinAgreeUnderConcurrentColumnWrite(t *testing.T) {
	for i := 0; i < 25; i++ {
		ps, book := stripeFixture(t)
		svc := NewAudiobookService(ps)
		var wg sync.WaitGroup
		var editErr, modErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, editErr = svc.UpdateAudiobook(context.Background(), book.ID, narratorEdit("Selina Kyle"))
		}()
		go func() {
			defer wg.Done()
			_, modErr = ps.ModifyBook(book.ID, func(b *database.Book) error {
				v := "Harvey Dent"
				b.Narrator = &v
				return nil
			})
		}()
		wg.Wait()
		require.NoError(t, editErr)
		require.NoError(t, modErr)

		row, err := ps.GetBookByID(book.ID)
		require.NoError(t, err)
		require.NotNil(t, row.Narrator)
		require.Equal(t, []string{*row.Narrator}, joinNames(t, ps, book.ID),
			"the narrator column and the book_narrators join come from different writers")
	}
}

// Two writers of the same join, one through the edit and one through
// SetBookNarrators: the final join is one writer's complete cast, never a mix.
func TestUpdateAudiobook_NarratorJoinIsOneCompleteWriteUnderRace(t *testing.T) {
	for i := 0; i < 25; i++ {
		ps, book := stripeFixture(t)
		svc := NewAudiobookService(ps)
		a, err := ps.CreateNarrator("Barbara Gordon")
		require.NoError(t, err)
		b, err := ps.CreateNarrator("Dick Grayson")
		require.NoError(t, err)

		var wg sync.WaitGroup
		var editErr, setErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, editErr = svc.UpdateAudiobook(context.Background(), book.ID, narratorEdit("Alfred Pennyworth"))
		}()
		go func() {
			defer wg.Done()
			setErr = ps.SetBookNarrators(book.ID, []database.BookNarrator{
				{BookID: book.ID, NarratorID: a.ID, Role: "narrator", Position: 0},
				{BookID: book.ID, NarratorID: b.ID, Role: "co-narrator", Position: 1},
			})
		}()
		wg.Wait()
		require.NoError(t, editErr)
		require.NoError(t, setErr)

		got := joinNames(t, ps, book.ID)
		require.Contains(t, [][]string{
			{"Alfred Pennyworth"},
			{"Barbara Gordon", "Dick Grayson"},
		}, got, "the join is a mix of two writes")
	}
}

// A title-only edit leaves both credit lists exactly as stored: no rewrite, so
// ordering is not re-normalised.
func TestUpdateAudiobook_TitleOnlyEditDoesNotRewriteCredits(t *testing.T) {
	ps, book := stripeFixture(t)
	a1, err := ps.CreateAuthor("Bruce Wayne")
	require.NoError(t, err)
	a2, err := ps.CreateAuthor("Lucius Fox")
	require.NoError(t, err)
	// Tied positions: a normalising rewrite would reorder these.
	stored := []database.BookAuthor{
		{BookID: book.ID, AuthorID: a2.ID, Role: "author", Position: 0},
		{BookID: book.ID, AuthorID: a1.ID, Role: "co-author", Position: 0},
	}
	require.NoError(t, ps.SetBookAuthors(book.ID, stored))
	before, err := ps.GetBookAuthors(book.ID)
	require.NoError(t, err)

	_, err = NewAudiobookService(ps).UpdateAudiobook(context.Background(), book.ID, &UpdateAudiobookRequest{
		Updates:    &AudiobookUpdate{Book: &database.Book{Title: "Year Two"}},
		RawPayload: map[string]json.RawMessage{"title": json.RawMessage(`"Year Two"`)},
	})
	require.NoError(t, err)
	after, err := ps.GetBookAuthors(book.ID)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// A store that cannot write the row and its credits together fails the edit;
// there is no separate-setter fallback.
type noCreditsStore struct{ audiobookStore }

func TestUpdateAudiobook_StoreWithoutCreditsWriteFailsTheEdit(t *testing.T) {
	ps, book := stripeFixture(t)
	_, err := NewAudiobookService(noCreditsStore{ps}).UpdateAudiobook(context.Background(), book.ID, narratorEdit("Selina Kyle"))
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrInvalidAudiobookUpdate))
	require.Empty(t, joinNames(t, ps, book.ID))
}
