// file: core/kernel/module_tasks.go
// version: 1.0.0
// guid: c41e7a02-93d5-4b86-a1f0-6e2d8b7c5934
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

// moduleTasks is one module's Deps.Tasks. Deps are built at P6, and the executor
// they act through is built at P7 and only when the app runs one, so the value
// handed to Register is this forwarder: until bind, and for ever in an app that
// builds no executor, every call returns module.ErrTasksNotBound. The bound
// client is read atomically, so a module's goroutine that calls it while P7
// binds sees either state and never a torn one.
type moduleTasks struct {
	bound atomic.Pointer[boundModuleTasks]
}

// boundModuleTasks wraps the client so the atomic pointer holds a concrete type.
type boundModuleTasks struct{ c tasks.Client }

var _ tasks.Client = (*moduleTasks)(nil)

func (l *moduleTasks) bind(c tasks.Client) { l.bound.Store(&boundModuleTasks{c: c}) }

func (l *moduleTasks) client() (tasks.Client, error) {
	b := l.bound.Load()
	if b == nil {
		return nil, module.ErrTasksNotBound
	}
	return b.c, nil
}

func (l *moduleTasks) Start(ctx context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	c, err := l.client()
	if err != nil {
		return tasks.RunRef{}, err
	}
	return c.Start(ctx, kind, req)
}

func (l *moduleTasks) Get(ctx context.Context, id tasks.RunID) (tasks.RunView, error) {
	c, err := l.client()
	if err != nil {
		return tasks.RunView{}, err
	}
	return c.Get(ctx, id)
}

func (l *moduleTasks) Runs(ctx context.Context, q tasks.RunQuery) iter.Seq2[tasks.RunView, error] {
	c, err := l.client()
	if err != nil {
		return func(yield func(tasks.RunView, error) bool) { yield(tasks.RunView{}, err) }
	}
	return c.Runs(ctx, q)
}

func (l *moduleTasks) Cancel(ctx context.Context, id tasks.RunID) error {
	c, err := l.client()
	if err != nil {
		return err
	}
	return c.Cancel(ctx, id)
}

func (l *moduleTasks) Decide(ctx context.Context, id tasks.RunID, d tasks.Decision) error {
	c, err := l.client()
	if err != nil {
		return err
	}
	return c.Decide(ctx, id, d)
}

func (l *moduleTasks) Pause(ctx context.Context, reason string) error {
	c, err := l.client()
	if err != nil {
		return err
	}
	return c.Pause(ctx, reason)
}

func (l *moduleTasks) Unpause(ctx context.Context) error {
	c, err := l.client()
	if err != nil {
		return err
	}
	return c.Unpause(ctx)
}

func (l *moduleTasks) PauseState(ctx context.Context) (tasks.Pause, bool, error) {
	c, err := l.client()
	if err != nil {
		return tasks.Pause{}, false, err
	}
	return c.PauseState(ctx)
}

func (l *moduleTasks) Timeline(ctx context.Context, q tasks.TimelineQuery) ([]tasks.RunView, error) {
	c, err := l.client()
	if err != nil {
		return nil, err
	}
	return c.Timeline(ctx, q)
}

func (l *moduleTasks) Log(ctx context.Context, id tasks.RunID, q tasks.PageQuery) iter.Seq2[tasks.LogLine, error] {
	c, err := l.client()
	if err != nil {
		return func(yield func(tasks.LogLine, error) bool) { yield(tasks.LogLine{}, err) }
	}
	return c.Log(ctx, id, q)
}

func (l *moduleTasks) Discard(ctx context.Context, id tasks.RunID) error {
	c, err := l.client()
	if err != nil {
		return err
	}
	return c.Discard(ctx, id)
}

// moduleHealth is one module's Deps.Health: Reporter answers
// module.ErrHealthNotBound until P7 registers the module's components, then the
// reporter of a component the module declared, and health.ErrNotOwned for any
// other ID, so a module can never report another component's status.
type moduleHealth struct {
	bound atomic.Pointer[map[health.ComponentID]health.Reporter]
}

var _ health.Reporters = (*moduleHealth)(nil)

func (l *moduleHealth) bind(reporters map[health.ComponentID]health.Reporter) {
	l.bound.Store(&reporters)
}

func (l *moduleHealth) Reporter(id health.ComponentID) (health.Reporter, error) {
	m := l.bound.Load()
	if m == nil {
		return nil, module.ErrHealthNotBound
	}
	if r, ok := (*m)[id]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("%w: %s", health.ErrNotOwned, id)
}

// moduleTaskRows is one module's Deps.TaskRows: the run store narrowed to the
// module's own registered kinds. Like moduleTasks it is bound at P7 and read
// atomically; until then every call returns module.ErrTaskRowsNotBound.
type moduleTaskRows struct {
	bound atomic.Pointer[boundTaskRows]
}

// boundTaskRows is the P7 state: the store, the module's own kinds, and a small
// cache of the plans it read.
type boundTaskRows struct {
	runs  tasks.RunStore
	kinds map[tasks.Kind]bool
	own   []tasks.Kind // sorted, the kinds an empty query means

	mu    sync.Mutex
	plans map[tasks.RunID]map[string]json.RawMessage // terminal plan runs' rows by Row.ID
}

