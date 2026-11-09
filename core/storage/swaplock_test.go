// file: core/storage/swaplock_test.go
// version: 1.0.0
// guid: 38b05a8f-6477-410c-8f06-4f68a7d1580a
// last-edited: 2026-11-09

package storage

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// hasFlock: the swap lock is flock(2) here (swaplock_flock.go's build
// tags); elsewhere both modes are one exclusive lock.
func hasFlock() bool {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "netbsd", "openbsd", "dragonfly":
		return true
	}
	return false
}

// TestSwapLock: shared takes coexist, and the two modes exclude each other
// within this process (each take opens the file anew), so a serve boot and
// an offline swap can never both hold it. A release lets the other mode in.
func TestSwapLock(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	mustClose := func(c io.Closer) {
		t.Helper()
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	flock := hasFlock()

	first, err := LockSwapShared(vfs.Default, state)
	if err != nil {
		t.Fatalf("first shared take: %v", err)
	}
	fi, err := os.Stat(SwapLockPath(state))
	if err != nil {
		t.Fatal(err)
	}
	if flock && fi.Mode().Perm() != 0o600 {
		t.Fatalf("the lock file's mode is %v, want 0600", fi.Mode().Perm())
	}
	if flock {
		second, err := LockSwapShared(vfs.Default, state)
		if err != nil {
			t.Fatalf("a second shared take beside the first: %v", err)
		}
		mustClose(second)
	}
	if _, err := LockSwapExclusive(vfs.Default, state); !errors.Is(err, ErrSwapLockHeld) {
		t.Fatalf("an exclusive take under a shared holder = %v, want ErrSwapLockHeld", err)
	}
	mustClose(first)

	ex, err := LockSwapExclusive(vfs.Default, state)
	if err != nil {
		t.Fatalf("an exclusive take once the shared holder released: %v", err)
	}
	if _, err := LockSwapShared(vfs.Default, state); !errors.Is(err, ErrSwapLockHeld) {
		t.Fatalf("a shared take under the exclusive holder = %v, want ErrSwapLockHeld", err)
	}
	if _, err := LockSwapExclusive(vfs.Default, state); !errors.Is(err, ErrSwapLockHeld) {
		t.Fatalf("a second exclusive take = %v, want ErrSwapLockHeld", err)
	}
	mustClose(ex)
	again, err := LockSwapShared(vfs.Default, state)
	if err != nil {
		t.Fatalf("a shared take once the exclusive holder released: %v", err)
	}
	mustClose(again)
}

// TestSwapLockRefusals: a relative or empty state directory is refused, a
// symbolic link in the lock file's place is not followed, and a filesystem
// other than the real one takes no lock (the crash-matrix kit's MemFS).
func TestSwapLockRefusals(t *testing.T) {
	for _, dir := range []string{"", "state"} {
		if _, err := LockSwapExclusive(vfs.Default, dir); err == nil {
			t.Fatalf("state directory %q: no error", dir)
		}
	}
	mem := vfs.NewMem()
	a, err := LockSwapExclusive(mem, "/state")
	if err != nil {
		t.Fatal(err)
	}
	b, err := LockSwapExclusive(mem, "/state")
	if err != nil {
		t.Fatalf("a second take in memory: %v (no lock is taken there)", err)
	}
	if err := errors.Join(a.Close(), b.Close()); err != nil {
		t.Fatal(err)
	}
	if !hasFlock() {
		return
	}
	state := t.TempDir()
	target := filepath.Join(t.TempDir(), "joker")
	if err := os.Symlink(target, SwapLockPath(state)); err != nil {
		t.Fatal(err)
	}
	if l, err := LockSwapShared(vfs.Default, state); err == nil {
		_ = l.Close()
		if _, serr := os.Stat(target); serr == nil {
			t.Fatal("the swap lock followed a symbolic link and created its target")
		}
		t.Fatal("the swap lock took a symbolic link in its place")
	}
}
