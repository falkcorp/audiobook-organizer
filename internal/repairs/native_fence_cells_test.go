// file: internal/repairs/native_fence_cells_test.go
// version: 0.1.0
// guid: 2e7b9d40-6a15-4c83-b1f6-9d04a7c35e18
// last-edited: 2026-11-28

package repairs

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest/parity"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
)

// The native repairs kind, run by core's real executor (the parity kit boots
// it over a real store) through the whole path: plan, approve, apply. A fixer's
// Apply is parked inside the executor's own Writer.Do (core's check already
// passed for the row), the run is stopped through core, and the Apply's next
// write must be refused with tasks.ErrFenced and leave the row as it was.
//
// That is the case HEAD's fence cannot cover: core's executor never puts a HEAD
// fence on the run's context, and opswriter.FenceFromContext of that context
// answers nil (always writable). Each cell asserts the precondition from inside
// the Apply (headFenceNil), so a cell that passed because HEAD's fence happened
// to be there would fail instead.

// stopFixer is the trim fixer whose Apply is parked until the run is stopped.
type stopFixer struct {
	*trimFixer
	mode   string // "ctx": park until the run's context ends, then write; "lease": write, then outlast the lease
	lapse  time.Duration
	parked chan string

	mu    sync.Mutex
	calls map[string]int
	obs   map[string]applyObs
}

// applyObs is what an Apply saw when it was let go.
type applyObs struct {
	err          error // what the write after the stop returned
	cause        error // context.Cause of the context the Apply was given
	headFenceNil bool  // opswriter.FenceFromContext(ctx).Check() == nil: nothing of HEAD's was there
}

func (f *stopFixer) Apply(ctx context.Context, w *Writer, fresh Row) error {
	f.mu.Lock()
	if f.calls == nil {
		f.calls, f.obs = map[string]int{}, map[string]applyObs{}
	}
	f.calls[fresh.RowID]++
	f.mu.Unlock()
	switch f.mode {
	case "ctx":
		select {
		case f.parked <- fresh.RowID:
		default:
		}
		<-ctx.Done()
		err := f.trimFixer.Apply(ctx, w, fresh)
		f.mu.Lock()
		f.obs[fresh.RowID] = applyObs{err: err, cause: context.Cause(ctx), headFenceNil: opswriter.FenceFromContext(ctx).Check() == nil}
		f.mu.Unlock()
		return err
	default: // "lease"
		err := f.trimFixer.Apply(ctx, w, fresh)
		time.Sleep(f.lapse)
		return err
	}
}

