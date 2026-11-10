// file: core/tasks/native_test.go
// version: 1.1.0
// guid: eb6000b7-aaf8-47bf-b775-57052084dc34
// last-edited: 2026-11-10

package tasks_test

import (
	"bytes"
	"context"
	"iter"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// harness is a store booted on an in-memory file system that holds the
// c/tasks/ keyspace, and NativeStores over it.
type harness struct {
	fs     *vfs.MemFS
	path   string
	ks     storage.Keyspace
	native tasks.Stores // NativeStores(ks) as it is
	stores tasks.Stores // the same, with Runs wrapped to add the Crasher
}

// bootHarness opens path on fs as the kernel's P2, P4a and P4b would, with
// core/tasks registered by taskstore.StorageSpec, and closes it at cleanup.
func bootHarness(t *testing.T, fs *vfs.MemFS, path string) *harness {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            path,
		Owners:          []storage.OwnerSpec{{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(fs),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, ok := db.Keyspace(taskstore.Owner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", taskstore.Owner)
	}
	h := &harness{fs: fs, path: path, ks: ks, native: tasks.NativeStores(ks)}
	h.stores = h.native
	h.stores.Runs = crashRuns{RunStore: h.native.Runs, h: h}
	return h
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return bootHarness(t, vfs.NewCrashableMem(), filepath.Join(t.TempDir(), "main.pebble"))
}

// crashRuns is NativeStores' RunStore plus taskstest.Crasher: a crash clone
// of the harness's file system, booted again.
type crashRuns struct {
	tasks.RunStore
	h *harness
}

func (c crashRuns) NewBatch() *storage.Batch { return c.RunStore.(taskstest.BatchOpener).NewBatch() }
func (c crashRuns) Importer() tasks.Importer {
	return c.RunStore.(taskstest.ImporterProvider).Importer()
}

func (c crashRuns) CrashClone(t *testing.T) tasks.Stores {
	return bootHarness(t, c.h.fs.CrashClone(vfs.CrashCloneCfg{}), c.h.path).stores
}

func gotham(id string) entity.Ref { return entity.Ref{Module: "gotham", Kind: "villains", ID: id} }

func runOf(id tasks.RunID, state tasks.RunState) tasks.RunRecord {
	created := time.Date(2026, time.March, 14, 21, 0, 0, 0, time.UTC)
	r := tasks.RunRecord{
		ID: id, Kind: "gotham.patrol", Mode: tasks.ModeLive, State: state, Created: created, Actor: tasks.Actor{UserID: "bruce"},
		Priority: tasks.PriorityHigh, Attempt: 1, FenceEpoch: 1,
	}
	if state.Terminal() {
		r.Finished = created.Add(time.Hour)
	}
	return r
}

// TestNativeStores_RoundTrip (05-tasks 5T.9, SIMP4-3): every RunStore,
// ClockStore, PauseStore and StandDownStore method over c/tasks/, each write
// landing at its 5T.6.1 key as a codec.JSON v1 value, then
// taskstest.StoresSuite over NativeStores on core's in-memory storage. The
// row's hello.count half needs hello to register its kind on a coretest App
// (F2-16's wiring), which adds that cell.
func TestNativeStores_RoundTrip(t *testing.T) {
	t.Run("KeyLayout", testKeyLayout)
	t.Run("Extensions", func(t *testing.T) {
		h := newHarness(t)
		r := h.native.Runs
		if _, ok := r.(taskstest.BatchOpener); !ok {
			t.Error("NativeStores' Runs is not a taskstest.BatchOpener")
		}
		if _, ok := r.(taskstest.ImporterProvider); !ok {
			t.Error("NativeStores' Runs is not a taskstest.ImporterProvider")
		}
		if _, ok := r.(taskstest.Crasher); ok {
			t.Error("NativeStores' Runs offers a crash clone; only the test harness may")
		}
	})
	t.Run("NativeImporterSharesTheKeyspace", func(t *testing.T) {
		h := newHarness(t)
		ctx := t.Context()
		imp := tasks.NativeImporter(h.ks)
		b := h.ks.NewIndexedBatch()
		defer func() { _ = b.Close() }()
		if err := imp.Run(ctx, b, runOf("01HEADIMPORTED", tasks.StateRunning)); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
		got, err := h.native.Runs.Get(ctx, "01HEADIMPORTED")
		if err != nil || got.State != tasks.StateInterrupted {
			t.Fatalf("run imported by NativeImporter, read by NativeStores: %+v %v", got, err)
		}
	})
	t.Run("RenameKindMovesTheDefinitionSnapshot", func(t *testing.T) {
		h := newHarness(t)
		ctx := t.Context()
		snapshot := []byte(`j{"v":1,"Title":"Patrol the East End"}`)
		if err := h.ks.Set(ctx, taskstore.DefKey("gotham.patrol"), snapshot); err != nil {
			t.Fatal(err)
		}
		imp := tasks.NativeImporter(h.ks)
		for range 2 { // the second pass finds nothing: idempotent
			b := h.ks.NewIndexedBatch()
			next, err := imp.RenameKind(ctx, b, "gotham.patrol", "gotham.night-patrol", "", 10)
			if err != nil || next != "" {
				t.Fatalf("RenameKind = %q, %v", next, err)
			}
			if err := b.Commit(ctx, storage.Sync); err != nil {
				t.Fatal(err)
			}
			_ = b.Close()
		}
		if ok, _ := h.ks.Has(ctx, taskstore.DefKey("gotham.patrol")); ok {
			t.Error("the old kind's definition snapshot remains")
		}
		if v, err := h.ks.Get(ctx, taskstore.DefKey("gotham.night-patrol")); err != nil || !bytes.Equal(v, snapshot) {
			t.Errorf("the new kind's definition snapshot = %q, %v", v, err)
		}
	})
	taskstest.StoresSuite(t, func(t *testing.T) tasks.Stores { return newHarness(t).stores })
}

// testKeyLayout drives each method once and checks the key it wrote.
func testKeyLayout(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	s := h.native
	if h.ks.Prefix() != "c/tasks/" {
		t.Fatalf("keyspace prefix %q", h.ks.Prefix())
	}
	has := func(what string, key []byte, want bool) {
		t.Helper()
		ok, err := h.ks.Has(ctx, key)
		if err != nil || ok != want {
			t.Errorf("%s: present=%v err=%v, want present=%v", what, ok, err, want)
		}
	}
	commit := func(stage func(b *storage.Batch) error) {
		t.Helper()
		b := h.ks.NewIndexedBatch()
		defer func() { _ = b.Close() }()
		if err := stage(b); err != nil {
			t.Fatalf("stage: %v", err)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	id, err := s.Runs.NewID(ctx)
	if err != nil || len(id) != 26 {
		t.Fatalf("NewID = %q, %v", id, err)
	}
	r := runOf(id, tasks.StateQueued)
	commit(func(b *storage.Batch) error { return s.Runs.Insert(ctx, b, r) })
	raw, err := h.ks.Get(ctx, taskstore.RunKey(string(id)))
	if err != nil || !bytes.HasPrefix(raw, []byte(`j{"v":1,`)) {
		t.Fatalf("run row = %q, %v; want a codec.JSON v1 value", raw, err)
	}
	has("queue entry", taskstore.QueueKey(uint8(tasks.PriorityHigh), r.Created, string(id)), true)
	has("kind index", taskstore.KindIndexKey("gotham.patrol", string(id)), true)
	has("state index", taskstore.StateIndexKey("queued", string(id)), true)
	if got, err := s.Runs.Get(ctx, id); err != nil || got.ID != id || got.Priority != tasks.PriorityHigh || got.Held {
		t.Fatalf("Get = %+v, %v", got, err)
	}

	running := r
	running.State, running.FenceEpoch, running.Started = tasks.StateRunning, 2, r.Created.Add(time.Minute)
	commit(func(b *storage.Batch) error {
		return s.Runs.Transition(ctx, b, id, tasks.Expect{State: tasks.StateQueued, Attempt: 1, FenceEpoch: 1}, running)
	})
	has("queue entry after the claim", taskstore.QueueKey(uint8(tasks.PriorityHigh), r.Created, string(id)), false)
	has("old state index", taskstore.StateIndexKey("queued", string(id)), false)
	has("new state index", taskstore.StateIndexKey("running", string(id)), true)
	commit(func(b *storage.Batch) error { return s.Runs.Progress(ctx, b, id, 3, 9, "Crime Alley") })

	commit(func(b *storage.Batch) error {
		if err := s.Runs.AppendEvent(ctx, b, id, 1, tasks.RunEvent{Event: "claim"}); err != nil {
			return err
		}
		if err := s.Runs.AppendIntent(ctx, b, id, 1, tasks.Write{Name: "gotham.lock_up"}, nil); err != nil {
			return err
		}
		if err := s.Runs.PutItem(ctx, b, id, "joker", tasks.ItemOutcome{Key: "joker", Outcome: "failed"}); err != nil {
			return err
		}
		if err := s.Runs.AppendLog(ctx, b, id, 1, tasks.LogLine{Message: "Bat-Signal lit"}); err != nil {
			return err
		}
		if err := s.Runs.SaveCheckpoint(ctx, b, id, "watermark", []byte("penguin")); err != nil {
			return err
		}
		if err := s.Runs.SaveChunk(ctx, b, id, tasks.Chunk{N: 2}); err != nil {
			return err
		}
		if err := s.Runs.MarkRow(ctx, b, id, "plan:joker"); err != nil {
			return err
		}
		if err := s.Runs.Strike(ctx, b, id, tasks.Strike{Kind: "stuck"}); err != nil {
			return err
		}
		if err := s.Runs.Debounce(ctx, b, tasks.DebounceBucket{Kind: "gotham.patrol", Bucket: "east-end", First: r.Created, Last: r.Created}); err != nil {
			return err
		}
		if err := s.Runs.Stamp(ctx, b, gotham("joker"), "gotham.patrol", r.Created); err != nil {
			return err
		}
		if err := s.Clocks.Advance(ctx, b, "gotham.patrol", tasks.Clock{}, false, tasks.Clock{LastRun: r.Created}); err != nil {
			return err
		}
		if err := s.Pause.Set(ctx, b, &tasks.Pause{Reason: "Bat-Signal maintenance"}); err != nil {
			return err
		}
		return s.StandDown.Set(ctx, b, "gotham.blackout", &tasks.StandDown{Holder: id})
	})
	if err := s.Runs.ReplaceRows(ctx, id, "plan:", 1, func(int) (tasks.Row, error) { return tasks.Row{ID: "plan:joker"}, nil }); err != nil {
		t.Fatal(err)
	}
	for what, key := range map[string][]byte{
		"event page":  taskstore.PageKey(string(id), taskstore.PageEvents, taskstore.SeqSeg(1)),
		"intent page": taskstore.PageKey(string(id), taskstore.PageIntents, taskstore.SeqSeg(1)),
		"item page":   taskstore.PageKey(string(id), taskstore.PageItems, keys.Str("joker")),
		"log page":    taskstore.PageKey(string(id), taskstore.PageLog, taskstore.SeqSeg(1)),
		"checkpoint":  taskstore.CheckpointKey(string(id), "watermark"),
		"chunk":       taskstore.ChunkKey(string(id), 2),
		"row":         taskstore.RowKey(string(id), "plan:joker"),
		"mark":        taskstore.MarkKey(string(id), "plan:joker"),
		"bucket":      taskstore.DebounceKey("gotham.patrol", "east-end"),
		"stamp":       taskstore.ReqKey(taskstore.Subject{Module: "gotham", Kind: "villains", ID: "joker"}, "gotham.patrol"),
		"stamp rev":   taskstore.ReqRevKey("gotham.patrol", taskstore.Subject{Module: "gotham", Kind: "villains", ID: "joker"}),
		"clock":       taskstore.SchedKey("gotham.patrol"),
		"pause":       taskstore.PauseKey(),
		"stand-down":  taskstore.StandDownKey("gotham.blackout"),
	} {
		has(what, key, true)
	}
	strikes := 0
	for _, err := range h.ks.ScanPrefix(ctx, taskstore.StrikePrefix(string(id))) {
		if err != nil {
			t.Fatal(err)
		}
		strikes++
	}
	if strikes != 1 {
		t.Errorf("strike keys = %d, want 1", strikes)
	}
	if got := collectAll(t, s.Runs.Unfinished(ctx)); len(got) != 1 {
		t.Errorf("Unfinished = %d runs", len(got))
	}
	if c, err := s.Runs.Counts(ctx); err != nil || c[tasks.StateRunning] != 1 {
		t.Errorf("Counts = %v, %v", c, err)
	}

	done := running
	done.State, done.Finished = tasks.StateSucceeded, r.Created.Add(time.Hour)
	commit(func(b *storage.Batch) error {
		return s.Runs.Transition(ctx, b, id, tasks.Expect{State: tasks.StateRunning, Attempt: 1, FenceEpoch: 2}, done)
	})
	has("done index", taskstore.DoneIndexKey(done.Finished, string(id)), true)
	commit(func(b *storage.Batch) error { return s.Runs.Delete(ctx, b, id) })
	var left []string
	for kv, err := range h.ks.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(kv.Key, []byte(id)) {
			left = append(left, string(kv.Key))
		}
	}
	// The stamp, its reverse index and the stand-down marker are keyed by
	// subject and hold key, not by run: every key the run owns is gone.
	for _, k := range left {
		t.Errorf("a key of the deleted run remains: %q", k)
	}

	old := runOf("patrol-old", tasks.StateFailed)
	commit(func(b *storage.Batch) error { return s.Runs.Insert(ctx, b, old) })
	if n, err := s.Runs.Prune(ctx, old.Finished.Add(time.Second), 10); err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	has("pruned run", taskstore.RunKey("patrol-old"), false)
	has("pruned done index", taskstore.DoneIndexKey(old.Finished, "patrol-old"), false)
}

func collectAll[T any](t *testing.T, seq iter.Seq2[T, error]) []T {
	t.Helper()
	var out []T
	for v, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// TestPerRunSeq_NoReissueAfterCrash (DATA-19): the per-run Seq counters are
// advanced in the batch that appends the page entry, so a crash between the
// row write and the commit loses both, and a crash after the commit keeps
// both: no boot ever reissues a seq that names a stored entry.
func TestPerRunSeq_NoReissueAfterCrash(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	id := tasks.RunID("case-seq-crash")
	r := runOf(id, tasks.StateRunning)
	open := func(s tasks.Stores) *storage.Batch { return s.Runs.(taskstest.BatchOpener).NewBatch() }
	commit := func(s tasks.Stores, stage func(b *storage.Batch) error) error {
		b := open(s)
		defer func() { _ = b.Close() }()
		if err := stage(b); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	}
	// appendEvent is the fenced owner's build: it reads the row, advances
	// Seq.Event and appends the event at the new seq, in one batch.
	appendEvent := func(s tasks.Stores, b *storage.Batch, what string) (uint64, error) {
		cur, err := s.Runs.Get(ctx, id)
		if err != nil {
			return 0, err
		}
		next := cur
		next.Seq.Event++
		if err := s.Runs.Transition(ctx, b, id, tasks.Expect{State: cur.State, Attempt: cur.Attempt, FenceEpoch: cur.FenceEpoch}, next); err != nil {
			return 0, err
		}
		return next.Seq.Event, s.Runs.AppendEvent(ctx, b, id, next.Seq.Event, tasks.RunEvent{Event: what})
	}
	events := func(s tasks.Stores) map[uint64]string {
		out := map[uint64]string{}
		for e, err := range s.Runs.Pages(ctx, id, tasks.PageQuery{Page: tasks.PageEvents}) {
			if err != nil {
				t.Fatal(err)
			}
			out[e.Seq] = e.Event.Event
		}
		return out
	}

	if err := commit(h.stores, func(b *storage.Batch) error { return h.stores.Runs.Insert(ctx, b, r) }); err != nil {
		t.Fatal(err)
	}
	if err := commit(h.stores, func(b *storage.Batch) error { _, err := appendEvent(h.stores, b, "claim"); return err }); err != nil {
		t.Fatal(err)
	}

	// Crash between the row write and the commit: the staged seq 2 is lost
	// with its event.
	b := open(h.stores)
	seq, err := appendEvent(h.stores, b, "lost-on-crash")
	if err != nil || seq != 2 {
		t.Fatalf("staged seq %d, %v", seq, err)
	}
	crashed := h.stores.Runs.(taskstest.Crasher).CrashClone(t)
	_ = b.Close()
	if got, err := crashed.Runs.Get(ctx, id); err != nil || got.Seq.Event != 1 {
		t.Fatalf("after a crash before the commit: Seq %+v, %v; want Event 1", got.Seq, err)
	}
	if ev := events(crashed); len(ev) != 1 || ev[1] != "claim" {
		t.Fatalf("events after the crash = %v", ev)
	}
	// The next boot's owner takes seq 2 for a new entry: no stored entry
	// carried it, so nothing is reissued.
	if err := commit(crashed, func(b *storage.Batch) error { _, err := appendEvent(crashed, b, "resume"); return err }); err != nil {
		t.Fatal(err)
	}
	// Crash after the commit: the row and the entry survive together, and
	// the next seq is 3.
	again := crashed.Runs.(taskstest.Crasher).CrashClone(t)
	if got, err := again.Runs.Get(ctx, id); err != nil || got.Seq.Event != 2 {
		t.Fatalf("after a crash past the commit: Seq %+v, %v; want Event 2", got.Seq, err)
	}
	if ev := events(again); len(ev) != 2 || ev[2] != "resume" {
		t.Fatalf("events after the second crash = %v", ev)
	}
	b2 := open(again)
	defer func() { _ = b2.Close() }()
	if seq, err := appendEvent(again, b2, "complete"); err != nil || seq != 3 {
		t.Fatalf("next seq after the crash = %d, %v; want 3", seq, err)
	}
}
