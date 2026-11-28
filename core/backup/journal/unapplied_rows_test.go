// file: core/backup/journal/unapplied_rows_test.go
// version: 0.1.0
// guid: 5a0c7d93-e1b4-4268-9f3a-7b2d4e8c1a60
// last-edited: 2026-11-21

package journal

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// TestScanUnapplied_ListsRowsReadUnappliedCannot: ReadUnapplied fails on the
// first row that does not decode; ScanUnapplied lists every row, the poison one
// flagged and the malformed key flagged, in key order.
func TestScanUnapplied_ListsRowsReadUnappliedCannot(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	fx.stageUnapplied(t, UnappliedOf(futureLine(2, "a0"), false), UnappliedOf(futureLine(10, "c0"), true))
	b := fx.ks.NewBatch()
	must(t, b.Set([]byte("journal_unapplied/00000000000000000005"), []byte("{broken")))
	must(t, b.Set([]byte("journal_unapplied/not-a-seq"), []byte(`{"seq":3,"mac":"x","kind":"k"}`)))
	must(t, b.Commit(ctx, storage.Sync))
	_ = b.Close()

	if _, err := ReadUnapplied(ctx, fx.ks); err == nil {
		t.Fatal("ReadUnapplied read a queue with a poison row; the test's premise is gone")
	}
	rows, err := ScanUnapplied(ctx, fx.ks)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("%d rows, want 4: %+v", len(rows), rows)
	}
	type want struct {
		seq       uint64
		malformed bool
		poison    bool
	}
	for i, w := range []want{{2, false, false}, {5, false, true}, {10, false, false}, {0, true, false}} {
		r := rows[i]
		if r.Seq != w.seq || r.Malformed != w.malformed || (r.Err != nil) != w.poison || (r.Entry == nil) != w.poison {
			t.Errorf("row %d = %+v, want %+v", i, r, w)
		}
	}
	if !rows[2].Entry.Closed || rows[0].Entry.Closed {
		t.Errorf("closed flags: %v %v", rows[0].Entry.Closed, rows[2].Entry.Closed)
	}
}

// TestStageUnappliedRowDelete_RemovesPoisonAndRefusesForeignKeys: the row is
// deleted by the key it has, which StageUnappliedDelete (seq-built) cannot do
// for a row whose key and entry disagree, and a key outside the queue is
// refused so the tool can name no other row.
func TestStageUnappliedRowDelete_RemovesPoisonAndRefusesForeignKeys(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	const poison = "journal_unapplied/00000000000000000005"
	b := fx.ks.NewBatch()
	must(t, b.Set([]byte(poison), []byte("{broken")))
	must(t, b.Commit(ctx, storage.Sync))
	_ = b.Close()

	rows, err := ScanUnapplied(ctx, fx.ks)
	must(t, err)
	del := fx.ks.NewBatch()
	defer func() { _ = del.Close() }()
	for _, bad := range []string{"journal_head", "journal_unapplied/", ""} {
		if err := StageUnappliedRowDelete(del, fx.ks, UnappliedRow{Key: bad}); err == nil {
			t.Errorf("key %q was accepted", bad)
		}
	}
	if del.Count() != 0 {
		t.Fatalf("refused keys staged %d write(s)", del.Count())
	}
	must(t, StageUnappliedRowDelete(del, fx.ks, rows[0]))
	must(t, del.Commit(ctx, storage.Sync))
	left, err := ScanUnapplied(ctx, fx.ks)
	must(t, err)
	if len(left) != 0 {
		t.Errorf("the poison row survived: %+v", left)
	}
}

// TestStageUnappliedRowRepair_ReplacesOnlyAPoisonRowWithItsOwnLine: a poison
// row becomes the entry for the line at its seq with Closed false, and nothing
// else can be repaired: a row that decodes (its Closed is evidence), a malformed
// key, a line of another seq.
func TestStageUnappliedRowRepair_ReplacesOnlyAPoisonRowWithItsOwnLine(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	b := fx.ks.NewBatch()
	must(t, b.Set([]byte("journal_unapplied/00000000000000000005"), []byte("{broken")))
	must(t, b.Set([]byte("journal_unapplied/00000000000000000007"), []byte(`{"seq":7,"mac":"c0","kind":"batcave.future","closed":true}`)))
	must(t, b.Set([]byte("journal_unapplied/not-a-seq"), []byte("{broken")))
	must(t, b.Commit(ctx, storage.Sync))
	_ = b.Close()
	rows, err := ScanUnapplied(ctx, fx.ks)
	must(t, err)
	byKey := map[string]UnappliedRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	line := futureLine(5, "a0")
	rep := fx.ks.NewBatch()
	defer func() { _ = rep.Close() }()
	if err := StageUnappliedRowRepair(rep, fx.ks, byKey["journal_unapplied/00000000000000000007"], futureLine(7, "c0")); err == nil {
		t.Error("a row that decodes was repaired")
	}
	if err := StageUnappliedRowRepair(rep, fx.ks, byKey["journal_unapplied/not-a-seq"], line); err == nil {
		t.Error("a malformed key was repaired")
	}
	if err := StageUnappliedRowRepair(rep, fx.ks, byKey["journal_unapplied/00000000000000000005"], futureLine(6, "a0")); err == nil {
		t.Error("a line of another seq repaired the row")
	}
	if rep.Count() != 0 {
		t.Fatalf("refused repairs staged %d write(s)", rep.Count())
	}
	must(t, StageUnappliedRowRepair(rep, fx.ks, byKey["journal_unapplied/00000000000000000005"], line))
	must(t, rep.Commit(ctx, storage.Sync))
	got, err := ScanUnapplied(ctx, fx.ks)
	must(t, err)
	var fixed *Unapplied
	for _, r := range got {
		if r.Seq == 5 {
			fixed = r.Entry
		}
	}
	if fixed == nil || *fixed != UnappliedOf(line, false) {
		t.Errorf("row 5 = %+v, want %+v", fixed, UnappliedOf(line, false))
	}
}
