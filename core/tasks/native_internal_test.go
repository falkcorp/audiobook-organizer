// file: core/tasks/native_internal_test.go
// version: 1.1.1
// guid: 077fc32b-be7d-4ba9-9c02-c2ee627bcf57
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/storage"
)

// nativeEnv is NativeStores' pieces over a fixture store's c/tasks/ keyspace.
type nativeEnv struct {
	ks   storage.Keyspace
	runs *nativeRuns
	imp  *nativeImporter
}

func newNativeEnv(t *testing.T) nativeEnv {
	t.Helper()
	ks := openTasksKeyspace(t)
	s := NativeStores(ks)
	runs := s.Runs.(*nativeRuns)
	return nativeEnv{ks: ks, runs: runs, imp: runs.Importer().(*nativeImporter)}
}

// openTasksKeyspace boots an in-memory store as the kernel's P2, P4a and P4b
// do (storage.Open, Prepare, Migrate) with c/tasks/ registered by
// taskstore.StorageSpec, returns that keyspace and closes the store at
// cleanup. It opens storage itself instead of calling coretest.NewStore:
// coretest imports app, app imports kernel and kernel's imports reach this
// package (through internal/storagemigrate), so an in-package test that
// imports coretest is an import cycle.
func openTasksKeyspace(t *testing.T) storage.Keyspace {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join(t.TempDir(), "main.pebble"),
		Owners:          []storage.OwnerSpec{{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("boot: %v", errors.Join(err, o.Close()))
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ks, ok := db.Keyspace(taskstore.Owner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", taskstore.Owner)
	}
	return ks
}

func (e nativeEnv) commit(t *testing.T, stage func(b *storage.Batch) error) error {
	t.Helper()
	b := e.ks.NewIndexedBatch()
	defer func() { _ = b.Close() }()
	if err := stage(b); err != nil {
		return err
	}
	return b.Commit(t.Context(), storage.Sync)
}

func batRun(id RunID, state RunState) RunRecord {
	r := RunRecord{ID: id, Kind: "gotham.patrol", State: state, Created: time.Date(2026, 3, 14, 21, 0, 0, 0, time.UTC), Attempt: 1, FenceEpoch: 1}
	if state.Terminal() {
		r.Finished = r.Created.Add(time.Hour)
	}
	return r
}

// TestPrune_WalksPastWhatItCannotDelete: done-index entries Prune cannot
// act on never stall it (DATA8-8). Seventy undecodable rows sit ahead of the
// prunable runs, more than one chunk; an orphan entry (no row) and a stale
// one (the row is queued) are removed; limit bounds the runs deleted, and a
// later call picks up where the walk can act again.
func TestPrune_WalksPastWhatItCannotDelete(t *testing.T) {
	e := newNativeEnv(t)
	ctx := t.Context()
	cutoff := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	early := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	late := batRun("late", StateSucceeded)
	late.Finished = cutoff.Add(time.Hour)
	oldB := batRun("old-b", StateCanceled)
	oldB.Finished = oldB.Finished.Add(30 * time.Minute)
	for _, r := range []RunRecord{batRun("old-a", StateCanceled), oldB, batRun("requeued", StateQueued), late} {
		if err := e.commit(t, func(b *storage.Batch) error { return e.runs.Insert(ctx, b, r) }); err != nil {
			t.Fatal(err)
		}
	}
	const corrupt = 70
	b := e.ks.NewBatch()
	for i := range corrupt {
		id := fmt.Sprintf("corrupt-%02d", i)
		if err := errors.Join(
			b.Expect(e.ks, taskstore.RunKey(id), nil),
			b.Set(taskstore.RunKey(id), []byte("not a codec value")),
			b.Set(taskstore.DoneIndexKey(early.Add(time.Duration(i)*time.Minute), id), nil),
		); err != nil {
			t.Fatal(err)
		}
	}
	orphan := taskstore.DoneIndexKey(early.Add(24*time.Hour), "ghost")
	stale := taskstore.DoneIndexKey(early.Add(24*time.Hour), "requeued")
	if err := errors.Join(b.Set(orphan, nil), b.Set(stale, nil), b.Commit(ctx, storage.Sync), b.Close()); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1, 1, 0} {
		n, err := e.runs.Prune(ctx, cutoff, 1)
		if err != nil || n != want {
			t.Fatalf("Prune call %d = %d, %v; want %d", i+1, n, err, want)
		}
	}
	for id, want := range map[RunID]bool{"old-a": false, "old-b": false, "requeued": true, "late": true} {
		if _, err := e.runs.Get(ctx, id); (err == nil) != want {
			t.Errorf("run %s present=%v, want %v", id, err == nil, want)
		}
	}
	for _, k := range [][]byte{orphan, stale} {
		if ok, err := e.ks.Has(ctx, k); err != nil || ok {
			t.Errorf("done entry %q: present=%v err=%v, want removed", k, ok, err)
		}
	}
	if es, err := e.runs.st.DoneBefore(ctx, cutoff, nil, 0); err != nil || len(es) != corrupt {
		t.Errorf("done entries left = %d, %v; want the %d undecodable rows'", len(es), err, corrupt)
	}
	// A listing hides each undecodable row behind an error and goes on.
	var listed, failed int
	for _, err := range e.runs.List(ctx, RunQuery{}) {
		if err != nil {
			failed++
			continue
		}
		listed++
	}
	if listed != 2 || failed != corrupt {
		t.Errorf("List over corrupt rows: %d rows, %d errors; want 2 and %d", listed, failed, corrupt)
	}
}

// TestPlainBatchRefused: every method that reads before it writes refuses a
// batch that is not indexed, so no second write of a run in one build can
// read past the first and lose it (ErrBatchNotIndexed).
func TestPlainBatchRefused(t *testing.T) {
	e := newNativeEnv(t)
	ctx := t.Context()
	run := batRun("patrol-1", StateQueued)
	if err := e.commit(t, func(b *storage.Batch) error { return e.runs.Insert(ctx, b, run) }); err != nil {
		t.Fatal(err)
	}
	running := run
	running.State = StateRunning
	clocks := NativeStores(e.ks).Clocks
	for name, stage := range map[string]func(b *storage.Batch) error{
		"Insert": func(b *storage.Batch) error { return e.runs.Insert(ctx, b, batRun("patrol-2", StateQueued)) },
		"Transition": func(b *storage.Batch) error {
			return e.runs.Transition(ctx, b, run.ID, Expect{StateQueued, 1, 1}, running)
		},
		"Progress": func(b *storage.Batch) error { return e.runs.Progress(ctx, b, run.ID, 1, 2, "Ace Chemicals") },
		"Debounce": func(b *storage.Batch) error {
			return e.runs.Debounce(ctx, b, DebounceBucket{Kind: run.Kind, First: run.Created, Last: run.Created})
		},
		"Clock":        func(b *storage.Batch) error { return clocks.Advance(ctx, b, run.Kind, Clock{}, false, Clock{}) },
		"Importer.Run": func(b *storage.Batch) error { return e.imp.Run(ctx, b, batRun("arkham-1", StateQueued)) },
		"RunData":      func(b *storage.Batch) error { return e.imp.RunData(ctx, b, run.ID, RunData{}) },
		"RenameKind": func(b *storage.Batch) error {
			_, err := e.imp.RenameKind(ctx, b, run.Kind, "gotham.night-patrol", "", 10)
			return err
		},
	} {
		b := e.ks.NewBatch()
		err := stage(b)
		if cerr := b.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		if !errors.Is(err, ErrBatchNotIndexed) {
			t.Errorf("%s on a plain batch: %v, want ErrBatchNotIndexed", name, err)
		}
	}
	if got, err := e.runs.Get(ctx, run.ID); err != nil || got.State != StateQueued {
		t.Fatalf("run after the refusals: %+v, %v", got, err)
	}
}

// TestTerminalWithoutFinishRefused: a terminal record must carry its finish
// time, through every path that stages a run row (else the done index puts
// it before every cutoff and Prune deletes it at once).
func TestTerminalWithoutFinishRefused(t *testing.T) {
	e := newNativeEnv(t)
	ctx := t.Context()
	run := batRun("patrol-1", StateRunning)
	if err := e.commit(t, func(b *storage.Batch) error { return e.runs.Insert(ctx, b, run) }); err != nil {
		t.Fatal(err)
	}
	unfinished := func(id RunID, s RunState) RunRecord {
		r := batRun(id, s)
		r.Finished = time.Time{}
		return r
	}
	for name, stage := range map[string]func(b *storage.Batch) error{
		"Insert": func(b *storage.Batch) error { return e.runs.Insert(ctx, b, unfinished("patrol-2", StateSucceeded)) },
		"Transition": func(b *storage.Batch) error {
			return e.runs.Transition(ctx, b, run.ID, Expect{StateRunning, 1, 1}, unfinished(run.ID, StateFailed))
		},
		"Importer.Run": func(b *storage.Batch) error { return e.imp.Run(ctx, b, unfinished("arkham-1", StateCanceled)) },
	} {
		if err := e.commit(t, stage); err == nil || !strings.Contains(err.Error(), "without a finish time") {
			t.Errorf("%s of a terminal run without Finished: %v", name, err)
		}
	}
	if n, err := e.runs.Prune(ctx, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), 10); err != nil || n != 0 {
		t.Fatalf("Prune = %d, %v; nothing may be prunable", n, err)
	}
}

