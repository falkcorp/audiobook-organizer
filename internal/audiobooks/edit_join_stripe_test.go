// file: internal/audiobooks/edit_join_stripe_test.go
// version: 1.1.0
// guid: 4b9d2e60-7a31-4c85-9f1e-d3a6c0b58e27
// last-edited: 2026-10-30

package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
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

func titleEdit(title string) *UpdateAudiobookRequest {
	raw, _ := json.Marshal(title)
	return &UpdateAudiobookRequest{
		Updates:    &AudiobookUpdate{Book: &database.Book{Title: title}},
		RawPayload: map[string]json.RawMessage{"title": raw},
	}
}

// rawValue is the stored bytes under key, or nil when the key is absent.
func rawValue(t *testing.T, ps *database.PebbleStore, key string) []byte {
	t.Helper()
	val, closer, err := ps.DB().Get([]byte(key))
	if err != nil {
		require.ErrorIs(t, err, pebble.ErrNotFound)
		return nil
	}
	defer closer.Close()
	return append([]byte(nil), val...)
}

// seedLegacyAuthors stores two author rows with tied positions as the raw
// book_authors value, bypassing SetBookAuthors (which normalises positions): a
// rewrite of this list by anything that normalises would change the bytes.
func seedLegacyAuthors(t *testing.T, ps *database.PebbleStore, bookID string) []byte {
	t.Helper()
	a1, err := ps.CreateAuthor("Bruce Wayne")
	require.NoError(t, err)
	a2, err := ps.CreateAuthor("Lucius Fox")
	require.NoError(t, err)
	raw, err := json.Marshal([]database.BookAuthor{
		{BookID: bookID, AuthorID: a2.ID, Role: "co-author", Position: 0},
		{BookID: bookID, AuthorID: a1.ID, Role: "author", Position: 0},
	})
	require.NoError(t, err)
	require.NoError(t, ps.DB().Set([]byte("book_authors:"+bookID), raw, nil))
	return raw
}

// A title-only edit leaves both credit lists exactly as stored: no rewrite, so
// ordering is not re-normalised. The lists are compared as raw stored bytes; the
// legacy rows have tied positions, which any normalising rewrite would change.
func TestUpdateAudiobook_TitleOnlyEditDoesNotRewriteCredits(t *testing.T) {
	ps, book := stripeFixture(t)
	seeded := seedLegacyAuthors(t, ps, book.ID)
	require.Equal(t, seeded, rawValue(t, ps, "book_authors:"+book.ID))
	n, err := ps.CreateNarrator("Selina Kyle")
	require.NoError(t, err)
	narratorRaw, err := json.Marshal([]database.BookNarrator{
		{BookID: book.ID, NarratorID: n.ID, Role: "narrator", Position: 0},
		{BookID: book.ID, NarratorID: n.ID, Role: "co-narrator", Position: 0},
	})
	require.NoError(t, err)
	require.NoError(t, ps.DB().Set([]byte("book_narrators:"+book.ID), narratorRaw, nil))

	got, err := NewAudiobookService(ps).UpdateAudiobook(context.Background(), book.ID, titleEdit("Year Two"))
	require.NoError(t, err)
	require.Equal(t, "Year Two", got.Title)
	require.Equal(t, seeded, rawValue(t, ps, "book_authors:"+book.ID))
	require.Equal(t, narratorRaw, rawValue(t, ps, "book_narrators:"+book.ID))
}

// A narrator-only edit rewrites the narrator list and leaves the author list's
// stored bytes alone (ModifyBookCredits stages only a list the callback changed).
func TestUpdateAudiobook_NarratorEditDoesNotRewriteAuthorList(t *testing.T) {
	ps, book := stripeFixture(t)
	seeded := seedLegacyAuthors(t, ps, book.ID)

	_, err := NewAudiobookService(ps).UpdateAudiobook(context.Background(), book.ID, narratorEdit("Selina Kyle"))
	require.NoError(t, err)
	require.Equal(t, []string{"Selina Kyle"}, joinNames(t, ps, book.ID))
	require.Equal(t, seeded, rawValue(t, ps, "book_authors:"+book.ID))
}

// One corrupt credit value must not block an edit that does not touch the
// credits: a title-only save still lands and leaves both values as they were.
func TestUpdateAudiobook_TitleOnlyEditSurvivesCorruptCreditKeys(t *testing.T) {
	for _, key := range []string{"book_authors:", "book_narrators:"} {
		t.Run(key, func(t *testing.T) {
			ps, book := stripeFixture(t)
			corrupt := []byte("{not json")
			require.NoError(t, ps.DB().Set([]byte(key+book.ID), corrupt, nil))

			got, err := NewAudiobookService(ps).UpdateAudiobook(context.Background(), book.ID, titleEdit("Year Two"))
			require.NoError(t, err)
			require.Equal(t, "Year Two", got.Title)
			row, err := ps.GetBookByID(book.ID)
			require.NoError(t, err)
			require.Equal(t, "Year Two", row.Title)
			require.Equal(t, corrupt, rawValue(t, ps, key+book.ID))
		})
	}
}

// A store that cannot write the row and its credits together fails the edit;
// there is no separate-setter fallback. It fails before any narrator record is
// created, so nothing is left behind.
type noCreditsStore struct{ audiobookStore }

func TestUpdateAudiobook_StoreWithoutCreditsWriteFailsTheEdit(t *testing.T) {
	ps, book := stripeFixture(t)
	_, err := NewAudiobookService(noCreditsStore{ps}).UpdateAudiobook(context.Background(), book.ID, narratorEdit("Selina Kyle"))
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrInvalidAudiobookUpdate))
	require.Empty(t, joinNames(t, ps, book.ID))
	orphan, err := ps.GetNarratorByName("Selina Kyle")
	require.NoError(t, err)
	require.Nil(t, orphan, "the refused edit created a narrator record")
}

// failingNarratorStore is a PebbleStore whose CreateNarrator fails.
type failingNarratorStore struct{ *database.PebbleStore }

func (failingNarratorStore) CreateNarrator(string) (*database.Narrator, error) {
	return nil, errors.New("narrator index unavailable")
}

// A narrator that cannot be created fails the edit: the Narrator column is not
// saved without the junction that matches it.
func TestUpdateAudiobook_NarratorCreateFailureFailsTheEdit(t *testing.T) {
	ps, book := stripeFixture(t)
	_, err := NewAudiobookService(failingNarratorStore{ps}).UpdateAudiobook(context.Background(), book.ID, narratorEdit("Selina Kyle"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "Selina Kyle")
	row, err := ps.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Nil(t, row.Narrator, "the narrator column was saved without its junction")
	require.Empty(t, joinNames(t, ps, book.ID))
}

// Unreadable book authors (needed to clean a narrator credit) fail a narrator
// edit instead of saving the column and leaving the junction alone.
func TestUpdateAudiobook_NarratorEditWithUnreadableAuthorsFailsTheEdit(t *testing.T) {
	ps, book := stripeFixture(t)
	require.NoError(t, ps.DB().Set([]byte("book_authors:"+book.ID), []byte("{not json"), nil))

	_, err := NewAudiobookService(ps).UpdateAudiobook(context.Background(), book.ID, narratorEdit("Selina Kyle"))
	require.Error(t, err)
	row, err := ps.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Nil(t, row.Narrator, "the narrator column was saved without its junction")
	require.Empty(t, joinNames(t, ps, book.ID))
}
