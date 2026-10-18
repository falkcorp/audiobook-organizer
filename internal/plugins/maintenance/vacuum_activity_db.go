// file: internal/plugins/maintenance/vacuum_activity_db.go
// version: 1.2.0
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
// DISK SPACE AND TEMP DIRECTORY. VACUUM rewrites the whole database. It builds
// the compacted copy in a temp file and then copies it back through the WAL, so
// the peak EXTRA space is about TWICE the compacted size (the pages in use,
// (page_count - freelist_count) * page_size; the main file's size is not used
// because recent pages live in the -wal), not the file's size: the rebuilt database passes through the -wal
// on the database's volume until the post-vacuum TRUNCATE returns it. Before it
// starts, VacuumActivity pins SQLite's temp directory to the database's own
// directory (otherwise the temp copy lands in TMPDIR, the root pool on prod: the
// 2026-09-09 outage) and REFUSES, fail closed, if that pin fails or if free space
// there is below required_bytes = 2 x compacted + max(1 GiB, 10%). The preview
// reports free_bytes, required_bytes and would_refuse so the answer is known
// before going live. ZFS snapshots that hold the database's old blocks can push
// the real peak past 2x and make the net reclaim negative (the old blocks stay
// pinned), so purge snapshots first.
//
// STALLS WHILE IT RUNS. A live VACUUM holds the activity store's single writer
// connection for its whole run. Synchronous callers of SQLActivityStore.Record
// (the library scan, once per book; metadata applies and write-back; admin debug)
// block until it finishes, and the log-tee's activity writer, whose channel holds
// 10k entries, drops entries once that fills. So scans and metadata applies
// stall and some activity log lines may be dropped while it runs. Owner decision
// 2026-10-18: accept that and say so; the follow-up that gives Record a context
// or a deferral is todo.d/2026-10-18-activity-record-nonblocking.md. As cheap
// protection a live run refuses to start while any op in vacuumBlockingDefIDs (library
// scan, import, metadata fetch/apply/write-back, version-twin metadata) is queued
// or running; a metadata apply started from the UI during the run still stalls. That check is POINT-IN-TIME,
// not a lock (an op enqueued a moment later is not caught), the same shape as
// dedupe-book-file-rows' scan guard; a preview is read-only and always allowed.
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
			"Preview unless dry_run=false: a preview reports file size, page and free-page counts, the reclaimable estimate and a free-space check. " +
			"A live run needs about twice the compacted size free on the database's volume and refuses without it. " +
			"While it runs scans and metadata applies stall and some activity log lines may be dropped; " +
			"it refuses to start while the listed scan and metadata ops are active, but a metadata apply started from the UI during the run will stall until it finishes. " +
			"ZFS snapshots holding the database's old blocks can push peak usage past 2x and make the net reclaim negative, so purge snapshots first.",
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

	if !dryRun {
		if err := p.refuseWhileActivityWritersActive(); err != nil {
			logging.Error(ctx, err.Error())
			_ = reporter.Log(slog.LevelError, err.Error())
			return err
		}
	}

	rep, runErr := p.deps.VacuumActivityDB(ctx, dryRun)

	var msg string
	level := slog.LevelInfo
	switch {
	case runErr != nil:
		level = slog.LevelError
		if dryRun {
			msg = fmt.Sprintf("PREVIEW FAILED: %v", runErr)
		} else {
			msg = fmt.Sprintf("FAILED after %s: %v (on disk %d -> %d bytes)",
				rep.Duration.Round(time.Millisecond), runErr, rep.Before.DiskBytes(), rep.After.DiskBytes())
		}
	case !rep.Supported:
		msg = "Activity backend has no SQLite database; nothing to vacuum"
	case dryRun:
		verdict := "a live run would proceed"
		if rep.Before.WouldRefuse {
			verdict = "a live run would REFUSE"
			if rep.Before.PreflightNote != "" {
				verdict += " (" + rep.Before.PreflightNote + ")"
			}
		}
		msg = fmt.Sprintf(
			"PREVIEW: activity database is %d bytes (+%d bytes WAL); %d of %d pages are free, about %d bytes reclaimable. "+
				"Disk check: free_bytes=%d required_bytes=%d would_refuse=%t, %s. "+
				"On a live run, scans and metadata applies stall and some activity log lines may be dropped while it runs. "+
				"ZFS snapshots holding the database's old blocks can push peak usage past 2x and make the net reclaim negative, so purge snapshots first. "+
				"Rerun with dry_run=false to vacuum.",
			rep.Before.FileBytes, rep.Before.WALBytes, rep.Before.FreelistCount, rep.Before.PageCount, rep.Before.ReclaimableBytes,
			rep.Before.FreeBytes, rep.Before.RequiredBytes, rep.Before.WouldRefuse, verdict)
	default:
		msg = fmt.Sprintf("Vacuumed the activity database in %s: on disk %d -> %d bytes, %d bytes reclaimed",
			rep.Duration.Round(time.Millisecond), rep.Before.DiskBytes(), rep.After.DiskBytes(), rep.BytesReclaimed)
	}
	if runErr != nil {
		logging.Error(ctx, msg)
	} else {
		logging.Info(ctx, msg)
	}
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

// vacuumBlockingDefIDs are the ops whose synchronous activity Record calls (or
// whole-library write pressure) a live VACUUM would stall: the library scan and
// the metadata fetch, apply and write-back ops.
var vacuumBlockingDefIDs = map[string]bool{
	"library.scan":                      true,
	"library.import":                    true, // PerformScan -> OnBookScanned -> synchronous Record
	"metadata.apply-when-scanned":       true,
	"maintenance.version-twin-metadata": true,
	"library.folder-auto-scan":          true,
	"library.bulk-metadata-fetch":       true,
	"library.bulk-write-back":           true,
	"metadata.batch-apply-cached":       true,
	"metadata.batch-save":               true,
}

// refuseWhileActivityWritersActive fails closed: an unreadable queue is not
// proof of an idle queue. Point-in-time, not a lock (see vacuumActivityDBDef). A
// zombie 'running' row with no live handle also refuses, which is the safe
// direction; clear it rather than bypassing this check.
func (p *Plugin) refuseWhileActivityWritersActive() error {
	return refuseWhileOpsActive(p.deps.OperationQueueStore(), VacuumActivityDBDefID, vacuumBlockingDefIDs)
}

func refuseWhileOpsActive(queue OpQueueReader, opName string, blocking map[string]bool) error {
	if queue == nil {
		return fmt.Errorf("%s: cannot verify no scan or metadata op is active; refusing to vacuum", opName)
	}
	active, err := queue.ListActiveOperationsV2()
	if err != nil {
		return fmt.Errorf("%s: cannot list active operations; refusing to vacuum: %w", opName, err)
	}
	for _, op := range active {
		if blocking[op.DefID] && (op.Status == "running" || op.Status == "queued") {
			return fmt.Errorf("%s: %s is %s (op %s); refusing to vacuum: a live VACUUM holds the activity "+
				"writer and would stall it. Retry when it finishes; a preview (dry_run=true) is always allowed",
				opName, op.DefID, op.Status, op.ID)
		}
	}
	return nil
}
