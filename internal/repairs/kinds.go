// file: internal/repairs/kinds.go
// version: 1.0.0
// guid: 0f3a6c1e-2b7d-4d58-9a41-6c8e5b2d7f13
// last-edited: 2026-11-12

package repairs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
)

// This file is the native-shell half of the repairs framework (05-tasks
// §5T.8.2): every Fixer becomes a tasks.Fixer definition of kind
// books.repairs.<fixer-id>, and the framework's plan/apply engine (RunPlan,
// RunApply) is replaced, for that kind, by the executor's fixer runner. The
// definitions are dark: nothing registers them until F5-18 retires the v2
// repairs.plan / repairs.apply operations.
//
// What the executor owns after the cutover, and this file therefore does not
// reproduce: the plan rows (stored as tasks.Row), the approval, the per-run
// applied marks, the checkpoint of an interrupted apply, the worker pool and
// the exclusive refs of the rows. What stays in this file is what only the
// fixers know: how a row is planned, re-evaluated and written, and the
// framework guards that withhold a row.
//
// The mapping of a repairs.Row to a tasks.Row is PlanRowOf, and it is the one
// mapping: the plan stage stores it, the apply stage re-derives it from a fresh
// Replan and compares fingerprints, and F5-16's import of HEAD's stored plan
// rows calls the same function, so an imported row fingerprints exactly as the
// row a native plan would have stored.

// KindPrefix is the kind of every converted fixer up to its id: the kind of the
// fixer "duplicate-copies" is "books.repairs.duplicate-copies".
const KindPrefix = "books.repairs."

// ApplyConcurrencyKey is the ConcurrencyKey of every converted kind, plan runs
// included: one run of any repairs kind at a time, HEAD's rule that two
// repairs.apply operations never run at once (a plan, which HEAD kept on a key
// of its own, queues behind it: a Fixer definition has one key).
const ApplyConcurrencyKey tasks.Key = "books.repairs.apply"

// PlannedRows reads the rows of the plan an apply run was started against.
// The executor's Load callback is handed the run and a row id only, not the
// planned row, and a fixer's Replan needs the row as it was planned (its State,
// its fingerprint), so the shell asks for it here. The kernel owns the
// implementation (F5-18): a run's plan_ref names the plan run whose rows
// these are.
type PlannedRows interface {
	// PlanOf is the id of the plan run the apply run applies.
	PlanOf(ctx context.Context, applyRun tasks.RunID) (plan string, err error)
	// PlannedRow is the stored State of one planned row of that plan, as
	// PlanRowOf stored it (HEAD's stored row bytes, byte for byte); found is
	// false when the plan holds no such row.
	PlannedRow(ctx context.Context, applyRun tasks.RunID, rowID string) (state json.RawMessage, found bool, err error)
}

// KindDeps is what the definitions need from the app besides the fixers.
type KindDeps struct {
	// Guard, Tags and Series are the framework guards' readers (PlanDeps).
	// Without a Guard a plan or an apply fails: a row the guard cannot see is
	// not applicable.
	Guard  GuardReader
	Tags   GuardTagReader
	Series SeriesNamer
	// NewWriter builds the run's repairs.Writer, the only write path handed to
	// a fixer. It is called once per apply run, on the first row, with the run
	// the writes belong to.
	NewWriter func(ctx context.Context, run tasks.RunID, f Fixer) (*Writer, error)
	// PlanRows reads the planned rows of an apply run's plan (PlannedRows).
	PlanRows PlannedRows
	// BindStores puts the fixer stores (WithStores) on a context. The fixers
	// read their stores from the context, never from what they captured
	// (conversion rule 3), so this is what hands them the shell's.
	BindStores func(ctx context.Context) context.Context
	// Scopes resolves a HEAD permission atom to the native scopes it needs
	// (books' atom table, which repairs cannot import). An atom it does not
	// know leaves the kind without a Start requirement, which the constructor
	// reports: a kind whose gate cannot be resolved is not registrable.
	Scopes func(atom auth.Permission) ([]rbac.Permission, bool)
	// StandDownKey is the concurrency key the library scan runs under. An
	// apply holds a stand-down of it for its writes, as HEAD's RunApply
	// holds the scan stand-down. Empty: no stand-down is taken.
	StandDownKey tasks.Key
	// Concurrency is the apply and guard worker count; 0 means
	// defaultApplyConcurrency.
	Concurrency int
}

