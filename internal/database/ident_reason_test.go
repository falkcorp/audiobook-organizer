// file: internal/database/ident_reason_test.go
// version: 1.0.0
// guid: e19cbdbb-ddee-44d1-92db-478665f68674
// last-edited: 2026-10-16

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func identReasonStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.EnsureIdentIndex(t.Context()))
	return s
}

func identDirtyIDs(t *testing.T, s *PebbleStore) []string {
	t.Helper()
	page, err := s.IdentDirtyPage("", 100)
	require.NoError(t, err)
	ids := make([]string, len(page))
	for i, e := range page {
		ids[i] = e.BookID
	}
	return ids
}

// The driver's reason attaches while the book's question fingerprint is the
// one it was recorded under, and stops attaching once the book is retitled.
func TestIdentStateFor_AttachesReasonWhileQuestionUnchanged(t *testing.T) {
	s := identReasonStore(t)
	b, err := s.CreateBook(&Book{Title: "Title 000123", FilePath: "/lib/Title 000123/a.m4b"})
	require.NoError(t, err)
	require.NoError(t, s.SetIdentReason(b.ID, IdentReasonNoUsableTitle, IdentQuestionFP(b)))

	v, ok, err := s.IdentStateFor(b.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, IdentUnasked, v.State)
	require.Equal(t, IdentReasonNoUsableTitle, v.Reason)
	require.True(t, v.Primary)

	b.Title = "Title 000124"
	_, err = s.UpdateBook(b.ID, b)
	require.NoError(t, err)
	v, _, err = s.IdentStateFor(b.ID)
	require.NoError(t, err)
	require.Empty(t, v.Reason, "a reason recorded for the old title must not attach")

	// Clearing a reason, present or not, is harmless.
	require.NoError(t, s.SetIdentReason(b.ID, "", ""))
	require.NoError(t, s.SetIdentReason(b.ID, "", ""))
}

// The retitle chokepoint marks the book dirty; a candidate-cache write (the
// driver's own write) and a non-identity book write do not, or the driver
// would loop on its own output.
func TestCandidateCachePutDoesNotMarkDirty(t *testing.T) {
	s := identReasonStore(t)
	b, err := s.CreateBook(&Book{Title: "Title 000125", FilePath: "/lib/Title 000125/a.m4b"})
	require.NoError(t, err)
	require.Empty(t, identDirtyIDs(t, s), "CreateBook itself marks nothing (the scanner does)")

	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: b.ID}))
	require.Empty(t, identDirtyIDs(t, s), "a candidate-cache write marked the book")

	narrator := "Narrator 03"
	b.Narrator = &narrator
	_, err = s.UpdateBook(b.ID, b)
	require.NoError(t, err)
	require.Empty(t, identDirtyIDs(t, s), "a non-identity write marked the book")

	b.Title = "Title 000126"
	_, err = s.UpdateBook(b.ID, b)
	require.NoError(t, err)
	require.Equal(t, []string{b.ID}, identDirtyIDs(t, s), "a retitle must mark the book")
}

// MarkIdentDirtyBestEffort is nil-safe and does nothing for a store without
// the capability.
func TestMarkIdentDirtyBestEffort_NilSafe(t *testing.T) {
	require.False(t, MarkIdentDirtyBestEffort(nil, "b1", "test"))
	require.False(t, MarkIdentDirtyBestEffort(struct{}{}, "b1", "test"))
	s := identReasonStore(t)
	require.True(t, MarkIdentDirtyBestEffort(s, "b1", "test"))
	require.Equal(t, []string{"b1"}, identDirtyIDs(t, s))
}
