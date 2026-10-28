// file: internal/plugins/maintenance/retired_versions_fixer.go
// version: 1.6.3
// guid: b1bac43f-5852-47a1-abc2-fe00e90a031a
// last-edited: 2026-10-28

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/boundedcall"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// retiredVersionsFixerID is the Repairs-lane id of the retired-versions
// preview fixer. The plan names it `retired-versions`, and the decisions
// store files its rejects under op_kind `repairs:retired-versions`.
const retiredVersionsFixerID = "retired-versions"

// retiredMinAgeDaysDefault is the owner's minimum age (2026-10-22): a trashed
// book is not a candidate until it has sat in the trash this many days, a
// waiting period in which a mistaken delete is likely to be noticed. It says
// nothing about merge undo: a combine journal does not expire, so a book can be
// absorbed by a combine that is still undoable long after this age passes (see
// the combine_undo_aware flag). The min_age_days param overrides it (0 turns the
// gate off).
const retiredMinAgeDaysDefault = 14

// Categories of a retired-versions row. Each row carries exactly one, as its
// Row.Class (so ByClass counts every row and the rows endpoint filters on it).
// The categories that will not be retired are also its Row.Skipped kind, so
// skipped_by_kind carries them too; the two that WOULD be retired are
// applicable rows.
//
// Precedence, first match wins. The cheap structural facts (the version
// group, the target, the shared paths, the lexical iTunes counts) are
// gathered for every row, so a row carries its target and its evidence
// whatever category it lands in. The reads that cost a store or disk round
// trip (the stat of a no-target row's files, the owner-veto lookup, the
// symlink-resolved iTunes check, the owner-manual check) happen at their own
// gate, so a row that an earlier gate decided never pays for them.
//
// A book that an unreversed combine journal names as absorbed is NOT a
// category: it is classified by the rules below like any other book and
// carries the flag combine_undo_aware (Current, plus an evidence line naming
// the journal), because the owner's decision of 2026-10-22 is to make the
// cleanup undo-aware rather than to hold those books. Retiring one is meant to
// be reversible by undoing the combine (the apply side, a later PR, journals
// the retire against the combine journal and UndoCombine reverses it first).
// The journal index is read once per run; a read that fails fails the plan,
// because no row can be flagged without it. Only combine journals
// ("merge:combine-journal:") are read: dedup auto-merge journals
// ("dedup:automerge:") live in the embedding store, which the plugin's deps do
// not expose, and carry their own undo (UnmergeAuto restores both book rows
// from their book_ver snapshots); the apply PR must read them too, and until
// then the minimum age is all that stands between a book they name and a
// retire.
//
//  1. trash_labelled_deleted: library_state is "deleted". Three paths write
//     that label (a merge, a manual delete, a bulk delete), so it does not
//     say the owner deleted the book on purpose; the row is left alone.
//  2. no_book_files: the book owns no book_file row, so there is nothing to
//     retire. It needs no retire to be purged.
//  3. target_ambiguous: the version group has live members but no single
//     primary (versionprimary.Incumbent returns nil). Never guessed.
//  4. no_target_files_missing / no_target_files_present: no live target (no
//     live group member, no group, and no live book at the end of its
//     merged-into chain). NOT retired: the book stays in the trash. Split by
//     whether any of its files is still on disk (an os.Stat per file; a stat
//     that cannot be answered counts the file as present and is reported as
//     rows_stat_unknown).
//  5. owner_vetoed: the owner rejected this book's retired-versions proposal
//     (a live reject under op_kind repairs:retired-versions with the book as
//     the entity), or rejected joining this book and its target
//     (decisions.PairVetoed, same_work or same_copy). Every stored id of a
//     reject is resolved through the retired-version index and the merge
//     survivor, and the reject keeps the books each end went through, so it
//     also holds a book that was merged into, retired onto or absorbed by the
//     target after the owner rejected it (internal/decisions, Vetoes.PairVetoed).
//  6. owner_veto_unresolvable: a reject that could touch this pair names an id
//     that cannot be resolved to one book (a shell whose retired versions name
//     two holders, a merge chain that loops), or the shell or its target is
//     such an id. NOT retired: a reject that cannot be ruled out holds the row,
//     and the reason lists every such id. Only the rows that touch one are
//     held; the others are planned as usual.
//  7. itunes_needs_owner_approval: a file of the book OR of its target sits
//     under the frozen books/itunes/** tree, symlinks resolved. The owner
//     approves these separately.
//  8. owner_manual_only: the book OR its target is Doctor Who / Big Finish /
//     Torchwood by path, series, title, credits, transcribed fields or a
//     franchise tag (repairs.GuardOwnerManualBooks, the framework guard's own
//     check, trashed books included: the guard RunPlan runs passes over a
//     trashed book, so the fixer asks it itself). The owner applies these by
//     hand; without this category such a row would be counted would_retire and
//     then skipped by the framework's guard.
//  9. age_unknown / too_recent: soft-deleted less than min_age_days ago, or no
//     soft-delete time on the row.
//  10. has_listening_state / listening_state_unknown: any user has listening
//     state on the book, or it could not be read (fails closed).
//  11. would_retire_same_path: at least one row's path equals a live row's
//     path. Retired anyway; both rows are kept.
//  12. would_retire: everything else.
//
// The plan's rule 5 (a content-hash match between the shell and its target)
// is deferred to a later PR; so is rule 2 (merge.ResolveMergeSurvivor).
const (
	retCatTrashLabelled    = "trash_labelled_deleted"
	retCatNoFiles          = "no_book_files"
	retCatAmbiguous        = "target_ambiguous"
	retCatNoTargetMissing  = "no_target_files_missing"
	retCatNoTargetPresent  = "no_target_files_present"
	retCatVetoed           = "owner_vetoed"
	retCatVetoUnresolvable = "owner_veto_unresolvable"
	retCatOwnerManual      = "owner_manual_only"
	retCatITunes           = "itunes_needs_owner_approval"
	retCatAgeUnknown       = "age_unknown"
	retCatTooRecent        = "too_recent"
	retCatListening        = "has_listening_state"
	retCatListeningUnknown = "listening_state_unknown"
	retCatSamePath         = "would_retire_same_path"
	retCatWouldRetire      = "would_retire"
	// retCatAlreadyRetired is a book whose file records are already retired
	// onto a live book (a finished apply): it owns no live row, and a
	// retired version names it as its shell. Skipped, so a second plan and a
	// second apply count it done and write nothing. Only a book a retired
	// version names AND that version holds rows for counts: a version left
	// empty by a failed commit does not make its shell retired.
	retCatAlreadyRetired = "already_retired"
	// retCatTailPending is an already-retired book whose external-id,
	// merged-into or chain steps a cut-off apply left unfinished, onto a live
	// holder. It is applicable, so a FRESH plan can finish it; Apply writes
	// only that tail (no version, no rows). A re-plan of a row the cut-off run
	// was planned as returns it with the planned fingerprint (see Replan).
	retCatTailPending = "retire_tail_pending"
	// retCatCombineLinkPending is the Replan-only hold on a combine_undo_aware
	// row while apply is on: retiring it must be reversible by undoing its
	// combine (plan section 6.5), and that link is not built yet.
	retCatCombineLinkPending = "combine_undo_link_pending"
	// retCatGone is only ever produced by a re-plan: the book left the trash
	// (restored or purged) after the plan.
	retCatGone = "gone"
	// retCatError is a row whose reads failed; the reason says which.
	retCatError = "error"

	// retFlagCombineUndoAware is the row flag (Row.Flags) of a book an unreversed
	// combine journal names as absorbed. It rides every category; the plan's
	// by_flag count and the "flag:combine_undo_aware" filter list those rows.
	retFlagCombineUndoAware = "combine_undo_aware"
)

// How a target was found.
const (
	retMethodMergedInto   = "merged_into"
	retMethodGroupPrimary = "group_primary"
)

// Version-group shapes of a row (Current["group_class"]).
const (
	retGroupWithLive    = "group_with_live_member"
	retGroupWithoutLive = "group_without_live_member"
	retGroupNone        = "no_group"
)

// retiredEntityBook is the decisions store's entity type for a book.
const retiredEntityBook = "book"

