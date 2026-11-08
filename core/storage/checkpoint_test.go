// file: core/storage/checkpoint_test.go
// version: 1.0.0
// guid: 77194a40-fc27-4829-9543-097e945b3bdd
// last-edited: 2026-11-08

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// headStore is an env over a store a pre-core build wrote: legacy rows only,
// no install marker, so the first core boot takes a checkpoint.
func headStore(t *testing.T, fs vfs.FS, owners ...OwnerSpec) *env {
	t.Helper()
	e := newEnvFS(t, fs, owners...)
	e.seedRaw("joker:1", "ha", "joker:2", "haha")
	return e
}

func cpOwners() []OwnerSpec {
	return []OwnerSpec{
		{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}},
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{step(1, "cave", "cave", "1")}}},
	}
}

// readRecord reads a checkpoint's <name>.json.
func readRecord(t *testing.T, e *env, name string) checkpointRecord {
	t.Helper()
	f, err := e.fs.Open(filepath.Join(cpDir(e), name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var rec checkpointRecord
	if err := json.NewDecoder(f).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func cpDir(e *env) string { return filepath.Join(filepath.Dir(e.path), ".mediacore-checkpoints") }

// failingOwners is cpOwners plus a wayne step 2 that always fails: an upgrade
// that leaves its checkpoint open.
func failingOwners() []OwnerSpec {
	return []OwnerSpec{
		{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}},
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
			step(1, "cave", "cave", "1"),
			{Version: 2, Name: "broken", Compat: Additive, Up: func(context.Context, *MigrationTx) error { return errors.New("bane broke it") }},
		}}},
	}
}

// fixedOwners is the build that repairs failingOwners' step 2.
func fixedOwners() []OwnerSpec {
	return []OwnerSpec{
		{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}},
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
			step(1, "cave", "cave", "1"), step(2, "fixed", "fixed", "1"),
		}}},
	}
}

// TestCheckpointPrecedesEveryWrite (05-storage §5S.10, R2-8): on an upgrade
// boot the checkpoint holds no c/ or m/ row the boot wrote: not the marker,
// the ledgers, the steps or their bookkeeping.
func TestCheckpointPrecedesEveryWrite(t *testing.T) {
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	db := e.open()
	name := db.CheckpointName()
	if name == "" {
		t.Fatal("an upgrade boot took no checkpoint")
	}
	copyDB := filepath.Join(cpDir(e), name, "db")
	if got := rawKeys(t, e.fs, copyDB, "c/"); len(got) != 0 {
		t.Fatalf("checkpoint holds core rows %v", got)
	}
	if got := rawKeys(t, e.fs, copyDB, "m/"); len(got) != 0 {
		t.Fatalf("checkpoint holds module rows %v", got)
	}
	if got := rawKeys(t, e.fs, copyDB, "joker:"); len(got) != 2 {
		t.Fatalf("checkpoint legacy rows = %v", got)
	}
	ks := db.storageKS()
	if ok, _ := ks.Has(context.Background(), installKey()); !ok {
		t.Fatal("the live store has no marker")
	}
	rec := readRecord(t, e, name)
	if rec.CompletedAt == nil || rec.DeleteAfter == nil || rec.DeleteAfter.Sub(*rec.CompletedAt) != checkpointRetention {
		t.Fatalf("a completed attempt's record = %+v", rec)
	}
	if p := readPointer(t, e); p.Name != name || p.State != "completed" {
		t.Fatalf("pointer = %+v", p)
	}
	// A boot with nothing to run takes none.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if db2 := e.open(); db2.CheckpointName() != "" {
		t.Fatal("a boot with nothing to run took a checkpoint")
	}
}

