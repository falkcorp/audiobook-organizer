// file: internal/plugins/maintenance/repairs_replan_after_apply_test.go
// version: 1.2.2
// guid: 5e2a7c91-3b48-4d06-9f1e-84c0a6d2b735
// last-edited: 2026-11-05

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// The refresh plan an apply starts is recorded as the fixer's newest plan
// through repairs.LastRunRecorder; pin that the production store satisfies it.
// Kept in a test file so it does not count against the database.Store
// reference ratchet (internal/database/store_consumers_test.go).
var _ repairs.LastRunRecorder = database.Store(nil)

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
	// beforeReplan and onApply, when set, run at the start of a row's Replan
	// and after its Apply wrote: the tests that need a lease lost or a context
	// cancelled between two rows use them.
	beforeReplan func(rowID string)
	onApply      func(rowID string)
	// planErr, when set, fails every Plan call.
	planErr error
}

func (*refreshFixer) ID() string          { return refreshFixerID }
func (*refreshFixer) Title() string       { return "Refresh fixture" }
func (*refreshFixer) Description() string { return "test" }
func (*refreshFixer) Meta() repairs.Meta  { return repairs.Meta{} }

func (f *refreshFixer) Plan(_ context.Context, params json.RawMessage, _ registry.Reporter) ([]repairs.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.params = append(f.params, params)
	if f.planErr != nil {
		return nil, f.planErr
	}
	var rows []repairs.Row
	for id := range f.open {
		if f.open[id] {
			rows = append(rows, repairs.Row{RowID: id, BookIDs: []string{id}, Fingerprint: "open"})
		}
	}
	return rows, nil
}

