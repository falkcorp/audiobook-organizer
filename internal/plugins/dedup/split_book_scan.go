// file: internal/plugins/dedup/split_book_scan.go
// version: 1.1.0
// guid: 4c6e8f0b-3a5b-7c9d-1e2f-4a6b8c0d2e3f
// last-edited: 2026-07-17

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// splitBookScanDef registers the split-book backfill scan op. It walks
// every book in the store, runs the chapter-cluster detector, and
// persists the resulting candidates to the Pebble split-book keyspace
// so the HTTP list endpoint and the merge-split-books CLI can consume
// them.
func (p *Plugin) splitBookScanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.split-book-scan",
		Plugin:          "dedup",
		DisplayName:     "Split-book backfill scan",
		Description:     "Scans the library for one-Book-per-chapter clusters and saves merge candidates.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.split-book-scan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
		},
		Run: p.runSplitBookScan,
	}
}

func (p *Plugin) runSplitBookScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("split-book scan: store not available")
	}
	if p.embeddingStore == nil {
		return fmt.Errorf("split-book scan: embedding store not available (needed for shared Pebble DB)")
	}

	scanProg := sdk.NewProgress(reporter, 0)
	scanProg.Start("Scanning library for split-book clusters...")
	// Book-loading progress: total is unknown up front, so the callback
	// updates the indeterminate frame's message (and the op log) every
	// 1000 books instead of a percentage.
	cands, err := dedupengine.DetectSplitBookCandidates(ctx, p.store, func(loaded int) {
		scanProg.StepN(0, fmt.Sprintf("Loading books: %d loaded...", loaded))
		if loaded%10000 == 0 {
			reporter.Logger().Info("split-book scan: loading books", "loaded", loaded)
		}
	})
	if err != nil {
		reporter.Logger().Error("split-book detector error", "error", err)
		return fmt.Errorf("split-book scan: %w", err)
	}

	prog := sdk.NewProgress(reporter, len(cands))
	prog.Start(fmt.Sprintf("Found %d candidate cluster(s); persisting...", len(cands)))

	db := p.embeddingStore.PebbleDB()
	if db == nil {
		return fmt.Errorf("split-book scan: shared Pebble DB unavailable")
	}
	store := dedupengine.NewSplitBookStore(db)
	if err := store.SaveAll(cands); err != nil {
		reporter.Logger().Error("split-book SaveAll error", "error", err)
		return fmt.Errorf("split-book scan persist: %w", err)
	}

	prog.StepN(len(cands), fmt.Sprintf("Persisted %d / %d cluster(s)", len(cands), len(cands)))
	prog.Finalize("writing results...")
	prog.Done(fmt.Sprintf("Split-book scan complete — %d candidate cluster(s) saved", len(cands)))
	reporter.Logger().Info("split-book scan complete", "candidates", len(cands))
	return nil
}
