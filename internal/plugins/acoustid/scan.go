// file: internal/plugins/acoustid/scan.go
// version: 1.0.0
// guid: e5f6a7b8-c9d0-1234-def0-123456789abc
// last-edited: 2026-05-06

package acoustid

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) scanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "acoustid.scan",
		Liveness:        sdk.LivenessManual,
		Plugin:          "acoustid",
		DisplayName:     "AcoustID fingerprint scan",
		Description:     "Runs AcoustID fingerprint-based dedup scan comparing acoustic fingerprints.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "acoustid.fingerprint",
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         6 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapFilesRead,
			sdk.CapFilesExecute,
			sdk.CapSubprocessSpawn,
		},
		Run: p.runAcoustIDScan,
	}
}

func (p *Plugin) runAcoustIDScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}

	_ = reporter.UpdateProgress(0, 1, "Starting AcoustID fingerprint scan...")

	// Pass REAL done/total instead of remapping to 0..100. The UI computes
	// the percentage with 2-decimal precision, so a 49915-book scan shows
	// "551 / 49915 (1.10%)" instead of being welded to "1 / 100 (1%)" for
	// hundreds of books at a time. The dedup-by-percent throttle (which
	// was hiding real progress) is replaced by the engine's existing
	// every-50-books emit cadence.
	scanErr := p.engine.AcoustIDScan(ctx, func(done, total int) {
		if total <= 0 {
			return
		}
		_ = reporter.UpdateProgress(done, total, fmt.Sprintf("Scanning books: %d / %d", done, total))
	})
	if scanErr != nil {
		reporter.Logger().Error("AcoustIDScan error", "error", scanErr)
		return fmt.Errorf("acoustid scan: %w", scanErr)
	}

	pendingCount := 0
	if p.embeddingStore != nil {
		filter := database.CandidateFilter{EntityType: "book", Status: "pending", Layer: "acoustid", Limit: 1}
		if _, total, listErr := p.embeddingStore.ListCandidates(filter); listErr == nil {
			pendingCount = total
		}
	}
	_ = reporter.UpdateProgress(1, 1,
		fmt.Sprintf("AcoustID scan complete — %d pending candidate(s)", pendingCount))
	return nil
}
