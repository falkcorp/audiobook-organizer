// file: internal/server/legacy_backfill_op.go
// version: 1.0.0
// guid: 9e2b7f04-6c31-4a58-b0d9-52f81c6ae374
// last-edited: 2026-08-22

// legacy_backfill_op registers operations.backfill-legacy-status, the supervised
// repair for v1 operation rows that never left a non-terminal status because the
// status bridge did not exist when they were created.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// legacyBackfillOpParams controls one backfill pass.
//
// DryRun defaults to TRUE via DefaultParams below, and that default is the whole
// safety story: this op rewrites historical prod rows, so an operator who
// triggers it with no body gets a plan, not a write.
type legacyBackfillOpParams struct {
	DryRun bool `json:"dry_run"`
}

// RegisterLegacyStatusBackfillOp registers "operations.backfill-legacy-status".
func (s *Server) RegisterLegacyStatusBackfillOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "operations.backfill-legacy-status",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "operations",
		DisplayName:     "Backfill Legacy Operation Status",
		Description:     "Resolves old v1 operation rows stuck at pending, using the v2 run that did the work as evidence. Reports a plan by default and only writes when dry_run is false.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,

		// ResumeDrop, not ResumeRestart. A half-applied pass is safe to abandon:
		// the op is idempotent (an already-terminal row is skipped on the next
		// run), so re-running from scratch is strictly better than resuming into
		// a partially-written state whose progress we did not persist.
		ResumePolicy:   opsregistry.ResumeDrop,
		ConcurrencyKey: "operations.backfill-legacy-status",

		Permissions:  []auth.Permission{auth.PermSettingsManage},
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},

		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			// Absent params means dry run. Note this is the opposite of Go's zero
			// value, so the decode has to be explicit rather than relying on it.
			p := legacyBackfillOpParams{DryRun: true}
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("backfill-legacy-status: decode params: %w", err)
				}
			}

			if s.opRegistry == nil {
				return fmt.Errorf("backfill-legacy-status: registry not initialized")
			}

			_ = reporter.UpdateProgress(0, 100, "Scanning operation rows...")
			report, err := s.opRegistry.BackfillLegacyOpStatus(ctx, p.DryRun)
			if err != nil {
				return err
			}

			mode := "DRY RUN — nothing written"
			if !p.DryRun {
				mode = "APPLIED"
			}
			_ = reporter.Log(slog.LevelInfo, "legacy status backfill: "+mode,
				slog.Int("total_v1_rows", report.TotalV1Rows),
				slog.Int("non_terminal", report.NonTerminal),
				slog.Int("resolved_from_v2", report.ResolvedFromV2),
				slog.Int("marked_interrupted", report.MarkedInterrupted),
				slog.Int("applied", report.Applied),
				slog.Int("apply_errors", report.ApplyErrors),
			)

			for status, n := range report.ByNewStatus {
				_ = reporter.Log(slog.LevelInfo, "legacy status backfill: by new status",
					slog.String("new_status", status), slog.Int("count", n))
			}
			for opType, n := range report.ByType {
				_ = reporter.Log(slog.LevelInfo, "legacy status backfill: by type",
					slog.String("type", opType), slog.Int("count", n))
			}
			if report.OldestCreatedAt != nil && report.NewestCreatedAt != nil {
				_ = reporter.Log(slog.LevelInfo, "legacy status backfill: affected range",
					slog.Time("oldest", *report.OldestCreatedAt),
					slog.Time("newest", *report.NewestCreatedAt))
			}

			// The full plan, one entry per row, so a dry run is reviewable rather
			// than merely summarised. A sample cannot show that the one row an
			// operator cares about was classified correctly.
			for _, d := range report.Decisions {
				_ = reporter.Log(slog.LevelDebug, "legacy status backfill: row",
					slog.String("op_id", d.OpID),
					slog.String("type", d.Type),
					slog.String("old_status", d.OldStatus),
					slog.String("new_status", d.NewStatus),
					slog.String("evidence", d.Evidence),
					slog.String("v2_op_id", d.V2OpID),
					slog.String("v2_status", d.V2Status),
					slog.Time("created_at", d.CreatedAt),
					slog.Bool("applied", d.Applied),
					slog.String("apply_error", d.ApplyError),
				)
			}

			_ = reporter.UpdateProgress(100, 100, fmt.Sprintf(
				"%s — %d of %d rows non-terminal (%d resolved from v2, %d interrupted)",
				mode, report.NonTerminal, report.TotalV1Rows,
				report.ResolvedFromV2, report.MarkedInterrupted))

			if report.ApplyErrors > 0 {
				return fmt.Errorf("backfill-legacy-status: %d of %d writes failed",
					report.ApplyErrors, report.NonTerminal)
			}
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterLegacyStatusBackfillOp(reg)
	})
}
