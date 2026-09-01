// file: internal/fileops/copy.go
// version: 1.2.0
// guid: 3f6c1a58-9d2e-4b07-8c34-71ae5d9b0f42
// last-edited: 2026-09-01

package fileops

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Package fileops — the single byte-copy implementation.
//
// # Why this file exists
//
// Six hand-rolled file copies lived in this repository, three of them inside
// this very package. They disagreed on four axes that each silently change
// what a caller gets:
//
//	                                  existing dst   dst mode      fsync  partial dst
//	fileops/reflink.go                refuse         0664 literal  no     removed
//	fileops/write_tags_safe.go        truncate       src's mode    yes    kept
//	fileops/safe_operations.go        truncate       src's mode    yes    kept
//	organizer/organizer.go            (temp+rename)  umask default yes    removed
//	itunes/service/transfer.go        (temp+rename)  0600 (!)      NO     removed
//	itunes/service/writeback_batcher  truncate       0644 literal  NO     kept
//
// Two of those disagreements were live defects, not preferences:
//
//   - itunes/service/transfer.go wrote its ITL backup through os.CreateTemp,
//     which creates at 0600, and then renamed it into place — so every backup
//     that path produced was owner-only. That is the same failure the
//     2026-08-14 E08 canary caught in write_tags_safe (100 books went
//     share-unreadable after a tag rewrite replaced an 0664 file with an 0600
//     one); it simply had not been looked for on the iTunes side.
//   - Neither ITL backup writer fsynced. A backup that is still in page cache
//     when the original is mutated is not a backup: one crash loses both
//     copies. The whole point of those two call sites is to survive a crash.
//
// The three functions below are named for the question each answers, and they
// are deliberately three rather than one flags-struct: "may I clobber dst" is
// not a tuning knob, it is the contract, and a caller that has to pass
// `Exclusive: false` to get the destructive behaviour will pass it by accident.
//
// Atomicity is NOT one of the axes here. Whether a copy lands via a temp file
// and a rename is the caller's policy — organizer needs safeRename's
// refuse-on-collision, the ITL writers need plain replace — so callers keep
// their own rename step and use these for the bytes.
//
// # Permission bits are a per-caller decision, not a package default
//
// The first version of this file applied the SOURCE's mode everywhere, which
// re-introduced the very failure it was written to prevent. The three
// operations want three different answers and none of them can be inferred:
//
//   - Copying a file to make a sibling of ITSELF — a backup, or a temp that
//     will be renamed back over it — wants the source's mode, because the
//     source IS the file whose mode is correct. (CopyFile, CopyFileInto)
//   - INGESTING a foreign file into the library wants the library's default,
//     NOT the source's. The source came from a download client whose umask is
//     none of our business: a torrent client running umask 077 hands us an
//     0600 .m4b, and adopting that mode makes every organized file owner-only
//     and takes the Samba share dark, with no error anywhere. Before this
//     package the ingest paths used os.Create / a 0664 literal, both floored
//     by our own umask, and were safe by accident. (CopyFileIngest)
//   - REPLACING an existing file wants the DESTINATION's mode. Restoring a
//     library from a backup written months ago under a different writer's
//     hardcoded 0644 must not stamp that 0644 onto a live 0664 library.
//     (CopyFileAtomic)

// syncFile is a seam for testing. Durability cannot be asserted without
// crashing the machine, but whether the fsync is still WIRED UP can be — and it
// was the absence of exactly this call in two iTunes backup writers that
// motivated this package. Without the seam, deleting the Sync below leaves every
// test in this package green, which is how the guarantee got lost the first
// time.
var syncFile = func(f *os.File) error { return f.Sync() }

// syncDir fsyncs a directory so that a newly created file's NAME is durable,
// not just its bytes. fsync on a file commits the data; on ext4 (data=ordered)
// and XFS it does not commit the parent directory entry, so a crash can leave
// the blocks allocated under no name at all. For a backup — a file whose sole
// purpose is to exist after a crash — syncing the data and not the name
// delivers half the guarantee.
//
// Directory fsync is not universally supported: it returns EINVAL or ENOTSUP
// on some filesystems (and on network mounts), where it has never been the
// durability mechanism anyway. Those two are treated as success. Every other
// error is surfaced, because at that point something is genuinely wrong with
// the directory we just wrote into.
//
// It goes through a seam for the same reason syncFile does: deleting the
// directory fsync from CopyFileAtomic left every test in this package green.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("fileops: open directory %s for sync: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
			return nil
		}
		return fmt.Errorf("fileops: sync directory %s: %w", dir, err)
	}
	return nil
}

// LibraryFileMode is the mode an INGESTED library file is created with, before
// the process umask is applied — the same 0664 the reflink path has always
// used. Group-writable on purpose: the library is served over a share.
const LibraryFileMode = 0o664

// modeSource says where a destination's permission bits come from. See the
// "Permission bits are a per-caller decision" section above; there is no
// correct default, which is why this is not a defaultable option.
type modeSource int

