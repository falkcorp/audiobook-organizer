// file: core/backup/journal/unapplied_test.go
// version: 1.0.0
// guid: b2246e94-6521-4613-80c1-b9ec0376c80a
// last-edited: 2026-11-12

package journal

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// futureLine is a line of a kind only a newer build writes.
func futureLine(seq uint64, mac string) Line {
	return Line{Seq: seq, Kind: "user.suspended", At: time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC), MAC: mac}
}

// stageUnapplied stages and commits entries in one batch.
func (fx *fixture) stageUnapplied(t *testing.T, us ...Unapplied) {
	t.Helper()
	ctx := context.Background()
	b := fx.ks.NewBatch()
	defer func() { _ = b.Close() }()
	for _, u := range us {
		must(t, StageUnapplied(ctx, b, fx.ks, u))
	}
	if b.Count() > 0 {
		must(t, b.Commit(ctx, storage.Sync))
	}
}

func (fx *fixture) unapplied(t *testing.T) []Unapplied {
	t.Helper()
	got, err := ReadUnapplied(context.Background(), fx.ks)
	must(t, err)
	return got
}

func seqsOf(us []Unapplied) []uint64 {
	var out []uint64
	for _, u := range us {
		out = append(out, u.Seq)
	}
	return out
}

// TestUnapplied_ListsInJournalOrderAndDeletesInTheCallersBatch: the entries come
// back in seq order whatever order they were staged in (the key is fixed-width),
// carry what the line said, and the delete rides in the caller's batch: it is
// gone when that commits and not before.
func TestUnapplied_ListsInJournalOrderAndDeletesInTheCallersBatch(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	fx.stageUnapplied(t, UnappliedOf(futureLine(10, "c0"), false), UnappliedOf(futureLine(2, "a0"), false), UnappliedOf(futureLine(9, "b0"), true))
	got := fx.unapplied(t)
	if !slices.Equal(seqsOf(got), []uint64{2, 9, 10}) {
		t.Fatalf("entries = %v, want seqs [2 9 10]", seqsOf(got))
	}
	if e := got[1]; e.Kind != "user.suspended" || e.MAC != "b0" || !e.Closed || !e.At.Equal(time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("entry 9 = %+v: it should carry the line's kind, mac, time and the closed flag", e)
	}

	b := fx.ks.NewBatch()
	must(t, StageUnappliedDelete(b, fx.ks, 9))
	if got := fx.unapplied(t); len(got) != 3 {
		t.Fatalf("%d entries before the batch committed, want 3", len(got))
	}
	must(t, b.Commit(ctx, storage.Sync))
	must(t, b.Close())
	if got := seqsOf(fx.unapplied(t)); !slices.Equal(got, []uint64{2, 10}) {
		t.Errorf("entries after the delete = %v, want [2 10]", got)
	}

	b = fx.ks.NewBatch()
	must(t, StageUnappliedDelete(b, fx.ks, 2))
	must(t, b.Close()) // never committed: the line was not applied
	if got := seqsOf(fx.unapplied(t)); !slices.Equal(got, []uint64{2, 10}) {
		t.Errorf("an uncommitted delete removed an entry: %v", got)
	}
}

// TestUnapplied_RestagingTheSameLineKeepsItsClosedFlag: a boot that crashed
// after queueing a line queues it again from the journal; that must not take
// back the Closed an earlier boot recorded (one fail-closed pass per entry), but
// a different line at that seq (a replaced journal) takes the entry over.
func TestUnapplied_RestagingTheSameLineKeepsItsClosedFlag(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	u := UnappliedOf(futureLine(5, "aa"), false)
	fx.stageUnapplied(t, u)
	must(t, MarkUnappliedClosed(ctx, fx.ks, []Unapplied{u}))
	if got := fx.unapplied(t); len(got) != 1 || !got[0].Closed {
		t.Fatalf("entries = %+v, want one closed", got)
	}

	fx.stageUnapplied(t, u) // the same line, queued again, unclosed
	if got := fx.unapplied(t); len(got) != 1 || !got[0].Closed {
		t.Errorf("entries after re-staging the same line = %+v, want it still closed", got)
	}

	other := UnappliedOf(futureLine(5, "bb"), false)
	fx.stageUnapplied(t, other)
	if got := fx.unapplied(t); len(got) != 1 || got[0].MAC != "bb" || got[0].Closed {
		t.Errorf("entries after another line at seq 5 = %+v, want that line's, unclosed", got)
	}
}

