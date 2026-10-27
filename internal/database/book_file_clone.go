// file: internal/database/book_file_clone.go
// version: 1.0.1
// guid: 7b0e4c18-52fd-4a6e-9c33-1d8a5f2e6b40
// last-edited: 2026-10-26

package database

import (
	"maps"
	"slices"
)

// cloneBookFile returns a copy of f that shares no memory with it: every
// pointer, slice and map field is copied, so writing through one of them on the
// copy (or on f afterwards) cannot reach the other.
//
// The batch book_file writers stage from a copy and, when a pass is thrown away
// (the lock set grew, an owner vanished), restore the caller's struct from the
// copy they took before staging. A plain `c := *f` copies the pointer and slice
// HEADERS, so that restore is only as good as the guarantee that no staging step
// ever writes through a pointer or into a slice or map in place, a guarantee
// nothing enforced. This makes the restore correct by construction instead.
//
// TestCloneBookFile_SharesNothing fills every field by reflection, so a field
// added to BookFile with a reference type that is not copied here fails that
// test rather than silently aliasing again.
func cloneBookFile(f *BookFile) BookFile {
	c := *f
	c.RawTags = maps.Clone(f.RawTags)
	c.AcoustIDFingerprint = slices.Clone(f.AcoustIDFingerprint)
	c.LastScanMtime = cloneBookFileScalar(f.LastScanMtime)
	c.LastScanSize = cloneBookFileScalar(f.LastScanSize)
	c.NeedsRescan = cloneBookFileScalar(f.NeedsRescan)
	c.FingerprintFailedAt = cloneBookFileScalar(f.FingerprintFailedAt)
	c.FingerprintFailureReason = cloneBookFileScalar(f.FingerprintFailureReason)
	c.FingerprintFailureDetail = cloneBookFileScalar(f.FingerprintFailureDetail)
	c.FingerprintDiagnosticJSON = cloneBookFileScalar(f.FingerprintDiagnosticJSON)
	c.AcoustIDOnlineLookedUpAt = cloneBookFileScalar(f.AcoustIDOnlineLookedUpAt)
	c.ImportedFromDelugeAt = cloneBookFileScalar(f.ImportedFromDelugeAt)
	c.IntroTranscription = cloneBookFileScalar(f.IntroTranscription)
	c.TranscribedTitle = cloneBookFileScalar(f.TranscribedTitle)
	c.TranscribedAuthor = cloneBookFileScalar(f.TranscribedAuthor)
	c.TranscribedNarrator = cloneBookFileScalar(f.TranscribedNarrator)
	c.TranscribedTranslator = cloneBookFileScalar(f.TranscribedTranslator)
	c.TranscribedCoverArtist = cloneBookFileScalar(f.TranscribedCoverArtist)
	c.IntroTranscribedAt = cloneBookFileScalar(f.IntroTranscribedAt)
	c.TranscribeStatus = cloneBookFileScalar(f.TranscribeStatus)
	c.TranscribeError = cloneBookFileScalar(f.TranscribeError)
	c.TranscribeAttemptedAt = cloneBookFileScalar(f.TranscribeAttemptedAt)
	c.IntroTimeoutAt = cloneBookFileScalar(f.IntroTimeoutAt)
	// IntroMeta holds only value fields, so copying the struct is a deep copy.
	c.IntroMeta = cloneBookFileScalar(f.IntroMeta)
	return c
}

// cloneBookFileScalar copies the value a pointer field points at. T must hold
// no reference fields of its own (every use above is a scalar, time.Time or
// IntroMeta).
func cloneBookFileScalar[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