const (
	// modeFromSource: dst takes src's permission bits exactly.
	modeFromSource modeSource = iota
	// modeLibraryDefault: dst is created at LibraryFileMode and left to the
	// umask, exactly as os.Create/0664 did before this package existed.
	modeLibraryDefault
	// modeKeepDest: dst keeps the mode it already has; if it does not exist,
	// falls back to the source's.
	modeKeepDest
)

// CopyFile copies src to dst, creating dst or truncating it if it already
// exists. dst takes src's permission bits exactly (umask does not apply), the
// data and dst's directory entry are both fsynced before return, and a
// partially-written dst is removed if the copy fails.
//
// This is the "make a sibling copy of this file" operation: a backup, or a
// second copy of something the library already owns. To bring a FOREIGN file
// into the library, use CopyFileIngest — adopting an external file's mode is
// how a download client's umask becomes the library's permissions.
func CopyFile(src, dst string) error {
	return copyBytes(src, dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, modeFromSource, true, true)
}

// CopyFileExclusive is CopyFile except that an existing dst is refused: it
// returns the os.OpenFile error unwrapped, so both errors.Is(err, fs.ErrExist)
// and os.IsExist(err) report true. (os.IsExist does not unwrap, so this error
// must not be decorated — Reflink's fallback and organizer's race recovery both
// branch on it.)
//
// The exclusivity is O_EXCL on dst itself, not a stat-then-create check, so two
// concurrent callers cannot both believe they won.
func CopyFileExclusive(src, dst string) error {
	return copyBytes(src, dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, modeFromSource, true, true)
}

// CopyFileIngest copies a file from OUTSIDE the library into it. dst is created
// at LibraryFileMode under the process umask and does NOT take src's mode.
//
// The source of an ingest is a download client, a watch folder, or another
// user's share — none of whose umask is ours. A client running umask 077 hands
// us an 0600 file; adopting that makes every organized library file owner-only
// and the share stops serving them, with every copy reporting success. The
// paths this replaces (os.Create in organizer, a 0664 literal in the reflink
// fallback) were umask-floored and so could never be more restrictive than the
// library's own policy.
func CopyFileIngest(src, dst string) error {
	return copyBytes(src, dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, modeLibraryDefault, true, true)
}

// CopyFileIngestExclusive is CopyFileIngest with an existing dst refused, on
// the same terms as CopyFileExclusive. This is the fallback under
// ReflinkOrCopy, so it must match the reflink path's non-truncating contract.
func CopyFileIngestExclusive(src, dst string) error {
	return copyBytes(src, dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, modeLibraryDefault, true, true)
}

// CopyFileInto copies src's bytes into dst, which MUST already exist — dst is
// opened without O_CREATE and truncated in place, keeping its identity (inode,
// hardlinks, any descriptor a caller still holds). dst is NOT removed if the
// copy fails, because the caller created it and owns its lifetime; that is the
// case for a file from os.CreateTemp that the caller will rename or clean up
// itself.
//
// dst takes SRC's permission bits, not its own: the caller's temp file is an
// artifact (os.CreateTemp produces 0600) and src is the file being rewritten,
// whose mode is the one that must survive. Renaming an 0600 temp over a library
// file is exactly how the 2026-08-14 E08 canary found 100 books gone
// share-unreadable.
//
// No directory fsync: dst's name already exists and is already durable.
func CopyFileInto(src, dst string) error {
	return copyBytes(src, dst, os.O_TRUNC|os.O_WRONLY, modeFromSource, false, false)
}

// copyBytes is the one implementation.
//
// removeOnErr controls whether a failed copy unlinks dst — true when this
// function created dst, false when the caller did. syncParent controls the
// directory fsync that makes a NEWLY CREATED dst's name durable.
func copyBytes(src, dst string, flag int, mode modeSource, removeOnErr, syncParent bool) error {
	return copyBytesTo(src, dst, dst, flag, mode, removeOnErr, syncParent)
}

