// file: internal/plugins/maintenance/batch_poller.go
// version: 1.1.0
// guid: c9d0e1f2-a3b4-5678-2345-890123456789
// last-edited: 2026-08-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) batchPollerDef() sdk.OperationDef {
	sched := "*/5 * * * *" // every 5 minutes
	return sdk.OperationDef{
		ID:              "maintenance.batch-poller",
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 5 * time.Minute, // LivenessNone requires an explicit budget
		Plugin:          "maintenance",
		DisplayName:     "OpenAI batch poller",
		Description:     "Polls OpenAI for completed batch jobs and routes results to the appropriate handler.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.batch-poller",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         5 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapNetworkOpenAI},
		Run:             p.runBatchPoller,
	}
}

func (p *Plugin) runBatchPoller(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if !p.deps.HasBatchPoller() {
		_ = reporter.Log(slog.LevelInfo, "Batch poller not configured, skipping")
		return nil
	}
	processed, err := p.deps.PollBatch(ctx)
	if err != nil {
		logging.Warn(ctx, "batch-poller", "err", err)
	}
	if processed > 0 {
		msg := fmt.Sprintf("Processed %d completed batches", processed)
		logging.Info(ctx, "batch-poller", "msg", msg)
		_ = reporter.Log(slog.LevelInfo, msg)
	}
	return nil
}
