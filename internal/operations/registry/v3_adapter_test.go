// file: internal/operations/registry/v3_adapter_test.go
// version: 1.4.1
// guid: 3ffc035f-1ee4-4409-afce-613375ef8fcd
// last-edited: 2026-10-20

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// newPebbleV3 is a real in-memory Pebble store: its v2 writes stage the opv3
// run record (05-PR4), so CommitLedgerV3 and the intent writers have a run.
func newPebbleV3(t *testing.T) *database.PebbleStore {
	t.Helper()
	p, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// startV3Registry starts a registry on store; Shutdown runs at cleanup,
// before the store closes.
func startV3Registry(t *testing.T, store database.OpsV2Store, opts Options) *Registry {
	t.Helper()
	if opts.WatchdogInterval == 0 {
		opts.WatchdogInterval = time.Hour
	}
	if opts.SweepInterval == 0 {
		opts.SweepInterval = time.Hour
	}
	r := NewWithOptions(store, slog.Default(), 4, opts)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = r.Shutdown(sctx)
		cancel()
	})
	return r
}

func awaitTerminal(t *testing.T, store database.OpsV2Store, id string) *database.OperationV2Row {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		row, err := store.GetOperationV2(id)
		if err == nil && row != nil && isTerminalStatus(row.Status) {
			return row
		}
		time.Sleep(10 * time.Millisecond)
	}
	row, _ := store.GetOperationV2(id)
	t.Fatalf("op %s did not finish: %+v", id, row)
	return nil
}

func resultOf(t *testing.T, row *database.OperationV2Row, into any) {
	t.Helper()
	if row.ResultData == nil {
		t.Fatalf("op %s has no result (status %s, error %v)", row.ID, row.Status, row.ErrorMessage)
	}
	if err := json.Unmarshal([]byte(*row.ResultData), into); err != nil {
		t.Fatalf("result %s: %v", *row.ResultData, err)
	}
}

