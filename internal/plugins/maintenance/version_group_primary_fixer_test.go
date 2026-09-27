// file: internal/plugins/maintenance/version_group_primary_fixer_test.go
// version: 1.0.0
// guid: 7b2d9e46-0c81-4a37-b5f9-1e6c3a8d4f20
// last-edited: 2026-09-27

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// ---- fakes ----

// scriptedScan is a scan controller: a scan is "running" until an acquire
// succeeds (it is then paused) and resumes on release. failAcquires acquires
// fail first; renewsLeft (-1 = unlimited) counts successful renewals.
type scriptedScan struct {
	mu                  sync.Mutex
	failAcquires        int
	acquires, releases  int
	renewsLeft          int
	running, pausedByUs bool
}

func (s *scriptedScan) AcquireScanStandDown(_ context.Context, holder, _ string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquires++
	if holder == "" {
		return nil, errors.New("empty holder")
	}
	if s.failAcquires > 0 {
		s.failAcquires--
		return nil, errors.New("scan stand-down: scan did not park within 60s")
	}
	if s.running {
		s.running, s.pausedByUs = false, true
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.releases++
		if s.pausedByUs {
			s.running = true
		}
	}, nil
}

func (s *scriptedScan) RenewScanStandDown(string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.renewsLeft < 0 {
		return true
	}
	if s.renewsLeft == 0 {
		return false
	}
	s.renewsLeft--
	return true
}

func (s *scriptedScan) ScanStandDownValid(string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewsLeft != 0
}

// planOps serves stored op rows to repairs.apply.
type planOps struct {
	mu   sync.Mutex
	rows map[string]*database.OperationV2Row
}

func (q *planOps) ListActiveOperationsV2() ([]database.OperationV2Row, error) { return nil, nil }
func (q *planOps) GetOperationV2(id string) (*database.OperationV2Row, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.rows[id], nil
}

// scanDeps is fakeDeps with a scripted scan controller and a plan store.
type scanDeps struct {
	fakeDeps
	scan *scriptedScan
	ops  *planOps
}

func (d scanDeps) AcquireScanStandDown(ctx context.Context, h, r string) (func(), error) {
	return d.scan.AcquireScanStandDown(ctx, h, r)
}
func (d scanDeps) RenewScanStandDown(h string) bool { return d.scan.RenewScanStandDown(h) }
func (d scanDeps) ScanStandDownValid(h string) bool { return d.scan.ScanStandDownValid(h) }
func (d scanDeps) OperationQueueStore() OpQueueReader {
	if d.ops != nil {
		return d.ops
	}
	return d.fakeDeps.OperationQueueStore()
}

type repairsOpReporter struct {
	fakeReporter
	id     string
	mu     sync.Mutex
	result any
}

func (r *repairsOpReporter) OpID() string { return r.id }
func (r *repairsOpReporter) SetResult(v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = v
	return nil
}

var noWait = repairs.WaitOptions{Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}

func (f *vgRepairFixture) plugin(deps ServerDeps) *Plugin {
	return &Plugin{deps: deps, standDownWait: noWait}
}

// withRoot points config.AppConfig.RootDir at the fixture root for the
// fixer, which reads it the way the op's Run does.
func withRoot(t *testing.T, root string) {
	t.Helper()
	old := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = old })
}

// stubFixerProbe makes the fixer's ffprobe the fixture's stub.
func (f *vgRepairFixture) fixer(p *Plugin) *vgPrimaryFixer {
	fx := newVGPrimaryFixer(p)
	fx.probe = f.probe
	return fx
}

// ---- adapter == op ----

type vgPlanView struct {
	kind, winner, demoted, fills string
}

// TestVGPrimaryFixer_PlanMatchesTheOpDryRun is the no-duplication proof: on
// the same fixture, every group the op's dry run reports appears as one row
// with the same decision, winner, demotions and carried fields.
func TestVGPrimaryFixer_PlanMatchesTheOpDryRun(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	withRoot(t, f.root)
	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{})
	require.NoError(t, err)

	p := f.plugin(fakeDeps{store: f.s})
	rows, err := f.fixer(p).Plan(context.Background(), nil, &opIDReporter{id: "op-plan"})
	require.NoError(t, err)
	require.Len(t, rows, len(rep.Groups))

	fromOp := map[string]vgPlanView{}
	for i := range rep.Groups {
		g := &rep.Groups[i]
		fromOp[g.GroupID] = viewOf(g)
	}
	for _, r := range rows {
		g, ok := r.Detail.(*vgRepairGroupReport)
		require.True(t, ok)
		require.Equal(t, fromOp[r.RowID], viewOf(g), "group %s", r.RowID)
		require.Equal(t, vgWritable(g.Kind), r.Applicable(), "group %s", r.RowID)
		require.NotEmpty(t, r.Fingerprint)
	}

	byID := map[string]repairs.Row{}
	for _, r := range rows {
		byID[r.RowID] = r
	}
	double := byID["vg-double"]
	require.Equal(t, []string{"A", "B", "C"}, double.BookIDs, "every live member, not only the changed ones")
	require.Equal(t, "A", double.Proposed["primary"])
	require.Equal(t, "B, C", double.Proposed["demote"])
	require.Equal(t, "description, publisher, asin", double.Proposed["fill_from_B"])
	require.Equal(t, "A, B", double.Current["primaries"])
	require.Equal(t, repairs.RiskReview, double.Risk, "carried fields make it a review row")
	require.Equal(t, "Book A", double.Title)
	held := byID["vg-held"]
	require.False(t, held.Applicable())
}

