// file: internal/plugins/maintenance/vacuum_activity_db.go
// version: 1.0.0
// guid: fa1a4efa-5368-45f0-bccb-6fda6360e461
// last-edited: 2026-10-18

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// VacuumActivityDBDefID is the def id of the activity-database VACUUM.
const VacuumActivityDBDefID = "maintenance.vacuum-activity-db"

type vacuumActivityDBParams struct {
	// DryRun and DryRunCamel are pointers so an ABSENT key can be told apart from
	// an explicit false; opmode.ResolveDryRun maps omitted to a preview. Owner
	// rule 2026-09-25: every writing op previews unless the caller says
	// dry_run=false.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

// VacuumActivityDBResult is the op's persisted result.
type VacuumActivityDBResult struct {
	database.ActivityVacuumReport
}

// vacuumActivityDBDef is the only way to hand freed pages of the SQLite activity
// database back to the filesystem.
//
// WHY AN OP. Compaction, pruning and the retired summary clamp shorten or delete
// rows but leave their pages allocated, so activity.sqlite does not shrink until
// a VACUUM rewrites it. auto_vacuum is never set and maintenance.db-optimize only
// compacts Pebble, so without this op nothing ever returns that space. The first
// production clamp on 2026-09-08 freed 10.66 GB of pages this way. The route that
// used to reach VacuumActivity (POST /activity/clamp-summaries) was retired in
// 01-P81g; the capability lives on here, behind a preview.
//
// PREVIEW. dry_run defaults to true (opmode.ResolveDryRun). A preview reports the
// file size, -wal size, page_count, freelist_count and the reclaimable estimate
// (freelist_count * page_size) and writes nothing. dry_run=false runs VACUUM and
// then the post-vacuum WAL truncate, and reports the bytes actually returned.
//
// TEMP SPACE. VACUUM rewrites the whole database, so it needs roughly the file's
// size again in free space on the volume holding the activity database while it
// runs. The preview is the place to check that before going live.
//
// LIVENESS: NONE, WITH A BUDGET. The VACUUM is one blocking statement with no
// progress callback, so the only true declaration is LivenessNone with an
// explicit budget (see optimizeActivityDBDef for why LivenessManual would invite
// a five-minute strike). 2h is a ceiling for a database of tens of GB on a loaded
// host, not an estimate.
//
// CONCURRENCY. Shares cleanup-activity-log's key, so it never runs beside
// activity compaction, digest recompaction, the filter-index backfill or the
// Pebble reclaim, all of which write the same activity data. VACUUM holds an
// exclusive lock on the SQLite writer connection for its whole run, so foreground
// activity writes wait it out; run it when that is acceptable.
//
// Deliberately NO Schedule: it is a job you trigger, after looking at the preview.
func (p *Plugin) vacuumActivityDBDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              VacuumActivityDBDefID,
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 2 * time.Hour,
		Plugin:          "maintenance",
		DisplayName:     "Vacuum activity database",
		Description: "Rewrites the SQLite activity database to give freed pages back to the filesystem, then truncates its WAL. " +
			"Preview unless dry_run=false: a preview reports file size, page and free-page counts and the reclaimable estimate.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.cleanup-activity-log",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runVacuumActivityDB,
	}
}

func (p *Plugin) runVacuumActivityDB(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params vacuumActivityDBParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: invalid params: %w", VacuumActivityDBDefID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(VacuumActivityDBDefID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}

	mode := "PREVIEW"
	if !dryRun {
		mode = "LIVE"
	}
	startMsg := fmt.Sprintf("Vacuum of the activity database starting (%s)", mode)
	logging.Info(ctx, startMsg)
	_ = reporter.Log(slog.LevelInfo, startMsg)
	_ = reporter.UpdateProgress(0, 1, startMsg)

	rep, runErr := p.deps.VacuumActivityDB(ctx, dryRun)

	var msg string
	switch {
	case !rep.Supported:
		msg = "Activity backend has no SQLite database; nothing to vacuum"
	case dryRun:
		msg = fmt.Sprintf(
			"PREVIEW: activity database is %d bytes (+%d bytes WAL); %d of %d pages are free, about %d bytes reclaimable. Rerun with dry_run=false to vacuum.",
			rep.Before.FileBytes, rep.Before.WALBytes, rep.Before.FreelistCount, rep.Before.PageCount, rep.Before.ReclaimableBytes)
	case runErr != nil:
		msg = fmt.Sprintf("FAILED after %s: %v (on disk %d -> %d bytes)",
			rep.Duration.Round(time.Millisecond), runErr, rep.Before.DiskBytes(), rep.After.DiskBytes())
	default:
		msg = fmt.Sprintf("Vacuumed the activity database in %s: on disk %d -> %d bytes, %d bytes reclaimed",
			rep.Duration.Round(time.Millisecond), rep.Before.DiskBytes(), rep.After.DiskBytes(), rep.BytesReclaimed)
	}
	level := slog.LevelInfo
	if runErr != nil {
		level = slog.LevelError
	}
	logging.Info(ctx, msg)
	_ = reporter.Log(level, msg)
	_ = reporter.UpdateProgress(1, 1, msg)

	if setErr := opsregistry.ReporterSetResult(reporter, VacuumActivityDBResult{rep}); setErr != nil {
		if runErr != nil {
			return fmt.Errorf("vacuum-activity-db: %w (and persisting the result failed: %v)", runErr, setErr)
		}
		return fmt.Errorf("vacuum-activity-db: persist result: %w", setErr)
	}
	if runErr != nil {
		return fmt.Errorf("vacuum-activity-db: %w", runErr)
	}
	return nil
}
