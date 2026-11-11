// file: core/storage/rewrite_test.go
// version: 1.0.0
// guid: 61b37f0c-b4a2-43ad-be51-e6dc6d233e3f
// last-edited: 2026-11-11

package storage

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// m8Step is a wayne M8 (DataStep.Rewrite) whose Run is run.
func m8Step(run func(ctx context.Context, db *DB) error) DataStep {
	return DataStep{Owner: "wayne", ID: "wayne.rewrite", Cutover: true, Rewrite: true, Run: run}
}

// copyOwed is the bytes P4a's free-space refusal says it owes M8's copy.
func copyOwed(t *testing.T, err error) uint64 {
	t.Helper()
	mustErrIs(t, err, ErrCheckpointFreeSpace)
	m := regexp.MustCompile(`the store rewrite's copy (\d+)\)`).FindStringSubmatch(err.Error())
	if m == nil {
		t.Fatalf("the refusal does not name M8's copy: %v", err)
	}
	n, perr := strconv.ParseUint(m[1], 10, 64)
	if perr != nil {
		t.Fatal(perr)
	}
	return n
}

// TestCheckpointHeadroomOwesM8sCopyOnlyWhilePending (05-storage §5S.3.12
// "Free space", §5S.3.13 phase 2): P4a's headroom adds the store's size for
// M8's copy while M8's record on the live store is pending, and owes nothing
// for it once the store reads copied: that store is the copy, swapped in by
// a boot killed before phase 4, and its bytes are spent. The copy carries
// copied (RewriteCopy); the original never does.
func TestCheckpointHeadroomOwesM8sCopyOnlyWhilePending(t *testing.T) {
	ctx := context.Background()
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.DataSteps = []DataStep{m8Step(func(context.Context, *DB) error { return errors.New("must not run") })}
	e.opts.freeSpace = func(string) (uint64, error) { return 1, nil }
	_, err := e.tryOpen()
	if got := copyOwed(t, err); got == 0 {
		t.Errorf("with M8's record pending the headroom owes its copy nothing: %v", err)
	}

	e.opts.DataSteps = nil
	e.opts.freeSpace = func(string) (uint64, error) { return 1 << 50, nil }
	db := e.open()
	if err := db.SetRewriteRecord(ctx, "wayne.rewrite", RewriteCopied); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.DataSteps = []DataStep{m8Step(func(context.Context, *DB) error { return errors.New("must not run") })}
	e.opts.freeSpace = func(string) (uint64, error) { return 1, nil }
	_, err = e.tryOpen()
	if got := copyOwed(t, err); got != 0 {
		t.Errorf("a store at copied (the copy, swapped in) still owes M8's copy %d bytes: %v", got, err)
	}
}

// identities is a dirIdentity seam: each path's identity, settable.
type identities struct {
	mu sync.Mutex
	m  map[string][2]uint64
}

func (i *identities) set(path string, fsid, ino uint64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.m[path] = [2]uint64{fsid, ino}
}

func (i *identities) of(path string) (uint64, uint64, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	id, ok := i.m[path]
	if !ok {
		return 0, 0, errors.New("no identity for " + path)
	}
	return id[0], id[1], nil
}

// attemptRecordOf is the boot's attempt checkpoint record.
func attemptRecordOf(t *testing.T, db *DB) checkpointRecord {
	t.Helper()
	recs, err := db.checkpointRecords()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.name == db.AttemptCheckpoint() {
			return r.rec
		}
	}
	t.Fatalf("no record for the attempt's checkpoint %q among %d", db.AttemptCheckpoint(), len(recs))
	return checkpointRecord{}
}

