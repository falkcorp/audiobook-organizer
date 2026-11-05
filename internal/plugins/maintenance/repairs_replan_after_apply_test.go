// file: internal/plugins/maintenance/repairs_replan_after_apply_test.go
// version: 1.0.0
// guid: 5e2a7c91-3b48-4d06-9f1e-84c0a6d2b735
// last-edited: 2026-11-05

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// A trial is a stored, never-edited plan, so an apply that wrote rows leaves
// them listed (sticky) until a new plan runs. runRepairsApply starts that plan
// itself; these tests drive the plan op and the apply op as the registry would
// and read the rows back through the same Page the rows endpoint serves.

const refreshFixerID = "refresh-fixture"

// refreshFixer plans one row per book still open and closes a row when applied:
// a plan run after an apply no longer lists what the apply wrote.
type refreshFixer struct {
	mu     sync.Mutex
	open   map[string]bool
	params []json.RawMessage // params of every Plan call
}

func (*refreshFixer) ID() string          { return refreshFixerID }
func (*refreshFixer) Title() string       { return "Refresh fixture" }
func (*refreshFixer) Description() string { return "test" }

func (f *refreshFixer) Plan(_ context.Context, params json.RawMessage, _ registry.Reporter) ([]repairs.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.params = append(f.params, params)
	var rows []repairs.Row
	for id := range f.open {
		if f.open[id] {
			rows = append(rows, repairs.Row{RowID: id, BookIDs: []string{id}, Fingerprint: "open"})
		}
	}
	return rows, nil
}

func (f *refreshFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open[planned.RowID] {
		planned.Fingerprint = "closed"
	}
	return planned, nil
}

func (f *refreshFixer) Apply(_ context.Context, _ *repairs.Writer, fresh repairs.Row) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.open[fresh.RowID] = false
	return nil
}

func (f *refreshFixer) planCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.params)
}

// refreshOps is the queue store: op rows and plan rows (planOps), the active
// list the refresh looks a queued plan up in, and the settings the lane's
// last-plan pointers live in.
type refreshOps struct {
	*planOps
	amu      sync.Mutex
	active   []database.OperationV2Row
	settings map[string]string
}

func (q *refreshOps) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	q.amu.Lock()
	defer q.amu.Unlock()
	return append([]database.OperationV2Row(nil), q.active...), nil
}

func (q *refreshOps) SetSetting(key, value, _ string, _ bool) error {
	q.amu.Lock()
	defer q.amu.Unlock()
	q.settings[key] = value
	return nil
}

func (q *refreshOps) setting(key string) string {
	q.amu.Lock()
	defer q.amu.Unlock()
	return q.settings[key]
}

// enqueued is one EnqueueOp call.
type enqueued struct {
	defID  string
	params any
}

type refreshDeps struct {
	fakeDeps
	ops *refreshOps

	mu       sync.Mutex
	enqueues []enqueued
	err      error
}

func (d *refreshDeps) OperationQueueStore() OpQueueReader { return d.ops }

func (d *refreshDeps) EnqueueOp(_ context.Context, defID string, params any) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return "", d.err
	}
	d.enqueues = append(d.enqueues, enqueued{defID, params})
	return fmt.Sprintf("op-plan-refresh-%d", len(d.enqueues)), nil
}

func (d *refreshDeps) calls() []enqueued {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]enqueued(nil), d.enqueues...)
}

type refreshFixture struct {
	t      *testing.T
	p      *Plugin
	deps   *refreshDeps
	fixer  *refreshFixer
	books  map[string]string // title -> book id
	planID string
}

func newRefreshFixture(t *testing.T) *refreshFixture {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	fx := &refreshFixture{t: t, fixer: &refreshFixer{open: map[string]bool{}}, books: map[string]string{}}
	for _, title := range []string{"Year One", "The Long Halloween", "Hush"} {
		b, err := st.CreateBook(&database.Book{Title: title, FilePath: "/lib/Bruce Wayne/" + title + "/" + title + ".m4b"})
		require.NoError(t, err)
		fx.books[title] = b.ID
		fx.fixer.open[b.ID] = true
	}
	ops := &refreshOps{
		planOps:  &planOps{MemOpRows: dbtest.NewMemOpRows(), rows: map[string]*database.OperationV2Row{}},
		settings: map[string]string{},
	}
	fx.deps = &refreshDeps{fakeDeps: fakeDeps{store: st}, ops: ops}
	fx.p = &Plugin{deps: fx.deps, standDownWait: noWait}
	require.NoError(t, fx.p.Repairs().Register(fx.fixer))
	return fx
}