// Kinds returns one tasks.Fixer definition per fixer, kind KindPrefix+ID.
func Kinds(fixers []Fixer, deps KindDeps) []tasks.Definition {
	out := make([]tasks.Definition, 0, len(fixers))
	for _, f := range fixers {
		out = append(out, newKind(f, deps).definition())
	}
	return out
}

// Kinds returns the registry's fixers as definitions, in the lane's order.
func (r *Registry) Kinds(deps KindDeps) []tasks.Definition { return Kinds(r.List(), deps) }

// repairKind is one fixer's native definition and the per-run state its
// callbacks share.
type repairKind struct {
	f    Fixer
	deps KindDeps
	conc int
	// runs holds the state of every apply run that has loaded a row, by run
	// id: its writer, its scope, the rows evaluated and waiting for their
	// Apply. An entry lives until its run's context ends.
	runs sync.Map // tasks.RunID -> *runSlot
}

func newKind(f Fixer, deps KindDeps) *repairKind {
	conc := deps.Concurrency
	if conc <= 0 {
		conc = defaultApplyConcurrency
	}
	return &repairKind{f: f, deps: deps, conc: conc}
}

// candidate is the C of the fixer definition. In a plan it is a row the fixer
// planned and no guard has seen; in an apply it is the row as it was planned
// (read from the stored plan by Load), tied to its run. The executor passes the
// same type to Evaluate at plan and at apply and says nothing of which it is,
// so the candidate says.
type candidate struct {
	row Row
	run *runState // nil in a plan
}

// definition builds the tasks.Fixer for the kind.
func (k *repairKind) definition() tasks.Definition {
	f := k.f
	return tasks.Fixer(tasks.Kind(KindPrefix+f.ID()), tasks.FixerSpec[json.RawMessage, candidate]{
		Common:      k.common(),
		Candidates:  k.candidates,
		Load:        k.load,
		Evaluate:    k.evaluate,
		Apply:       k.apply,
		AfterApply:  k.afterApply,
		Parallelism: tasks.Fixed(k.conc, "I/O-bound: point reads and ffprobe header reads"),
		// Guards stay empty on purpose: the executor drops a planned row a
		// Guard refuses, where HEAD's plan keeps it, listed and skipped. The
		// framework guards run inside Evaluate instead, and hold the row.
		//
		// PartitionBy stays unset: HEAD partitions rows that share a book
		// transitively (union-find), which one key per row cannot say. The
		// rows' Subjects are every book of the row, and the executor makes
		// rows with overlapping subjects exclusive, which is the same
		// guarantee (no two workers write one book) without the grouping.
	})
}

func (k *repairKind) common() tasks.Common {
	f := k.f
	c := tasks.Common{
		Title: f.Title(),
		Help:  f.Description(),
		// Every fixer's rows are books and the series they name; the single
		// Write the apply makes per row (see apply) is a "books" write.
		Effects:        tasks.Writes(tasks.Res("books", "books"), tasks.Res("books", "series")),
		Resume:         tasks.ResumeContinue,
		Timeout:        6 * time.Hour,
		Priority:       tasks.PriorityNormal,
		ConcurrencyKey: ApplyConcurrencyKey,
		Approval:       &tasks.Approval{PlanRequired: true},
	}
	if req := k.requirement(); !req.IsZero() {
		// HEAD gates starting, approving and READING the plan rows with the
		// same permission set (opaccess), so View is the requirement too, not
		// the default task viewer.
		c.Perms = tasks.Permissions{Start: req, Approve: req, View: req}
	}
	return c
}