// errRetiredApplyDisabled is what Apply returns while the feature flag is off.
// The apply routes and the engine refuse a PlanOnly fixer before they reach
// Apply, so this is the last lock, not the first.
var errRetiredApplyDisabled = errors.New("retired-versions: apply is disabled; set retire_versions_apply_enabled (env RETIRE_VERSIONS_APPLY_ENABLED) to enable it. This fixer only previews while the flag is off")

// retiredMergedHops caps the merged-into chain, as the merge package's own
// forward resolution does.
const retiredMergedHops = 10

// retiredStatMaxAbandoned caps how many of ONE run's timed-out file stats may
// still be hung. A stat that times out is abandoned, not stopped (boundedcall.Call):
// its goroutine, and the OS thread blocked in the syscall, lasts until the
// mount answers. Past this many at once the mount is dead and continuing would
// only stack threads (48 workers on a dead share would pass the runtime's 10k
// thread limit), so the plan fails instead. Per run, so a stat that never
// returns does not fail every later run; the stragglers of earlier runs are
// reported through retiredStatsAbandoned. At most this cap plus one per worker
// can be abandoned, because a worker checks the count after each give-up. A
// var for tests, read once per run.
var retiredStatMaxAbandoned int64 = 8

// retiredStatsAbandoned is the process-wide gauge of abandoned file stats whose
// goroutine has not yet returned, across every run. It gates nothing; a plan
// logs it at start when non-zero.
var retiredStatsAbandoned atomic.Int64

// Exported as audiobook_organizer_ops_abandoned_calls{source="retired_versions_stat"},
// registered when the fixer is built at server start (newRetiredVersionsFixer),
// so the series reads 0 until a stat is abandoned. Not from init(): see
// repairs.registerAbandonedGaugeOnce (the meter's instrumentation version is
// read when opsmetrics.Default() first runs, and package init is before
// telemetry.SetVersion).
var retiredAbandonedGaugeOnce sync.Once

// registerAbandonedGauge puts retiredStatsAbandoned on rec's ops.abandoned_calls.
func registerAbandonedGauge(rec *opsmetrics.Recorder) {
	rec.RegisterAbandoned(opsmetrics.SourceRetiredVersionsStat, &retiredStatsAbandoned)
}

// errRetiredUndoRead fails a plan whose combine journals cannot be read: which
// rows carry the combine_undo_aware flag depends on them, so a partial preview
// would show some combine-absorbed rows without it.
var errRetiredUndoRead = errors.New("retired-versions: the combine journals cannot be read")

// errRetiredStatCap fails a plan whose file stats are hanging in bulk.
var errRetiredStatCap = errors.New("retired-versions: too many file stats are hung")

