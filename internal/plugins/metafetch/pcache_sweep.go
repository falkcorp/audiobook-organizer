// file: internal/plugins/metafetch/pcache_sweep.go
// version: 1.0.0
// guid: 1817dbb3-1b63-474d-af55-ff93d468ae1b
// last-edited: 2026-10-11

package metafetch

// metafetch.pcache-sweep: pruning for the shared provider-answer cache
// (pcache:). Nothing else ever deletes a pcache: row -- an expired row is
// kept on read and only overwritten when the same question is asked again --
// so without this op the family only grows.
//
// It deletes two kinds of row, and nothing else:
//
//  1. A question or product row (pcache:q:, pcache:asin:) that no read would
//     serve: past its TTL, empty while its provider's negative caching is
//     off, or undecodable. "Would not serve" is the read path's own rule
//     (database.PCacheRowDead with metafetch.PCacheTTLFor), and each row is
//     re-read and re-judged right before its page is deleted, so a row
//     rewritten with a fresh answer since the page was read is kept.
//  2. A book's pointer row (pcache:book:<id>) whose book no longer resolves.
//     Only the pointer row itself is deleted: the shared answers it lists
//     may serve other books and live until their own TTL. A lookup ERROR is
//     not an absence and is never deleted (the reaper's rule,
//     internal/plugins/maintenance/metadata_cache_reap.go). A soft-deleted
//     book still resolves and keeps its row.
//
// It never touches the per-book metadata_fetch_cache: rows or anything
// outside pcache:. Dry run (preview) is the default.
//
// Not scheduled: its sibling cache sweep, maintenance.metadata-cache-reap,
// is run by hand too.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const (
	pcacheSweepOpID = "metafetch.pcache-sweep"

	// pcacheSweepPageSize bounds memory: one page of rows is read, judged and
	// (on apply) deleted in one batch before the next is read.
	pcacheSweepPageSize = 500

	// pcacheSweepResolveConcurrency bounds the pointer-row book lookups on a
	// page. Each is a point-get against the store, so this is store-bound;
	// the same figure as the metadata-cache reaper's resolve sweep.
	pcacheSweepResolveConcurrency = 8
)

type pcacheSweepParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

// pcacheSweepStore is the raw key space plus the one library read the
// pointer-row check needs.
type pcacheSweepStore interface {
	database.RawKVStore
	GetBookByID(id string) (*database.Book, error)
}

var _ pcacheSweepStore = (database.Store)(nil)

// pcacheSweepResult is the run's accounting, persisted as the op result.
type pcacheSweepResult struct {
	DryRun bool `json:"dry_run"`
	// Scanned rows by kind.
	Questions int64 `json:"questions"`
	Products  int64 `json:"products"`
	Pointers  int64 `json:"pointers"`
	// Expired is the question/product rows no read would serve, by reason
	// (ExpiredByReason); ExpiredDeleted of them were deleted (0 on a dry
	// run) and ExpiredKept were found fresh on the delete-time re-check.
	Expired         int64            `json:"expired"`
	ExpiredByReason map[string]int64 `json:"expired_by_reason,omitempty"`
	ExpiredDeleted  int64            `json:"expired_deleted"`
	ExpiredKept     int64            `json:"expired_kept_on_recheck"`
	// OrphanPointers is the pointer rows whose book does not resolve;
	// OrphanPointersDeleted of them were deleted (0 on a dry run).
	OrphanPointers        int64 `json:"orphan_pointers"`
	OrphanPointersDeleted int64 `json:"orphan_pointers_deleted"`
	// PointerLookupErrors is the pointer rows whose book could not be
	// resolved either way: never deleted.
	PointerLookupErrors int64 `json:"pointer_lookup_errors"`
	// DeleteErrors is rows a delete (or its re-check read) failed for.
	DeleteErrors int64 `json:"delete_errors"`
}

func (r pcacheSweepResult) summary() string {
	mode := "DRY RUN"
	if !r.DryRun {
		mode = "APPLIED"
	}
	return fmt.Sprintf("%s questions=%d products=%d pointers=%d expired=%d expired_deleted=%d expired_kept=%d "+
		"orphan_pointers=%d orphan_pointers_deleted=%d pointer_lookup_errors=%d delete_errors=%d",
		mode, r.Questions, r.Products, r.Pointers, r.Expired, r.ExpiredDeleted, r.ExpiredKept,
		r.OrphanPointers, r.OrphanPointersDeleted, r.PointerLookupErrors, r.DeleteErrors)
}

