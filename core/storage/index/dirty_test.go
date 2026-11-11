// file: core/storage/index/dirty_test.go
// version: 1.0.0
// guid: f791d8fa-55b4-41c6-ab73-231f0ae10350
// last-edited: 2026-11-11

package index_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/storage/index/indextest"
)

const dbPath = "/batcave/index.pebble"

func openMem(t *testing.T) *indextest.Fixture {
	t.Helper()
	return indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
}

func pendingIDs(t *testing.T, d *index.DirtySet) []string {
	t.Helper()
	ms, err := d.Pending(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

// TestIndexMarkDirtyInSameBatch is G4: the dirty mark, the generation touch and
// the entity write commit together or not at all.
func TestIndexMarkDirtyInSameBatch(t *testing.T) {
	ctx := t.Context()
	f := openMem(t)
	d := f.DirtySet(indextest.Signal)
	der := f.Derived(indextest.Signal)
	g0, err := der.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A batch that is closed uncommitted leaves no entity, no mark and no
	// generation change.
	b := f.Module.NewBatch()
	if err := b.Set([]byte("villain/joker"), []byte("arkham")); err != nil {
		t.Fatal(err)
	}
	if err := d.Mark(ctx, b, "joker"); err != nil {
		t.Fatal(err)
	}
	if got := pendingIDs(t, d); len(got) != 0 {
		t.Fatalf("a mark is visible before its batch commits: %v", got)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Module.Get(ctx, []byte("villain/joker")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("entity of an abandoned batch: %v", err)
	}
	if got := pendingIDs(t, d); len(got) != 0 {
		t.Fatalf("mark of an abandoned batch: %v", got)
	}
	if g, _ := der.Current(ctx); g != g0 {
		t.Fatal("an abandoned batch moved the generation")
	}

	// The committed batch carries all three.
	b = f.Module.NewBatch()
	if err := b.Set([]byte("villain/joker"), []byte("arkham")); err != nil {
		t.Fatal(err)
	}
	if err := d.Mark(ctx, b, "joker"); err != nil {
		t.Fatal(err)
	}
	if got := b.Participants(); !slices.Contains(got, index.Owner) || !slices.Contains(got, indextest.Module) {
		t.Fatalf("participants %v: the one batch must span the entity, the dirty set and the generation", got)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if got := pendingIDs(t, d); !slices.Equal(got, []string{"joker"}) {
		t.Fatalf("pending = %v", got)
	}
	if _, err := f.Module.Get(ctx, []byte("villain/joker")); err != nil {
		t.Fatalf("entity: %v", err)
	}
	if g, _ := der.Current(ctx); g == g0 {
		t.Fatal("MarkDirty's batch did not touch the index's generation")
	}
}

func mustCurrent(t *testing.T, f *indextest.Fixture, name string) storage.Generation {
	t.Helper()
	g, err := f.Derived(name).Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestIndexMarkDirtyTouchesOnlyItsIndex: marking an id of one index leaves the
// sibling index's generation alone.
func TestIndexMarkDirtyTouchesOnlyItsIndex(t *testing.T) {
	ctx := t.Context()
	f := openMem(t)
	cave0 := mustCurrent(t, f, indextest.Cave)
	b := f.Module.NewBatch()
	if err := f.DirtySet(indextest.Signal).Mark(ctx, b, "robin"); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if mustCurrent(t, f, indextest.Cave) != cave0 {
		t.Fatal("marking the signal index touched the cave index")
	}
	if got := pendingIDs(t, f.DirtySet(indextest.Cave)); len(got) != 0 {
		t.Fatalf("the cave index's dirty set holds %v", got)
	}
}

// TestIndexDirtySetSurvivesCrash: a committed mark is on disk after a crash
// that drops everything unsynced; a batch that never committed is not.
func TestIndexDirtySetSurvivesCrash(t *testing.T) {
	ctx := t.Context()
	mem := vfs.NewCrashableMem()
	f := indextest.Open(t, storage.FSOf(mem), dbPath)
	d := f.DirtySet(indextest.Signal)

	committed := f.Module.NewBatch()
	if err := d.Mark(ctx, committed, "oracle"); err != nil {
		t.Fatal(err)
	}
	if err := committed.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	staged := f.Module.NewBatch()
	if err := d.Mark(ctx, staged, "bane"); err != nil {
		t.Fatal(err)
	}
	clone := mem.CrashClone(vfs.CrashCloneCfg{})
	_ = staged.Close()

	g := indextest.Open(t, storage.FSOf(clone), dbPath)
	got := pendingIDs(t, g.DirtySet(indextest.Signal))
	if !slices.Equal(got, []string{"oracle"}) {
		t.Fatalf("after the crash pending = %v, want [oracle]", got)
	}
}

// TestDirtySetClearKeepsAReMark: Clear removes a mark only while it holds the
// token the reconciler read; a re-mark that committed in between survives.
func TestDirtySetClearKeepsAReMark(t *testing.T) {
	ctx := t.Context()
	f := openMem(t)
	d := f.DirtySet(indextest.Signal)
	mark := func(id string) {
		t.Helper()
		b := f.Module.NewBatch()
		if err := d.Mark(ctx, b, id); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
	}
	mark("alfred")
	ms, err := d.Pending(ctx, 0)
	if err != nil || len(ms) != 1 {
		t.Fatalf("pending %v %v", ms, err)
	}
	read := ms[0]

	mark("alfred") // the entity changed again after the reconciler read the mark
	cleared, err := d.Clear(ctx, read)
	if err != nil {
		t.Fatal(err)
	}
	if cleared {
		t.Fatal("Clear removed a mark that was re-marked after it was read")
	}
	now, _ := d.Pending(ctx, 0)
	if len(now) != 1 || bytes.Equal(now[0].Token, read.Token) {
		t.Fatalf("re-mark lost or token reused: %v vs %x", now, read.Token)
	}
	cleared, err = d.Clear(ctx, now[0])
	if err != nil || !cleared {
		t.Fatalf("Clear of the current token = %v, %v", cleared, err)
	}
	if n, _ := d.Count(ctx); n != 0 {
		t.Fatalf("count after clear = %d", n)
	}
}

// TestDirtySetConflictsAreRetryable: two batches that mark one id cannot both
// commit blind; the loser fails with ErrConflict and succeeds on retry.
func TestDirtySetConflictsAreRetryable(t *testing.T) {
	ctx := t.Context()
	f := openMem(t)
	d := f.DirtySet(indextest.Signal)
	a, b := f.Module.NewBatch(), f.Module.NewBatch()
	for _, x := range []*storage.Batch{a, b} {
		if err := d.Mark(ctx, x, "gordon"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("second concurrent mark of one id: %v, want ErrConflict", err)
	}
	err := storage.RetryOnConflict(ctx, 3, func(ctx context.Context) error {
		x := f.Module.NewBatch()
		defer x.Close()
		if err := d.Mark(ctx, x, "gordon"); err != nil {
			return err
		}
		return x.Commit(ctx, storage.Sync)
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// TestDirtySetIDsAreOpaque: an id with separators, NULs and non-UTF-8 bytes
// round-trips through the key encoding.
func TestDirtySetIDsAreOpaque(t *testing.T) {
	ctx := t.Context()
	f := openMem(t)
	d := f.DirtySet(indextest.Signal)
	ids := []string{"a/b", "gotham\x00city", "\xff\xfe", "x\x01y", "ünï"}
	for _, id := range ids {
		b := f.Module.NewBatch()
		if err := d.Mark(ctx, b, id); err != nil {
			t.Fatalf("%q: %v", id, err)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			t.Fatalf("%q: %v", id, err)
		}
	}
	got := pendingIDs(t, d)
	slices.Sort(got)
	want := slices.Clone(ids)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("pending %q, want %q", got, want)
	}
	if ms, _ := d.Pending(ctx, 2); len(ms) != 2 {
		t.Fatalf("Pending(2) = %d marks", len(ms))
	}
	b := f.Module.NewBatch()
	defer b.Close()
	if err := d.Mark(ctx, b, ""); err == nil {
		t.Fatal("an empty id was marked")
	}
}

func TestNewDirtySetRefusesAForeignKeyspace(t *testing.T) {
	f := openMem(t)
	if _, err := index.NewDirtySet(f.Module, f.Derived(indextest.Signal), "gotham", "signal"); err == nil {
		t.Fatal("a dirty set over a module keyspace was built")
	}
	if _, err := index.NewDirtySet(f.Root, f.Derived(indextest.Signal), "Gotham", "signal"); err == nil {
		t.Fatal("an invalid module name was accepted")
	}
}