func (f *refreshFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	if f.beforeReplan != nil {
		f.beforeReplan(planned.RowID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open[planned.RowID] {
		planned.Fingerprint = "closed"
	}
	return planned, nil
}

func (f *refreshFixer) Apply(_ context.Context, _ *repairs.Writer, fresh repairs.Row) error {
	f.mu.Lock()
	f.open[fresh.RowID] = false
	f.mu.Unlock()
	if f.onApply != nil {
		f.onApply(fresh.RowID)
	}
	return nil
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

func (q *refreshOps) GetSetting(key string) (*database.Setting, error) {
	q.amu.Lock()
	defer q.amu.Unlock()
	v, ok := q.settings[key]
	if !ok {
		return nil, errors.New("setting not found")
	}
	return &database.Setting{Key: key, Value: v}, nil
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

	// renew and valid, when set, answer the scan stand-down lease's renewal
	// and validity checks; unset, the lease is always held (fakeDeps).
	renew func() bool
	valid func() bool
}

func (d *refreshDeps) OperationQueueStore() OpQueueReader { return d.ops }

func (d *refreshDeps) RenewScanStandDown(h string) bool {
	if d.renew != nil {
		return d.renew()
	}
	return d.fakeDeps.RenewScanStandDown(h)
}

func (d *refreshDeps) ScanStandDownValid(h string) bool {
	if d.valid != nil {
		return d.valid()
	}
	return d.fakeDeps.ScanStandDownValid(h)
}

func (d *refreshDeps) EnqueueOp(ctx context.Context, defID string, params any) (string, error) {
	// A real enqueue writes through ctx: a cancelled one fails.
	if err := ctx.Err(); err != nil {
		return "", err
	}
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
	t     *testing.T
	p     *Plugin
	deps  *refreshDeps
	fixer *refreshFixer
	books map[string]string // title -> book id
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
	fx.deps.ops.rows[opID] = &database.OperationV2Row{ID: opID, DefID: repairs.PlanOpID, Status: "completed", ResultData: &s, Params: string(raw)}
	fx.deps.ops.planOps.mu.Unlock()
	return res
}

// runApply runs the apply op and returns its stored result.
func (fx *refreshFixture) runApply(opID, planOpID string, rowIDs []string, dryRun bool) (*repairs.ApplyResult, error) {
	fx.t.Helper()
	return fx.runApplyCtx(context.Background(), opID, planOpID, rowIDs, dryRun)
}

func (fx *refreshFixture) runApplyCtx(ctx context.Context, opID, planOpID string, rowIDs []string, dryRun bool) (*repairs.ApplyResult, error) {
	fx.t.Helper()
	raw, err := json.Marshal(repairs.ApplyParams{FixerID: refreshFixerID, PlanOpID: planOpID, RowIDs: rowIDs, DryRun: &dryRun})
	require.NoError(fx.t, err)
	rep := &repairsOpReporter{id: opID}
	runErr := fx.p.runRepairsApply(ctx, raw, rep)
	res, _ := rep.result.(*repairs.ApplyResult)
	return res, runErr
}

// refreshOf runs the refresh plan an apply enqueued (the newest EnqueueOp call)
// under the op id the apply reported, as the registry would.
func (fx *refreshFixture) refreshOf(res *repairs.ApplyResult) *repairs.PlanResult {
	fx.t.Helper()
	require.NotEmpty(fx.t, res.ReplanOpID)
	calls := fx.deps.calls()
	require.NotEmpty(fx.t, calls)
	pp, ok := calls[len(calls)-1].params.(repairs.PlanParams)
	require.True(fx.t, ok)
	return fx.runPlan(res.ReplanOpID, pp)
}

// loadErr is what the rows endpoint and an apply meet loading a stored plan.
func (fx *refreshFixture) loadErr(planOpID string) error {
	fx.t.Helper()
	ops := fx.deps.ops
	rows, err := repairs.PlanRowStoreOf(ops)
	require.NoError(fx.t, err)
	_, err = repairs.LoadPlan(ops, rows, planOpID, refreshFixerID)
	return err
}

// gateAfterFirstApply makes the fixer's rows run one at a time at the one beat
// that matters: the first row to start replans and applies at once, every other
// row waits in its Replan until that apply has written and afterFirst has run.
// The default apply runs four rows at once, so without the gate a test cannot
// say which rows a lease lost or a cancel between two rows still leaves to run.
func gateAfterFirstApply(f *refreshFixer, afterFirst func()) {
	var mu sync.Mutex
	first := ""
	flipped := make(chan struct{})
	var once sync.Once
	f.beforeReplan = func(rowID string) {
		mu.Lock()
		if first == "" {
			first = rowID
		}
		isFirst := first == rowID
		mu.Unlock()
		if isFirst {
			return
		}
		select {
		case <-flipped:
		case <-time.After(5 * time.Second):
		}
	}
	f.onApply = func(string) {
		once.Do(func() {
			afterFirst()
			close(flipped)
		})
	}
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

// SF2: the lane keeps showing a good trial when the refresh never completes.
// The last-plan pointer moves at enqueue (the lane follows that plan and shows
// its progress); the completed-plan pointer moves only when a plan has stored
// all its rows, so a failed or dropped refresh leaves it on the last good one.
func TestRepairsApply_FailedOrDroppedRefreshLeavesTheCompletedPointerOnTheLastGoodTrial(t *testing.T) {
	fx := newRefreshFixture(t)
	last, completed := repairs.LastPlanKey(refreshFixerID), repairs.LastCompletedPlanKey(refreshFixerID)
	require.Empty(t, fx.deps.ops.setting(completed))
	fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID})
	require.Equal(t, "op-plan-1", fx.deps.ops.setting(completed), "a finished plan is the completed one")

	res, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Year One"]}, false)
	require.NoError(t, err)
	require.Equal(t, "op-plan-refresh-1", res.ReplanOpID)

	// Dropped (a restart: repairs.plan is ResumeDrop) or still queued: the
	// refresh stored nothing.
	require.Equal(t, "op-plan-refresh-1", fx.deps.ops.setting(last), "the lane follows the refresh")
	require.Equal(t, "op-plan-1", fx.deps.ops.setting(completed), "but the completed pointer still names a trial that loads")
	require.NoError(t, fx.loadErr(fx.deps.ops.setting(completed)))

	// Failed: the plan errors before it stores its rows.
	fx.fixer.mu.Lock()
	fx.fixer.planErr = errors.New("library unreadable")
	fx.fixer.mu.Unlock()
	raw, err := json.Marshal(fx.deps.calls()[0].params)
	require.NoError(t, err)
	require.Error(t, fx.p.runRepairsPlan(context.Background(), raw, &repairsOpReporter{id: "op-plan-refresh-1"}))
	require.Equal(t, "op-plan-1", fx.deps.ops.setting(completed))
	require.NoError(t, fx.loadErr("op-plan-1"))

	// Once a refresh completes the pointer moves to it.
	fx.fixer.mu.Lock()
	fx.fixer.planErr = nil
	fx.fixer.mu.Unlock()
	fx.refreshOf(res)
	require.Equal(t, "op-plan-refresh-1", fx.deps.ops.setting(completed))
}

// SF3: every apply that writes stores a full plan row set, so a refresh
// retires the refresh it replaces, and nothing else.
func TestRepairsPlan_PrunesTheRefreshItReplacesButNeverTheReviewersTrial(t *testing.T) {
	fx := newRefreshFixture(t)
	params := json.RawMessage(`{"scope":"gotham"}`)
	fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID, Params: params})

	res1, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Year One"]}, false)
	require.NoError(t, err)
	fx.refreshOf(res1)
	refresh1 := res1.ReplanOpID
	require.NoError(t, fx.loadErr("op-plan-1"), "the reviewer's own trial is never pruned")
	require.NoError(t, fx.loadErr(refresh1), "the completed pointer's old target was a trial, not a refresh")

	res2, err := fx.runApply("op-apply-2", refresh1, []string{fx.books["Hush"]}, false)
	require.NoError(t, err)
	require.NotEqual(t, refresh1, res2.ReplanOpID)
	fx.refreshOf(res2)
	refresh2 := res2.ReplanOpID

	// The first refresh's rows are gone; the trial and the new refresh stay.
	err = fx.loadErr(refresh1)
	require.ErrorIs(t, err, repairs.ErrPlanPruned)
	require.True(t, repairs.PlanNeedsRerun(err), "served as REPAIR_PLAN_NEEDS_RERUN")
	require.NoError(t, fx.loadErr("op-plan-1"))
	require.NoError(t, fx.loadErr(refresh2))
	require.Equal(t, []string{"The Long Halloween"}, fx.listing(refresh2))
	require.Equal(t, refresh2, fx.deps.ops.setting(repairs.LastCompletedPlanKey(refreshFixerID)))

	// An old tab still showing the pruned refresh and applying from it is told
	// to re-run, and nothing is written.
	before := len(fx.deps.calls())
	_, err = fx.runApply("op-apply-3", refresh1, []string{fx.books["The Long Halloween"]}, false)
	require.Error(t, err)
	require.True(t, repairs.PlanNeedsRerun(err), "the apply is refused with the re-run error: %v", err)
	require.Len(t, fx.deps.calls(), before, "a refused apply starts no refresh")
	fx.fixer.mu.Lock()
	require.True(t, fx.fixer.open[fx.books["The Long Halloween"]], "nothing was written")
	fx.fixer.mu.Unlock()
}

