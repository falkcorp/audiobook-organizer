// file: core/internal/taskstore/store_test.go
// version: 1.1.1
// guid: 2d78def0-3849-42b2-8b19-419c3bee7249
// last-edited: 2026-11-10

package taskstore

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// boot opens a fixture store holding the c/tasks/ keyspace as the kernel
// registers it, and returns the staging layer over it.
func boot(t *testing.T) *Store {
	t.Helper()
	ks := openKeyspace(t)
	if ks.Prefix() != "c/tasks/" {
		t.Fatalf("prefix %q", ks.Prefix())
	}
	return New(ks)
}

// openKeyspace boots an in-memory store as the kernel's P2, P4a and P4b do
// (storage.Open, Prepare, Migrate) with c/tasks/ registered by StorageSpec,
// returns that keyspace and closes the store at cleanup. It opens storage
// itself instead of calling coretest.NewStore: coretest imports app, app
// imports kernel and kernel's imports reach this package (through
// internal/storagemigrate and tasks), so an in-package test that imports
// coretest is an import cycle.
func openKeyspace(t *testing.T) storage.Keyspace {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join(t.TempDir(), "main.pebble"),
		Owners:          []storage.OwnerSpec{{Owner: Owner, Spec: StorageSpec(), Enabled: true}},
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
	ks, ok := db.Keyspace(Owner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", Owner)
	}
	return ks
}

func t0() time.Time { return time.Date(2026, time.March, 14, 21, 0, 0, 0, time.UTC) }

// TestStorageSpec_SerializedIsExactlyTheGuardFamilies (5T.6.1): a write to a
// guard key without a precondition is refused (ErrBlindWrite) and a write to
// every other family is not.
func TestStorageSpec_SerializedIsExactlyTheGuardFamilies(t *testing.T) {
	st := boot(t)
	ctx := t.Context()
	if got := len(StorageSpec().Serialized); got != 4 || !StorageSpec().Keyspace {
		t.Fatalf("StorageSpec = %+v", StorageSpec())
	}
	subj := Subject{Module: "gotham", Kind: "villains", ID: "joker"}
	guard := map[string][]byte{
		"run":      RunKey("patrol-1"),
		"debounce": DebounceKey("gotham.patrol", "east-end"),
		"sched":    SchedKey("gotham.patrol"),
		"idem":     IdemKey("gotham.patrol", "bat-signal"),
	}
	free := map[string][]byte{
		"queue":     QueueKey(2, t0(), "patrol-1"),
		"ix/kind":   KindIndexKey("gotham.patrol", "patrol-1"),
		"ix/state":  StateIndexKey("queued", "patrol-1"),
		"ix/done":   DoneIndexKey(t0(), "patrol-1"),
		"page":      PageKey("patrol-1", PageLog, SeqSeg(1)),
		"ckpt":      CheckpointKey("patrol-1", "watermark"),
		"chunk":     ChunkKey("patrol-1", 0),
		"row":       RowKey("patrol-1", "plan:joker"),
		"mark":      MarkKey("patrol-1", "plan:joker"),
		"strike":    StrikeKey("patrol-1", 1),
		"pause":     PauseKey(),
		"standdown": StandDownKey("gotham.blackout"),
		"req":       ReqKey(subj, "gotham.patrol"),
		"req-rev":   ReqRevKey("gotham.patrol", subj),
		"def":       DefKey("gotham.patrol"),
	}
	for name, k := range guard {
		if err := st.Keyspace().Set(ctx, k, []byte("x")); !errors.Is(err, storage.ErrBlindWrite) {
			t.Errorf("blind write of the %s guard key: %v, want ErrBlindWrite", name, err)
		}
		b := st.NewBatch()
		if err := st.ExpectValue(b, k, nil); err != nil {
			t.Fatalf("%s: Expect: %v", name, err)
		}
		if err := st.Set(b, k, []byte("x")); err != nil {
			t.Fatalf("%s: Set: %v", name, err)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			t.Errorf("guarded write of the %s key: %v", name, err)
		}
		_ = b.Close()
	}
	for name, k := range free {
		if err := st.Keyspace().Set(ctx, k, nil); err != nil {
			t.Errorf("write of the %s key: %v", name, err)
		}
		b := st.NewBatch()
		if err := st.ExpectValue(b, k, nil); !errors.Is(err, storage.ErrNotSerialized) {
			t.Errorf("Expect on the %s key: %v, want ErrNotSerialized", name, err)
		}
		_ = b.Close()
	}
}