// runPlan runs the plan op under opID with params and stores its op row, as
// the registry does when it completes.
func (fx *refreshFixture) runPlan(opID string, params repairs.PlanParams) *repairs.PlanResult {
	fx.t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(fx.t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(fx.t, fx.p.runRepairsPlan(context.Background(), raw, rep))
	res := rep.result.(*repairs.PlanResult)
	data, err := json.Marshal(res)
	require.NoError(fx.t, err)
	s := string(data)
	fx.deps.ops.planOps.mu.Lock()
	fx.deps.ops.rows[opID] = &database.OperationV2Row{ID: opID, DefID: repairs.PlanOpID, Status: "completed", ResultData: &s}
	fx.deps.ops.planOps.mu.Unlock()
	return res
}

// runApply runs the apply op and returns its stored result.
func (fx *refreshFixture) runApply(opID, planOpID string, rowIDs []string, dryRun bool) (*repairs.ApplyResult, error) {
	fx.t.Helper()
	raw, err := json.Marshal(repairs.ApplyParams{FixerID: refreshFixerID, PlanOpID: planOpID, RowIDs: rowIDs, DryRun: &dryRun})
	require.NoError(fx.t, err)
	rep := &repairsOpReporter{id: opID}
	runErr := fx.p.runRepairsApply(context.Background(), raw, rep)
	res, _ := rep.result.(*repairs.ApplyResult)
	return res, runErr
}

// listing is what the rows endpoint serves for a plan: the applicable rows'
// book titles, via the same LoadPlan + Page.
func (fx *refreshFixture) listing(planOpID string) []string {
	fx.t.Helper()
	ops := fx.deps.ops
	rows, err := repairs.PlanRowStoreOf(ops)
	require.NoError(fx.t, err)
	plan, err := repairs.LoadPlan(ops, rows, planOpID, refreshFixerID)
	require.NoError(fx.t, err)
	page, err := plan.Page(planOpID, repairs.FilterApplicable, "", 0, 100)
	require.NoError(fx.t, err)
	byID := map[string]string{}
	for title, id := range fx.books {
		byID[id] = title
	}
	var titles []string
	for _, r := range page.Rows {
		titles = append(titles, byID[r.RowID])
	}
	sort.Strings(titles)
	return titles
}

func TestRepairsApply_RefreshesTheTrialSoAppliedRowsLeaveTheListing(t *testing.T) {
	fx := newRefreshFixture(t)
	planParams := json.RawMessage(`{"scope":"gotham"}`)
	fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID, Params: planParams})
	require.Equal(t, []string{"Hush", "The Long Halloween", "Year One"}, fx.listing("op-plan-1"))

	res, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Year One"]}, false)
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied)

	// The apply started one plan: same fixer, the trial's own params, tagged
	// with the apply that asked for it.
	calls := fx.deps.calls()
	require.Len(t, calls, 1)
	require.Equal(t, repairs.PlanOpID, calls[0].defID)
	pp, ok := calls[0].params.(repairs.PlanParams)
	require.True(t, ok)
	require.Equal(t, refreshFixerID, pp.FixerID)
	require.JSONEq(t, string(planParams), string(pp.Params))
	require.Equal(t, "op-apply-1", pp.AfterApplyOpID)

	// The lane learns of it from the apply result and from GET /repairs' pointer.
	require.Equal(t, "op-plan-refresh-1", res.ReplanOpID)
	require.Empty(t, res.ReplanError)
	require.Equal(t, "op-plan-refresh-1", fx.deps.ops.setting(repairs.LastPlanKey(refreshFixerID)))

	// Until that plan runs the old trial is unchanged: this is the sticky list.
	require.Equal(t, []string{"Hush", "The Long Halloween", "Year One"}, fx.listing("op-plan-1"))

	// The refresh runs with the enqueued params and no longer lists the row.
	fresh := fx.runPlan("op-plan-refresh-1", pp)
	require.Equal(t, 2, fresh.Applicable)
	require.Equal(t, []string{"Hush", "The Long Halloween"}, fx.listing("op-plan-refresh-1"))
	fx.fixer.mu.Lock()
	require.JSONEq(t, string(planParams), string(fx.fixer.params[len(fx.fixer.params)-1]), "the refresh plans with the original params")
	fx.fixer.mu.Unlock()
}

func TestRepairsApply_DryRunDoesNotRefreshTheTrial(t *testing.T) {
	fx := newRefreshFixture(t)
	fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID})
	res, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Hush"]}, true)
	require.NoError(t, err)
	require.Equal(t, 1, res.ByOutcome[repairs.OutcomeWouldApply])
	require.Empty(t, fx.deps.calls())
	require.Empty(t, res.ReplanOpID)
	require.Empty(t, fx.deps.ops.setting(repairs.LastPlanKey(refreshFixerID)))
}

