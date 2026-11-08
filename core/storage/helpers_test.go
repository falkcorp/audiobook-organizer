// file: core/storage/helpers_test.go
// version: 1.0.2
// guid: 56109ea6-e975-416a-9681-1f2102068005
// last-edited: 2026-11-08

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/codec"
)

// Fixture owners: a module with a keyspace ("wayne"), a module with legacy
// claims ("gotham"), and a core subsystem ("core/tasks").

func wayneSpec() Spec { return Spec{Keyspace: true} }

func gothamClaims() []Claim {
	return []Claim{
		{Prefix: "joker:", Mode: ClaimReadWrite},
		{Prefix: "joker:retired:", Mode: ClaimRetired},
		{Key: "joker_counter", Mode: ClaimReadWrite, Serialized: true},
		{Prefix: "arkham:", Mode: ClaimReadOnly},
	}
}

// env is one test store: its filesystem, path and options.
type env struct {
	t    *testing.T
	fs   vfs.FS
	path string
	opts OpenOptions
}

// newEnv is an in-memory store with the given owners; the path is a fresh
// temp-dir name, used only as a name inside the in-memory filesystem.
func newEnv(t *testing.T, owners ...OwnerSpec) *env {
	t.Helper()
	return newEnvFS(t, vfs.NewMem(), owners...)
}

func newEnvFS(t *testing.T, fs vfs.FS, owners ...OwnerSpec) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.pebble")
	e := &env{t: t, fs: fs, path: path}
	e.opts = OpenOptions{
		Path: path, Owners: owners, Build: "batcomputer-1",
		FS:        FS{fs: fs},
		freeSpace: func(string) (uint64, error) { return 1 << 50, nil },
	}
	return e
}

// open runs Open, Prepare and Migrate and registers the close.
func (e *env) open() *DB {
	e.t.Helper()
	db, err := e.tryOpen()
	if err != nil {
		e.t.Fatalf("boot: %v", err)
	}
	return db
}

// tryOpen runs the boot and returns its first error; a DB returned is closed
// at cleanup.
func (e *env) tryOpen() (*DB, error) {
	e.t.Helper()
	op, err := Open(context.Background(), e.opts)
	if err != nil {
		return nil, err
	}
	db, err := op.Prepare(context.Background())
	if err != nil {
		_ = op.Close()
		return nil, err
	}
	if err := db.Migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	e.t.Cleanup(func() { _ = db.Close() })
	return db, nil
}

// seedRaw writes keys straight into the store with Pebble, as HEAD (a build
// without core storage) would.
func (e *env) seedRaw(kv ...string) {
	e.t.Helper()
	db, err := cpebble.Open(e.path, &cpebble.Options{FS: e.fs, Logger: quiet{}})
	if err != nil {
		e.t.Fatal(err)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if err := db.Set([]byte(kv[i]), []byte(kv[i+1]), cpebble.Sync); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		e.t.Fatal(err)
	}
}

// rawKeys lists every key of a closed store (or of a checkpoint copy) under
// prefix.
func rawKeys(t *testing.T, fs vfs.FS, path string, prefix string) []string {
	t.Helper()
	db, err := cpebble.Open(path, &cpebble.Options{FS: fs, ReadOnly: true, Logger: quiet{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	it, err := db.NewIter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	var out []string
	for ok := it.First(); ok; ok = it.Next() {
		if bytes.HasPrefix(it.Key(), []byte(prefix)) {
			out = append(out, string(it.Key()))
		}
	}
	return out
}

// crashOnce returns a hook that clones the crashable filesystem the first
// time it runs (the "crash"), recording the clone.
func crashOnce(fs *vfs.MemFS, into **vfs.MemFS) func() {
	return func() {
		if *into == nil {
			*into = fs.CrashClone(vfs.CrashCloneCfg{})
		}
	}
}

// mustErrIs fails unless errors.Is(err, target).
func mustErrIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

// stamp reads a committed level stamp through storage's keyspace.
func stamp(t *testing.T, db *DB, o Owner) (int, bool) {
	t.Helper()
	raw, err := db.storageKS().Get(context.Background(), levelKey(o))
	if errors.Is(err, ErrNotFound) {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	rec, err := codec.JSON[stampRecord](1).Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Level, true
}

// step is a versioned Additive step that sets one key of the keyspace.
func step(v int, name, key, value string) Migration {
	return Migration{Version: v, Name: name, Compat: Additive, Up: func(ctx context.Context, tx *MigrationTx) error {
		return tx.Set([]byte(key), []byte(value))
	}}
}

// quiet discards Pebble's log lines in the raw opens above.
type quiet struct{}

func (quiet) Infof(string, ...any)  {}
func (quiet) Errorf(string, ...any) {}
func (quiet) Fatalf(format string, args ...any) {
	panic(fmt.Sprintf(format, args...))
}