// requirement is RequiredPermissions(f) as one AllOf over the native scopes
// of every atom. A zero Requirement when an atom does not resolve.
func (k *repairKind) requirement() rbac.Requirement {
	if k.deps.Scopes == nil {
		return rbac.Requirement{}
	}
	var scopes []rbac.Permission
	for _, atom := range RequiredPermissions(k.f) {
		s, ok := k.deps.Scopes(atom)
		if !ok || len(s) == 0 {
			return rbac.Requirement{}
		}
		scopes = append(scopes, s...)
	}
	return rbac.AllOf(scopes...)
}

// bind puts the fixer stores on ctx.
func (k *repairKind) bind(ctx context.Context) context.Context {
	if k.deps.BindStores == nil {
		return ctx
	}
	return k.deps.BindStores(ctx)
}

// --- plan ---

// candidates is the plan: the fixer's own Plan over the whole library, the
// same row checks as RunPlan, and the rows as the items the executor evaluates
// (each through the framework guards, in evaluate) on its worker pool.
func (k *repairKind) candidates(rc *tasks.RC, params json.RawMessage) (tasks.Source[candidate], error) {
	f := k.f
	if k.deps.Guard == nil {
		return tasks.Source[candidate]{}, fmt.Errorf("repairs: plan %s: no guard reader", f.ID())
	}
	rows, err := f.Plan(k.bind(rc.Context()), params, newRCReporter(rc))
	if err != nil {
		return tasks.Source[candidate]{}, fmt.Errorf("repairs: plan %s: %w", f.ID(), err)
	}
	rows, err = checkPlanRows(f, rows)
	if err != nil {
		return tasks.Source[candidate]{}, err
	}
	slices.SortFunc(rows, func(a, b Row) int { return strings.Compare(a.RowID, b.RowID) })
	items := make([]candidate, len(rows))
	for i := range rows {
		items[i] = candidate{row: rows[i]}
	}
	return tasks.Source[candidate]{
		Items: items,
		Key:   func(c candidate) string { return c.row.RowID },
		Order: tasks.Snapshot,
		Total: tasks.Known(int64(len(items))),
	}, nil
}

// --- apply ---

// load is the apply's read of one row: the row as the plan stored it. A row
// the plan does not hold is gone (the executor skips it).
func (k *repairKind) load(rc *tasks.RC, params json.RawMessage, rowID string) (candidate, bool, error) {
	if k.deps.PlanRows == nil {
		return candidate{}, false, fmt.Errorf("repairs: apply %s: no plan row reader", k.f.ID())
	}
	ctx := rc.Context()
	state, found, err := k.deps.PlanRows.PlannedRow(ctx, rc.RunID(), rowID)
	if err != nil {
		return candidate{}, false, fmt.Errorf("repairs: apply %s: read planned row %q: %w", k.f.ID(), rowID, err)
	}
	if !found {
		return candidate{}, false, nil
	}
	planned, err := decodePlannedRow(rowID, state)
	if err != nil {
		return candidate{}, false, err
	}
	rs, err := k.runState(rc)
	if err != nil {
		return candidate{}, false, err
	}
	return candidate{row: planned, run: rs}, true, nil
}

// decodePlannedRow reads a stored row. It is HEAD's decodeRow: the stored
// State of a native plan row and the bytes HEAD stored are the same JSON.
func decodePlannedRow(id string, state json.RawMessage) (Row, error) {
	return decodeRow(id, state)
}

// evaluate is the one callback the executor runs at plan and at apply.
//
// Plan: the fixer's row goes through the framework guards, as RunPlan puts it,
// and is stored as PlanRowOf(row).
//
// Apply: the row is re-evaluated from scratch, as RunApply's applyOne does:
// guards on the planned row, the fixer's Replan, then every check applyOne
// makes between Replan and Apply. The executor compares the fingerprint of what
// is returned with the planned row's and applies only on equal; every refusal
// here therefore returns a row whose fingerprint cannot equal the planned one
// (refusedRow), so the executor skips it as changed since the plan and never
// reaches Apply. A row that passes is kept for apply, which needs its Detail.
func (k *repairKind) evaluate(ic *tasks.ItemRC, params json.RawMessage, c candidate) (tasks.Row, error) {
	if c.run == nil {
		return k.evaluatePlan(ic, c.row)
	}
	return k.evaluateApply(ic, params, c)
}

