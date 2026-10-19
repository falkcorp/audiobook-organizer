// file: internal/server/server_test_seams_test.go
// version: 1.0.0
// guid: 2e7b4c91-5d38-4a6f-8b0e-1c9d3f5a7e64
// last-edited: 2026-10-18

package server

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// These helpers reach package-private state that production code never touches
// through these entry points. They used to sit in non-test files, where
// nothing but tests called them, so they were compiled into the server binary
// for no caller.

// applyCachedCandidateForBook is applyCachedCandidateForBookTimed with a fresh
// phase-timing recorder and none of the batch op's claim index, candidate pin or
// bulk mode: the plain per-book apply the tests drive.
func applyCachedCandidateForBook(
	svc cachedApplyService,
	books bookReader,
	id string,
	writeBack bool,
	checkpoint func() error,
) applyOutcome {
	return applyCachedCandidateForBookTimed(context.Background(), svc, books, id, writeBack, checkpoint, metafetch.NewApplyPhaseTimings(), nil, nil, "")
}

// setSearchIndex replaces the server's search index, so a test can wire a
// pre-built Bleve index without running the real Start() lifecycle, which opens
// the index itself.
func (s *Server) setSearchIndex(idx *search.BleveIndex) {
	s.searchIndex = idx
}

// setTrace installs (or, with nil, removes) the table's trace hook.
func (pl *pathLocks) setTrace(fn func(event string)) {
	pl.mu.Lock()
	pl.trace = fn
	pl.mu.Unlock()
}

// SearchIndexDroppedCount reports index events dropped since process start.
func SearchIndexDroppedCount() int64 { return searchIndexDropped.Load() }