func (f *stopFixer) applyCalls(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

func (f *stopFixer) observed(id string) (applyObs, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.obs[id]
	return o, ok
}

// kitPlanRows reads the planned rows of an apply run from the kit's own store:
// the plan run an apply names (PlanRef) and the row the plan stored.
type kitPlanRows struct{ k **parity.Kit }

func (r kitPlanRows) PlanOf(_ context.Context, apply tasks.RunID) (string, error) {
	return string((*r.k).Get(apply).PlanRef), nil
}

func (r kitPlanRows) PlannedRow(ctx context.Context, apply tasks.RunID, rowID string) (json.RawMessage, bool, error) {
	plan := (*r.k).Get(apply).PlanRef
	for row, err := range (*r.k).Raw().Runs.Rows(ctx, plan) {
		if err != nil {
			return nil, false, err
		}
		if row.ID == "plan:"+rowID { // the executor stores a plan's rows under the plan: prefix
			return row.State, true, nil
		}
	}
	return nil, false, nil
}

// standDownLeaseOpt is the kit's Cfg setting the executor's stand-down lease.
// Cfg takes a func over core's internal Config type, which this module cannot
// name, so the function is made by reflection and sets the one field.
func standDownLeaseOpt(d time.Duration) parity.Opt {
	cfgFn := reflect.TypeOf(parity.Cfg).In(0)
	set := reflect.MakeFunc(cfgFn, func(args []reflect.Value) []reflect.Value {
		args[0].Elem().FieldByName("StandDownLease").Set(reflect.ValueOf(d))
		return nil
	})
	return reflect.ValueOf(parity.Cfg).Call([]reflect.Value{set})[0].Interface().(parity.Opt)
}

// nativeApply boots the repairs kind of f over s in the real executor with the
// given attempt timeout (0 keeps the kind's own), plans it, approves b1 and b2,
// and returns the apply run's id.
func nativeApply(t *testing.T, s *memStore, f Fixer, timeout time.Duration, opts ...parity.Opt) (*parity.Kit, tasks.RunID) {
	t.Helper()
	var k *parity.Kit
	rk := newKind(f, KindDeps{
		Guard:  s,
		Series: seriesNamer,
		NewWriter: func(_ context.Context, _ tasks.RunID, f Fixer) (*Writer, error) {
			return NewWriter(s, s, f.ID(), "bulk_update", "rp-"), nil
		},
		PlanRows:     kitPlanRows{&k},
		BindStores:   func(ctx context.Context) context.Context { return WithStores(ctx, "shell") },
		Scopes:       testScopes,
		StandDownKey: "books.scan",
		Concurrency:  1,
	})
	c := rk.common()
	if timeout > 0 {
		c.Timeout = timeout
	}
	k = parity.NewKit(t)
	k.Boot(parity.Defs(rk.definitionOf(c)), append([]parity.Opt{parity.WithPrincipal()}, opts...)...)
	kind := tasks.Kind(KindPrefix + f.ID())
	// The kind's own start and approve requirement (books:edit), held by two
	// callers, as the plan-then-approve gate wants.
	ref, err := k.X.Start(parity.AsUser("robin", "books:edit"), kind, tasks.StartRequest{Mode: tasks.ModeRequestPreview})
	require.NoError(t, err)
	k.WaitState(ref.ID, tasks.StateSucceeded)
	require.NoError(t, k.X.Approve(parity.AsUser("gordon", "books:edit"), ref.ID, tasks.ApproveRequest{Rows: []string{"b1", "b2"}}))
	var apply tasks.RunID
	require.Eventually(t, func() bool {
		for _, r := range k.List([]tasks.Kind{kind}) {
			if r.PlanRef == ref.ID {
				apply = r.ID
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "approving the plan created no apply run")
	return k, apply
}

func newStopFixer(s *memStore, mode string) *stopFixer {
	return &stopFixer{trimFixer: &trimFixer{s: s}, mode: mode, parked: make(chan string, 4)}
}

// requireRefusedAfterStop is the shared assertion of the cancel and timeout
// cells: the Apply parked in core's Do was let go after the stop and its write
// was refused by core's fence, leaving the row as planned.
func requireRefusedAfterStop(t *testing.T, s *memStore, f *stopFixer, row string, cause error) {
	t.Helper()
	require.Eventually(t, func() bool { _, ok := f.observed(row); return ok }, 10*time.Second, 10*time.Millisecond)
	o, _ := f.observed(row)
	require.True(t, o.headFenceNil, "the defect's precondition: no HEAD fence on the context a native run gives its work")
	require.ErrorIs(t, o.cause, cause, "the run's context ends with core's cause")
	require.Error(t, o.err, "the write after the stop is refused")
	require.ErrorIs(t, o.err, tasks.ErrFenced, "refused by core's fence")
	require.False(t, errors.Is(o.err, opswriter.ErrFenced), "not by HEAD's")
	require.Equal(t, map[string]string{"b1": " One ", "b2": " Two "}[row], s.title(row), "no partial row: the title is as planned")
}

func TestNativeFence_RepairsApply_CancelRefusesTheNextWrite(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := newStopFixer(s, "ctx")
	k, apply := nativeApply(t, s, f, 0)
	var row string
	select {
	case row = <-f.parked:
	case <-time.After(30 * time.Second):
		t.Fatalf("the apply never reached a row (state %s)", k.Get(apply).State)
	}
	require.NoError(t, k.X.CancelAs(context.Background(), apply, tasks.Actor{UserID: "alfred"}))
	requireRefusedAfterStop(t, s, f, row, tasks.ErrCanceled)
	r := k.WaitState(apply, tasks.StateCanceled, tasks.StateFailed)
	require.NotEqual(t, tasks.StateSucceeded, r.State)
}

func TestNativeFence_RepairsApply_TimeoutRefusesTheNextWrite(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := newStopFixer(s, "ctx")
	k, apply := nativeApply(t, s, f, 600*time.Millisecond)
	var row string
	select {
	case row = <-f.parked:
	case <-time.After(30 * time.Second):
		t.Fatalf("the apply never reached a row (state %s)", k.Get(apply).State)
	}
	requireRefusedAfterStop(t, s, f, row, tasks.ErrTimedOut)
	r := k.WaitState(apply, tasks.StateTimedOut, tasks.StateFailed, tasks.StateCanceled)
	require.NotEqual(t, tasks.StateSucceeded, r.State)
}

// Lease loss, at the base: the hold lapses (the lease runs out with no write
// renewing it) and core refuses the NEXT row at its start with ErrHoldLost,
// which is a tasks.ErrFenced; the fixer is not called for it and the row is not
// written. A lapse does not end the run's context at the base, so a write made
// inside a row's own Apply after the lapse is not stopped here (see the
// skipped cell below, for #5062).
func TestNativeFence_RepairsApply_LeaseLossRefusesTheNextRow(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := newStopFixer(s, "lease")
	f.lapse = 900 * time.Millisecond
	k, apply := nativeApply(t, s, f, 0, standDownLeaseOpt(400*time.Millisecond))
	r := k.WaitState(apply, tasks.StateFailed, tasks.StateSucceeded, tasks.StateCanceled, tasks.StateTimedOut)
	// A Fixer run counts a refused row and goes on, so the run ends succeeded
	// with the row failed: one row applied, one refused.
	require.JSONEq(t, `{"applied":1,"failed":1}`, string(r.Result), "the first row applied; the second was refused")
	require.Equal(t, 1, f.applyCalls("b1"), "the first row ran under the live hold")
	require.Equal(t, "One", s.title("b1"))
	require.Equal(t, 0, f.applyCalls("b2"), "the second row's start was refused: the fixer is not called")
	require.Equal(t, " Two ", s.title("b2"), "no partial row")
}

// The in-row case of lease loss: a write made inside an Apply after the lease
// lapsed. At the base core's lapse sets a flag the NEXT Writer.Do or item start
// reads and does not end the run's context, so the native fence (which reads
// the context) cannot see it; #5062 (F5-18e r3) cancels the context with
// tasks.ErrHoldLost on a lapse. Re-run this cell, un-skipped, after #5062 is on
// the base: its body is the cancel cell with the stop being the lapse.
func TestNativeFence_RepairsApply_LeaseLostMidRowAfterPR5062(t *testing.T) {
	t.Skip("needs #5062: at the base a lapsed hold does not end the run context, so a write inside a row's own Apply is not refused until the next Do (named in the F5-18 commit 1 hand-back)")
}
