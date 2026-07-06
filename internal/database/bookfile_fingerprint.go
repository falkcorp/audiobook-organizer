// file: internal/database/bookfile_fingerprint.go
// version: 1.2.0
// guid: d1e2f3g4-h5i6-7890-jkml-no1234567890
// last-edited: 2026-07-05

package database

import "time"

// GetAcoustIDSeg0 satisfies fingerprint.FileWithFingerprint. Returns a
// non-empty string when the file has any fingerprint data, allowing
// fingerprint.ComputeFingerprintFields to compute the per-book
// fingerprint_status badge.
//
// After fable5 T019, AcoustIDSeg0..6 are stripped from memdb rows before
// insertion (memdb_strip.go). To preserve the badge for memdb-sourced callers
// (GetBookFilesForIDs → ComputeFingerprintFields), we fall back to
// AcoustIDFingerprintDurationSec: the duration is retained in memdb (it is a
// float64 scalar, never stripped) and is non-zero only when a whole-file
// chromaprint was successfully computed.
//
// Pebble-direct callers are unaffected — their BookFile copies have the
// original AcoustIDSeg0 value populated from storage.
func (bf *BookFile) GetAcoustIDSeg0() string {
	if bf == nil {
		return ""
	}
	if bf.AcoustIDSeg0 != "" {
		return bf.AcoustIDSeg0
	}
	// Fallback: whole-file fingerprint present (AcoustIDSeg0 stripped from
	// memdb rows by stripBookFileForMemdb — use duration as presence proxy).
	if bf.AcoustIDFingerprintDurationSec > 0 {
		return "wf" // non-empty sentinel; callers only test != ""
	}
	return ""
}

// GetUpdatedAt returns the UpdatedAt field for use with fingerprint.FileWithFingerprint interface.
func (bf *BookFile) GetUpdatedAt() time.Time {
	if bf == nil {
		return time.Time{}
	}
	return bf.UpdatedAt
}

// GetAcoustIDSeg0 satisfies fingerprint.FileWithFingerprint for BookFileCore
// (the STOREFID-typed projection returned by GetBookFilesForIDsCore).
// BookFileCore never carries the raw AcoustIDSeg0 field (it is one of the
// heavy fields stripped from the memdb projection — see BookFileCore's doc
// comment), so this always uses the AcoustIDFingerprintDurationSec presence
// proxy described on (*BookFile).GetAcoustIDSeg0 above. This is not a
// behavior change for memdb-sourced callers: AcoustIDSeg0 is already always
// "" for memdb rows, so (*BookFile).GetAcoustIDSeg0 already fell through to
// this same proxy for every caller of the old GetBookFilesForIDs.
func (c *BookFileCore) GetAcoustIDSeg0() string {
	if c == nil {
		return ""
	}
	if c.AcoustIDFingerprintDurationSec > 0 {
		return "wf" // non-empty sentinel; callers only test != ""
	}
	return ""
}

// GetUpdatedAt returns the UpdatedAt field for use with
// fingerprint.FileWithFingerprint interface.
func (c *BookFileCore) GetUpdatedAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	return c.UpdatedAt
}