func (p *Plugin) pcacheSweepDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          pcacheSweepOpID,
		Plugin:      "metafetch",
		DisplayName: "Sweep the shared metadata answer cache",
		Description: "Deletes shared provider-answer cache rows (pcache:) that are dead: question and product " +
			"answers past their TTL (the same rule a read applies: metadata_fetch_cache_ttl_days for a found " +
			"answer, the provider's metadata_fetch.negative_ttl_days for an empty one), empty answers kept while " +
			"negative caching is off, rows that do not decode, and per-book pointer rows whose book no longer " +
			"exists (only the pointer row; the shared answers it lists are left to their own TTL). Never touches " +
			"the per-book metadata_fetch_cache rows. Defaults to a dry run; dry_run=false deletes.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  pcacheSweepOpID,
		// ResumeDrop: a delete pass interrupted midway is not picked back up;
		// re-running is idempotent (a deleted row is not found again).
		ResumePolicy: sdk.ResumeDrop,
		// LivenessManual: the walk reports progress once per page.
		Liveness:    sdk.LivenessManual,
		Cancellable: true,
		// CapLibraryWrite although no library row is written: there is no
		// cache-specific grant, and a delete op must not claim read-only.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runPCacheSweep,
	}
}

// pcacheSweepRun is one run's state. Counters are atomic: the pointer-row
// lookups of a page run on a bounded pool.
type pcacheSweepRun struct {
	store    pcacheSweepStore
	reporter sdk.Reporter
	log      logger.LevelLogger
	dry      bool
	now      time.Time
	rule     database.PCacheTTLRule

	total, done atomic.Int64

	questions, products, pointers                atomic.Int64
	expired, expiredDeleted, expiredKept         atomic.Int64
	orphans, orphansDeleted, lookupErrs, delErrs atomic.Int64

	byReason map[string]int64 // touched only by the sequential page loop
}

func (p *Plugin) runPCacheSweep(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params pcacheSweepParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: invalid params: %w", pcacheSweepOpID, err)
		}
	}
	dry, err := opmode.ResolveDryRun(pcacheSweepOpID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	store, ok := p.store.(pcacheSweepStore)
	if !ok || store == nil {
		return fmt.Errorf("%s: store does not support the shared cache sweep", pcacheSweepOpID)
	}
	run := &pcacheSweepRun{
		store: store, reporter: reporter, log: logger.New("pcache-sweep"), dry: dry,
		now: time.Now(), rule: metafetch.PCacheTTLFor, byReason: map[string]int64{},
	}
	res, err := run.sweep(ctx)
	if err != nil {
		return err
	}
	run.log.Info("%s complete: %s", pcacheSweepOpID, res.summary())
	if err := registry.ReporterSetResult(reporter, res); err != nil {
		run.log.Warn("%s: persisting the result failed: %v", pcacheSweepOpID, err)
	}
	return nil
}

// sweep walks the question rows, the product rows and the pointer rows, in
// that order, one page at a time.
func (r *pcacheSweepRun) sweep(ctx context.Context) (pcacheSweepResult, error) {
	if n, err := r.store.CountPrefix(database.PCachePrefix); err == nil {
		r.total.Store(n)
	}
	r.log.Info("starting: dry_run=%t rows=%d", r.dry, r.total.Load())
	for _, prefix := range []string{database.PCacheQuestionPrefix, database.PCacheASINPrefix} {
		if err := r.walk(ctx, prefix, r.sweepAnswerPage); err != nil {
			return pcacheSweepResult{}, err
		}
	}
	if err := r.walk(ctx, database.PCacheBookPrefix, r.sweepPointerPage); err != nil {
		return pcacheSweepResult{}, err
	}
	return pcacheSweepResult{
		DryRun: r.dry, Questions: r.questions.Load(), Products: r.products.Load(), Pointers: r.pointers.Load(),
		Expired: r.expired.Load(), ExpiredByReason: r.byReason, ExpiredDeleted: r.expiredDeleted.Load(),
		ExpiredKept: r.expiredKept.Load(), OrphanPointers: r.orphans.Load(), OrphanPointersDeleted: r.orphansDeleted.Load(),
		PointerLookupErrors: r.lookupErrs.Load(), DeleteErrors: r.delErrs.Load(),
	}, nil
}