// TestNativeRefusals: the validation and refusal paths of every staging
// method.
func TestNativeRefusals(t *testing.T) {
	e := newNativeEnv(t)
	ctx := t.Context()
	run := batRun("patrol-1", StateRunning)
	if err := e.commit(t, func(b *storage.Batch) error { return e.runs.Insert(ctx, b, run) }); err != nil {
		t.Fatal(err)
	}
	refused := map[string]func(b *storage.Batch) error{
		"Insert without an ID":  func(b *storage.Batch) error { return e.runs.Insert(ctx, b, RunRecord{}) },
		"Insert without a kind": func(b *storage.Batch) error { return e.runs.Insert(ctx, b, RunRecord{ID: "x"}) },
		"Insert in a bad state": func(b *storage.Batch) error { return e.runs.Insert(ctx, b, batRun("x", "held")) },
		"Transition to other ID": func(b *storage.Batch) error {
			return e.runs.Transition(ctx, b, run.ID, Expect{StateRunning, 1, 1}, batRun("other", StateFailed))
		},
		"Transition to other kind": func(b *storage.Batch) error {
			n := run
			n.Kind = "gotham.case-file"
			return e.runs.Transition(ctx, b, run.ID, Expect{StateRunning, 1, 1}, n)
		},
		"PutItem without a key":        func(b *storage.Batch) error { return e.runs.PutItem(ctx, b, run.ID, "", ItemOutcome{}) },
		"SaveCheckpoint without name":  func(b *storage.Batch) error { return e.runs.SaveCheckpoint(ctx, b, run.ID, "", nil) },
		"SaveChunk with negative N":    func(b *storage.Batch) error { return e.runs.SaveChunk(ctx, b, run.ID, Chunk{N: -1}) },
		"MarkRow without a row ID":     func(b *storage.Batch) error { return e.runs.MarkRow(ctx, b, run.ID, "") },
		"AppendEvent of a missing run": func(b *storage.Batch) error { return e.runs.AppendEvent(ctx, b, "nobody", 1, RunEvent{}) },
		"AppendIntent of missing run":  func(b *storage.Batch) error { return e.runs.AppendIntent(ctx, b, "nobody", 1, Write{}, nil) },
		"PutItem of a missing run":     func(b *storage.Batch) error { return e.runs.PutItem(ctx, b, "nobody", "k", ItemOutcome{}) },
		"AppendLog of a missing run":   func(b *storage.Batch) error { return e.runs.AppendLog(ctx, b, "nobody", 1, LogLine{}) },
		"Strike of a missing run":      func(b *storage.Batch) error { return e.runs.Strike(ctx, b, "nobody", Strike{}) },
		"SaveChunk of a missing run":   func(b *storage.Batch) error { return e.runs.SaveChunk(ctx, b, "nobody", Chunk{}) },
		"Delete of a missing run":      func(b *storage.Batch) error { return e.runs.Delete(ctx, b, "nobody") },
		"Clock advance without kind": func(b *storage.Batch) error {
			return NativeStores(e.ks).Clocks.Advance(ctx, b, "", Clock{}, false, Clock{})
		},
		"Importer.Clock without kind": func(b *storage.Batch) error { return e.imp.Clock(ctx, b, "", Clock{}) },
		"Importer.Debounce, no First": func(b *storage.Batch) error { return e.imp.Debounce(ctx, b, DebounceBucket{Kind: "gotham.patrol"}) },
		"Importer.StandDown, no key":  func(b *storage.Batch) error { return e.imp.StandDown(ctx, b, StandDown{}) },
		"Importer.Stamp, no subject":  func(b *storage.Batch) error { return e.imp.Stamp(ctx, b, entity.Ref{}, "gotham.patrol", time.Time{}) },
		"RunData, unknown page": func(b *storage.Batch) error {
			return e.imp.RunData(ctx, b, run.ID, RunData{Pages: []PageEntry{{Page: 9}}})
		},
		"RunData, item without key": func(b *storage.Batch) error {
			return e.imp.RunData(ctx, b, run.ID, RunData{Pages: []PageEntry{{Page: PageItems}}})
		},
		"RunData, bad checkpoint": func(b *storage.Batch) error {
			return e.imp.RunData(ctx, b, run.ID, RunData{Checkpoints: map[string][]byte{"": nil}})
		},
		"RunData, bad chunk": func(b *storage.Batch) error {
			return e.imp.RunData(ctx, b, run.ID, RunData{Chunks: []Chunk{{N: -2}}})
		},
		"RunData, bad row":  func(b *storage.Batch) error { return e.imp.RunData(ctx, b, run.ID, RunData{Rows: []Row{{}}}) },
		"RunData, bad mark": func(b *storage.Batch) error { return e.imp.RunData(ctx, b, run.ID, RunData{Marks: []string{""}}) },
		"RunData, nil batch": func(*storage.Batch) error {
			return e.imp.RunData(ctx, nil, run.ID, RunData{})
		},
		"Insert, nil batch": func(*storage.Batch) error { return e.runs.Insert(ctx, nil, batRun("y", StateQueued)) },
		"Pause.Set, nil batch": func(*storage.Batch) error {
			return NativeStores(e.ks).Pause.Set(ctx, nil, &Pause{})
		},
	}
	for name, stage := range refused {
		if err := e.commit(t, stage); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	for _, name := range []string{"AppendEvent of a missing run", "Strike of a missing run", "Delete of a missing run"} {
		if err := e.commit(t, refused[name]); !errors.Is(err, ErrRunNotFound) {
			t.Errorf("%s: %v, want ErrRunNotFound", name, err)
		}
	}
	// A cancelled context fails every read.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for name, err := range map[string]error{
		"Get":    func() error { _, err := e.runs.Get(cancelled, run.ID); return err }(),
		"Counts": func() error { _, err := e.runs.Counts(cancelled); return err }(),
		"Prune":  func() error { _, err := e.runs.Prune(cancelled, time.Now(), 1); return err }(),
		"Stamped": func() error {
			_, _, err := e.runs.Stamped(cancelled, entity.Ref{Module: "gotham", Kind: "villains"}, "gotham.patrol")
			return err
		}(),
		"Clock.Get": func() error { _, _, err := NativeStores(e.ks).Clocks.Get(cancelled, "gotham.patrol"); return err }(),
		"Pause.Get": func() error { _, _, err := NativeStores(e.ks).Pause.Get(cancelled); return err }(),
		"Checkpoint": func() error {
			_, _, err := e.runs.Checkpoint(cancelled, run.ID, "watermark")
			return err
		}(),
	} {
		if err == nil {
			t.Errorf("%s under a cancelled context: no error", name)
		}
	}
	for name, seq := range map[string]func(func(error) bool){
		"Queued": func(y func(error) bool) {
			e.runs.Queued(cancelled)(func(_ RunRecord, err error) bool { return y(err) })
		},
		"Unfinished": func(y func(error) bool) {
			e.runs.Unfinished(cancelled)(func(_ RunRecord, err error) bool { return y(err) })
		},
		"List": func(y func(error) bool) {
			e.runs.List(cancelled, RunQuery{})(func(_ RunRecord, err error) bool { return y(err) })
		},
		"ListKinds": func(y func(error) bool) {
			e.runs.List(cancelled, RunQuery{Kinds: []Kind{"gotham.patrol"}})(func(_ RunRecord, err error) bool { return y(err) })
		},
		"Chunks": func(y func(error) bool) {
			e.runs.Chunks(cancelled, run.ID)(func(_ Chunk, err error) bool { return y(err) })
		},
		"Pages": func(y func(error) bool) {
			e.runs.Pages(cancelled, run.ID, PageQuery{Page: PageLog})(func(_ PageEntry, err error) bool { return y(err) })
		},
	} {
		got := false
		seq(func(err error) bool { got = got || err != nil; return true })
		if !got {
			t.Errorf("%s under a cancelled context yielded no error", name)
		}
	}
}

