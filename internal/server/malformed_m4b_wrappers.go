// file: internal/server/malformed_m4b_wrappers.go
// version: 1.4.0
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-09-14

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
	// Every remuxed file's book_file row learns the new bytes' hashes, or the
	// next rescan treats the file as replaced.
	remuxer.SetBookFileStore(s.store)
	// Protected files (Deluge save paths, the iTunes library) are skipped:
	// the walk covers all of RootDir, and a protected directory can sit
	// under it. Same predicate the tag-write guard uses.
	remuxer.SetProtectedChecker(s.protectedChecker())
	return remuxer.RemuxMalformedFiles(ctx, progress)
}

// transcodeMalformedM4BFiles is a thin wrapper that delegates to the remux package.
// ctx lets the library walk stop early on shutdown (SYS-1). See
// remuxMalformedM4BFiles for the progress/error threading rationale (C2).
func (s *Server) transcodeMalformedM4BFiles(ctx context.Context, progress func(processed, total int, msg string)) error {
	transcoder := remux.NewTranscoder(s.store)
	// Hashes plus codec, bitrate and sample rate, which a transcode changes.
	transcoder.SetBookFileStore(s.store)
	transcoder.SetProtectedChecker(s.protectedChecker())
	return transcoder.TranscodeMalformedFiles(ctx, progress)
}