// TestUnapplied_MarkClosedTouchesOnlyTheEntriesItWasToldAbout: an entry that is
// gone, or that now stands for another line, is left alone, and nothing to mark
// writes nothing.
func TestUnapplied_MarkClosedTouchesOnlyTheEntriesItWasToldAbout(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	a, b := UnappliedOf(futureLine(3, "aa"), false), UnappliedOf(futureLine(4, "bb"), false)
	fx.stageUnapplied(t, a, b)

	stale := b
	stale.MAC = "zz"
	gone := UnappliedOf(futureLine(99, "xx"), false)
	must(t, MarkUnappliedClosed(ctx, fx.ks, []Unapplied{a, stale, gone}))
	got := fx.unapplied(t)
	if len(got) != 2 || !got[0].Closed || got[1].Closed {
		t.Errorf("entries = %+v, want seq 3 closed and seq 4 (another mac) open, nothing created for seq 99", got)
	}
	must(t, MarkUnappliedClosed(ctx, fx.ks, nil))
	must(t, MarkUnappliedClosed(ctx, fx.ks, []Unapplied{a})) // already closed: nothing to write
	if got := fx.unapplied(t); len(got) != 2 {
		t.Errorf("entries = %+v", got)
	}
}

// TestUnapplied_AnEntryThatDoesNotDecodeIsAnError: a queue row nobody can read
// names a revocation nobody can identify; reading the queue says so instead of
// stepping over it, and staging over it is not a way to lose it.
func TestUnapplied_AnEntryThatDoesNotDecodeIsAnError(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	b := fx.ks.NewBatch()
	must(t, b.In(fx.ks).Set(unappliedKey(7), []byte("{not json")))
	must(t, b.Commit(ctx, storage.Sync))
	must(t, b.Close())

	if _, err := ReadUnapplied(ctx, fx.ks); err == nil || !strings.Contains(err.Error(), string(unappliedKey(7))) {
		t.Fatalf("ReadUnapplied over an undecodable entry = %v, want an error naming it", err)
	}
	sb := fx.ks.NewBatch()
	defer func() { _ = sb.Close() }()
	if err := StageUnapplied(ctx, sb, fx.ks, UnappliedOf(futureLine(7, "aa"), false)); err == nil {
		t.Error("StageUnapplied overwrote an entry it could not read")
	}
}

// TestChainLineAt: the verified line at a seq, and nothing for the header, a seq
// past the end, or a seq a base (retention) dropped.
func TestChainLineAt(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	fx.commitRevoke(t, j, "joker")
	fx.commitRevoke(t, j, "penguin")
	must(t, j.Close())
	c := fx.chain(t)
	l, ok := c.LineAt(2)
	if !ok || l.Seq != 2 || l.MAC != c.Lines[1].MAC || l.Kind != UserDisabled {
		t.Errorf("LineAt(2) = %+v, %v", l, ok)
	}
	for _, seq := range []uint64{0, 3, 1 << 40} {
		if l, ok := c.LineAt(seq); ok {
			t.Errorf("LineAt(%d) = %+v, true, want nothing", seq, l)
		}
	}

	head := fx.head(t)
	must(t, os.Remove(fx.path))
	hdr := Header{InstallID: installID, App: "gotham", StartedAt: time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)}
	must(t, CreateBased(fx.path, fx.keys, hdr, head))
	j = fx.open(t)
	fx.commitRevoke(t, j, "riddler")
	must(t, j.Close())
	c = fx.chain(t)
	for _, seq := range []uint64{1, 2} {
		if l, ok := c.LineAt(seq); ok {
			t.Errorf("LineAt(%d) on a journal based at 2 = %+v, true, want nothing: retention dropped it", seq, l)
		}
	}
	if l, ok := c.LineAt(3); !ok || l.Seq != 3 {
		t.Errorf("LineAt(3) = %+v, %v, want the first line after the base", l, ok)
	}
	if _, ok := (&Chain{}).LineAt(1); ok {
		t.Error("LineAt on an empty chain found a line")
	}
}