// retiredVersionsReader is every read the fixer makes from the ops store, and
// nothing else. Naming it keeps the preview's "no writes to books or
// book_file rows" a compile-time fact: none of these methods writes. OpsStore
// is a superset, so the plugin's deps satisfy it.
type retiredVersionsReader interface {
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	GetBookFilesForIDsCore(bookIDs []string) (map[string][]database.BookFileCore, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	// GetOperationChanges reads a retire op's journal for its plan records (the
	// external ids a retire planned to move, retirePlanRecord).
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
}

// retiredVersionsFixer moves every soft-deleted book's file rows onto the
// live book it was a loser of, as a hidden "retired version" (docs: the
// retired-version plan; PR 1 is the preview, PR 7 the Apply). Plan and Replan
// are read-only and classify each trashed book and name its target. Apply
// (retired_versions_apply.go) writes only while retire_versions_apply_enabled
// is on; with it off (the default) the fixer is PlanOnly: the routes and the
// engine refuse an apply, the lane offers none, and Apply refuses again
// itself. Manual runs only: nothing schedules it.
//
// The plan pass is one classification per trashed book, fanned out over a
// bounded worker pool sized to runtime.NumCPU(). Each worker writes only its
// own rows[i]. What the workers share is read once per run through a
// once-per-key cache: a version group's members, any book read by id (a
// liveness check, a merged-into hop, a target), each target's file counts and
// iTunes verdict, and the combine-journal index. A shell's own file rows and
// its path owners are read per shell and not cached. The caches live exactly
// as long as one run.
type retiredVersionsFixer struct {
	p *Plugin
	// now is the clock the age gate reads; tests fix it.
	now func() time.Time
	// statTimeout bounds one stat of a library file, so a stuck share cannot
	// hold a worker for the life of the plan.
	statTimeout time.Duration
	// stat is os.Stat; tests swap it to make a stat fail or hang. A run reads
	// it once, before any bounded call starts.
	stat func(string) (os.FileInfo, error)
	// applyEnabled reads the retire_versions_apply_enabled flag (config's
	// RLock accessor); tests swap it so they never touch the global config.
	applyEnabled func() bool
	// retireWriterFor builds the journaled write surface of one retire from the
	// run's Writer; tests wrap it. See retireWriter.
	retireWriterFor func(w *repairs.Writer) (retireWriter, error)
}

func newRetiredVersionsFixer(p *Plugin) *retiredVersionsFixer {
	retiredAbandonedGaugeOnce.Do(func() { registerAbandonedGauge(opsmetrics.Default()) })
	f := &retiredVersionsFixer{p: p, now: time.Now, statTimeout: 15 * time.Second, stat: os.Stat,
		applyEnabled: config.RetireVersionsApplyEnabled}
	f.retireWriterFor = f.newJournaledRetireWriter
	return f
}

// applyOn reports whether the apply flag is on. A fixer built without the
// field (a test literal) reads the flag as off.
func (f *retiredVersionsFixer) applyOn() bool { return f.applyEnabled != nil && f.applyEnabled() }

var (
	_ repairs.Fixer           = (*retiredVersionsFixer)(nil)
	_ repairs.NoScanStandDown = (*retiredVersionsFixer)(nil)
	_ repairs.PlanOnly        = (*retiredVersionsFixer)(nil)
)

func (f *retiredVersionsFixer) ID() string { return retiredVersionsFixerID }
func (f *retiredVersionsFixer) Title() string {
	if f.applyOn() {
		return "Retired versions"
	}
	return "Retired versions (preview)"
}
func (f *retiredVersionsFixer) Description() string {
	if f.applyOn() {
		return "Moves the file records of each book in the trash onto the live book it lost to, as a hidden " +
			"retired version, never deleting one (an Apply is undone from the Operations page). The plan " +
			"classifies every trashed book by what retiring it would do and says why a book would NOT be " +
			"retired: no live target, trashed under min_age_days ago (14 by default; the min_age_days param " +
			"overrides it, 0 turns the gate off), has listening state, files under the iTunes library on either " +
			"side, Doctor Who / Big Finish / Torchwood on either side (the owner applies those by hand), " +
			"labelled deleted, or an owner veto on the pair. A book that a combine journal not yet undone names " +
			"as absorbed is flagged combine_undo_aware and is held (retry_later) at apply until its retire can be " +
			"undone with that combine. A book already retired shows as already_retired and is not written again; one whose retire was cut off part way shows as retire_tail_pending and an Apply finishes just the unfinished steps."
	}
	return "Preview only. Classifies every book in the trash by what retiring it would do: its file records " +
		"moved onto the live book it lost to, as a hidden retired version, never deleted. Each row names its " +
		"target and says why a book would NOT be retired: no live target, trashed under min_age_days ago " +
		"(14 by default; the min_age_days param overrides it, 0 turns the gate off), has listening state, " +
		"files under the iTunes library on either side, Doctor Who / Big Finish / Torchwood on either side " +
		"(the owner applies those by hand), labelled deleted, or an owner veto on the pair. A book that a " +
		"combine journal not yet undone names as absorbed is classified like any other and flagged " +
		"combine_undo_aware: its retire is meant to be reversible by undoing that combine, and the plan " +
		"counts those rows separately. The plan's rule 5 (a content-hash match between the book and its " +
		"target) is not applied yet. Nothing is written; Apply is switched off (retire_versions_apply_enabled), " +
		"so this fixer has no Apply control."
}

// NoScanStandDown: while apply is off the preview writes nothing through the
// Writer and nothing a library scan writes, so a run of it never needs to park
// a running scan. With apply on it moves file rows a scan also writes, so it
// takes the stand-down like every other Repairs apply (and its Writer is not
// restricted to nothing).
func (f *retiredVersionsFixer) NoScanStandDown() bool { return !f.applyOn() }

// PlanOnly: while the apply flag is off, Apply is refused by the routes,
// hidden by the lane, and refused again by Apply itself.
func (f *retiredVersionsFixer) PlanOnly() bool { return !f.applyOn() }

// retiredVersionsParams are the plan's own params.
type retiredVersionsParams struct {
	// MinAgeDays overrides the 14-day minimum age. Zero is allowed (no gate).
	MinAgeDays *int `json:"min_age_days,omitempty"`
}

func parseRetiredVersionsParams(raw json.RawMessage) (time.Duration, error) {
	days := retiredMinAgeDaysDefault
	if len(raw) > 0 && string(raw) != "null" {
		var p retiredVersionsParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return 0, fmt.Errorf("%s: bad params: %w", retiredVersionsFixerID, err)
		}
		if p.MinAgeDays != nil {
			if *p.MinAgeDays < 0 {
				return 0, fmt.Errorf("%s: min_age_days must not be negative", retiredVersionsFixerID)
			}
			days = *p.MinAgeDays
		}
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

// onceVal is one lazily loaded, shared value of a retiredRun cache.
type onceVal[T any] struct {
	once sync.Once
	v    T
	err  error
}

// cachedOnce loads key once across every worker and hands all of them the
// same result, error included.
func cachedOnce[T any](m *sync.Map, key string, load func() (T, error)) (T, error) {
	e, _ := m.LoadOrStore(key, &onceVal[T]{})
	ov := e.(*onceVal[T])
	ov.once.Do(func() { ov.v, ov.err = load() })
	return ov.v, ov.err
}

// retiredRun is the state of one Plan or one Replan: its readers, the age
// gate and the caches. The caches live exactly as long as the run, so a plan
// is a consistent snapshot and a re-plan never reads a stale one.
type retiredRun struct {
	f        *retiredVersionsFixer
	store    retiredVersionsReader
	paths    database.BookFilePathLookup
	probe    *merge.UserStateProbe
	vetoes   OwnerVetoReader
	journals CombineJournalScanner
	// resolver settles symlinks for the iTunes check; its cache is shared by
	// every worker and is safe for that.
	resolver *repairs.PathResolver
	// guard, tags and series are the framework guard's inputs, the ones
	// runRepairsPlan hands RunPlan, for the owner-manual check.
	guard  repairs.GuardReader
	tags   repairs.GuardTagReader
	series repairs.SeriesNamer
	minAge time.Duration
	now    time.Time

	// stat, bound and maxAbandoned are read once, so an abandoned stat never
	// reads a field a test is restoring.
	stat         func(string) (os.FileInfo, error)
	bound        time.Duration
	maxAbandoned int64
	// abandoned counts this run's timed-out stats that have not returned.
	abandoned atomic.Int64

	// retired reads the retired side of the book_file table; nil when the store
	// cannot (a test double), in which case no book is reported already retired.
	retired database.RetiredBookFileReader
	// detail makes buildRow attach a retiredDetail (the shell's rows, the
	// resolved target, the age gate) to the row. Only a Replan or an under-lock
	// re-check sets it: a whole-library Plan holds every row in memory until
	// the engine stores it, and must not carry 51,000 shells' file rows.
	detail bool

	books   sync.Map // book id -> *onceVal[*database.Book]
	groups  sync.Map // version group id -> *onceVal[[]database.Book]
	targets sync.Map // target book id -> *onceVal[*retiredTarget]
	undo    sync.Map // "journals" -> *onceVal[map[string][]string]
	// plans is the session's (or plan's) cache of retire plan records by op id;
	// see retiredShared.plans.
	plans *sync.Map
	// sessionUndo is the unreversed-combine index an apply run read once
	// (retiredSession); nil outside one, where undo above reads it lazily.
	sessionUndo *retiredUndo
}

func (f *retiredVersionsFixer) newRun(ctx context.Context, params json.RawMessage) (*retiredRun, error) {
	minAge, err := parseRetiredVersionsParams(params)
	if err != nil {
		return nil, err
	}
	return f.newRunWithMinAge(ctx, minAge)
}

// retiredShared is what every row of one run needs and no row changes: the
// series names the owner-manual check reads, the listing of users the
// listening-state probe asks, and the index of unreversed combine journals.
// The owner's rejects are NOT here: they are resolved against the books as they
// are now, and an apply changes that between rows (newRunWithMinAge).
// Each is a whole-library read (every series, every user, every combine
// journal), so a run reads them ONCE. An apply row reads them from its
// retiredSession; a plan reads them once for its single run.
type retiredShared struct {
	series  repairs.SeriesNamer
	probe   *merge.UserStateProbe
	journal CombineJournalScanner
	// plans caches the plan records of each retire op a row reads, by op id
	// (retiredRun.opPlanRecords): read lazily, at most once per op per session.
	plans sync.Map
}

// loadShared reads the run-invariant state. Without the series names the
// owner-manual check cannot see a Doctor Who book whose path does not say so,
// so a failed read fails the run (the plan op does the same).
func (f *retiredVersionsFixer) loadShared(ctx context.Context) (*retiredShared, error) {
	ops := f.p.deps.OpsStore()
	users := f.p.deps.MergeUserStateStore()
	if ops == nil || users == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	all, err := ops.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("%s: list series for the owner-manual check: %w", retiredVersionsFixerID, err)
	}
	journals := f.p.deps.CombineJournals()
	if journals == nil {
		return nil, fmt.Errorf("%s: the combine journal cannot be read on this server; refusing to plan without it", retiredVersionsFixerID)
	}
	probe, err := merge.NewUserStateProbe(users)
	if err != nil {
		return nil, fmt.Errorf("%s: list users for the listening-state check: %w", retiredVersionsFixerID, err)
	}
	return &retiredShared{series: repairs.SeriesNamesFrom(all), probe: probe, journal: journals}, nil
}

// newRunWithMinAge is newRun for a caller that already holds the age gate: an
// Apply, which is handed no params and takes the gate its Replan used from the
// row's retiredDetail. Inside an apply run (BeginApply) the run-invariant state
// is the session's, read before the first row and so never under the merge
// lock; otherwise it is read now.
func (f *retiredVersionsFixer) newRunWithMinAge(ctx context.Context, minAge time.Duration) (*retiredRun, error) {
	ops := f.p.deps.OpsStore()
	paths := f.p.deps.FragmentRepairReader()
	if ops == nil || paths == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	var sh *retiredShared
	sess := retiredSessionFrom(ctx)
	if sess != nil {
		if sess.err != nil {
			return nil, sess.err
		}
		sh = sess.shared
	} else {
		var err error
		if sh, err = f.loadShared(ctx); err != nil {
			return nil, err
		}
	}
	// The owner's rejects are read for THIS run, never shared across the rows of
	// an apply session: every stored id is resolved through the retired-version
	// index and the merge chain, and the row before this one may just have
	// retired a shell a reject names. A snapshot resolved before that retire
	// would not see that the reject now reaches the holder, and the veto would be
	// dropped (decisions.RowCache keeps only the raw log rows, so the scan is
	// still shared). The reader loads now, so a run that cannot READ the rejects
	// (the store or a journal failed) is refused whole instead of becoming a plan
	// of error rows. An id that cannot be resolved is not such a failure: the rows
	// it touches are held (owner_veto_unresolvable), the rest planned.
	vetoes := f.p.deps.OwnerVetoes()
	if vetoes == nil {
		return nil, fmt.Errorf("%s: the owner's rejects cannot be read on this server; refusing to plan without them", retiredVersionsFixerID)
	}
	if err := vetoes.Load(ctx); err != nil {
		return nil, fmt.Errorf("%s: read the owner's rejects: %w", retiredVersionsFixerID, err)
	}
	now := time.Now
	if f.now != nil {
		now = f.now
	}
	stat := f.stat
	if stat == nil {
		stat = os.Stat
	}
	bound := f.statTimeout
	if bound <= 0 {
		bound = 15 * time.Second
	}
	run := &retiredRun{f: f, store: ops, paths: paths, probe: sh.probe, vetoes: vetoes, journals: sh.journal,
		resolver: repairs.NewPathResolver(), guard: ops, tags: f.p.repairsGuardTags(), series: sh.series,
		minAge: minAge, now: now(), plans: &sh.plans,
		stat: stat, bound: bound, maxAbandoned: retiredStatMaxAbandoned}
	if sess != nil {
		run.sessionUndo = &sess.undo
	}
	if rr, ok := database.AsCapability[database.RetiredBookFileReader](ops); ok {
		run.retired = rr
	}
	return run, nil
}

// book reads a book once per run; (nil, nil) is a book that does not exist.
func (r *retiredRun) book(id string) (*database.Book, error) {
	return cachedOnce(&r.books, id, func() (*database.Book, error) { return r.store.GetBookByID(id) })
}

// retiredLive reports whether b is a book that is neither soft-deleted nor
// labelled into the trash (database.IsInTrash covers both).
func retiredLive(b *database.Book) bool { return b != nil && !database.IsInTrash(b) }

// isLive reports whether id is a live book (retiredLive).
func (r *retiredRun) isLive(id string) (bool, error) {
	b, err := r.book(id)
	if err != nil {
		return false, fmt.Errorf("read book %s: %w", id, err)
	}
	return retiredLive(b), nil
}

func (r *retiredRun) group(gid string) ([]database.Book, error) {
	return cachedOnce(&r.groups, gid, func() ([]database.Book, error) { return r.store.GetBooksByVersionGroup(gid) })
}

// filesOf reads one book's file rows (the Core projection).
func (r *retiredRun) filesOf(id string) ([]database.BookFileCore, error) {
	m, err := r.store.GetBookFilesForIDsCore([]string{id})
	if err != nil {
		return nil, err
	}
	return m[id], nil
}

// retiredTarget is what a plan needs of a target book, read once per target
// however many shells point at it.
type retiredTarget struct {
	title   string
	files   int
	missing int
	// itunes is set when a file of the target sits under the frozen iTunes
	// tree (or its location could not be settled); itunesWhy says which.
	itunes    bool
	itunesWhy string
	// manualWhy is non-empty when the target is Doctor Who / Big Finish /
	// Torchwood (or the check could not settle it); the owner applies those.
	manualWhy string
}

func (r *retiredRun) target(id string) (*retiredTarget, error) {
	return cachedOnce(&r.targets, id, func() (*retiredTarget, error) {
		b, err := r.book(id)
		if err != nil {
			return nil, fmt.Errorf("read target %s: %w", id, err)
		}
		if b == nil {
			return nil, fmt.Errorf("target %s does not exist", id)
		}
		files, err := r.filesOf(id)
		if err != nil {
			return nil, fmt.Errorf("read book_file rows of target %s: %w", id, err)
		}
		t := &retiredTarget{title: b.Title, files: len(files)}
		//lint:ignore SA1019 the legacy book.itunes_path still marks an iTunes book until it is drained; the iTunes gate must read it or it would let an iTunes book be retired
		paths := []string{b.FilePath, retiredDeref(b.ITunesPath)}
		for i := range files {
			if files[i].Missing {
				t.missing++
			}
			paths = append(paths, files[i].FilePath, files[i].ITunesPath)
		}
		t.itunes, t.itunesWhy = r.itunesVerdict(id, paths)
		if t.manualWhy, err = r.ownerManual(id); err != nil {
			return nil, fmt.Errorf("owner-manual check of target %s: %w", id, err)
		}
		return t, nil
	})
}

// itunesVerdict runs the Repairs framework's own hands-off check over paths,
// symlinks resolved (a library symlink into books/itunes/** is under it however
// it is spelled). A path whose location could not be settled counts as a hit:
// it may be under the tree, and a preview does not guess it is not.
func (r *retiredRun) itunesVerdict(id string, paths []string) (bool, string) {
	kind, _ := repairs.GuardBookPathsWith(r.resolver, id, paths, "")
	switch kind {
	case repairs.SkipITunes:
		return true, "has a file under the iTunes library (books/itunes/**)"
	case repairs.SkipGuardUnreadable:
		return true, "could not tell whether a file is under the iTunes library (a path would not resolve)"
	}
	return false, ""
}

// ownerManual runs the framework guard's owner-manual check over one book,
// trashed or not, with the same inputs RunPlan's guard gets (paths, series,
// title, credits, transcribed fields, franchise tags), and returns why the
// owner applies it by hand ("" when it is clear). A path the guard could not
// settle comes back as a reason too: it may name a Doctor Who folder behind a
// link, and a preview does not guess it does not. A read error is returned.
func (r *retiredRun) ownerManual(id string) (string, error) {
	kind, why, err := repairs.GuardOwnerManualBooks(r.guard, r.tags, r.series, r.resolver, []string{id})
	if err != nil {
		return "", err
	}
	if kind == "" {
		return "", nil
	}
	return why, nil
}

// undoShells reads the combine-journal index once per run: absorbed book id ->
// the unreversed journals that name it.
func (r *retiredRun) undoShells() (map[string][]string, error) {
	if r.sessionUndo != nil {
		return r.sessionUndo.idx, r.sessionUndo.err
	}
	return cachedOnce(&r.undo, "journals", func() (map[string][]string, error) {
		idx, err := merge.UnreversedCombineShells(r.journals)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errRetiredUndoRead, err)
		}
		return idx, nil
	})
}

