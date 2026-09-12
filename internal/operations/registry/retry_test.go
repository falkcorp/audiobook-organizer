// file: internal/operations/registry/retry_test.go
// version: 1.1.0
// guid: 7c2e9f14-5b3a-4d86-a1f0-3e8b6c9d2a47
// last-edited: 2026-09-12

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// startedRetryRegistry returns a started registry over store. Rows are planted
// AFTER Start in these tests on purpose: Start runs the boot resume sweep, which
// would otherwise resume an interrupted_quiesced row on its own before the
// manual retry under test ever ran.
func startedRetryRegistry(t *testing.T, store *fakeStore, defs ...registry.OperationDef) *registry.Registry {
	t.Helper()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{WatchdogInterval: 30 * time.Second})
	for _, d := range defs {
		if err := r.RegisterOp(d); err != nil {
			t.Fatalf("register %s: %v", d.ID, err)
		}
	}
	r.Start(t.Context())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(ctx)
	})
	return r
}

func waitForRetryStatus(t *testing.T, store *fakeStore, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if store.statusOf(id) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("op %s: status %q, want %q", id, store.statusOf(id), want)
}

func opCount(store *fakeStore) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.ops)
}

func rowOf(t *testing.T, store *fakeStore, id string) database.OperationV2Row {
	t.Helper()
	row, err := store.GetOperationV2(id)
	if err != nil || row == nil {
		t.Fatalf("op %s: row missing (%v)", id, err)
	}
	return *row
}

// Every status in the interrupted family — including the legacy bare
// "interrupted" and interrupted_dropped, which the boot sweep never resumes —
// is re-queued as the SAME row: it runs under the same id, finishes there, and
// no second row is minted. resume_count is untouched; the retry is recorded in
// manual_retry_count instead.
func TestRetryInterrupted_ReRunsTheSameRowForEveryInterruptedStatus(t *testing.T) {
	statuses := []string{"interrupted", "interrupted_quiesced", "interrupted_ask", "interrupted_restart", "interrupted_dropped"}
	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			store := newFakeStore()
			var runs atomic.Int32
			def := makeValidDef("test.retry-same-row")
			def.ResumePolicy = registry.ResumeRestart
			def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
				runs.Add(1)
				return nil
			}
			r := startedRetryRegistry(t, store, def)

			id := insertOpV2(store, def.ID, "test", 1, status, `{"k":"v"}`)
			before := opCount(store)

			if err := r.RetryInterrupted(t.Context(), id, "alice"); err != nil {
				t.Fatalf("RetryInterrupted: %v", err)
			}
			waitForRetryStatus(t, store, id, "completed")

			if got := runs.Load(); got != 1 {
				t.Errorf("Run called %d times, want 1", got)
			}
			if got := opCount(store); got != before {
				t.Errorf("row count %d -> %d: a retry of an interrupted op must not mint a new row", before, got)
			}
			row := rowOf(t, store, id)
			if row.ResumeCount != 0 {
				t.Errorf("resume_count = %d, want 0: a manual retry is not an automatic restart", row.ResumeCount)
			}
			if row.ManualRetryCount != 1 {
				t.Errorf("manual_retry_count = %d, want 1", row.ManualRetryCount)
			}
		})
	}
}