// TestRenameKind_EdgeCases: stale and dangling kind-index entries, a stamp,
// a bucket and an idempotency record that the new kind already holds, and a
// clock both kinds hold.
func TestRenameKind_EdgeCases(t *testing.T) {
	e := newNativeEnv(t)
	ctx := t.Context()
	const from, to Kind = "gotham.patrol", "gotham.night-patrol"
	joker := entity.Ref{Module: "gotham", Kind: "villains", ID: "joker"}
	early := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	later := early.Add(24 * time.Hour)
	if err := e.commit(t, func(b *storage.Batch) error {
		if err := e.runs.Insert(ctx, b, batRun("patrol-1", StateQueued)); err != nil {
			return err
		}
		if err := e.runs.Stamp(ctx, b, joker, from, early); err != nil {
			return err
		}
		if err := e.runs.Stamp(ctx, b, joker, to, later); err != nil {
			return err
		}
		for _, k := range []Kind{from, to} {
			if err := NativeStores(e.ks).Clocks.Advance(ctx, b, k, Clock{}, false, Clock{ScheduleHash: string(k)}); err != nil {
				return err
			}
		}
		// In bucket "east" from's bucket arrived last, in "west" to's did:
		// the later bucket's parameters win the merge either way.
		for _, d := range []DebounceBucket{
			{Kind: from, Bucket: "east", Last: later, Params: json.RawMessage(`{"district":"east-end"}`)},
			{Kind: to, Bucket: "east", Last: early, Params: json.RawMessage(`{"district":"the-narrows"}`)},
			{Kind: from, Bucket: "west", Last: early, Params: json.RawMessage(`{"district":"crime-alley"}`)},
			{Kind: to, Bucket: "west", Last: later, Params: json.RawMessage(`{"district":"diamond-district"}`)},
		} {
			d.First = early
			d.Subjects = []entity.Ref{{Module: "gotham", Kind: "villains", ID: string(d.Kind)}}
			if err := e.runs.Debounce(ctx, b, d); err != nil {
				return err
			}
		}
		// Idempotency records: one only from holds, one both hold.
		for k, v := range map[string]string{
			string(taskstore.IdemKey(string(from), "bat-signal")): "run-cave",
			string(taskstore.IdemKey(string(from), "batarang")):   "run-old",
			string(taskstore.IdemKey(string(to), "batarang")):     "run-new",
		} {
			if err := errors.Join(b.Expect(e.ks, []byte(k), nil), b.Set([]byte(k), []byte(v))); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A dangling index entry (no row) and a stale one (the row names another
	// kind) under from.
	for _, k := range [][]byte{taskstore.KindIndexKey(string(from), "ghost"), taskstore.KindIndexKey(string(from), "stale")} {
		if err := e.ks.Set(ctx, k, nil); err != nil {
			t.Fatal(err)
		}
	}
	stale := batRun("stale", StateQueued)
	stale.Kind = "gotham.case-file"
	if err := e.commit(t, func(b *storage.Batch) error { return e.runs.Insert(ctx, b, stale) }); err != nil {
		t.Fatal(err)
	}
	if err := e.commit(t, func(b *storage.Batch) error {
		next, err := e.imp.RenameKind(ctx, b, from, to, "", 100)
		if err == nil && next != "" {
			t.Errorf("RenameKind left cursor %q", next)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, k := range [][]byte{taskstore.KindIndexKey(string(from), "ghost"), taskstore.KindIndexKey(string(from), "stale"), taskstore.SchedKey(string(from))} {
		if ok, _ := e.ks.Has(ctx, k); ok {
			t.Errorf("%q survived the rename", k)
		}
	}
	if got, _ := e.runs.Get(ctx, "stale"); got.Kind != "gotham.case-file" {
		t.Errorf("the stale entry's run was renamed to %s", got.Kind)
	}
	if c, ok, _ := NativeStores(e.ks).Clocks.Get(ctx, to); !ok || c.ScheduleHash != string(to) {
		t.Errorf("the new kind's own clock was overwritten: %+v", c)
	}
	if at, ok, _ := e.runs.Stamped(ctx, joker, to); !ok || !at.Equal(later) {
		t.Errorf("stamp kept %v, want the later %v", at, later)
	}
	var buckets []DebounceBucket
	for d, err := range e.runs.DebounceBuckets(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		buckets = append(buckets, d)
	}
	wantParams := map[string]string{"east": `{"district":"east-end"}`, "west": `{"district":"diamond-district"}`}
	if len(buckets) != 2 {
		t.Errorf("buckets after the rename = %+v, want two merged buckets of %s", buckets, to)
	}
	for _, d := range buckets {
		if d.Kind != to || len(d.Subjects) != 2 || string(d.Params) != wantParams[d.Bucket] || !d.Last.Equal(later) {
			t.Errorf("bucket %s after the rename = %+v, want a merged bucket of %s with params %s", d.Bucket, d, to, wantParams[d.Bucket])
		}
	}
	for k, want := range map[string]string{"bat-signal": "run-cave", "batarang": "run-new"} {
		v, err := e.ks.Get(ctx, taskstore.IdemKey(string(to), k))
		if err != nil || string(v) != want {
			t.Errorf("idem %s of the new kind = %q, %v; want %q", k, v, err, want)
		}
		if ok, _ := e.ks.Has(ctx, taskstore.IdemKey(string(from), k)); ok {
			t.Errorf("idem %s of the old kind survived the rename", k)
		}
	}
	// Cursor round trip.
	for _, c := range []string{"", formatRenameCursor(renameStamps, []byte{1, 2}), formatRenameCursor(renameClock, nil)} {
		p, after, err := parseRenameCursor(c)
		if err != nil || (c != "" && formatRenameCursor(p, after) != c) {
			t.Errorf("cursor %q round trip: %d %x %v", c, p, after, err)
		}
	}
	for _, bad := range []string{"x:00", "9:00", "1:zz", "1"} {
		if _, _, err := parseRenameCursor(bad); err == nil {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}
