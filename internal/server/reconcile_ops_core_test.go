// file: internal/server/reconcile_ops_core_test.go
// version: 1.1.0
// guid: d7cd87bc-6284-4a71-a1ab-081002a42264
// last-edited: 2026-11-19

package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
)

// fakeReconcileScanStore plays the engine: it optionally beats the heartbeat,
// hands the saver a result, and returns err.
type fakeReconcileScanStore struct {
	payload  json.RawMessage
	err      error
	beats    int
	gotHooks bool
}

func (f *fakeReconcileScanStore) RunReconcileScan(_ context.Context, save reconcile.SaveResultFunc, _ operations.ProgressReporter, hooks reconcile.ScanHooks) error {
	f.gotHooks = hooks.Heartbeat != nil
	if hooks.Heartbeat != nil {
		hooks.Heartbeat()
		f.beats++
	}
	if f.payload != nil {
		if err := save(f.payload); err != nil {
			return err
		}
	}
	return f.err
}

func TestReconcileScanCore(t *testing.T) {
	t.Run("returns the saved result, forwards it, beats the heartbeat and announces", func(t *testing.T) {
		store := &fakeReconcileScanStore{payload: json.RawMessage(`{"matches":2}`)}
		act := &dedupCallLog{}
		var forwarded []any
		touches := 0
		env := dedupTestEnv(false, "op-scan", &forwarded)
		env.Touch = func() { touches++ }

		got, err := reconcileScanCore(context.Background(), reconcileScanEnv{t7Env: env, Store: store, Activity: act})
		require.NoError(t, err)
		require.JSONEq(t, `{"matches":2}`, string(got))
		require.Equal(t, []any{json.RawMessage(`{"matches":2}`)}, forwarded, "HEAD's SetResult must see what the engine saved")
		require.True(t, store.gotHooks)
		require.Equal(t, 1, touches, "the engine's heartbeat must stamp the run's liveness")
		require.Equal(t, []string{"op-scan"}, act.flushed)
		require.Equal(t, []string{"op-scan|reconcile.scan|Reconcile scan completed"}, act.emitted)
		require.Equal(t, []string{"reconcile"}, act.sources)
	})

	t.Run("a failure is wrapped, keeps the partial result and says failed", func(t *testing.T) {
		boom := errors.New("walk failed")
		store := &fakeReconcileScanStore{payload: json.RawMessage(`{"partial":true}`), err: boom}
		act := &dedupCallLog{}
		got, err := reconcileScanCore(context.Background(), reconcileScanEnv{t7Env: dedupTestEnv(false, "op-scan", nil), Store: store, Activity: act})
		require.ErrorIs(t, err, boom)
		require.ErrorContains(t, err, "reconcile.scan: walk failed")
		require.JSONEq(t, `{"partial":true}`, string(got))
		require.Equal(t, []string{"op-scan|reconcile.scan|Reconcile scan failed: walk failed"}, act.emitted)
	})

	t.Run("a cancel is announced as canceled, not failed", func(t *testing.T) {
		store := &fakeReconcileScanStore{err: context.Canceled}
		act := &dedupCallLog{}
		_, err := reconcileScanCore(context.Background(), reconcileScanEnv{t7Env: dedupTestEnv(false, "op-scan", nil), Store: store, Activity: act})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, []string{"op-scan|reconcile.scan|Reconcile scan canceled"}, act.emitted)
	})

	t.Run("a shell without an activity log still runs", func(t *testing.T) {
		store := &fakeReconcileScanStore{payload: json.RawMessage(`{}`)}
		got, err := reconcileScanCore(context.Background(), reconcileScanEnv{t7Env: dedupTestEnv(false, "", nil), Store: store})
		require.NoError(t, err)
		require.JSONEq(t, `{}`, string(got))
	})

	t.Run("a saver error reaches the engine", func(t *testing.T) {
		store := &fakeReconcileScanStore{payload: json.RawMessage(`{}`)}
		env := dedupTestEnv(false, "op-scan", nil)
		env.SetResult = func(any) error { return errors.New("row write failed") }
		_, err := reconcileScanCore(context.Background(), reconcileScanEnv{t7Env: env, Store: store})
		require.ErrorContains(t, err, "row write failed")
	})
}

// fakeReconcileApplyWriter plays the engine for the apply.
type fakeReconcileApplyWriter struct {
	dedupCallLog
	payloads []json.RawMessage
	err      error
	called   bool
	gotOp    string
	gotN     int
	gotLog   logger.Logger
}

func (f *fakeReconcileApplyWriter) ExecuteReconcile(_ context.Context, opID string, save reconcile.SaveResultFunc, matches []reconcile.ReconcileApplyItem, log logger.Logger) error {
	f.called = true
	f.gotOp, f.gotN, f.gotLog = opID, len(matches), log
	for _, p := range f.payloads {
		if err := save(p); err != nil {
			return err
		}
	}
	return f.err
}