func TestAdaptV2DefRunsUnchanged(t *testing.T) {
	store := newPebbleV3(t)
	r := startV3Registry(t, store, Options{})
	var ran atomic.Int32
	v2 := OperationDef{ID: "test.v2-adapted", Plugin: "test", DisplayName: "Adapted", ConcurrencyKey: "test.key",
		ResumePolicy: ResumeDrop, Liveness: LivenessManual, Writes: []Resource{ResBooks},
		Run: func(context.Context, json.RawMessage, Reporter) error { ran.Add(1); return nil }}
	adapted := FromV2(v2)
	if adapted.Kind() != ops.KindAdapted || adapted.ID() != v2.ID {
		t.Fatalf("FromV2 = %s %s", adapted.Kind(), adapted.ID())
	}
	if err := ops.ValidateCatalog([]ops.Definition{adapted}, nil); err != nil {
		t.Fatalf("an adapted def with no permission relies on X2: %v", err)
	}
	if err := Register(r, "test", adapted); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Def(v2.ID)
	if !ok || got.ConcurrencyKey != "test.key" || got.ResumePolicy != ResumeDrop || got.Liveness != LivenessManual ||
		!slices.Equal(got.Writes, v2.Writes) || got.Permissions != nil {
		t.Fatalf("the adapted def was changed on the way in: %+v", got)
	}
	id, err := r.EnqueueOp(context.Background(), v2.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row := awaitTerminal(t, store, id); row.Status != "completed" || ran.Load() != 1 {
		t.Fatalf("adapted run: %s, ran %d", row.Status, ran.Load())
	}
	if cat := r.Catalog(); len(cat) != 1 || cat[0].Kind() != ops.KindAdapted {
		t.Fatalf("catalog = %v", cat)
	}
}

func TestNativeDefRunsOnRegistry(t *testing.T) {
	store := newPebbleV3(t)
	r := startV3Registry(t, store, Options{})
	type res struct {
		Seen    int64 `json:"seen"`
		Skipped int64 `json:"skipped"`
	}
	def := ops.Batch("test.native-batch", ops.BatchSpec[ops.NoParams, int, res]{
		Title:      "native",
		Effects:    ops.ReadOnly(ops.ResBooks),
		Permission: auth.PermLibraryView,
		Source: func(*ops.RC, ops.NoParams) (ops.Source[int], error) {
			return ops.Source[int]{Items: seq(700), Key: itemKey}, nil
		},
		Item: func(rc *ops.ItemRC, _ ops.NoParams, i int) error {
			rc.Count("seen", 1)
			if i%100 == 0 {
				rc.Skip("hundred")
			}
			return nil
		},
		Finish: func(_ *ops.RC, _ ops.NoParams, s ops.Summary) (res, error) {
			return res{Seen: s.Counter("seen"), Skipped: s.Skipped}, nil
		},
	})
	if err := Register(r, "test", def); err != nil {
		t.Fatal(err)
	}
	od, _ := r.Def("test.native-batch")
	if od.Liveness != LivenessRunItems || od.ResumePolicy != ResumeRestart || od.ConcurrencyKey != "test.native-batch" ||
		!slices.Equal(od.Permissions, []auth.Permission{auth.PermLibraryView}) || od.Timeout != 24*time.Hour {
		t.Fatalf("registered def = %+v", od)
	}
	id, err := r.EnqueueOp(context.Background(), "test.native-batch", nil)
	if err != nil {
		t.Fatal(err)
	}
	row := awaitTerminal(t, store, id)
	if row.Status != "completed" {
		t.Fatalf("status %s: %v", row.Status, row.ErrorMessage)
	}
	var got res
	resultOf(t, row, &got)
	if got.Seen != 700 || got.Skipped != 7 {
		t.Fatalf("result = %+v", got)
	}
	rec, err := store.GetRunV3(id)
	if err != nil || rec == nil {
		t.Fatalf("v3 record: %v %v", rec, err)
	}
	if rec.Progress.Done != 700 || rec.Progress.Chunks == nil || rec.Progress.Chunks.Done != 3 || rec.Progress.Counters["skipped:hundred"] != 7 {
		t.Fatalf("typed progress = %+v chunks %+v", rec.Progress, rec.Progress.Chunks)
	}
	if l, _ := store.GetLedgerV3(id); l != nil {
		t.Fatal("the terminal transition must delete the ledger")
	}
	if err := r.ValidateCatalog(nil); err != nil {
		t.Fatalf("catalog: %v", err)
	}
}

func TestRegisterRefusesAnInvalidNativeDef(t *testing.T) {
	r := startV3Registry(t, newPebbleV3(t), Options{})
	def := ops.Task("test.no-perm", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Title:   "x",
		Effects: ops.Writes(ops.ResBooks),
		Run:     func(*ops.RC, ops.NoParams) (ops.NoResult, error) { return ops.NoResult{}, nil },
	})
	var ce *ops.CatalogError
	if err := Register(r, "test", def); !errors.As(err, &ce) || ce.Refusal != ops.RefuseNoPermission {
		t.Fatalf("err = %v, want the no-permission refusal", err)
	}
	if _, ok := r.Def("test.no-perm"); ok {
		t.Fatal("a refused def was registered")
	}
}

// TestScanKeySingleExecutor: a v2 def and a v3 def that share a key
// serialize against each other on the one executor, and registering the v3
// def adds no key namespace of its own.
func TestScanKeySingleExecutor(t *testing.T) {
	store := newPebbleV3(t)
	r := startV3Registry(t, store, Options{})
	const key = "test.shared-key"
	release := make(chan struct{})
	var v2Running, overlap atomic.Bool
	v2 := OperationDef{ID: "test.v2-scan", Plugin: "test", ConcurrencyKey: key, ResumePolicy: ResumeDrop,
		Liveness: LivenessManual, Run: func(ctx context.Context, _ json.RawMessage, _ Reporter) error {
			v2Running.Store(true)
			defer v2Running.Store(false)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		}}
	if err := r.RegisterOp(v2); err != nil {
		t.Fatal(err)
	}
	v3 := ops.Task("test.v3-scan", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Title:      "v3",
		Effects:    ops.ReadOnly(),
		Permission: auth.PermLibraryView,
		Exclusive:  key,
		Run: func(*ops.RC, ops.NoParams) (ops.NoResult, error) {
			if v2Running.Load() {
				overlap.Store(true)
			}
			return ops.NoResult{}, nil
		},
	})
	if err := Register(r, "test", v3); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, d := range r.ActiveDefs() {
		keys[d.ConcurrencyKey] = true
	}
	if len(keys) != 1 || !keys[key] {
		t.Fatalf("distinct keys = %v, want just %s", keys, key)
	}
	ctx := context.Background()
	id2, err := r.EnqueueOp(ctx, "test.v2-scan", nil)
	if err != nil {
		t.Fatal(err)
	}
	for !v2Running.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	id3, err := r.EnqueueOp(ctx, "test.v3-scan", nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if row, _ := store.GetOperationV2(id3); row.Status != "queued" {
		t.Fatalf("the v3 run is %s while the v2 run holds the shared key", row.Status)
	}
	close(release)
	awaitTerminal(t, store, id2)
	if row := awaitTerminal(t, store, id3); row.Status != "completed" || overlap.Load() {
		t.Fatalf("v3 run %s, overlap %v", row.Status, overlap.Load())
	}
}