// retVetoShellRejected is the owner_veto_family of a row held by the owner's
// reject of the shell's own proposal (not by a pair veto in a family).
const retVetoShellRejected = "shell_rejected"

// retVetoUnresolvable is the owner_veto_family of a row held because the
// owner's rejects could not be ruled out: an id they or the row name cannot be
// resolved to one book.
const retVetoUnresolvable = "unresolvable"

// vetoed returns why the owner's rejects hold the row of shell a retiring onto
// b: retVetoShellRejected when a live reject of a's repairs:retired-versions
// proposal exists (sticky: it holds whatever the target), else the op family in
// which the owner rejected joining a and b, else retVetoUnresolvable (with
// every id that could not be resolved) when a reject that could touch the pair
// cannot be ruled out, "" for none. A definite veto wins over an unresolvable
// one: the row is held either way, and the definite reason is the one the owner
// can act on. The pair is asked through the owner-veto reader, whose ids are
// resolved to the books that survive (and keep the books they went through), so
// a veto naming a retired, purged, merged-away or absorbed book still holds.
func (r *retiredRun) vetoed(a, b string) (string, []decisions.UnresolvedID, error) {
	key, err := decisions.RetiredVersionsSubjectKey(a)
	if err != nil {
		return "", nil, fmt.Errorf("fingerprint the proposal for %s: %w", a, err)
	}
	rejected, err := r.vetoes.SubjectRejected(decisions.OpKindRetiredVersions, key, "")
	if err != nil {
		return "", nil, fmt.Errorf("read the owner's rejects for %s: %w", a, err)
	}
	if rejected {
		return retVetoShellRejected, nil, nil
	}
	var unresolved []decisions.UnresolvedID
	seen := map[string]bool{}
	for _, fam := range []string{decisions.FamilySameWork, decisions.FamilySameCopy} {
		v, err := r.vetoes.PairVetoed(retiredEntityBook, a, b, fam)
		var ue *decisions.UnresolvableError
		if errors.As(err, &ue) {
			for _, u := range ue.IDs {
				if k := u.EntityType + "\x00" + u.ID; !seen[k] {
					seen[k] = true
					unresolved = append(unresolved, u)
				}
			}
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("read the owner's rejects for %s and %s: %w", a, b, err)
		}
		if v {
			return fam, nil, nil
		}
	}
	if len(unresolved) > 0 {
		sort.Slice(unresolved, func(i, j int) bool {
			if unresolved[i].EntityType != unresolved[j].EntityType {
				return unresolved[i].EntityType < unresolved[j].EntityType
			}
			return unresolved[i].ID < unresolved[j].ID
		})
		return retVetoUnresolvable, unresolved, nil
	}
	return "", nil, nil
}

// retiredUnresolvedDetail words the ids a reject could not be ruled out for.
func retiredUnresolvedDetail(ids []decisions.UnresolvedID) string {
	parts := make([]string, len(ids))
	for i, u := range ids {
		parts[i] = fmt.Sprintf("%s %s (%v)", u.EntityType, u.ID, u.Err)
	}
	return strings.Join(parts, "; ")
}

// logUnresolvedVetoIDs reports, once per plan, every stored id of the owner's
// rejects that could not be resolved (all of them, not the first), so an
// operator sees the whole set rather than finding them one row at a time.
func (r *retiredRun) logUnresolvedVetoIDs(rep registry.Reporter) {
	ids := r.vetoes.Unresolved()
	if len(ids) == 0 {
		return
	}
	_ = rep.Log(slog.LevelWarn, fmt.Sprintf("%d id(s) named by the owner's rejects cannot be resolved to one book; the rows that touch them are held as %s: %s",
		len(ids), retCatVetoUnresolvable, retiredUnresolvedDetail(ids)))
}

