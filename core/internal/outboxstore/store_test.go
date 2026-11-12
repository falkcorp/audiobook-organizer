// file: core/internal/outboxstore/store_test.go
// version: 1.0.0
// guid: a6f2d813-3c9e-47b0-8d51-0e7b4a9c2f68
// last-edited: 2026-11-12

package outboxstore_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/storage"
)

// The store is opened through storage.Open/Prepare/Migrate over MemFS, never
// coretest.NewStore: core/internal/outboxstore is in the kernel's import graph
// (the W27 rule), and coretest imports the kernel.

func openStore(t testing.TB) (*outboxstore.Store, storage.Keyspace) {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:   "/batcave/outbox.pebble",
		Owners: []storage.OwnerSpec{{Owner: outboxstore.Owner, Spec: outboxstore.StorageSpec(), Enabled: true}},
		Build:  "outboxstore+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, ok := db.Keyspace(outboxstore.Owner)
	if !ok {
		t.Fatal("no outbox keyspace")
	}
	return outboxstore.New(ks), ks
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

const (
	topic    = "gotham.signal.raised.v1"
	consumer = "gotham.dispatch.respond"
)

// Every key kind round-trips through its parser, and the encoding keeps seqs
// and partitions in numeric order and one topic's prefix from covering
// another's keys.
func TestKeys_RoundTripAndOrder(t *testing.T) {
	if tp, id, err := outboxstore.ParseInKey(outboxstore.InKey(topic, "01JABC")); err != nil || tp != topic || id != "01JABC" {
		t.Fatalf("in key: %q %q %v", tp, id, err)
	}
	if tp, pp, seq, err := outboxstore.ParseMsgKey(outboxstore.MsgKey(topic, 7, 42)); err != nil || tp != topic || pp != 7 || seq != 42 {
		t.Fatalf("msg key: %q %d %d %v", tp, pp, seq, err)
	}
	if c, tp, pp, err := outboxstore.ParseCurKey(outboxstore.CurKey(consumer, topic, 3)); err != nil || c != consumer || tp != topic || pp != 3 {
		t.Fatalf("cur key: %q %q %d %v", c, tp, pp, err)
	}
	if c, tp, seq, err := outboxstore.ParseDeadKey(outboxstore.DeadKey(consumer, topic, 9)); err != nil || c != consumer || tp != topic || seq != 9 {
		t.Fatalf("dead key: %q %q %d %v", c, tp, seq, err)
	}
	if tp, err := outboxstore.ParseSeqKey(outboxstore.SeqKey(topic)); err != nil || tp != topic {
		t.Fatalf("seq key: %q %v", tp, err)
	}

	if bytes.Compare(outboxstore.MsgKey(topic, 1, 9), outboxstore.MsgKey(topic, 1, 10)) >= 0 {
		t.Fatal("seq 9 sorts after seq 10: the seq is not zero-padded")
	}
	if bytes.Compare(outboxstore.MsgKey(topic, 2, 99), outboxstore.MsgKey(topic, 10, 1)) >= 0 {
		t.Fatal("partition 2 sorts after partition 10")
	}
	long := topic + "x"
	if bytes.HasPrefix(outboxstore.MsgKey(long, 0, 1), outboxstore.MsgTopicPrefix(topic)) {
		t.Fatal("one topic's prefix covers a longer topic's keys")
	}
	if !bytes.HasPrefix(outboxstore.MsgKey(topic, 5, 1), outboxstore.MsgPrefix(topic, 5)) ||
		bytes.HasPrefix(outboxstore.MsgKey(topic, 6, 1), outboxstore.MsgPrefix(topic, 5)) {
		t.Fatal("MsgPrefix does not select exactly one partition")
	}
	if _, _, _, err := outboxstore.ParseMsgKey(outboxstore.InKey(topic, "x")); err == nil {
		t.Fatal("an in/ key parsed as a message key")
	}
}

// Partition is a pure function of (key, partitions) inside [0, partitions),
// and spreads keys.
func TestPartition(t *testing.T) {
	seen := map[int]bool{}
	for i := range 400 {
		key := fmt.Sprintf("villain-%d", i)
		p := outboxstore.Partition(key, 16)
		if p < 0 || p >= 16 || p != outboxstore.Partition(key, 16) {
			t.Fatalf("Partition(%q, 16) = %d", key, p)
		}
		seen[p] = true
	}
	if len(seen) < 12 {
		t.Fatalf("400 keys reached only %d of 16 partitions", len(seen))
	}
	if outboxstore.Partition("anything", 1) != 0 {
		t.Fatal("one partition must be partition 0")
	}
	if !outboxstore.ValidPartitions(99) || outboxstore.ValidPartitions(100) || outboxstore.ValidPartitions(0) {
		t.Fatal("ValidPartitions bounds are 1 to 99")
	}
	if outboxstore.Partitions(0) != outboxstore.DefaultPartitions {
		t.Fatal("zero declared partitions means the default")
	}
}

func stageIntake(t testing.TB, s *outboxstore.Store, ks storage.Keyspace, id string, payload string, notBefore time.Time) {
	t.Helper()
	b := ks.NewBatch()
	defer b.Close()
	must(t, s.StageIntake(b, topic, id, outboxstore.Intake{
		ID: id, Key: "batcave", Payload: []byte(strconv.Quote(payload)), StagedAt: time.Now(), Producer: "gotham",
		NotBefore: notBefore,
	}))
	must(t, b.Commit(context.Background(), storage.Sync))
}

// ScanIntake returns rows in key order, never more than limit, never more than
// maxBytes of payload after the first, counts delayed rows without returning
// them, and returns the rows before an unreadable one together with its error.
func TestScanIntake_Bounds(t *testing.T) {
	s, ks := openStore(t)
	ctx := context.Background()
	now := time.Now()
	for i := range 6 {
		stageIntake(t, s, ks, fmt.Sprintf("01J%03d", i), strings.Repeat("x", 10), time.Time{})
	}
	stageIntake(t, s, ks, "01J900", "late", now.Add(time.Hour))

	rows, delayed, err := s.ScanIntake(ctx, topic, now, 4, 1<<20)
	must(t, err)
	if len(rows) != 4 || rows[0].ID != "01J000" || rows[3].ID != "01J003" || delayed != 0 {
		t.Fatalf("limit: %d rows (%v), delayed %d", len(rows), rows, delayed)
	}
	rows, delayed, err = s.ScanIntake(ctx, topic, now, 100, 25)
	must(t, err)
	if len(rows) != 2 || delayed != 0 {
		t.Fatalf("maxBytes 25 over 10-byte payloads returned %d rows (delayed %d), want 2", len(rows), delayed)
	}
	rows, _, err = s.ScanIntake(ctx, topic, now, 100, 1)
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("a first row larger than maxBytes must still be returned alone, got %d", len(rows))
	}
	rows, delayed, err = s.ScanIntake(ctx, topic, now, 100, 1<<20)
	must(t, err)
	if len(rows) != 6 || delayed != 1 {
		t.Fatalf("%d rows, %d delayed; want 6 and 1", len(rows), delayed)
	}
	rows, delayed, err = s.ScanIntake(ctx, topic, now.Add(2*time.Hour), 100, 1<<20)
	must(t, err)
	if len(rows) != 7 || delayed != 0 {
		t.Fatalf("once due: %d rows, %d delayed", len(rows), delayed)
	}

	// An unreadable row ends the scan with the rows before it.
	b := ks.NewBatch()
	must(t, b.Set(outboxstore.InKey(topic, "01J003x"), []byte("j{\"v\":2}")))
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()
	rows, _, err = s.ScanIntake(ctx, topic, now, 100, 1<<20)
	if err == nil || len(rows) != 4 {
		t.Fatalf("an unreadable row: %d rows, err %v; want the 4 before it and an error", len(rows), err)
	}
	topics, err := s.IntakeTopics(ctx)
	if err == nil && len(topics) != 1 {
		t.Fatalf("IntakeTopics = %v", topics)
	}
}

