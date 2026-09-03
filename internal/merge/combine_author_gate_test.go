// file: internal/merge/combine_author_gate_test.go
// version: 1.0.0
// guid: 01819a26-c9c8-4974-9930-65c74b8cce25
// last-edited: 2026-09-03

package merge

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

func combineTwo(t *testing.T, store database.Store, author string) (string, error) {
	t.Helper()
	survivor := &database.Book{ID: ulid.Make().String(), Title: "Ch 1", Format: "mp3", FilePath: "/tmp/g/a.mp3"}
	other := &database.Book{ID: ulid.Make().String(), Title: "Ch 2", Format: "mp3", FilePath: "/tmp/g/b.mp3"}
	for _, b := range []*database.Book{survivor, other} {
		_, err := store.CreateBook(b)
		require.NoError(t, err)
	}
	ms := NewService(store)
	_, err := ms.CombineBooks([]string{survivor.ID, other.ID}, survivor.ID,
		&CombineOverride{Author: author})
	return survivor.ID, err
}

// 🔴 A junk override must not become an author row. The Combine override is
// often PREFILLED from one of the books being combined, so a book already
// carrying "Track 01" would otherwise propagate that name onto the survivor.
func TestCombineBooks_RejectsUnusableAuthorOverride(t *testing.T) {
	store := setupTestStore(t)
	id, err := combineTwo(t, store, "Track 01")
	require.NoError(t, err, "combine itself must still succeed")

	if a, gerr := store.GetAuthorByName("Track 01"); gerr == nil && a != nil {
		t.Errorf("created author row %q from a rejected override", a.Name)
	}
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	if b.AuthorID != nil {
		t.Errorf("survivor got AuthorID %v from a rejected override", *b.AuthorID)
	}
}

// A numbered override carrying a real person resolves to the person, and the
// author row is created under the CLEANED name rather than the raw one.
func TestCombineBooks_StripsNumberingFromAuthorOverride(t *testing.T) {
	store := setupTestStore(t)
	id, err := combineTwo(t, store, "001-147 Kevin J Anderson")
	require.NoError(t, err)

	if a, gerr := store.GetAuthorByName("001-147 Kevin J Anderson"); gerr == nil && a != nil {
		t.Errorf("stored the raw numbered name as an author row: %q", a.Name)
	}
	got, err := store.GetAuthorByName("Kevin J Anderson")
	require.NoError(t, err)
	if got == nil {
		t.Fatal("the real author was not created from the numbered override")
	}
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	if b.AuthorID == nil || *b.AuthorID != got.ID {
		t.Errorf("survivor AuthorID = %v, want %d", b.AuthorID, got.ID)
	}
}
