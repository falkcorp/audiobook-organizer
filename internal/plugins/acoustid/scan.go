// file: internal/plugins/acoustid/scan.go
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1234-def0-123456789abc
// last-edited: 2026-11-19

package acoustid

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// scanKind is rule R3's native kind for acoustid.scan.
const scanKind = tasks.Kind("books.acoustid.scan")

// scanHelp is the definition's description, HEAD's and the native kind's Help
// (rule R5: verbatim).
const scanHelp = "Runs AcoustID fingerprint-based dedup scan comparing acoustic fingerprints."

// scanEngine is the engine surface the scan core takes: the one method HEAD's
// body called on p.engine, with its signature. The engine writes the candidate
// and labeled-example rows through its own store, which in a native run is
// built over the run's gated writer by KindDeps.NewEngine (seams.d/F5-05.md).
type scanEngine interface {
	AcoustIDScan(ctx context.Context, progress func(done, total int)) error
}

// scanEnv is what the scan core takes from its shell (rules R2 and R10).
// Candidates is nil when the plugin has no embedding store.
type scanEnv struct {
	kindEnv
	Engine     scanEngine
	Candidates candidateReads
}

// scanResult is the native run's result: the pending acoustid candidates the
// scan left. HEAD stored no result, so its shell drops it.
type scanResult struct {
	PendingCandidates int `json:"pending_candidates"`
}

func (p *Plugin) scanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "acoustid.scan",
		Liveness:        sdk.LivenessManual,
		Plugin:          "acoustid",
		DisplayName:     "AcoustID fingerprint scan",
		Description:     scanHelp,
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
	_, err := scanCore(ctx, p.headScanEnv(reporter))
	return err
}

// headScanEnv is HEAD's env for the scan. A plugin with no engine or no
// embedding store hands the core a nil interface (not a typed-nil pointer in
// one), which the core refuses (engine) or skips (candidates) as HEAD did.
func (p *Plugin) headScanEnv(reporter sdk.Reporter) scanEnv {
	env := scanEnv{kindEnv: headEnv(reporter, true)}
	if p.engine != nil {
		env.Engine = p.engine
	}
	if p.embeddingStore != nil {
		env.Candidates = p.embeddingStore
	}
	return env
}

// scanCore is the body of runAcoustIDScan, shared by both shells.
func scanCore(ctx context.Context, env scanEnv) (scanResult, error) {
	if env.Engine == nil {
		return scanResult{}, fmt.Errorf("dedup engine not available")
	}

	env.Progress(0, 1, "Starting AcoustID fingerprint scan...")

	// Pass REAL done/total instead of remapping to 0..100. The UI computes
	// the percentage with 2-decimal precision, so a 49915-book scan shows
	// "551 / 49915 (1.10%)" instead of being welded to "1 / 100 (1%)" for
	// hundreds of books at a time. The dedup-by-percent throttle (which
	// was hiding real progress) is replaced by the engine's existing
	// every-50-books emit cadence.
	scanErr := env.Engine.AcoustIDScan(ctx, func(done, total int) {
		if total <= 0 {
			return
		}
		env.Progress(done, total, fmt.Sprintf("Scanning books: %d / %d", done, total))
	})
	if scanErr != nil {
		env.Logger.Error("AcoustIDScan error", "error", scanErr)
		return scanResult{}, fmt.Errorf("acoustid scan: %w", scanErr)
	}

	pendingCount := 0
	if env.Candidates != nil {
		filter := database.CandidateFilter{EntityType: "book", Status: "pending", Layer: "acoustid", Limit: 1}
		if _, total, listErr := env.Candidates.ListCandidates(filter); listErr == nil {
			pendingCount = total
		}
	}
	env.Progress(1, 1,
		fmt.Sprintf("AcoustID scan complete — %d pending candidate(s)", pendingCount))
	return scanResult{PendingCandidates: pendingCount}, nil
}