// warmingPebble reports a startup warm-up in progress until released.
type warmingPebble struct {
	*database.PebbleStore
	warm atomic.Bool
}

func (w *warmingPebble) WarmupStatus() (ready, done bool, ms int64) {
	return w.warm.Load(), w.warm.Load(), 1
}

// TestReadinessWaitDoesNotTimeOut: a v3 run queued before the store is
// ready waits in the queue (ReadyStore), the wait does not count against its
// ProgressTimeout, and it runs once the store is ready. A ReadyNone def runs
// at once.
func TestReadinessWaitDoesNotTimeOut(t *testing.T) {
	ws := &warmingPebble{PebbleStore: newPebbleV3(t)}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	r := startV3Registry(t, ws, Options{WatchdogInterval: 10 * time.Millisecond,
		LivenessNow: func() time.Time { return time.Unix(0, clock.Load()) }})
	var started atomic.Bool
	waits := ops.Task("test.waits", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Title:      "waits",
		Effects:    ops.ReadOnly(ops.ResBooks),
		Permission: auth.PermLibraryView,
		Run: func(rc *ops.RC, _ ops.NoParams) (ops.NoResult, error) {
			started.Store(true)
			// Several watchdog cycles of silence: a wait that had counted
			// would trip ProgressTimeout here.
			time.Sleep(100 * time.Millisecond)
			return ops.NoResult{}, nil
		},
	})
	housekeeping := ops.Task("test.no-wait", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Title:      "no wait",
		Effects:    ops.ReadOnly().NeedsReady(ops.ReadyNone),
		Permission: auth.PermLibraryView,
		Run:        func(*ops.RC, ops.NoParams) (ops.NoResult, error) { return ops.NoResult{}, nil },
	})
	for _, d := range []ops.Definition{waits, housekeeping} {
		if err := Register(r, "test", d); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	id, err := r.EnqueueOp(ctx, "test.waits", nil)
	if err != nil {
		t.Fatal(err)
	}
	quick, err := r.EnqueueOp(ctx, "test.no-wait", nil)
	if err != nil {
		t.Fatal(err)
	}
	if row := awaitTerminal(t, ws, quick); row.Status != "completed" {
		t.Fatalf("a ReadyNone run must not wait: %s", row.Status)
	}
	time.Sleep(100 * time.Millisecond)
	if started.Load() {
		t.Fatal("a ReadyStore run started while the store was warming")
	}
	if row, _ := ws.GetOperationV2(id); row.Status != "queued" || row.ProgressMessage != WarmupStatusMessage {
		t.Fatalf("held run = %s %q", row.Status, row.ProgressMessage)
	}
	// Far past every progress timeout, still waiting.
	clock.Add(int64(2 * time.Hour))
	time.Sleep(50 * time.Millisecond)
	ws.warm.Store(true)
	row := awaitTerminal(t, ws, id)
	if row.Status != "completed" || !started.Load() {
		t.Fatalf("after warm-up: %s (%v)", row.Status, row.ErrorMessage)
	}
}

