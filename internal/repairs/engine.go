// file: internal/repairs/engine.go
// version: 1.1.0
// guid: 9b3e7f40-2d15-4a86-9c1f-6e0a4d8b7c25
// last-edited: 2026-09-28

package repairs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// Op ids of the two framework operations.
const (
	PlanOpID  = "repairs.plan"
	ApplyOpID = "repairs.apply"
)

// SkipGuardUnreadable marks a row whose books could not be read for the
// guard. Fail closed: a row the guard cannot see is not applicable.
const SkipGuardUnreadable = "skipped_guard_unreadable"

// Apply outcomes of one row.
const (
	OutcomeApplied          = "applied"
	OutcomeWouldApply       = "would_apply"
	OutcomeChangedSincePlan = "changed_since_plan"
	// OutcomePartial: the fixer wrote part of the row before finding a
	// change (for the vg fixer: the winner is crowned, some demotions are
	// not). The row is NOT unchanged; re-plan it.
	OutcomePartial       = "partially_applied"
	OutcomeGuarded       = "skipped_guard"
	OutcomeNotApplicable = "not_applicable"
	OutcomeFailed        = "failed"
	OutcomeAborted       = "aborted_standdown_lost"
)

// ErrStandDownLost aborts an apply whose scan stand-down lease lapsed: the
// scanner has resumed, so further writes would race it.
var ErrStandDownLost = errors.New("repairs: scan stand-down lease lost; remaining rows not written")

