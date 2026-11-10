// file: core/tasks/native_internal_test.go
// version: 1.0.1
// guid: 077fc32b-be7d-4ba9-9c02-c2ee627bcf57
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
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
	ks := coretest.NewStore(t, storage.OwnerSpec{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}).Keyspace(taskstore.Owner)
	s := NativeStores(ks)
	runs := s.Runs.(*nativeRuns)
	return nativeEnv{ks: ks, runs: runs, imp: runs.Importer().(*nativeImporter)}
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

// TestPruneChunk_SkipsWhatTheFreshReadRules out: a candidate that is no
// longer terminal, finished after the cutoff, missing or undecodable on the
// chunk's own read is skipped and never deleted (DATA8-8).
func TestPruneChunk_SkipsWhatTheFreshReadRulesOut(t *testing.T) {
	e := newNativeEnv(t)
	ctx := t.Context()
	cutoff := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	late := batRun("late", StateSucceeded)
	late.Finished = cutoff.Add(time.Hour)
	for _, r := range []RunRecord{batRun("old", StateCanceled), batRun("requeued", StateQueued), late} {
		if err := e.commit(t, func(b *storage.Batch) error { return e.runs.Insert(ctx, b, r) }); err != nil {
			t.Fatal(err)
		}
	}
	b := e.ks.NewBatch()
	if err := b.Expect(e.ks, taskstore.RunKey("corrupt"), nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Set(taskstore.RunKey("corrupt"), []byte("not a codec value")); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	n, err := e.runs.pruneChunk(ctx, []string{"old", "requeued", "late", "missing", "corrupt"}, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("pruneChunk = %d, %v; want only the old terminal run", n, err)
	}
	for id, want := range map[RunID]bool{"old": false, "requeued": true, "late": true} {
		if _, err := e.runs.Get(ctx, id); (err == nil) != want {
			t.Errorf("run %s present=%v, want %v", id, err == nil, want)
		}
	}
	if _, err := e.runs.Get(ctx, "corrupt"); err == nil || errors.Is(err, ErrRunNotFound) {
		t.Errorf("corrupt row: %v, want a decode error (left in place)", err)
	}
	// A listing hides the undecodable row behind an error and goes on.
	var listed, failed int
	for _, err := range e.runs.List(ctx, RunQuery{}) {
		if err != nil {
			failed++
			continue
		}
		listed++
	}
	if listed != 2 || failed != 1 {
		t.Errorf("List over a corrupt row: %d rows, %d errors; want 2 and 1", listed, failed)
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

// TestRenameKind_EdgeCases: stale and dangling kind-index entries, a stamp
// and a bucket that the new kind already holds, and a clock both kinds
// hold.
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
			if err := e.runs.Debounce(ctx, b, DebounceBucket{Kind: k, Subjects: []entity.Ref{{Module: "gotham", Kind: "villains", ID: string(k)}}, First: early, Last: early}); err != nil {
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
	if len(buckets) != 1 || buckets[0].Kind != to || len(buckets[0].Subjects) != 2 {
		t.Errorf("buckets after the rename = %+v, want one merged bucket of %s", buckets, to)
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