func TestPipelineOnRegistry(t *testing.T) {
	old := awaitPollInterval
	awaitPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { awaitPollInterval = old })
	store := newPebbleV3(t)
	r := startV3Registry(t, store, Options{})
	type count struct{ N int }
	first := ops.Task("test.p-first", ops.TaskSpec[ops.NoParams, count]{
		Title:      "first",
		Effects:    ops.ReadOnly(),
		Permission: auth.PermLibraryView,
		Run:        func(*ops.RC, ops.NoParams) (count, error) { return count{N: 4}, nil },
	})
	var gotN atomic.Int64
	second := ops.Task("test.p-second", ops.TaskSpec[count, ops.NoResult]{
		Title:      "second",
		Effects:    ops.ReadOnly(),
		Permission: auth.PermLibraryView,
		Run:        func(_ *ops.RC, p count) (ops.NoResult, error) { gotN.Store(int64(p.N)); return ops.NoResult{}, nil },
	})
	pipe := ops.Pipeline("test.pipe", ops.PipelineSpec[ops.NoParams]{
		Title:      "pipe",
		Permission: auth.PermLibraryView,
		Stages: []ops.Stage[ops.NoParams]{
			{Name: "first", Def: first},
			{Name: "second", Def: second, After: []string{"first"}, Params: func(_ ops.NoParams, res ops.Results) (any, error) {
				var c count
				err := res.Decode("first", &c)
				return count{N: c.N * 10}, err
			}},
		},
	})
	if err := RegisterBundle(r, ops.Bundle{Plugin: "test", Defs: []ops.Definition{first, second, pipe}}); err != nil {
		t.Fatal(err)
	}
	id, err := r.EnqueueOp(context.Background(), "test.pipe", nil)
	if err != nil {
		t.Fatal(err)
	}
	row := awaitTerminal(t, store, id)
	if row.Status != "completed" || gotN.Load() != 40 {
		t.Fatalf("pipeline %s (%v), second got %d", row.Status, row.ErrorMessage, gotN.Load())
	}
	var out ops.PipelineResult
	resultOf(t, row, &out)
	if out.Stages["second"].State != "completed" {
		t.Fatalf("stages = %+v", out.Stages)
	}
	child, _ := store.GetOperationV2(out.Stages["first"].RunID)
	if child == nil || child.ParentID == nil || *child.ParentID != id {
		t.Fatalf("a stage run must link to its pipeline: %+v", child)
	}
}

