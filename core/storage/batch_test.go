// file: core/storage/batch_test.go
// version: 1.0.0
// guid: c1fd20f4-e973-4127-8a09-c47f9835a7e6
// last-edited: 2026-11-07

package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
)

func atomicOwners() []OwnerSpec {
	return []OwnerSpec{
		{Owner: "wayne", Spec: wayneSpec(), Enabled: true},
		{Owner: "core/outbox", Spec: Spec{Keyspace: true}, Enabled: true},
		{Owner: "gotham", Spec: Spec{Claims: gothamClaims()}, Enabled: true},
	}
}

// TestBatchAtomicAcrossBoundKeyspaces (05-storage §5S.10): a module row, an
// outbox entry and a legacy row commit as one batch; a crash before the
// apply leaves none of them and a crash after leaves all of them.
func TestBatchAtomicAcrossBoundKeyspaces(t *testing.T) {
	ctx := context.Background()
	for _, crashBefore := range []bool{true, false} {
		t.Run(fmt.Sprintf("crashBefore=%v", crashBefore), func(t *testing.T) {
			fs := vfs.NewCrashableMem()
			e := newEnvFS(t, fs, atomicOwners()...)
			db := e.open()
			ks, _ := db.Keyspace("wayne")
			outbox, _ := db.Keyspace("core/outbox")
			lr, _ := db.Legacy("gotham")
			b := ks.NewBatch().Bind(outbox).Bind(lr)
			if err := b.Set([]byte("row"), []byte("alfred")); err != nil {
				t.Fatal(err)
			}
			if err := b.In(outbox).Set([]byte("evt"), []byte("1")); err != nil {
				t.Fatal(err)
			}
			if err := b.In(lr).Set([]byte("joker:1"), []byte("x")); err != nil {
				t.Fatal(err)
			}
			if got := b.Participants(); !slices.Equal(got, []Owner{"wayne", "core/outbox", "gotham"}) {
				t.Fatalf("participants = %v", got)
			}
			var clone *vfs.MemFS
			if crashBefore {
				db.hooks.beforeApply = crashOnce(fs, &clone)
			}
			if err := b.Commit(ctx, Sync); err != nil {
				t.Fatal(err)
			}
			if !crashBefore {
				clone = fs.CrashClone(vfs.CrashCloneCfg{})
			}
			var got []string
			for _, p := range []string{"m/wayne/", "c/outbox/", "joker:"} {
				got = append(got, rawKeys(t, clone, e.path, p)...)
			}
			if crashBefore && len(got) != 0 {
				t.Fatalf("crash before the apply left %v", got)
			}
			if !crashBefore && len(got) != 3 {
				t.Fatalf("crash after the apply left %v, want all three", got)
			}
		})
	}
	// A write through a handle that is not bound poisons the batch.
	db := newEnv(t, atomicOwners()...).open()
	ks, _ := db.Keyspace("wayne")
	outbox, _ := db.Keyspace("core/outbox")
	b := ks.NewBatch()
	if err := b.stage(outbox, opSet, []byte("evt"), nil); !errors.Is(err, ErrOutsideKeyspace) {
		t.Fatalf("unbound write = %v", err)
	}
	if err := b.Commit(ctx, Sync); !errors.Is(err, ErrOutsideKeyspace) {
		t.Fatalf("poisoned commit = %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("In of an unbound handle did not panic")
			}
		}()
		ks.NewBatch().In(outbox)
	}()
}