// StageSequence numbers rows in the order given, deletes their intake rows,
// stamps the partition count, and a second batch built from the same stale
// read of seq/ cannot also commit (no message is numbered twice).
func TestStageSequence_ConflictAndStamp(t *testing.T) {
	s, ks := openStore(t)
	ctx := context.Background()
	for i := range 3 {
		stageIntake(t, s, ks, fmt.Sprintf("01J%03d", i), "p", time.Time{})
	}
	rows, _, err := s.ScanIntake(ctx, topic, time.Now(), 100, 1<<20)
	must(t, err)

	b1, b2 := s.OpenBatch(), s.OpenBatch()
	defer b1.Close()
	defer b2.Close()
	hi, err := s.StageSequence(ctx, b1, topic, 4, rows[:2])
	must(t, err)
	if hi.High != 2 || hi.Partitions != 4 {
		t.Fatalf("after two rows: %+v", hi)
	}
	if _, err := s.StageSequence(ctx, b2, topic, 4, rows[:2]); err != nil {
		t.Fatal(err)
	}
	must(t, b1.Commit(ctx, storage.Sync))
	if err := b2.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("a second batch built on the same read of seq/ committed: %v", err)
	}
	got, ok, err := s.ReadHigh(ctx, topic)
	must(t, err)
	if !ok || got.High != 2 {
		t.Fatalf("high %+v after one winner, want 2", got)
	}
	left, _, err := s.ScanIntake(ctx, topic, time.Now(), 100, 1<<20)
	must(t, err)
	if len(left) != 1 || left[0].ID != "01J002" {
		t.Fatalf("intake left %v, want only the third row", left)
	}
	b3 := s.OpenBatch()
	defer b3.Close()
	hi, err = s.StageSequence(ctx, b3, topic, 4, left)
	must(t, err)
	must(t, b3.Commit(ctx, storage.Sync))
	if hi.High != 3 {
		t.Fatalf("the next seq is %d, want 3", hi.High)
	}

	// A changed partition count is refused and stages nothing.
	stageIntake(t, s, ks, "01J010", "p", time.Time{})
	more, _, _ := s.ScanIntake(ctx, topic, time.Now(), 100, 1<<20)
	b4 := s.OpenBatch()
	defer b4.Close()
	if _, err := s.StageSequence(ctx, b4, topic, 8, more); !errors.Is(err, outboxstore.ErrPartitionsChanged) {
		t.Fatalf("StageSequence over a changed partition count: %v", err)
	}
}

