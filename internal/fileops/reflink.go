// file: internal/fileops/reflink.go
// version: 1.2.0
// guid: 3ef49b71-3f2d-4ecd-ad6b-abd926f282d1
// last-edited: 2026-09-01

// Reflink support — the single copy-on-write clone implementation for the
// whole codebase.
//
// # Why this is here and not in four places
//
// Before this file, reflink was implemented four times:
//
//	internal/organizer/reflink_unix.go       real FICLONE, O_EXCL destination
//	internal/deluge/import_unix.go           real FICLONE, os.Create destination
//	internal/reconcile/itunes_heal.go        shelled out to `cp --reflink=always`
//	internal/plugins/deluge/centralization.go a STUB that always returned an
//	                                          error, so every file that path
//	                                          touched was byte-copied in full
//
// The stub was invisible to every mechanism that normally catches a defect: it
// compiled, no test failed, its caller handled the returned error correctly,
// and the operation reported success. Only disk consumption suffered, and
// nothing asserted on disk consumption. It shipped alongside an exported
// helper in a sibling package whose doc comment explicitly offered itself to
// that very caller.
//
// # Destination semantics: never truncate
//
// Reflink refuses an existing destination rather than overwriting it. Two of
// the four originals used os.Create, which TRUNCATES; under a concurrent
// worker pool a stat-then-create race can zero a file another worker just
// finished writing. os.Link has never had that hazard because it fails with
// EEXIST, and organizer's copy documented O_EXCL for exactly this reason. That
// stricter behavior is now uniform, and ReflinkOrCopy's fallback honors it too
// — a caller that wants replacement must remove the destination itself, so the
// overwrite is written down at the call site instead of hiding in a helper.
package fileops

import (
	"errors"
	"io/fs"
)

// ErrReflinkUnsupported reports that the filesystem cannot clone extents (or
// the platform has no clone primitive). It is the signal to fall back to a
// byte copy; it never means the destination was rejected. Callers that must
// distinguish "cannot clone here" from "destination already exists" should
// test errors.Is(err, ErrReflinkUnsupported) and errors.Is(err, fs.ErrExist)
// respectively.
var ErrReflinkUnsupported = errors.New("fileops: reflink not supported on this filesystem")

// Reflink creates a copy-on-write clone of src at dst.
//
// dst must not already exist: an existing destination yields an error
// satisfying errors.Is(err, fs.ErrExist) and the destination is left untouched.
// On any failure no partial destination is left behind.
//
// A successful clone consumes no additional space until one side is written;
// the two files are independent inodes sharing extents, NOT hardlinks, so a
// later write to one never reaches the other.
//
// Returns ErrReflinkUnsupported when the filesystem cannot clone, which is the
// caller's cue to fall back — see ReflinkOrCopy.
func Reflink(src, dst string) error {
	return reflinkPlatform(src, dst)
}

// ReflinkOrCopy clones src to dst, falling back to a byte copy when the
// filesystem cannot clone.
//
// The fallback keeps Reflink's destination semantics: an existing dst is an
// error, never an overwrite. An fs.ErrExist from the clone attempt is returned
// as-is rather than being retried as a copy — falling back there would convert
// "refused to clobber" into "clobbered".
func ReflinkOrCopy(src, dst string) error {
	err := Reflink(src, dst)
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return err
	}
	return CopyFileIngestExclusive(src, dst)
}
