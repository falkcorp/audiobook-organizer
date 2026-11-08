// file: core/coretest/store.go
// version: 1.0.0
// guid: dbff53d4-dfb7-43f4-a120-4d14293d066d
// last-edited: 2026-11-07

package coretest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// storeBuild is the binary ID a fixture store records in its build fields
// (05-storage §5S.3.7 "Binary ID"): a fixed value, so two fixture boots of
// one test count as one build.
const storeBuild = "coretest+fixture"

// Store is a core store for a test, the store fixture over storage (04 §8.5
// R1). It boots the way the kernel's P2, P4a and P4b do (storage.Open, then
// Prepare, then Migrate), so a fresh store gets the fresh-install batch and
// runs every Seed step, and a reboot takes the guard, the checkpoint and the
// migrations of a real upgrade. The store lives in a real directory under
// tb.TempDir(); tb's cleanup closes it. Do not close the DB yourself.
type Store struct {
	tb   testing.TB
	path string
	db   *storage.DB
}

// NewStore boots a fresh store whose owners are owners plus the implicit
// core/storage, and returns it with every runnable step applied. Any boot
// error fails tb.
func NewStore(tb testing.TB, owners ...storage.OwnerSpec) *Store {
	tb.Helper()
	s := &Store{tb: tb, path: filepath.Join(tb.TempDir(), "storage", "main.pebble")}
	s.db = s.boot(owners)
	tb.Cleanup(func() {
		if s.db != nil {
			if err := s.db.Close(); err != nil {
				tb.Errorf("coretest.Store: close: %v", err)
			}
		}
	})
	return s
}

// DB is the open store.
func (s *Store) DB() *storage.DB { return s.db }

// Path is the store's directory (core.storage.path).
func (s *Store) Path() string { return s.path }

// Keyspace is owner's keyspace; it fails tb when owner has none.
func (s *Store) Keyspace(owner storage.Owner) storage.Keyspace {
	s.tb.Helper()
	ks, ok := s.db.Keyspace(owner)
	if !ok {
		s.tb.Fatalf("coretest.Store: owner %s has no keyspace", owner)
	}
	return ks
}

// Reboot closes the store and boots it again with owners, as the next boot
// of a build that declares them would, and returns the new DB. Handles cut
// from the previous DB fail with storage.ErrClosed. Any error fails tb.
func (s *Store) Reboot(owners ...storage.OwnerSpec) *storage.DB {
	s.tb.Helper()
	db := s.db
	s.db = nil
	if err := db.Close(); err != nil {
		s.tb.Fatalf("coretest.Store: close before reboot: %v", err)
	}
	s.db = s.boot(owners)
	return s.db
}

// boot is P2, P4a and P4b over the fixture's path. The checkpoint margin is
// one byte: a fixture's checkpoint is tiny, and the default 2 GiB margin
// would make a reboot fail on a nearly full test disk.
func (s *Store) boot(owners []storage.OwnerSpec) *storage.DB {
	s.tb.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: s.path, Owners: owners, Build: storeBuild, FreeSpaceMargin: 1})
	if err != nil {
		s.tb.Fatalf("coretest.Store: open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		s.tb.Fatalf("coretest.Store: boot: %v", err)
	}
	return db
}