func (k *repairKind) evaluatePlan(ic *tasks.ItemRC, row Row) (tasks.Row, error) {
	if k.deps.Guard == nil {
		return tasks.Row{}, fmt.Errorf("repairs: plan %s: no guard reader", k.f.ID())
	}
	// One resolver per row here, not per run: the executor's items share no
	// state a plan could hang one on. The per-folder memo is an optimisation,
	// not a guard.
	guardPlanRow(k.f, k.deps.Guard, k.deps.Tags, k.deps.Series, NewPathResolver(), &row)
	if err := settlePlanRow(&row); err != nil {
		return tasks.Row{}, fmt.Errorf("repairs: plan %s: row %q: %w", k.f.ID(), row.RowID, err)
	}
	return PlanRowOf(row)
}

func (k *repairKind) evaluateApply(ic *tasks.ItemRC, params json.RawMessage, c candidate) (tasks.Row, error) {
	f, rs, planned := k.f, c.run, c.row
	ctx := rs.bind(ic.Context())
	refuse := func(outcome, why string) (tasks.Row, error) {
		ic.Log().Info("repairs row refused at apply", "fixer", f.ID(), "row", planned.RowID, "outcome", outcome, "why", why)
		return refusedRow(planned, outcome, why)
	}
	if k.deps.Guard == nil {
		return tasks.Row{}, fmt.Errorf("repairs: apply %s: no guard reader", f.ID())
	}
	if IsPlanOnly(f) {
		return refuse("not_applicable", ErrPlanOnly.Error())
	}
	// The framework guards, on fresh reads, before the fixer sees the row.
	if kind, why, err := GuardRowBooksFor(f, k.deps.Guard, k.deps.Tags, k.deps.Series, rs.paths, planned, nil, false); err != nil {
		return refuse(OutcomeGuarded, SkipGuardUnreadable+": "+err.Error())
	} else if kind != "" {
		return refuse(OutcomeGuarded, kind+": "+why)
	}
	fresh, err := f.Replan(ctx, params, planned, newItemReporter(ic))
	if err != nil {
		return tasks.Row{}, fmt.Errorf("replan: %w", err)
	}
	if fresh.RowID != planned.RowID {
		return tasks.Row{}, fmt.Errorf("replan returned row %q for %q", fresh.RowID, planned.RowID)
	}
	// A transient hold (RetryLater) changes the fingerprint without the row
	// changing: when the row's fingerprint with the hold cleared is the
	// planned one, the row is retry_later, kept out of the marks and tried
	// again by the next run of the plan. An empty RetryFingerprint never
	// matches: fail closed, the row is changed since the plan.
	if fresh.Skipped != "" && fresh.RetryLater && fresh.RetryFingerprint != "" &&
		fresh.RetryFingerprint == planned.Fingerprint {
		held := fresh
		held.Fingerprint = planned.Fingerprint
		rs.keep(held, fmt.Errorf("%w: %s", ErrRetryLater, changedWhy(fresh)))
		return k.applyRow(held, rs)
	}
	if fresh.Fingerprint != planned.Fingerprint {
		// The executor compares and skips. The reason is the fixer's, logged
		// so the owner can read why (changedWhy).
		if why := changedWhy(fresh); why != "" {
			ic.Log().Info("repairs row changed since the plan", "fixer", f.ID(), "row", planned.RowID, "why", why)
		}
		return PlanRowOf(fresh)
	}
	if !fresh.Applicable() {
		return refuse(OutcomeNotApplicable, fresh.Skipped)
	}
	// The books the apply may write under books/itunes/** are the ones the
	// plan showed; Inputs carry them, so a difference is a changed fingerprint
	// and was returned above. Checked here as well so it cannot depend on the
	// mapping.
	if !sameIDSet(planned.OwnerITunesDatabaseOnly, fresh.OwnerITunesDatabaseOnly) {
		return refuse(OutcomeChangedSincePlan, "the iTunes-library books the row writes differ from the plan's")
	}
	// A book the re-plan added to the row must pass the guard too.
	if extra := newIDs(planned.BookIDs, fresh.BookIDs); len(extra) > 0 {
		if kind, why, err := GuardRowBooksFor(f, k.deps.Guard, k.deps.Tags, k.deps.Series, rs.paths, fresh, extra, false); err != nil {
			return refuse(OutcomeGuarded, SkipGuardUnreadable+": "+err.Error())
		} else if kind != "" {
			return refuse(OutcomeGuarded, kind+": "+why)
		}
	}
	rs.keep(fresh, nil)
	return k.applyRow(fresh, rs)
}

