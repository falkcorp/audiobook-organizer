// file: internal/server/collection_refresh_op.go
// version: 2.0.0
// guid: 0c8d3b61-7e94-4a25-b3f0-9d1a6e2c5b47
// last-edited: 2026-10-25

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// collectionRefreshOpID is the op the hourly collections_refresh task
// enqueues.
const collectionRefreshOpID = "collections.refresh-relative-dynamic"

// collectionRefreshParams: dry_run defaults to TRUE (owner rule 2026-09-25: every
// writing op previews unless told otherwise). The preview evaluates every
// relative-date dynamic collection and reports how many would change without
// storing anything; the scheduled task sends dry_run=false.
type collectionRefreshParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

var collectionRefreshLog = logger.New("collections-refresh-relative")

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterCollectionRefreshOp(reg)
	})
}

// RegisterCollectionRefreshOp registers collections.refresh-relative-dynamic.
//
// WHY IT EXISTS. A dynamic collection's members are stored (MaterializedBookIDs)
// because the ABS surface serves them without evaluating a query. A query with a
// relative date (last_played:>-30d) answers differently as the clock moves, so a
// stored answer that nothing refreshes goes stale: an ABS reader would see
// "played in the last 30 days" frozen at the last native read or materialize.
// This op re-evaluates exactly those collections, through the handler's evaluate
// with no viewer, and stores a changed member list. A collection whose query uses
// a per-user field (last_played:>-30d) is not one of them: its members are
// evaluated for each viewer on every read and never stored, so there is nothing to
// refresh and this op has no viewer to evaluate it for.
func (s *Server) RegisterCollectionRefreshOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:          collectionRefreshOpID,
		Preview:     opsregistry.PreviewSpec{Params: collectionRefreshParams{}},
		Liveness:    opsregistry.LivenessManual,
		Plugin:      "collections",
		DisplayName: "Refresh Relative-Date Collections",
		Description: "Re-evaluates every dynamic collection whose query has a relative date (for example date_added:>-7d) so the members the Audiobookshelf surface serves slide with the clock. A collection whose query uses a per-user field (read_status, progress_pct, last_played) is not refreshed: its members are evaluated for each viewer on every read and nothing is stored for it. A collection edited while the run is in flight is skipped and picked up next run. Dry run by default: counts how many would change and stores nothing.",
		// Low: the work is a handful of searches and anything a person started goes first.
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Timeout:         30 * time.Minute,
		// Nothing to resume from: each run re-evaluates from the stored rows.
		ResumePolicy:   opsregistry.ResumeRestart,
		ConcurrencyKey: collectionRefreshOpID,
		// Collections are not one of the named write-set resources; the name is
		// informational (the registry does not gate on it).
		Writes:       []opsregistry.Resource{opsregistry.Resource("collections")},
		Permissions:  []auth.Permission{auth.PermCollectionsManage},
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run:          s.runCollectionRefreshOp,
	})
}

// collectionsRefreshPending is the scheduler's precondition for the hourly task:
// a relative-date dynamic collection exists and the search index is open.
func (s *Server) collectionsRefreshPending() (bool, error) {
	if s.storeForWiring() == nil {
		return false, nil
	}
	return s.collectionHandler().RelativeRefreshPending()
}

func (s *Server) runCollectionRefreshOp(ctx context.Context, raw json.RawMessage, reporter opsregistry.Reporter) error {
	var p collectionRefreshParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%s: decode params: %w", collectionRefreshOpID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(collectionRefreshOpID, p.DryRun, p.DryRunCamel)
	if err != nil {
		return err
	}
	res, runErr := s.collectionHandler().RefreshRelativeDynamic(ctx, dryRun, func(done, total int) {
		_ = reporter.UpdateProgress(done, total, fmt.Sprintf("refreshed %d of %d collections", done, total))
	})
	if res.IndexUnavailable {
		// One line, and a clean finish: the index being closed or rebuilding is
		// routine around a start or a reindex, and the next hourly run retries.
		mode := ""
		if dryRun {
			mode = "dry run: "
		}
		msg := fmt.Sprintf("%ssearch index not open: skipped %d relative-date dynamic collection(s); the next run retries", mode, res.Considered)
		_ = reporter.Log(slog.LevelInfo, msg)
		collectionRefreshLog.Info("%s opID=%s", msg, logger.SanitizeLogValue(opsregistry.ReporterOpID(reporter)))
		_ = reporter.UpdateProgress(1, 1, msg)
		return nil
	}
	mode := ""
	if dryRun {
		mode = "dry run: "
	}
	msg := fmt.Sprintf("%s%d relative-date dynamic collection(s) considered: %d refreshed, %d unchanged, %d skipped (edited during the run), %d failed",
		mode, res.Considered, res.Refreshed, res.Unchanged, res.Conflicts, res.Failed)
	_ = reporter.Log(slog.LevelInfo, msg)
	collectionRefreshLog.Info("%s opID=%s", msg, logger.SanitizeLogValue(opsregistry.ReporterOpID(reporter)))
	if res.Considered == 0 {
		// No collection to walk means no per-item call stamped liveness.
		_ = reporter.UpdateProgress(1, 1, msg)
	}
	return runErr
}