// copyBytesTo is copyBytes with the mode resolved against modePath rather than
// against the file being written. CopyFileAtomic writes a temp file that it
// then renames over dst: the mode that matters is dst's, and the temp's own
// 0600 is an artifact of os.CreateTemp. Resolving against the temp is how the
// first version of CopyFileAtomic produced 0600 destinations — the exact bug
// this package exists to fix, reproduced one function away from its own
// changelog entry.
func copyBytesTo(src, dst, modePath string, flag int, mode modeSource, removeOnErr, syncParent bool) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("fileops: cannot read source file %s: %w", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("fileops: stat source %s: %w", src, err)
	}

	// Refuse a self-copy. O_TRUNC on the same inode empties the file before
	// io.Copy reads a byte, and io.Copy then reports success having moved
	// nothing: total data loss returning nil. Two of the six implementations
	// this package replaced (both temp-then-rename) were immune by accident,
	// so the consolidation must not lose the property. os.SameFile rather than
	// a path comparison, because a hardlink or a symlink reaches the same
	// inode under a different name.
	if dstInfo, statErr := os.Stat(dst); statErr == nil && os.SameFile(info, dstInfo) {
		return fmt.Errorf("fileops: refusing to copy %s onto itself (%s is the same file)", src, dst)
	}

	perm, permErr := resolvePerm(modePath, info, mode)
	if permErr != nil {
		return permErr
	}

	out, err := os.OpenFile(dst, flag, perm)
	if err != nil {
		// Undecorated on purpose — see CopyFileExclusive.
		if os.IsExist(err) {
			return err
		}
		if flag&os.O_CREATE == 0 && errors.Is(err, fs.ErrNotExist) {
			// Distinguish "the caller broke this function's contract" from the
			// disk-space/permissions case below; only CopyFileInto can get here.
			return fmt.Errorf("fileops: destination %s does not exist and this "+
				"copy does not create it: %w", dst, err)
		}
		return fmt.Errorf("fileops: cannot create destination file %s: %w "+
			"(check parent directory permissions and disk space)", dst, err)
	}

	fail := func(format string, args ...any) error {
		_ = out.Close()
		if removeOnErr {
			_ = os.Remove(dst)
		}
		return fmt.Errorf(format, args...)
	}

	// The perm argument to OpenFile applies only when it CREATES dst, and even
	// then it is masked by umask. Chmod when the bits on disk are not already
	// the ones we want — and only then: fchmod returns EPERM on CIFS without
	// unix extensions and on vfat/exfat, and an unconditional chmod would turn
	// a correct copy on a network share into a hard failure. modeLibraryDefault
	// deliberately never chmods, because deferring to the umask IS its policy.
	if mode != modeLibraryDefault {
		if cur, statErr := out.Stat(); statErr != nil || cur.Mode().Perm() != perm {
			if err := out.Chmod(perm); err != nil {
				return fail("fileops: chmod destination %s to %v: %w", dst, perm, err)
			}
		}
	}
	if _, err := io.Copy(out, in); err != nil {
		return fail("fileops: copy %s -> %s: %w", src, dst, err)
	}
	// Sync before Close: a backup still in page cache is not a backup.
	if err := syncFile(out); err != nil {
		return fail("fileops: sync destination %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		if removeOnErr {
			_ = os.Remove(dst)
		}
		return fmt.Errorf("fileops: close destination %s: %w", dst, err)
	}
	// The bytes are durable; the NAME is not until the directory is synced.
	if syncParent {
		if err := syncDir(filepath.Dir(dst)); err != nil {
			if removeOnErr {
				_ = os.Remove(dst)
			}
			return err
		}
	}
	return nil
}

// resolvePerm answers "what mode should dst end up with" for one policy.
func resolvePerm(dst string, srcInfo fs.FileInfo, mode modeSource) (fs.FileMode, error) {
	switch mode {
	case modeLibraryDefault:
		return LibraryFileMode, nil
	case modeKeepDest:
		if dstInfo, err := os.Stat(dst); err == nil {
			return dstInfo.Mode().Perm(), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("fileops: stat destination %s: %w", dst, err)
		}
		// No destination yet — there is no mode to preserve, so the source's
		// is the only defensible answer.
		return srcInfo.Mode().Perm(), nil
	default:
		return srcInfo.Mode().Perm(), nil
	}
}

// CopyFileAtomic copies src over dst so that dst is never observed truncated or
// half-written: the bytes go to a temp file in dst's own directory, are fsynced,
// and are then renamed over dst in one step. dst does not have to exist.
//
// This is the shape every "restore the live file from its backup" path needs.
// Two of them had hand-rolled it without the fsync, and a third
// (itunes/service/writeback_batcher's post-rename-validation rollback) used a
// plain os.WriteFile over the LIVE iTunes library — a crash partway through
// that write leaves neither a good library nor a good original, which is the
// one outcome a rollback exists to prevent.
//
// The temp file is named ".<base>.tmp-*" so it can never be mistaken for a
// ".bak-*" rotation candidate by the ITL retention sweeps.
//
// The parent directory fsync that makes the rename itself crash-durable is
// best-effort: the rename has already succeeded by that point, and failing the
// call would report a completed restore as a failure — the strictly worse
// error. It is not silently swallowed for convenience; there is nothing left to
// undo.
func CopyFileAtomic(src, dst string) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fileops: create temp beside %s: %w", dst, err)
	}
	tmpPath := tmp.Name()
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("fileops: close temp %s: %w", tmpPath, cerr)
	}

	// modeKeepDest: the temp is renamed OVER dst, so it must carry dst's own
	// mode, not src's. This path restores a live iTunes library from a backup,
	// and the backups already on disk were written by two earlier writers at a
	// hardcoded 0644 and 0600 — stamping either onto a 0664 group-writable
	// library takes the share down at exactly the moment a rollback runs.
	// os.CreateTemp makes the temp 0600, so it must be chmodded either way.
	if err := copyBytesTo(src, tmpPath, dst, os.O_TRUNC|os.O_WRONLY, modeKeepDest, false, false); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("fileops: rename %s -> %s: %w", tmpPath, dst, err)
	}
	// syncDir, not an inline best-effort open: routed through the same seam as
	// every other durability call in this file so that deleting it fails a
	// test. The first version of this function opened and synced the directory
	// inline and discarded both errors — and removing all four lines left every
	// test in the package green.
	return syncDir(dir)
}
