// file: internal/server/handlers/metadata_cache_ident_counts_test.go
// version: 1.0.0
// guid: 12551103-eea0-4207-90b8-15df669deded
// last-edited: 2026-10-11

package handlers

import (
	"encoding/json"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestIdentCountsSummary: the review summary's ident_counts block is the
// identification index's primary-scope counters, with missing = unasked +
// asked_empty, and keeps its shape (ready=false, zeros) when the store has
// no index.
func TestIdentCountsSummary(t *testing.T) {
	s, err := database.NewPebbleStoreInMemory("identsummary_" + ulid.Make().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()

	mk := func(title string) string {
		b, err := s.CreateBook(&database.Book{Title: title, FilePath: "/lib/sum/" + title, Format: "m4b"})
		require.NoError(t, err)
		return b.ID
	}
	mk("Title 000001") // unasked
	empty := mk("Title 000002")
	require.NoError(t, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: empty}))
	cand := mk("Title 000003")
	require.NoError(t, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: cand, Candidates: []json.RawMessage{json.RawMessage(`{}`)}}))
	held := mk("Title 000004")
	_, err = s.ModifyBook(held, func(b *database.Book) error { v := "no_match"; b.MetadataReviewStatus = &v; return nil })
	require.NoError(t, err)

	got := identCountsSummary(s)
	require.Equal(t, true, got["ready"])
	require.Equal(t, int64(1), got["unasked"])
	require.Equal(t, int64(1), got["asked_empty"])
	require.Equal(t, int64(1), got["has_candidates"])
	require.Equal(t, int64(0), got["applied"])
	require.Equal(t, int64(1), got["held"])
	require.Equal(t, int64(2), got["missing"])

	none := identCountsSummary(nil)
	require.Equal(t, false, none["ready"])
	require.Equal(t, int64(0), none["missing"])
	require.Len(t, none, len(got), "the same keys either way")
}
