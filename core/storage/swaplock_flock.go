// file: core/storage/swaplock_flock.go
// version: 1.0.0
// guid: ef78ecfd-f631-4f1b-8683-69001adbe6db
// last-edited: 2026-11-09

//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package storage

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// swapLockMode is the lock file's mode. Only the state directory's owner
// (the service user) and root open it, and flock(2) needs no more than a
// read-only descriptor.
const swapLockMode = 0o600

// StateDirOwner returns the uid that owns stateDir on the real filesystem.
// ok is false on any other filesystem (the crash kit's MemFS), where no
// other user can reach the files, and where files have no owner. The
// offline commands that write the store or the state directory run only as
// this uid (kernel.ErrNotStateDirOwner, R1-25 review B-1).
func StateDirOwner(fs vfs.FS, stateDir string) (uid uint32, ok bool, err error) {
	if vfs.Root(fs) != vfs.Default {
		return 0, false, nil
	}
	fi, err := os.Stat(stateDir)
	if err != nil {
		return 0, false, fmt.Errorf("storage: stat the state directory %s: %w", stateDir, err)
	}
	st, err := statOf(fi)
	if err != nil {
		return 0, false, fmt.Errorf("storage: the state directory %s: %w", stateDir, err)
	}
	return st.uid, true, nil
}

// swapLockStat is the part of fstat(2) a take checks, in widths that are
// the same on every platform (syscall.Stat_t's are not).
type swapLockStat struct {
	mode  uint32 // S_IFMT type and permission bits
	nlink uint64
	uid   uint32
}

func statOf(fi os.FileInfo) (swapLockStat, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return swapLockStat{}, fmt.Errorf("no stat(2) record for %s", fi.Name())
	}
	return swapLockStat{mode: uint32(st.Mode), nlink: uint64(st.Nlink), uid: st.Uid}, nil
}

// checkSwapLockDir refuses a state directory that group or others can
// write: whoever can write it can create the lock file first, or replace
// it, and so hold the lock against every opener and swap. The lock file's
// own 0600 and the owner check keep them from opening it. Read and search
// bits are allowed: the deploy unit's directory is 0755 (a bare mkdir -p),
// and neither config.EnsureSecureStateDir nor app.ResolveStateDir tightens
// an existing directory, so a 0700 rule would refuse those boots.
func checkSwapLockDir(dir swapLockStat) error {
	if perm := dir.mode & 0o7777; perm&0o022 != 0 {
		return fmt.Errorf("group or others can write it (mode %04o), so another user could create or replace the swap lock: chmod g-w,o-w it", perm)
	}
	return nil
}

// planSwapLock decides whether a take sets the open lock file's mode back
// to swapLockMode, or why it refuses the file, from fstat of the file and of
// the state directory and the effective uid. The one change goes through
// the descriptor, so only the inode the open reached is changed; the checks
// below make sure that inode is the lock file and nothing else (R1-25
// review B2):
//
//   - a regular file: O_NONBLOCK let the open of a planted FIFO return, and
//     this refuses it;
//   - one link: a hard link planted at the lock's name could name any file
//     on the filesystem;
//   - owned by the state directory's owner, the user the service runs as,
//     who is the only one that creates it (openSwapLockFile).
//
// Only the owner changes the file. Any other taker (root running a
// read-only command, say) locks the owner's file as it is, or refuses: a
// take never leaves a file the service cannot open, and never needs to give
// one back to it (R1-25 review B-1, which removed a root take's fchown).
func planSwapLock(file, dir swapLockStat, euid int) (chmod bool, err error) {
	if file.mode&syscall.S_IFMT != syscall.S_IFREG {
		return false, fmt.Errorf("it is not a regular file (type %#o)", file.mode&syscall.S_IFMT)
	}
	if file.nlink != 1 {
		return false, fmt.Errorf("it has %d links; a lock file has one", file.nlink)
	}
	if file.uid != dir.uid {
		return false, fmt.Errorf("it is owned by uid %d, not the state directory's owner (uid %d): chown it to that owner", file.uid, dir.uid)
	}
	chmod = file.mode&0o7777 != swapLockMode
	if chmod && uint32(euid) != dir.uid {
		return false, fmt.Errorf("its mode is %04o, not %04o, and only the state directory's owner (uid %d) changes it; this process is uid %d",
			file.mode&0o7777, swapLockMode, dir.uid, euid)
	}
	return chmod, nil
}

// openSwapLockFile opens the lock file in the state directory root,
// creating it 0600 if absent when create is set (the taker is the state
// directory's owner); any other taker only opens the file the owner made,
// so no take leaves a lock file the service cannot open. O_NONBLOCK keeps a
// planted FIFO from blocking the open (planSwapLock then refuses it). The
// mode is set at creation, not left to the take's fchmod: between the two,
// a stranger could open a 0644 file in a 0755 directory and hold the lock.
func openSwapLockFile(root *os.Root, create bool) (*os.File, error) {
	flag := os.O_RDONLY | syscall.O_NONBLOCK
	if create {
		flag |= os.O_CREATE
	}
	return root.OpenFile(SwapLockName, flag, swapLockMode)
}

