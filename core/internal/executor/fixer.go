// file: core/internal/executor/fixer.go
// version: 1.0.0
// guid: 9689d192-751d-423a-b144-61fe27e2a8d1
// last-edited: 2026-11-10

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

// planPrefix is the row prefix a Fixer plan writes (RunStore.ReplaceRows).
const planPrefix = "plan"

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
		func(i int) (tasks.Row, error) { return rows[i], nil }); err != nil {
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
		for row, err := range a.e.runs.Rows(ctx, ref) {
			if err != nil {
				return nil, err
			}
			plan[row.ID] = row
		}
		var approved []string
		ok, err := a.Restore(ckptApproved, &approved)
		if err != nil {
			return nil, err
		}
		if ok {
			ids = approved
		} else {
			for id := range plan {
				ids = append(ids, id)
			}
		}
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
	ids = slices.DeleteFunc(ids, func(id string) bool { return marked[id] || plan[id].ID == "" })
	slices.Sort(ids)
	var mu sync.Mutex
	var applied []string
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
	}, tasks.ItemsOptions[string]{
		OnError:       tasks.Collect,
		ExclusiveRefs: func(id string) []entity.Ref { return plan[id].Subjects },
	})
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	slices.Sort(applied)
	if fs.AfterApply != nil {
		if err := fs.AfterApply(rc, applied); err != nil {
			return nil, err
		}
	}
	return json.Marshal(fixerResult{Applied: len(applied), Skipped: int(res.Skipped), Failed: int(res.Failed)})
}