// PlanParams are the params of repairs.plan.
type PlanParams struct {
	FixerID string          `json:"fixer_id"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// PlanResult is the stored result of repairs.plan: every row, uncapped.
type PlanResult struct {
	FixerID    string          `json:"fixer_id"`
	Params     json.RawMessage `json:"params,omitempty"`
	PlannedAt  time.Time       `json:"planned_at"`
	Total      int             `json:"total"`
	Applicable int             `json:"applicable"`
	// SkippedByKind counts the rows apply will not write, by Row.Skipped.
	SkippedByKind map[string]int `json:"skipped_by_kind"`
	// ByClass counts every row (applicable and skipped) by Row.Class, for a
	// fixer that sets one. Each count is a filter of the rows endpoint.
	ByClass map[string]int `json:"by_class,omitempty"`
	Rows    []Row          `json:"rows"`
}

// PlanDeps is what RunPlan needs besides the fixer.
type PlanDeps struct {
	Guard  GuardReader
	Series SeriesNamer
	// Concurrency of the guard pass; 0 means runtime.NumCPU().
	Concurrency int
}

// RunPlan runs f.Plan, then the framework guards over every applicable row
// (reading each row's books and files; a read failure marks the row
// SkipGuardUnreadable), sorts the rows by id and tallies them. Read-only:
// it takes no stand-down and runs during a library.scan.
func RunPlan(ctx context.Context, f Fixer, params json.RawMessage, deps PlanDeps, reporter registry.Reporter) (*PlanResult, error) {
	if deps.Guard == nil {
		return nil, fmt.Errorf("repairs: plan %s: no guard reader", f.ID())
	}
	rows, err := f.Plan(ctx, params, reporter)
	if err != nil {
		return nil, fmt.Errorf("repairs: plan %s: %w", f.ID(), err)
	}
	seen := make(map[string]bool, len(rows))
	for i := range rows {
		if rows[i].RowID == "" || seen[rows[i].RowID] {
			return nil, fmt.Errorf("repairs: plan %s: empty or duplicate row id %q", f.ID(), rows[i].RowID)
		}
		seen[rows[i].RowID] = true
	}
	idx := make([]int, 0, len(rows))
	for i := range rows {
		if rows[i].Applicable() {
			idx = append(idx, i)
		}
	}
	conc := deps.Concurrency
	if conc <= 0 {
		conc = runtime.NumCPU()
	}
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i: no two workers share a
	// row, so the slice needs no lock.
	gerr := registry.RunItems(ctx, reporter, idx, func(_ context.Context, i int) error {
		defer done.Add(1)
		kind, why, err := GuardBooks(deps.Guard, deps.Series, rows[i].BookIDs)
		switch {
		case err != nil:
			rows[i].Skipped, rows[i].SkipReason = SkipGuardUnreadable, err.Error()
		case kind != "":
			rows[i].Skipped, rows[i].SkipReason = kind, why
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: conc,
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Guarding rows %d/%d", done.Load(), total) },
	})
	if gerr != nil {
		return nil, fmt.Errorf("repairs: plan %s: guard pass: %w", f.ID(), gerr)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RowID < rows[j].RowID })
	res := &PlanResult{FixerID: f.ID(), Params: params, PlannedAt: time.Now().UTC(), Total: len(rows),
		SkippedByKind: map[string]int{}, Rows: rows}
	for i := range rows {
		if rows[i].Applicable() {
			res.Applicable++
		} else {
			res.SkippedByKind[rows[i].Skipped]++
		}
		if c := rows[i].Class; c != "" {
			if res.ByClass == nil {
				res.ByClass = map[string]int{}
			}
			res.ByClass[c]++
		}
	}
	return res, nil
}

// OpReader reads a stored operation row.
type OpReader interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
}

// Plan-loading errors, so the HTTP layer can map them to status codes.
var (
	ErrPlanNotFound    = errors.New("repairs: plan operation not found")
	ErrNotAPlan        = errors.New("repairs: operation is not a repairs.plan run")
	ErrPlanOtherFixer  = errors.New("repairs: plan belongs to another fixer")
	ErrPlanNotComplete = errors.New("repairs: plan operation has not completed")
)

// LoadPlan reads a completed repairs.plan op's stored result and checks that
// it belongs to fixerID.
func LoadPlan(r OpReader, planOpID, fixerID string) (*PlanResult, error) {
	row, err := r.GetOperationV2(planOpID)
	if err != nil {
		return nil, fmt.Errorf("repairs: read plan op %s: %w", planOpID, err)
	}
	if row == nil {
		return nil, fmt.Errorf("%w: %s", ErrPlanNotFound, planOpID)
	}
	if row.DefID != PlanOpID {
		return nil, fmt.Errorf("%w: %s is %s", ErrNotAPlan, planOpID, row.DefID)
	}
	if row.Status != "completed" || row.ResultData == nil {
		return nil, fmt.Errorf("%w: %s is %s", ErrPlanNotComplete, planOpID, row.Status)
	}
	var plan PlanResult
	if err := json.Unmarshal([]byte(*row.ResultData), &plan); err != nil {
		return nil, fmt.Errorf("repairs: decode plan %s: %w", planOpID, err)
	}
	if plan.FixerID != fixerID {
		return nil, fmt.Errorf("%w: %s was planned for %s", ErrPlanOtherFixer, planOpID, plan.FixerID)
	}
	return &plan, nil
}

// Page filters.
const (
	FilterAll        = ""
	FilterApplicable = "applicable"
	FilterSkipped    = "skipped"
)

// RowsPage is one page of a stored plan.
type RowsPage struct {
	PlanOpID   string    `json:"plan_op_id"`
	FixerID    string    `json:"fixer_id"`
	PlannedAt  time.Time `json:"planned_at"`
	Filter     string    `json:"filter,omitempty"`
	Class      string    `json:"class,omitempty"`
	Offset     int       `json:"offset"`
	Limit      int       `json:"limit"`
	Total      int       `json:"total"` // rows matching the filter
	Applicable int       `json:"applicable"`
	// SkippedByKind is the whole plan's tally, whatever the filter.
	SkippedByKind map[string]int `json:"skipped_by_kind"`
	// ByClass is the whole plan's per-class tally, whatever the filter.
	ByClass map[string]int `json:"by_class,omitempty"`
	// ByClassInFilter tallies by class only the rows matching filter (class
	// aside): exactly the rows a class chip on this tab lists.
	ByClassInFilter map[string]int `json:"by_class_in_filter,omitempty"`
	Rows            []Row          `json:"rows"`
}

// Page returns rows [offset, offset+limit) of the plan's rows matching
// filter and, when class is not empty, of that Row.Class. An unknown filter
// is an error. Rows is never nil.
func (p *PlanResult) Page(planOpID, filter, class string, offset, limit int) (*RowsPage, error) {
	switch filter {
	case FilterAll, FilterApplicable, FilterSkipped:
	default:
		return nil, fmt.Errorf("repairs: unknown filter %q (want applicable or skipped)", filter)
	}
	var match []Row
	var inFilter map[string]int
	for i := range p.Rows {
		if filter != FilterAll && p.Rows[i].Applicable() != (filter == FilterApplicable) {
			continue
		}
		if c := p.Rows[i].Class; c != "" {
			if inFilter == nil {
				inFilter = map[string]int{}
			}
			inFilter[c]++
		}
		if class != "" && p.Rows[i].Class != class {
			continue
		}
		match = append(match, p.Rows[i])
	}
	if offset < 0 {
		offset = 0
	}
	out := &RowsPage{PlanOpID: planOpID, FixerID: p.FixerID, PlannedAt: p.PlannedAt, Filter: filter,
		Class: class, Offset: offset, Limit: limit, Total: len(match), Applicable: p.Applicable,
		SkippedByKind: p.SkippedByKind, ByClass: p.ByClass, ByClassInFilter: inFilter, Rows: []Row{}}
	if offset < len(match) {
		end := offset + limit
		if end > len(match) {
			end = len(match)
		}
		out.Rows = append(out.Rows, match[offset:end]...)
	}
	return out, nil
}

// ApplyParams are the params of repairs.apply. Only an explicit
// dry_run:false writes (owner rule 2026-09-25: an omitted mode is a preview).
type ApplyParams struct {
	FixerID  string   `json:"fixer_id"`
	PlanOpID string   `json:"plan_op_id"`
	RowIDs   []string `json:"row_ids"`
	DryRun   *bool    `json:"dry_run,omitempty"`
	DryRunC  *bool    `json:"dryRun,omitempty"`
	// Resume is the checkpoint of an interrupted run (set by the op through
	// the registry's checkpoint merge, never by a caller): the rows it had
	// already settled. A resumed run keeps those results and does not
	// re-apply them.
	Resume *ApplyCheckpoint `json:"resume,omitempty"`
}

// ApplyCheckpoint is what repairs.apply checkpoints while it runs: every row
// settled so far, with its outcome. Merged back into the op's params as
// "resume" when the server restarts mid-apply (ResumeRestart).
type ApplyCheckpoint struct {
	Settled []RowResult `json:"settled"`
}

// settledForResume reports whether a checkpointed outcome is final. An
// aborted row was never attempted and is applied again on resume.
func settledForResume(outcome string) bool {
	return outcome != "" && outcome != OutcomeAborted && outcome != OutcomeWouldApply
}

// RowResult is the apply outcome of one requested row.
type RowResult struct {
	RowID   string `json:"row_id"`
	Outcome string `json:"outcome"`
	// Skipped is the guard kind (skipped_guard) or the plan row's skip kind
	// (not_applicable).
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ApplyResult is the stored result of repairs.apply.
type ApplyResult struct {
	FixerID          string         `json:"fixer_id"`
	PlanOpID         string         `json:"plan_op_id"`
	DryRun           bool           `json:"dry_run"`
	Requested        int            `json:"requested"`
	NotInPlan        []string       `json:"not_in_plan,omitempty"`
	ByOutcome        map[string]int `json:"by_outcome"`
	Applied          int            `json:"applied"`
	ChangedSincePlan int            `json:"changed_since_plan"`
	Partial          int            `json:"partially_applied"`
	Failed           int            `json:"failed"`
	BookWrites       int            `json:"book_writes"`
	HistoryRows      int            `json:"history_rows"`
	HistoryFailed    int            `json:"history_rows_failed"`
	// StandDownHeld: the library-scan stand-down was held for the writes.
	StandDownHeld bool        `json:"standdown_held"`
	Aborted       string      `json:"aborted,omitempty"`
	Rows          []RowResult `json:"rows"`
}

// ApplyDeps is what RunApply needs besides the fixer and the plan.
type ApplyDeps struct {
	Guard  GuardReader
	Series SeriesNamer
	// StandDown may be nil only where there is no registry (tests, degraded
	// contexts); the writes then run with no interlock, as every other
	// apply op does in that case.
	StandDown StandDown
	// Writer is the only write path handed to the fixer.
	Writer *Writer
	// OpID keys the stand-down; required for a write.
	OpID string
	Wait WaitOptions
	// Concurrency of the per-partition workers; 0 means 4 (the fixers so far
	// are I/O-bound: point reads and ffprobe header reads).
	Concurrency int
	// Resumed is the checkpoint of an interrupted run of this same apply;
	// its settled rows are reported as they were and not re-applied.
	Resumed *ApplyCheckpoint
	// Checkpoint, when set, is called with every row settled so far after
	// each write-run row settles (throttled; see checkpointEvery). A dry run
	// never checkpoints.
	Checkpoint func(ApplyCheckpoint) error
}

// checkpointEvery / checkpointInterval throttle ApplyDeps.Checkpoint: a row
// whose settle was not yet checkpointed when the server stops is re-planned
// on resume, and a fixer's Replan recognises its own finished steps, so a
// lost checkpoint costs a re-check, not a double write. Variables so tests
// can checkpoint every row.
var (
	checkpointEvery    = 5
	checkpointInterval = 15 * time.Second
)

const defaultApplyConcurrency = 4

// RunApply applies rowIDs of plan through f.
//
// For each selected row, in order: renew the stand-down lease (hard abort
// when lost), re-run the framework guards on fresh reads, Replan the row and
// refuse it as changed_since_plan when its fingerprint differs from the
// stored plan's, check the lease is still valid, then hand the fresh row to
// f.Apply. A dry run (dryRun) stops before the lease and the write and
// reports would_apply; it takes no stand-down.
//
// CONCURRENCY: rows are partitioned so that rows sharing any book id land in
// the same partition (union-find over Row.BookIDs); partitions run on a
// bounded RunItems pool and each partition's rows run in order, so no two
// workers ever write the same book.
//
// On a lost lease the result is complete up to the abort (remaining rows are
// aborted_standdown_lost) and ErrStandDownLost is returned with it; the
// caller persists the result before returning the error.
func RunApply(ctx context.Context, f Fixer, plan *PlanResult, planOpID string, rowIDs []string,
	dryRun bool, deps ApplyDeps, reporter registry.Reporter) (*ApplyResult, error) {
	res := &ApplyResult{FixerID: f.ID(), PlanOpID: planOpID, DryRun: dryRun, ByOutcome: map[string]int{}}
	if deps.Guard == nil {
		return res, fmt.Errorf("repairs: apply %s: no guard reader", f.ID())
	}
	if !dryRun && deps.Writer == nil {
		return res, fmt.Errorf("repairs: apply %s: no writer", f.ID())
	}
	byID := make(map[string]*Row, len(plan.Rows))
	for i := range plan.Rows {
		byID[plan.Rows[i].RowID] = &plan.Rows[i]
	}
	ids := normalizeIDs(rowIDs)
	res.Requested = len(ids)
	if len(ids) == 0 {
		return res, fmt.Errorf("repairs: apply %s: no row_ids; pick them from the plan", f.ID())
	}
	var (
		mu      sync.Mutex
		results []RowResult
	)
	record := func(r RowResult) {
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
	}
	resumed := map[string]bool{}
	if deps.Resumed != nil && !dryRun {
		for _, r := range deps.Resumed.Settled {
			if settledForResume(r.Outcome) && !resumed[r.RowID] {
				resumed[r.RowID] = true
				record(r)
			}
		}
	}
	var selected []Row
	for _, id := range ids {
		row, ok := byID[id]
		switch {
		case resumed[id]:
			// Settled before the restart; its result is already recorded.
		case !ok:
			res.NotInPlan = append(res.NotInPlan, id)
		case !row.Applicable():
			record(RowResult{RowID: id, Outcome: OutcomeNotApplicable, Skipped: row.Skipped})
		default:
			selected = append(selected, *row)
		}
	}

	holder, held := "", false
	if !dryRun && len(selected) > 0 {
		if deps.StandDown != nil && deps.OpID == "" {
			return res, ErrNoHolderID
		}
		release, h, err := AcquireStandDownWaiting(ctx, deps.StandDown, deps.OpID, "repairs.apply "+f.ID(), reporter, deps.Wait)
		if err != nil {
			return res, err
		}
		defer release()
		holder, held = deps.OpID, h
		res.StandDownHeld = held
	}

	var lost atomic.Bool
	var done atomic.Int64
	// Checkpoint state: the rows settled since the last checkpoint and when
	// it was written. Guarded by mu, like results.
	sinceCP, lastCP := 0, time.Now()
	settle := func(r RowResult) {
		mu.Lock()
		results = append(results, r)
		if dryRun || deps.Checkpoint == nil || r.Outcome == OutcomeAborted {
			mu.Unlock()
			return
		}
		sinceCP++
		if sinceCP < checkpointEvery && time.Since(lastCP) < checkpointInterval {
			mu.Unlock()
			return
		}
		cp := ApplyCheckpoint{Settled: append([]RowResult(nil), results...)}
		sinceCP, lastCP = 0, time.Now()
		// Written under mu so checkpoints land in order: a slower earlier one
		// can never overwrite a later one with fewer rows.
		if err := deps.Checkpoint(cp); err != nil && reporter != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("repairs.apply: checkpoint not written (a restart re-checks these rows): %v", err))
		}
		mu.Unlock()
	}
	parts := partitionRows(selected)
	conc := deps.Concurrency
	if conc <= 0 {
		conc = defaultApplyConcurrency
	}
	runErr := registry.RunItems(ctx, reporter, parts, func(pctx context.Context, part []Row) error {
		for _, planned := range part {
			settle(applyOne(pctx, f, plan.Params, planned, dryRun, deps, holder, held, &lost, reporter))
			done.Add(1)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: conc,
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, _ int) string {
			return fmt.Sprintf("Rows %d/%d (%d settled before a restart)", done.Load(), len(selected), len(resumed))
		},
	})

	sort.Slice(results, func(i, j int) bool { return results[i].RowID < results[j].RowID })
	res.Rows = results
	for _, r := range results {
		res.ByOutcome[r.Outcome]++
		switch r.Outcome {
		case OutcomeApplied:
			res.Applied++
		case OutcomeChangedSincePlan:
			res.ChangedSincePlan++
		case OutcomePartial:
			res.Partial++
		case OutcomeFailed:
			res.Failed++
		}
	}
	if deps.Writer != nil {
		res.BookWrites = deps.Writer.Writes()
		res.HistoryRows = deps.Writer.HistoryRows()
		res.HistoryFailed = deps.Writer.HistoryFailed()
	}
	if lost.Load() {
		res.Aborted = ErrStandDownLost.Error()
		return res, ErrStandDownLost
	}
	if runErr != nil {
		return res, runErr
	}
	return res, nil
}

func applyOne(ctx context.Context, f Fixer, params json.RawMessage, planned Row, dryRun bool,
	deps ApplyDeps, holder string, held bool, lost *atomic.Bool, reporter registry.Reporter) RowResult {
	out := RowResult{RowID: planned.RowID}
	abort := func() RowResult {
		out.Outcome = OutcomeAborted
		return out
	}
	if lost.Load() {
		return abort()
	}
	// Heartbeat: RunItems stamps op progress but does not renew the lease.
	if !dryRun && StandDownLost(deps.StandDown, holder, held) {
		lost.Store(true)
		return abort()
	}
	if kind, why, err := GuardBooks(deps.Guard, deps.Series, planned.BookIDs); err != nil {
		out.Outcome, out.Skipped, out.Error = OutcomeGuarded, SkipGuardUnreadable, err.Error()
		return out
	} else if kind != "" {
		out.Outcome, out.Skipped, out.Error = OutcomeGuarded, kind, why
		return out
	}
	fresh, err := f.Replan(ctx, params, planned, reporter)
	if err != nil {
		out.Outcome, out.Error = OutcomeFailed, "replan: "+err.Error()
		return out
	}
	if fresh.RowID != planned.RowID {
		out.Outcome, out.Error = OutcomeFailed, fmt.Sprintf("replan returned row %q for %q", fresh.RowID, planned.RowID)
		return out
	}
	if fresh.Fingerprint != planned.Fingerprint {
		out.Outcome = OutcomeChangedSincePlan
		return out
	}
	if !fresh.Applicable() {
		out.Outcome, out.Skipped = OutcomeNotApplicable, fresh.Skipped
		return out
	}
	// A book the re-plan added to the row must pass the guard too.
	if extra := newIDs(planned.BookIDs, fresh.BookIDs); len(extra) > 0 {
		if kind, why, err := GuardBooks(deps.Guard, deps.Series, extra); err != nil {
			out.Outcome, out.Skipped, out.Error = OutcomeGuarded, SkipGuardUnreadable, err.Error()
			return out
		} else if kind != "" {
			out.Outcome, out.Skipped, out.Error = OutcomeGuarded, kind, why
			return out
		}
	}
	if dryRun {
		out.Outcome = OutcomeWouldApply
		return out
	}
	// Last check before the write: the lease must still be live.
	if held && !deps.StandDown.ScanStandDownValid(holder) {
		lost.Store(true)
		return abort()
	}
	if err := f.Apply(ctx, deps.Writer, fresh); err != nil {
		out.Error = err.Error()
		switch {
		case errors.Is(err, ErrPartiallyApplied):
			out.Outcome = OutcomePartial
		case errors.Is(err, ErrChangedSincePlan):
			out.Outcome = OutcomeChangedSincePlan
		default:
			out.Outcome = OutcomeFailed
		}
		return out
	}
	out.Outcome = OutcomeApplied
	return out
}

// partitionRows groups rows that share any book id (transitively) and keeps
// each group in plan order.
func partitionRows(rows []Row) [][]Row {
	parent := make([]int, len(rows))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	owner := map[string]int{}
	for i := range rows {
		for _, b := range rows[i].BookIDs {
			if j, ok := owner[b]; ok {
				if ri, rj := find(i), find(j); ri != rj {
					parent[ri] = rj
				}
			} else {
				owner[b] = i
			}
		}
	}
	groups := map[int][]Row{}
	var order []int
	for i := range rows {
		r := find(i)
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], rows[i])
	}
	out := make([][]Row, 0, len(order))
	for _, r := range order {
		out = append(out, groups[r])
	}
	return out
}

func normalizeIDs(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func newIDs(old, fresh []string) []string {
	have := make(map[string]bool, len(old))
	for _, id := range old {
		have[id] = true
	}
	var out []string
	for _, id := range fresh {
		if !have[id] {
			out = append(out, id)
		}
	}
	return out
}