func TestRepairsPlan_PruneLeavesWhatItMustNotTouch(t *testing.T) {
	gotham := json.RawMessage(`{"scope":"gotham"}`)
	arkham := json.RawMessage(`{"scope":"arkham"}`)

	t.Run("a plan an apply is still running from", func(t *testing.T) {
		fx := newRefreshFixture(t)
		fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID, Params: gotham})
		res1, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Year One"]}, false)
		require.NoError(t, err)
		fx.refreshOf(res1)
		refresh1 := res1.ReplanOpID
		ap, err := json.Marshal(repairs.ApplyParams{FixerID: refreshFixerID, PlanOpID: refresh1})
		require.NoError(t, err)
		fx.deps.ops.amu.Lock()
		fx.deps.ops.active = []database.OperationV2Row{{ID: "op-apply-live", DefID: repairs.ApplyOpID, Status: "running", Params: string(ap)}}
		fx.deps.ops.amu.Unlock()
		res2, err := fx.runApply("op-apply-2", refresh1, []string{fx.books["Hush"]}, false)
		require.NoError(t, err)
		fx.refreshOf(res2)
		require.NoError(t, fx.loadErr(refresh1), "an apply is reading it: the rows stay")
	})

	t.Run("a plan with other params", func(t *testing.T) {
		fx := newRefreshFixture(t)
		fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID, Params: gotham})
		res1, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Year One"]}, false)
		require.NoError(t, err)
		fx.refreshOf(res1)
		fx.runPlan("op-plan-arkham", repairs.PlanParams{FixerID: refreshFixerID, Params: arkham, AfterApplyOpID: "op-apply-9"})
		require.NoError(t, fx.loadErr(res1.ReplanOpID), "another trial's refresh does not retire this one")
	})

	t.Run("a trial of other params does not retire a refresh", func(t *testing.T) {
		fx := newRefreshFixture(t)
		fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID, Params: gotham})
		res1, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Year One"]}, false)
		require.NoError(t, err)
		fx.refreshOf(res1)
		fx.runPlan("op-plan-2", repairs.PlanParams{FixerID: refreshFixerID, Params: arkham})
		require.NoError(t, fx.loadErr(res1.ReplanOpID), "a manual trial of another scope leaves it")
		require.NoError(t, fx.loadErr("op-plan-1"))
	})

	t.Run("a manual trial of the same params retires the refresh it supersedes", func(t *testing.T) {
		fx := newRefreshFixture(t)
		fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID, Params: gotham})
		res1, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Year One"]}, false)
		require.NoError(t, err)
		fx.refreshOf(res1)
		fx.runPlan("op-plan-2", repairs.PlanParams{FixerID: refreshFixerID, Params: gotham})
		require.ErrorIs(t, fx.loadErr(res1.ReplanOpID), repairs.ErrPlanPruned, "the reviewer's new trial replaces it")
		require.NoError(t, fx.loadErr("op-plan-2"))
		require.NoError(t, fx.loadErr("op-plan-1"), "and the reviewer's earlier trial is never pruned")
	})
}

