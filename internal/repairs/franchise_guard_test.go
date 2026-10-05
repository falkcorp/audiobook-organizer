// file: internal/repairs/franchise_guard_test.go
// version: 1.0.1
// guid: 2b7d4e19-6a3c-4f05-8e91-c4d7a2b6f3e0
// last-edited: 2026-10-04

package repairs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

type tagMap map[string][]database.BookTag

func (m tagMap) GetBookTagsDetailed(id string) ([]database.BookTag, error) {
	if id == "boom" {
		return nil, errors.New("tags down")
	}
	return m[id], nil
}

// A franchise tag holds a book whose title, path and credits name nothing:
// the guard stays reliable after a rename.
func TestGuardBooks_FranchiseTagHolds(t *testing.T) {
	s := newMemStore()
	s.add("tagged", "Escape from Reality", "/lib/neutral/x", nil)
	s.add("plain", "Escape from Reality", "/lib/neutral/y", nil)
	s.add("boom", "Escape from Reality", "/lib/neutral/z", nil)
	tags := tagMap{"tagged": {{Tag: "franchise:doctor-who", Source: "franchise-matcher"}},
		"plain": {{Tag: "range:war-master", Source: "user"}, {Tag: "franchise:star-wars", Source: "user"}}}
	k, why, err := GuardBooks(s, tags, nil, NewPathResolver(), []string{"tagged"})
	require.NoError(t, err)
	require.Equal(t, SkipOwnerManual, k, why)
	require.Contains(t, why, "franchise:doctor-who")
	k, _, err = GuardBooks(s, tags, nil, NewPathResolver(), []string{"plain"})
	require.NoError(t, err)
	require.Empty(t, k, "a range tag, or another franchise's tag, is not an owner-manual hold")
	_, _, err = GuardBooks(s, tags, nil, NewPathResolver(), []string{"boom"})
	require.Error(t, err, "a tag read failure fails closed")
}

// The transcribed intro (book and file) holds a blank-titled rip.
func TestGuardBooks_TranscribedFields(t *testing.T) {
	s := newMemStore()
	s.add("bt", "Track 01", "/lib/neutral/a", nil)
	tt := "Doctor Who: The Chimes of Midnight"
	s.books["bt"].TranscribedTitle = &tt
	s.add("ft", "Track 01", "/lib/neutral/b", nil)
	ta := "Big Finish Productions presents"
	s.files["ft"] = []database.BookFile{{ID: "f1", BookID: "ft", FilePath: "/lib/neutral/b/01.mp3", TranscribedAuthor: &ta}}
	for _, id := range []string{"bt", "ft"} {
		k, why, err := GuardBooks(s, nil, nil, NewPathResolver(), []string{id})
		require.NoError(t, err)
		require.Equal(t, SkipOwnerManual, k, "%s: %s", id, why)
	}
}

// Census range terms now hold in the Repairs guard (newly held, widening).
func TestGuardBooks_CensusRangeTerms(t *testing.T) {
	s := newMemStore()
	s.add("sg", "Gift of the Gods", "/lib/Unknown Author/x", nil)
	s.books["sg"].Narrator = strPtr("Stargate SG-1 - Series 2")
	s.add("cy", "Genesis of the Cybermen", "/lib/Unknown Author/y", nil)
	s.add("tg", "War Master's Gate", "/lib/Adrian Tchaikovsky/Shadows of the Apt/09", nil)
	for id, held := range map[string]bool{"sg": true, "cy": true, "tg": false} {
		k, why, err := GuardBooks(s, nil, nil, NewPathResolver(), []string{id})
		require.NoError(t, err)
		require.Equal(t, held, k == SkipOwnerManual, "%s: %s", id, why)
	}
}

func strPtr(s string) *string { return &s }

type tagsOnlyFixer struct{ testFixer }

func (tagsOnlyFixer) BookTagsOnly() bool { return true }

type testFixer struct{}

func (testFixer) ID() string          { return "t" }
func (testFixer) Title() string       { return "t" }
func (testFixer) Description() string { return "t" }
func (testFixer) Plan(context.Context, json.RawMessage, registry.Reporter) ([]Row, error) {
	return nil, nil
}
func (testFixer) Replan(_ context.Context, _ json.RawMessage, r Row, _ registry.Reporter) (Row, error) {
	return r, nil
}
func (testFixer) Apply(context.Context, *Writer, Row) error { return nil }

// A tags-only fixer is not guarded: its rows are the Doctor Who books.
func TestGuardBooksFor_BookTagsOnlySkipsGuard(t *testing.T) {
	s := newMemStore()
	s.add("dw", "Doctor Who: Placebo Effect", "/books/itunes/iTunes Media/x.m4b", nil)
	k, _, err := GuardBooksFor(testFixer{}, s, nil, nil, NewPathResolver(), []string{"dw"})
	require.NoError(t, err)
	require.NotEmpty(t, k)
	k, _, err = GuardBooksFor(tagsOnlyFixer{}, s, nil, nil, NewPathResolver(), []string{"dw"})
	require.NoError(t, err)
	require.Empty(t, k)
}

// AddBookTag journals before it writes, leaves an existing tag (any source)
// alone, and its row reverts only while the source is still the repair's.
func TestWriterAddBookTag(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	b, err := st.CreateBook(&database.Book{Title: "x", FilePath: "/lib/x", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, st.AddBookTagWithSource(b.ID, "range:war-master", "user"))
	w := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-tags").WithTags(st)

	wrote, err := w.AddBookTag(b.ID, "Franchise:Doctor-Who", "franchise-matcher")
	require.NoError(t, err)
	require.True(t, wrote)
	wrote, err = w.AddBookTag(b.ID, "range:war-master", "franchise-matcher")
	require.NoError(t, err)
	require.False(t, wrote, "a person's tag is never re-sourced")

	rows, err := st.GetOperationChanges("op-tags")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, undo.ChangeTypeBookTagAdd, rows[0].ChangeType)
	require.Equal(t, "franchise:doctor-who", rows[0].FieldName)
	require.Empty(t, undo.NotRestorableLabel(rows[0]))

	tags, err := st.GetBookTagsDetailed(b.ID)
	require.NoError(t, err)
	require.NoError(t, undo.CheckBookTagAdd(tags, rows[0]))
	// A person claims it: the revert must refuse.
	require.NoError(t, st.AddBookTagWithSource(b.ID, "franchise:doctor-who", "user"))
	tags, err = st.GetBookTagsDetailed(b.ID)
	require.NoError(t, err)
	require.Error(t, undo.CheckBookTagAdd(tags, rows[0]))
	require.NoError(t, st.RemoveBookTag(b.ID, "franchise:doctor-who"))
	tags, err = st.GetBookTagsDetailed(b.ID)
	require.NoError(t, err)
	require.ErrorIs(t, undo.CheckBookTagAdd(tags, rows[0]), undo.ErrAlreadyRestored)

	// No journal wired: nothing is written.
	w2 := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithTags(st)
	_, err = w2.AddBookTag(b.ID, "franchise:torchwood", "franchise-matcher")
	require.ErrorIs(t, err, ErrNotJournaled)
	tags, err = st.GetBookTagsDetailed(b.ID)
	require.NoError(t, err)
	for _, tg := range tags {
		require.NotEqual(t, "franchise:torchwood", tg.Tag)
	}
}