func viewOf(g *vgRepairGroupReport) vgPlanView {
	v := vgPlanView{kind: g.Kind, winner: g.WinnerID, demoted: strings.Join(g.DemotedIDs, ",")}
	if g.CarryOver != nil {
		var fs []string
		for _, fl := range g.CarryOver.Fills {
			fs = append(fs, fl.Field+"="+fl.Value)
		}
		sort.Strings(fs)
		v.fills = strings.Join(fs, ";")
	}
	return v
}

// ---- through the framework ops ----

func runPlanOp(t *testing.T, p *Plugin, ops *planOps, opID string) *repairs.PlanResult {
	t.Helper()
	params, err := json.Marshal(repairs.PlanParams{FixerID: vgPrimaryFixerID})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, p.runRepairsPlan(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.PlanResult)
	require.True(t, ok)
	data, err := json.Marshal(res)
	require.NoError(t, err)
	s := string(data)
	ops.mu.Lock()
	ops.rows[opID] = &database.OperationV2Row{ID: opID, DefID: repairs.PlanOpID, Status: "completed", ResultData: &s}
	ops.mu.Unlock()
	return res
}

func runApplyOp(t *testing.T, p *Plugin, planOpID string, rowIDs []string, dryRun *bool) (*repairs.ApplyResult, error) {
	t.Helper()
	params, err := json.Marshal(repairs.ApplyParams{FixerID: vgPrimaryFixerID, PlanOpID: planOpID, RowIDs: rowIDs, DryRun: dryRun})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: "op-apply"}
	runErr := p.runRepairsApply(context.Background(), params, rep)
	res, _ := rep.result.(*repairs.ApplyResult)
	return res, runErr
}

func newRepairsFixture(t *testing.T, scan *scriptedScan) (*vgRepairFixture, *Plugin, *planOps) {
	t.Helper()
	f := newVGRepairFixture(t)
	f.seed(t)
	withRoot(t, f.root)
	ops := &planOps{rows: map[string]*database.OperationV2Row{}}
	p := f.plugin(scanDeps{fakeDeps: fakeDeps{store: f.s}, scan: scan, ops: ops})
	fx := p.Repairs()
	vf, _ := fx.Get(vgPrimaryFixerID)
	vf.(*vgPrimaryFixer).probe = f.probe
	return f, p, ops
}

func TestRepairsOps_ApplyWritesThroughTheOpWriterWithHistory(t *testing.T) {
	scan := &scriptedScan{renewsLeft: -1, running: true}
	f, p, ops := newRepairsFixture(t, scan)
	plan := runPlanOp(t, p, ops, "op-plan")
	require.Equal(t, 2, plan.Total)
	require.Equal(t, 1, plan.Applicable)

	// Omitted mode is a preview: nothing written, no stand-down.
	res, err := runApplyOp(t, p, "op-plan", []string{"vg-double"}, nil)
	require.NoError(t, err)
	require.True(t, res.DryRun)
	require.Equal(t, 1, res.ByOutcome[repairs.OutcomeWouldApply])
	require.Zero(t, scan.acquires)
	require.Equal(t, "true", f.flag(t, "B"))

	no := false
	res, err = runApplyOp(t, p, "op-plan", []string{"vg-double", "vg-held"}, &no)
	require.NoError(t, err, "a running scan is paused, not a refusal")
	require.Equal(t, 1, res.Applied)
	require.Equal(t, 1, res.ByOutcome[repairs.OutcomeNotApplicable])
	require.True(t, scan.pausedByUs)
	require.Equal(t, 1, scan.releases)
	require.True(t, scan.running, "the scan resumes after the apply")

	require.Equal(t, "true", f.flag(t, "A"))
	require.Equal(t, "false", f.flag(t, "B"))
	require.Equal(t, "false", f.flag(t, "C"))
	require.Equal(t, "true", f.flag(t, "S"), "held group untouched")
	a, err := f.s.GetBookByID("A")
	require.NoError(t, err)
	require.Equal(t, "donor description", *a.Description)
	require.Equal(t, "Keep", *a.Narrator)

	hist, err := f.s.GetBookChangeHistory("B", 100)
	require.NoError(t, err)
	require.Len(t, hist, 1)
	require.Equal(t, "is_primary_version", hist[0].Field)
	require.Equal(t, vgPrimaryFixerID, hist[0].Source)
	require.True(t, strings.HasPrefix(hist[0].BatchID, "repairs-"))
	histA, err := f.s.GetBookChangeHistory("A", 100)
	require.NoError(t, err)
	require.Len(t, histA, 3, "one row per carried field")
	// A: 3 carried fields; B: flag true→false; C: flag nil→false.
	require.Equal(t, 5, res.HistoryRows)
}