// TestBatchResumesThroughTheExecutor is the chunk-ledger resume end to end on
// the real executor: a graceful Shutdown interrupts a running v3 Batch after
// two of its four chunks are in the ledger; a new registry over the same
// store resumes the same op id, leases only the two unfinished chunks, and
// finishes with every item counted once.
func TestBatchResumesThroughTheExecutor(t *testing.T) {
	store := newPebbleV3(t)
	const size = 10
	mkDef := func(ran *counts, block bool) ops.Definition {
		return ops.Batch("test.resumable", ops.BatchSpec[ops.NoParams, int, ops.NoResult]{
			Title:      "resumable",
			Effects:    ops.ReadOnly(),
			Permission: auth.PermLibraryView,
			Source: func(*ops.RC, ops.NoParams) (ops.Source[int], error) {
				return ops.Source[int]{Items: seq(4 * size), Key: itemKey}, nil
			},
			Item: func(rc *ops.ItemRC, _ ops.NoParams, i int) error {
				ran.add(i)
				if block && i/size >= 2 {
					<-rc.Context().Done() // in flight at the shutdown
					return rc.Context().Err()
				}
				return nil
			},
			Concurrency: ops.Network(2, "two chunks finish, two are in flight"),
			ChunkSize:   size,
		})
	}

	first := newCounts()
	r1 := NewWithOptions(store, slog.Default(), 2, Options{WatchdogInterval: time.Hour, SweepInterval: time.Hour})
	if err := Register(r1, "test", mkDef(first, true)); err != nil {
		t.Fatal(err)
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	r1.Start(ctx1)
	opID, err := r1.EnqueueOp(ctx1, "test.resumable", nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		l, err := store.GetLedgerV3(opID)
		if err != nil {
			t.Fatal(err)
		}
		if l != nil && slices.Equal(bitsSet(*l), []int{0, 1}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chunks 0 and 1 never reached the ledger: %+v", l)
		}
		time.Sleep(20 * time.Millisecond)
	}
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	if err := r1.Shutdown(sctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	row, err := store.GetOperationV2(opID)
	if err != nil || row == nil || row.Status != "interrupted_quiesced" {
		t.Fatalf("after shutdown: %+v %v", row, err)
	}

	// The new process registers its defs before Start, as the server does:
	// the boot sweep needs the def to resume the run.
	second := newCounts()
	r2 := NewWithOptions(store, slog.Default(), 2, Options{WatchdogInterval: time.Hour, SweepInterval: time.Hour})
	if err := Register(r2, "test", mkDef(second, false)); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	r2.Start(ctx2)
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		if err := r2.Shutdown(sctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		cancel2()
	})
	row = awaitTerminal(t, store, opID)
	if row.Status != "completed" {
		t.Fatalf("resumed run: %s %v", row.Status, row.ErrorMessage)
	}
	for i := range 4 * size {
		if c := i / size; c < 2 && second.get(i) != 0 {
			t.Fatalf("item %d of chunk %d, complete before the shutdown, ran again", i, c)
		}
		if first.get(i)+second.get(i) < 1 {
			t.Fatalf("item %d never ran", i)
		}
	}
	rec, err := store.GetRunV3(opID)
	if err != nil || rec == nil || rec.Progress.Done != 4*size || rec.Progress.Chunks == nil || rec.Progress.Chunks.Done != 4 {
		t.Fatalf("resumed run progress: %+v %v", rec, err)
	}
}

// TestBatchResumesAfterACrash: the process dies mid-batch (no Shutdown: the
// row is left "running" with chunks 0 and 1 in the ledger). The next
// process's boot sweep (bootSweepV3) maps it to interrupted{crash}, the
// ResumeRestart policy re-queues the same op id (resumeRestart), and the
// runner leases only chunks 2 and 3.
func TestBatchResumesAfterACrash(t *testing.T) {
	store := newPebbleV3(t)
	const size = 10
	mkDef := func(ran *counts) ops.Definition {
		return ops.Batch("test.crashy", ops.BatchSpec[ops.NoParams, int, ops.NoResult]{
			Title:      "crashy",
			Effects:    ops.ReadOnly(),
			Permission: auth.PermLibraryView,
			Source: func(*ops.RC, ops.NoParams) (ops.Source[int], error) {
				return ops.Source[int]{Items: seq(4 * size), Key: itemKey}, nil
			},
			Item: func(rc *ops.ItemRC, _ ops.NoParams, i int) error {
				ran.add(i)
				if i/size >= 2 && rc.OpID() == "crashing" {
					<-rc.Context().Done()
					return rc.Context().Err()
				}
				return nil
			},
			Concurrency: ops.Network(2, "two chunks finish, two are in flight"),
			ChunkSize:   size,
		})
	}

	// The dead process: its run row says running, and its runner got
	// chunks 0 and 1 into the ledger before the world stopped.
	opID := "01M4VZZZZZZZZZZZZZZZZZZZZZ"
	now := time.Now().UTC()
	if err := store.InsertOperationV2(database.OperationV2Row{ID: opID, DefID: "test.crashy", Status: "queued", Priority: 1, QueuedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateOperationV2Status(opID, "running", &now, nil, nil); err != nil {
		t.Fatal(err)
	}
	first := newCounts()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc := ops.NewRC(ops.RunEnv{Context: ctx, OpID: "crashing", DefID: "test.crashy", Mode: ops.Live})
	job, err := ops.Compile(mkDef(first)).OpenBatch(rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runBatch(ctx, batchConfig{opID: opID, job: job, rc: rc, store: store, commitEvery: -1,
		afterCommit: func(l database.Ledger, _ database.ProgressSnapshot) bool {
			return slices.Equal(bitsSet(l), []int{0, 1})
		}})
	if !errors.Is(err, errWorldStopped) {
		t.Fatalf("dead process: %v", err)
	}
	if row, _ := store.GetOperationV2(opID); row == nil || row.Status != "running" {
		t.Fatalf("fixture: the row must still read running, got %+v", row)
	}

	second := newCounts()
	r := NewWithOptions(store, slog.Default(), 2, Options{WatchdogInterval: time.Hour, SweepInterval: time.Hour})
	if err := Register(r, "test", mkDef(second)); err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithCancel(context.Background())
	r.Start(rctx)
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		if err := r.Shutdown(sctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		rcancel()
	})
	row := awaitTerminal(t, store, opID)
	if row.Status != "completed" {
		t.Fatalf("resumed run: %s %v", row.Status, row.ErrorMessage)
	}
	for i := range 4 * size {
		if i/size < 2 && second.get(i) != 0 {
			t.Fatalf("item %d of a chunk the dead process recorded ran again", i)
		}
		if first.get(i)+second.get(i) < 1 {
			t.Fatalf("item %d never ran", i)
		}
	}
	rec, err := store.GetRunV3(opID)
	if err != nil || rec == nil || rec.Progress.Done != 4*size || rec.Attempt < 2 {
		t.Fatalf("resumed record: %+v %v", rec, err)
	}
}

// TestLeaseLossThroughWriterFailsRun drives a real stand-down lease loss:
// the run holds the stand-down and writes through its Writer; the lease
// lapses mid-run, the next write is refused and the registry revokes the
// fence. The run must end failed, with only the chunk that finished before
// the loss counted -- not "completed" with the rest counted as failures.
func TestLeaseLossThroughWriterFailsRun(t *testing.T) {
	store := newPebbleV3(t)
	var ids []string
	for i := range 20 {
		b, err := store.CreateBook(&database.Book{Title: fmt.Sprintf("book %02d", i), FilePath: fmt.Sprintf("/library/%02d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	r := startV3Registry(t, store, Options{ScanStandDownLease: time.Minute,
		ScanStandDownNow: func() time.Time { return time.Unix(0, clock.Load()) }})
	var once sync.Once
	var written atomic.Int64
	def := ops.Batch("test.lease-loss", ops.BatchSpec[ops.NoParams, string, ops.NoResult]{
		Title:      "lease",
		Effects:    ops.Writes(ops.ResBooks),
		Permission: auth.PermSettingsManage,
		Source: func(*ops.RC, ops.NoParams) (ops.Source[string], error) {
			return ops.Source[string]{Items: ids, Key: func(s string) string { return s }}, nil
		},
		Item: func(rc *ops.ItemRC, _ ops.NoParams, id string) error {
			var herr error
			once.Do(func() { _, herr = rc.StandDown() })
			if herr != nil {
				return herr
			}
			if written.Load() == 7 {
				clock.Add(int64(2 * time.Minute)) // the lease lapses
			}
			_, err := rc.Writer().ModifyBook(rc.Context(), id, func(b *database.Book) error { b.Title += " (x)"; return nil })
			if err == nil {
				written.Add(1)
			}
			return err
		},
		Concurrency: ops.Sequential("one stand-down holder writing in order"),
		ChunkSize:   5,
	})
	if err := Register(r, "test", def); err != nil {
		t.Fatal(err)
	}
	id, err := r.EnqueueOp(context.Background(), "test.lease-loss", map[string]bool{"dry_run": false})
	if err != nil {
		t.Fatal(err)
	}
	row := awaitTerminal(t, store, id)
	if row.Status == "completed" {
		t.Fatal("a run that lost its stand-down lease ended completed")
	}
	if written.Load() != 7 {
		t.Fatalf("%d writes landed, want the 7 before the loss", written.Load())
	}
	rec, err := store.GetRunV3(id)
	if err != nil || rec == nil || rec.Progress.Done != 5 || rec.Progress.Failed != 0 {
		t.Fatalf("progress after the loss: %+v %v", rec, err)
	}
}

// v2SurfaceStore hides every capability but the v2 operations surface: no
// write-ahead intents, no operation journal.
type v2SurfaceStore struct{ database.OpsV2Store }

// S4a: a Live run of a writing def refuses to start on a store that cannot
// back its Writer (no intents, no journal, no metadata history); a Preview
// run, which writes nothing, still runs. TestLiveWriterNeedsHistory covers a
// store that has everything but the history recorder.
func TestLiveWriterNeedsIntentsAndJournal(t *testing.T) {
	inner := newPebbleV3(t)
	store := v2SurfaceStore{inner}
	r := startV3Registry(t, store, Options{})
	var ran atomic.Int32
	def := ops.Task("test.writes", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Title:      "w",
		Effects:    ops.Writes(ops.ResBooks),
		Permission: auth.PermSettingsManage,
		Run:        func(*ops.RC, ops.NoParams) (ops.NoResult, error) { ran.Add(1); return ops.NoResult{}, nil },
	})
	if err := Register(r, "test", def); err != nil {
		t.Fatal(err)
	}
	live, err := r.EnqueueOp(context.Background(), "test.writes", map[string]bool{"dry_run": false})
	if err != nil {
		t.Fatal(err)
	}
	row := awaitTerminal(t, inner, live)
	if row.Status != "failed" || row.ErrorMessage == nil || ran.Load() != 0 {
		t.Fatalf("live run on a store without intents/journal: %s ran=%d", row.Status, ran.Load())
	}
	preview, err := r.EnqueueOp(context.Background(), "test.writes", nil)
	if err != nil {
		t.Fatal(err)
	}
	if row := awaitTerminal(t, inner, preview); row.Status != "completed" || ran.Load() != 1 {
		t.Fatalf("preview run: %s ran=%d", row.Status, ran.Load())
	}
}

// N4: a native registration that the registry refuses leaves no native
// entry behind, and a duplicate does not disturb the original's.
func TestRegisterFailureLeavesNoNativeEntry(t *testing.T) {
	r := startV3Registry(t, newPebbleV3(t), Options{})
	if err := r.RegisterOp(OperationDef{ID: "test.v2", Plugin: "test", ResumePolicy: ResumeDrop, Liveness: LivenessManual,
		Run: func(context.Context, json.RawMessage, Reporter) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	clash := ops.Task("test.native", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Title:      "n",
		Effects:    ops.ReadOnly(),
		Permission: auth.PermLibraryView,
		FormerIDs:  []string{"test.v2"},
		Run:        func(*ops.RC, ops.NoParams) (ops.NoResult, error) { return ops.NoResult{}, nil },
	})
	if err := Register(r, "test", clash); err == nil {
		t.Fatal("a former id naming a registered def was accepted")
	}
	r.mu.RLock()
	_, stale := r.v3defs["test.native"]
	r.mu.RUnlock()
	if stale {
		t.Fatal("a refused registration left a native catalog entry")
	}
	good := ops.Task("test.good", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Title:      "g",
		Effects:    ops.ReadOnly(),
		Permission: auth.PermLibraryView,
		Run:        func(*ops.RC, ops.NoParams) (ops.NoResult, error) { return ops.NoResult{}, nil },
	})
	if err := Register(r, "test", good); err != nil {
		t.Fatal(err)
	}
	if err := Register(r, "test", good); err == nil {
		t.Fatal("a duplicate registration was accepted")
	}
	kinds := map[string]ops.Kind{}
	for _, d := range r.Catalog() {
		kinds[d.ID()] = d.Kind()
	}
	if kinds["test.good"] != ops.KindTask || kinds["test.v2"] != ops.KindAdapted || len(kinds) != 2 {
		t.Fatalf("catalog = %v", kinds)
	}
}

// noHistoryStore is a v3-capable store without the metadata history
// recorder: RecordMetadataChange is shadowed by a field of the same name, so
// the method set no longer satisfies opswriter.HistoryRecorder.
type noHistoryStore struct {
	*database.PebbleStore
	RecordMetadataChange struct{}
}

// S4 (re-review): a Live writing run refuses to start when the store has
// intents and the journal but no metadata history recorder.
func TestLiveWriterNeedsHistory(t *testing.T) {
	inner := newPebbleV3(t)
	store := noHistoryStore{PebbleStore: inner}
	if _, ok := database.AsCapability[opswriter.HistoryRecorder](store); ok {
		t.Fatal("the fixture still has a history recorder")
	}
	if _, ok := database.AsCapability[opswriter.IntentStore](store); !ok {
		t.Fatal("the fixture lost its intents")
	}
	r := startV3Registry(t, store, Options{})
	var ran atomic.Int32
	def := ops.Task("test.writes", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Title:      "w",
		Effects:    ops.Writes(ops.ResBooks),
		Permission: auth.PermSettingsManage,
		Run:        func(*ops.RC, ops.NoParams) (ops.NoResult, error) { ran.Add(1); return ops.NoResult{}, nil },
	})
	if err := Register(r, "test", def); err != nil {
		t.Fatal(err)
	}
	live, err := r.EnqueueOp(context.Background(), "test.writes", map[string]bool{"dry_run": false})
	if err != nil {
		t.Fatal(err)
	}
	row := awaitTerminal(t, inner, live)
	if row.Status != "failed" || row.ErrorMessage == nil || ran.Load() != 0 {
		t.Fatalf("live run without a history recorder: %s ran=%d", row.Status, ran.Load())
	}
}

// TestStopDecidesFinishLikeTheRegistry: the batch runner leaves Finish to the
// resumed attempt exactly when the registry records the stop as a resumable
// interruption (stopResumes). A shutdown of a ResumeContinue batch skips
// Finish and the boot resume runs it once over all 40 items; a user cancel
// of the same def runs Finish in the stopped attempt and is never resumed.
func TestStopDecidesFinishLikeTheRegistry(t *testing.T) {
	const size, n = 10, 40
	type probe struct {
		block    atomic.Bool
		parked   atomic.Int32
		finished atomic.Int32
		lastDone atomic.Int64
		resumed  atomic.Bool // rc.Resumed() in the last Finish
	}
	cont := ops.ResumeContinue
	mkDef := func(p *probe) ops.Definition {
		return ops.Batch("test.stopper", ops.BatchSpec[ops.NoParams, int, ops.NoResult]{
			Title:      "stopper",
			Effects:    ops.ReadOnly(),
			Permission: auth.PermLibraryView,
			Resume:     &cont,
			Source: func(*ops.RC, ops.NoParams) (ops.Source[int], error) {
				return ops.Source[int]{Items: seq(n), Key: itemKey}, nil
			},
			Item: func(rc *ops.ItemRC, _ ops.NoParams, i int) error {
				if i >= 2*size && p.block.Load() {
					p.parked.Add(1)
					<-rc.Context().Done()
					return rc.Context().Err()
				}
				return nil
			},
			Finish: func(rc *ops.RC, _ ops.NoParams, s ops.Summary) (ops.NoResult, error) {
				p.finished.Add(1)
				p.lastDone.Store(s.Done)
				p.resumed.Store(rc.Resumed())
				return ops.NoResult{}, nil
			},
			Concurrency: ops.Network(2, "two chunks finish, two park"),
			ChunkSize:   size,
		})
	}
	start := func(t *testing.T, store *database.PebbleStore, p *probe) *Registry {
		r := NewWithOptions(store, slog.Default(), 2, Options{WatchdogInterval: time.Hour, SweepInterval: time.Hour})
		if err := Register(r, "test", mkDef(p)); err != nil {
			t.Fatal(err)
		}
		r.Start(context.Background())
		return r
	}
	awaitParked := func(t *testing.T, p *probe) {
		deadline := time.Now().Add(10 * time.Second)
		for p.parked.Load() < 2 {
			if time.Now().After(deadline) {
				t.Fatal("workers never parked")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	shutdown := func(t *testing.T, r *Registry) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	}

	t.Run("shutdown resumes and finishes once", func(t *testing.T) {
		store := newPebbleV3(t)
		p := &probe{}
		p.block.Store(true)
		r1 := start(t, store, p)
		opID, err := r1.EnqueueOp(context.Background(), "test.stopper", nil)
		if err != nil {
			t.Fatal(err)
		}
		awaitParked(t, p)
		shutdown(t, r1)
		if p.finished.Load() != 0 {
			t.Fatal("Finish ran in the attempt the shutdown stopped; the resume runs it again")
		}
		if row, _ := store.GetOperationV2(opID); row == nil || row.Status != "interrupted_quiesced" {
			t.Fatalf("after shutdown: %+v", row)
		}
		p.block.Store(false)
		r2 := start(t, store, p)
		defer shutdown(t, r2)
		if row := awaitTerminal(t, store, opID); row.Status != "completed" {
			t.Fatalf("resumed run: %s %v", row.Status, row.ErrorMessage)
		}
		if p.finished.Load() != 1 || p.lastDone.Load() != n {
			t.Fatalf("Finish ran %d times, last Done %d; want once over %d", p.finished.Load(), p.lastDone.Load(), n)
		}
		if !p.resumed.Load() {
			t.Fatal("the resumed attempt's rc.Resumed() is false")
		}
	})

	t.Run("user cancel finishes in the stopped attempt", func(t *testing.T) {
		store := newPebbleV3(t)
		p := &probe{}
		p.block.Store(true)
		r := start(t, store, p)
		defer shutdown(t, r)
		opID, err := r.EnqueueOp(context.Background(), "test.stopper", nil)
		if err != nil {
			t.Fatal(err)
		}
		awaitParked(t, p)
		if err := r.Cancel(opID); err != nil {
			t.Fatal(err)
		}
		if row := awaitTerminal(t, store, opID); row.Status != "canceled" {
			t.Fatalf("canceled run: %s", row.Status)
		}
		if p.finished.Load() != 1 {
			t.Fatalf("Finish ran %d times after a user cancel, want 1", p.finished.Load())
		}
		if p.resumed.Load() {
			t.Fatal("a first attempt's rc.Resumed() is true")
		}
	})
}
