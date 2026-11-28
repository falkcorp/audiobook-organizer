// file: internal/books/writer/gate_empty_row_run_test.go
// version: 0.1.0
// guid: 4a9c1e72-83d5-4f60-b2e7-6c0d8a35f194
// last-edited: 2026-11-28

package writer_test

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest/parity"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
)

// Every method of the shared table with an EMPTY row (the sentinel
// MarkBookAggregatesBackfillDone; the op rows ReplaceOpRowsPrefix, SaveClassLists
// and ClearClassLists; the calls that write nothing themselves,
// InvalidateAuthorsCache, NewUserStateProbe, HardDeleteWithoutUserState) stamps
// no Effects resource, so it cannot go through Writer.Do, which refuses a write
// that names none. Gate.Do used to run it with no fence at all: a run core had
// canceled, timed out or lost the hold of kept writing through it. These cells
// run a real kind in core's real executor, stop it by each cause, and call every
// empty-row method through a Gate over the run's own writer, from a context
// with no run in it (what the group writers' runless() gives): the call must be
// refused with tasks.ErrFenced and its function must not run.

// emptyRowMethods is every empty-row method of the shared table, sorted.
func emptyRowMethods(t *testing.T) []string {
	t.Helper()
	var out []string
	for m, row := range writer.SharedRows() {
		if len(row) == 0 {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	// The named four are the ones that write; the table lists three more. A
	// table that lost them (or grew) must be looked at, not silently re-pinned.
	require.GreaterOrEqual(t, len(out), 7, "the shared table's empty rows: %v", out)
	for _, m := range []string{"MarkBookAggregatesBackfillDone", "ReplaceOpRowsPrefix", "SaveClassLists", "ClearClassLists"} {
		require.Contains(t, out, m)
	}
	return out
}

// emptyRowOutcome is what one empty-row call did.
type emptyRowOutcome struct {
	ran bool
	err error
}

type emptyRowRun struct {
	mu   sync.Mutex
	out  map[string]emptyRowOutcome
	done chan struct{}
}

func (r *emptyRowRun) outcomes() map[string]emptyRowOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := map[string]emptyRowOutcome{}
	for k, v := range r.out {
		cp[k] = v
	}
	return cp
}

// leaseOpt sets the executor's stand-down lease. parity.Cfg takes a func over
// core's internal Config, which this module cannot name, so it is built by
// reflection and sets the one field.
func leaseOpt(d time.Duration) parity.Opt {
	cfgFn := reflect.TypeOf(parity.Cfg).In(0)
	set := reflect.MakeFunc(cfgFn, func(args []reflect.Value) []reflect.Value {
		args[0].Elem().FieldByName("StandDownLease").Set(reflect.ValueOf(d))
		return nil
	})
	return reflect.ValueOf(parity.Cfg).Call([]reflect.Value{set})[0].Interface().(parity.Opt)
}

// startEmptyRowRun boots a kind whose body (when `wait` returns) calls every
// empty-row method through a Gate over rc.Writer(). wait runs with the run's RC
// after it signalled started.
func startEmptyRowRun(t *testing.T, timeout time.Duration, hold bool, wait func(rc *tasks.RC), opts ...parity.Opt) (*parity.Kit, tasks.RunID, *emptyRowRun, *parity.Probe) {
	t.Helper()
	methods := emptyRowMethods(t)
	run := &emptyRowRun{out: map[string]emptyRowOutcome{}, done: make(chan struct{})}
	k := parity.NewKit(t)
	p := k.Probe("books.gate-empty-row")
	p.Body(func(p *parity.Probe, rc *tasks.RC, _ parity.Params) (string, error) {
		if hold {
			h, err := rc.StandDown("books.scan")
			if err != nil {
				return "", err
			}
			defer h.Release()
		}
		select {
		case p.Started <- rc.RunID():
		default:
		}
		wait(rc)
		g := writer.ForRun(rc.Writer(), writer.SharedRows())
		for _, m := range methods {
			var o emptyRowOutcome
			o.err = g.Err(context.Background(), m, writer.Ref("books", ""), func() error { o.ran = true; return nil })
			run.mu.Lock()
			run.out[m] = o
			run.mu.Unlock()
		}
		close(run.done)
		return "", nil
	})
	defs := []parity.DefOpt{parity.Resume(tasks.ResumeFromZero)}
	if timeout > 0 {
		defs = append(defs, parity.Timeout(timeout))
	}
	k.Boot(parity.Defs(p.Def(defs...)), opts...)
	id := k.Start(p.Kind, nil)
	select {
	case <-p.Started:
	case <-time.After(30 * time.Second):
		t.Fatalf("the run never started (state %s)", k.Get(id).State)
	}
	return k, id, run, p
}

func requireAllRefused(t *testing.T, run *emptyRowRun, methods []string) {
	t.Helper()
	select {
	case <-run.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the body never reached the empty-row calls")
	}
	got := run.outcomes()
	require.Len(t, got, len(methods))
	for _, m := range methods {
		o := got[m]
		require.False(t, o.ran, "%s ran after the stop", m)
		require.ErrorIs(t, o.err, tasks.ErrFenced, "%s was not refused by core's fence", m)
	}
}

func TestGate_EmptyRowCancelTimeoutLease(t *testing.T) {
	methods := emptyRowMethods(t)

	t.Run("cancel", func(t *testing.T) {
		k, id, run, _ := startEmptyRowRun(t, 0, false, func(rc *tasks.RC) { <-rc.Context().Done() })
		require.NoError(t, k.X.CancelAs(context.Background(), id, tasks.Actor{UserID: "alfred"}))
		requireAllRefused(t, run, methods)
	})

	t.Run("timeout", func(t *testing.T) {
		_, _, run, _ := startEmptyRowRun(t, 500*time.Millisecond, false, func(rc *tasks.RC) { <-rc.Context().Done() })
		requireAllRefused(t, run, methods)
	})

	// The lease runs out with no write renewing it; the context is NOT ended at
	// the base, so only the executor's permit and lease check can refuse.
	t.Run("lease loss", func(t *testing.T) {
		_, _, run, _ := startEmptyRowRun(t, 0, true, func(*tasks.RC) { time.Sleep(900 * time.Millisecond) }, leaseOpt(400*time.Millisecond))
		requireAllRefused(t, run, methods)
	})

	// The control: the same run, never stopped and under a live hold, runs every
	// call, so the refusals above are the stop and nothing else.
	t.Run("control: a live run runs every call", func(t *testing.T) {
		_, _, run, _ := startEmptyRowRun(t, 0, true, func(*tasks.RC) {})
		select {
		case <-run.done:
		case <-time.After(30 * time.Second):
			t.Fatal("the body never reached the empty-row calls")
		}
		got := run.outcomes()
		require.Len(t, got, len(methods))
		for _, m := range methods {
			require.NoError(t, got[m].err, m)
			require.True(t, got[m].ran, m)
		}
	})
}
