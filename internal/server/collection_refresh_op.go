// file: internal/server/collection_refresh_op.go
// version: 2.2.0
// guid: 0c8d3b61-7e94-4a25-b3f0-9d1a6e2c5b47
// last-edited: 2026-11-19

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
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
	return reg.RegisterOp(s.collectionRefreshDef())
}

// collectionRefreshDef is HEAD's definition. Its Run is the v2 shell.
func (s *Server) collectionRefreshDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
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
	}
}

// collectionsRefreshPending is the scheduler's precondition for the hourly task:
// a relative-date dynamic collection exists and the search index is open.
func (s *Server) collectionsRefreshPending() (bool, error) {
	if s.storeForWiring() == nil {
		return false, nil
	}
	return s.collectionHandler().RelativeRefreshPending()
}

// collectionRefreshNativeParams are the native kind's params: HEAD's without the
// two dry-run spellings, which the run's mode carries.
type collectionRefreshNativeParams struct{}

// collectionRefreshStore is the read the preview makes: the same evaluation as
// the live run, counting and storing nothing (RefreshRelativeDynamic with
// dryRun true).
type collectionRefreshStore interface {
	PreviewRefreshRelativeDynamic(ctx context.Context, progress func(done, total int)) (handlers.RelativeRefreshResult, error)
}

// collectionRefreshWriter is the one write the run makes: the handler's refresh,
// which stores a changed member list (dryRun false).
type collectionRefreshWriter interface {
	RefreshRelativeDynamic(ctx context.Context, progress func(done, total int)) (handlers.RelativeRefreshResult, error)
}

// collectionRefreshEnv is what the shared core takes from its shell (rules R2,
// R10).
type collectionRefreshEnv struct {
	t7Env
	Store  collectionRefreshStore
	Writer collectionRefreshWriter
}

// collectionRefreshCore is the body of the op, shared by HEAD's shell and the
// native kind. A dry run reads through env.Store; a live run writes through
// env.Writer.
func collectionRefreshCore(ctx context.Context, env collectionRefreshEnv) error {
	dryRun := !env.Live
	progress := func(done, total int) {
		env.progress(done, total, fmt.Sprintf("refreshed %d of %d collections", done, total))
	}
	var (
		res    handlers.RelativeRefreshResult
		runErr error
	)
	if dryRun {
		res, runErr = env.Store.PreviewRefreshRelativeDynamic(ctx, progress)
	} else {
		res, runErr = env.Writer.RefreshRelativeDynamic(ctx, progress)
	}
	if res.IndexUnavailable {
		// One line, and a clean finish: the index being closed or rebuilding is
		// routine around a start or a reindex, and the next hourly run retries.
		mode := ""
		if dryRun {
			mode = "dry run: "
		}
		msg := fmt.Sprintf("%ssearch index not open: skipped %d relative-date dynamic collection(s); the next run retries", mode, res.Considered)
		env.Log(slog.LevelInfo, msg)
		collectionRefreshLog.Info("%s opID=%s", msg, logger.SanitizeLogValue(env.OpID))
		env.progress(1, 1, msg)
		return nil
	}
	mode := ""
	if dryRun {
		mode = "dry run: "
	}
	msg := fmt.Sprintf("%s%d relative-date dynamic collection(s) considered: %d refreshed, %d unchanged, %d skipped (edited during the run), %d failed",
		mode, res.Considered, res.Refreshed, res.Unchanged, res.Conflicts, res.Failed)
	env.Log(slog.LevelInfo, msg)
	collectionRefreshLog.Info("%s opID=%s", msg, logger.SanitizeLogValue(env.OpID))
	if res.Considered == 0 {
		// No collection to walk means no per-item call stamped liveness.
		env.progress(1, 1, msg)
	}
	return runErr
}

// headCollectionRefresh is HEAD's side of both interfaces: the collection
// handler's RefreshRelativeDynamic, called with dryRun true for the read and
// false for the write, as HEAD's body called it.
type headCollectionRefresh struct{ h *handlers.CollectionHandler }

func (c headCollectionRefresh) PreviewRefreshRelativeDynamic(ctx context.Context, progress func(done, total int)) (handlers.RelativeRefreshResult, error) {
	return c.h.RefreshRelativeDynamic(ctx, true, progress)
}

func (c headCollectionRefresh) RefreshRelativeDynamic(ctx context.Context, progress func(done, total int)) (handlers.RelativeRefreshResult, error) {
	return c.h.RefreshRelativeDynamic(ctx, false, progress)
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
	h := headCollectionRefresh{h: s.collectionHandler()}
	return collectionRefreshCore(ctx, collectionRefreshEnv{t7Env: t7HeadEnv(reporter, !dryRun), Store: h, Writer: h})
}

// collectionRefreshNative is the native books.collections.refresh-relative-dynamic
// kind. Preview evaluates and counts through the read side; Live stores a
// changed member list through the writer.
func (s *Server) collectionRefreshNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(collectionRefreshOpID)
	c := t7Common(s.collectionRefreshDef(), sched)
	c.Effects = t7Writes(ops.ResCollections)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			if _, err := t7DecodeNative[collectionRefreshNativeParams](kind, raw); err != nil {
				return struct{}{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return struct{}{}, err
			}
			env := collectionRefreshEnv{t7Env: t7NativeEnv(rc), Store: deps.Store}
			if env.Live {
				w, err := t7NewWriter(kind, deps, rc)
				if err != nil {
					return struct{}{}, err
				}
				env.Writer = w
			}
			return struct{}{}, collectionRefreshCore(rc.Context(), env)
		},
	})
}
