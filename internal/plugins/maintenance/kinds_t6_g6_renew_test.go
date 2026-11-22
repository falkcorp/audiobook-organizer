// file: internal/plugins/maintenance/kinds_t6_g6_renew_test.go
// version: 0.1.0
// guid: b3a9e6d4-5c18-4f72-9d0b-27e8c41a6f35
// last-edited: 2026-11-22

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// TODO-F5-11-HOLD-RENEW (F5-18e): the native stand-down renews its hold from the
// shell's own heartbeat and a lapse stops the shell. Core's lease tests
// (core/internal/executor/standdown_renew_test.go) pin the lease itself; these
// pin that the group 6 shells use it.

// Lost is the renew: each call extends the lease (a counted Hold.Renew) and a
// lapse reports true from then on. Returning false without renewing fails the
// count; renewing without reporting the error fails the lapse.
func TestT6G6NativeStandDown_LostRenewsTheHoldAndReportsALapse(t *testing.T) {
	h := &t6g6Host{FakeRun: taskstest.NewFakeRun(context.Background(), recoverMissingFilesKind), mode: tasks.ModeLive}
	rc := tasks.NewRC(h)
	hold, err := t6g6NativeStandDown(rc, "books.gotham-watch")(context.Background(), "recover apply")
	require.NoError(t, err)

	for range 3 {
		require.False(t, hold.Lost(), "a hold inside its lease is not lost")
	}
	require.Equal(t, 3, h.renewCount(), "every Lost call renews the lease (HEAD's per-item heartbeat)")

	h.lapseWhen(3)
	require.True(t, hold.Lost(), "a lapsed lease reports lost")
	require.True(t, hold.Lost(), "and stays lost")
	require.False(t, h.released, "Lost does not release")
	hold.Release()
	require.True(t, h.released)
}

// t6g6RecoverCase is the recover-missing-files live case of the shell table.
func t6g6RecoverCase(t *testing.T) t6g6WriteCase {
	t.Helper()
	for _, c := range t6g6WriteCases() {
		if c.kind == recoverMissingFilesKind {
			return c
		}
	}
	t.Fatal("no recover-missing-files case in the shell table")
	return t6g6WriteCase{}
}

// runT6G6KindOn is runT6G6Kind over a host the test built (to arm its hold).
func runT6G6KindOn(t *testing.T, d tasks.Definition, h *t6g6Host, params string) error {
	t.Helper()
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok, "%T has no ExecutorRunners", d)
	decode, task, _, _ := r.ExecutorRunners()
	require.NotNil(t, task, "%s is not a Task", d.Kind())
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	p, err := decode(raw)
	require.NoError(t, err)
	_, err = task.Run(tasks.NewRC(h), p)
	return err
}

// A native recover apply renews the hold while it works and stamps liveness from
// its heartbeat; a hold whose lease lapsed before the first write stops it with
// nothing written. Each of the three is a separate mutant: Lost not renewing (the
// count), Lost always false (the writes), and the heartbeat not stamping (the
// touches).
func TestT6G6NativeRecover_RenewsTheHoldStampsLivenessAndStopsOnALapse(t *testing.T) {
	c := t6g6RecoverCase(t)
	gate := &t6g6GateWriter{name: "gotham"}
	run := func(t *testing.T, lapse bool) (*t6g6Probe, *t6g6Host) {
		p := newT6G6Probe(t)
		c.setup(t, p)
		def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), recoverMissingFilesKind)
		h := &t6g6Host{FakeRun: taskstest.NewFakeRun(context.Background(), def.Kind()), mode: tasks.ModeLive, w: gate}
		if lapse {
			h.lapseWhen(0)
		}
		require.NoError(t, runT6G6KindOn(t, def, h, c.params))
		return p, h
	}

	p, h := run(t, false)
	require.Equal(t, c.live, p.rec.snapshot(), "a held lease writes")
	require.Positive(t, h.renewCount(), "the apply renewed the hold from its heartbeat")
	require.Positive(t, h.touches.Load(), "the apply stamped the run's liveness from its heartbeat")

	p, h = run(t, true)
	require.Empty(t, p.rec.snapshot(), "a lapsed hold writes nothing")
	require.Positive(t, h.renewCount())
	require.True(t, h.released, "the lapsed hold is still released at the end")
}

// The repoint's per-row loop asks the hold before each write: a lapsed lease
// leaves the row where it was.
func TestT6G6NativeRepoint_ALapsedHoldLeavesTheRowAlone(t *testing.T) {
	gate := &t6g6GateWriter{name: "gotham"}
	run := func(t *testing.T, lapse bool) (rbfFixture, *t6g6Probe, *t6g6Host, error) {
		f := newRBFFixture(t, -1, "-")
		p := newT6G6Probe(t)
		p.store = t6g6RBFStore{s: f.s}
		p.rec.backing = f.s
		def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), rbfKind)
		b, err := json.Marshal(map[string]any{"repoints": []rbfRepoint{f.item()}})
		require.NoError(t, err)
		h := &t6g6Host{FakeRun: taskstest.NewFakeRun(context.Background(), def.Kind()), mode: tasks.ModeLive, w: gate}
		if lapse {
			h.lapseWhen(0)
		}
		return f, p, h, runT6G6KindOn(t, def, h, string(b))
	}

	f, _, h, err := run(t, false)
	require.NoError(t, err)
	require.Equal(t, f.newPath, f.storedRow(t).FilePath, "a held lease repoints the row")
	require.Positive(t, h.renewCount(), "the repoint renewed the hold")

	f, p, h, _ := run(t, true)
	require.Equal(t, f.oldPath, f.storedRow(t).FilePath, "a lapsed hold must not repoint the row")
	require.NotContains(t, p.rec.snapshot(), "ModifyBookFile "+f.row)
	require.Positive(t, h.renewCount())
}