// TestRewriteSwapMovesTheAttemptRecordToTheCopy (05-storage §5S.3.13 phase
// 3, §5S.3.12 "Sources"): RewriteSwap stages the copy's directory identity
// as the attempt record's next identity before anything moves, so a kill
// after the copy moves into place still finds the record of this store
// (ofStore), and after the swap promotes it to the record's own identity,
// the epoch unchanged; the boot's generation takes the new identity and is
// no longer owed at step 9 (the copy holds it). The swap here only moves
// the identity (the dirIdentity seam), since MemFS has none.
func TestRewriteSwapMovesTheAttemptRecordToTheCopy(t *testing.T) {
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	ids := &identities{m: map[string][2]uint64{e.path: {11, 1}, RewriteDir(e.path): {11, 2}}}
	e.opts.dirIdentity = ids.of
	var before, during, after checkpointRecord
	var epochNewAfter bool
	var epochAfter epochRecord
	e.opts.DataSteps = []DataStep{m8Step(func(ctx context.Context, db *DB) error {
		before = attemptRecordOf(t, db)
		err := db.RewriteSwap(ctx, func(context.Context) error {
			during = attemptRecordOf(t, db)
			ids.set(e.path, 11, 2) // the copy, renamed into place, keeps its inode
			return nil
		}, func(context.Context, error) error {
			t.Error("a swap whose copy reopens was swapped back")
			return nil
		})
		if err != nil {
			return err
		}
		after = attemptRecordOf(t, db)
		epochNewAfter, epochAfter = db.boot.epochNew, db.boot.epoch
		return markDone(ctx, db, "wayne.rewrite")
	})}
	e.open()
	if before.DirIno != 1 || before.NextDirFSID != 0 || before.NextDirIno != 0 {
		t.Errorf("the record before the swap = %+v, want the original's identity and no next one", before)
	}
	if during.DirIno != 1 || during.NextDirFSID != 11 || during.NextDirIno != 2 || during.Epoch != before.Epoch {
		t.Errorf("the record while the swap runs = %+v, want the copy's identity staged as next, epoch %d", during, before.Epoch)
	}
	if after.DirFSID != 11 || after.DirIno != 2 || after.NextDirFSID != 0 || after.NextDirIno != 0 || after.Epoch != before.Epoch {
		t.Errorf("the record after the swap = %+v, want it promoted to the copy's identity, epoch %d", after, before.Epoch)
	}
	if epochNewAfter {
		t.Error("after the swap the boot still owes its generation at step 9; the copy holds it")
	}
	if epochAfter.DirFSID != 11 || epochAfter.DirIno != 2 || epochAfter.Epoch != before.Epoch {
		t.Errorf("the boot's generation after the swap = %+v, want epoch %d with the copy's identity", epochAfter, before.Epoch)
	}
}

// TestCheckpointRecordOfStore (§5S.3.12 "Sources"): a record is of the
// store when the epoch matches and the store's identity is the record's own
// or, after M8 staged it, its next one; an unset next identity matches
// nothing.
func TestCheckpointRecordOfStore(t *testing.T) {
	rec := checkpointRecord{Epoch: 3, DirFSID: 11, DirIno: 1}
	staged := rec
	staged.NextDirFSID, staged.NextDirIno = 11, 2
	for name, c := range map[string]struct {
		rec  checkpointRecord
		e    epochRecord
		want bool
	}{
		"own identity":            {rec, epochRecord{Epoch: 3, DirFSID: 11, DirIno: 1}, true},
		"another epoch":           {rec, epochRecord{Epoch: 4, DirFSID: 11, DirIno: 1}, false},
		"another directory":       {rec, epochRecord{Epoch: 3, DirFSID: 11, DirIno: 2}, false},
		"the staged copy":         {staged, epochRecord{Epoch: 3, DirFSID: 11, DirIno: 2}, true},
		"the staged copy, epoch":  {staged, epochRecord{Epoch: 4, DirFSID: 11, DirIno: 2}, false},
		"no next on a zero store": {rec, epochRecord{Epoch: 3}, false},
	} {
		if got := c.rec.ofStore(c.e); got != c.want {
			t.Errorf("%s: ofStore = %t, want %t", name, got, c.want)
		}
	}
}