// A reviewer who started a trial with other params after the apply is looking
// at that one: the refresh must not take the last-plan pointer from it.
func TestRepairsApply_RefreshLeavesANewerTrialWithOtherParamsAsTheLastPlan(t *testing.T) {
	fx := newRefreshFixture(t)
	last := repairs.LastPlanKey(refreshFixerID)
	fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID, Params: json.RawMessage(`{"scope":"gotham"}`)})
	fx.runPlan("op-plan-arkham", repairs.PlanParams{FixerID: refreshFixerID, Params: json.RawMessage(`{"scope":"arkham"}`)})
	fx.deps.ops.amu.Lock()
	fx.deps.ops.settings[last] = "op-plan-arkham"
	fx.deps.ops.amu.Unlock()

	res, err := fx.runApply("op-apply-1", "op-plan-1", []string{fx.books["Year One"]}, false)
	require.NoError(t, err)
	require.Equal(t, "op-plan-refresh-1", res.ReplanOpID, "the refresh is still enqueued and reported")
	require.Equal(t, "op-plan-arkham", fx.deps.ops.setting(last), "but the newer trial stays the last plan")

	// With the same params the refresh does replace it.
	fx.deps.ops.amu.Lock()
	fx.deps.ops.settings[last] = "op-plan-1"
	fx.deps.ops.amu.Unlock()
	fx.fixer.mu.Lock()
	fx.fixer.open[fx.books["Hush"]] = true
	fx.fixer.mu.Unlock()
	res, err = fx.runApply("op-apply-2", "op-plan-1", []string{fx.books["Hush"]}, false)
	require.NoError(t, err)
	require.Equal(t, res.ReplanOpID, fx.deps.ops.setting(last))
}

// SF4: a cancel or a lost lease cuts the apply short after some rows wrote;
// those rows leave the trial stale just as a finished apply's do.
func TestRepairsApply_CancelledApplyStillEnqueuesTheRefresh(t *testing.T) {
	fx := newRefreshFixture(t)
	fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gateAfterFirstApply(fx.fixer, cancel)

	res, _ := fx.runApplyCtx(ctx, "op-apply-1", "op-plan-1",
		[]string{fx.books["Year One"], fx.books["The Long Halloween"], fx.books["Hush"]}, false)
	require.Error(t, ctx.Err(), "the apply was cancelled mid-run")
	require.NotNil(t, res)
	require.GreaterOrEqual(t, res.Applied, 1, "the first row wrote before the cancel")
	require.Empty(t, res.ReplanError, "the refresh is enqueued through a context the cancel does not reach")
	require.Equal(t, "op-plan-refresh-1", res.ReplanOpID)
	require.Len(t, fx.deps.calls(), 1)
}

