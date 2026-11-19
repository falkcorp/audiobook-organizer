// file: internal/plugins/dedup/full_scan.go
// version: 2.3.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-11-18

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

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
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
	remaining := max(total-done, 0)
	etaSeconds := float64(remaining) / rate
	eta := time.Duration(etaSeconds * float64(time.Second))
	return fmt.Sprintf(" (%.1f books/sec, ~%s remaining)", rate, eta.Round(time.Second))
}

func (p *Plugin) fullScanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.full-scan",
		Liveness:        sdk.LivenessRunItems,
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

// fullScanKind is rule R3's native kind for dedup.full-scan.
const fullScanKind = tasks.Kind("books.dedup.full-scan")

// fullScanEngine is the engine surface the core takes. FullScan hosts the
// bounded worker pools (Layer-1 and the unified-scoring pass, both
// registry.RunItems at runtime.NumCPU(), internal/dedup/engine.go), so this core
// has no item loop of its own.
type fullScanEngine interface {
	purgeStaleEngine
	FullScan(ctx context.Context, progress func(phase string, done, total int)) error
}

// fullScanStore is the reads the core takes.
type fullScanStore interface {
	// LSHIndexBuilt reports whether the LSH index is built and whether the store
	// carries that flag at all (available).
	LSHIndexBuilt() (built, available bool)
	// ListCandidates is the candidate count read for the completion message.
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
}

// fullScanEnv is what the core takes from its shell.
type fullScanEnv struct {
	kindEnv
	Engine fullScanEngine
	Store  fullScanStore
}

// fullScanResult is the native run's result.
type fullScanResult struct {
	PendingCandidates int `json:"pending_candidates"`
}

// fullScanCore is the body of runFullScan, shared by both shells.
func fullScanCore(ctx context.Context, env fullScanEnv) (fullScanResult, error) {
	// Phase 1 — Hygiene: purge stale candidates before the scan so the
	// index phase starts with a clean candidate table.
	startProg := newProgress(env.kindEnv, 0)
	startProg.Start("Purging stale candidates...")
	if deleted, err := env.Engine.PurgeStaleCandidates(ctx); err != nil {
		env.Logger.Error("purge stale candidates error", "error", err)
	} else if deleted > 0 {
		env.Logger.Info("purged stale candidates before scan", "count", deleted)
	}

	// Phase 2 — Index + score. FullScan reports (phase, done, total); one
	// *sdk.Progress per phase, built lazily on that phase's first callback.
	var (
		prog      *sdk.Progress
		scoreProg *sdk.Progress

		scanStart  time.Time
		scoreStart time.Time
	)
	fullScanErr := env.Engine.FullScan(ctx, func(phase string, done, total int) {
		if total <= 0 {
			return
		}
		switch phase {
		case "scan":
			if prog == nil {
				prog = newProgress(env.kindEnv, total)
				prog.Start(fmt.Sprintf("Scanning books: 0 / %d", total))
				scanStart = time.Now()
			}
			prog.StepN(done, fmt.Sprintf("Scanning books: %d / %d%s", done, total, etaSuffix(scanStart, done, total)))
		case "score":
			if scoreProg == nil {
				scoreProg = newProgress(env.kindEnv, total)
				scoreProg.Start(fmt.Sprintf("Composing scores: 0 / %d", total))
				scoreStart = time.Now()
			}
			scoreProg.StepN(done, fmt.Sprintf("Composing scores: %d / %d%s", done, total, etaSuffix(scoreStart, done, total)))
		}
	})
	if fullScanErr != nil {
		env.Logger.Error("FullScan error", "error", fullScanErr)
		return fullScanResult{}, fmt.Errorf("dedup scan: %w", fullScanErr)
	}

	if prog == nil {
		prog = newProgress(env.kindEnv, 0)
		prog.Start("Scanning books: 0 / 0")
	}
	if scoreProg == nil {
		scoreProg = newProgress(env.kindEnv, 0)
		scoreProg.Start("Composing scores: 0 / 0")
	}
	scoreProg.Finalize("scoring complete")
	scoreProg.Done("Composing scores complete")

	// Phase 3 — LSH candidates: surface the skip reason in the operation log.
	// The collector already self-gates; the engine's FullScan invoked it when
	// the index is built.
	if built, ok := env.Store.LSHIndexBuilt(); ok && !built {
		env.Logger.Info(
			"full-scan: LSH phase skipped — index not yet built",
			"hint", "run dedup.lsh-index-build to enable sub-linear AcoustID matching",
		)
	}

	pendingCount := 0
	filter := database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 1}
	if _, total, listErr := env.Store.ListCandidates(filter); listErr == nil {
		pendingCount = total
	}
	prog.Finalize("writing results...")
	prog.Done(fmt.Sprintf("Dedup scan complete — %d pending candidates", pendingCount))
	return fullScanResult{PendingCandidates: pendingCount}, nil
}

// runFullScan is the v2 shell: HEAD's Run over fullScanCore.
func (p *Plugin) runFullScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}
	_, err := fullScanCore(ctx, fullScanEnv{
		kindEnv: headEnv(reporter, true),
		Engine:  p.engine,
		Store:   headScoringReads{store: p.store, emb: p.embeddingStore},
	})
	return err
}