// The run's log continues under the one id: the first run's line, then the
// retry boundary naming the operator, then the resumed run's own line.
func TestRetryInterrupted_LogsContinueUnderOneIDWithABoundaryLine(t *testing.T) {
	store := newFakeStore()
	def := makeValidDef("test.retry-logs")
	def.ResumePolicy = registry.ResumeRestart
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		return rep.Log(slog.LevelInfo, "resumed run line")
	}
	r := startedRetryRegistry(t, store, def)

	id := insertOpV2(store, def.ID, "test", 1, "interrupted_quiesced", "{}")
	_ = store.AppendOpLogsV2([]database.OpLogV2Row{{
		OperationID: id, Level: "info", Message: "first run line", CreatedAt: time.Now().Add(-time.Hour),
	}})

	if err := r.RetryInterrupted(t.Context(), id, "alice"); err != nil {
		t.Fatalf("RetryInterrupted: %v", err)
	}
	waitForRetryStatus(t, store, id, "completed")

	var msgs []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logs, _ := store.GetOpLogsV2(id, 1000)
		sort.SliceStable(logs, func(i, j int) bool { return logs[i].CreatedAt.Before(logs[j].CreatedAt) })
		msgs = msgs[:0]
		for _, l := range logs {
			msgs = append(msgs, l.Message)
		}
		if strings.Contains(strings.Join(msgs, "|"), "resumed run line") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	joined := strings.Join(msgs, "|")
	iFirst := strings.Index(joined, "first run line")
	iRetry := strings.Index(joined, "manual retry requested by alice")
	iResumed := strings.Index(joined, "resumed run line")
	if iFirst < 0 || iRetry < 0 || iResumed < 0 || iFirst >= iRetry || iRetry >= iResumed {
		t.Fatalf("log under %s should read first run -> retry boundary -> resumed run; got %q", id, msgs)
	}
}

// A saved checkpoint is merged into params for a restartable def, and consumed.
// A ResumeRequeue def declares "re-run from zero", so its checkpoint is dropped.
func TestRetryInterrupted_CheckpointHonoredExceptForRequeueDefs(t *testing.T) {
	cases := []struct {
		policy     registry.ResumePolicy
		wantResume bool
	}{
		{registry.ResumeRestart, true},
		{registry.ResumeAsk, true},
		{registry.ResumeRequeue, false},
	}
	for _, tc := range cases {
		t.Run(map[registry.ResumePolicy]string{registry.ResumeRestart: "restart", registry.ResumeAsk: "ask", registry.ResumeRequeue: "requeue"}[tc.policy], func(t *testing.T) {
			store := newFakeStore()
			var mu sync.Mutex
			var got map[string]any
			def := makeValidDef("test.retry-checkpoint")
			def.ResumePolicy = tc.policy
			def.Run = func(_ context.Context, params json.RawMessage, _ registry.Reporter) error {
				mu.Lock()
				defer mu.Unlock()
				_ = json.Unmarshal(params, &got)
				return nil
			}
			r := startedRetryRegistry(t, store, def)

			id := insertOpV2(store, def.ID, "test", 1, "interrupted_ask", `{"folder":"a"}`)
			_ = store.UpsertOpStateV2(database.OpStateV2Row{
				OperationID: id, StateBlob: []byte(`{"resume_offset":42}`), SchemaVersion: 2, WrittenAt: time.Now(),
			})

			if err := r.RetryInterrupted(t.Context(), id, "alice"); err != nil {
				t.Fatalf("RetryInterrupted: %v", err)
			}
			waitForRetryStatus(t, store, id, "completed")

			mu.Lock()
			defer mu.Unlock()
			if got["folder"] != "a" {
				t.Errorf("original params lost: %v", got)
			}
			_, resumed := got["resume_offset"]
			if resumed != tc.wantResume {
				t.Errorf("resume_offset present = %v, want %v (params %v)", resumed, tc.wantResume, got)
			}
			if st, _ := store.GetOpStateV2(id); st != nil {
				t.Errorf("checkpoint must be consumed or cleared by the retry, still present: %s", st.StateBlob)
			}
		})
	}
}