// retiredFacts is everything one book's category is decided from.
type retiredFacts struct {
	shell database.BookCore
	files []database.BookFileCore

	groupID    string
	groupClass string

	targetID     string
	targetMethod string
	target       *retiredTarget
	// ambiguous is why a group with live members yielded no single target.
	ambiguous string

	// sharedRows counts the shell's rows whose path a live book's row also
	// holds; sharedWith lists those live books.
	sharedRows int
	sharedWith []string
	// mergedLive is the live book the shell's merged-into chain ends at.
	// It is a target only when the shell also has a version group with a live
	// member; otherwise it is evidence on a no-target row, never a target
	// (the owner's answer: no live group member, or no group, is no target).
	mergedLive string
	// rule4 is the one live book that holds a row at EVERY path of the shell:
	// the plan's "same-path holder" heuristic, reported on a row with no
	// target and never used to pick one.
	rule4 string

	// itunesRows and itunesPIDRows are the cheap lexical counts, kept as
	// evidence; the gate itself uses the symlink-resolved check.
	itunesRows    int
	itunesPIDRows int
	// itunesSide and itunesWhy say which side tripped the iTunes gate
	// ("shell" or "target"; "" when neither did).
	itunesSide string
	itunesWhy  string

	missingFlag int
	// absentOnDisk and statUnknown are the stat counts, only computed for a
	// no-target row (-1 otherwise).
	absentOnDisk int
	statUnknown  int

	vetoFamily string
	// vetoUnresolved lists the ids that held the row as retCatVetoUnresolvable.
	vetoUnresolved []decisions.UnresolvedID
	// undoJournal lists the combine journals, not undone, that name the shell
	// as absorbed; a non-empty list is the combine_undo_aware flag.
	undoJournal []string
	// ownerManualSide and ownerManualWhy say which side tripped the
	// owner-manual check ("shell" or "target"; "" when neither did).
	ownerManualSide string
	ownerManualWhy  string
}

func retiredDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// classify reads everything a trashed book's category depends on and returns
// its row. A read that fails returns an error: the caller turns it into an
// error row, or (for an incomplete ownership lookup or a hung mount) fails the
// plan.
func (r *retiredRun) classify(ctx context.Context, shell database.BookCore) (repairs.Row, error) {
	files, err := r.filesOf(shell.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book_file rows of %s: %w", shell.ID, err)
	}
	if len(files) == 0 {
		// A book with no live row may be one a finished apply emptied: its rows
		// are retired onto a live book and a retired version names it as the
		// shell. That is "done", not "nothing to retire".
		if row, done, err := r.alreadyRetired(shell); err != nil {
			return repairs.Row{}, err
		} else if done {
			return row, nil
		}
	}
	fa := retiredFacts{shell: shell, files: files, groupClass: retGroupNone, absentOnDisk: -1, statUnknown: -1}
	idx, err := r.undoShells()
	if err != nil {
		return repairs.Row{}, err
	}
	fa.undoJournal = idx[shell.ID]
	for i := range files {
		if files[i].Missing {
			fa.missingFlag++
		}
	}
	members, err := r.loadGroup(&fa)
	if err != nil {
		return repairs.Row{}, err
	}
	if err := r.resolveTarget(&fa, members); err != nil {
		return repairs.Row{}, err
	}
	if fa.targetID != "" {
		if fa.target, err = r.target(fa.targetID); err != nil {
			return repairs.Row{}, err
		}
	}
	if err := r.readPathOwners(&fa); err != nil {
		return repairs.Row{}, err
	}
	r.countITunes(&fa)

	cat, why, err := r.decide(ctx, &fa)
	if err != nil {
		return repairs.Row{}, err
	}
	return r.buildRow(&fa, cat, why), nil
}

// loadGroup reads the shell's version group and sets its shape. A member is
// live by retiredLive, the same test isLive makes, so a member labelled into
// the trash without the deletion flag does not count.
func (r *retiredRun) loadGroup(fa *retiredFacts) ([]database.Book, error) {
	fa.groupID = retiredDeref(fa.shell.VersionGroupID)
	if fa.groupID == "" {
		return nil, nil
	}
	members, err := r.group(fa.groupID)
	if err != nil {
		return nil, fmt.Errorf("read version group %s: %w", fa.groupID, err)
	}
	fa.groupClass = retGroupWithoutLive
	for i := range members {
		if members[i].ID != fa.shell.ID && retiredLive(&members[i]) {
			fa.groupClass = retGroupWithLive
			break
		}
	}
	return members, nil
}

// resolveTarget finds the one live book the shell's rows would be retired
// onto: first the end of its merged-into chain, then its version group's
// primary. Both need a version group with a live member (the owner's answer
// on 2026-10-22); a merged-into chain on a shell without one is recorded as
// evidence only. A tie is ambiguous, never guessed. The plan's rule 2
// (merge.ResolveMergeSurvivor) is not used: without the journal's
// flag-holder follows it degrades to the USER-STATE redirect, which can name
// the flag holder rather than the book that kept the audio.
func (r *retiredRun) resolveTarget(fa *retiredFacts, members []database.Book) error {
	seen := map[string]bool{fa.shell.ID: true}
	cur := retiredDeref(fa.shell.MergedIntoBookID)
	for hop := 0; hop < retiredMergedHops && cur != "" && !seen[cur]; hop++ {
		seen[cur] = true
		b, err := r.book(cur)
		if err != nil {
			return fmt.Errorf("read merged-into book %s: %w", cur, err)
		}
		if b == nil {
			break
		}
		if retiredLive(b) {
			fa.mergedLive = cur
			if fa.groupClass == retGroupWithLive {
				fa.targetID, fa.targetMethod = cur, retMethodMergedInto
			}
			return nil
		}
		cur = retiredDeref(b.MergedIntoBookID)
	}
	if fa.groupClass != retGroupWithLive {
		return nil
	}
	var aliveErr error
	inc := versionprimary.Incumbent(members, func(id string) bool {
		live, err := r.isLive(id)
		if err != nil && aliveErr == nil {
			aliveErr = err
		}
		return live
	})
	if aliveErr != nil {
		return aliveErr
	}
	switch {
	case inc == nil:
		fa.ambiguous = "the version group has live members but no single primary"
	case database.IsInTrash(inc):
		fa.ambiguous = "the group's primary " + inc.ID + " is labelled into the trash"
	default:
		fa.targetID, fa.targetMethod = inc.ID, retMethodGroupPrimary
	}
	return nil
}

// readPathOwners finds, for each distinct path of the shell's rows, the live
// books that also hold a row there. The lookup is the complete multi-row one
// and fails closed: the single-owner index under-reports, which would turn a
// same-path row into a plain one. A holder that is itself in the trash does
// not count (two trashed shells at one path share nothing live).
func (r *retiredRun) readPathOwners(fa *retiredFacts) error {
	ownersAt := map[string][]string{}
	var order []string
	for i := range fa.files {
		p := fa.files[i].FilePath
		if p == "" {
			continue
		}
		if _, done := ownersAt[p]; done {
			continue
		}
		rows, err := database.BookFileRowsAtPathStrict(r.paths, p)
		if err != nil {
			return fmt.Errorf("who holds %q: %w", p, err)
		}
		owners := []string{}
		for j := range rows {
			// A retired row is a hidden version of a book, not a live file of
			// it: the holder of a retired row at this path does not hold the
			// path. Counting it would flip every sibling shell of a finished
			// retire to same-path, and change its fingerprint, after the first
			// shell of the group was retired.
			if rows[j].BookID == fa.shell.ID || rows[j].Retired {
				continue
			}
			live, err := r.isLive(rows[j].BookID)
			if err != nil {
				return err
			}
			if live && !retiredContains(owners, rows[j].BookID) {
				owners = append(owners, rows[j].BookID)
			}
		}
		sort.Strings(owners)
		ownersAt[p] = owners
		order = append(order, p)
	}
	union := map[string]bool{}
	for i := range fa.files {
		if owners := ownersAt[fa.files[i].FilePath]; len(owners) > 0 {
			fa.sharedRows++
			for _, o := range owners {
				union[o] = true
			}
		}
	}
	for o := range union {
		fa.sharedWith = append(fa.sharedWith, o)
	}
	sort.Strings(fa.sharedWith)
	// rule4: one live book at every path.
	if len(order) > 0 {
		common := append([]string(nil), ownersAt[order[0]]...)
		for _, p := range order[1:] {
			var next []string
			for _, o := range common {
				if retiredContains(ownersAt[p], o) {
					next = append(next, o)
				}
			}
			common = next
		}
		if len(common) == 1 {
			fa.rule4 = common[0]
		}
	}
	return nil
}

func retiredContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// countITunes counts, as evidence, the shell's rows that spell a path under
// the frozen iTunes tree or carry an iTunes persistent id. The gate does not
// use these counts: it uses the symlink-resolved check.
func (r *retiredRun) countITunes(fa *retiredFacts) {
	for i := range fa.files {
		if pathutil.UnderFrozenITunesTree(fa.files[i].FilePath) || pathutil.UnderFrozenITunesTree(fa.files[i].ITunesPath) {
			fa.itunesRows++
		}
		if fa.files[i].ITunesPersistentID != "" {
			fa.itunesPIDRows++
		}
	}
}

// shellITunes runs the resolved iTunes check over every path of the shell:
// the book's own two and each file row's two.
func (r *retiredRun) shellITunes(fa *retiredFacts) (bool, string) {
	//lint:ignore SA1019 the legacy book.itunes_path still marks an iTunes book until it is drained; the iTunes gate must read it or it would let an iTunes book be retired
	paths := []string{fa.shell.FilePath, retiredDeref(fa.shell.ITunesPath)}
	for i := range fa.files {
		paths = append(paths, fa.files[i].FilePath, fa.files[i].ITunesPath)
	}
	return r.itunesVerdict(fa.shell.ID, paths)
}

// decide applies the category precedence documented on the retCat constants.
func (r *retiredRun) decide(ctx context.Context, fa *retiredFacts) (cat, why string, err error) {
	if strings.EqualFold(strings.TrimSpace(retiredDeref(fa.shell.LibraryState)), "deleted") {
		return retCatTrashLabelled, "labelled \"deleted\" (the trash label; a merge, a manual delete and a bulk delete all write it), so it cannot be told from a deliberate delete", nil
	}
	if len(fa.files) == 0 {
		return retCatNoFiles, "the book owns no file records, so there is nothing to retire", nil
	}
	if fa.targetID == "" && fa.ambiguous != "" {
		return retCatAmbiguous, fa.ambiguous + "; no target is guessed", nil
	}
	if fa.targetID == "" {
		absent, unknown, err := r.countAbsent(ctx, fa.files)
		if err != nil {
			return "", "", err
		}
		fa.absentOnDisk, fa.statUnknown = absent, unknown
		if absent == len(fa.files) {
			return retCatNoTargetMissing, "no live target (" + r.noTargetWhy(fa) + "); every file is missing from disk; stays in the trash", nil
		}
		return retCatNoTargetPresent, "no live target (" + r.noTargetWhy(fa) + "); at least one file is still on disk; stays in the trash", nil
	}

	// From here the row has a target.
	fam, unresolved, err := r.vetoed(fa.shell.ID, fa.targetID)
	if err != nil {
		return "", "", err
	}
	if fam != "" {
		fa.vetoFamily, fa.vetoUnresolved = fam, unresolved
		switch fam {
		case retVetoShellRejected:
			return retCatVetoed, "the owner rejected retiring this book (a reject of its retired-versions proposal)", nil
		case retVetoUnresolvable:
			return retCatVetoUnresolvable, "a reject of the owner's that could touch this book and its target cannot be ruled out, because " +
				strconv.Itoa(len(unresolved)) + " id(s) cannot be resolved to one book: " + retiredUnresolvedDetail(unresolved), nil
		}
		return retCatVetoed, "the owner rejected joining this book and its target (" + fam + ")", nil
	}
	if hit, w := r.shellITunes(fa); hit {
		fa.itunesSide, fa.itunesWhy = "shell", w
		return retCatITunes, "the book " + w + "; retiring it needs the owner's approval", nil
	}
	if fa.target.itunes {
		fa.itunesSide, fa.itunesWhy = "target", fa.target.itunesWhy
		return retCatITunes, "its target " + fa.target.itunesWhy + "; retiring onto it needs the owner's approval", nil
	}
	if why, err := r.ownerManual(fa.shell.ID); err != nil {
		return "", "", fmt.Errorf("owner-manual check of %s: %w", fa.shell.ID, err)
	} else if why != "" {
		fa.ownerManualSide, fa.ownerManualWhy = "shell", why
		return retCatOwnerManual, "the book is Doctor Who / Big Finish / Torchwood; the owner applies these by hand (" + why + ")", nil
	}
	if fa.target.manualWhy != "" {
		fa.ownerManualSide, fa.ownerManualWhy = "target", fa.target.manualWhy
		return retCatOwnerManual, "its target is Doctor Who / Big Finish / Torchwood; the owner applies these by hand (" + fa.target.manualWhy + ")", nil
	}
	at := fa.shell.MarkedForDeletionAt
	switch {
	case at == nil || at.IsZero():
		return retCatAgeUnknown, "the row has no soft-delete time, so its age cannot be shown to be at least the minimum", nil
	case r.now.Sub(*at) < r.minAge:
		return retCatTooRecent, fmt.Sprintf("trashed %d day(s) ago; the minimum age is %d day(s)",
			int(r.now.Sub(*at).Hours()/24), int(r.minAge.Hours()/24)), nil
	}
	has, perr := r.probe.Has(fa.shell.ID)
	switch {
	case perr != nil:
		return retCatListeningUnknown, "listening state could not be read: " + perr.Error(), nil
	case has:
		return retCatListening, "a user has listening state on this book", nil
	case fa.sharedRows > 0:
		return retCatSamePath, fmt.Sprintf("%d row(s) share a path with a live book's row; both rows are kept", fa.sharedRows), nil
	}
	return retCatWouldRetire, "", nil
}

// retiredJoinCapped joins at most n ids and says how many more there were.
func retiredJoinCapped(ids []string, n int) string {
	if len(ids) <= n {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:n], ", ") + fmt.Sprintf(" and %d more", len(ids)-n)
}

func (r *retiredRun) noTargetWhy(fa *retiredFacts) string {
	switch fa.groupClass {
	case retGroupWithoutLive:
		return "its version group has no live member"
	case retGroupWithLive:
		return "its version group has no usable primary"
	}
	return "it has no version group"
}

// retiredStat is the answer of one bounded stat.
type retiredStat int

const (
	retStatPresent retiredStat = iota
	retStatAbsent
	retStatUnknown
)

// retiredNotExist reports whether err means the path is not there: not found,
// a path component that is not a directory, or a name too long to exist.
// Anything else (permission denied, I/O error) leaves the file's state
// unknown.
func retiredNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG)
}

// countAbsent counts the files that are not on disk now, and the files whose
// state could not be settled. A stat that fails for any reason other than "does
// not exist", or that outlives the bound, counts the file as present and as
// unknown: the preview never claims a file is missing on a guess, and says how
// many it did not settle. The stat is sequential within a row, the context is
// checked before each, and once this run has more hung stats than the cap the
// plan fails (errRetiredStatCap), as tag-backfill does for tag reads.
func (r *retiredRun) countAbsent(ctx context.Context, files []database.BookFileCore) (absent, unknown int, err error) {
	for i := range files {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		p := files[i].FilePath
		if p == "" {
			absent++
			continue
		}
		stat := r.stat
		got, cerr := boundedcall.Call(ctx, r.bound, func() (retiredStat, error) {
			_, serr := stat(p)
			switch {
			case serr == nil:
				return retStatPresent, nil
			case retiredNotExist(serr):
				return retStatAbsent, nil
			}
			return retStatUnknown, nil
		}, &r.abandoned, &retiredStatsAbandoned)
		if cerr != nil {
			if ctx.Err() != nil {
				return 0, 0, ctx.Err()
			}
			unknown++
			if n := r.abandoned.Load(); n >= r.maxAbandoned {
				return 0, 0, fmt.Errorf("%w: %d of this run's file stats are still hung (cap %d); failing rather than stacking more blocked threads, check the mount", errRetiredStatCap, n, r.maxAbandoned)
			}
			continue
		}
		switch got {
		case retStatAbsent:
			absent++
		case retStatUnknown:
			unknown++
		}
	}
	return absent, unknown, nil
}

