// file: internal/playlist/playlist_test.go
// version: 3.0.0
// guid: 3b4c5d6e-7f8a-9b0c-1d2e-3f4a5b6c7d8e
// last-edited: 2026-08-14

// NOTE(fable5 T022): Tests that relied on database.DB (getBooksInSeries,
// savePlaylistToDatabase, GeneratePlaylistsForSeries with live data) were
// removed; those code paths were deleted with the SQLite store. The
// file-level M3U path (generatePlaylistFile + its PlaylistItem) was deleted
// on 2026-08-14 — it had zero non-test callers.

package playlist

import (
	"testing"
)

// TestGeneratePlaylistsForSeriesReturnsError verifies that the legacy
// SQLite-backed GeneratePlaylistsForSeries path now returns an error
// (the implementation was removed in fable5 T022).
func TestGeneratePlaylistsForSeriesReturnsError(t *testing.T) {
	if err := GeneratePlaylistsForSeries(); err == nil {
		t.Error("expected GeneratePlaylistsForSeries to return an error after SQLite removal")
	}
}
