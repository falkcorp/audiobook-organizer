// file: internal/server/handlers/interfaces.go
// version: 1.3.0
// guid: e5f6a7b8-c9d0-1234-5678-90abcdef0123
// last-edited: 2026-10-07

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

// FileIOPool is the narrow *server.FileIOPool subset used by handlers that
// schedule slow file I/O (cover-art embedding, audio tag writes, renames) off
// the HTTP request path. Only Submit is used.
//
// Mirrors metadatahandler.FileIOPool; both exist so neither package has to
// import the other. Callers must guard against typed-nil boxing at wire time
// (see wire_handlers.go) so the `pool != nil` checks in handlers stay honest.
type FileIOPool interface {
	Submit(bookID string, fn func()) bool
}
