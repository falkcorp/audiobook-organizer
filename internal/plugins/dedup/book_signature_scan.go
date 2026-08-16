// file: internal/plugins/dedup/book_signature_scan.go
// version: 1.0.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-05-06

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

func (p *Plugin) runBookSignatureScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}

	startProg := sdk.NewProgress(reporter, 0)
	startProg.Start("Starting book signature scan...")

	var prog *sdk.Progress
	scanErr := p.engine.BookSignatureScan(ctx, func(done, total int) {
		if total <= 0 {
			return
		}
		if prog == nil {
			prog = sdk.NewProgress(reporter, total)
			prog.Start(fmt.Sprintf("Scanning books: 0 / %d", total))
		}
		prog.StepN(done, fmt.Sprintf("Scanning books: %d / %d", done, total))
	})
	if scanErr != nil {
		reporter.Logger().Error("BookSignatureScan error", "error", scanErr)
		return fmt.Errorf("book signature scan: %w", scanErr)
	}

	if prog == nil {
		prog = sdk.NewProgress(reporter, 0)
		prog.Start("Scanning books: 0 / 0")
	}

	pendingCount := 0
	if p.embeddingStore != nil {
		filter := database.CandidateFilter{EntityType: "book", Status: "pending", Layer: "book_signature", Limit: 1}
		if _, total, listErr := p.embeddingStore.ListCandidates(filter); listErr == nil {
			pendingCount = total
		}
	}
	prog.Finalize("writing results...")
	prog.Done(fmt.Sprintf("Book signature scan complete — %d pending candidate(s)", pendingCount))
	return nil
}