func TestRepairsOps_ApplyRefusesAGroupChangedSincePlan(t *testing.T) {
	f, p, ops := newRepairsFixture(t, &scriptedScan{renewsLeft: -1})
	runPlanOp(t, p, ops, "op-plan")
	// Another writer fixes B's flag after the plan.
	_, err := f.s.ModifyBook("B", func(b *database.Book) error {
		v := false
		b.IsPrimaryVersion = &v
		return nil
	})
	require.NoError(t, err)
	no := false
	res, err := runApplyOp(t, p, "op-plan", []string{"vg-double"}, &no)
	require.NoError(t, err)
	require.Equal(t, 1, res.ChangedSincePlan)
	require.Zero(t, res.Applied)
	require.Equal(t, "nil", f.flag(t, "C"), "nothing written for the changed group")
	hist, err := f.s.GetBookChangeHistory("A", 100)
	require.NoError(t, err)
	require.Empty(t, hist)
}

func TestRepairsOps_AcquireFailureWaitsThenApplies(t *testing.T) {
	scan := &scriptedScan{renewsLeft: -1, running: true, failAcquires: 3}
	f, p, ops := newRepairsFixture(t, scan)
	runPlanOp(t, p, ops, "op-plan")
	no := false
	res, err := runApplyOp(t, p, "op-plan", []string{"vg-double"}, &no)
	require.NoError(t, err)
	require.Equal(t, 4, scan.acquires, "three failed attempts, then the one that held")
	require.Equal(t, 1, res.Applied)
	require.Equal(t, "false", f.flag(t, "B"))
}

func TestRepairsOps_LeaseLapseAbortsAndPersistsTheReport(t *testing.T) {
	scan := &scriptedScan{renewsLeft: 0}
	f, p, ops := newRepairsFixture(t, scan)
	runPlanOp(t, p, ops, "op-plan")
	no := false
	res, err := runApplyOp(t, p, "op-plan", []string{"vg-double"}, &no)
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	require.NotNil(t, res, "the per-row report is persisted before the abort returns")
	require.Equal(t, 1, res.ByOutcome[repairs.OutcomeAborted])
	require.Equal(t, "true", f.flag(t, "B"))
}

func TestRepairsOps_PlanRunsDuringAScanWithNoStandDown(t *testing.T) {
	scan := &scriptedScan{renewsLeft: -1, running: true}
	_, p, ops := newRepairsFixture(t, scan)
	runPlanOp(t, p, ops, "op-plan")
	require.Zero(t, scan.acquires)
	require.True(t, scan.running)
}

// ---- the old op's stand-down retrofit ----

func TestVGPrimaryRepair_ApplyPausesARunningScanInsteadOfRefusing(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	scan := &scriptedScan{renewsLeft: -1, running: true, failAcquires: 2}
	p := f.plugin(scanDeps{fakeDeps: fakeDeps{store: f.s}, scan: scan})
	rep, err := p.versionGroupPrimaryRepair(context.Background(), vgPrimaryRepairParams{
		Apply: true, GroupIDs: []string{"vg-double"},
	}, f.root, f.probe, &opIDReporter{id: "op-vgpr"})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Applied)
	require.Equal(t, 3, scan.acquires, "an acquire failure is waited out, not a refusal")
	require.True(t, scan.pausedByUs)
	require.True(t, scan.running, "resumed on release")
}

func TestVGPrimaryRepair_LeaseLapseAborts(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	scan := &scriptedScan{renewsLeft: 0}
	p := f.plugin(scanDeps{fakeDeps: fakeDeps{store: f.s}, scan: scan})
	rep, err := p.versionGroupPrimaryRepair(context.Background(), vgPrimaryRepairParams{
		Apply: true, GroupIDs: []string{"vg-double"},
	}, f.root, f.probe, &opIDReporter{id: "op-vgpr"})
	require.ErrorIs(t, err, errVGRepairStandDownLost)
	require.NotEmpty(t, rep.Aborted)
	require.Equal(t, "true", f.flag(t, "B"))
}