// osSwapLock takes flock(2) on the state directory's lock file, shared or
// exclusive, without waiting, as the process whose effective uid is euid
// (os.Geteuid; a test passes another uid to drive a non-owner's take
// unprivileged). The file is opened anew on every take, so a second take
// in this process contends as one in another process would.
//
// The directory is opened as an os.Root and the file opened relative to it
// (openat), so the directory whose mode and owner are checked is the one
// the file is in. os.Root refuses a symbolic link that leaves the
// directory and follows one that stays in it, so the name is then checked
// to be the file opened (Lstat, which reads a link itself): a link at the
// lock's name is refused either way.
func osSwapLock(stateDir string, exclusive bool, euid int) (_ ExclusiveSwapLock, err error) {
	path := SwapLockPath(stateDir)
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return nil, fmt.Errorf("storage: open the swap lock's state directory %s: %w", stateDir, err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("storage: close the swap lock's state directory %s: %w", stateDir, cerr))
		}
	}()
	dfi, err := root.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("storage: stat the swap lock's state directory %s: %w", stateDir, err)
	}
	dir, err := statOf(dfi)
	if err != nil {
		return nil, fmt.Errorf("storage: the swap lock's state directory %s: %w", stateDir, err)
	}
	if err := checkSwapLockDir(dir); err != nil {
		return nil, fmt.Errorf("storage: the swap lock's state directory %s: %w", stateDir, err)
	}

	owner := uint32(euid) == dir.uid
	f, err := openSwapLockFile(root, owner)
	if err != nil {
		switch {
		case !owner && errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("storage: the swap lock %s does not exist, and only the state directory's owner (uid %d) creates it; this process is uid %d: run it as that user, or start the service once: %w",
				path, dir.uid, euid, err)
		case errors.Is(err, os.ErrPermission):
			return nil, fmt.Errorf("storage: open the swap lock %s: %w (uid %d cannot read it: it belongs to the state directory's owner, uid %d, mode %04o; run this as that user)",
				path, err, euid, dir.uid, swapLockMode)
		}
		return nil, fmt.Errorf("storage: open the swap lock %s: %w", path, err)
	}
	if err := secureSwapLock(root, f, dir, euid); err != nil {
		err = fmt.Errorf("storage: the swap lock %s: %w", path, err)
		// Nothing was locked, so the descriptor holds nothing; a failed
		// close still goes back with the refusal.
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("storage: close the swap lock %s: %w", path, cerr))
		}
		return nil, err
	}

	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	for {
		err = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			err = fmt.Errorf("%w (%s)", ErrSwapLockHeld, path)
		} else {
			err = fmt.Errorf("storage: take the swap lock %s: %w", path, err)
		}
		// errors.Is on ErrSwapLockHeld still matches the joined error.
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("storage: close the swap lock %s: %w", path, cerr))
		}
		return nil, err
	}
	return &flockSwapLock{f: f, path: path}, nil
}

// secureSwapLock checks the open lock file (the name is the file opened,
// then planSwapLock) and makes its one change, the owner's fchmod, through
// the descriptor.
func secureSwapLock(root *os.Root, f *os.File, dir swapLockStat, euid int) error {
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat it: %w", err)
	}
	if named, err := root.Lstat(SwapLockName); err != nil || !os.SameFile(named, fi) {
		return fmt.Errorf("the name is not the file opened (a symbolic link, or replaced since the open): %v", err)
	}
	file, err := statOf(fi)
	if err != nil {
		return err
	}
	chmod, err := planSwapLock(file, dir, euid)
	if err != nil {
		return err
	}
	if chmod {
		if err := f.Chmod(swapLockMode); err != nil {
			return fmt.Errorf("set its mode to %04o: %w", swapLockMode, err)
		}
	}
	return nil
}

// flockSwapLock is a take's open lock file; closing it releases the lock.
type flockSwapLock struct {
	f    *os.File
	path string
}

func (l *flockSwapLock) Close() error { return l.f.Close() }

// Downgrade asks flock(2) for the shared lock on the same descriptor,
// without waiting (ExclusiveSwapLock.Downgrade says why a failure may
// leave nothing held).
func (l *flockSwapLock) Downgrade() error {
	var err error
	for {
		err = syscall.Flock(int(l.f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	runtime.KeepAlive(l.f)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return fmt.Errorf("%w (%s): another process took it while it was turned shared", ErrSwapLockHeld, l.path)
	}
	return fmt.Errorf("storage: turn the swap lock %s shared: %w", l.path, err)
}