// maxCachedPlans bounds the plan cache: an apply reads one plan, and a point
// read per applied row would otherwise scan the plan's rows each time.
const maxCachedPlans = 4

var _ module.TaskRows = (*moduleTaskRows)(nil)

// bind hands the handle the run store and the kinds the module registered.
func (l *moduleTaskRows) bind(runs tasks.RunStore, kinds []tasks.Kind) {
	b := &boundTaskRows{runs: runs, kinds: make(map[tasks.Kind]bool, len(kinds)), plans: map[tasks.RunID]map[string]json.RawMessage{}}
	for _, k := range kinds {
		b.kinds[k] = true
	}
	b.own = slices.Sorted(func(yield func(tasks.Kind) bool) {
		for k := range b.kinds {
			if !yield(k) {
				return
			}
		}
	})
	l.bound.Store(b)
}

func (l *moduleTaskRows) get() (*boundTaskRows, error) {
	b := l.bound.Load()
	if b == nil {
		return nil, module.ErrTaskRowsNotBound
	}
	return b, nil
}

// record reads run id and refuses it unless its kind is the module's.
func (b *boundTaskRows) record(ctx context.Context, id tasks.RunID) (tasks.RunRecord, error) {
	rec, err := b.runs.Get(ctx, id)
	if err != nil {
		return tasks.RunRecord{}, err
	}
	if !b.kinds[rec.Kind] {
		return tasks.RunRecord{}, fmt.Errorf("%w: run %s", module.ErrTaskRowsForeign, id)
	}
	return rec, nil
}

func (l *moduleTaskRows) List(ctx context.Context, q tasks.RunQuery) iter.Seq2[tasks.RunRecord, error] {
	fail := func(err error) iter.Seq2[tasks.RunRecord, error] {
		return func(yield func(tasks.RunRecord, error) bool) { yield(tasks.RunRecord{}, err) }
	}
	b, err := l.get()
	if err != nil {
		return fail(err)
	}
	for _, k := range q.Kinds {
		if !b.kinds[k] {
			return fail(fmt.Errorf("%w: kind %s", module.ErrTaskRowsForeign, k))
		}
	}
	if len(q.Kinds) == 0 {
		q.Kinds = b.own
	}
	return b.runs.List(ctx, q)
}

func (l *moduleTaskRows) Rows(ctx context.Context, id tasks.RunID) iter.Seq2[tasks.Row, error] {
	fail := func(err error) iter.Seq2[tasks.Row, error] {
		return func(yield func(tasks.Row, error) bool) { yield(tasks.Row{}, err) }
	}
	b, err := l.get()
	if err != nil {
		return fail(err)
	}
	if _, err := b.record(ctx, id); err != nil {
		return fail(err)
	}
	return b.runs.Rows(ctx, id)
}

func (l *moduleTaskRows) ReplaceRows(ctx context.Context, id tasks.RunID, prefix string, n int, at func(i int) (tasks.Row, error)) error {
	b, err := l.get()
	if err != nil {
		return err
	}
	if _, err := b.record(ctx, id); err != nil {
		return err
	}
	return b.runs.ReplaceRows(ctx, id, prefix, n, at)
}

func (l *moduleTaskRows) PlanOf(ctx context.Context, applyRun tasks.RunID) (string, error) {
	b, err := l.get()
	if err != nil {
		return "", err
	}
	rec, err := b.record(ctx, applyRun)
	if err != nil {
		return "", err
	}
	return string(rec.PlanRef), nil
}

func (l *moduleTaskRows) PlannedRow(ctx context.Context, applyRun tasks.RunID, rowID string) (json.RawMessage, bool, error) {
	b, err := l.get()
	if err != nil {
		return nil, false, err
	}
	rec, err := b.record(ctx, applyRun)
	if err != nil {
		return nil, false, err
	}
	if rec.PlanRef == "" {
		return nil, false, fmt.Errorf("%w: apply run %s names no plan", executor.ErrPlanRequired, applyRun)
	}
	rows, err := b.plan(ctx, rec.PlanRef)
	if err != nil {
		return nil, false, err
	}
	state, found := rows[rowID]
	return state, found, nil
}

// plan is the stored State of every row of plan run id, by Row.ID. The plan
// run must be the module's; its rows are cached only once it is terminal, when
// they can no longer change (ReplaceRows is refused on a terminal run).
func (b *boundTaskRows) plan(ctx context.Context, id tasks.RunID) (map[string]json.RawMessage, error) {
	b.mu.Lock()
	rows, ok := b.plans[id]
	b.mu.Unlock()
	if ok {
		return rows, nil
	}
	rec, err := b.record(ctx, id)
	if err != nil {
		return nil, err
	}
	rows = map[string]json.RawMessage{}
	for row, err := range b.runs.Rows(ctx, id) {
		if err != nil {
			return nil, err
		}
		if rid, ok := strings.CutPrefix(row.ID, executor.PlanRowStoredID("")); ok {
			rows[rid] = row.State
		}
	}
	if rec.State.Terminal() {
		b.mu.Lock()
		if len(b.plans) >= maxCachedPlans {
			for k := range b.plans {
				delete(b.plans, k)
				break
			}
		}
		b.plans[id] = rows
		b.mu.Unlock()
	}
	return rows, nil
}
