// file: core/library/placeholder.go
// version: 1.0.0
// guid: e32a84be-9156-4dcd-911a-707f4e977cbc
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// Publishing a name that must not already exist needs one atomic step that
// refuses an existing name. The library has three, in order:
//
//  1. a hard link (the temp stays, and goes afterwards);
//  2. a no-replace rename (renameat2 RENAME_NOREPLACE, renameatx_np RENAME_EXCL),
//     or for a regular file link-then-unlink;
//  3. where the filesystem has neither (exFAT, FAT, an SMB or FUSE mount without
//     hard links or the flag): reserve the name first. An exclusive create (or
//     mkdir) refuses an existing name atomically; the transfer then renames over
//     the entry it made itself. The journal says first that a placeholder may
//     exist (Placing), so a crash between the create and the rename leaves an
//     entry recovery can tell from a user's file: empty, and the path it was to be
//     replaced by still where it was.
//
// What step 3 cannot do is stop another process from deleting the placeholder
// and creating a file of its own between the check and the rename. That window
// is narrow (two syscalls apart) and only another writer to the same directory
// can use it; it is the cost of publishing on a filesystem that offers no atomic
// primitive, and it is disclosed in seams.d/F7-06.md. Nothing outside the
// placeholder protocol replaces a name.

// placeMark journals that a placeholder may exist at the destination, and which
// path is being renamed there (nil clears it). The row it writes belongs to the
// caller: a transfer's row, a recovery's copy of it, or a quarantine record.
type placeMark func(ctx context.Context, placing *Path) error

// publishTemp makes dst the temp's file, never replacing anything: a hard link
// where the filesystem has them (the temp stays and the caller removes it),
// otherwise a no-replace rename (the temp is consumed). Either way dst exists on
// success and an existing dst is fs.ErrExist.
func (m *manager) publishTemp(ctx context.Context, mark placeMark, dfs fsOps, root RootID, tmp, dst string) error {
	err := dfs.Link(tmp, dst)
	if !errors.Is(err, libfs.ErrNoHardLinks) {
		return err
	}
	return m.renameExclusive(ctx, mark, dfs, Path{Root: root, Rel: tmp}, dfs, dst, false)
}

// renameExclusive renames from (in sfs) to to (in dfs) without replacing: the
// kernel's no-replace rename where there is one, otherwise the placeholder
// protocol. isDir says what is being renamed (the placeholder has its type).
// A rename across devices is ErrCrossDevice and nothing was made.
func (m *manager) renameExclusive(ctx context.Context, mark placeMark, sfs fsOps, from Path, dfs fsOps, to string, isDir bool) error {
	err := sfs.renameNoReplaceInto(dfs, from.Rel, to)
	if !errors.Is(err, libfs.ErrNoAtomicRename) {
		return err
	}
	return m.renameViaPlaceholder(ctx, mark, sfs, from, dfs, to, isDir)
}

// renameFresh is renameExclusive for a name that is unique by construction (a
// ULID names it: the quarantine's holding path, a replaced file's aside): no
// other writer is creating it, so where the filesystem has no atomic no-replace
// rename the name is checked absent and the rename goes ahead without a
// placeholder.
func (m *manager) renameFresh(sfs fsOps, from string, dfs fsOps, to string) error {
	err := sfs.renameNoReplaceInto(dfs, from, to)
	if !errors.Is(err, libfs.ErrNoAtomicRename) {
		return err
	}
	if _, there, perr := peek(dfs, to); perr != nil {
		return perr
	} else if there {
		return &fs.PathError{Op: "rename", Path: to, Err: fs.ErrExist}
	}
	return sfs.renameOverInto(dfs, from, to)
}

