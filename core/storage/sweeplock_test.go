// file: core/storage/sweeplock_test.go
// version: 1.0.0
// guid: 6931a6c8-f440-454f-a43a-fa2c40e68350
// last-edited: 2026-11-09

package storage

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// sweepLockChildEnv names the store whose sweep lock the child process of
// TestSweepLockHeldByAnotherProcessIsBusy holds.
const sweepLockChildEnv = "MEDIACORE_SWEEPLOCK_CHILD"

// sweepLockArea is the checkpoints of a store in a fresh directory, its
// checkpoint directory created as the sweeper creates it.
func sweepLockArea(t *testing.T) *Checkpoints {
	t.Helper()
	cps, err := CheckpointsOf(FSOf(vfs.Default), filepath.Join(t.TempDir(), "batcave.pebble"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cps.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return cps
}

// TestPlanSweepLock (R1-25 follow-up SF-1): the sweep lock file is refused
// unless it is a regular file the checkpoint directory's owner owns, and
// each refusal says what to do.
func TestPlanSweepLock(t *testing.T) {
	p := filepath.Join("batcave", sweepLockName)
	for _, tc := range []struct {
		name      string
		regular   bool
		file, dir uint32
		want      []string // nil: allowed
	}{
		{"the owner's regular file", true, 1000, 1000, nil},
		{"root's file in root's directory", true, 0, 0, nil},
		{"root's file in the service user's directory", true, 0, 1000, []string{"owned by uid 0", "owner (uid 1000)", "chown it to uid 1000", "or remove it"}},
		{"the service user's file in root's directory", true, 1000, 0, []string{"owned by uid 1000", "owner (uid 0)", "chown it to uid 0"}},
		{"not a regular file", false, 1000, 1000, []string{"not a regular file", "remove it while no sweep"}},
		{"another user's directory", false, 0, 1000, []string{"not a regular file"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := planSweepLock(p, tc.regular, tc.file, tc.dir)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("planSweepLock = %v, want nil", err)
				}
				return
			}
			mustErrIs(t, err, ErrCheckpointSweepLockForeign)
			if errors.Is(err, ErrCheckpointSweepBusy) {
				t.Fatalf("planSweepLock = %v, also ErrCheckpointSweepBusy", err)
			}
			for _, w := range append(tc.want, p) {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("planSweepLock = %v, want it to say %q", err, w)
				}
			}
		})
	}
}

