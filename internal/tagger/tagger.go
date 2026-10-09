// file: internal/tagger/tagger.go
// version: 1.5.0
// guid: 3b4c5d6e-7f8a-9b0c-1d2e-3f4a5b6c7d8e
// last-edited: 2026-10-09

package tagger

import "fmt"

// UpdateSeriesTags updates the audio files with series metadata tags.
// NOTE: This function used the legacy global database.DB (SQLite) which was
// removed in fable5 TASK-022. Use the tag-writing pipeline via the Store API
// (server/handlers/tags.go) for production workflows.
func UpdateSeriesTags() error {
	return fmt.Errorf("UpdateSeriesTags: the legacy SQLite path was removed in fable5 T022; use the Store-backed tag-write pipeline instead")
}