// applyRow is the tasks.Row an apply's Evaluate returns for a row it kept: the
// row's mapping, marked with its run so Apply finds the kept row.
func (k *repairKind) applyRow(r Row, rs *runState) (tasks.Row, error) {
	row, err := PlanRowOf(r)
	if err != nil {
		return tasks.Row{}, err
	}
	return withRun(row, rs.run)
}

// apply writes one row the executor found unchanged, through the run's
// repairs.Writer, as one gated write of the run's tasks.Writer: Preview
// records the intent and writes nothing (the fixer is not called), Live fences
// the write when the run is stopped.
func (k *repairKind) apply(ic *tasks.ItemRC, w tasks.Writer, params json.RawMessage, row tasks.Row) error {
	f := k.f
	rs := k.runStateOf(row)
	if rs == nil {
		return fmt.Errorf("repairs: apply %s: row %q was not evaluated by this run; refusing to write it", f.ID(), row.ID)
	}
	held, ok := rs.take(row.ID)
	if !ok {
		return fmt.Errorf("repairs: apply %s: row %q has no fresh evaluation; refusing to write it", f.ID(), row.ID)
	}
	if held.refuse != nil {
		return held.refuse
	}
	if IsPlanOnly(f) {
		return ErrPlanOnly
	}
	ctx := rs.bind(ic.Context())
	subject := tasks.ResID("books", "books", row.ID)
	if len(held.row.BookIDs) > 0 {
		subject = tasks.ResID("books", "books", held.row.BookIDs[0])
	}
	err := w.Do(ctx, tasks.Write{
		Name:     "books.repairs." + f.ID(),
		Resource: tasks.Res("books", "books"),
		Subject:  subject,
	}, func(ctx context.Context) ([]tasks.Change, error) {
		// The repairs.Writer journals what it writes itself (history rows,
		// the operation journal), so the changes are not also reported here.
		return nil, f.Apply(ctx, rs.writer, held.row)
	})
	return mapApplyError(err)
}

// mapApplyError makes a fixer's Apply error say what the executor's writer
// says: a fenced write is tasks.ErrFenced and a row that changed under the
// fixer's own lock is tasks.ErrChangedSincePlan. The original error stays in
// the chain (errors.Is on the repairs sentinels still holds), so the fixer's
// account of how far the row got is not lost.
func mapApplyError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, tasks.ErrPreview), errors.Is(err, tasks.ErrFenced), errors.Is(err, tasks.ErrChangedSincePlan):
		return err
	case errors.Is(err, opswriter.ErrFenced):
		return fmt.Errorf("%w: %w", tasks.ErrFenced, err)
	case errors.Is(err, ErrChangedSincePlan):
		return fmt.Errorf("%w: %w", tasks.ErrChangedSincePlan, err)
	default:
		return err
	}
}

