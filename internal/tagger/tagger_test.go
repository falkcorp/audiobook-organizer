// file: internal/tagger/tagger_test.go
// version: 2.1.0
// guid: 8c9d0e1f-2a3b-4c5d-6e7f-8a9b0c1d2e3f
// last-edited: 2026-10-09

// NOTE(fable5 T022): setupTaggerDB and TestUpdateSeriesTags were removed;
// they tested the SQLite-backed UpdateSeriesTags path which was removed in
// fable5 T022. The placeholder file-format tests (updateFileTags and its
// per-format helpers) went with those helpers on 2026-10-09 (01-P6).

package tagger

import (
	"testing"
)

// TestUpdateSeriesTagsReturnsError verifies that the legacy SQLite-backed
// UpdateSeriesTags path (removed in fable5 T022) now returns an error
// with a clear migration message.
func TestUpdateSeriesTagsReturnsError(t *testing.T) {
	if err := UpdateSeriesTags(); err == nil {
		t.Error("expected UpdateSeriesTags to return an error after SQLite removal")
	}
}
