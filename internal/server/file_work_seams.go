// file: internal/server/file_work_seams.go
// version: 1.0.1
// guid: 38879fed-3d78-4eb6-a109-a6d40cb43d9c
// last-edited: 2026-11-18

package server

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// writeBackBookFiles and finishApplyFileWork are the server's calls into
// metafetch's per-book file work. Each takes the job's intent, which every
// caller here states as metafetch.Unattended (a bulk write-back, a batch save,
// apply-when-scanned): the attended callers, the single-book dialog and the
// single-book write-back, reach metafetch through the handler interfaces, not
// these seams. Taking it as a parameter keeps each caller's choice visible to
// a test that records it. They are package variables only so a test
// can see the context each bulk entry point hands down: the bulk ops wrap it
// with tagger.WithoutBackup (owner decision D69), and a dropped wrap leaves a
// .bak-* copy beside every file the op touches, which no other test notices.
// Production never reassigns them.
var (
	writeBackBookFiles = func(mfs *metafetch.Service, ctx context.Context, bookID string, intent metafetch.FetchIntent) (int, error) {
		return mfs.WriteBackMetadataForBookContext(ctx, bookID, intent)
	}
	finishApplyFileWork = func(mfs *metafetch.Service, ctx context.Context, bookID, pendingCoverURL string, fileIO, writeTags bool, intent metafetch.FetchIntent, checkpoint func() error) error {
		return mfs.FinishApplyFileWork(ctx, bookID, pendingCoverURL, fileIO, writeTags, intent, checkpoint)
	}
)
