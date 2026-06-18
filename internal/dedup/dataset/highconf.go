// file: internal/dedup/dataset/highconf.go
// version: 1.0.0
// guid: 3b9e1c70-8d42-4a16-9f53-7c2e6a8b1d04
// last-edited: 2026-06-18

package dataset

import "github.com/falkcorp/audiobook-organizer/internal/database"

// MineHighConfidenceDup detects high-precision DUPLICATE signals between two books
// that the rule Classify() does not emit as positives. It is the positive-label
// counterpart to Classify's negative catchers, used by the dedup.mine-gold-labels
// op to seed the tuning dataset with auto-mined true_dup labels
// (label_source="auto_high_conf") from in-house ground truth.
//
// Returns (label="true_dup", reason, fires=true) on the FIRST signal that fires,
// in descending order of confidence; ("", "", false) if none fires.
//
// Signals (highest confidence first):
//  1. shared file hash      — identical bytes; a definitive duplicate (no audio gate)
//  2. shared AcoustID recording id — implies both sides were fingerprinted from real audio
//  3. shared ASIN/ISBN13/ISBN10    — strong identity, gated on plausible audio on BOTH
//     sides so two metadata-only stubs sharing an id are never mislabeled
func MineHighConfidenceDup(a, b *database.Book, aFiles, bFiles []database.BookFile) (label, reason string, fires bool) {
	if a == nil || b == nil {
		return "", "", false
	}
	if h := sharedFileHash(a, aFiles, b, bFiles); h != "" {
		return "true_dup", "shared file hash " + shortHash(h), true
	}
	if rid := sharedRecordingID(aFiles, bFiles); rid != "" {
		return "true_dup", "shared acoustid recording " + rid, true
	}
	if sidePlausibleAudioRaw(aFiles) && sidePlausibleAudioRaw(bFiles) {
		if id := sharedExternalID(a, b); id != "" {
			return "true_dup", "shared " + id, true
		}
	}
	return "", "", false
}

// sharedFileHash returns a non-empty file hash present on both books (book-level
// FileHash or any BookFile.FileHash), or "" if there is no overlap.
func sharedFileHash(a *database.Book, aFiles []database.BookFile, b *database.Book, bFiles []database.BookFile) string {
	set := make(map[string]struct{})
	if a.FileHash != nil && *a.FileHash != "" {
		set[*a.FileHash] = struct{}{}
	}
	for i := range aFiles {
		if aFiles[i].FileHash != "" {
			set[aFiles[i].FileHash] = struct{}{}
		}
	}
	if b.FileHash != nil && *b.FileHash != "" {
		if _, ok := set[*b.FileHash]; ok {
			return *b.FileHash
		}
	}
	for i := range bFiles {
		if h := bFiles[i].FileHash; h != "" {
			if _, ok := set[h]; ok {
				return h
			}
		}
	}
	return ""
}

// sharedRecordingID returns an AcoustID online recording id present on a file of
// each book, or "" if none is shared.
func sharedRecordingID(aFiles, bFiles []database.BookFile) string {
	set := make(map[string]struct{})
	for i := range aFiles {
		if id := aFiles[i].AcoustIDOnlineRecordingID; id != "" {
			set[id] = struct{}{}
		}
	}
	for i := range bFiles {
		if id := bFiles[i].AcoustIDOnlineRecordingID; id != "" {
			if _, ok := set[id]; ok {
				return id
			}
		}
	}
	return ""
}

// sharedExternalID returns a labeled (kind + value) external identifier shared by
// both books, preferring ASIN > ISBN13 > ISBN10. Returns "" if none is shared.
func sharedExternalID(a, b *database.Book) string {
	if v := bothEqual(a.ASIN, b.ASIN); v != "" {
		return "ASIN " + v
	}
	if v := bothEqual(a.ISBN13, b.ISBN13); v != "" {
		return "ISBN13 " + v
	}
	if v := bothEqual(a.ISBN10, b.ISBN10); v != "" {
		return "ISBN10 " + v
	}
	return ""
}

// bothEqual returns the shared value when both pointers are non-nil, non-empty,
// and equal; otherwise "".
func bothEqual(x, y *string) string {
	if x != nil && y != nil && *x != "" && *x == *y {
		return *x
	}
	return ""
}

// sidePlausibleAudioRaw reports whether a book side shows evidence of real audio:
// any file with positive duration, or a file at/above the stub floor. Mirrors the
// engine/rules plausible-audio signal but operates on raw BookFiles.
func sidePlausibleAudioRaw(files []database.BookFile) bool {
	for i := range files {
		f := &files[i]
		if f.AcoustIDFingerprintDurationSec > 0 || f.Duration > 0 {
			return true
		}
		if f.FileSize >= minPlausibleAudioBytes {
			return true
		}
	}
	return false
}

// shortHash truncates a hash for human-readable reasons.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