func TestReconcileApplyCore(t *testing.T) {
	two := reconcileApplyOpParams{Matches: make([]reconcile.ReconcileApplyItem, 2)}

	t.Run("an empty operation id is refused before any move", func(t *testing.T) {
		w := &fakeReconcileApplyWriter{}
		_, err := reconcileApplyCore(context.Background(), two, reconcileApplyEnv{t7Env: dedupTestEnv(true, "", nil), Writer: w})
		require.ErrorContains(t, err, "reconcile.apply: reporter returned no operation id")
		require.False(t, w.called, "nothing may be journaled under a blank key")
		require.Empty(t, w.emitted)
	})

	t.Run("applies the matches under the run's id and returns the last saved result", func(t *testing.T) {
		w := &fakeReconcileApplyWriter{payloads: []json.RawMessage{json.RawMessage(`{"done":1}`), json.RawMessage(`{"done":2}`)}}
		var forwarded []any
		got, err := reconcileApplyCore(context.Background(), two, reconcileApplyEnv{t7Env: dedupTestEnv(true, "op-apply", &forwarded), Writer: w})
		require.NoError(t, err)
		require.JSONEq(t, `{"done":2}`, string(got), "the engine saves progressively; the last one is the result")
		require.Len(t, forwarded, 2)
		require.Equal(t, "op-apply", w.gotOp)
		require.Equal(t, 2, w.gotN)
		require.NotNil(t, w.gotLog)
		require.Equal(t, []string{"op-apply"}, w.flushed)
		require.Equal(t, []string{"op-apply|reconcile.apply|Reconcile apply completed: 2 matches applied"}, w.emitted)
		require.Equal(t, []string{"reconcile"}, w.sources)
	})

	t.Run("a failure is returned unwrapped and announced", func(t *testing.T) {
		boom := errors.New("rename failed")
		w := &fakeReconcileApplyWriter{payloads: []json.RawMessage{json.RawMessage(`{"done":1}`)}, err: boom}
		got, err := reconcileApplyCore(context.Background(), two, reconcileApplyEnv{t7Env: dedupTestEnv(true, "op-apply", nil), Writer: w})
		require.Same(t, boom, err)
		require.JSONEq(t, `{"done":1}`, string(got), "what was applied before the failure is kept")
		require.Equal(t, []string{"op-apply|reconcile.apply|Reconcile apply failed: rename failed"}, w.emitted)
	})
}

func TestReconcileApplyPrecheck(t *testing.T) {
	old := config.Get().BulkApplyMaxItems
	t.Cleanup(func() { config.SetForTest(func(c *config.Config) { c.BulkApplyMaxItems = old }) })
	config.SetForTest(func(c *config.Config) { c.BulkApplyMaxItems = 2 })

	require.NoError(t, reconcileApplyPrecheck(reconcileApplyOpParams{Matches: make([]reconcile.ReconcileApplyItem, 2)}))
	err := reconcileApplyPrecheck(reconcileApplyOpParams{Matches: make([]reconcile.ReconcileApplyItem, 3)})
	var ex *applycap.ExceededError
	require.ErrorAs(t, err, &ex)
	require.Equal(t, reconcileApplyOpID, ex.Op)
}

// The precheck refuses a match type the scan never produces before the first
// write, for both shells.
func TestReconcileApplyPrecheck_RefusesAnUnknownMatchType(t *testing.T) {
	old := config.Get().BulkApplyMaxItems
	t.Cleanup(func() { config.SetForTest(func(c *config.Config) { c.BulkApplyMaxItems = old }) })
	config.SetForTest(func(c *config.Config) { c.BulkApplyMaxItems = 10 })

	require.NoError(t, reconcileApplyPrecheck(reconcileApplyOpParams{Matches: []reconcile.ReconcileApplyItem{{BookID: "b1", MatchType: "filename"}}}))
	err := reconcileApplyPrecheck(reconcileApplyOpParams{Matches: []reconcile.ReconcileApplyItem{{BookID: "b1", MatchType: "filname"}}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown match_type")
}

// The native apply refuses an over-cap match set before it builds a writer, as
// HEAD's shell does before its first move.
func TestReconcileApplyNative_RefusesOverCapBeforeAnyWriter(t *testing.T) {
	old := config.Get().BulkApplyMaxItems
	t.Cleanup(func() { config.SetForTest(func(c *config.Config) { c.BulkApplyMaxItems = old }) })
	config.SetForTest(func(c *config.Config) { c.BulkApplyMaxItems = 1 })

	built := false
	deps := t7TestDeps(t)
	deps.NewWriter = func(tasks.Writer) t7Writer {
		built = true
		panic(t7ReachedCore{})
	}
	d := t7ByKind(t, t7ServerDefs(t, deps), t7KindFor(reconcileApplyOpID))

	reached, err := t7RunKind(t, d, tasks.ModeLive, &t7GateWriter{name: "apply"}, `{"matches":[{},{}]}`)
	require.Nil(t, reached)
	require.False(t, built, "an over-cap set must be refused before a writer exists")
	var ex *applycap.ExceededError
	require.ErrorAs(t, err, &ex)
}