// renameViaPlaceholder is step 3 above.
func (m *manager) renameViaPlaceholder(ctx context.Context, mark placeMark, sfs fsOps, from Path, dfs fsOps, to string, isDir bool) error {
	bg := context.WithoutCancel(ctx)
	if err := mark(ctx, &from); err != nil {
		return err
	}
	ph, made, err := reserveName(dfs, to, isDir)
	if err != nil {
		if made {
			// Something was made and cannot be identified, so it is not removed
			// here: the intent stays journaled and recovery clears it.
			return fmt.Errorf("library: reserved %s but cannot confirm it: %w", to, err)
		}
		// A refused reservation made nothing: the intent is withdrawn.
		return errors.Join(err, mark(bg, nil))
	}
	// Still the entry this call made? Anything else is another writer's: the
	// placeholder's identity was read from the open descriptor that created it, so
	// a swap after the create is seen here.
	if cur, cerr := dfs.Lstat(to); cerr != nil || !libfs.SameFile(cur, ph) {
		return errors.Join(&fs.PathError{Op: "rename", Path: to, Err: fs.ErrExist}, cerr, mark(bg, nil))
	}
	if err := sfs.renameOverInto(dfs, from.Rel, to); err != nil {
		rerr := removeIfSame(dfs, to, ph)
		if rerr != nil {
			// The placeholder could not be taken away: leave the intent journaled.
			return errors.Join(err, rerr)
		}
		return errors.Join(err, mark(bg, nil))
	}
	if merr := mark(bg, nil); merr != nil {
		// The rename is done; a stale intent is harmless (recovery removes an entry
		// only while the renamed path still exists).
		m.log.Warn("library: could not clear a placeholder mark after the rename; recovery will", "to", to, "err", merr)
	}
	return nil
}

// reserveName creates the placeholder: an exclusive empty file, or an exclusive
// empty directory when a directory is being renamed there. The file has mode 0,
// so nothing is opened for writing through it. It returns the entry's identity, read from the
// descriptor that created it (a directory has none: it is read right after), and
// whether anything was made.
func reserveName(dfs fsOps, name string, isDir bool) (ph fs.FileInfo, made bool, err error) {
	if isDir {
		// Owner-only, not mode 0: rename(2) onto a directory needs to be able to
		// enter it on some systems, and nobody but the owner may add to it.
		if err := dfs.mkdirExclusive(name, 0o700); err != nil {
			return nil, false, err
		}
		st, err := dfs.Lstat(name)
		return st, true, err
	}
	f, err := dfs.CreateExclusive(name, 0)
	if err != nil {
		return nil, false, err
	}
	st, serr := f.Stat()
	return st, true, errors.Join(serr, f.Close())
}

// removeIfSame removes name when it is still the entry ph describes.
func removeIfSame(fsys fsOps, name string, ph fs.FileInfo) error {
	cur, err := fsys.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !libfs.SameFile(cur, ph):
		return nil // not ours any more
	}
	if err := fsys.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// emptyEntry is the shape of a placeholder: an empty regular file or a
// directory (a directory that is not empty is refused by the removal itself).
func emptyEntry(st fs.FileInfo) bool {
	return st.IsDir() || (st.Mode().IsRegular() && st.Size() == 0)
}

// dropPlaceholder is recovery's half: a journal row says a placeholder rename onto
// to was under way. The empty entry at to is removed only if the path being
// renamed there (from) still exists, is not the same file, and the entry has a
// placeholder's shape; anything else at to is not this transfer's and stays.
// It reports whether to is now free.
func (m *manager) dropPlaceholder(fromFS fsOps, from string, toFS fsOps, to string) (bool, error) {
	fst, fok, err := peek(fromFS, from)
	if err != nil || !fok {
		return false, err
	}
	tst, tok, err := peek(toFS, to)
	if err != nil || !tok {
		return false, err
	}
	if libfs.SameFile(fst, tst) || !emptyEntry(tst) {
		return false, nil
	}
	if err := toFS.Remove(to); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		m.log.Warn("library: could not remove the placeholder a crashed transfer left", "to", to, "err", err)
		return false, nil
	}
	return true, nil
}

// classifyUnsupported names a failure that means "this filesystem cannot do that"
// (no hard links, no atomic rename, an errno that is ENOTSUP) ErrNotSupported, so
// a caller tests one sentinel and never a raw errno. The original error stays in
// the chain.
func classifyUnsupported(err error) error {
	if err == nil || errors.Is(err, ErrNotSupported) || !errors.Is(err, errors.ErrUnsupported) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrNotSupported, err)
}
