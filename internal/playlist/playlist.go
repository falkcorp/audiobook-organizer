// file: internal/playlist/playlist.go
// version: 3.0.0
// guid: 2a3b4c5d-6e7f-8a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-08-14

package playlist

import (
	"fmt"
	"log/slog"
)

// GeneratePlaylistsForSeries generates playlists for all identified series.
//
// NOTE: The legacy implementation read from the global SQLite database.DB
// which was removed in fable5 TASK-022. This function now returns an error
// to avoid silent failures. Use the Store-backed playlist API
// (server/handlers/playlists.go) for production workflows.
func GeneratePlaylistsForSeries() error {
	slog.Warn("GeneratePlaylistsForSeries: legacy SQLite path removed in fable5 T022; use Store-backed playlist API")
	return fmt.Errorf("GeneratePlaylistsForSeries: the legacy SQLite path was removed in fable5 T022; use the Store-backed playlist API instead")
}