// The double-run guard: requeueInPlace bypasses EnqueueOp's dedupe, so a live
// run of the same def must refuse the retry, and nothing may be written.
// A crash-leftover "running" row with no live handle must NOT refuse it (the
// same zombie rule EnqueueOp applies), or one stale row would block every retry
// of the def until restart.
func TestRetryInterrupted_RefusesWhileAnotherRunOfTheDefIsLive(t *testing.T) {
	store := newFakeStore()
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	def := makeValidDef("test.retry-guard")
	def.ResumePolicy = registry.ResumeRestart
	def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	r := startedRetryRegistry(t, store, def)
	defer close(release)

	liveID, err := r.EnqueueOp(t.Context(), def.ID, map[string]string{"live": "1"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	<-started
	waitForRetryStatus(t, store, liveID, "running")

	id := insertOpV2(store, def.ID, "test", 1, "interrupted_quiesced", "{}")
	err = r.RetryInterrupted(t.Context(), id, "alice")
	if !errors.Is(err, registry.ErrOpActive) {
		t.Fatalf("err = %v, want ErrOpActive while %s is running", err, liveID)
	}
	if got := store.statusOf(id); got != "interrupted_quiesced" {
		t.Errorf("refused retry wrote status %q; must leave the row untouched", got)
	}
	if row := rowOf(t, store, id); row.ManualRetryCount != 0 {
		t.Errorf("refused retry recorded manual_retry_count=%d", row.ManualRetryCount)
	}
}

func TestRetryInterrupted_IgnoresZombieRunningRowOfTheDef(t *testing.T) {
	store := newFakeStore()
	def := makeValidDef("test.retry-zombie")
	def.ResumePolicy = registry.ResumeRestart
	r := startedRetryRegistry(t, store, def)

	_ = insertOpV2(store, def.ID, "test", 1, "running", "{}") // no live handle
	id := insertOpV2(store, def.ID, "test", 1, "interrupted_dropped", "{}")
	if err := r.RetryInterrupted(t.Context(), id, "alice"); err != nil {
		t.Fatalf("a zombie running row must not refuse the retry: %v", err)
	}
	waitForRetryStatus(t, store, id, "completed")
}

func TestRetryInterrupted_RefusesUnknownAndNonInterruptedRows(t *testing.T) {
	store := newFakeStore()
	def := makeValidDef("test.retry-refuse")
	def.ResumePolicy = registry.ResumeRestart
	r := startedRetryRegistry(t, store, def)

	// Pebble answers (nil, nil) for a missing id and RetryInterrupted maps that
	// to ErrOpNotFound; this fake answers with an error instead, so only the
	// refusal itself is asserted here. The handler 404s on either shape.
	before := opCount(store)
	if err := r.RetryInterrupted(t.Context(), "no-such-op", "alice"); err == nil {
		t.Errorf("unknown id: retry must be refused")
	}
	if got := opCount(store); got != before {
		t.Errorf("a refused retry of an unknown id created %d row(s)", got-before)
	}
	for _, status := range []string{"failed", "canceled", "completed", "queued"} {
		id := insertOpV2(store, def.ID, "test", 1, status, "{}")
		if err := r.RetryInterrupted(t.Context(), id, "alice"); !errors.Is(err, registry.ErrOpNotRetryable) {
			t.Errorf("%s: err = %v, want ErrOpNotRetryable", status, err)
		}
	}
	orphan := insertOpV2(store, "test.not-registered", "test", 1, "interrupted_ask", "{}")
	if err := r.RetryInterrupted(t.Context(), orphan, "alice"); !errors.Is(err, registry.ErrOpNotRetryable) {
		t.Errorf("unregistered def: err = %v, want ErrOpNotRetryable", err)
	}
}

// The force-drop trap. checkInfiniteRestart drops a ResumeRestart row whose
// resume_count >= 3 with no progress — which is exactly the state of a row it
// already force-dropped. The operator's retries of such a row must run, three
// times in a row, and not be re-dropped on dispatch.
func TestRetryInterrupted_ManualRetriesDoNotTripTheRestartStrikeGuard(t *testing.T) {
	store := newFakeStore()
	var runs atomic.Int32
	def := makeValidDef("test.retry-strike")
	def.ResumePolicy = registry.ResumeRestart
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		runs.Add(1)
		return nil
	}
	r := startedRetryRegistry(t, store, def)

	id := insertOpV2(store, def.ID, "test", 1, "interrupted_dropped", "{}")
	store.mu.Lock()
	row := store.ops[id]
	row.ResumeCount = 3 // what a force-dropped row looks like
	row.HighWaterProgress = 0
	store.ops[id] = row
	store.mu.Unlock()

	for i := 1; i <= 3; i++ {
		if err := r.RetryInterrupted(t.Context(), id, "alice"); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		waitForRetryStatus(t, store, id, "completed")
		if got := runs.Load(); got != int32(i) {
			t.Fatalf("after manual retry %d Run ran %d times; the op was force-dropped instead of run", i, got)
		}
		now := time.Now().UTC()
		msg := "interrupted again"
		_ = store.UpdateOperationV2Status(id, "interrupted_dropped", nil, &now, &msg)
	}
	final := rowOf(t, store, id)
	if final.ResumeCount != 3 || final.ManualRetryCount != 3 || final.ResumeCountAtManualRetry != 3 {
		t.Errorf("counters resume=%d manual=%d baseline=%d, want 3/3/3",
			final.ResumeCount, final.ManualRetryCount, final.ResumeCountAtManualRetry)
	}
}

// The hazard the old new-row retry left behind: the interrupted_quiesced row
// stayed in the boot resume set, and once the retry had finished no queued or
// running row of the def existed to supersede it, so the next restart ran the
// same work again. With a same-row retry the row ends completed and the next
// boot has nothing to resume.
func TestRetryInterrupted_NextBootDoesNotRerunARetriedQuiescedOp(t *testing.T) {
	store := newFakeStore()
	var runs atomic.Int32
	newDef := func() registry.OperationDef {
		def := makeValidDef("test.retry-boot")
		def.ResumePolicy = registry.ResumeRestart
		def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
			runs.Add(1)
			return nil
		}
		return def
	}

	r1 := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{WatchdogInterval: 30 * time.Second})
	if err := r1.RegisterOp(newDef()); err != nil {
		t.Fatal(err)
	}
	r1.Start(t.Context())
	id := insertOpV2(store, "test.retry-boot", "test", 1, "interrupted_quiesced", "{}")
	if err := r1.RetryInterrupted(t.Context(), id, "alice"); err != nil {
		t.Fatalf("RetryInterrupted: %v", err)
	}
	// Wait for the RUN, then for every row of the def to settle.
	deadline := time.Now().Add(5 * time.Second)
	for runs.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for time.Now().Before(deadline) {
		if rows, _ := store.ListActiveOperationsV2(); len(rows) == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = r1.Shutdown(ctx)
	cancel()

	r2 := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{WatchdogInterval: 30 * time.Second})
	if err := r2.RegisterOp(newDef()); err != nil {
		t.Fatal(err)
	}
	r2.Start(t.Context())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r2.Shutdown(ctx)
	})
	time.Sleep(300 * time.Millisecond)

	if got := runs.Load(); got != 1 {
		t.Fatalf("Run ran %d times across the retry and the next boot, want 1: the retried work ran again on restart", got)
	}
	resumable, _ := store.ListResumableOperationsV2()
	if len(resumable) != 0 {
		t.Errorf("rows still in the boot resume set after the retry finished: %+v", resumable)
	}
}