// afterApply hands an AfterApplier fixer the books of the rows the run applied,
// once, after every row settled (HEAD's runAfterApply). A failure is logged and
// fails nothing: the rows are written, and the follow-up is only owed.
func (k *repairKind) afterApply(rc *tasks.RC, applied []string) error {
	f := k.f
	rs := k.runStateOfRun(rc.RunID())
	defer func() {
		if rs != nil {
			rs.release()
		}
	}()
	aa, ok := f.(AfterApplier)
	if !ok || len(applied) == 0 || rc.Mode() != tasks.ModeLive {
		return nil
	}
	if k.deps.PlanRows == nil {
		return fmt.Errorf("repairs: apply %s: no plan row reader for the follow-up", f.ID())
	}
	// Not rc.Context(): a cancelled apply still leaves the books its finished
	// rows wrote, and their follow-up is owed all the same.
	ctx := context.WithoutCancel(rc.Context())
	var (
		mu   sync.Mutex
		seen = map[string]bool{}
		ids  []string
	)
	// Reading one stored row per applied row is a store read per item over
	// what can be thousands of rows: a bounded pool, not a loop.
	_, err := tasks.ForEach(ctx, tasks.Fixed(k.conc, "stored plan row point reads"), applied,
		func(ctx context.Context, id string) error {
			state, found, err := k.deps.PlanRows.PlannedRow(ctx, rc.RunID(), id)
			if err != nil {
				return err
			}
			if !found {
				return nil
			}
			row, err := decodePlannedRow(id, state)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			for _, b := range row.BookIDs {
				if !seen[b] {
					seen[b] = true
					ids = append(ids, b)
				}
			}
			return nil
		}, tasks.ItemsOptions[string]{OnError: tasks.Collect})
	if err != nil {
		rc.Log().Warn("repairs follow-up not started: the applied rows' books could not be read",
			"fixer", f.ID(), "rows", len(applied), "err", err)
		return nil
	}
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	bound := k.bind(ctx)
	if rs != nil {
		bound = rs.bind(bound)
	}
	followUp, err := aa.AfterApply(bound, ids)
	if err != nil {
		rc.Log().Warn("repairs follow-up for applied books not started", "fixer", f.ID(), "books", len(ids), "err", err)
		return nil
	}
	if followUp != "" {
		rc.Log().Info("repairs follow-up started", "fixer", f.ID(), "books", len(ids), "follow_up", followUp)
	}
	return nil
}

// --- per-run state ---

// heldRow is a row an apply's Evaluate passed (or held back as retry_later)
// and its Apply has not taken yet.
type heldRow struct {
	row    Row
	refuse error // non-nil: Apply answers it and writes nothing
}

// runState is one apply run's state in one kind.
type runState struct {
	k      *repairKind
	run    tasks.RunID
	writer *Writer
	paths  *PathResolver
	scope  context.Context // the fixer's ApplyScoped context, or nil
	endFn  func()          // ends the fixer's scope
	slot   *runSlot        // where k.runs keeps this state
	hold   tasks.Hold      // the library-scan stand-down, or nil
	once   sync.Once       // release

	mu   sync.Mutex
	rows map[string]heldRow
}

// runState returns the state of rc's run, building it on the first row: the
// writer, the fixer's ApplyScoped scope (once per run, not per row, as HEAD's
// RunApply calls BeginApply) and, for a Live run of a fixer that writes
// library rows, the scan stand-down. It is released when the run's context
// ends (or, sooner, in afterApply).
func (k *repairKind) runState(rc *tasks.RC) (*runState, error) {
	run := rc.RunID()
	v, _ := k.runs.LoadOrStore(run, &runSlot{})
	slot := v.(*runSlot)
	// Every worker that loads a row of the run comes here at once: the first
	// builds the state (BeginApply, the writer, the stand-down) and the others
	// wait for it, so each of those happens once per run.
	slot.once.Do(func() {
		defer slot.done.Store(true)
		slot.rs, slot.err = k.newRunState(rc)
		if slot.err != nil {
			// A failed build is not kept: a resumed attempt of the run tries again.
			k.runs.CompareAndDelete(run, slot)
			return
		}
		slot.rs.slot = slot
		context.AfterFunc(rc.Context(), slot.rs.release)
	})
	return slot.rs, slot.err
}

