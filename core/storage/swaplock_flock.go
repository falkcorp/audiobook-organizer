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
)

// swapLockMode is the lock file's mode. Only the state directory's owner
// (the service user) and root open it, and flock(2) needs no more than a
// read-only descriptor.
const swapLockMode = 0o600

// swapLockStat is the part of fstat(2) a take checks, in widths that are
// the same on every platform (syscall.Stat_t's are not).
type swapLockStat struct {
	mode     uint32 // S_IFMT type and permission bits
	nlink    uint64
	uid, gid uint32
}

func statOf(fi os.FileInfo) (swapLockStat, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return swapLockStat{}, fmt.Errorf("no stat(2) record for %s", fi.Name())
	}
	return swapLockStat{mode: uint32(st.Mode), nlink: uint64(st.Nlink), uid: st.Uid, gid: st.Gid}, nil
}

// swapLockFix is what a take changes on the open lock file before it locks.
type swapLockFix struct {
	chown bool // to the state directory's uid and gid
	chmod bool // to swapLockMode
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

// planSwapLock decides what a take changes on the open lock file, or why it
// refuses it, from fstat of the file and of the state directory and the
// effective uid. Every change goes through the descriptor, so only the
// inode the open reached is changed; the checks below make sure that inode
// is the lock file and nothing else (R1-25 review B2):
//
//   - a regular file: O_NONBLOCK let the open of a planted FIFO return, and
//     this refuses it;
//   - one link: a hard link planted at the lock's name could name any file
//     on the filesystem (a root-owned one included), which a root take would
//     otherwise fchown and fchmod;
//   - owned by this process or by the state directory's owner.
//
// Then: a root take gives the file to the state directory's owner (uid and
// gid), so the service, which runs as that owner, can still open the file
// root created; and a mode other than swapLockMode is set back to it. A
// change this process may not make (it is neither root nor the file's
// owner) refuses rather than leaving the file as it is.
func planSwapLock(file, dir swapLockStat, euid int) (swapLockFix, error) {
	if file.mode&syscall.S_IFMT != syscall.S_IFREG {
		return swapLockFix{}, fmt.Errorf("it is not a regular file (type %#o)", file.mode&syscall.S_IFMT)
	}
	if file.nlink != 1 {
		return swapLockFix{}, fmt.Errorf("it has %d links; a lock file has one", file.nlink)
	}
	root := euid == 0
	if file.uid != uint32(euid) && file.uid != dir.uid {
		return swapLockFix{}, fmt.Errorf("it is owned by uid %d, neither this process (uid %d) nor the state directory's owner (uid %d)",
			file.uid, euid, dir.uid)
	}
	fix := swapLockFix{
		chown: root && (file.uid != dir.uid || file.gid != dir.gid),
		chmod: file.mode&0o7777 != swapLockMode,
	}
	if fix.chmod && !root && file.uid != uint32(euid) {
		return swapLockFix{}, fmt.Errorf("its mode is %04o, not %04o, and this process (uid %d) does not own it to change that",
			file.mode&0o7777, swapLockMode, euid)
	}
	return fix, nil
}

// openSwapLockFile opens, creating it 0600 if absent, the lock file in the
// state directory root. O_RDONLY|O_CREATE needs write permission on the
// directory only when the file is absent; O_NONBLOCK keeps a planted FIFO
// from blocking the open (planSwapLock then refuses it). The mode is set at
// creation, not left to the take's fchmod: between the two, a stranger
// could open a 0644 file in a 0755 directory and hold the lock.
func openSwapLockFile(root *os.Root) (*os.File, error) {
	return root.OpenFile(SwapLockName, os.O_RDONLY|os.O_CREATE|syscall.O_NONBLOCK, swapLockMode)
}

// osSwapLock takes flock(2) on the state directory's lock file, shared or
// exclusive, without waiting, as the process whose effective uid is euid
// (os.Geteuid; a test passes 0 to drive the root path unprivileged). The
// file is opened anew on every take, so a second take in this process
// contends as one in another process would.
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

	f, err := openSwapLockFile(root)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("storage: open the swap lock %s: %w (uid %d cannot read it: it belongs to the state directory's owner, uid %d, mode %04o; chown it to that owner)",
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
// then planSwapLock) and makes its changes through the descriptor: fchown
// to the state directory's owner, then fchmod, since a chown may clear mode
// bits.
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
	fix, err := planSwapLock(file, dir, euid)
	if err != nil {
		return err
	}
	if fix.chown {
		if err := f.Chown(int(dir.uid), int(dir.gid)); err != nil {
			return fmt.Errorf("give it to the state directory's owner (uid %d, gid %d): %w", dir.uid, dir.gid, err)
		}
	}
	if fix.chmod {
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
