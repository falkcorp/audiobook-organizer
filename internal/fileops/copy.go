// file: internal/fileops/copy.go
// version: 1.1.0
// guid: 3f6c1a58-9d2e-4b07-8c34-71ae5d9b0f42
// last-edited: 2026-09-01

package fileops

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// syncFile is a seam for testing. Durability cannot be asserted without
// crashing the machine, but whether the fsync is still WIRED UP can be — and it
// was the absence of exactly this call in two iTunes backup writers that
// motivated this package. Without the seam, deleting the Sync below leaves every
// test in this package green, which is how the guarantee got lost the first
// time.
var syncFile = func(f *os.File) error { return f.Sync() }

// CopyFile copies src to dst, creating dst or truncating it if it already
// exists. dst ends up with src's permission bits exactly (umask does not apply),
// the data is fsynced before return, and a partially-written dst is removed if
// the copy fails, so a failed call never leaves a truncated file behind.
//
// Use CopyFileExclusive when an existing destination must be refused rather
// than replaced.
func CopyFile(src, dst string) error {
	return copyBytes(src, dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, true)
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
	return copyBytes(src, dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, true)
}

// CopyFileInto copies src's bytes into dst, which MUST already exist — dst is
// opened without O_CREATE and truncated in place, keeping its identity (inode,
// hardlinks, any descriptor a caller still holds). dst is NOT removed if the
// copy fails, because the caller created it and owns its lifetime; that is the
// case for a file from os.CreateTemp that the caller will rename or clean up
// itself.
//
// dst still takes src's permission bits: os.CreateTemp produces 0600, and
// renaming such a file over a library file is exactly how the 2026-08-14 E08
// canary found 100 books gone share-unreadable.
func CopyFileInto(src, dst string) error {
	return copyBytes(src, dst, os.O_TRUNC|os.O_WRONLY, false)
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

	// CopyFileInto, not CopyFile: the temp file already exists and its mode
	// must become src's, which is exactly what CopyFileInto guarantees.
	// os.CreateTemp makes it 0600; renaming that over dst unchanged is how the
	// iTunes backups became owner-only.
	if err := CopyFileInto(src, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("fileops: rename %s -> %s: %w", tmpPath, dst, err)
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// copyBytes is the one implementation. removeOnErr controls whether a failed
// copy unlinks dst — true when this function created dst, false when the caller
// did.
func copyBytes(src, dst string, flag int, removeOnErr bool) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("fileops: cannot read source file %s: %w", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("fileops: stat source %s: %w", src, err)
	}
	perm := info.Mode().Perm()

	out, err := os.OpenFile(dst, flag, perm)
	if err != nil {
		// Undecorated on purpose — see CopyFileExclusive.
		if os.IsExist(err) {
			return err
		}
		// The operator hint used to live only in organizer's bespoke copy; a
		// failed destination open is almost always a full disk or a parent
		// directory the service cannot write, and every caller wants to know.
		return fmt.Errorf("fileops: cannot create destination file %s: %w (check parent directory permissions and disk space)", dst, err)
	}

	fail := func(format string, args ...any) error {
		_ = out.Close()
		if removeOnErr {
			_ = os.Remove(dst)
		}
		return fmt.Errorf(format, args...)
	}

	// The perm argument to OpenFile applies only when it CREATES dst, and even
	// then it is masked by umask. Chmod unconditionally so dst carries src's
	// mode exactly whether it was created here or handed to us pre-existing.
	if err := out.Chmod(perm); err != nil {
		return fail("fileops: chmod destination %s to %v: %w", dst, perm, err)
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
	return nil
}