// retiredCurrent builds a row's Current map without its zero and empty
// entries: a plan holds one row per trashed book (tens of thousands) in one
// operation's stored result, so a key that says nothing is not stored.
type retiredCurrent map[string]string

func (c retiredCurrent) put(k, v string) {
	if v != "" {
		c[k] = v
	}
}

func (c retiredCurrent) putN(k string, n int) {
	if n > 0 {
		c[k] = strconv.Itoa(n)
	}
}

// buildRow turns a decided book into its Repairs row.
func (r *retiredRun) buildRow(fa *retiredFacts, cat, why string) repairs.Row {
	sh := fa.shell
	cur := retiredCurrent{"group_class": fa.groupClass, "rows": strconv.Itoa(len(fa.files))}
	cur.putN("rows_missing_flag", fa.missingFlag)
	cur.putN("rows_same_path", fa.sharedRows)
	cur.putN("rows_itunes", fa.itunesRows)
	cur.putN("itunes_pid_rows", fa.itunesPIDRows)
	cur.put("shares_paths_with", strings.Join(fa.sharedWith, ","))
	cur.put("target_book_id", fa.targetID)
	cur.put("target_method", fa.targetMethod)
	cur.put("library_state", retiredDeref(sh.LibraryState))
	cur.put("group_id", fa.groupID)
	if at := sh.MarkedForDeletionAt; at != nil && !at.IsZero() {
		cur["soft_deleted_at"] = at.UTC().Format(time.RFC3339)
		cur["age_days"] = strconv.Itoa(int(r.now.Sub(*at).Hours() / 24))
	}
	if fa.absentOnDisk >= 0 {
		cur["rows_absent_on_disk"] = strconv.Itoa(fa.absentOnDisk)
	}
	if fa.statUnknown >= 0 {
		cur.putN("rows_stat_unknown", fa.statUnknown)
	}
	cur.put("owner_veto_family", fa.vetoFamily)
	if len(fa.vetoUnresolved) > 0 {
		ids := make([]string, len(fa.vetoUnresolved))
		for i, u := range fa.vetoUnresolved {
			ids[i] = u.ID
		}
		cur["owner_veto_unresolved_ids"] = strings.Join(ids, ",")
	}
	cur.put("itunes_side", fa.itunesSide)
	cur.put("owner_manual_side", fa.ownerManualSide)
	if len(fa.undoJournal) > 0 {
		cur["combine_undo_aware"] = "true"
	}

	row := repairs.Row{
		RowID:   sh.ID,
		BookIDs: []string{sh.ID},
		Title:   sh.Title,
		Class:   cat,
		Risk:    repairs.RiskLow,
		Reason:  why,
		Current: cur,
		Members: []repairs.RowMember{{BookID: sh.ID, Title: sh.Title, Role: "shell", Files: len(fa.files), MissingFiles: fa.missingFlag}},
	}
	if fa.targetID == "" && fa.mergedLive != "" {
		cur["merged_into_target"] = fa.mergedLive
		row.Evidence = append(row.Evidence, "merged-into chain ends at live book "+fa.mergedLive+"; not a target, the book has no live version-group member")
	}
	if fa.targetID == "" && fa.rule4 != "" {
		cur["same_path_holder"] = fa.rule4
		row.Evidence = append(row.Evidence, "live book "+fa.rule4+" holds a row at every path of this book; not used as a target")
	}
	if fa.statUnknown > 0 {
		row.Evidence = append(row.Evidence, fmt.Sprintf("%d file(s) could not be checked on disk (timeout or error); counted as present", fa.statUnknown))
	}
	if len(fa.undoJournal) > 0 {
		row.Flags = []string{retFlagCombineUndoAware}
		row.Evidence = append(row.Evidence, "an unreversed combine journal names this book as absorbed ("+retiredJoinCapped(fa.undoJournal, 3)+
			"); undoing that combine would restore it, so a retire is meant to be reversible by that undo (combine_undo_aware)")
	}
	if len(fa.vetoUnresolved) > 0 {
		row.Evidence = append(row.Evidence, "owner rejects held because these ids cannot be resolved to one book: "+retiredUnresolvedDetail(fa.vetoUnresolved))
	}
	if fa.itunesSide != "" {
		row.Evidence = append(row.Evidence, "iTunes check tripped on the "+fa.itunesSide+": "+fa.itunesWhy)
	}
	if fa.ownerManualSide != "" {
		row.Evidence = append(row.Evidence, "owner-manual check tripped on the "+fa.ownerManualSide+": "+fa.ownerManualWhy)
	}
	if fa.targetID != "" {
		row.BookIDs = append(row.BookIDs, fa.targetID)
		row.Members = append(row.Members, repairs.RowMember{BookID: fa.targetID, Title: fa.target.title, Role: "target",
			Files: fa.target.files, MissingFiles: fa.target.missing})
	}
	if fa.sharedRows > 0 && fa.targetID != "" && (len(fa.sharedWith) > 1 || fa.sharedWith[0] != fa.targetID) {
		row.Risk = repairs.RiskReview
		row.Evidence = append(row.Evidence, "shares a path with a live book that is not the target")
	}
	if fa.itunesSide != "" || fa.ownerManualSide != "" || (fa.targetID == "" && fa.ambiguous != "") || cat == retCatVetoUnresolvable {
		row.Risk = repairs.RiskReview
	}

	switch cat {
	case retCatWouldRetire, retCatSamePath:
		row.Proposed = map[string]string{"retired_version_of": fa.targetID}
		if cat == retCatWouldRetire {
			row.Reason = "preview: its file records would move onto " + fa.targetID + " as a hidden retired version"
		} else {
			row.Reason = "preview: " + why + "; its file records would move onto " + fa.targetID + " as a hidden retired version"
		}
	default:
		if (cat == retCatITunes || cat == retCatOwnerManual) && fa.targetID != "" {
			row.Proposed = map[string]string{"retired_version_of": fa.targetID}
		}
		row.Skipped, row.SkipReason = cat, why
	}
	row.Fingerprint = retiredFingerprint(fa, cat)
	if r.detail {
		row.Detail = &retiredDetail{minAge: r.minAge, shell: sh, files: fa.files, targetID: fa.targetID,
			targetMethod: fa.targetMethod, category: cat, sharedWith: fa.sharedWith, sharedRows: fa.sharedRows,
			itunesRows: fa.itunesRows, groupID: fa.groupID}
	}
	return row
}

