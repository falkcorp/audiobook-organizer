// file: internal/plugins/maintenance/activity_reclaim.go
// version: 1.0.0
// guid: 5a2e7c41-6b83-4d09-9f27-c1a840b6e35d
// last-edited: 2026-09-08

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// activityReclaimDefaultRetain is how much recent activity history the reclaim
// leaves in Pebble when the caller does not say. It is well above
// database.activityReclaimMinRetain so the default keeps a couple of days of
// fallback rather than sitting on the floor.
const activityReclaimDefaultRetain = 72 * time.Hour

type activityReclaimParams struct {
	// DryRun is a pointer so an ABSENT field can be told apart from an explicit
	// false. A plain bool would default to false, making "trigger it with no
	// params" mean "delete", which is exactly backwards for a destructive op.
	DryRun      *bool `json:"dry_run"`
	RetainHours int   `json:"retain_hours"`
}

func (p *Plugin) activityReclaimDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:     "maintenance.activity-reclaim",
		Plugin: "maintenance",
		// LivenessNone with an explicit budget, not LivenessManual.
		//
		// The work is one blocking Prune per tier with no callback of its own,
		// so progress can only be reported BETWEEN tiers — and on production's
		// `change` tier a single call can run for a long time. Declaring
		// LivenessManual would invite exactly the five-minute stuck-strike that
		// maintenance.db-optimize was killed by every week (see db.go), so this
		// declares the truth instead: reporting granularity is coarse, here is a
		// real budget for it.
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 45 * time.Minute,
		DisplayName:     "Reclaim migrated activity log",
		Description: "Deletes Pebble-side activity rows that the SQLite cutover has made redundant. " +
			"Reports a census and refuses to delete while reads are still served from Pebble. " +
			"Dry-run unless dry_run=false.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.activity-reclaim",
		// Cancellable: the census and the prune loop both check ctx between
		// tiers, and a partial prune is safe — the op is idempotent and a rerun
		// simply continues from whatever is left.
		Cancellable: true,
		Isolate:     false,
		Timeout:     60 * time.Minute,
		// Deliberately NO Schedule. This deletes data, and its safety rests on a
		// cutover flag an operator should be looking at when it runs. It is a
		// job you trigger, not one that fires at 3am.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runActivityReclaim,
	}
}

func (p *Plugin) runActivityReclaim(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	var args activityReclaimParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return fmt.Errorf("invalid parameters: %w", err)
		}
	}
	dryRun := true
	if args.DryRun != nil {
		dryRun = *args.DryRun
	}
	retain := activityReclaimDefaultRetain
	if args.RetainHours > 0 {
		retain = time.Duration(args.RetainHours) * time.Hour
	}

	mode := "DRY RUN"
	if !dryRun {
		mode = "APPLY"
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"Activity reclaim starting (%s, retaining %s of recent history)", mode, retain))
	_ = reporter.UpdateProgress(0, len(actReclaimTierOrder), "Counting activity rows on both backends...")

	onTier := func(tier string, idx, total int, t database.ActivityReclaimTier) {
		_ = reporter.UpdateProgress(idx, total, fmt.Sprintf(
			"Pruned tier %s (%d/%d): %d rows deleted", tier, idx, total, t.Deleted))
	}

	res, err := p.deps.ReclaimMigratedActivity(ctx, retain, dryRun, onTier)
	if err != nil {
		return fmt.Errorf("activity reclaim: %w", err)
	}

	// The census goes to the log whatever happened, including on a refusal —
	// that is the difference between a job that can only say "no" and one that
	// tells you what is blocking it.
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"Activity census: Pebble %d rows, SQLite %d rows, reads_on_sqlite=%t, %d rows older than %s",
		res.PrimaryRows, res.SecondaryRows, res.ReadSecondary,
		res.EligibleRows, res.Cutoff.Format(time.RFC3339)))

	if res.Refused {
		msg := "Nothing reclaimed: " + res.RefusedReason
		_ = reporter.Log(slog.LevelWarn, msg)
		_ = reporter.UpdateProgress(len(actReclaimTierOrder), len(actReclaimTierOrder), msg)
		return nil
	}

	if dryRun {
		msg := fmt.Sprintf(
			"DRY RUN: %d of %d Pebble activity rows are eligible to delete. Rerun with dry_run=false to apply.",
			res.EligibleRows, res.PrimaryRows)
		_ = reporter.Log(slog.LevelInfo, msg)
		_ = reporter.UpdateProgress(len(actReclaimTierOrder), len(actReclaimTierOrder), msg)
		return nil
	}

	// Say plainly that deleting keys is not the same as freeing bytes. Pebble
	// releases space only when a compaction rewrites the sstables the deleted
	// keys lived in, and that compaction transiently needs headroom rather than
	// giving it back immediately.
	msg := fmt.Sprintf(
		"Reclaimed %d Pebble activity rows (%d remain). Disk space is NOT freed until "+
			"maintenance.db-optimize compacts the database, and that compaction transiently "+
			"grows on-disk size before it shrinks.",
		res.RowsDeleted, res.PrimaryRows-res.RowsDeleted)
	_ = reporter.Log(slog.LevelInfo, msg)
	_ = reporter.UpdateProgress(len(actReclaimTierOrder), len(actReclaimTierOrder), msg)
	return nil
}

// actReclaimTierOrder mirrors database.actTiers for progress denominators only.
// The database package owns the real list; this is not a second source of truth
// for WHICH tiers get pruned, only for how many steps to draw.
var actReclaimTierOrder = []string{"change", "debug", "audit", "info", "batch", "system", "digest"}
