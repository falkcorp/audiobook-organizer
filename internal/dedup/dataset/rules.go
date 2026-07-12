// file: internal/dedup/dataset/rules.go
// version: 1.2.0
// guid: 9e2b4c71-3a85-4d60-8f29-1b7c6a4e5d02
// last-edited: 2026-07-11

package dataset

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// partVsWholeRatioMax is the duration-ratio ceiling below which a pair is
// classified as a part matched against a whole book (not a duplicate).
// A ratio below 0.5 means the shorter side is less than half the longer side.
const partVsWholeRatioMax = 0.5

// minPlausibleAudioBytes mirrors the engine-side hasPlausibleAudio floor
// (internal/dedup/engine.go). A book with no positive duration AND a largest
// file below this size is a stub/placeholder, not real audio.
const minPlausibleAudioBytes = 256 * 1024 // 256 KiB

// Classify runs the deterministic catchers in priority order and returns the
// first firing rule's (label, reason, fires=true). If no rule fires, it returns
// ("", "", false). The caller (backfill op) uses fires=false as "unsure" and
// leaves the example unlabeled for human or ML review.
//
// Priority order (highest first):
//  1. wholeBookSignatureMatch → true_dup  (strong positive oracle)
//  2. missingFile             → unsure   (file absence is evidence-free for dup-ness)
//  3. implausibleAudio        → not_dup  (hard negative: stub/placeholder side)
//  4. partVsWhole             → not_dup  (hard negative: duration mismatch; unsure when the pair shares identity — unit corruption)
func Classify(ex database.LabeledExample) (label, reason string, fires bool) {
	if l, r, ok := wholeBookSignatureMatch(ex); ok {
		return l, r, true
	}
	if l, r, ok := missingFile(ex); ok {
		return l, r, true
	}
	if l, r, ok := implausibleAudio(ex); ok {
		return l, r, true
	}
	if l, r, ok := partVsWhole(ex); ok {
		return l, r, true
	}
	return "", "", false
}

// wholeBookSignatureMatch is the positive oracle: both sides have a computed
// whole-book signature and signatureRelation is "match" (sim ≥ 0.95 per Task 6
// wiring in builder.go). Returns true_dup.
func wholeBookSignatureMatch(ex database.LabeledExample) (string, string, bool) {
	if ex.A.WholeBookSigPresent && ex.B.WholeBookSigPresent && ex.SignatureRelation == "match" {
		return "true_dup", "whole-book signatures match", true
	}
	return "", "", false
}

// SharesIdentity reports whether the pair carries matching hard-identity
// evidence (same ASIN, same version group, or identical primary path).
// Empty values are UNKNOWN and never match — unknown is non-disqualifying.
// Consumers: partVsWhole's unit-corruption guard (this file) and the
// suspicious-label queue predicate (TASK-04).
func SharesIdentity(ex database.LabeledExample) bool {
	if ex.A.ASIN != "" && ex.A.ASIN == ex.B.ASIN {
		return true
	}
	if ex.A.VersionGroupID != "" && ex.A.VersionGroupID == ex.B.VersionGroupID {
		return true
	}
	if ex.A.PrimaryPath != "" && ex.A.PrimaryPath == ex.B.PrimaryPath {
		return true
	}
	return false
}

// missingFile fires when either side has no resolvable files. File absence is
// evidence-free for dup-ness (a book whose files are currently unresolvable may
// still be a real duplicate), so this rule emits unsure — never not_dup — and
// leaves the pair unlabeled rather than poisoning the gold set.
func missingFile(ex database.LabeledExample) (string, string, bool) {
	if !ex.A.FilesExist {
		return "unsure", "side A has no resolvable files", true
	}
	if !ex.B.FilesExist {
		return "unsure", "side B has no resolvable files", true
	}
	return "", "", false
}

// implausibleAudio fires not_dup when either side has no plausible audio — no
// positive duration AND a largest file below the stub floor. This is the dataset
// counterpart to the engine emission gate (hasPlausibleAudio) and catches the
// residual stub / unscanned-placeholder pairs that missingFile (file records
// exist) and partVsWhole (zero duration → ratio 0) both miss.
//
// A genuine unscanned copy (large file, zero duration) has FileSizeBytes at or
// above the floor and is deliberately NOT suppressed — it is a real duplicate
// awaiting a scan, left unlabeled for the signature/duration catchers later.
func implausibleAudio(ex database.LabeledExample) (string, string, bool) {
	if sideImplausibleAudio(ex.A) {
		return "not_dup", "side A is a stub/placeholder (no duration, file < 256 KiB)", true
	}
	if sideImplausibleAudio(ex.B) {
		return "not_dup", "side B is a stub/placeholder (no duration, file < 256 KiB)", true
	}
	return "", "", false
}

// sideImplausibleAudio reports whether a book side has no evidence of real audio
// content: zero/unknown duration AND a largest file below the plausible floor.
func sideImplausibleAudio(f database.BookFeatures) bool {
	return f.TotalDurationSec <= 0 && f.FileSizeBytes < minPlausibleAudioBytes
}

// partVsWhole fires when both durations are known and the min/max ratio is below
// partVsWholeRatioMax, indicating the shorter entry is a chapter or excerpt of
// the longer one rather than a full duplicate.
//
// Note: when either side has TotalDurationSec == 0 (unknown duration),
// BuildExample sets DurationRatio = 0, so this catcher deliberately does
// not fire — the pair is left unlabeled for human/ML review (do not "fix"
// this by classifying zero-duration pairs as not_dup; size cannot prove a
// part-vs-whole relationship).
func partVsWhole(ex database.LabeledExample) (string, string, bool) {
	if ex.A.TotalDurationSec > 0 && ex.B.TotalDurationSec > 0 &&
		ex.DurationRatio > 0 && ex.DurationRatio < partVsWholeRatioMax {
		// A pair that shares hard identity (ASIN / version group / primary path)
		// but shows an extreme ratio is almost always a ms/sec duration-unit
		// corruption, not a genuine part-vs-whole — the 2026-07-08 calibration
		// hand-verified every such flagged pair was a real duplicate. Emit unsure
		// rather than poisoning the gold set with a false not_dup.
		if SharesIdentity(ex) {
			return "unsure", fmt.Sprintf("duration ratio %.3f but pair shares identity — suspected unit corruption", ex.DurationRatio), true
		}
		return "not_dup", fmt.Sprintf("duration ratio %.3f — part vs whole", ex.DurationRatio), true
	}
	return "", "", false
}
