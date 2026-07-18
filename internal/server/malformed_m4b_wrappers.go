// file: internal/server/malformed_m4b_wrappers.go
// version: 1.2.0
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-07-18

package server

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/remux"
)

// remuxMalformedM4BFiles is a thin wrapper that delegates to the remux package.
// ctx lets the library walk stop early on shutdown (SYS-1). progress and the
// error return are threaded straight through to the op reporter (C2) so a
// multi-hour ffmpeg walk surfaces live progress and can fail the op on a
// fatal setup problem instead of silently completing.
func (s *Server) remuxMalformedM4BFiles(ctx context.Context, progress func(processed, total int, msg string)) error {
	remuxer := remux.New(s.store)
	return remuxer.RemuxMalformedFiles(ctx, progress)
}

// transcodeMalformedM4BFiles is a thin wrapper that delegates to the remux package.
// ctx lets the library walk stop early on shutdown (SYS-1). See
// remuxMalformedM4BFiles for the progress/error threading rationale (C2).
func (s *Server) transcodeMalformedM4BFiles(ctx context.Context, progress func(processed, total int, msg string)) error {
	transcoder := remux.NewTranscoder(s.store)
	return transcoder.TranscodeMalformedFiles(ctx, progress)
}