func TestRepairsApply_NothingWrittenDoesNotRefreshTheTrial(t *testing.T) {
	fx := newRefreshFixture(t)
	fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID})
	// The library moved on after the trial: the row replans as changed and the
	// apply writes nothing.
	fx.fixer.mu.Lock()
	fx.fixer.open[fx.books["The Long Halloween"]] = false
	fx.fixer.mu.Unlock()
	res, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["The Long Halloween"]}, false)
	require.NoError(t, err)
	require.Zero(t, res.Applied)
	require.Equal(t, 1, res.ChangedSincePlan)
	require.Empty(t, fx.deps.calls(), "a refused row changed nothing the trial reports")
	require.Empty(t, res.ReplanOpID)
}

// Two owner-applies back to back: the second must not be handed the first's
// plan when that one has already started, because it began before the second
// write and still lists the row. A plan still queued has not started, so it is
// shared.
func TestRepairsApply_BackToBackAppliesNeverShareARunningRefresh(t *testing.T) {
	fx := newRefreshFixture(t)
	params := json.RawMessage(`{"scope":"gotham"}`)
	fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID, Params: params})
	planParamsJSON := func(after string) string {
		b, err := json.Marshal(repairs.PlanParams{FixerID: refreshFixerID, Params: params, AfterApplyOpID: after})
		require.NoError(t, err)
		return string(b)
	}

	res, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Year One"]}, false)
	require.NoError(t, err)
	require.Equal(t, "op-plan-refresh-1", res.ReplanOpID)

	// The first refresh is now RUNNING when the second apply finishes.
	fx.deps.ops.amu.Lock()
	fx.deps.ops.active = []database.OperationV2Row{{ID: "op-plan-refresh-1", DefID: repairs.PlanOpID, Status: "running", Params: planParamsJSON("op-apply-1")}}
	fx.deps.ops.amu.Unlock()
	res, err = fx.runApply("op-apply-2", "op-plan-1", []string{fx.books["Hush"]}, false)
	require.NoError(t, err)
	require.Equal(t, "op-plan-refresh-2", res.ReplanOpID, "a running plan began before this write: a new one is queued")
	require.Len(t, fx.deps.calls(), 2)

	// A QUEUED plan of the same fixer and params is shared (null params count
	// as none), so a run of applies queues one refresh.
	fx.deps.ops.amu.Lock()
	fx.deps.ops.active = []database.OperationV2Row{{ID: "op-plan-queued", DefID: repairs.PlanOpID, Status: "queued", Params: planParamsJSON("op-apply-2")}}
	fx.deps.ops.amu.Unlock()
	fx.fixer.mu.Lock()
	fx.fixer.open[fx.books["The Long Halloween"]] = true
	fx.fixer.mu.Unlock()
	res, err = fx.runApply("op-apply-3", "op-plan-1", []string{fx.books["The Long Halloween"]}, false)
	require.NoError(t, err)
	require.Equal(t, "op-plan-queued", res.ReplanOpID)
	require.Len(t, fx.deps.calls(), 2, "nothing new was enqueued")
	require.Equal(t, "op-plan-queued", fx.deps.ops.setting(repairs.LastPlanKey(refreshFixerID)))

	// A queued plan of another fixer or other params is not this trial's.
	fx.deps.ops.amu.Lock()
	fx.deps.ops.active = []database.OperationV2Row{
		{ID: "op-plan-other-params", DefID: repairs.PlanOpID, Status: "queued", Params: `{"fixer_id":"refresh-fixture","params":{"scope":"arkham"}}`},
		{ID: "op-plan-other-fixer", DefID: repairs.PlanOpID, Status: "queued", Params: `{"fixer_id":"other","params":{"scope":"gotham"}}`},
	}
	fx.deps.ops.amu.Unlock()
	fx.fixer.mu.Lock()
	fx.fixer.open[fx.books["Hush"]] = true
	fx.fixer.mu.Unlock()
	res, err = fx.runApply("op-apply-4", "op-plan-1", []string{fx.books["Hush"]}, false)
	require.NoError(t, err)
	require.Equal(t, "op-plan-refresh-3", res.ReplanOpID)
}

// The apply's writes are made whatever the refresh does: a plan that cannot be
// started is reported on the result, never as a failed apply.
func TestRepairsApply_RefreshFailureIsReportedNotFatal(t *testing.T) {
	fx := newRefreshFixture(t)
	fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID})
	fx.deps.err = errors.New("queue paused")
	res, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Year One"]}, false)
	require.NoError(t, err, "the apply itself succeeded")
	require.Equal(t, 1, res.Applied)
	require.Empty(t, res.ReplanOpID)
	require.Contains(t, res.ReplanError, "queue paused")
	require.Empty(t, fx.deps.ops.setting(repairs.LastPlanKey(refreshFixerID)), "no pointer to a plan that was never started")
}
