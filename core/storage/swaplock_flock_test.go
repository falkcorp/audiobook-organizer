// file: core/storage/swaplock_flock_test.go
// version: 1.0.0
// guid: b56b4d22-5fc9-4a3d-af98-c417fb77ce17
// last-edited: 2026-11-09

//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// The R1-25 review B2 and B-1 tests: the lock file stays 0600 and belongs
// to the state directory's owner, only the owner creates or changes it, and
// a take refuses (without locking or changing anything) a lock file that is
// not a regular file, has another link, is a symbolic link or is not the
// owner's, and a state directory group or others can write. A take as root
// is osSwapLock with euid 0, and one as a stranger with another uid: a
// non-owner's decisions, run unprivileged.

func statPath(t *testing.T, path string) *syscall.Stat_t {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s: no Stat_t", path)
	}
	return st
}

// takeWithin runs a shared take as euid and fails the test if it has not
// returned within a few seconds: a planted FIFO's open used to block
// forever. It returns the take's error, or one saying it succeeded.
func takeWithin(t *testing.T, state string, euid int) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		l, err := osSwapLock(state, false, euid)
		if err == nil {
			err = l.Close()
			if err == nil {
				err = errors.New("the take succeeded")
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the take did not return: the open blocked")
		return nil
	}
}

// TestPlanSwapLock is the decision table, with owners no unprivileged test
// can create.
func TestPlanSwapLock(t *testing.T) {
	const svc, other = 1939, 1940 // the state directory's owner, a stranger
	dir := swapLockStat{mode: syscall.S_IFDIR | 0o700, nlink: 2, uid: svc}
	reg := func(perm uint32, nlink uint64, uid uint32) swapLockStat {
		return swapLockStat{mode: syscall.S_IFREG | perm, nlink: nlink, uid: uid}
	}
	cases := []struct {
		name    string
		file    swapLockStat
		euid    int
		chmod   bool
		refuses string
	}{
		{name: "the service's own file", file: reg(0o600, 1, svc), euid: svc},
		{name: "root over the service's file", file: reg(0o600, 1, svc), euid: 0},
		{name: "a stranger over the service's file", file: reg(0o600, 1, svc), euid: other},
		{name: "the service's file left 0644", file: reg(0o644, 1, svc), euid: svc, chmod: true},
		{name: "setuid bits are cleared", file: reg(0o4600, 1, svc), euid: svc, chmod: true},
		{name: "the service's 0644 file, as root", file: reg(0o644, 1, svc), euid: 0, refuses: "only the state directory's owner"},
		{name: "the service's 0644 file, as a stranger", file: reg(0o644, 1, svc), euid: other, refuses: "only the state directory's owner"},
		{name: "a file root created, as root", file: reg(0o600, 1, 0), euid: 0, refuses: "owned by uid 0"},
		{name: "a file root created, as the service", file: reg(0o600, 1, 0), euid: svc, refuses: "owned by uid 0"},
		{name: "a hard link", file: reg(0o600, 2, svc), euid: svc, refuses: "2 links"},
		{name: "a hard link to a root file, as root", file: reg(0o600, 2, 0), euid: 0, refuses: "2 links"},
		{name: "a FIFO", file: swapLockStat{mode: syscall.S_IFIFO | 0o600, nlink: 1, uid: svc}, euid: svc, refuses: "not a regular file"},
		{name: "a directory", file: swapLockStat{mode: syscall.S_IFDIR | 0o700, nlink: 2, uid: svc}, euid: 0, refuses: "not a regular file"},
		{name: "a stranger's file, as root", file: reg(0o600, 1, other), euid: 0, refuses: "owned by uid 1940"},
		{name: "a stranger's file, as the service", file: reg(0o600, 1, other), euid: svc, refuses: "owned by uid 1940"},
	}
	for _, c := range cases {
		chmod, err := planSwapLock(c.file, dir, c.euid)
		if c.refuses != "" {
			if err == nil || !strings.Contains(err.Error(), c.refuses) {
				t.Errorf("%s: err = %v, want a refusal naming %q", c.name, err, c.refuses)
			}
			continue
		}
		if err != nil || chmod != c.chmod {
			t.Errorf("%s: = %v, %v; want %v, nil", c.name, chmod, err, c.chmod)
		}
	}

	for _, perm := range []uint32{0o700, 0o750, 0o755, 0o500} {
		if err := checkSwapLockDir(swapLockStat{mode: syscall.S_IFDIR | perm}); err != nil {
			t.Errorf("state directory %04o: %v, want accepted", perm, err)
		}
	}
	for _, perm := range []uint32{0o770, 0o720, 0o702, 0o777, 0o1777} {
		if err := checkSwapLockDir(swapLockStat{mode: syscall.S_IFDIR | perm}); err == nil {
			t.Errorf("state directory %04o: accepted, want refused", perm)
		}
	}
}