func TestRepairsApply_LostStandDownStillEnqueuesTheRefreshForWhatWasWritten(t *testing.T) {
	fx := newRefreshFixture(t)
	fx.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID})
	var lost atomic.Bool
	fx.deps.renew = func() bool { return !lost.Load() }
	fx.deps.valid = func() bool { return !lost.Load() }
	gateAfterFirstApply(fx.fixer, func() { lost.Store(true) })

	res, err := fx.runApply("op-apply-1", "op-plan-1",
		[]string{fx.books["Year One"], fx.books["The Long Halloween"], fx.books["Hush"]}, false)
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	require.NotNil(t, res)
	require.Equal(t, 1, res.Applied, "one row wrote before the lease lapsed")
	require.Equal(t, 2, res.ByOutcome[repairs.OutcomeAborted])
	require.Equal(t, "op-plan-refresh-1", res.ReplanOpID, "the written row is owed a refresh whatever ended the run")
	require.Len(t, fx.deps.calls(), 1)

	// A lost lease that wrote nothing refreshes nothing.
	fx2 := newRefreshFixture(t)
	fx2.runPlan("op-plan-1", repairs.PlanParams{FixerID: refreshFixerID})
	fx2.deps.renew = func() bool { return false }
	res2, err := fx2.runApply("op-apply-1", "op-plan-1", []string{fx2.books["Hush"]}, false)
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	require.Zero(t, res2.Applied)
	require.Empty(t, fx2.deps.calls())
}

// Why PlanParams.AfterApplyOpID exists: the registry folds an enqueue into any
// active op of the same def with byte-identical params (registry.go, the
// ConcurrencyKey dedupe), queued or running. A refresh enqueued with the
// trial's bare params would be handed the plan already running, which began
// before this apply's writes and still lists them.
func TestRepairsPlanRegistry_AfterApplyOpIDKeepsARefreshFromFoldingIntoARunningPlan(t *testing.T) {
	fx := newRefreshFixture(t)
	reg := registry.New(fx.deps.store, slog.New(slog.DiscardHandler), 4, nil)
	def := fx.p.repairsPlanDef()
	require.Equal(t, repairs.PlanOpID, def.ConcurrencyKey, "the dedupe applies to the real plan def")
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	def.Run = func(ctx context.Context, _ json.RawMessage, _ sdk.Reporter) error {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	require.NoError(t, reg.RegisterOp(def))
	ctx, stop := context.WithCancel(context.Background())
	reg.Start(ctx)
	t.Cleanup(func() {
		close(release)
		stop()
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		_ = reg.Shutdown(sctx)
	})

	params := json.RawMessage(`{"scope":"gotham"}`)
	trial, err := reg.EnqueueOp(ctx, repairs.PlanOpID, repairs.PlanParams{FixerID: refreshFixerID, Params: params})
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the plan never started running")
	}

	// The same request while it runs is folded into it: the pin.
	again, err := reg.EnqueueOp(ctx, repairs.PlanOpID, repairs.PlanParams{FixerID: refreshFixerID, Params: params})
	require.NoError(t, err)
	require.Equal(t, trial, again, "byte-identical params fold into the running plan")

	// The refresh's params differ by the apply that asked for it: a new plan.
	refresh, err := reg.EnqueueOp(ctx, repairs.PlanOpID, repairs.PlanParams{FixerID: refreshFixerID, Params: params, AfterApplyOpID: "op-apply-1"})
	require.NoError(t, err)
	require.NotEqual(t, trial, refresh, "a refresh is never folded into a plan that began before the write")

	// Two applies are two refreshes, but one apply asking twice is one.
	other, err := reg.EnqueueOp(ctx, repairs.PlanOpID, repairs.PlanParams{FixerID: refreshFixerID, Params: params, AfterApplyOpID: "op-apply-2"})
	require.NoError(t, err)
	require.NotEqual(t, refresh, other)
	same, err := reg.EnqueueOp(ctx, repairs.PlanOpID, repairs.PlanParams{FixerID: refreshFixerID, Params: params, AfterApplyOpID: "op-apply-1"})
	require.NoError(t, err)
	require.Equal(t, refresh, same)
}