// runSlot is one run's place in repairKind.runs: filled once.
type runSlot struct {
	once sync.Once
	done atomic.Bool // rs and err are set
	rs   *runState
	err  error
}

func (k *repairKind) newRunState(rc *tasks.RC) (*runState, error) {
	f := k.f
	rs := &runState{k: k, run: rc.RunID(), paths: NewPathResolver(), rows: map[string]heldRow{}}
	ctx := k.bind(rc.Context())
	live := rc.Mode() == tasks.ModeLive
	if live {
		if k.deps.NewWriter == nil {
			return nil, fmt.Errorf("repairs: apply %s: no writer", f.ID())
		}
		w, err := k.deps.NewWriter(ctx, rc.RunID(), f)
		if err != nil {
			return nil, fmt.Errorf("repairs: apply %s: writer: %w", f.ID(), err)
		}
		if w == nil {
			return nil, fmt.Errorf("repairs: apply %s: no writer", f.ID())
		}
		// A BookTagsOnly fixer skipped the framework guard on the promise that
		// it writes book tags only; a NoScanStandDown fixer writes no library
		// row at all. The writer enforces both.
		if AllowsBookTagsOnly(f) {
			w.restrictToTags()
		}
		runCtx := rc.Context()
		w.setFence(func() error {
			if err := runCtx.Err(); err != nil {
				return fmt.Errorf("%w: %w: the run was stopped (%v)", tasks.ErrFenced, opswriter.ErrFenced, context.Cause(runCtx))
			}
			return nil
		})
		prov := ApplyProvenance{ActorUserID: rc.Actor().UserID}
		if plan, perr := k.deps.PlanRows.PlanOf(ctx, rc.RunID()); perr == nil {
			prov.PlanOpID = plan
		}
		w.setProvenance(prov)
		if SkipsScanStandDown(f) {
			w.restrictToNothing()
		} else if k.deps.StandDownKey != "" {
			hold, err := rc.StandDown(k.deps.StandDownKey)
			if err != nil {
				return nil, fmt.Errorf("repairs: apply %s: scan stand-down: %w", f.ID(), err)
			}
			rs.hold = hold
		}
		rs.writer = w
	}
	if sc, ok := f.(ApplyScoped); ok {
		scoped, end := sc.BeginApply(ctx, !live)
		rs.scope, rs.endFn = scoped, end
	}
	return rs, nil
}

// runStateOf finds the state of the run a row came out of evaluate for: the
// run's id rides in the State of the row evaluate returned (withRun), which is
// the row the executor hands to Apply and is stored nowhere.
func (k *repairKind) runStateOf(row tasks.Row) *runState {
	var s applyState
	if json.Unmarshal(row.State, &s) != nil || s.Run == "" {
		return nil
	}
	return k.runStateOfRun(s.Run)
}

// applyState is the State of a row evaluate returns at apply.
type applyState struct {
	Run tasks.RunID `json:"run"`
}

// withRun marks a row evaluate returns at apply with its run.
func withRun(row tasks.Row, run tasks.RunID) (tasks.Row, error) {
	b, err := json.Marshal(applyState{Run: run})
	if err != nil {
		return tasks.Row{}, err
	}
	row.State = b
	return row, nil
}

func (k *repairKind) runStateOfRun(run tasks.RunID) *runState {
	if v, ok := k.runs.Load(run); ok {
		if slot := v.(*runSlot); slot.done.Load() && slot.err == nil {
			return slot.rs
		}
	}
	return nil
}

// keep records a row evaluate passed for apply to take.
func (rs *runState) keep(row Row, refuse error) {
	rs.mu.Lock()
	rs.rows[row.RowID] = heldRow{row: row, refuse: refuse}
	rs.mu.Unlock()
}

// take removes and returns the row kept for id: a row is applied at most once
// per evaluation.
func (rs *runState) take(id string) (heldRow, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	h, ok := rs.rows[id]
	delete(rs.rows, id)
	return h, ok
}