// Two writers of one pair cannot both commit: each loads the cursor with
// GetForUpdate and StagePair bumps Rev, so the loser fails with ErrConflict
// and nothing it staged lands. A writer that loaded an absent pair loses to one
// that created it.
func TestPair_GuardProtocol(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	p := outboxstore.Pair{Consumer: consumer, Topic: topic, PP: 2}
	now := time.Now()

	create := func() *storage.Batch {
		b := s.OpenBatch()
		st, err := s.LoadPair(ctx, b, p)
		must(t, err)
		if st.Has {
			t.Fatal("pair exists before its creation")
		}
		st.Cur.Seq = 1
		must(t, s.StagePair(b, p, st, now))
		return b
	}
	a, b := create(), create()
	defer a.Close()
	defer b.Close()
	must(t, a.Commit(ctx, storage.Sync))
	if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("two creators of one pair both committed: %v", err)
	}

	advance := func(seq uint64) *storage.Batch {
		b := s.OpenBatch()
		st, err := s.LoadPair(ctx, b, p)
		must(t, err)
		st.Cur.Seq = seq
		st.Try.Entries = []outboxstore.TryEntry{{Seq: seq + 1, Attempt: 2, NextAt: now, Token: "error"}}
		must(t, s.StagePair(b, p, st, now))
		return b
	}
	x, y := advance(5), advance(7)
	defer x.Close()
	defer y.Close()
	must(t, x.Commit(ctx, storage.Sync))
	if err := y.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("two writers of one pair both committed: %v", err)
	}
	st, err := s.ReadPair(ctx, p)
	must(t, err)
	if st.Cur.Seq != 5 || st.Cur.Rev != 2 || len(st.Try.Entries) != 1 || st.Try.Entries[0].Seq != 6 {
		t.Fatalf("pair state %+v, want the first writer's, Rev 2", st)
	}

	// A StagePair with no retry entries removes the try record.
	c := s.OpenBatch()
	defer c.Close()
	st2, err := s.LoadPair(ctx, c, p)
	must(t, err)
	st2.Try.Entries = nil
	must(t, s.StagePair(c, p, st2, now))
	must(t, c.Commit(ctx, storage.Sync))
	st3, err := s.ReadPair(ctx, p)
	must(t, err)
	if len(st3.Try.Entries) != 0 || st3.Cur.Rev != 3 {
		t.Fatalf("after clearing the retries: %+v", st3)
	}
}

