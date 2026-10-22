// file: internal/plugins/maintenance/repairs_ops_planonly_test.go
// version: 1.0.0
// guid: c4e1a97b-3d62-4b08-a5f9-7e20d8b61c43
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// previewThatCouldApply is plan-only, and its Apply would succeed: the only
// thing between it and a write is a refusal.
type previewThatCouldApply struct{ applies atomic.Int32 }

func (*previewThatCouldApply) ID() string          { return "preview-that-could-apply" }
func (*previewThatCouldApply) Title() string       { return "Preview" }
func (*previewThatCouldApply) Description() string { return "test" }
func (*previewThatCouldApply) PlanOnly() bool      { return true }
func (*previewThatCouldApply) Plan(context.Context, json.RawMessage, registry.Reporter) ([]repairs.Row, error) {
	return []repairs.Row{{RowID: "r1", BookIDs: []string{"r1"}, Fingerprint: "f"}}, nil
}
func (*previewThatCouldApply) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	return planned, nil
}
func (f *previewThatCouldApply) Apply(context.Context, *repairs.Writer, repairs.Row) error {
	f.applies.Add(1)
	return nil
}

// planOnlyCountingOps records which op rows repairs.apply reads (it reads its
// own row for the actor first, whatever the fixer; the plan's row is the one
// only a run that got past the op-level check would read).
type planOnlyCountingOps struct {
	*planOps
	cmu  sync.Mutex
	gets []string
}

func (q *planOnlyCountingOps) GetOperationV2(id string) (*database.OperationV2Row, error) {
	q.cmu.Lock()
	q.gets = append(q.gets, id)
	q.cmu.Unlock()
	return q.planOps.GetOperationV2(id)
}

type planOnlyOpsDeps struct {
	fakeDeps
	ops *planOnlyCountingOps
}

func (d planOnlyOpsDeps) OperationQueueStore() OpQueueReader { return d.ops }

// The apply OP refuses a plan-only fixer itself, before it reads the plan: the
// routes and the engine refuse it too, but an op enqueued by another path (a
// retry, a scheduled run, a hand-built params blob) reaches only this check.
// The proof that it is THIS check and not the engine's (which would refuse
// after the plan was read, and would also stop the writes) is that no op row
// read for the plan, and the error carries the op's own prefix.
func TestRepairsOps_ApplyOpRefusesAPlanOnlyFixerBeforeReadingAnything(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ops := &planOnlyCountingOps{planOps: &planOps{MemOpRows: dbtest.NewMemOpRows(), rows: map[string]*database.OperationV2Row{}}}
	p := &Plugin{deps: planOnlyOpsDeps{fakeDeps: fakeDeps{store: st}, ops: ops}}
	fx := &previewThatCouldApply{}
	require.NoError(t, p.Repairs().Register(fx))

	// A stored plan the apply could use, so a run that got past the op-level
	// check would find it and reach the engine.
	planRep := &repairsOpReporter{id: "op-plan"}
	planParams, err := json.Marshal(repairs.PlanParams{FixerID: fx.ID()})
	require.NoError(t, err)
	require.NoError(t, p.runRepairsPlan(context.Background(), planParams, planRep))
	res := planRep.result.(*repairs.PlanResult)
	data, err := json.Marshal(res)
	require.NoError(t, err)
	s := string(data)
	ops.rows["op-plan"] = &database.OperationV2Row{ID: "op-plan", DefID: repairs.PlanOpID, Status: "completed", ResultData: &s}

	dry := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fx.ID(), PlanOpID: "op-plan", RowIDs: []string{"r1"}, DryRun: &dry})
	require.NoError(t, err)
	runErr := p.runRepairsApply(context.Background(), params, &repairsOpReporter{id: "op-apply"})
	require.ErrorIs(t, runErr, repairs.ErrPlanOnly)
	require.ErrorContains(t, runErr, repairs.ApplyOpID+": fixer \""+fx.ID()+"\"", "the op's own refusal, not the engine's")
	ops.cmu.Lock()
	require.NotContains(t, ops.gets, "op-plan", "the op refused before it read the plan's op row")
	ops.cmu.Unlock()
	require.Zero(t, fx.applies.Load(), "nothing was applied")
}
