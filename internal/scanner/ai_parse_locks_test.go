// file: internal/scanner/ai_parse_locks_test.go
// version: 1.0.0
// guid: 3f0c9a7e-2b64-4d1f-8a55-c1e7d9b2f604
// last-edited: 2026-09-02

package scanner

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// lockBlank records a user lock on key for bookID with no value: the user
// looked at the empty field and said "leave it empty".
func lockBlank(t *testing.T, store database.Store, bookID, key string) {
	t.Helper()
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID: bookID, Field: key, OverrideLocked: true,
	}))
}

// TestSaveAIFieldsHonorsALockedBlankSeries: the AI nomination path fills a
// field only when it is empty, which used to be the whole test -- a locked
// EMPTY series was indistinguishable from an unlocked one, so the AI's guess
// landed on it. The row here has a blank series the user locked, and the AI
// result carries a series, a position and a narrator. Series and position must
// stay blank; the unlocked narrator must still be written, so the guard is
// shown to be per-field rather than a blanket refusal.
func TestSaveAIFieldsHonorsALockedBlankSeries(t *testing.T) {
	store := aiSaveStore(t)
	row, err := store.CreateBook(&database.Book{FilePath: "/lib/standalone.m4b", Title: "A Standalone"})
	require.NoError(t, err)
	lockBlank(t, store, row.ID, database.FieldKeySeriesName)

	_, err = saveAIFieldsToPrimary(context.Background(), row.ID, &Book{
		FilePath: "/lib/standalone.m4b",
		Series:   "Not Actually A Series",
		Position: 3,
		Narrator: "A Narrator",
	})
	require.NoError(t, err)

	got, err := store.GetBookByID(row.ID)
	require.NoError(t, err)
	require.Nil(t, got.SeriesID, "the user locked the series blank; the AI's series must not land")
	require.Nil(t, got.SeriesSequence, "a series lock protects the position too")
	require.NotNil(t, got.Narrator, "narrator is unlocked and must still be filled")
	require.Equal(t, "A Narrator", *got.Narrator)

	all, err := store.GetAllSeries()
	require.NoError(t, err)
	require.Empty(t, all, "a locked series must not even be CREATED as a side effect")
}

// Every other lockable key the AI path writes, one at a time on a fixture where
// the AI has a value for all of them: the locked one stays blank, the rest land.
func TestSaveAIFieldsHonorsEachLockKeyItWrites(t *testing.T) {
	cases := []struct {
		key   string
		blank func(t *testing.T, b *database.Book)
	}{
		{database.FieldKeyTitle, func(t *testing.T, b *database.Book) { require.Equal(t, "", b.Title) }},
		{database.FieldKeyAuthorName, func(t *testing.T, b *database.Book) { require.Nil(t, b.AuthorID) }},
		{database.FieldKeyNarrator, func(t *testing.T, b *database.Book) { require.Nil(t, b.Narrator) }},
		{database.FieldKeyPublisher, func(t *testing.T, b *database.Book) { require.Nil(t, b.Publisher) }},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			store := aiSaveStore(t)
			row, err := store.CreateBook(&database.Book{FilePath: "/lib/blank.m4b"})
			require.NoError(t, err)
			lockBlank(t, store, row.ID, tc.key)

			_, err = saveAIFieldsToPrimary(context.Background(), row.ID, &Book{
				FilePath:  "/lib/blank.m4b",
				Title:     "AI Title",
				Author:    "AI Author",
				Narrator:  "AI Narrator",
				Publisher: "AI Publisher",
			})
			require.NoError(t, err)

			got, err := store.GetBookByID(row.ID)
			require.NoError(t, err)
			tc.blank(t, got)

			// Something unlocked must have landed, or the guard is a blanket refusal.
			filled := 0
			if got.Title != "" {
				filled++
			}
			if got.AuthorID != nil {
				filled++
			}
			if got.Narrator != nil {
				filled++
			}
			if got.Publisher != nil {
				filled++
			}
			require.Equal(t, 3, filled, "exactly the three unlocked fields must be filled")
		})
	}
}

// Lock rows unreadable: fail closed. Nothing is written, but the row is still
// reported for stamping (the parse was attempted).
func TestSaveAIFieldsFailsClosedWhenLocksAreUnreadable(t *testing.T) {
	updates := 0
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, FilePath: "/lib/x.m4b"}, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			updates++
			return b, nil
		},
		GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) {
			return nil, errors.New("pebble: closed")
		},
	}
	SetStore(mock)
	t.Cleanup(func() { SetStore(nil) })

	stamped, err := saveAIFieldsToPrimary(context.Background(), "b1", &Book{
		FilePath: "/lib/x.m4b", Title: "AI Title", Narrator: "AI Narrator",
	})
	require.NoError(t, err)
	require.Equal(t, "/lib/x.m4b", stamped)
	require.Equal(t, 0, updates, "with the locks unreadable, nothing may be written")
}
