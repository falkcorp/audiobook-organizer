// file: internal/server/file_work_seams.go
// version: 1.0.0
// guid: 38879fed-3d78-4eb6-a109-a6d40cb43d9c
// last-edited: 2026-10-10

package server

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// writeBackBookFiles and finishApplyFileWork are the server's calls into
// metafetch's per-book file work. They are package variables only so a test
// can see the context each bulk entry point hands down: the bulk ops wrap it
// with tagger.WithoutBackup (owner decision D69), and a dropped wrap leaves a
// .bak-* copy beside every file the op touches, which no other test notices.
// Production never reassigns them.
var (
	writeBackBookFiles = func(mfs *metafetch.Service, ctx context.Context, bookID string) (int, error) {
		return mfs.WriteBackMetadataForBookContext(ctx, bookID)
	}
	finishApplyFileWork = func(mfs *metafetch.Service, ctx context.Context, bookID, pendingCoverURL string, fileIO, writeTags bool, checkpoint func() error) error {
		return mfs.FinishApplyFileWork(ctx, bookID, pendingCoverURL, fileIO, writeTags, checkpoint)
	}
)
