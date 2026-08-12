// file: internal/server/handlers/interfaces.go
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1234-5678-90abcdef0123
// last-edited: 2026-08-11

package handlers

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/plugin"
)

// EventPublisher is the narrow interface for publishing domain events to the plugin bus.
// Used by handlers that trigger side effects visible to plugins.
type EventPublisher interface {
	Publish(ctx context.Context, event plugin.Event)
}

// WriteBackEnqueuer is the narrow interface for enqueuing book write-back jobs.
//
// NOTE: the concrete implementation wired in production is
// *itunesservice.WriteBackBatcher — it syncs the book to the iTunes library.
// It is NOT the audio-tag writer. Writing tags into the audio files themselves
// goes through metafetch.Service.ApplyMetadataFileIO /
// WriteBackMetadataForBook, scheduled off the request path via FileIOPool.
// Conflating the two is what caused the Metadata Review screen to update only
// the database and never the files (fix/review-apply-writes-tags).
type WriteBackEnqueuer interface {
	Enqueue(bookID string)
}

// FileIOPool is the narrow *server.FileIOPool subset used by handlers that
// schedule slow file I/O (cover-art embedding, audio tag writes, renames) off
// the HTTP request path. Only Submit is used.
//
// Mirrors metadatahandler.FileIOPool; both exist so neither package has to
// import the other. Callers must guard against typed-nil boxing at wire time
// (see wire_handlers.go) so the `pool != nil` checks in handlers stay honest.
type FileIOPool interface {
	Submit(bookID string, fn func())
}