func serializedOwners() []OwnerSpec {
	return []OwnerSpec{
		{Owner: "wayne", Spec: Spec{Keyspace: true, Serialized: []string{"ledger/"}}, Enabled: true},
		{Owner: "core/tasks", Spec: Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
		{Owner: "gotham", Spec: Spec{Claims: gothamClaims()}, Enabled: true},
	}
}

// TestExpectConflictAppliesNothing (05-storage §5S.10, -race): two writers
// read the same Serialized key; one commits, the other gets ErrConflict and
// applies nothing; concurrent read-modify-write loses no update.
func TestExpectConflictAppliesNothing(t *testing.T) {
	ctx := context.Background()
	db := newEnv(t, serializedOwners()...).open()
	ks, _ := db.Keyspace("core/tasks")
	if err := put(ctx, ks, "n", "0"); err != nil {
		t.Fatal(err)
	}
	b1, b2 := ks.NewBatch(), ks.NewBatch()
	for _, b := range []*Batch{b1, b2} {
		if _, err := b.GetForUpdate(ctx, ks, []byte("n")); err != nil {
			t.Fatal(err)
		}
		if err := b.Set([]byte("n"), []byte("1")); err != nil {
			t.Fatal(err)
		}
	}
	if err := b2.Expect(ks, []byte("side"), nil); err != nil {
		t.Fatal(err)
	}
	if err := b2.Set([]byte("side"), []byte("effect")); err != nil {
		t.Fatal(err)
	}
	if err := b1.Commit(ctx, Sync); err != nil {
		t.Fatal(err)
	}
	mustErrIs(t, b2.Commit(ctx, Sync), ErrConflict)
	if ok, _ := ks.Has(ctx, []byte("side")); ok {
		t.Fatal("the conflicting batch applied a write")
	}
	// Concurrent increments with RetryOnConflict: no lost update.
	const workers, each = 8, 25
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range each {
				err := RetryOnConflict(ctx, 10_000, func(ctx context.Context) error {
					b := ks.NewBatch()
					defer func() { _ = b.Close() }()
					v, err := b.GetForUpdate(ctx, ks, []byte("n"))
					if err != nil {
						return err
					}
					n, _ := strconv.Atoi(string(v))
					if err := b.Set([]byte("n"), []byte(strconv.Itoa(n+1))); err != nil {
						return err
					}
					return b.Commit(ctx, NoSync)
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if v, _ := ks.Get(ctx, []byte("n")); string(v) != strconv.Itoa(1+workers*each) {
		t.Fatalf("counter = %s, want %d", v, 1+workers*each)
	}
}

// TestLeakedBatchBlocksNobody (05-storage §5S.10): a batch with
// preconditions that is never committed or closed holds no stripe and no
// lock; other commits on the same key proceed.
func TestLeakedBatchBlocksNobody(t *testing.T) {
	ctx := context.Background()
	db := newEnv(t, serializedOwners()...).open()
	ks, _ := db.Keyspace("core/tasks")
	leaked := ks.NewIndexedBatch()
	if _, err := leaked.GetForUpdate(ctx, ks, []byte("n")); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := leaked.Set([]byte("n"), []byte("leaked")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		b := ks.NewBatch()
		if err := b.Expect(ks, []byte("n"), nil); err != nil {
			done <- err
			return
		}
		if err := b.Set([]byte("n"), []byte("other")); err != nil {
			done <- err
			return
		}
		done <- b.Commit(ctx, Sync)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a leaked batch blocked another commit")
	}
	// The leaked batch now conflicts instead of overwriting.
	mustErrIs(t, leaked.Commit(ctx, Sync), ErrConflict)
}

// TestSerializedKeyspaceRefusesBlindWrite (05-storage §5S.10, R6-1): a Set
// or Delete in a Serialized sub-prefix of a keyspace without an Expect on
// its key fails with ErrBlindWrite, a DeleteRange there too; a write with
// its Expect commits; a Sequence refill in a Serialized range commits.
func TestSerializedKeyspaceRefusesBlindWrite(t *testing.T) {
	ctx := context.Background()
	db := newEnv(t, serializedOwners()...).open()
	ks, _ := db.Keyspace("wayne")
	ledger := ks.Sub("ledger")
	mustErrIs(t, ledger.Set(ctx, []byte("gold"), []byte("1")), ErrBlindWrite)
	mustErrIs(t, ledger.Delete(ctx, []byte("gold")), ErrBlindWrite)
	mustErrIs(t, ks.DeleteRange(ctx, nil, nil), ErrBlindWrite)
	if err := ks.Set(ctx, []byte("cave"), []byte("1")); err != nil {
		t.Fatalf("a write outside the Serialized range: %v", err)
	}
	b := ledger.NewBatch()
	if err := b.Expect(ledger, []byte("gold"), nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Set([]byte("gold"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, Sync); err != nil {
		t.Fatal(err)
	}
	mustErrIs(t, ks.NewBatch().Expect(ks, []byte("cave"), nil), ErrNotSerialized)
	tasks, _ := db.Keyspace("core/tasks")
	for i := range 100 {
		if _, err := tasks.Sequence("ids").Next(ctx); err != nil {
			t.Fatalf("Sequence in a Serialized keyspace, id %d: %v", i, err)
		}
	}
	// A legacy claim takes stripes but never refuses a blind write (A-3).
	lr, _ := db.Legacy("gotham")
	if err := lr.Set(ctx, []byte("joker_counter"), []byte("7")); err != nil {
		t.Fatal(err)
	}
}

// TestUnserializedBulkWriteTakesNoStripes (05-storage §5S.10, DATA8-2): a
// 100 000-key commit into a non-Serialized legacy family takes no stripe, so
// it completes while every stripe is held, and does not delay a Serialized
// commit.
func TestUnserializedBulkWriteTakesNoStripes(t *testing.T) {
	ctx := context.Background()
	db := newEnv(t, serializedOwners()...).open()
	var took atomic.Int64
	db.hooks.stripes = func(n int) { took.Add(int64(n)) }
	lr, _ := db.Legacy("gotham")
	b := lr.NewBatch()
	for i := range 100_000 {
		if err := b.Set(fmt.Appendf(nil, "joker:%06d", i), []byte("ha")); err != nil {
			t.Fatal(err)
		}
	}
	unlock := db.commitStripes(nil, true)
	took.Store(0)
	done := make(chan error, 1)
	go func() { done <- b.Commit(ctx, NoSync) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the bulk commit waited for a stripe")
	}
	unlock()
	if n := took.Load(); n != 0 {
		t.Fatalf("bulk commit took %d stripes", n)
	}
	tasks, _ := db.Keyspace("core/tasks")
	sb := tasks.NewBatch()
	if err := sb.Expect(tasks, []byte("k"), nil); err != nil {
		t.Fatal(err)
	}
	if err := sb.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := sb.Commit(ctx, Sync); err != nil {
		t.Fatal(err)
	}
	if n := took.Load(); n != 1 {
		t.Fatalf("a Serialized commit took %d stripes, want 1", n)
	}
}

// TestSequenceNeverReusesAfterCrash (05-storage §5S.10): a crash at every
// point between a refill and the commits that consume its IDs, the refill's
// own commit included, never lets a later boot reissue an ID.
func TestSequenceNeverReusesAfterCrash(t *testing.T) {
	ctx := context.Background()
	for crashAt := range 12 {
		for _, inRefill := range []bool{false, true} {
			t.Run(fmt.Sprintf("after=%d/inRefill=%v", crashAt, inRefill), func(t *testing.T) {
				fs := vfs.NewCrashableMem()
				owners := []OwnerSpec{{Owner: "wayne", Spec: wayneSpec(), Enabled: true}}
				e := newEnvFS(t, fs, owners...)
				e.opts.SequenceLease = 4
				db := e.open()
				ks, _ := db.Keyspace("wayne")
				seq := ks.Sequence("ids")
				var issued []uint64
				for range crashAt {
					id, err := seq.Next(ctx)
					if err != nil {
						t.Fatal(err)
					}
					issued = append(issued, id)
					if err := ks.Set(ctx, fmt.Appendf(nil, "row/%d", id), nil); err != nil {
						t.Fatal(err)
					}
				}
				var clone *vfs.MemFS
				if inRefill {
					// Crash inside the next refill's commit, before it applies.
					db.hooks.beforeApply = crashOnce(fs, &clone)
					for clone == nil {
						id, err := seq.Next(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if clone == nil {
							issued = append(issued, id)
						}
					}
				} else {
					clone = fs.CrashClone(vfs.CrashCloneCfg{})
				}
				after := newEnvFS(t, clone, owners...)
				after.path = e.path
				after.opts.Path = e.path
				db2 := after.open()
				ks2, _ := db2.Keyspace("wayne")
				for range 10 {
					id, err := ks2.Sequence("ids").Next(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if slices.Contains(issued, id) || id == 0 {
						t.Fatalf("id %d reissued after the crash (issued before: %v)", id, issued)
					}
				}
			})
		}
	}
}

// TestSequenceAdvancePast (05-storage §5S.10, DATA2-16): after AdvancePast(n)
// every ID is greater than n, also across a crash.
func TestSequenceAdvancePast(t *testing.T) {
	ctx := context.Background()
	fs := vfs.NewCrashableMem()
	owners := []OwnerSpec{{Owner: "wayne", Spec: wayneSpec(), Enabled: true}}
	e := newEnvFS(t, fs, owners...)
	db := e.open()
	ks, _ := db.Keyspace("wayne")
	seq := ks.Sequence("ids")
	if id, err := seq.Next(ctx); err != nil || id != 1 {
		t.Fatalf("first id = %d, %v", id, err)
	}
	if err := seq.AdvancePast(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	if id, _ := seq.Next(ctx); id <= 1000 {
		t.Fatalf("id after AdvancePast(1000) = %d", id)
	}
	// AdvancePast never moves the mark backwards.
	if err := seq.AdvancePast(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if id, _ := seq.Next(ctx); id <= 1000 {
		t.Fatalf("id after AdvancePast(5) = %d", id)
	}
	clone := fs.CrashClone(vfs.CrashCloneCfg{})
	after := newEnvFS(t, clone, owners...)
	after.path, after.opts.Path = e.path, e.path
	db2 := after.open()
	ks2, _ := db2.Keyspace("wayne")
	if id, err := ks2.Sequence("ids").Next(ctx); err != nil || id <= 1000 {
		t.Fatalf("id after a crash = %d, %v", id, err)
	}
}

func put(ctx context.Context, ks Keyspace, k, v string) error {
	b := ks.NewBatch()
	if err := b.Expect(ks, []byte(k), nil); err != nil && !errors.Is(err, ErrNotSerialized) {
		return err
	}
	if err := b.Set([]byte(k), []byte(v)); err != nil {
		return err
	}
	return b.Commit(ctx, Sync)
}
