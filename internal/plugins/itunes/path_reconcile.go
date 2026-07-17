// file: internal/plugins/itunes/path_reconcile.go
// version: 1.1.1
// guid: d4e5f6a7-b8c9-0123-defg-234567890123
// last-edited: 2026-07-17

package itunes

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) pathReconciledDef() sdk.OperationDef {
	// No Schedule: the Run below is an unimplemented stub (C1). The former
	// daily "0 4 * * *" cron burned an op-history row every night doing
	// nothing. Restore a schedule only together with a real implementation.
	return sdk.OperationDef{
		ID:                    "itunes.path-reconcile",
		Plugin:                "itunes",
		DisplayName:           "iTunes Path Reconcile",
		Description:           "Reconcile iTunes track paths after library reorganizations.",
		Isolate:               false,
		ResumePolicy:          sdk.ResumeDrop,
		DefaultPriority:       sdk.PriorityLow,
		Cancellable:           true,
		Timeout:               60 * time.Minute,
		ConcurrencyKey:        "itunes.path-reconcile",
		MinCheckpointInterval: 30 * time.Second,
		Run:                   p.runPathReconcile,
	}
}

func (p *Plugin) runPathReconcile(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	// TODO: Implement iTunes path reconciliation operation.
	// This should call p.svc.Paths.Reconcile(ctx, opID, progress).
	_ = reporter.Log(slog.LevelWarn, "op not implemented — no-op", slog.String("def_id", "itunes.path-reconcile"))
	return nil
}
