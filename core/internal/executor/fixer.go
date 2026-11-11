// file: core/internal/executor/fixer.go
// version: 1.3.0
// guid: 9689d192-751d-423a-b144-61fe27e2a8d1
// last-edited: 2026-11-10

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

// planPrefix prefixes a Fixer plan's stored row IDs: a plan row is stored
// as "plan:" + Row.ID, since RunStore.ReplaceRows replaces the rows whose
// IDs begin with a prefix (taskstest's storesuite shape). planRows strips
// it, so Approve, the marks and the apply run name the definition's Row.ID.
const planPrefix = "plan:"

// planRows reads a plan run's rows keyed by Row.ID (rows outside the plan
// prefix are not the plan's).
func (e *executor) planRows(ctx context.Context, id tasks.RunID) (map[string]tasks.Row, error) {
	out := map[string]tasks.Row{}
	for row, err := range e.runs.Rows(ctx, id) {
		if err != nil {
			return nil, err
		}
		rid, ok := strings.CutPrefix(row.ID, planPrefix)
		if !ok {
			continue
		}
		row.ID = rid
		out[rid] = row
	}
	return out, nil
}

// fixerResult is a Fixer run's stored result.
type fixerResult struct {
	Rows    int `json:"rows,omitempty"`
	Applied int `json:"applied,omitempty"`
	Skipped int `json:"skipped,omitempty"`
	Failed  int `json:"failed,omitempty"`
}

// runFixer is the Fixer runner (5T.3.1, 5T.3.7): a Preview run is the plan
// (Candidates → Evaluate → rows); a Live run applies the rows of its plan
// (PlanRef: only the approved rows, each re-evaluated and refused when it
// changed since the plan) or, without a plan, the rows it evaluates. Each
// applied row takes its Row.Subjects as ExclusiveRefs and is marked as it
// returns, so a resumed attempt skips it.
func (a *attempt) runFixer(rc *tasks.RC, p any) (json.RawMessage, error) {
	if a.rec.Mode == tasks.ModePreview {
		return a.fixerPlan(rc, p)
	}
	return a.fixerApply(rc, p)
}

// fixerCandidates drains the candidate source (a plan holds every row).
func (a *attempt) fixerCandidates(rc *tasks.RC, p any) ([]any, error) {
	src, err := a.d.fixer.Candidates(rc, p)
	if err != nil {
		return nil, err
	}
	if _, _, dirty := src.Dirty(); dirty {
		return nil, ErrDirtySource
	}
	if src.Pages == nil {
		return src.Items, nil
	}
	var out []any
	after := ""
	for {
		items, next, err := src.Pages(rc.Context(), after, tasks.DefaultChunkSize)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) == 0 || next == "" {
			return out, nil
		}
		after = next
	}
}

// evaluate runs Evaluate over the candidates; rows with an ID and no guard
// objection are kept, sorted by ID.
func (a *attempt) evaluate(rc *tasks.RC, p any) ([]tasks.Row, tasks.ItemsResult, error) {
	fs := a.d.fixer
	cands, err := a.fixerCandidates(rc, p)
	if err != nil {
		return nil, tasks.ItemsResult{}, err
	}
	var mu sync.Mutex
	var rows []tasks.Row
	res, _ := tasks.RunItems(rc, a.itemsPar(fs.Parallelism), cands, func(ic *tasks.ItemRC, c any) error {
		row, err := fs.Evaluate(ic, p, c)
		if err != nil {
			return err
		}
		if row.ID == "" {
			return nil
		}
		if reason := guard(fs.Guards, row); reason != "" {
			ic.Skip(reason)
			return nil
		}
		mu.Lock()
		rows = append(rows, row)
		mu.Unlock()
		return nil
	}, tasks.ItemsOptions[any]{OnError: tasks.Collect})
	slices.SortFunc(rows, func(x, y tasks.Row) int { return strings.Compare(x.ID, y.ID) })
	return rows, res, nil
}

// approval reads the approved row IDs Approve staged with this run. It is
// read straight from the store, past the ResumeFromZero filter: a from-zero
// resume voids progress, never the approval. No record fails closed.
func (a *attempt) approval(ctx context.Context) ([]string, error) {
	raw, found, err := a.e.runs.Checkpoint(ctx, a.id, ckptApproved)
	if err != nil {
		return nil, err
	}
	if !found || len(raw) == 0 {
		return nil, fmt.Errorf("%w: run %s (plan %s)", ErrNoApproval, a.id, a.rec.PlanRef)
	}
	var rows []string
	if err := json.Unmarshal(unwrapData(raw), &rows); err != nil {
		return nil, fmt.Errorf("executor: approval record of %s: %w", a.id, err)
	}
	return rows, nil
}

func guard(gs []tasks.Guard, row tasks.Row) string {
	for _, g := range gs {
		if reason := g(row); reason != "" {
			return reason
		}
	}
	return ""
}

