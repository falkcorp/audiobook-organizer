// file: core/storage/index/indextest/fixture.go
// version: 1.0.0
// guid: 6bcc9f39-42a4-4a2b-9908-42f1ccfa498f
// last-edited: 2026-11-11

// Package indextest is the test support of storage/index: a store booted the
// way the kernel boots one (storage.Open, Prepare, Migrate) with a module that
// declares derived indexes and the core c/index/ keyspace registered, so a
// test of the dirty set, the generation rule or an engine runs on the real
// storage with no kernel (and no import cycle: coretest imports the kernel).
package indextest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
)

// Module is the fixture's module owner.
const Module storage.Owner = "gotham"

// Derived index names the fixture's module declares.
const (
	Signal = "signal"
	Cave   = "cave"
)

// Fixture is one booted store.
type Fixture struct {
	T      testing.TB
	DB     *storage.DB
	Module storage.Keyspace // gotham's keyspace m/gotham/
	Root   storage.Keyspace // c/index/
	Path   string
	FS     storage.FS

	once sync.Once
	err  error
}

// Open boots the store at path on fs and closes it at cleanup. path must be
// absolute.
func Open(t testing.TB, fs storage.FS, path string) *Fixture {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: path,
		Owners: []storage.OwnerSpec{
			{Owner: Module, Enabled: true, Spec: storage.Spec{
				Keyspace: true,
				Derived: []storage.DerivedIndex{
					{Name: Signal, Version: "1"},
					{Name: Cave, Version: "1", Blocking: true},
				},
			}},
			{Owner: index.Owner, Enabled: true, Spec: index.StorageSpec()},
		},
		Build:           "indextest+fixture",
		FreeSpaceMargin: 1,
		FS:              fs,
	})
	if err != nil {
		t.Fatalf("indextest: open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("indextest: boot: %v", errors.Join(err, o.Close()))
	}
	mod, ok := db.Keyspace(Module)
	if !ok {
		t.Fatal("indextest: the module has no keyspace")
	}
	root, ok := db.Keyspace(index.Owner)
	if !ok {
		t.Fatal("indextest: c/index/ has no keyspace")
	}
	f := &Fixture{T: t, DB: db, Module: mod, Root: root, Path: path, FS: fs}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("indextest: close: %v", err)
		}
	})
	return f
}

// Close closes the store; it is idempotent, so a test that restarts the store
// may close it itself and leave the cleanup a no-op.
func (f *Fixture) Close() error {
	f.once.Do(func() { f.err = f.DB.Close() })
	return f.err
}

// Derived is the module's handle for the declared index name.
func (f *Fixture) Derived(name string) storage.Derived {
	f.T.Helper()
	d, err := f.DB.Derived(Module, name)
	if err != nil {
		f.T.Fatalf("indextest: derived %q: %v", name, err)
	}
	return d
}

// DirtySet is the dirty set of the module's index name.
func (f *Fixture) DirtySet(name string) *index.DirtySet {
	f.T.Helper()
	d, err := index.NewDirtySet(f.Root, f.Derived(name), string(Module), name)
	if err != nil {
		f.T.Fatalf("indextest: dirty set %q: %v", name, err)
	}
	return d
}
