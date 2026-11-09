// file: core/storage/offline_test.go
// version: 1.0.0
// guid: 4c1e8a37-2b9d-4f05-a6e3-91d7b05c2f48
// last-edited: 2026-11-08

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
)

// offlineOwners is a build with one keyspace owner at version 1.
func offlineOwners() []OwnerSpec {
	return []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{step(1, "cave", "cave", "1")}}}}
}

// served boots e once (a serve boot) and closes it, leaving a store with a
// marker and nothing pending.
func served(t *testing.T, e *env) {
	t.Helper()
	db, err := e.tryOpen()
	if err != nil {
		t.Fatalf("serve boot: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// openOffline runs Open with readOnly and MustExist, then Offline.
func openOffline(t *testing.T, e *env, readOnly bool) (*DB, *Opened, error) {
	t.Helper()
	opts := e.opts
	opts.ReadOnly, opts.MustExist = readOnly, true
	op, err := Open(context.Background(), opts)
	if err != nil {
		return nil, nil, err
	}
	db, err := op.Offline()
	if err != nil {
		_ = op.Close()
		return nil, op, err
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, op, nil
}

func mustPending(t *testing.T, err error, want string) {
	t.Helper()
	if !errors.Is(err, ErrOfflinePendingSteps) {
		t.Fatalf("err = %v, want ErrOfflinePendingSteps", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want it to name %q", err, want)
	}
}

// TestOfflineRefusesEmptyStore (§5S.4 "Offline open" rule 2): an empty
// store's fresh install is serve's, so both modes refuse it, and MustExist
// never creates a directory that holds no store.
func TestOfflineRefusesEmptyStore(t *testing.T) {
	e := newEnv(t, offlineOwners()...)
	if _, _, err := openOffline(t, e, false); err == nil {
		t.Fatal("offline open of a path with no store succeeded")
	}
	if ok, _ := exists(e.fs, filepath.Join(e.path, "CURRENT")); ok {
		t.Fatal("the offline open created a store")
	}
	// An empty Pebble directory (a store created and never written).
	db, err := cpebble.Open(e.path, &cpebble.Options{FS: e.fs, Logger: quiet{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, ro := range []bool{false, true} {
		_, _, err := openOffline(t, e, ro)
		mustPending(t, err, "empty")
	}
}

func exists(fs vfs.FS, p string) (bool, error) {
	_, err := fs.Stat(p)
	if err == nil {
		return true, nil
	}
	if oserrorNotExist(err) {
		return false, nil
	}
	return false, err
}

// TestOfflineRefusesPendingSteps (§5S.4 "Offline open" rule 2, DATA7-11):
// a store a newer build has not migrated is refused in both modes, naming
// each runnable step; nothing is written.
func TestOfflineRefusesPendingSteps(t *testing.T) {
	e := newEnv(t, offlineOwners()...)
	served(t, e)
	e.opts.Owners = []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
		step(1, "cave", "cave", "1"), step(2, "batmobile", "batmobile", "1"),
	}}}}
	for _, ro := range []bool{false, true} {
		_, _, err := openOffline(t, e, ro)
		mustPending(t, err, "wayne")
		var pe *OfflinePendingError
		if !errors.As(err, &pe) || len(pe.Steps) != 1 || pe.Steps[0].Version != 2 {
			t.Fatalf("pending steps = %+v, want wayne v2", pe)
		}
	}
	if got := rawKeys(t, e.fs, e.path, "m/wayne/batmobile"); len(got) != 0 {
		t.Fatalf("the refused open ran the step: %v", got)
	}
}

// TestOfflineReadWriteRefusesMarkerless (§5S.4 "Offline open"): a HEAD
// store has no install marker; serve takes its checkpoint and writes the
// marker first, so a read-write offline open refuses it, and a read-only
// one reads it without writing the marker.
func TestOfflineReadWriteRefusesMarkerless(t *testing.T) {
	e := headStore(t, vfs.NewMem(), OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
	_, _, err := openOffline(t, e, false)
	mustPending(t, err, "install marker")
	db, _, err := openOffline(t, e, true)
	if err != nil {
		t.Fatalf("read-only offline open of a HEAD store: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := rawKeys(t, e.fs, e.path, "c/"); len(got) != 0 {
		t.Fatalf("the read-only open wrote core rows: %v", got)
	}
}

// TestOfflineReadWriteRefusesStrandedAttempt: an upgrade attempt a kill
// left uncompleted is serve's to complete (§5S.4 step 6), so a read-write
// offline open refuses it.
func TestOfflineReadWriteRefusesStrandedAttempt(t *testing.T) {
	e := newEnv(t, offlineOwners()...)
	db := e.open()
	raw, err := codecAttempt().Encode(attemptRecord{Checkpoint: "20261108T000000Z-0badc0de"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.storageKS().Set(context.Background(), attemptKey(), raw); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = openOffline(t, e, false)
	mustPending(t, err, "20261108T000000Z-0badc0de")
	if _, _, err := openOffline(t, e, true); err != nil {
		t.Fatalf("read-only offline open with a stranded attempt: %v", err)
	}
}

// TestOfflineReadWriteRefusesRatchet: a store below the build's Pebble
// format is ratcheted by serve behind a checkpoint, so a read-write offline
// open refuses it.
func TestOfflineReadWriteRefusesRatchet(t *testing.T) {
	e := newEnv(t, offlineOwners()...)
	served(t, e)
	// Copy every row into a store at the format below the pinned one: the
	// same marker and stamps, an older format.
	src, err := cpebble.Open(e.path, &cpebble.Options{FS: e.fs, Logger: quiet{}, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(filepath.Dir(e.path), "old.pebble")
	dst, err := cpebble.Open(old, &cpebble.Options{FS: e.fs, Logger: quiet{}, FormatMajorVersion: ipebble.Format - 1})
	if err != nil {
		t.Fatal(err)
	}
	it, err := src.NewIter(nil)
	if err != nil {
		t.Fatal(err)
	}
	for it.First(); it.Valid(); it.Next() {
		if err := dst.Set(it.Key(), it.Value(), cpebble.NoSync); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(it.Close(), src.Close(), dst.Flush(), dst.Close()); err != nil {
		t.Fatal(err)
	}
	e.path, e.opts.Path = old, old
	_, _, err = openOffline(t, e, false)
	mustPending(t, err, "Pebble format")
	if _, _, err := openOffline(t, e, true); err != nil {
		t.Fatalf("read-only offline open below the format: %v", err)
	}
}

// TestOfflineReadWriteCommits: a served store opens read-write offline;
// a write commits and is there at the next boot; Prepare, Migrate and
// MarkServed refuse the offline DB.
func TestOfflineReadWriteCommits(t *testing.T) {
	e := newEnv(t, offlineOwners()...)
	served(t, e)
	db, op, err := openOffline(t, e, false)
	if err != nil {
		t.Fatal(err)
	}
	if op.InstallID() == "" {
		t.Fatal("InstallID is empty on a served store")
	}
	if _, err := op.Prepare(context.Background()); err == nil {
		t.Fatal("Prepare after Offline succeeded")
	}
	if err := db.Migrate(context.Background()); err == nil {
		t.Fatal("Migrate after Offline succeeded")
	}
	if err := db.MarkServed(time.Now()); err == nil {
		t.Fatal("MarkServed on an offline DB succeeded")
	}
	ks, ok := db.Keyspace("wayne")
	if !ok {
		t.Fatal("no wayne keyspace")
	}
	if err := ks.Set(context.Background(), []byte("robin"), []byte("1")); err != nil {
		t.Fatalf("offline write: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := rawKeys(t, e.fs, e.path, "m/wayne/robin"); len(got) != 1 {
		t.Fatalf("offline write not durable: %v", got)
	}
}

// TestOfflineReadOnlyWritesNothing (§5S.4 "Offline open" rule 5): a
// read-only open refuses every write with ErrReadOnly.
func TestOfflineReadOnlyWritesNothing(t *testing.T) {
	e := newEnv(t, offlineOwners()...)
	served(t, e)
	db, op, err := openOffline(t, e, true)
	if err != nil {
		t.Fatal(err)
	}
	if op.InstallID() == "" {
		t.Fatal("InstallID is empty on a served store")
	}
	ks, _ := db.Keyspace("wayne")
	mustErrIs(t, ks.Set(context.Background(), []byte("robin"), []byte("1")), ErrReadOnly)
}

// TestOfflineRefusesHeldStore (§5S.3.12 "In place"): Pebble's directory
// lock refuses an open, read-only included, while another process (here,
// another open) holds the store.
func TestOfflineRefusesHeldStore(t *testing.T) {
	e := newEnv(t, offlineOwners()...)
	served(t, e)
	held, err := e.tryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	if _, _, err := openOffline(t, e, true); err == nil {
		t.Fatal("read-only open of a held store succeeded")
	}
}

// TestCheckpointsLatestConsumeRelease: the checkpoint records read without
// the store: Latest is the pointer's record; Consume and Release write
// machine 3's edges once.
func TestCheckpointsLatestConsumeRelease(t *testing.T) {
	fs := vfs.NewMem()
	e := headStore(t, fs, failingOwners()...)
	if _, err := e.tryOpen(); err == nil {
		t.Fatal("the failing upgrade succeeded")
	}
	cps, err := CheckpointsOf(FS{fs: fs}, e.path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	cp, ok, err := cps.Latest()
	if err != nil || !ok {
		t.Fatalf("Latest = %v, %v", ok, err)
	}
	if cp.Status != "open" || cp.NeedsAcceptLoss() || !cp.Restorable() {
		t.Fatalf("latest = %+v, want an open restorable checkpoint", cp)
	}
	if cp.Store() != filepath.Join(cpDir(e), cp.Name, "db") {
		t.Fatalf("store copy = %s", cp.Store())
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if err := cps.Release(cp.Name, now); err != nil {
		t.Fatal(err)
	}
	if err := cps.Release(cp.Name, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rec := readRecord(t, e, cp.Name)
	if rec.DeleteAfter == nil || !rec.DeleteAfter.Equal(now.Add(checkpointRetention)) {
		t.Fatalf("delete_after = %v, want written once at the first release", rec.DeleteAfter)
	}
	if rec.pending() {
		t.Fatal("a released record is still pending: the next upgrade would reuse it")
	}
	cp, _, _ = cps.Latest()
	if cp.Restorable() {
		t.Fatal("a released checkpoint is restorable")
	}
	if err := cps.Consume(cp.Name, now); err != nil {
		t.Fatal(err)
	}
	if rec := readRecord(t, e, cp.Name); rec.Status != statusConsumed || rec.pending() {
		t.Fatalf("record = %+v, want consumed", rec)
	}
	mustErrIs(t, cps.Consume("20991231T000000Z-deadbeef", now), ErrCheckpointGone)
}

// TestCheckpointsNoPointer: a store with no attempt has no latest
// checkpoint, and a pointer naming a pruned record is ErrCheckpointGone.
func TestCheckpointsNoPointer(t *testing.T) {
	e := newEnv(t, offlineOwners()...)
	served(t, e)
	cps, err := CheckpointsOf(FS{fs: e.fs}, e.path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cps.Latest(); ok || err != nil {
		t.Fatalf("Latest on a fresh install = %v, %v", ok, err)
	}
	pointer, err := json.Marshal(checkpointPointer{Name: "20261108T000000Z-0badc0de", State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ipebble.WriteFileAtomic(cps.a.fs, cps.a.pointerPath(), pointer); err != nil {
		t.Fatal(err)
	}
	_, _, err = cps.Latest()
	mustErrIs(t, err, ErrCheckpointGone)
	if _, err := CheckpointsOf(FS{fs: e.fs}, "relative/main.pebble", "", nil); err == nil {
		t.Fatal("a relative path was accepted")
	}
	_, err = CheckpointsOf(FS{fs: e.fs}, e.path, e.path, nil)
	mustErrIs(t, err, ErrCheckpointDirOverlap)
}

// TestResetRequestValidate (§5S.3.15 "Staging" (1)).
func TestResetRequestValidate(t *testing.T) {
	for _, tc := range []struct {
		req  ResetRequest
		want error
	}{
		{ResetRequest{Kind: "factory", Confirm: "RESET"}, nil},
		{ResetRequest{Kind: "store", Confirm: "RESET"}, nil},
		{ResetRequest{Kind: "factory", Confirm: "RESET", DeleteLibraryFiles: true}, nil},
		{ResetRequest{Kind: "factory", Confirm: "reset"}, ErrResetConfirmRequired},
		{ResetRequest{Kind: "factory"}, ErrResetConfirmRequired},
		{ResetRequest{Kind: "store", Confirm: "RESET", DeleteLibraryFiles: true}, ErrResetRequestInvalid},
		{ResetRequest{Kind: "joker", Confirm: "RESET"}, ErrResetRequestInvalid},
		{ResetRequest{Confirm: "RESET"}, ErrResetRequestInvalid},
	} {
		err := tc.req.Validate()
		if (tc.want == nil) != (err == nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%+v: err = %v, want %v", tc.req, err, tc.want)
		}
	}
	e := &LibraryConfirmError{Root: "/gotham/library", Files: 3}
	if !errors.Is(e, ErrLibraryConfirmRequired) || !strings.Contains(e.Error(), "/gotham/library") {
		t.Fatalf("LibraryConfirmError = %v", e)
	}
}

// TestConsumedRecordNeverPending: a consumed record names no copy, so it is
// never reused or completed, also when a writer left delete_after unset.
func TestConsumedRecordNeverPending(t *testing.T) {
	r := checkpointRecord{Status: statusConsumed}
	if r.pending() || r.open() {
		t.Fatal("a consumed record without delete_after is pending")
	}
	if c := checkpointOf("/gotham", "20261108T000000Z-0badc0de", r); c.Restorable() {
		t.Fatal("a consumed checkpoint is restorable")
	}
}