// TestSwapLockRefusesUnsafeLockFiles: a hard link, a symbolic link to a
// file inside the state directory, a FIFO and a directory at the lock's
// name are refused, as the service and as root (a non-owner, whose open
// creates nothing), and nothing they name is changed. A FIFO's open returns
// (O_NONBLOCK) instead of waiting for a writer.
func TestSwapLockRefusesUnsafeLockFiles(t *testing.T) {
	for _, who := range []struct {
		name string
		euid int
	}{{"service", os.Geteuid()}, {"root", 0}} {
		// unchanged plants a 0644 file at path and returns a check that its
		// mode and owner are as planted: a take that fchmodded or fchowned
		// through a link would change them.
		unchanged := func(t *testing.T, path string) func() {
			t.Helper()
			if err := os.WriteFile(path, []byte("ha"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			before := *statPath(t, path)
			return func() {
				t.Helper()
				after := statPath(t, path)
				if after.Mode != before.Mode || after.Uid != before.Uid || after.Gid != before.Gid {
					t.Fatalf("%s: what the link names changed: mode %o->%o uid %d->%d gid %d->%d",
						who.name, before.Mode, after.Mode, before.Uid, after.Uid, before.Gid, after.Gid)
				}
			}
		}
		t.Run(who.name+"/hard link", func(t *testing.T) {
			state := t.TempDir()
			// Joker's file sits outside the state directory.
			target := filepath.Join(t.TempDir(), "joker")
			check := unchanged(t, target)
			if err := os.Link(target, SwapLockPath(state)); err != nil {
				t.Skipf("no hard link here: %v", err)
			}
			if err := takeWithin(t, state, who.euid); errors.Is(err, ErrSwapLockHeld) || !strings.Contains(err.Error(), "2 links") {
				t.Fatalf("take over a hard link = %v, want a refusal naming the links", err)
			}
			check()
		})
		t.Run(who.name+"/symbolic link inside the state directory", func(t *testing.T) {
			state := t.TempDir()
			// os.Root follows a link that stays in the directory; the name
			// check refuses it.
			target := filepath.Join(state, "riddler")
			check := unchanged(t, target)
			if err := os.Symlink("riddler", SwapLockPath(state)); err != nil {
				t.Fatal(err)
			}
			if err := takeWithin(t, state, who.euid); !strings.Contains(err.Error(), "not the file opened") {
				t.Fatalf("take over a symbolic link = %v, want a refusal naming the link", err)
			}
			check()
		})
		t.Run(who.name+"/fifo", func(t *testing.T) {
			state := t.TempDir()
			if err := syscall.Mkfifo(SwapLockPath(state), 0o600); err != nil {
				t.Skipf("no FIFO here: %v", err)
			}
			if err := takeWithin(t, state, who.euid); !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("take over a FIFO = %v, want a refusal naming the file type", err)
			}
		})
		t.Run(who.name+"/directory", func(t *testing.T) {
			state := t.TempDir()
			if err := os.Mkdir(SwapLockPath(state), 0o700); err != nil {
				t.Fatal(err)
			}
			// The owner's open with O_CREATE refuses a directory (EISDIR)
			// before the file-type check can; a non-owner's plain open
			// reaches the file-type check. Either way there is no lock.
			if err := takeWithin(t, state, who.euid); errors.Is(err, ErrSwapLockHeld) || strings.Contains(err.Error(), "the take succeeded") {
				t.Fatalf("take over a directory = %v, want a refusal", err)
			}
		})
	}
}

// TestSwapLockRefusesAWritableStateDir: a state directory group or others
// can write is refused before the lock file is created; 0755 (the deploy
// unit's bare mkdir -p) and 0700 are taken.
func TestSwapLockRefusesAWritableStateDir(t *testing.T) {
	for _, perm := range []os.FileMode{0o770, 0o777} {
		state := t.TempDir()
		if err := os.Chmod(state, perm); err != nil {
			t.Fatal(err)
		}
		if _, err := LockSwapExclusive(vfs.Default, state); err == nil || !strings.Contains(err.Error(), "chmod g-w,o-w") {
			t.Fatalf("state directory %04o: take = %v, want a refusal naming the fix", perm, err)
		}
		if _, err := os.Lstat(SwapLockPath(state)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("state directory %04o: the refused take left a lock file (%v)", perm, err)
		}
	}
	for _, perm := range []os.FileMode{0o755, 0o700} {
		state := t.TempDir()
		if err := os.Chmod(state, perm); err != nil {
			t.Fatal(err)
		}
		l, err := LockSwapShared(vfs.Default, state)
		if err != nil {
			t.Fatalf("state directory %04o: %v", perm, err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSwapLockIsCreated0600: the open that creates the lock file creates it
// 0600, in a 0755 directory, not only once the take's fchmod has run.
func TestSwapLockIsCreated0600(t *testing.T) {
	state := t.TempDir()
	if err := os.Chmod(state, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	f, err := openSwapLockFile(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if perm := os.FileMode(statPath(t, SwapLockPath(state)).Mode) & os.ModePerm; perm != 0o600 {
		t.Fatalf("the open created the lock file %04o, want 0600 before any fchmod", perm)
	}
}

// TestSwapLockNonOwnerChangesNothing (R1-25 review B-1): a taker that is
// not the state directory's owner (root, as euid 0, or a stranger) never
// creates or changes the lock file. With no file it refuses, shared or
// exclusive, and creates none; over the owner's 0600 file it locks the file
// as it is; over the owner's 0644 file it refuses and leaves the mode, which
// the owner's own take then sets back to 0600.
func TestSwapLockNonOwnerChangesNothing(t *testing.T) {
	for _, euid := range []int{0, os.Geteuid() + 1} {
		state := t.TempDir()
		path := SwapLockPath(state)
		for _, exclusive := range []bool{false, true} {
			if l, err := osSwapLock(state, exclusive, euid); err == nil || !strings.Contains(err.Error(), "only the state directory's owner") {
				if l != nil {
					_ = l.Close()
				}
				t.Fatalf("uid %d, no lock file, exclusive %v: take = %v, want a refusal naming the owner", euid, exclusive, err)
			}
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("uid %d: a refused take created the lock file (%v)", euid, err)
		}

		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		before := *statPath(t, path)
		l, err := osSwapLock(state, true, euid)
		if err != nil {
			t.Fatalf("uid %d over the owner's 0600 file: %v", euid, err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if after := statPath(t, path); after.Mode != before.Mode || after.Uid != before.Uid || after.Gid != before.Gid {
			t.Fatalf("uid %d: the take changed the owner's file: mode %o->%o uid %d->%d gid %d->%d",
				euid, before.Mode, after.Mode, before.Uid, after.Uid, before.Gid, after.Gid)
		}

		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if l, err := osSwapLock(state, false, euid); err == nil || !strings.Contains(err.Error(), "only the state directory's owner") {
			if l != nil {
				_ = l.Close()
			}
			t.Fatalf("uid %d over the owner's 0644 file: take = %v, want a refusal naming the owner", euid, err)
		}
		if perm := os.FileMode(statPath(t, path).Mode) & os.ModePerm; perm != 0o644 {
			t.Fatalf("uid %d: the refused take changed the mode to %04o", euid, perm)
		}
		own, err := LockSwapShared(vfs.Default, state)
		if err != nil {
			t.Fatalf("the owner's take over its 0644 file: %v", err)
		}
		if err := own.Close(); err != nil {
			t.Fatal(err)
		}
		if perm := os.FileMode(statPath(t, path).Mode) & os.ModePerm; perm != 0o600 {
			t.Fatalf("after the owner's take the mode is %04o, want 0600", perm)
		}
	}
}

// TestStateDirOwner: the state directory's owner is this user on the real
// filesystem; another filesystem has none to check.
func TestStateDirOwner(t *testing.T) {
	state := t.TempDir()
	uid, ok, err := StateDirOwner(vfs.Default, state)
	if err != nil || !ok || uid != uint32(os.Geteuid()) {
		t.Fatalf("StateDirOwner = %d, %v, %v; want %d, true, nil", uid, ok, err, os.Geteuid())
	}
	if _, ok, err := StateDirOwner(vfs.NewMem(), state); ok || err != nil {
		t.Fatalf("StateDirOwner on a MemFS = %v, %v; want no owner, nil", ok, err)
	}
	if _, _, err := StateDirOwner(vfs.Default, filepath.Join(state, "missing")); err == nil {
		t.Fatal("StateDirOwner of a missing directory = nil, want an error")
	}
}

// TestSwapLockDowngrade (R1-25 review B3): an exclusive hold turned shared
// on the same file lets other shared takes in and still refuses an
// exclusive one; Close releases it.
func TestSwapLockDowngrade(t *testing.T) {
	state := t.TempDir()
	ex, err := LockSwapExclusive(vfs.Default, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockSwapShared(vfs.Default, state); !errors.Is(err, ErrSwapLockHeld) {
		t.Fatalf("a shared take under the exclusive hold = %v, want ErrSwapLockHeld", err)
	}
	if err := ex.Downgrade(); err != nil {
		t.Fatalf("Downgrade: %v", err)
	}
	sh, err := LockSwapShared(vfs.Default, state)
	if err != nil {
		t.Fatalf("a shared take beside the downgraded hold: %v", err)
	}
	if err := sh.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LockSwapExclusive(vfs.Default, state); !errors.Is(err, ErrSwapLockHeld) {
		t.Fatalf("an exclusive take beside the downgraded hold = %v, want ErrSwapLockHeld", err)
	}
	if err := ex.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := LockSwapExclusive(vfs.Default, state)
	if err != nil {
		t.Fatalf("an exclusive take once the downgraded hold closed: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
}