// retiredFingerprint hashes every input the category was decided from, so a
// re-plan that reads different state fingerprints differently.
func retiredFingerprint(fa *retiredFacts, cat string) string {
	parts := []string{cat, fa.shell.ID, fa.groupID, fa.groupClass, fa.targetID, fa.targetMethod, fa.ambiguous,
		retiredDeref(fa.shell.LibraryState), strconv.Itoa(fa.sharedRows), strings.Join(fa.sharedWith, ","),
		fa.rule4, fa.mergedLive, fa.itunesSide, fa.itunesWhy, fa.ownerManualSide, fa.ownerManualWhy, strconv.Itoa(fa.absentOnDisk), strconv.Itoa(fa.statUnknown),
		fa.vetoFamily, retiredUnresolvedDetail(fa.vetoUnresolved), strings.Join(fa.undoJournal, ",")}
	if fa.target != nil {
		parts = append(parts, strconv.Itoa(fa.target.files), strconv.Itoa(fa.target.missing), strconv.FormatBool(fa.target.itunes), fa.target.manualWhy)
	}
	if at := fa.shell.MarkedForDeletionAt; at != nil {
		parts = append(parts, strconv.FormatInt(at.Unix(), 10))
	}
	rows := make([]string, len(fa.files))
	for i := range fa.files {
		rows[i] = fa.files[i].ID + "|" + fa.files[i].FilePath + "|" + strconv.FormatBool(fa.files[i].Missing)
	}
	sort.Strings(rows)
	parts = append(parts, rows...)
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// retiredErrorRow is the row of a book whose reads failed.
func retiredErrorRow(shell database.BookCore, err error) repairs.Row {
	r := repairs.Row{RowID: shell.ID, BookIDs: []string{shell.ID}, Title: shell.Title, Class: retCatError,
		Skipped: retCatError, SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
	r.Fingerprint = retiredFingerprint(&retiredFacts{shell: shell, absentOnDisk: -1, statUnknown: -1}, retCatError+":"+err.Error())
	return r
}

// Plan classifies every book in the trash. Read-only.
func (f *retiredVersionsFixer) Plan(ctx context.Context, params json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	run, err := f.newRun(ctx, params)
	if err != nil {
		return nil, err
	}
	run.logUnresolvedVetoIDs(rep)
	// Stragglers of earlier runs still hold OS threads. A few are only worth a
	// warning; at the cap the mount is dead, and another plan would stack more
	// blocked threads on it, so the plan is refused until they return.
	if n := retiredStatsAbandoned.Load(); n >= run.maxAbandoned {
		return nil, fmt.Errorf("%w: %d file stat(s) of earlier runs are still hung (cap %d); not starting another plan, check the mount", errRetiredStatCap, n, run.maxAbandoned)
	} else if n > 0 {
		_ = rep.Log(slog.LevelWarn, fmt.Sprintf("%d file stat(s) abandoned by earlier runs are still hung; check the mount", n))
	}
	// The trash is listed by its own reader: the memdb core listings leave
	// soft-deleted books out, and this one falls back to a Pebble scan when
	// memdb is not serving. Limit and offset 0 mean the whole trash.
	trash, err := run.store.ListSoftDeletedBooks(0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("ListSoftDeletedBooks: %w", err)
	}
	shells := make([]database.BookCore, 0, len(trash))
	for i := range trash {
		shells = append(shells, trash[i].Core())
	}
	rows := make([]repairs.Row, len(shells))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i, and reads the run's
	// caches through sync.Map. No two workers touch a book row: the plan
	// writes none.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(shells)), func(ctx context.Context, i int) error {
		defer done.Add(1)
		row, err := run.classify(ctx, shells[i])
		switch {
		case err == nil:
			rows[i] = row
		case errors.Is(err, database.ErrOwnershipLookupIncomplete):
			// The ownership lookup is not complete (memdb warming up): every
			// row would fail the same way, and a plan of 51,000 error rows
			// is no preview. Fail the plan; it can be run again.
			return err
		case errors.Is(err, errRetiredUndoRead):
			// Every row needs the journal index; one unreadable journal row
			// would make 51,000 error rows. Fail the plan.
			return err
		case errors.Is(err, errRetiredStatCap):
			// A hung mount: every further stat would stack another blocked
			// thread. Fail the plan.
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			rows[i] = retiredErrorRow(shells[i], err)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeFail,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string {
			return fmt.Sprintf("Retired versions preview %d/%d", done.Load(), total)
		},
	})
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("classify the trash: %w", runErr)
	}
	_ = rep.Log(slog.LevelInfo, retiredSummary(rows))
	return rows, nil
}

// retiredSummary is the end-of-plan log line: what a no-target row knew that
// the plan did not use as a target, and how many rows come from a combine that
// can still be undone.
func retiredSummary(rows []repairs.Row) string {
	var noTarget, merged, holder, statUnknown, undoAware, undoAwareRetire int
	for i := range rows {
		switch rows[i].Class {
		case retCatNoTargetMissing, retCatNoTargetPresent:
			noTarget++
		}
		if rows[i].Current["merged_into_target"] != "" {
			merged++
		}
		if rows[i].Current["same_path_holder"] != "" {
			holder++
		}
		if rows[i].Current["rows_stat_unknown"] != "" {
			statUnknown++
		}
		if rows[i].Current["combine_undo_aware"] == "true" {
			undoAware++
			if rows[i].Applicable() {
				undoAwareRetire++
			}
		}
	}
	return fmt.Sprintf("retired-versions preview: %d trashed book(s); %d have no target, of which %d have a merged-into chain ending at a live book and %d have a live book at every path (neither used as a target); %d row(s) had files whose state on disk could not be settled; "+
		"%d row(s) come from a combine that can still be undone (%d of them would be retired: that retire will be reversible by undoing the combine)",
		len(rows), noTarget, merged, holder, statUnknown, undoAware, undoAwareRetire)
}

// Replan classifies one book again from fresh reads, by the same code Plan
// runs. A book no longer in the trash comes back skipped "gone" with a
// different fingerprint, never an error. The combine-journal read is a full
// scan per call (no per-book index yet).
//
// Two answers are specific to an apply (flag on):
//   - a combine_undo_aware row that would be applied comes back held
//     (combine_undo_link_pending, retry_later) because its retire must be
//     reversible by undoing the combine and that link is not built; nothing is
//     written for it and the same plan can be applied again once it is;
//   - a book whose rows an interrupted apply already retired, but whose
//     external-id, merged-into or chain steps are unfinished, comes back
//     applicable again (retire_tail_pending) carrying the unfinished steps for
//     Apply. When the planned row was an ordinary retire onto that holder it
//     keeps the PLANNED fingerprint (the row it was planned as is the row being
//     finished); a fresh plan finds the same row on its own. A book with
//     nothing left to do that the plan had as applicable comes back skipped
//     under the planned fingerprint, which the engine reports as not_applicable
//     (skipped), not changed_since_plan: the work is done, not different.
func (f *retiredVersionsFixer) Replan(ctx context.Context, params json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	run, err := f.newRun(ctx, params)
	if err != nil {
		return repairs.Row{}, err
	}
	run.detail = true
	b, err := run.store.GetBookByID(planned.RowID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", planned.RowID, err)
	}
	if b == nil || !b.IsSoftDeleted() {
		r := repairs.Row{RowID: planned.RowID, BookIDs: []string{planned.RowID}, Class: retCatGone, Skipped: retCatGone,
			SkipReason: "the book is no longer in the trash", Reason: "the book is no longer in the trash", Risk: repairs.RiskLow}
		r.Fingerprint = retiredFingerprint(&retiredFacts{shell: database.BookCore{ID: planned.RowID}, absentOnDisk: -1, statUnknown: -1}, retCatGone)
		return r, nil
	}
	row, err := run.classify(ctx, b.Core())
	if err != nil || !f.applyOnFor(ctx) {
		return row, err
	}
	switch {
	case row.Class == retCatAlreadyRetired || row.Class == retCatTailPending:
		return resumeRetired(row, planned), nil
	case row.Applicable() && slices.Contains(row.Flags, retFlagCombineUndoAware):
		return holdCombineLink(row), nil
	}
	return row, nil
}

// holdCombineLink turns an applicable combine_undo_aware row into the transient
// hold: skipped, retry_later, with RetryFingerprint the fingerprint the row had
// so the engine matches it to the plan. The row's Detail is dropped; nothing of
// it may be applied.
func holdCombineLink(row repairs.Row) repairs.Row {
	why := "an unreversed combine journal names this book as absorbed, and retiring it must be undone by undoing that combine; " +
		"that link is not built yet (plan section 6.5), so the row is held, nothing written"
	held := row
	held.Detail = nil
	held.Skipped, held.SkipReason, held.Reason = retCatCombineLinkPending, why, why
	held.RetryLater = true
	held.RetryFingerprint = row.Fingerprint
	h := sha256.Sum256([]byte(row.Fingerprint + "\x00" + retCatCombineLinkPending))
	held.Fingerprint = hex.EncodeToString(h[:])
	return held
}

// resumeRetired is the Replan answer for a book that is already retired
// (done is already_retired or retire_tail_pending). When the planned row was
// applicable and its retire landed in the holder the plan named, the answer
// carries the planned fingerprint: with a tail left the row is applicable again
// (the work the plan promised is being finished) and without one it stays the
// already_retired skip, so the engine settles it as not_applicable rather than
// as a row that changed under the plan. A planned row that was itself a
// retire_tail_pending row only needs to be returned as classified; any other
// holder is a different answer and keeps its own fingerprint (changed_since_plan).
func resumeRetired(done, planned repairs.Row) repairs.Row {
	d, ok := done.Detail.(*retiredDetail)
	if !ok || !d.done || !planned.Applicable() || d.holder == "" || d.holder != planned.Current["target_book_id"] {
		return done
	}
	if done.Class == retCatTailPending {
		if planned.Class == retCatTailPending {
			return done
		}
		done.Class = planned.Class
		done.Reason = "resume: the file records are already retired onto " + d.holder + "; finishing " + d.pending.String()
		done.Proposed = planned.Proposed
	}
	done.Fingerprint = planned.Fingerprint
	return done
}
