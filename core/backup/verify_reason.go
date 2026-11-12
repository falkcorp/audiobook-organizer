// file: core/backup/verify_reason.go
// version: 1.0.0
// guid: 07b88033-ee2b-4800-a7ab-9b9ede0d845b
// last-edited: 2026-11-12

package backup

import (
	"errors"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// VerifyReason is why a verification failed, as one of a closed set of codes.
//
// A verification's error text (VerifyResult.Detail) is free text: it quotes
// host paths, entry names and the errors of the files and the store it touched.
// It is for the operator's log and the on-disk sidecar. A reason code is the
// part of a verdict that may leave the process: an API body or a UI shows the
// code, never the text, so nothing about the host rides along with a failure.
type VerifyReason string

// The reason codes. They are a wire contract (the <archive>.verify.json "reason"
// member, the API's "reason" member), so a code is never renamed or reused.
const (
	VerifyReasonNone             VerifyReason = ""                  // the verification passed
	VerifyReasonChecksumMismatch VerifyReason = "checksum_mismatch" // the archive's bytes differ from its checksum sidecar
	VerifyReasonNoChecksum       VerifyReason = "no_checksum"       // the checksum sidecar is missing, unreadable or not a SHA-256
	VerifyReasonManifestMismatch VerifyReason = "manifest_mismatch" // the manifest is missing, invalid, or disagrees with the entries (an entry changed, was added or is absent)
	VerifyReasonUnsafeContent    VerifyReason = "unsafe_content"    // an entry name or type, or the archive file itself, that the reader refuses
	VerifyReasonMalformed        VerifyReason = "malformed_archive" // the compressed stream or the tar structure is truncated or corrupt
	VerifyReasonLimitExceeded    VerifyReason = "limit_exceeded"    // the archive is past a size or entry-count limit
	VerifyReasonNeedsNewerBuild  VerifyReason = "needs_newer_build" // the archived store needs a newer build than this one
	VerifyReasonStoreUnopenable  VerifyReason = "store_unopenable"  // the archived store did not open read-only
	VerifyReasonFailed           VerifyReason = "failed"            // anything else, including a cause not classified above
)

// Valid reports whether r is one of the codes above (the empty code included).
func (r VerifyReason) Valid() bool {
	switch r {
	case VerifyReasonNone, VerifyReasonChecksumMismatch, VerifyReasonNoChecksum, VerifyReasonManifestMismatch,
		VerifyReasonUnsafeContent, VerifyReasonMalformed, VerifyReasonLimitExceeded, VerifyReasonNeedsNewerBuild,
		VerifyReasonStoreUnopenable, VerifyReasonFailed:
		return true
	}
	return false
}

// Normalize is the code a verdict may be shown with: r when it is a code this build
// knows, VerifyReasonFailed when the verdict failed under a code it does not
// (a sidecar a person edited, or one a later build wrote), and none when the
// verdict passed, whatever the sidecar claims. Every reader of a persisted
// reason goes through it, so what leaves the package is always in the set.
func (r VerifyReason) Normalize(ok bool) VerifyReason {
	switch {
	case ok:
		return VerifyReasonNone
	case r.Valid() && r != VerifyReasonNone:
		return r
	}
	return VerifyReasonFailed
}

// openFailure marks a failure of the open level: the archived store was
// extracted and then did not open. Its text is the text it always had; the
// type is what lets reasonOf tell it from an extraction failure.
type openFailure struct {
	what string
	err  error
}

func (e *openFailure) Error() string { return "backup: " + e.what + ": " + e.err.Error() }
func (e *openFailure) Unwrap() error { return e.err }

// reasonOf sorts a verification failure into a code. The more specific causes
// come first because an error can carry several (verifyLegacy joins the scan's
// error with the manifest's); an error that matches none is VerifyReasonFailed,
// never a pass.
func reasonOf(err error) VerifyReason {
	if err == nil {
		return VerifyReasonNone
	}
	if errors.Is(err, ErrBackupTooNew) {
		return VerifyReasonNeedsNewerBuild
	}
	// An open-level failure is the store's, whatever the store's error wraps (a
	// corrupt table can surface io.ErrUnexpectedEOF): check it before the
	// archive-stream causes so it is not taken for one.
	if _, ok := errors.AsType[*openFailure](err); ok {
		return VerifyReasonStoreUnopenable
	}
	switch {
	case errors.Is(err, ErrChecksumMismatch):
		return VerifyReasonChecksumMismatch
	case errors.Is(err, ErrNoChecksum):
		return VerifyReasonNoChecksum
	case errors.Is(err, backuparchive.ErrLimit):
		return VerifyReasonLimitExceeded
	case errors.Is(err, backuparchive.ErrEntryName), errors.Is(err, backuparchive.ErrEntryType),
		errors.Is(err, backuparchive.ErrNotRegularFile), errors.Is(err, backuparchive.ErrDuplicateEntry):
		return VerifyReasonUnsafeContent
	case backuparchive.IsMalformed(err):
		return VerifyReasonMalformed
	case errors.Is(err, ErrManifest), errors.Is(err, backuparchive.ErrEntryMismatch):
		return VerifyReasonManifestMismatch
	}
	return VerifyReasonFailed
}