// StageCursorInit creates a cursor per partition at the topic's head (or at
// zero with fromBeginning) and refuses to race a sequencing batch.
func TestStageCursorInit(t *testing.T) {
	s, ks := openStore(t)
	ctx := context.Background()
	for i := range 3 {
		stageIntake(t, s, ks, fmt.Sprintf("01J%03d", i), "p", time.Time{})
	}
	rows, _, _ := s.ScanIntake(ctx, topic, time.Now(), 100, 1<<20)
	bs := s.OpenBatch()
	_, err := s.StageSequence(ctx, bs, topic, 2, rows)
	must(t, err)
	must(t, bs.Commit(ctx, storage.Sync))
	bs.Close()

	b := s.OpenBatch()
	n, err := s.StageCursorInit(ctx, b, consumer, topic, 2, false, time.Now())
	must(t, err)
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()
	if n != 2 {
		t.Fatalf("created %d cursors, want one per partition", n)
	}
	for pp := range 2 {
		st, err := s.ReadPair(ctx, outboxstore.Pair{Consumer: consumer, Topic: topic, PP: pp})
		must(t, err)
		if !st.Has || st.Cur.Seq != 3 {
			t.Fatalf("partition %d cursor %+v, want the head (3)", pp, st)
		}
	}
	b = s.OpenBatch()
	n, err = s.StageCursorInit(ctx, b, "gotham.dispatch.relay", topic, 2, true, time.Now())
	must(t, err)
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()
	st, _ := s.ReadPair(ctx, outboxstore.Pair{Consumer: "gotham.dispatch.relay", Topic: topic, PP: 0})
	if n != 2 || st.Cur.Seq != 0 {
		t.Fatalf("fromBeginning: %d cursors, partition 0 at %d", n, st.Cur.Seq)
	}
	b = s.OpenBatch()
	n, err = s.StageCursorInit(ctx, b, consumer, topic, 2, false, time.Now())
	must(t, err)
	b.Close()
	if n != 0 {
		t.Fatalf("a second init created %d cursors", n)
	}
}

// Messages are read back by partition in seq order, counted and deleted
// through a seq, and the oldest are listed first.
func TestMessages_ReadCountDelete(t *testing.T) {
	s, ks := openStore(t)
	ctx := context.Background()
	for i := range 5 {
		stageIntake(t, s, ks, fmt.Sprintf("01J%03d", i), fmt.Sprintf("m%d", i), time.Time{})
	}
	rows, _, _ := s.ScanIntake(ctx, topic, time.Now(), 100, 1<<20)
	b := s.OpenBatch()
	_, err := s.StageSequence(ctx, b, topic, 1, rows)
	must(t, err)
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()

	got, err := s.MessagesAfter(ctx, topic, 0, 2, 10)
	must(t, err)
	if len(got) != 3 || got[0].Seq != 3 || got[2].Seq != 5 {
		t.Fatalf("MessagesAfter(2): %+v", got)
	}
	if got, err = s.MessagesAfter(ctx, topic, 0, 0, 2); err != nil || len(got) != 2 {
		t.Fatalf("limit 2: %d rows, %v", len(got), err)
	}
	if n, err := s.CountThrough(ctx, topic, 0, 1, 4); err != nil || n != 3 {
		t.Fatalf("CountThrough(1, 4) = %d, %v; want 3", n, err)
	}
	old, err := s.OldestMessages(ctx, topic, 0, 2)
	must(t, err)
	if len(old) != 2 || old[0].Seq != 1 {
		t.Fatalf("OldestMessages: %+v", old)
	}
	del := s.OpenBatch()
	must(t, s.StageDeleteThrough(del, topic, 0, 3))
	must(t, del.Commit(ctx, storage.Sync))
	del.Close()
	left, err := s.MessagesAfter(ctx, topic, 0, 0, 10)
	must(t, err)
	if len(left) != 2 || left[0].Seq != 4 {
		t.Fatalf("after deleting through 3: %+v", left)
	}
}

