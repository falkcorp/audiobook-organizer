// file: internal/server/handlers/interfaces.go
// version: 1.3.1
// guid: e5f6a7b8-c9d0-1234-5678-90abcdef0123
// last-edited: 2026-11-18

package handlers

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
)

// EventPublisher is the narrow interface for publishing domain events to the plugin bus.
// Used by handlers that trigger side effects visible to plugins.
type EventPublisher interface {
	Publish(ctx context.Context, event plugin.Event)
}

// FileIOPool is the narrow *server.FileIOPool subset used by handlers that
// schedule slow file I/O (cover-art embedding, audio tag writes, renames) off
// the HTTP request path. Only Submit is used. Its intent is whether a person
// asked for the job's file work on this one book (metafetch.Interactive) or a
// batch did (metafetch.Unattended); the pool persists it with the recovery
// record so a restart replays the job with it.
//
// Mirrors metadatahandler.FileIOPool; both exist so neither package has to
// import the other. Callers must guard against typed-nil boxing at wire time
// (see wire_handlers.go) so the `pool != nil` checks in handlers stay honest.
type FileIOPool interface {
	Submit(bookID string, intent metafetch.FetchIntent, fn func()) bool
}