// walk pages through prefix and hands each page to fn. Deleting rows of a
// page never disturbs the walk: the cursor is the last key read.
func (r *pcacheSweepRun) walk(ctx context.Context, prefix string, fn func(context.Context, []database.KVPair) error) error {
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pairs, next, err := r.store.ScanPrefixPage(prefix, after, pcacheSweepPageSize)
		if err != nil {
			return fmt.Errorf("%s: scan %s: %w", pcacheSweepOpID, prefix, err)
		}
		if err := fn(ctx, pairs); err != nil {
			return err
		}
		done := r.done.Add(int64(len(pairs)))
		total := max(r.total.Load(), done)
		_ = r.reporter.UpdateProgress(int(done), int(total), fmt.Sprintf("Swept %d/%d shared cache rows (expired=%d orphan pointers=%d)",
			done, total, r.expired.Load(), r.orphans.Load()))
		if next == "" {
			return nil
		}
		after = next
	}
}

// sweepAnswerPage judges one page of question or product rows and, on
// apply, deletes the dead ones after re-reading each.
func (r *pcacheSweepRun) sweepAnswerPage(_ context.Context, pairs []database.KVPair) error {
	var dead []string
	for _, kv := range pairs {
		switch database.PCacheKindOf(kv.Key) {
		case database.PCacheKindQuestion:
			r.questions.Add(1)
		case database.PCacheKindProduct:
			r.products.Add(1)
		default:
			continue // not a row family this pass owns
		}
		if isDead, reason := database.PCacheRowDead(kv.Key, kv.Value, r.rule, r.now); isDead {
			r.expired.Add(1)
			r.byReason[reason]++
			dead = append(dead, kv.Key)
		}
	}
	if r.dry || len(dead) == 0 {
		return nil
	}
	// Re-judge each row at delete time: the page is a snapshot and the cache
	// is live, so a question asked again since the read has a fresh answer
	// that must survive.
	confirmed := make([]string, 0, len(dead))
	for _, k := range dead {
		v, err := r.store.GetRaw(k)
		if err != nil {
			r.delErrs.Add(1)
			r.log.Warn("re-check read failed, row kept: key=%s err=%v", logger.SanitizeLogValue(k), err)
			continue
		}
		if v == nil {
			continue // already gone
		}
		if stillDead, _ := database.PCacheRowDead(k, v, r.rule, time.Now()); !stillDead {
			r.expiredKept.Add(1)
			continue
		}
		confirmed = append(confirmed, k)
	}
	r.deleteBatch(confirmed, &r.expiredDeleted)
	return nil
}

// sweepPointerPage resolves the books of one page of pointer rows on a
// bounded pool and, on apply, deletes the rows whose book is absent.
func (r *pcacheSweepRun) sweepPointerPage(ctx context.Context, pairs []database.KVPair) error {
	orphan := make([]bool, len(pairs))
	g, _ := errgroup.WithContext(ctx)
	g.SetLimit(pcacheSweepResolveConcurrency)
	for i, kv := range pairs {
		if database.PCacheKindOf(kv.Key) != database.PCacheKindPointer {
			continue
		}
		r.pointers.Add(1)
		bookID := strings.TrimPrefix(kv.Key, database.PCacheBookPrefix)
		g.Go(func() error {
			if strings.TrimSpace(bookID) == "" {
				r.lookupErrs.Add(1) // cannot be resolved, so never deleted
				return nil
			}
			book, err := r.store.GetBookByID(bookID)
			switch {
			case err != nil:
				// NOT an orphan: a store fault is unknowable, not absent.
				r.lookupErrs.Add(1)
			case book == nil:
				r.orphans.Add(1)
				orphan[i] = true
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if r.dry {
		return nil
	}
	keys := make([]string, 0, len(pairs))
	for i, kv := range pairs {
		if orphan[i] {
			keys = append(keys, kv.Key)
		}
	}
	r.deleteBatch(keys, &r.orphansDeleted)
	return nil
}

// deleteBatch deletes keys in one batch and counts them on ok, or on
// r.delErrs when the batch fails. Every key must be in the pcache: family.
func (r *pcacheSweepRun) deleteBatch(keys []string, ok *atomic.Int64) {
	if len(keys) == 0 {
		return
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, database.PCachePrefix) {
			// Unreachable: every key came from a pcache: prefix scan. Refuse
			// the whole batch rather than delete outside the family.
			r.delErrs.Add(int64(len(keys)))
			r.log.Error("refusing to delete outside the pcache: family: key=%s", logger.SanitizeLogValue(k))
			return
		}
	}
	sort.Strings(keys)
	if err := r.store.DeleteRawBatch(keys); err != nil {
		r.delErrs.Add(int64(len(keys)))
		r.log.Warn("batch delete of %d rows failed: %v", len(keys), err)
		return
	}
	ok.Add(int64(len(keys)))
}