// TestDeletePrefix_RefusedOverGuardFamilies (05-storage §5S.3.4): a
// DeleteRange over a guard family is refused, over any other family allowed.
func TestDeletePrefix_RefusedOverGuardFamilies(t *testing.T) {
	st := boot(t)
	ctx := t.Context()
	for _, p := range [][]byte{RunPrefix(), DebouncePrefix(""), keys.Prefix(keys.Str(famSched)), IdemPrefix("")} {
		b := st.NewBatch()
		if err := st.DeletePrefix(b, p); !errors.Is(err, storage.ErrBlindWrite) {
			t.Errorf("DeleteRange over %q: %v, want ErrBlindWrite", p, err)
		}
		_ = b.Close()
	}
	if err := st.Keyspace().Set(ctx, PageKey("patrol-1", PageLog, SeqSeg(1)), []byte("x")); err != nil {
		t.Fatal(err)
	}
	b := st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := st.DeletePrefix(b, PagePrefix("patrol-1", PageLog)); err != nil {
		t.Fatalf("DeleteRange over a page: %v", err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.Get(ctx, PageKey("patrol-1", PageLog, SeqSeg(1))); found {
		t.Fatal("page entry survived its DeleteRange")
	}
}

// TestQueueKey_PriorityThenEnqueueTime: PrioritySystem (4) sorts first, then
// High, Normal, Low; within a priority the earlier enqueue time first.
func TestQueueKey_PriorityThenEnqueueTime(t *testing.T) {
	ordered := [][]byte{
		QueueKey(4, t0().Add(time.Hour), "system"),
		QueueKey(3, t0(), "high-early"),
		QueueKey(3, t0().Add(time.Minute), "high-late"),
		QueueKey(2, t0().Add(-time.Hour), "normal"),
		QueueKey(1, time.Unix(-10, 0), "low-before-1970"),
		QueueKey(1, t0(), "low"),
	}
	if !slices.IsSortedFunc(ordered, bytes.Compare) {
		t.Fatal("queue keys do not sort in dispatch order")
	}
	if !bytes.HasPrefix(ordered[0], QueuePrefix()) {
		t.Fatal("queue key outside its prefix")
	}
}

// TestRowIDPrefix: the range of a row-ID prefix holds exactly the rows whose
// IDs begin with it, embedded zero bytes included.
func TestRowIDPrefix(t *testing.T) {
	rows := []string{"a", "a\x00b", "a\xffz", "ab", "b", "plan:", "plan:joker", "plan;x"}
	for _, prefix := range []string{"a", "a\x00", "plan:", "\xff"} {
		p := RowIDPrefix("patrol-1", prefix)
		end := keys.PrefixEnd(p)
		for _, id := range rows {
			k := RowKey("patrol-1", id)
			in := bytes.Compare(k, p) >= 0 && (end == nil || bytes.Compare(k, end) < 0)
			if in != strings.HasPrefix(id, prefix) {
				t.Errorf("prefix %q, row %q: in range = %v", prefix, id, in)
			}
		}
		if other := RowKey("patrol-2", prefix+"x"); bytes.Compare(other, p) >= 0 && bytes.Compare(other, end) < 0 {
			t.Errorf("prefix %q covers another run's row", prefix)
		}
	}
}

func has(t *testing.T, st *Store, key []byte) bool {
	t.Helper()
	_, found, err := st.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func commitRun(t *testing.T, st *Store, prev *RunIndex, next RunIndex) {
	t.Helper()
	b := st.NewBatch()
	defer func() { _ = b.Close() }()
	if _, _, err := st.ReadForUpdate(t.Context(), b, RunKey(next.ID)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRun(b, []byte("row"), prev, next); err != nil {
		t.Fatalf("PutRun: %v", err)
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestPutRun_MovesOnlyChangedDerivedKeys: the queue entry, the kind, state
// and done indexes follow the row, and nothing stale is left.
func TestPutRun_MovesOnlyChangedDerivedKeys(t *testing.T) {
	st := boot(t)
	ctx := t.Context()
	q := RunIndex{ID: "patrol-1", Kind: "gotham.patrol", State: "queued", Queued: true, Priority: 3, Enqueued: t0()}
	commitRun(t, st, nil, q)
	for _, k := range [][]byte{RunKey("patrol-1"), q.queueKey(), KindIndexKey("gotham.patrol", "patrol-1"), StateIndexKey("queued", "patrol-1")} {
		if !has(t, st, k) {
			t.Fatalf("missing %q after insert", k)
		}
	}
	running := q
	running.State, running.Queued = "running", false
	commitRun(t, st, &q, running)
	if has(t, st, q.queueKey()) || has(t, st, StateIndexKey("queued", "patrol-1")) || !has(t, st, StateIndexKey("running", "patrol-1")) {
		t.Fatal("claim did not move the queue entry and state index")
	}
	requeued := running
	requeued.State, requeued.Queued, requeued.Priority = "queued", true, 4
	commitRun(t, st, &running, requeued)
	if !has(t, st, requeued.queueKey()) || has(t, st, q.queueKey()) {
		t.Fatal("re-queue at a new priority left the old queue entry or missed the new one")
	}
	done := requeued
	done.State, done.Queued, done.Terminal, done.Finished, done.Kind = "succeeded", false, true, t0().Add(time.Hour), "gotham.night-patrol"
	commitRun(t, st, &requeued, done)
	if !has(t, st, done.doneKey()) || has(t, st, requeued.queueKey()) || has(t, st, KindIndexKey("gotham.patrol", "patrol-1")) || !has(t, st, KindIndexKey("gotham.night-patrol", "patrol-1")) {
		t.Fatal("terminal transition with a kind change left stale keys")
	}
	refinished := done
	refinished.Finished = t0().Add(2 * time.Hour)
	commitRun(t, st, &done, refinished)
	if has(t, st, done.doneKey()) || !has(t, st, refinished.doneKey()) {
		t.Fatal("a new finish time left the old done entry")
	}
	counts, err := st.StateCounts(ctx)
	if err != nil || len(counts) != 1 || counts["succeeded"] != 1 {
		t.Fatalf("StateCounts = %v, %v", counts, err)
	}
	b := st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := st.PutRun(b, nil, nil, RunIndex{}); err == nil {
		t.Error("PutRun without an ID succeeded")
	}
	if err := st.PutRun(b, nil, &RunIndex{ID: "other"}, RunIndex{ID: "patrol-1"}); err == nil {
		t.Error("PutRun over another run's index succeeded")
	}
	if err := st.DeleteRun(b, RunIndex{}); err == nil {
		t.Error("DeleteRun without an ID succeeded")
	}
}

// TestDeleteRun_RemovesEveryFamilyOfOneRun.
func TestDeleteRun_RemovesEveryFamilyOfOneRun(t *testing.T) {
	st := boot(t)
	ctx := t.Context()
	idx := func(id string) RunIndex {
		return RunIndex{ID: id, Kind: "gotham.case-file", State: "failed", Terminal: true, Finished: t0()}
	}
	owned := func(id string) [][]byte {
		return [][]byte{
			PageKey(id, PageEvents, SeqSeg(1)), PageKey(id, PageItems, keys.Str("joker")),
			CheckpointKey(id, "watermark"), ChunkKey(id, 3), RowKey(id, "plan:joker"),
			MarkKey(id, "plan:joker"), StrikeKey(id, 7),
		}
	}
	for _, id := range []string{"case-1", "case-10"} { // "case-1" is a byte prefix of "case-10"
		commitRun(t, st, nil, idx(id))
		for _, k := range owned(id) {
			if err := st.Keyspace().Set(ctx, k, []byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	b := st.NewBatch()
	defer func() { _ = b.Close() }()
	if _, _, err := st.ReadForUpdate(ctx, b, RunKey("case-1")); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRun(b, idx("case-1")); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("commit: %v", err)
	}
	gone := append(owned("case-1"), RunKey("case-1"), KindIndexKey("gotham.case-file", "case-1"), StateIndexKey("failed", "case-1"), DoneIndexKey(t0(), "case-1"))
	for _, k := range gone {
		if has(t, st, k) {
			t.Errorf("deleted run kept %q", k)
		}
	}
	for _, k := range append(owned("case-10"), RunKey("case-10"), DoneIndexKey(t0(), "case-10")) {
		if !has(t, st, k) {
			t.Errorf("the other run lost %q", k)
		}
	}
}

// TestScanIDsAndDoneBefore: the cursor and limit of Scan, the IDs of an
// index, and the done index's cutoff and limit.
func TestScanIDsAndDoneBefore(t *testing.T) {
	st := boot(t)
	ctx := t.Context()
	for i, id := range []string{"a", "b", "c", "d"} {
		commitRun(t, st, nil, RunIndex{ID: id, Kind: "gotham.patrol", State: "canceled", Terminal: true, Finished: t0().Add(time.Duration(i) * time.Hour)})
	}
	var got []string
	for kv, err := range st.Scan(ctx, RunPrefix(), RunKey("a"), 2) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(lastSeg(kv.Key)))
	}
	if !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("Scan after a, limit 2 = %v", got)
	}
	got = nil
	for id, err := range st.IDs(ctx, KindIndexPrefix("gotham.patrol")) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("IDs = %v", got)
	}
	idsOf := func(es []DoneEntry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	cutoff := t0().Add(150 * time.Minute)
	es, err := st.DoneBefore(ctx, cutoff, nil, 10)
	if err != nil || !slices.Equal(idsOf(es), []string{"a", "b", "c"}) {
		t.Fatalf("DoneBefore = %v, %v", idsOf(es), err)
	}
	if !es[1].Finished.Equal(t0().Add(time.Hour)) || !bytes.Equal(es[1].Key, DoneIndexKey(t0().Add(time.Hour), "b")) {
		t.Fatalf("DoneBefore entry = %+v", es[1])
	}
	if es, _ := st.DoneBefore(ctx, cutoff, nil, 2); !slices.Equal(idsOf(es), []string{"a", "b"}) {
		t.Fatalf("DoneBefore limit 2 = %v", idsOf(es))
	}
	if es, _ := st.DoneBefore(ctx, cutoff, es[0].Key, 10); !slices.Equal(idsOf(es), []string{"b", "c"}) {
		t.Fatalf("DoneBefore after a = %v", idsOf(es))
	}
}

// TestNextStrike_Unique: strike numbers are never issued twice.
func TestNextStrike_Unique(t *testing.T) {
	st := boot(t)
	seen := map[uint64]bool{}
	for range 100 {
		b := st.NewBatch()
		n, err := st.NextStrike(t.Context(), b)
		_ = b.Close()
		if err != nil || seen[n] {
			t.Fatalf("NextStrike = %d, %v (seen %v)", n, err, seen[n])
		}
		seen[n] = true
	}
	if _, err := st.NextStrike(t.Context(), nil); err == nil {
		t.Fatal("NextStrike with a nil batch succeeded")
	}
}

// TestReadForUpdate: absent and present guard keys, a nil batch and a
// batch that is not indexed.
func TestReadForUpdate(t *testing.T) {
	st := boot(t)
	ctx := t.Context()
	b := st.NewBatch()
	defer func() { _ = b.Close() }()
	if _, found, err := st.ReadForUpdate(ctx, b, RunKey("nobody")); err != nil || found {
		t.Fatalf("absent: found=%v err=%v", found, err)
	}
	commitRun(t, st, nil, RunIndex{ID: "somebody", Kind: "gotham.patrol", State: "queued"})
	b2 := st.NewBatch()
	defer func() { _ = b2.Close() }()
	if v, found, err := st.ReadForUpdate(ctx, b2, RunKey("somebody")); err != nil || !found || string(v) != "row" {
		t.Fatalf("present: %q found=%v err=%v", v, found, err)
	}
	if _, _, err := st.ReadForUpdate(ctx, nil, RunKey("x")); !errors.Is(err, ErrNilBatch) {
		t.Fatalf("nil batch: %v", err)
	}
	plain := st.Keyspace().NewBatch()
	defer func() { _ = plain.Close() }()
	if _, _, err := st.ReadForUpdate(ctx, plain, RunKey("somebody")); !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("plain batch: %v, want ErrNotIndexed", err)
	}
	for _, f := range []func() error{
		func() error { return st.Set(nil, PauseKey(), nil) },
		func() error { return st.Delete(nil, PauseKey()) },
		func() error { return st.DeletePrefix(nil, StandDownPrefix()) },
		func() error { return st.ExpectValue(nil, RunKey("x"), nil) },
	} {
		if err := f(); !errors.Is(err, ErrNilBatch) {
			t.Errorf("nil batch: %v", err)
		}
	}
}

// TestNewULID: 26 Crockford characters, sorted by time, and the entropy
// error surfaced.
func TestNewULID(t *testing.T) {
	r := bytes.NewReader(bytes.Repeat([]byte{0xA5}, 1000))
	a, err := NewULID(t0(), r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewULID(t0().Add(time.Millisecond), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 26 || strings.Trim(a, crockford) != "" || a >= b {
		t.Fatalf("ULIDs %q, %q", a, b)
	}
	zero, err := NewULID(time.UnixMilli(0), bytes.NewReader(make([]byte, 10)))
	if err != nil || zero != strings.Repeat("0", 26) {
		t.Fatalf("zero ULID = %q, %v", zero, err)
	}
	if _, err := NewULID(t0(), iotest.ErrReader(errors.New("no entropy in the Batcave"))); err == nil {
		t.Fatal("NewULID without entropy succeeded")
	}
}

// TestKeyParsers: every Parse* inverts its builder, and refuses a key of
// another family or shape.
func TestKeyParsers(t *testing.T) {
	joker := Subject{Module: "gotham", Kind: "villains", ID: "joker"}
	if k, id, err := ParseKindIndexKey(KindIndexKey("gotham.patrol", "p1")); err != nil || k != "gotham.patrol" || id != "p1" {
		t.Errorf("ParseKindIndexKey = %q %q %v", k, id, err)
	}
	if s, id, err := ParseStateIndexKey(StateIndexKey("queued", "p1")); err != nil || s != "queued" || id != "p1" {
		t.Errorf("ParseStateIndexKey = %q %q %v", s, id, err)
	}
	for _, at := range []time.Time{t0(), time.Date(1966, 7, 30, 0, 0, 0, 1, time.UTC)} {
		if f, id, err := ParseDoneIndexKey(DoneIndexKey(at, "p1")); err != nil || !f.Equal(at) || id != "p1" {
			t.Errorf("ParseDoneIndexKey(%v) = %v %q %v", at, f, id, err)
		}
	}
	if id, p, c, err := ParsePageKey(PageKey("p1", PageLog, SeqSeg(42))); err != nil || id != "p1" || p != PageLog {
		t.Errorf("ParsePageKey = %q %q %v", id, p, err)
	} else if n, err := ParseSeqSeg(c); err != nil || n != 42 {
		t.Errorf("ParseSeqSeg = %d %v", n, err)
	}
	if k, s, err := ParseReqRevKey(ReqRevKey("gotham.patrol", joker)); err != nil || k != "gotham.patrol" || s != joker {
		t.Errorf("ParseReqRevKey = %q %+v %v", k, s, err)
	}
	if k, key, err := ParseIdemKey(IdemKey("gotham.patrol", "bat-signal")); err != nil || k != "gotham.patrol" || key != "bat-signal" {
		t.Errorf("ParseIdemKey = %q %q %v", k, key, err)
	}
	for name, err := range map[string]error{
		"kind index from a state key": func() error { _, _, err := ParseKindIndexKey(StateIndexKey("queued", "p1")); return err }(),
		"state index from a run key":  func() error { _, _, err := ParseStateIndexKey(RunKey("p1")); return err }(),
		"done index, short time": func() error {
			_, _, err := ParseDoneIndexKey(keys.Join(keys.Str(famIndex), keys.Str(ixDone), keys.Str("x"), keys.Str("p1")))
			return err
		}(),
		"page from a ckpt key": func() error { _, _, _, err := ParsePageKey(CheckpointKey("p1", "c")); return err }(),
		"short seq":            func() error { _, err := ParseSeqSeg(keys.Str("x")); return err }(),
		"rev from a req key":   func() error { _, _, err := ParseReqRevKey(ReqKey(joker, "gotham.patrol")); return err }(),
		"idem from no key":     func() error { _, _, err := ParseIdemKey([]byte("no terminator")); return err }(),
	} {
		if !errors.Is(err, ErrMalformedKey) {
			t.Errorf("%s: %v, want ErrMalformedKey", name, err)
		}
	}
	if lastSeg([]byte("no terminator")) != nil {
		t.Fatal("lastSeg of a malformed key")
	}
	if !bytes.Equal(StateIndexPrefix(""), keys.Prefix(keys.Str(famIndex), keys.Str(ixState))) {
		t.Fatal("StateIndexPrefix(\"\")")
	}
	if !bytes.Equal(IdemPrefix("gotham.patrol"), keys.Prefix(keys.Str(famIdem), keys.Str("gotham.patrol"))) {
		t.Fatal("IdemPrefix(kind)")
	}
}
