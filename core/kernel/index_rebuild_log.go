// file: core/kernel/index_rebuild_log.go
// version: 0.1.0
// guid: 6e0b2a97-4d13-4c58-b8f4-1c9a7d5e3f20
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// The books search-index rebuild's log lines. They are a contract with the
// F3-07 rehearsal, which gates on them (f7-09-inputs.md section 4a item 4):
// the messages and the attribute names are fixed, and the fold (B3-a) logs them
// through logIndexRebuild, so a word cannot drift between the two.
//
// The rebuild runs after READY, as a queued task, so search is degraded until
// "finished" and the rebuild does not hold the start timeout: logIndexRebuild
// is called from the build task's Run, never from a boot step.
const (
	// IndexRebuildStarted: attrs generation (string, the index's generation
	// token at the start) and books (int64, the documents expected).
	IndexRebuildStarted = "books: search index rebuild started"
	// IndexRebuildFinished: attrs generation, docs (int64, the documents the
	// rebuild wrote) and elapsed_ms (int64).
	IndexRebuildFinished = "books: search index rebuild finished"
	// IndexRebuildFailed: attrs generation and err (the error).
	IndexRebuildFailed = "books: search index rebuild failed"
)

// logIndexRebuild runs rebuild between the started line and the finished or
// failed one, and returns the rebuild's own error unchanged. expected is the
// documents the rebuild is about to write; now is the clock of elapsed_ms (nil
// is time.Now). The rebuild reports how many documents it wrote.
func logIndexRebuild(ctx context.Context, log *slog.Logger, gen storage.Generation, expected int64, now func() time.Time,
	rebuild func(ctx context.Context) (docs int64, err error)) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	g := gen.String()
	log.InfoContext(ctx, IndexRebuildStarted, "generation", g, "books", expected)
	began := now()
	docs, err := rebuild(ctx)
	if err != nil {
		log.ErrorContext(ctx, IndexRebuildFailed, "generation", g, "err", err)
		return err
	}
	log.InfoContext(ctx, IndexRebuildFinished, "generation", g, "docs", docs, "elapsed_ms", now().Sub(began).Milliseconds())
	return nil
}