// TestSweepLockRefusesWhatTheOwnerCannotUse (R1-25 follow-up SF-1): a file
// at the sweep lock's name that the service could not take, or must not
// follow, is refused at once with ErrCheckpointSweepLockForeign, not
// retried for pruneLockWait as a busy lock, and a lock file this process
// cannot open is not called busy either. Before the follow-up each of
// these was ErrCheckpointSweepBusy: the sweeper skipped it at INFO every
// period, and prune and restore-offline waited it out and then failed
// saying another sweep was running.
func TestSweepLockRefusesWhatTheOwnerCannotUse(t *testing.T) {
	if !unixModes {
		t.Skip("files carry no owner uid this package reads here")
	}
	// lockNow takes the lock as restore-offline does and says how long the
	// refusal took.
	lockNow := func(t *testing.T, cps *Checkpoints) (time.Duration, error) {
		t.Helper()
		start := time.Now()
		unlock, err := cps.Lock(t.Context())
		took := time.Since(start)
		if err == nil {
			unlock()
		}
		if took > pruneLockWait/3 {
			t.Errorf("the refusal took %v; a busy lock is retried for %v, a foreign one must not be", took, pruneLockWait)
		}
		return took, err
	}
	foreign := func(t *testing.T, cps *Checkpoints, want string) {
		t.Helper()
		_, err := lockNow(t, cps)
		mustErrIs(t, err, ErrCheckpointSweepLockForeign)
		if errors.Is(err, ErrCheckpointSweepBusy) || !strings.Contains(err.Error(), want) {
			t.Fatalf("Lock = %v, want ErrCheckpointSweepLockForeign saying %q and not busy", err, want)
		}
		// prune-checkpoints and the sweeper take the same lock.
		if _, err := PruneCheckpoints(t.Context(), PruneOptions{Path: cps.a.dbPath}); !errors.Is(err, ErrCheckpointSweepLockForeign) {
			t.Fatalf("PruneCheckpoints = %v, want ErrCheckpointSweepLockForeign", err)
		}
	}

	t.Run("a symbolic link", func(t *testing.T) {
		cps := sweepLockArea(t)
		// Followed, the lock's create would truncate the link's target.
		target := filepath.Join(t.TempDir(), "joker-card")
		if err := os.WriteFile(target, []byte("why so serious"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(cps.Dir(), sweepLockName)); err != nil {
			t.Fatal(err)
		}
		foreign(t, cps, "not a regular file")
		if got, err := os.ReadFile(target); err != nil || string(got) != "why so serious" {
			t.Fatalf("the link's target = %q, %v; want it untouched", got, err)
		}
	})

	t.Run("a directory", func(t *testing.T) {
		cps := sweepLockArea(t)
		if err := os.Mkdir(filepath.Join(cps.Dir(), sweepLockName), 0o700); err != nil {
			t.Fatal(err)
		}
		foreign(t, cps, "not a regular file")
	})

	t.Run("another user's file", func(t *testing.T) {
		cps := sweepLockArea(t)
		p := filepath.Join(cps.Dir(), sweepLockName)
		if os.Geteuid() == 0 {
			// What a root prune before the first sweep left behind.
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(cps.Dir(), 1, -1); err != nil {
				t.Fatal(err)
			}
		} else {
			// A non-root process cannot make a file another user owns, but
			// where the kernel allows it, it can link one in. Linux's
			// fs.protected_hardlinks, or a bind-mounted file on another
			// device, refuses the link, and the subtest skips there;
			// TestPlanSweepLock covers the uid comparison everywhere. The
			// file must be one this process cannot write: a broken check
			// would hand the link to the lock's create, which truncates.
			const sys = "/etc/hosts"
			if f, err := os.OpenFile(sys, os.O_WRONLY, 0); err == nil {
				_ = f.Close()
				t.Skipf("this process may write %s, so a broken check could truncate it", sys)
			}
			if err := os.Link(sys, p); err != nil {
				t.Skipf("no file owned by another user can be linked in here: %v", err)
			}
			if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
				t.Skipf("the linked file is not a regular file (%v)", err)
			}
		}
		foreign(t, cps, "owned by uid ")
	})

	t.Run("an open this process may not make", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root opens anything")
		}
		cps := sweepLockArea(t)
		if err := os.Chmod(cps.Dir(), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cps.Dir(), 0o700) })
		_, err := lockNow(t, cps)
		if err == nil || errors.Is(err, ErrCheckpointSweepBusy) || errors.Is(err, ErrCheckpointSweepLockForeign) ||
			!errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "could not be opened") {
			t.Fatalf("Lock in a directory this process cannot write = %v, want the open's permission error, neither busy nor foreign", err)
		}
	})

	t.Run("the owner's lock file", func(t *testing.T) {
		cps := sweepLockArea(t)
		if err := os.WriteFile(filepath.Join(cps.Dir(), sweepLockName), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := lockNow(t, cps); err != nil {
			t.Fatalf("Lock over the owner's own lock file = %v, want it taken", err)
		}
	})
}

// TestSweepLockHeldByAnotherProcessIsBusy (R1-25 follow-up SF-1): the sweep
// lock another process holds is still ErrCheckpointSweepBusy, which prune
// and restore-offline wait for and the sweeper skips, now that a failed
// open of the lock file is not: pebble's lock reports an fcntl conflict as
// a bare errno and a failed open as a *PathError, and lock tells them
// apart by that.
func TestSweepLockHeldByAnotherProcessIsBusy(t *testing.T) {
	if db := os.Getenv(sweepLockChildEnv); db != "" {
		os.Exit(sweepLockChild(db))
	}
	if !unixModes {
		t.Skip("the lock is an fcntl lock only on unix")
	}
	cps := sweepLockArea(t)
	child := exec.Command(os.Args[0], "-test.run=^TestSweepLockHeldByAnotherProcessIsBusy$", "-test.count=1")
	child.Env = append(os.Environ(), sweepLockChildEnv+"="+cps.a.dbPath)
	child.Stderr = os.Stderr
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "LOCKED" {
		_ = stdin.Close()
		_ = child.Wait()
		t.Fatalf("the child said %q (%v), want LOCKED", line, err)
	}
	_, _, lerr := cps.a.lock()
	_ = stdin.Close()
	if err := child.Wait(); err != nil {
		t.Fatalf("the child: %v", err)
	}
	mustErrIs(t, lerr, ErrCheckpointSweepBusy)
	unlock, err := cps.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock after the child let go = %v, want it taken", err)
	}
	unlock()
}

// sweepLockChild holds the sweep lock of the store at db until its stdin
// closes, saying LOCKED once it holds it.
func sweepLockChild(db string) int {
	cps, err := CheckpointsOf(FSOf(vfs.Default), db, "", nil)
	if err != nil {
		fmt.Println("ERR", err)
		return 1
	}
	unlock, _, err := cps.a.lock()
	if err != nil {
		fmt.Println("ERR", err)
		return 1
	}
	defer unlock()
	fmt.Println("LOCKED")
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}
