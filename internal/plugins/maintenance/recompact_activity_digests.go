// file: internal/plugins/maintenance/recompact_activity_digests.go
// version: 1.0.0
// guid: 8b4f2e61-3d7a-4c95-a0e8-5f1c9d6b2a73
// last-edited: 2026-09-10

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logging"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// RecompactActivityDigestsDefID is the def id of the admin-triggered digest
// re-derivation. POST /api/v1/admin/recompact-digests enqueues it; the handler
// refers to it by this constant.
const RecompactActivityDigestsDefID = "maintenance.recompact-activity-digests"

// RecompactActivityDigestsResult is the op's persisted result: how many
// digests were rewritten and how many already had nothing legacy in them.
type RecompactActivityDigestsResult struct {
	Touched int `json:"touched"`
	Skipped int `json:"skipped"`
}

// recompactActivityDigestsDef is the admin "recompact digests" action.
//
// WHY AN OP AND NOT A HANDLER. Until 2026-09-10 POST /admin/recompact-digests
// re-derived every digest inside the HTTP request — the same shape the
// Activity page's Compact button had until PR #3214, and the same failure:
// on a store with years of digests across two backends the request outlives
// any browser timeout and the caller cannot tell whether the work is still
// going. As an op the request answers in milliseconds with an op id and the
// work is visible in the operations list.
//
// LIVENESS. LivenessNone with an explicit budget, not LivenessManual. The
// store's RecompactDigests is one blocking call per backend with no progress
// callback of its own (it checks ctx between digests but reports nothing), so
// there is nothing to forward to UpdateProgress mid-run. Declaring
// LivenessManual would invite the five-minute never_reported strike that
// killed the nightly cleanup until #3214; declaring the truth — coarse
// granularity, here is the budget — is what maintenance.activity-reclaim does
// for the same reason. Digests are one row per day per source, so the work is
// bounded by days of history, not by row volume; 1h is a ceiling, not an
// estimate.
//
// CONCURRENCY. Shares cleanup-activity-log's key so it never rewrites a digest
// that compaction is building at the same moment.
func (p *Plugin) recompactActivityDigestsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              RecompactActivityDigestsDefID,
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 60 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Recompact activity digests",
		Description:     "Re-derives type, tier and tags on daily digests that were compacted before enrichment existed, on every activity database.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.cleanup-activity-log",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runRecompactActivityDigests,
	}
}

func (p *Plugin) runRecompactActivityDigests(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	startMsg := "Re-deriving legacy daily-digest items on every activity database"
	logging.Info(ctx, startMsg)
	_ = reporter.Log(slog.LevelInfo, startMsg)
	_ = reporter.UpdateProgress(0, 0, startMsg)

	res, err := p.deps.RecompactActivityDigests(ctx)
	result := RecompactActivityDigestsResult{Touched: res.Touched, Skipped: res.Skipped}

	// The counters are reported whatever happened: on a failure they are what
	// was committed before it, which is the number an operator needs to decide
	// whether a retry is worth it.
	doneMsg := fmt.Sprintf("Recompacted %d digest(s), %d already current", result.Touched, result.Skipped)
	if err != nil {
		doneMsg = fmt.Sprintf("FAILED after recompacting %d digest(s) (%d already current): %v", result.Touched, result.Skipped, err)
		_ = reporter.Log(slog.LevelError, doneMsg)
	} else {
		_ = reporter.Log(slog.LevelInfo, doneMsg)
	}
	logging.Info(ctx, doneMsg)
	_ = reporter.UpdateProgress(result.Touched+result.Skipped, result.Touched+result.Skipped, doneMsg)

	if setErr := opsregistry.ReporterSetResult(reporter, result); setErr != nil {
		if err != nil {
			return fmt.Errorf("recompact-activity-digests: %w (and persisting the result failed: %v)", err, setErr)
		}
		return fmt.Errorf("recompact-activity-digests: persist result: %w", setErr)
	}
	if err != nil {
		return fmt.Errorf("recompact-activity-digests: %w", err)
	}
	return nil
}
