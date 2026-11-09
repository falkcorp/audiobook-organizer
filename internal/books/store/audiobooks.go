// file: internal/books/store/audiobooks.go
// version: 2.0.1
// guid: 7f8a9b0c-1d2e-3f4a-5b6c-7d8e9f0a1b2c
// last-edited: 2026-11-09

// Package database — this file previously contained package-level
// GetAudiobooks / GetAudiobookByID / UpdateAudiobook / DeleteAudiobook
// functions that queried the global *sql.DB. All of those operations are
// implemented on PebbleStore. The SQLite global-DB wrappers were removed
// in fable5 TASK-022.

package store
