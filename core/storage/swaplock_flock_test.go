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

// The R1-25 review B2 tests: the lock file stays 0600 and belongs to the
// state directory's owner, a root take gives it to that owner, and a take
// refuses (without locking or changing anything) a lock file that is not a
// regular file, has another link, is a symbolic link or has a stranger for
// an owner, and a state directory group or others can write. A take as root
// is osSwapLock with euid 0: the root path's decisions, run unprivileged.
// The real fchown still needs the permission it needs, so a test that
// reaches one makes it a change this user may make.

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
	dir := swapLockStat{mode: syscall.S_IFDIR | 0o700, nlink: 2, uid: svc, gid: svc}
	reg := func(perm uint32, nlink uint64, uid, gid uint32) swapLockStat {
		return swapLockStat{mode: syscall.S_IFREG | perm, nlink: nlink, uid: uid, gid: gid}
	}
	cases := []struct {
		name    string
		file    swapLockStat
		euid    int
		want    swapLockFix
		refuses string
	}{
		{name: "the service's own file", file: reg(0o600, 1, svc, svc), euid: svc},
		{name: "root created it", file: reg(0o600, 1, 0, 0), euid: 0, want: swapLockFix{chown: true}},
		{name: "root over the service's file", file: reg(0o600, 1, svc, svc), euid: 0},
		{name: "root over the service's file in another group", file: reg(0o600, 1, svc, other), euid: 0, want: swapLockFix{chown: true}},
		{name: "root created it under a tight umask", file: reg(0o400, 1, 0, 0), euid: 0, want: swapLockFix{chown: true, chmod: true}},
		{name: "the service's file left 0644", file: reg(0o644, 1, svc, svc), euid: svc, want: swapLockFix{chmod: true}},
		{name: "setuid bits are cleared", file: reg(0o4600, 1, svc, svc), euid: svc, want: swapLockFix{chmod: true}},
		{name: "a hard link", file: reg(0o600, 2, svc, svc), euid: svc, refuses: "2 links"},
		{name: "a hard link to a root file, as root", file: reg(0o600, 2, 0, 0), euid: 0, refuses: "2 links"},
		{name: "a FIFO", file: swapLockStat{mode: syscall.S_IFIFO | 0o600, nlink: 1, uid: svc, gid: svc}, euid: svc, refuses: "not a regular file"},
		{name: "a directory", file: swapLockStat{mode: syscall.S_IFDIR | 0o700, nlink: 2, uid: svc, gid: svc}, euid: 0, refuses: "not a regular file"},
		{name: "a stranger's file, as root", file: reg(0o600, 1, other, other), euid: 0, refuses: "owned by uid 1940"},
		{name: "a stranger's file, as the service", file: reg(0o600, 1, other, other), euid: svc, refuses: "owned by uid 1940"},
		{name: "the owner's 0644 file, as a stranger", file: reg(0o644, 1, svc, svc), euid: other, refuses: "does not own it"},
	}
	for _, c := range cases {
		got, err := planSwapLock(c.file, dir, c.euid)
		if c.refuses != "" {
			if err == nil || !strings.Contains(err.Error(), c.refuses) {
				t.Errorf("%s: err = %v, want a refusal naming %q", c.name, err, c.refuses)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: = %+v, %v; want %+v, nil", c.name, got, err, c.want)
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
// name are refused, as the service and as root, and nothing they name is
// changed. A FIFO's open returns (O_NONBLOCK) instead of waiting for a
// writer.
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
			// The open with O_CREATE refuses a directory (EISDIR) before the
			// file-type check can; either way there is no lock.
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
	f, err := openSwapLockFile(root)
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

// TestSwapLockRootTakeGivesTheFileToTheStateDirOwner: a root take over a
// lock file whose group is not the state directory's gives it the
// directory's uid and gid (a real fchown, to a group this user is in),
// sets a 0644 one back to 0600, and the service's take (this user, the
// directory's owner) still opens and locks it afterwards.
func TestSwapLockRootTakeGivesTheFileToTheStateDirOwner(t *testing.T) {
	state := t.TempDir()
	path := SwapLockPath(state)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	fileGid := statPath(t, path).Gid
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	dirGid := -1
	for _, g := range groups {
		if uint32(g) != fileGid {
			dirGid = g
			break
		}
	}
	if dirGid < 0 {
		t.Skip("this user is in one group, so no file here can have a group the state directory does not")
	}
	if err := os.Chown(state, -1, dirGid); err != nil {
		t.Skipf("cannot give the state directory group %d: %v", dirGid, err)
	}

	l, err := osSwapLock(state, true, 0)
	if err != nil {
		t.Fatalf("root take: %v", err)
	}
	st := statPath(t, path)
	dir := statPath(t, state)
	if st.Uid != dir.Uid || st.Gid != dir.Gid {
		t.Fatalf("after a root take the lock file is %d:%d, want the state directory's %d:%d", st.Uid, st.Gid, dir.Uid, dir.Gid)
	}
	if perm := os.FileMode(st.Mode) & os.ModePerm; perm != 0o600 {
		t.Fatalf("after a root take the lock file's mode is %04o, want 0600", perm)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := LockSwapShared(vfs.Default, state)
	if err != nil {
		t.Fatalf("the service's take after the root take: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
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

// TestSwapLockRootCreatesForTheService runs only as root: the root take
// creates the lock file in a state directory owned by another user and
// gives it to that user at 0600.
func TestSwapLockRootCreatesForTheService(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: it creates the lock file as root and checks the fchown to the state directory's owner")
	}
	const nobody = 65534
	state := t.TempDir()
	if err := os.Chown(state, nobody, nobody); err != nil {
		t.Fatal(err)
	}
	l, err := LockSwapExclusive(vfs.Default, state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	st := statPath(t, SwapLockPath(state))
	if st.Uid != nobody || st.Gid != nobody {
		t.Fatalf("the root-created lock file is %d:%d, want %d:%d", st.Uid, st.Gid, nobody, nobody)
	}
	if perm := os.FileMode(st.Mode) & os.ModePerm; perm != 0o600 {
		t.Fatalf("the root-created lock file's mode is %04o, want 0600", perm)
	}
}