// A concurrent EnqueueOp of the same def must not slip a second run in between
// RetryInterrupted's same-def check and its re-queue. manualRetryHook holds the
// retry inside that window while an EnqueueOp of a DedupeQueuedRuns def (which
// dedupes against any active run) is started. It must come back with the
// retried row's id, and no second row may exist.
//
// Mutation check: drop the admission lock from EnqueueOp and the enqueue reads
// the active set while the retried row is still interrupted, misses it, and
// inserts a second row inside the hook's window.
func TestRetryInterrupted_ConcurrentEnqueueCannotSlipASecondRunIn(t *testing.T) {
	store := newFakeStore()
	release := make(chan struct{})
	def := makeValidDef("test.retry-admission")
	def.ResumePolicy = registry.ResumeRestart
	def.ConcurrencyKey = "retry.admission"
	def.DedupeQueuedRuns = true
	def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	r := startedRetryRegistry(t, store, def)
	defer close(release)

	id := insertOpV2(store, def.ID, "test", 1, "interrupted_quiesced", "{}")

	type result struct {
		id  string
		err error
	}
	enqueued := make(chan result, 1)
	var returnedInsideWindow atomic.Bool
	store.manualRetryHook = func() {
		go func() {
			got, err := r.EnqueueOp(context.Background(), def.ID, nil)
			enqueued <- result{got, err}
		}()
		// Ample time for the enqueue to reach its dedupe read. With the
		// admission lock it is parked on the lock; without it, it finishes here
		// against an active set that does not contain the retried row.
		time.Sleep(200 * time.Millisecond)
		select {
		case res := <-enqueued:
			returnedInsideWindow.Store(true)
			enqueued <- res
		default:
		}
	}
	if err := r.RetryInterrupted(t.Context(), id, "alice"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	store.manualRetryHook = nil

	var res result
	select {
	case res = <-enqueued:
	case <-time.After(5 * time.Second):
		t.Fatal("the concurrent EnqueueOp never returned")
	}
	if res.err != nil {
		t.Fatalf("enqueue: %v", res.err)
	}
	if returnedInsideWindow.Load() {
		t.Error("EnqueueOp finished while the retry was between its same-def check and its re-queue")
	}
	if res.id != id {
		t.Errorf("EnqueueOp returned %s, want the retried row %s (deduped against it)", res.id, id)
	}
	if n := opCount(store); n != 1 {
		t.Errorf("%d operation rows exist, want 1: a second run was admitted next to the retry", n)
	}
}

// While a watchdog-abandoned Run of an op is still executing, Retry on that op
// must be refused: re-queuing it would start a second Run of the same op id
// next to the first. Once the runaway goroutine returns, Retry is accepted and
// the op runs again under the same id.
//
// Mutation check: drop the abandonedAlive refusal in RetryInterrupted and the
// first Retry is accepted while the first Run is still blocked.
func TestRetryInterrupted_RefusesWhileAnAbandonedRunIsStillExecuting(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,
		AbandonedCap:     10,
		AbandonGrace:     100 * time.Millisecond,
	})
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	def := makeValidDef("test.retry-abandoned")
	def.Plugin = "retry-abandoned-plugin"
	def.ResumePolicy = registry.ResumeDrop
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release // ignores ctx: a Run that will not die when canceled
		}
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("register: %v", err)
	}
	r.Start(ctx)
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(sctx)
	})
	var releaseOnce sync.Once
	releaseRun := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRun()

	id, err := r.EnqueueOp(ctx, def.ID, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first Run did not start")
	}
	_ = r.Cancel(id)
	// The watchdog marks the run abandoned-but-alive before it writes this
	// status, so once the status is visible the guard is armed.
	awaitStatus(t, store, id, "interrupted_dropped", 5*time.Second)

	err = r.RetryInterrupted(ctx, id, "alice")
	if !errors.Is(err, registry.ErrOpActive) {
		t.Fatalf("err = %v, want ErrOpActive while the abandoned Run is still executing", err)
	}
	if got := store.statusOf(id); got != "interrupted_dropped" {
		t.Errorf("refused retry wrote status %q; must leave the row untouched", got)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("Run called %d times while the first was still executing, want 1", n)
	}

	releaseRun()
	deadline := time.Now().Add(5 * time.Second)
	for r.AbandonedCount(def.Plugin) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.RetryInterrupted(ctx, id, "alice"); err != nil {
		t.Fatalf("retry after the abandoned Run returned: %v", err)
	}
	waitForRetryStatus(t, store, id, "completed")
	if n := calls.Load(); n != 2 {
		t.Errorf("Run called %d times in total, want 2", n)
	}
}
