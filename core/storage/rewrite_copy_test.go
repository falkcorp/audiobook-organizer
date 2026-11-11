// file: core/storage/rewrite_copy_test.go
// version: 1.0.0
// guid: 2d6f8278-89b0-4079-b5c9-33a54b18bc24
// last-edited: 2026-11-11

package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// copyIdentities is the dirIdentity seam for a copy on MemFS, which has no
// inodes: the live store and the copy's two directories get distinct
// identities.
func copyIdentities(e *env) *identities {
	return &identities{m: map[string][2]uint64{
		e.path:                         {11, 1},
		RewriteDir(e.path) + ".tmp":    {11, 2},
		RewriteDir(e.path):             {11, 2},
		RewriteDir(e.path) + ".ingest": {11, 3},
	}}
}

// TestRewriteCopyCarriesEveryKeyAndRecordsCopied (05-storage §5S.3.13 phase
// 2): after RewriteCompact, RewriteCopy writes every live key of the store
// into <db>.rewrite, the copy holds M8's record at copied while the live
// store's stays pending, c/storage/epoch carries the copy's directory
// identity, the temporary and ingest directories are gone, and a second copy
// replaces the first (an earlier attempt's final is removed first) with the
// same counts.
func TestRewriteCopyCarriesEveryKeyAndRecordsCopied(t *testing.T) {
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	e.opts.dirIdentity = copyIdentities(e).of
	final := RewriteDir(e.path)
	var first, second RewriteCopyStats
	var liveRecord string
	e.opts.DataSteps = []DataStep{m8Step(func(ctx context.Context, db *DB) error {
		if err := db.RewriteCompact(ctx); err != nil {
			return err
		}
		var err error
		if first, err = db.RewriteCopy(ctx, final, "wayne.rewrite"); err != nil {
			return err
		}
		if second, err = db.RewriteCopy(ctx, final, "wayne.rewrite"); err != nil {
			return err
		}
		if liveRecord, err = db.RewriteRecord(ctx, "wayne.rewrite"); err != nil {
			return err
		}
		return markDone(ctx, db, "wayne.rewrite")
	})}
	e.open()

	if first.Keys == 0 || first.Bytes == 0 || first.Tables != 1 {
		t.Errorf("first copy stats = %+v, want keys, bytes and one table", first)
	}
	if first != second {
		t.Errorf("second copy stats = %+v, want the first's %+v", second, first)
	}
	if liveRecord != "" {
		t.Errorf("the live store's M8 record = %q after the copy, want it still pending", liveRecord)
	}
	for _, leftover := range []string{final + ".tmp", final + ".ingest"} {
		if _, err := e.fs.Stat(leftover); err == nil {
			t.Errorf("%s survived a finished copy", leftover)
		}
	}
	got := rawKeys(t, e.fs, final, "joker:")
	if len(got) != 2 || got[0] != "joker:1" || got[1] != "joker:2" {
		t.Errorf("the copy's joker rows = %v, want joker:1 and joker:2", got)
	}
	cp, err := cpebble.Open(final, &cpebble.Options{FS: e.fs, ReadOnly: true, Logger: quiet{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cp.Close() }()
	v, closer, err := cp.Get([]byte("joker:2"))
	if err != nil || string(v) != "haha" {
		t.Errorf("the copy's joker:2 = %q, %v; want haha", v, err)
	}
	if closer != nil {
		_ = closer.Close()
	}
	var m8 int
	it, err := cp.NewIter(nil)
	if err != nil {
		t.Fatal(err)
	}
	for ok := it.First(); ok; ok = it.Next() {
		if strings.Contains(string(it.Key()), "wayne.rewrite") {
			rec, derr := DataMigrationCodec().Decode(it.Value())
			if derr != nil || rec.State != RewriteCopied || rec.Build != "batcomputer-1" {
				t.Errorf("the copy's M8 record = %+v, %v; want state copied, build batcomputer-1", rec, derr)
			}
			m8++
		}
	}
	_ = it.Close()
	if m8 != 1 {
		t.Errorf("the copy holds %d M8 records, want 1", m8)
	}
}

// TestRewriteCopyRefusals: the copy needs a writable boot (a read-only open
// is refused), stops on a cancelled context before writing a table, and a
// copy whose directory has no identity (the seam fails) is refused and
// leaves no final directory behind.
func TestRewriteCopyRefusals(t *testing.T) {
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	ro := e.opts
	ro.ReadOnly, ro.MustExist = true, true
	o, err := Open(context.Background(), ro)
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.db.RewriteCopy(context.Background(), RewriteDir(e.path), "wayne.rewrite")
	if err == nil || !strings.Contains(err.Error(), "writable boot") {
		t.Errorf("a read-only copy = %v, want the writable-boot refusal", err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}

	final := RewriteDir(e.path)
	var cancelled, noIdentity error
	e.opts.dirIdentity = copyIdentities(e).of
	e.opts.DataSteps = []DataStep{m8Step(func(ctx context.Context, db *DB) error {
		dead, cancel := context.WithCancel(ctx)
		cancel()
		_, cancelled = db.RewriteCopy(dead, final, "wayne.rewrite")
		db.boot.opts.dirIdentity = func(string) (uint64, uint64, error) { return 0, 0, errors.New("no inode here") }
		_, noIdentity = db.RewriteCopy(ctx, final, "wayne.rewrite")
		return markDone(ctx, db, "wayne.rewrite")
	})}
	e.open()
	if !errors.Is(cancelled, context.Canceled) {
		t.Errorf("a copy under a cancelled context = %v, want context.Canceled", cancelled)
	}
	if noIdentity == nil || !strings.Contains(noIdentity.Error(), "identity") || !strings.Contains(noIdentity.Error(), "no inode here") {
		t.Errorf("a copy with no directory identity = %v, want the identity refusal", noIdentity)
	}
	if _, err := e.fs.Stat(final); err == nil {
		t.Errorf("a refused copy left %s behind", final)
	}
}

// TestRewriteRecordStates: M8's record reads "" before it exists, the state
// SetRewriteRecord committed after, and a record that does not decode is an
// error naming M8 and the ID, not a silent pending.
func TestRewriteRecordStates(t *testing.T) {
	ctx := context.Background()
	db := headStore(t, vfs.NewMem(), cpOwners()...).open()
	if got, err := db.RewriteRecord(ctx, "wayne.rewrite"); err != nil || got != "" {
		t.Fatalf("a missing record = %q, %v; want empty", got, err)
	}
	if err := db.SetRewriteRecord(ctx, "wayne.rewrite", RewriteCopied); err != nil {
		t.Fatal(err)
	}
	if got, err := db.RewriteRecord(ctx, "wayne.rewrite"); err != nil || got != RewriteCopied {
		t.Fatalf("the committed record = %q, %v; want %q", got, err, RewriteCopied)
	}
	if err := db.storageKS().Set(ctx, DataMigrationKey("wayne.bad"), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RewriteRecord(ctx, "wayne.bad"); err == nil || !strings.Contains(err.Error(), "M8 record wayne.bad") {
		t.Errorf("an undecodable record = %v, want an error naming M8 and wayne.bad", err)
	}
}

// TestRewriteStepDeclaration: NewRewriteStep keeps the owner, the one ID
// and the steps it runs after, and RewriteLabels names the six labels of
// M8's storage half, each once.
func TestRewriteStepDeclaration(t *testing.T) {
	after := StepRef{Owner: "wayne", DataMigration: "wayne.import"}
	s := NewRewriteStep("wayne", "wayne.rewrite", after)
	if s.Owner != "wayne" || s.ID != "wayne.rewrite" || len(s.After) != 1 || s.After[0] != after {
		t.Errorf("NewRewriteStep = %+v", s)
	}
	seen := map[string]bool{}
	for _, l := range RewriteLabels() {
		if !strings.HasPrefix(l, "storage.rewrite.") || seen[l] {
			t.Errorf("label %q is not a unique storage.rewrite.* label", l)
		}
		seen[l] = true
	}
	if len(seen) != 6 {
		t.Errorf("%d labels, want 6", len(seen))
	}
}

// TestRewriteSwapRefusesOutsideAWritableMigrate: the swap runs inside a
// writable boot's Migrate only; a finished boot's DB and a read-only open are
// refused before anything is staged or closed, and the store keeps serving.
func TestRewriteSwapRefusesOutsideAWritableMigrate(t *testing.T) {
	ctx := context.Background()
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	db := e.open()
	err := db.RewriteSwap(ctx, func(context.Context) error { t.Error("swap ran"); return nil }, func(context.Context, error) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "inside a writable boot's Migrate") {
		t.Errorf("a swap on a finished boot = %v, want the Migrate-only refusal", err)
	}
	if _, err := db.RewriteRecord(ctx, "wayne.rewrite"); err != nil {
		t.Errorf("the refused swap closed the store: %v", err)
	}
	if got := db.Path(); got != e.path {
		t.Errorf("Path = %q, want %q", got, e.path)
	}
	if (&DB{}).AttemptCheckpoint() != "" || (&DB{}).buildID() != "" {
		t.Error("a DB with no boot reports an attempt checkpoint or a build")
	}
	if db.AttemptCheckpoint() == "" {
		t.Error("a boot that checkpointed reports no attempt checkpoint")
	}
	if db.buildID() != "batcomputer-1" {
		t.Errorf("buildID = %q, want batcomputer-1", db.buildID())
	}
}

// TestRewriteSwapReturnsTheSwapsErrorAndServesTheOriginal: a swap that fails
// before moving anything returns its own error, the engine is reopened at
// the store's path so the DB serves the original again (nothing was lost),
// and the attempt record keeps the original's identity with the copy's only
// staged as next.
func TestRewriteSwapReturnsTheSwapsErrorAndServesTheOriginal(t *testing.T) {
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	ids := &identities{m: map[string][2]uint64{e.path: {11, 1}, RewriteDir(e.path): {11, 2}}}
	e.opts.dirIdentity = ids.of
	stuck := errors.New("the batmobile is stuck")
	var swapErr error
	var rec checkpointRecord
	var joker string
	e.opts.DataSteps = []DataStep{m8Step(func(ctx context.Context, db *DB) error {
		swapErr = db.RewriteSwap(ctx, func(context.Context) error { return stuck },
			func(context.Context, error) error {
				t.Error("a swap that failed before the reopen was swapped back")
				return nil
			})
		rec = attemptRecordOf(t, db)
		lr, ok := db.Legacy("gotham")
		if !ok {
			return errors.New("no gotham legacy range")
		}
		v, gerr := lr.Get(ctx, []byte("joker:1"))
		joker = string(v)
		if gerr != nil {
			return gerr
		}
		return markDone(ctx, db, "wayne.rewrite")
	})}
	e.open()
	mustErrIs(t, swapErr, stuck)
	if joker != "ha" {
		t.Errorf("after the failed swap joker:1 = %q, want the original's ha", joker)
	}
	if rec.DirIno != 1 || rec.NextDirFSID != 11 || rec.NextDirIno != 2 {
		t.Errorf("attempt record after the failed swap = %+v, want the original's identity with the copy's staged as next", rec)
	}
}
