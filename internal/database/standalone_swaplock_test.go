// file: internal/database/standalone_swaplock_test.go
// version: 1.0.0
// guid: 42d42a6c-04c8-4629-9252-a76f0b42270f
// last-edited: 2026-11-09

package database

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/storage"
)

// TestStandaloneOpenerHoldsTheSwapLock (R1-25 review B1): with a state
// directory the standalone opener holds the swap lock shared until its
// store closes, so storage restore-offline and storage reset (which take it
// exclusively) refuse while ABO's CLI has the store open, and an open while
// one of them runs is refused before the slot is read. A failed open
// releases it, and without a state directory no lock is taken.
func TestStandaloneOpenerHoldsTheSwapLock(t *testing.T) {
	parent := t.TempDir()
	dbPath := filepath.Join(parent, "gotham.pebble")
	stateDir := t.TempDir()
	exclusive := func() error {
		l, err := storage.LockSwapExclusive(vfs.Default, stateDir)
		if err != nil {
			return err
		}
		return l.Close()
	}

	s, err := OpenStandalone(dbPath, StandaloneOptions{StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := exclusive(); !errors.Is(err, storage.ErrSwapLockHeld) {
		t.Fatalf("an offline swap's take under an open store = %v, want storage.ErrSwapLockHeld", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := exclusive(); err != nil {
		t.Fatalf("an offline swap's take once the store closed = %v", err)
	}

	l, err := storage.LockSwapExclusive(vfs.Default, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStandalone(dbPath, StandaloneOptions{StateDir: stateDir}); !errors.Is(err, storage.ErrSwapLockHeld) {
		if err == nil {
			_ = s.Close()
		}
		t.Fatalf("an open while an offline swap runs = %v, want storage.ErrSwapLockHeld", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// A refused open (here the store directory is a file) releases the lock.
	if _, err := OpenStandalone(filepath.Join(stateDir, storage.SwapLockName), StandaloneOptions{StateDir: stateDir}); err == nil {
		t.Fatal("a store path that is a file opened")
	}
	if err := exclusive(); err != nil {
		t.Fatalf("an offline swap's take after a refused open = %v; the open kept the lock", err)
	}

	unlocked, err := OpenStandalone(dbPath, StandaloneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer unlocked.Close()
	if err := exclusive(); err != nil {
		t.Fatalf("an offline swap's take beside an open without a state directory = %v, want no lock taken", err)
	}
}
