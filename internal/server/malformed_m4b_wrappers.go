// file: internal/server/malformed_m4b_wrappers.go
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-07-03

package server

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/remux"
)

// remuxMalformedM4BFiles is a thin wrapper that delegates to the remux package.
// ctx lets the library walk stop early on shutdown (SYS-1).
func (s *Server) remuxMalformedM4BFiles(ctx context.Context) {
	remuxer := remux.New(s.store)
	remuxer.RemuxMalformedFiles(ctx)
}

// transcodeMalformedM4BFiles is a thin wrapper that delegates to the remux package.
// ctx lets the library walk stop early on shutdown (SYS-1).
func (s *Server) transcodeMalformedM4BFiles(ctx context.Context) {
	transcoder := remux.NewTranscoder(s.store)
	transcoder.TranscodeMalformedFiles(ctx)
}
