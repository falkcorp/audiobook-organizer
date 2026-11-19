// file: internal/plugins/dedup/book_signature_scan.go
// version: 1.1.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-11-18

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

func (p *Plugin) bookSignatureScanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.book-signature-scan",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "dedup",
		DisplayName:     "Book signature scan",
		Description:     "Runs a unified per-book fingerprint scan comparing book signatures.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		Timeout:         120 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runBookSignatureScan,
	}
}

// bookSignatureScanKind is rule R3's native kind for dedup.book-signature-scan.
const bookSignatureScanKind = tasks.Kind("books.dedup.book-signature-scan")

// bookSignatureScanEngine is the one engine call the core makes. The scan's
// O(n^2) outer loop is sharded across runtime.NumCPU() workers inside the engine
// (internal/dedup/engine.go, CONC-1), so this core has no item loop of its own.
type bookSignatureScanEngine interface {
	BookSignatureScan(ctx context.Context, progress func(done, total int)) error
}

// bookSignatureScanStore is the read the completion message takes.
type bookSignatureScanStore interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
}

type bookSignatureScanEnv struct {
	kindEnv
	Engine bookSignatureScanEngine
	Store  bookSignatureScanStore
}

// bookSignatureScanResult is the native run's result.
type bookSignatureScanResult struct {
	PendingCandidates int `json:"pending_candidates"`
}

// bookSignatureScanCore is the body of runBookSignatureScan, shared by both shells.
func bookSignatureScanCore(ctx context.Context, env bookSignatureScanEnv) (bookSignatureScanResult, error) {
	startProg := newProgress(env.kindEnv, 0)
	startProg.Start("Starting book signature scan...")

	var prog *sdk.Progress
	scanErr := env.Engine.BookSignatureScan(ctx, func(done, total int) {
		if total <= 0 {
			return
		}
		if prog == nil {
			prog = newProgress(env.kindEnv, total)
			prog.Start(fmt.Sprintf("Scanning books: 0 / %d", total))
		}
		prog.StepN(done, fmt.Sprintf("Scanning books: %d / %d", done, total))
	})
	if scanErr != nil {
		env.Logger.Error("BookSignatureScan error", "error", scanErr)
		return bookSignatureScanResult{}, fmt.Errorf("book signature scan: %w", scanErr)
	}

	if prog == nil {
		prog = newProgress(env.kindEnv, 0)
		prog.Start("Scanning books: 0 / 0")
	}

	pendingCount := 0
	filter := database.CandidateFilter{EntityType: "book", Status: "pending", Layer: "book_signature", Limit: 1}
	if _, total, listErr := env.Store.ListCandidates(filter); listErr == nil {
		pendingCount = total
	}
	prog.Finalize("writing results...")
	prog.Done(fmt.Sprintf("Book signature scan complete — %d pending candidate(s)", pendingCount))
	return bookSignatureScanResult{PendingCandidates: pendingCount}, nil
}

// runBookSignatureScan is the v2 shell: HEAD's Run over bookSignatureScanCore.
func (p *Plugin) runBookSignatureScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}
	_, err := bookSignatureScanCore(ctx, bookSignatureScanEnv{
		kindEnv: headEnv(reporter, true),
		Engine:  p.engine,
		Store:   headScoringReads{store: p.store, emb: p.embeddingStore},
	})
	return err
}
