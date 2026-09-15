// file: internal/scheduler/author_split_lost_update_test.go
// version: 1.0.0
// guid: ce15c0ee-2b3c-4527-89e3-b2998eb307a7
// last-edited: 2026-09-14

package scheduler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// splitLostUpdateStore is a real PebbleStore with the OTHER writer wired in:
// it commits Duration=4242 on the row right after every raw GetBookByID and
// right before every ModifyBook takes the row lock, which is where a real
// concurrent write of another column lands in production. A site that reads
// the row and writes the WHOLE row back reverts it; ModifyBook keeps it.
type splitLostUpdateStore struct {
	*database.PebbleStore
}

func (s splitLostUpdateStore) landDuration(id string) {
	_, _ = s.PebbleStore.ModifyBook(id, func(b *database.Book) error {
		d := 4242
		b.Duration = &d
		return nil
	})
}

func (s splitLostUpdateStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.PebbleStore.GetBookByID(id)
	s.landDuration(id)
	return b, err
}

func (s splitLostUpdateStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.landDuration(id)
	return s.PebbleStore.ModifyBook(id, fn)
}

// TestAuthorSplitScanOp_PrimaryAuthorWriteDoesNotRevertConcurrentColumns pins
// the lost-update fix on the split's primary-author write (audit A1#15): it
// must set only AuthorID/Author, so a Duration another writer commits between
// its read and its write survives. Against the old GetBookByID ->
// UpdateBook(whole row) it fails with "Duration reverted".
func TestAuthorSplitScanOp_PrimaryAuthorWriteDoesNotRevertConcurrentColumns(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	b, err := st.CreateBook(&database.Book{
		Title:            "split-scan-live",
		FilePath:         "/split-scan-live/book.m4b",
		AuthorID:         new(composite.ID),
		IsPrimaryVersion: new(true),
	})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: composite.ID, Role: "author"}}))

	rep := runSplitScanOp(t, splitLostUpdateStore{st})

	alice, err := st.GetAuthorByName("Alice Smith")
	require.NoError(t, err)
	require.NotNil(t, alice)
	full, err := st.GetBookByID(b.ID)
	require.NoError(t, err)
	require.NotNil(t, full.AuthorID)
	require.Equal(t, alice.ID, *full.AuthorID, "primary author not repointed; op logs: %v", rep.logs)
	require.NotNil(t, full.Author)
	require.Equal(t, alice.ID, full.Author.ID, "denormalized Author must match AuthorID")
	if full.Duration == nil || *full.Duration != 4242 {
		t.Fatalf("Duration reverted by the primary-author write: got %v, want 4242 (the concurrent writer's value)", full.Duration)
	}
}
