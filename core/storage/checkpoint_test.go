// file: core/storage/checkpoint_test.go
// version: 1.0.0
// guid: 77194a40-fc27-4829-9543-097e945b3bdd
// last-edited: 2026-11-07

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
	failing := []OwnerSpec{
		{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}},
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
			step(1, "cave", "cave", "1"),
			{Version: 2, Name: "broken", Compat: Additive, Up: func(context.Context, *MigrationTx) error { return errors.New("bane broke it") }},
		}}},
	}
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
	e.opts.Owners = []OwnerSpec{failing[0], {Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
		step(1, "cave", "cave", "1"), step(2, "fixed", "fixed", "1"),
	}}}}
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
func TestPartialCheckpointSwept(t *testing.T) {
	fs := vfs.NewMem()
	e := headStore(t, fs, cpOwners()...)
	dir := cpDir(e)
	for _, d := range []string{"20260101T000000Z-catwoman", "20260102T000000Z-poison.tmp", "scratch"} {
		if err := fs.MkdirAll(filepath.Join(dir, d, "db"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f, err := fs.Create(filepath.Join(dir, "20260103T000000Z-twoface.json"), vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	db := e.open()
	names, _ := fs.List(dir)
	slices.Sort(names)
	want := []string{db.CheckpointName(), db.CheckpointName() + ".json", "scratch"}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("after the sweep: %v, want %v", names, want)
	}
	if strings.Contains(db.CheckpointName(), "catwoman") {
		t.Fatal("a record-less checkpoint was reused")
	}
}