// bind returns ctx with the fixer's stores and, when the fixer has one, its
// ApplyScoped scope: the scope's values, behind ctx's own.
func (rs *runState) bind(ctx context.Context) context.Context {
	ctx = rs.k.bind(ctx)
	if rs.scope == nil {
		return ctx
	}
	return scopedContext{Context: ctx, scope: rs.scope}
}

// release ends everything the run state holds. Safe to call more than once.
func (rs *runState) release() {
	rs.once.Do(func() {
		rs.k.runs.CompareAndDelete(rs.run, rs.slot)
		if rs.endFn != nil {
			rs.endFn()
		}
		if rs.hold != nil {
			rs.hold.Release()
		}
		rs.mu.Lock()
		rs.rows = map[string]heldRow{}
		rs.mu.Unlock()
	})
}

// scopedContext is an item's context with the values an ApplyScoped fixer's
// BeginApply put on the run's context. The item's own values win (its
// cancellation, deadline and lock holds are the item's), the scope's fill in
// what the item's context does not carry.
type scopedContext struct {
	context.Context
	scope context.Context
}

func (c scopedContext) Value(key any) any {
	if v := c.Context.Value(key); v != nil {
		return v
	}
	return c.scope.Value(key)
}

// --- the tasks.Row mapping ---

// Keys of the Inputs a row's fingerprint is made of.
const (
	inputFingerprint = "fingerprint"
	inputOwnerDBOnly = "owner_itunes_database_only"
	inputRefused     = "refused"
)

// PlanRowOf is the tasks.Row of a repairs.Row. Its State is the row's own JSON
// (what HEAD stores under a plan, byte for byte: the apply's Replan gets back
// the planned row from it), its Inputs are what the apply re-checks (the
// fixer's fingerprint, and the set of books an owner apply may write under
// books/itunes/**), and its Hold is the row's skip kind. F5-16's import of
// HEAD's stored rows must use this function.
func PlanRowOf(r Row) (tasks.Row, error) {
	// The wire format promises book_ids is an array, never null (see
	// PlanResult.normalize): the stored State says [] for a row built with none.
	if r.BookIDs == nil {
		r.BookIDs = []string{}
	}
	state, err := json.Marshal(r)
	if err != nil {
		return tasks.Row{}, fmt.Errorf("repairs: row %q does not encode: %w", r.RowID, err)
	}
	subjects := make([]entity.Ref, 0, len(r.BookIDs))
	for _, id := range r.BookIDs {
		subjects = append(subjects, tasks.ResID("books", "books", id))
	}
	owner := slices.Clone(r.OwnerITunesDatabaseOnly)
	slices.Sort(owner)
	return tasks.Row{
		ID:       r.RowID,
		Subjects: subjects,
		Title:    r.Title,
		Current:  r.Current,
		Proposed: r.Proposed,
		Reason:   r.Reason,
		Risk:     taskRisk(r.Risk),
		Class:    r.Class,
		Evidence: r.Evidence,
		Inputs: map[string]string{
			inputFingerprint: r.Fingerprint,
			inputOwnerDBOnly: strings.Join(owner, ","),
		},
		Hold:      r.Skipped,
		Retry:     r.RetryLater,
		OwnerOnly: r.OwnerApplicable,
		State:     state,
	}, nil
}

// taskRisk maps a fixer's risk word to the executor's grade. A word it does not
// know is graded medium, never low.
func taskRisk(risk string) tasks.Risk {
	if risk == RiskLow {
		return tasks.RiskLow
	}
	return tasks.RiskMedium
}

// refusedRow is the row an apply's Evaluate returns for a row it will not let
// the executor apply: planned's own mapping with an input no planned row has,
// so its fingerprint differs from the planned one for certain and the executor
// skips the row as changed since the plan (it never reaches apply).
func refusedRow(planned Row, outcome, why string) (tasks.Row, error) {
	row, err := PlanRowOf(planned)
	if err != nil {
		return tasks.Row{}, err
	}
	row.Hold = outcome
	row.Inputs[inputRefused] = outcome + ": " + why
	return row, nil
}
