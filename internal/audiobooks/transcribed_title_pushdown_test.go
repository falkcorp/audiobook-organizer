// file: internal/audiobooks/transcribed_title_pushdown_test.go
// version: 1.0.0
// guid: 8f3a1d62-4b09-4c7e-9a15-2e6c0b7d4f38
// last-edited: 2026-07-01

package audiobooks

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// TestGetAudiobooks_CarriesTranscribedTitleThroughPushdown is the load-bearing
// guard for FilterSpec.OnlyParsedTranscription. That filter runs in
// resolveFilterToBookIDs against the Books returned by GetAudiobooks — which,
// after PR #1660, resolves via the BookSummary pushdown (bookSummariesToBooks).
// If TranscribedTitle isn't threaded through BookSummary, every returned Book
// has a nil TranscribedTitle and the filter silently returns ZERO books.
//
// This exercises the ACTUAL service path (real PebbleStore + live memdb
// pushdown), not the stripBookForMemdb projection in isolation, so it fails
// loudly if the summary projection ever drops the field.
func TestGetAudiobooks_CarriesTranscribedTitleThroughPushdown(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	primary := true
	parsed := "Salem's Lot"

	// Book WITH a parsed transcription title.
	withTitle, err := ps.CreateBook(&database.Book{
		Title:            "book-with-title",
		IsPrimaryVersion: &primary,
		TranscribedTitle: &parsed,
	})
	require.NoError(t, err)

	// Book WITHOUT (raw-but-unparsed → TranscribedTitle nil).
	without, err := ps.CreateBook(&database.Book{
		Title:            "book-without-title",
		IsPrimaryVersion: &primary,
	})
	require.NoError(t, err)

	svc := NewAudiobookService(ps)
	got, err := svc.GetAudiobooks(context.Background(), 1000, 0, "", nil, nil,
		ListFilters{IsPrimaryVersion: &primary})
	require.NoError(t, err)

	byID := make(map[string]database.Book, len(got))
	for _, b := range got {
		byID[b.ID] = b
	}

	w, ok := byID[withTitle.ID]
	require.True(t, ok, "book-with-title missing from GetAudiobooks result")
	require.NotNil(t, w.TranscribedTitle,
		"TranscribedTitle dropped by the summary pushdown — OnlyParsedTranscription would return zero books")
	require.Equal(t, parsed, *w.TranscribedTitle)

	wo, ok := byID[without.ID]
	require.True(t, ok, "book-without-title missing from GetAudiobooks result")
	require.Nil(t, wo.TranscribedTitle, "unparsed book must have nil TranscribedTitle")
}
