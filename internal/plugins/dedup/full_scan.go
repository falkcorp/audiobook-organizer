// file: internal/plugins/dedup/full_scan.go
// version: 2.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-04

// T018: full_scan.go enforces phase ordering for the full dedup scan:
//
//  1. Hygiene: purge stale candidates (always).
//  2. Index (embedding+exact): FullScan via engine.
//  3. LSH candidates: CollectLSHAcoustID — only runs when the LSH index
//     exists (lsh_index_v1_done flag). When absent, a log line explains
//     how to enable it.
//
// The LSH gate in runFullScan is an op-level assertion complementing the
// collector-level gate already present in CollectLSHAcoustID. The op-level
// check lets the reporter surface a user-visible skip message in the
// operation log so operators know the LSH phase was skipped and why.

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// LSHFlagStore is the narrow interface used by runFullScan to assert whether
// the LSH index has been built before emitting the LSH-phase log line.
// *database.PebbleStore satisfies this interface. Other store implementations
// (SQLite, mocks) that do not carry an LSH index should return false.
type LSHFlagStore interface {
	IsLSHIndexBuilt() bool
}

// etaSuffix renders a rough "(N.N books/sec, ~T remaining)" suffix for a
// progress message, computed from how long the phase has been running and
// how many of its items are done. It exists to answer "give us an ETA" for
// the previously-silent unified-scoring pass, which can run for tens of
// minutes on a large library with zero other feedback. Returns "" until at
// least one item has completed (rate is undefined at done==0) or if elapsed
// time is effectively zero (avoids a divide-by-near-zero rate spike right
// after Start).
func etaSuffix(phaseStart time.Time, done, total int) string {
	if done <= 0 || phaseStart.IsZero() {
		return ""
	}
	elapsed := time.Since(phaseStart)
	if elapsed <= 0 {
		return ""
	}
	rate := float64(done) / elapsed.Seconds()
	if rate <= 0 {
		return ""
	}
	remaining := total - done
	if remaining < 0 {
		remaining = 0
	}
	etaSeconds := float64(remaining) / rate
	eta := time.Duration(etaSeconds * float64(time.Second))
	return fmt.Sprintf(" (%.1f books/sec, ~%s remaining)", rate, eta.Round(time.Second))
}

func (p *Plugin) fullScanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.full-scan",
		Plugin:          "dedup",
		DisplayName:     "Full dedup scan",
		Description:     "Runs a full embedding-based dedup scan, purging stale candidates first.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.full-scan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapNetworkOpenAI,
		},
		Run: p.runFullScan,
	}
}

func (p *Plugin) runFullScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}

	// Phase 1 — Hygiene: purge stale candidates before the scan so the
	// index phase starts with a clean candidate table.
	startProg := sdk.NewProgress(reporter, 0)
	startProg.Start("Purging stale candidates...")
	if deleted, err := p.engine.PurgeStaleCandidates(ctx); err != nil {
		reporter.Logger().Error("purge stale candidates error", "error", err)
	} else if deleted > 0 {
		reporter.Logger().Info("purged stale candidates before scan", "count", deleted)
	}

	// Phase 2 — Index + score: run exact + embedding collectors for every
	// primary book ("scan" phase), then compose the unified score for every
	// book ("score" phase). FullScan reports progress via a callback
	// (phase, done, total) — both phases iterate over all books and used to
	// report independently, but the "score" phase previously reported
	// nothing at all, leaving the operation log silent for the CPU-heavy
	// composite-scoring pass (observed: 25+ minutes silent on a ~29K-book
	// library). Build a separate *sdk.Progress tracker per phase, lazily on
	// that phase's first callback, same pattern as the original single
	// tracker.
	var (
		prog      *sdk.Progress
		scoreProg *sdk.Progress

		scanStart  time.Time
		scoreStart time.Time
	)
	fullScanErr := p.engine.FullScan(ctx, func(phase string, done, total int) {
		if total <= 0 {
			return
		}
		switch phase {
		case "scan":
			if prog == nil {
				prog = sdk.NewProgress(reporter, total)
				prog.Start(fmt.Sprintf("Scanning books: 0 / %d", total))
				scanStart = time.Now()
			}
			prog.StepN(done, fmt.Sprintf("Scanning books: %d / %d%s", done, total, etaSuffix(scanStart, done, total)))
		case "score":
			if scoreProg == nil {
				scoreProg = sdk.NewProgress(reporter, total)
				scoreProg.Start(fmt.Sprintf("Composing scores: 0 / %d", total))
				scoreStart = time.Now()
			}
			scoreProg.StepN(done, fmt.Sprintf("Composing scores: %d / %d%s", done, total, etaSuffix(scoreStart, done, total)))
		}
	})
	if fullScanErr != nil {
		reporter.Logger().Error("FullScan error", "error", fullScanErr)
		return fmt.Errorf("dedup scan: %w", fullScanErr)
	}

	if prog == nil {
		prog = sdk.NewProgress(reporter, 0)
		prog.Start("Scanning books: 0 / 0")
	}
	if scoreProg == nil {
		scoreProg = sdk.NewProgress(reporter, 0)
		scoreProg.Start("Composing scores: 0 / 0")
	}
	scoreProg.Finalize("scoring complete")
	scoreProg.Done("Composing scores complete")

	// Phase 3 — LSH candidates: assert that the LSH index has been built
	// before the engine's CollectLSHAcoustID collector runs. The collector
	// already self-gates (it calls IsLSHIndexBuilt() internally), but we
	// surface the skip reason here so it appears in the operation log and
	// is visible to operators.
	if flagStore, ok := p.store.(LSHFlagStore); ok {
		if !flagStore.IsLSHIndexBuilt() {
			reporter.Logger().Info(
				"full-scan: LSH phase skipped — index not yet built",
				"hint", "run dedup.lsh-index-build to enable sub-linear AcoustID matching",
			)
		}
		// If built, no-op: the engine's FullScan already invoked the LSH
		// collector via runUnifiedScoringForBook for each book.
	}

	// Fetch final candidate counts for the completion message.
	pendingCount := 0
	if p.embeddingStore != nil {
		filter := database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 1}
		if _, total, listErr := p.embeddingStore.ListCandidates(filter); listErr == nil {
			pendingCount = total
		}
	}
	prog.Finalize("writing results...")
	prog.Done(fmt.Sprintf("Dedup scan complete — %d pending candidates", pendingCount))
	return nil
}