func (a *attempt) fixerPlan(rc *tasks.RC, p any) (json.RawMessage, error) {
	rows, res, err := a.evaluate(rc, p)
	if err != nil {
		return nil, err
	}
	if rc.Context().Err() != nil {
		return nil, context.Cause(rc.Context())
	}
	if a.fenced.Load() {
		return nil, tasks.ErrFenced
	}
	if err := a.e.runs.ReplaceRows(context.WithoutCancel(rc.Context()), a.id, planPrefix, len(rows),
		func(i int) (tasks.Row, error) {
			r := rows[i]
			r.ID = planPrefix + r.ID
			return r, nil
		}); err != nil {
		return nil, err
	}
	return json.Marshal(fixerResult{Rows: len(rows), Skipped: int(res.Skipped), Failed: int(res.Failed)})
}

func (a *attempt) fixerApply(rc *tasks.RC, p any) (json.RawMessage, error) {
	fs := a.d.fixer
	ctx := rc.Context()
	plan := map[string]tasks.Row{}
	var ids []string
	if ref := a.rec.PlanRef; ref != "" {
		var err error
		if plan, err = a.e.planRows(ctx, ref); err != nil {
			return nil, err
		}
		rejected, err := a.e.planRejected(ctx, ref)
		if err != nil {
			return nil, err
		}
		if rejected {
			return nil, fmt.Errorf("%w: %s", ErrPlanRejected, ref)
		}
		approved, err := a.approval(ctx)
		if err != nil {
			return nil, err
		}
		ids = approved
	} else {
		rows, _, err := a.evaluate(rc, p)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			plan[r.ID] = r
			ids = append(ids, r.ID)
		}
	}
	marked := map[string]bool{}
	for m, err := range a.e.runs.Marks(ctx, a.id) {
		if err != nil {
			return nil, err
		}
		marked[m] = true
	}
	// A row an earlier attempt of this run applied carries its mark: it is
	// not applied again, but while its changes still stand it is one of the
	// run's applied rows, for AfterApply and the result (HEAD's
	// appliedByEarlierAttempt, pkg/ops/fixer.go: Applied counts across
	// attempts from the apply markers).
	var prior []string
	ids = slices.DeleteFunc(ids, func(id string) bool {
		if plan[id].ID == "" {
			return true
		}
		if marked[id] {
			prior = append(prior, id)
			return true
		}
		return false
	})
	slices.Sort(ids)
	prior, undone, err := a.standing(ctx, prior)
	if err != nil {
		return nil, err
	}
	// Row.Subjects always act as ExclusiveRefs at apply; PartitionBy, when
	// set, also runs the rows of one key in order on one worker (5T.3.1).
	opts := tasks.ItemsOptions[string]{
		OnError:       tasks.Collect,
		ExclusiveRefs: func(id string) []entity.Ref { return plan[id].Subjects },
	}
	if fs.PartitionBy != nil {
		opts.PartitionBy = func(id string) string { return fs.PartitionBy(plan[id]) }
	}
	var mu sync.Mutex
	applied := prior
	res, _ := tasks.RunItems(rc, a.itemsPar(fs.Parallelism), ids, func(ic *tasks.ItemRC, id string) error {
		planned := plan[id]
		c, ok, err := fs.Load(rc, p, id)
		if err != nil {
			return err
		}
		if !ok {
			ic.Skip("gone")
			return nil
		}
		row, err := fs.Evaluate(ic, p, c)
		if err != nil {
			return err
		}
		if row.Fingerprint() != planned.Fingerprint() {
			ic.Skip("changed_since_plan")
			a.log.Info("row changed since the plan; refused", "row", id, "err", tasks.ErrChangedSincePlan)
			return nil
		}
		if reason := guard(fs.Guards, row); reason != "" {
			ic.Skip(reason)
			return nil
		}
		w := ic.Writer()
		if err := fs.Apply(ic, w, p, row); err != nil && !errors.Is(err, tasks.ErrPreview) {
			return err
		}
		w.MarkRowApplied(ic.Context(), id)
		mu.Lock()
		applied = append(applied, id)
		mu.Unlock()
		return nil
	}, opts)
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	slices.Sort(applied)
	if fs.AfterApply != nil {
		if err := fs.AfterApply(rc, applied); err != nil {
			return nil, err
		}
	}
	return json.Marshal(fixerResult{Applied: len(applied), Skipped: int(res.Skipped) + undone, Failed: int(res.Failed)})
}

// standing keeps the marked rows whose changes still stand. With the
// module's ChangeSink, Stands decides (a row whose history was undone is
// not applied, HEAD's evidence rule); without one the mark is the evidence.
// A row that no longer stands is skipped as changed since the plan and is
// not written again (counted in undone).
func (a *attempt) standing(ctx context.Context, marked []string) (keep []string, undone int, err error) {
	sink := a.e.opts.ChangeSinks[a.d.module]
	if sink == nil {
		return marked, 0, nil
	}
	for _, id := range marked {
		ok, err := sink.Stands(ctx, a.id, id)
		if err != nil {
			return nil, 0, fmt.Errorf("executor: read the apply evidence of row %s: %w", id, err)
		}
		if !ok {
			a.log.Info("row applied by an earlier attempt was undone since; skipped", "row", id, "err", tasks.ErrChangedSincePlan)
			undone++
			continue
		}
		keep = append(keep, id)
	}
	return keep, undone, nil
}