// TestCheckpointIncludesStateFiles (05-storage §5S.10, 14 E4): the
// checkpoint holds each existing state file with its resolved original path
// and SHA-256; a missing one is skipped; one that cannot be read halts boot
// before any write.
func TestCheckpointIncludesStateFiles(t *testing.T) {
	fs := vfs.NewMem()
	e := headStore(t, fs, cpOwners()...)
	stateDir := filepath.Join(filepath.Dir(e.path), "state")
	if err := fs.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) {
		f, err := fs.Create(p, vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	keyPath := filepath.Join(stateDir, ".encryption_key")
	gapPath := filepath.Join(filepath.Dir(e.path), "config.yaml")
	write(keyPath, "alfred-pennyworth")
	write(gapPath, "batsignal: on\n")
	e.opts.StateFiles = []StateFile{
		{Label: "encryption_key", Path: keyPath},
		{Label: "keyring", Path: filepath.Join(stateDir, "keyring")},
		{Label: "gap_fill", Path: gapPath},
	}
	db := e.open()
	rec := readRecord(t, e, db.CheckpointName())
	if len(rec.StateFiles) != 2 {
		t.Fatalf("state files = %+v, want encryption_key and gap_fill", rec.StateFiles)
	}
	for _, sf := range rec.StateFiles {
		f, err := fs.Open(filepath.Join(cpDir(e), db.CheckpointName(), "state", sf.Label))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		_, _ = io.Copy(h, f)
		_ = f.Close()
		if hex.EncodeToString(h.Sum(nil)) != sf.SHA256 || (sf.Path != keyPath && sf.Path != gapPath) {
			t.Fatalf("state file record %+v does not match its copy", sf)
		}
	}
	// A state file that cannot be read halts boot before any write.
	bad := headStore(t, vfs.NewMem(), cpOwners()...)
	badPath := filepath.Join(filepath.Dir(bad.path), "unreadable")
	if err := bad.fs.MkdirAll(badPath, 0o700); err != nil { // a directory where a file should be
		t.Fatal(err)
	}
	bad.opts.StateFiles = []StateFile{{Label: "encryption_key", Path: badPath}}
	if _, err := bad.tryOpen(); err == nil || !strings.Contains(err.Error(), "encryption_key") {
		t.Fatalf("boot with an unreadable state file = %v", err)
	}
	if got := rawKeys(t, bad.fs, bad.path, "c/"); len(got) != 0 {
		t.Fatalf("a halted boot wrote %v", got)
	}
}

// TestCheckpointFreeSpaceRefused (05-storage §5S.10, OPS-25): too little
// space for the store, the state files and the margin halts boot before any
// write, with the numbers.
func TestCheckpointFreeSpaceRefused(t *testing.T) {
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	e.opts.freeSpace = func(string) (uint64, error) { return 1 << 20, nil }
	_, err := e.tryOpen()
	mustErrIs(t, err, ErrCheckpointFreeSpace)
	if !strings.Contains(err.Error(), "margin 2147483648") {
		t.Fatalf("refusal lacks the numbers: %v", err)
	}
	if got := rawKeys(t, e.fs, e.path, "c/"); len(got) != 0 {
		t.Fatalf("a refused boot wrote %v", got)
	}
	if names, _ := e.fs.List(cpDir(e)); len(names) != 0 {
		t.Fatalf("a refused boot left %v in the checkpoint directory", names)
	}
}

// TestCheckpointSurvivesCrashLoop (05-storage §5S.10): five failing boots,
// some committing steps, one with a replaced binary, reuse one pristine
// checkpoint; .migration-checkpoint keeps naming it; nothing is deleted; the
// boot that finally completes closes it.
func TestCheckpointSurvivesCrashLoop(t *testing.T) {
	fs := vfs.NewMem()
	failing := failingOwners()
	e := headStore(t, fs, failing...)
	var first string
	for boot := range 5 {
		if boot == 3 {
			e.opts.Build = "batcomputer-2" // a replaced binary retries the upgrade
		}
		if _, err := e.tryOpen(); err == nil {
			t.Fatalf("boot %d succeeded", boot)
		}
		p := readPointer(t, e)
		if first == "" {
			first = p.Name
		}
		if p.Name != first || p.State != "open" {
			t.Fatalf("boot %d: pointer = %+v, want %s open", boot, p, first)
		}
	}
	names, _ := fs.List(cpDir(e))
	slices.Sort(names)
	if want := []string{first, first + ".json"}; !slices.Equal(names, want) {
		t.Fatalf("checkpoint directory = %v, want %v", names, want)
	}
	if got := rawKeys(t, fs, filepath.Join(cpDir(e), first, "db"), "c/"); len(got) != 0 {
		t.Fatalf("the reused checkpoint is not pristine: %v", got)
	}
	// The fixed build completes the attempt.
	e.opts.Owners = fixedOwners()
	db := e.open()
	if db.CheckpointName() != first {
		t.Fatalf("the completing boot used %q, want %q", db.CheckpointName(), first)
	}
	if rec := readRecord(t, e, first); rec.CompletedAt == nil {
		t.Fatal("the completed attempt's record has no completed_at")
	}
}

// TestPartialCheckpointSwept (05-storage §5S.10, DATA3-22): a checkpoint
// directory without a record, and a <name>.tmp, are swept at boot and never
// reused; a record without its directory is dropped; the scratch area stays.
// Only names in the checkpoint grammar (<UTC stamp>-<8 hex>) are touched:
// anything else in the directory is not the sweep's (review BLOCKER-4).
func TestPartialCheckpointSwept(t *testing.T) {
	fs := vfs.NewMem()
	e := headStore(t, fs, cpOwners()...)
	dir := cpDir(e)
	// catwoman's directory has no record; poison's is a partial .tmp.
	const catwoman, poison = "20260101T000000Z-ca7a0001", "20260102T000000Z-901500aa.tmp"
	for _, d := range []string{catwoman, poison, "scratch", "batcave"} {
		if err := fs.MkdirAll(filepath.Join(dir, d, "db"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// twoface's record has no directory; joker.json is not a record name.
	for _, n := range []string{"20260103T000000Z-2face002.json", "joker.json"} {
		f, err := fs.Create(filepath.Join(dir, n), vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	db := e.open()
	names, _ := fs.List(dir)
	slices.Sort(names)
	want := []string{db.CheckpointName(), db.CheckpointName() + ".json", "scratch", "batcave", "joker.json"}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("after the sweep: %v, want %v", names, want)
	}
	if db.CheckpointName() == catwoman {
		t.Fatal("a record-less checkpoint was reused")
	}
}

// TestCheckpointDirIsTheParent (review BLOCKER-4): core.storage.checkpoint_dir
// names the directory that holds .mediacore-checkpoints/, never the leaf, so
// setting it to the DB's own parent leaves the live store alone (it used to
// sweep main.pebble as a record-less checkpoint); and Open refuses, before
// any write, a checkpoint directory that is relative or overlaps the store.
func TestCheckpointDirIsTheParent(t *testing.T) {
	t.Run("db-parent", func(t *testing.T) {
		fs := vfs.NewMem()
		e := headStore(t, fs, cpOwners()...)
		e.opts.CheckpointDir = filepath.Dir(e.path)
		db := e.open()
		name := db.CheckpointName()
		if name == "" {
			t.Fatal("an upgrade boot took no checkpoint")
		}
		if _, err := fs.Stat(filepath.Join(filepath.Dir(e.path), ".mediacore-checkpoints", name+".json")); err != nil {
			t.Fatalf("the record is not under <checkpoint_dir>/.mediacore-checkpoints: %v", err)
		}
		g, _ := db.Legacy("gotham")
		if v, err := g.Get(context.Background(), []byte("joker:1")); err != nil || string(v) != "ha" {
			t.Fatalf("the live store after the boot: joker:1 = %q, %v", v, err)
		}
	})
	for _, tc := range []struct {
		name  string
		set   func(e *env)
		match func(error) bool
	}{
		{"inside-the-store", func(e *env) { e.opts.CheckpointDir = e.path },
			func(err error) bool { return errors.Is(err, ErrCheckpointDirOverlap) }},
		{"holds-the-store", func(e *env) {
			parent := filepath.Dir(e.path)
			e.path = filepath.Join(parent, ".mediacore-checkpoints", "main.pebble")
			e.opts.Path, e.opts.CheckpointDir = e.path, parent
		}, func(err error) bool { return errors.Is(err, ErrCheckpointDirOverlap) }},
		{"relative", func(e *env) { e.opts.CheckpointDir = "batcave" },
			func(err error) bool { return err != nil && strings.Contains(err.Error(), "not absolute") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := vfs.NewMem()
			e := newEnvFS(t, fs, cpOwners()...)
			tc.set(e)
			if _, err := e.tryOpen(); !tc.match(err) {
				t.Fatalf("Open = %v, want a refusal", err)
			}
			if _, err := fs.Stat(e.path); err == nil {
				t.Fatal("Open created the store before refusing")
			}
		})
	}
}

// TestFreshStoreBesideOpenRecordGetsNewInstallID (review BLOCKER-3): a store
// set aside (a reset's Stage, or main.pebble deleted by hand) leaves its
// checkpoints behind. The empty store that replaces it is a new install: it
// takes a new install ID, never the open record's, so its first upgrade takes
// its own checkpoint instead of adopting the old store's copy.
func TestFreshStoreBesideOpenRecordGetsNewInstallID(t *testing.T) {
	fs := vfs.NewMem()
	e := headStore(t, fs, failingOwners()...)
	if _, err := e.tryOpen(); err == nil {
		t.Fatal("the failing upgrade succeeded")
	}
	old := readPointer(t, e).Name
	oldRec := readRecord(t, e, old)
	if !oldRec.open() {
		t.Fatalf("the failed attempt's record = %+v, want open", oldRec)
	}
	// Set the store and its sidecars aside; the checkpoints stay.
	dir, base := filepath.Dir(e.path), filepath.Base(e.path)
	names, err := fs.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if strings.HasPrefix(n, base) {
			if err := fs.RemoveAll(filepath.Join(dir, n)); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.opts.Owners = cpOwners()
	op, err := Open(context.Background(), e.opts)
	if err != nil {
		t.Fatal(err)
	}
	if !op.Fresh() {
		t.Fatal("the set-aside store did not boot as a fresh install")
	}
	db, err := op.Prepare(context.Background())
	if err == nil {
		err = db.Migrate(context.Background())
	}
	if err != nil {
		_ = op.Close()
		t.Fatal(err)
	}
	fresh := db.boot.installID
	if fresh == "" || fresh == oldRec.InstallID {
		t.Fatalf("the fresh store's install ID = %q, want a new one (the open record has %q)", fresh, oldRec.InstallID)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.Owners = fixedOwners()
	db = e.open()
	got := db.CheckpointName()
	if got == "" || got == old {
		t.Fatalf("the fresh store's first upgrade used checkpoint %q, want a new one (not %q)", got, old)
	}
	if rec := readRecord(t, e, got); rec.InstallID != fresh {
		t.Fatalf("the new checkpoint's install_id = %q, want %q", rec.InstallID, fresh)
	}
	if rec := readRecord(t, e, old); rec.InstallID != oldRec.InstallID || !rec.open() {
		t.Fatalf("the old store's record changed: %+v", rec)
	}
}

// TestSwappedStoreDoesNotInheritOpenRecord (coordinator, after the review):
// a marker-less store whose upgrade failed leaves an open record. A different
// marker-less store swapped in at the same path (a restore from backup, a
// copy) lands on the same epoch number, since neither store has an epoch row
// yet. It must not adopt the record's install ID or reuse its checkpoint:
// restoring that checkpoint would bring back the other store. The record
// carries the DB directory's identity, and only the store in that directory
// (same epoch, same identity) matches it. The "same directory" case is the
// control: the store whose attempt failed still reuses its checkpoint.
func TestSwappedStoreDoesNotInheritOpenRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		swap bool
	}{
		{"same directory", false},
		{"swapped store", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := vfs.NewMem()
			e := headStore(t, fs, failingOwners()...)
			ino := uint64(1)
			e.opts.dirIdentity = func(string) (uint64, uint64, error) { return 7, ino, nil }
			if _, err := e.tryOpen(); err == nil {
				t.Fatal("the failing upgrade succeeded")
			}
			old := readPointer(t, e).Name
			oldRec := readRecord(t, e, old)
			if !oldRec.open() || oldRec.DirDev != 7 || oldRec.DirIno != 1 {
				t.Fatalf("the failed attempt's record = %+v, want open with dir identity 7/1", oldRec)
			}
			if tc.swap {
				dir, base := filepath.Dir(e.path), filepath.Base(e.path)
				names, err := fs.List(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, n := range names {
					if strings.HasPrefix(n, base) {
						if err := fs.RemoveAll(filepath.Join(dir, n)); err != nil {
							t.Fatal(err)
						}
					}
				}
				e.seedRaw("riddler:1", "?", "riddler:2", "??")
				ino = 2
			}
			e.opts.Owners = fixedOwners()
			db := e.open()
			got := db.CheckpointName()
			if !tc.swap {
				if got != old || db.boot.installID != oldRec.InstallID {
					t.Fatalf("the same store used checkpoint %q as %q, want %q as %q", got, db.boot.installID, old, oldRec.InstallID)
				}
				return
			}
			if db.boot.installID == "" || db.boot.installID == oldRec.InstallID {
				t.Fatalf("the swapped store's install ID = %q, want a new one (the open record has %q)", db.boot.installID, oldRec.InstallID)
			}
			if got == "" || got == old {
				t.Fatalf("the swapped store used checkpoint %q, want its own (not %q)", got, old)
			}
			if rec := readRecord(t, e, got); rec.InstallID != db.boot.installID || rec.DirIno != 2 {
				t.Fatalf("the swapped store's record = %+v, want install %q and dir identity 7/2", rec, db.boot.installID)
			}
			if keys := rawKeys(t, fs, filepath.Join(cpDir(e), got, "db"), "riddler:"); len(keys) != 2 {
				t.Fatalf("the swapped store's checkpoint holds riddler keys %v, want both", keys)
			}
			if rec := readRecord(t, e, old); rec.InstallID != oldRec.InstallID || !rec.open() {
				t.Fatalf("the old store's record changed: %+v", rec)
			}
		})
	}
}

// TestSecondUpgradeTakesFreshCheckpoint (05-storage §5S.3.12, DATA3-1;
// review BLOCKER-1): only a crash loop reuses a checkpoint. A completed
// attempt's is never reused, and neither is a failed attempt's once a boot
// has served the store since: the previous binary serves, MarkServed writes
// ready_at, and the fixed build takes its own checkpoint, so a rollback to
// it keeps the writes made while rolled back.
func TestSecondUpgradeTakesFreshCheckpoint(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		e := headStore(t, vfs.NewMem(), cpOwners()...)
		db := e.open()
		first := db.CheckpointName()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if rec := readRecord(t, e, first); rec.CompletedAt == nil {
			t.Fatalf("the first upgrade's record = %+v, want completed", rec)
		}
		e.opts.Owners = fixedOwners()
		db = e.open()
		if got := db.CheckpointName(); got == "" || got == first {
			t.Fatalf("the second upgrade used checkpoint %q, want its own (not %q)", got, first)
		}
	})
	t.Run("served", func(t *testing.T) {
		e := headStore(t, vfs.NewMem(), failingOwners()...)
		if _, err := e.tryOpen(); err == nil {
			t.Fatal("the failing upgrade succeeded")
		}
		first := readPointer(t, e).Name
		// The previous binary: no step to run, so no checkpoint; it serves.
		e.opts.Owners = cpOwners()
		db := e.open()
		if db.CheckpointName() != "" {
			t.Fatalf("the previous binary took checkpoint %q", db.CheckpointName())
		}
		ready := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
		if err := db.MarkServed(ready); err != nil {
			t.Fatal(err)
		}
		ks, _ := db.Keyspace("wayne")
		if err := ks.Set(context.Background(), []byte("robin"), []byte("written-while-rolled-back")); err != nil {
			t.Fatal(err)
		}
		rec := readRecord(t, e, first)
		if rec.ReadyAt == nil || !rec.ReadyAt.Equal(ready) || rec.Status != "served" || rec.CompletedAt != nil {
			t.Fatalf("the served record = %+v, want ready_at %s, status served, no completed_at", rec, ready)
		}
		if p := readPointer(t, e); p.Name != first || p.State != "open" {
			t.Fatalf("pointer = %+v, want %s open (served is not a pointer state)", p, first)
		}
		// A second call changes nothing.
		if err := db.MarkServed(ready.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if again := readRecord(t, e, first); !again.ReadyAt.Equal(ready) {
			t.Fatalf("a second MarkServed moved ready_at to %s", again.ReadyAt)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		e.opts.Owners = fixedOwners()
		db = e.open()
		got := db.CheckpointName()
		if got == "" || got == first {
			t.Fatalf("the fixed build used checkpoint %q, want its own (not the pre-serving %q)", got, first)
		}
		if n := readRecord(t, e, got); n.InstallID != rec.InstallID {
			t.Fatalf("the new checkpoint's install_id = %q, want %q", n.InstallID, rec.InstallID)
		}
		if raw := rawKeys(t, e.fs, filepath.Join(cpDir(e), got, "db"), "m/wayne/robin"); len(raw) != 1 {
			t.Fatalf("the fixed build's checkpoint lacks the write made while rolled back: %v", raw)
		}
		if old := readRecord(t, e, first); old.ReadyAt == nil || old.CompletedAt != nil {
			t.Fatalf("the served record changed: %+v", old)
		}
	})
}

// TestStrandedAttemptCompletedNextBoot (05-storage §5S.4 step 9; review
// BLOCKER-2, probe A): a kill between step 9's batch and the checkpoint's
// completion leaves the record open with nothing left to run. The batch
// names the attempt in c/storage/attempt, so the next boot completes the
// record (record, then pointer) and deletes the key, and a later upgrade
// takes its own checkpoint instead of reusing the stranded one. Resolving
// is idempotent: a key whose record is already completed, or gone, is only
// deleted.
func TestStrandedAttemptCompletedNextBoot(t *testing.T) {
	t.Run("killed-before-completion", func(t *testing.T) {
		fs := vfs.NewCrashableMem()
		e := headStore(t, fs, cpOwners()...)
		crashed := errors.New("power cut")
		var clone *vfs.MemFS
		e.opts.hooks.beforeComplete = func() error {
			clone = fs.CrashClone(vfs.CrashCloneCfg{})
			return crashed
		}
		if _, err := e.tryOpen(); !errors.Is(err, crashed) {
			t.Fatalf("boot = %v, want the simulated crash", err)
		}
		after := newEnvFS(t, clone, cpOwners()...)
		after.path, after.opts.Path = e.path, e.path
		name := readPointer(t, after).Name
		if rec := readRecord(t, after, name); !rec.open() {
			t.Fatalf("the killed attempt's record = %+v, want open", rec)
		}
		if got := rawKeys(t, clone, e.path, "c/storage/attempt"); len(got) != 1 {
			t.Fatalf("attempt keys after the kill = %v, want one", got)
		}
		db := after.open()
		if got := db.CheckpointName(); got != "" {
			t.Fatalf("the next boot took or reused checkpoint %q", got)
		}
		rec := readRecord(t, after, name)
		if rec.CompletedAt == nil || rec.DeleteAfter == nil || rec.Status != "completed" {
			t.Fatalf("the stranded record after the next boot = %+v, want completed", rec)
		}
		if p := readPointer(t, after); p.Name != name || p.State != "completed" || p.DeleteAfter == nil || !p.DeleteAfter.Equal(*rec.DeleteAfter) {
			t.Fatalf("pointer = %+v, want %s completed with the record's delete_after", p, name)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if got := rawKeys(t, clone, e.path, "c/storage/attempt"); len(got) != 0 {
			t.Fatalf("attempt keys after the next boot = %v, want none", got)
		}
		after.opts.Owners = fixedOwners()
		db = after.open()
		if got := db.CheckpointName(); got == "" || got == name {
			t.Fatalf("the later upgrade used checkpoint %q, want its own (not the stranded %q)", got, name)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if got := rawKeys(t, clone, e.path, "c/storage/attempt"); len(got) != 0 {
			t.Fatalf("a completed upgrade left attempt keys %v", got)
		}
	})
	for _, tc := range []struct {
		name   string
		target func(cp string) string
	}{
		{"already-completed", func(cp string) string { return cp }},
		{"record-gone", func(string) string { return "20260101T000000Z-ba7ba7ba" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := headStore(t, vfs.NewMem(), cpOwners()...)
			db := e.open()
			cp := db.CheckpointName()
			raw, err := codecAttempt().Encode(attemptRecord{Checkpoint: tc.target(cp)})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.storageKS().Set(context.Background(), attemptKey(), raw); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, ptr := readRecord(t, e, cp), readPointer(t, e)
			if err := e.open().Close(); err != nil {
				t.Fatal(err)
			}
			if got := rawKeys(t, e.fs, e.path, "c/storage/attempt"); len(got) != 0 {
				t.Fatalf("attempt keys = %v, want none", got)
			}
			if rec := readRecord(t, e, cp); !rec.CompletedAt.Equal(*before.CompletedAt) || !rec.DeleteAfter.Equal(*before.DeleteAfter) {
				t.Fatalf("the completed record changed: %+v, was %+v", rec, before)
			}
			if p := readPointer(t, e); p.Name != ptr.Name || p.State != ptr.State {
				t.Fatalf("pointer = %+v, was %+v", p, ptr)
			}
		})
	}
}

// openFails is a vfs whose Open of one path fails, as an EIO or a
// permission error would.
type openFails struct {
	vfs.FS
	path string
}

var errScarecrow = errors.New("scarecrow's toxin: input/output error")

func (f openFails) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	if name == f.path {
		return nil, errScarecrow
	}
	return f.FS.Open(name, opts...)
}

// TestUnreadableCheckpointRecordFailsBoot (review MINOR-1): a record that
// exists but cannot be read fails the boot before any write. Skipping it
// would take a second full checkpoint beside the open one (and, on a
// marker-less store, orphan the record under a new install ID).
func TestUnreadableCheckpointRecordFailsBoot(t *testing.T) {
	mem := vfs.NewMem()
	e := headStore(t, mem, failingOwners()...)
	if _, err := e.tryOpen(); err == nil {
		t.Fatal("the failing upgrade succeeded")
	}
	name := readPointer(t, e).Name
	before, err := mem.List(cpDir(e))
	if err != nil {
		t.Fatal(err)
	}
	e.opts.fs = openFails{FS: mem, path: filepath.Join(cpDir(e), name+".json")}
	e.opts.Owners = fixedOwners()
	if _, err := e.tryOpen(); !errors.Is(err, errScarecrow) {
		t.Fatalf("boot with an unreadable record = %v, want the read error", err)
	}
	after, err := mem.List(cpDir(e))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(before)
	slices.Sort(after)
	if !slices.Equal(before, after) {
		t.Fatalf("checkpoint directory %v became %v: the refused boot wrote", before, after)
	}
}