// Dead letters round-trip, list in (consumer, topic, seq) order, count per
// consumer and delete.
func TestDead_StoreListCountDelete(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	b := s.OpenBatch()
	for _, seq := range []uint64{9, 3, 5} {
		must(t, s.StageDead(ctx, b, consumer, topic, seq, outboxstore.Dead{
			Msg: outboxstore.Msg{ID: fmt.Sprintf("id%d", seq), Key: "batcave", Payload: []byte(`{}`)}, Part: 1, Token: "error", Attempts: 3, At: time.Now(),
		}))
	}
	must(t, s.StageDead(ctx, b, "wayne.vault.guard", "wayne.vault.tripped.v1", 1, outboxstore.Dead{Token: "permanent", Attempts: 1}))
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()

	d, ok, err := s.GetDead(ctx, consumer, topic, 5)
	must(t, err)
	if !ok || d.Msg.ID != "id5" || d.Attempts != 3 {
		t.Fatalf("GetDead: %+v %v", d, ok)
	}
	if _, ok, _ := s.GetDead(ctx, consumer, topic, 6); ok {
		t.Fatal("GetDead found a letter that was never staged")
	}
	counts, err := s.DeadCounts(ctx)
	must(t, err)
	if counts[consumer] != 3 || counts["wayne.vault.guard"] != 1 {
		t.Fatalf("DeadCounts = %v", counts)
	}
	var seqs []uint64
	for _, row := range mustScanDead(t, s) {
		if row.Consumer == consumer {
			seqs = append(seqs, row.Seq)
		}
	}
	if len(seqs) != 3 || seqs[0] != 3 || seqs[1] != 5 || seqs[2] != 9 {
		t.Fatalf("dead letters of %s list as %v, want 3, 5, 9", consumer, seqs)
	}
	del := s.OpenBatch()
	if _, ok, err := s.LoadDead(ctx, del, consumer, topic, 5); err != nil || !ok {
		t.Fatalf("LoadDead of the letter to delete: %v %v", ok, err)
	}
	must(t, s.StageDeleteDead(del, consumer, topic, 5))
	must(t, del.Commit(ctx, storage.Sync))
	del.Close()
	if _, ok, _ := s.GetDead(ctx, consumer, topic, 5); ok {
		t.Fatal("a deleted dead letter is still there")
	}
}

func mustScanDead(t testing.TB, s *outboxstore.Store) []outboxstore.DeadRow {
	t.Helper()
	var out []outboxstore.DeadRow
	rows, _, err := s.ScanDead(context.Background(), nil, 100)
	must(t, err)
	out = append(out, rows...)
	return out
}

// StageIntake needs a batch, and a retry of one message under a fresh id never
// shares the intake key of the original.
func TestStageIntake_Refusals(t *testing.T) {
	s, ks := openStore(t)
	if err := s.StageIntake(nil, topic, "x", outboxstore.Intake{}); err == nil {
		t.Fatal("StageIntake accepted a nil batch")
	}
	stageIntake(t, s, ks, "01J001", "a", time.Time{})
	stageIntake(t, s, ks, "01J002", "b", time.Time{})
	n, _, _, err := s.IntakeStats(context.Background(), topic, 100)
	must(t, err)
	if n != 2 {
		t.Fatalf("%d intake rows, want 2", n)
	}
	n, capped, _, err := s.IntakeStats(context.Background(), topic, 1)
	must(t, err)
	if n != 1 || !capped {
		t.Fatalf("IntakeStats cap: n=%d capped=%v", n, capped)
	}
}
