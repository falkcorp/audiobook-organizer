// file: internal/dedup/dataset/rules.go
// version: 1.3.0
// guid: 9e2b4c71-3a85-4d60-8f29-1b7c6a4e5d02
// last-edited: 2026-07-12

package dataset

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/boilerplate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// sameTitleHighSimThreshold is the candidate-similarity floor at/above which a
// same-title pair is treated as "suspected same work" by the partVsWhole guard.
// Set to the whole-book signature match threshold (0.95) for consistency.
//
// Caveat: for the dominant "exact" candidate layer, LabeledExample.Similarity is
// an exact-title-match score (≈1.0 whenever titles match), NOT the author-aware
// embedding cosine — so on that layer this gate does NOT separate same-work from
// a genuine same-title/different-work collision. The guard's safety does not
// rest on the gate: it routes to unsure (a review-queue holding state that never
// merges), so even a genuine collision landing in unsure cannot cause a wrong
// merge. The threshold is kept as a conservative filter for the non-exact layers
// (embedding/acoustid/llm) and to require *some* positive similarity evidence.
const sameTitleHighSimThreshold = 0.95

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
//  4. partVsWhole             → not_dup  (hard negative: duration mismatch; unsure when the pair shares identity — unit corruption — OR shares a non-boilerplate title at high similarity — suspected same work)
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
		// A same-title, high-similarity pair whose only negative evidence is the
		// duration ratio is far more likely a same-work mismatch (a partial/sample
		// file, an abridged edition, or — very commonly on prod — a corrupt
		// duration inflating the ratio) than a genuine part-vs-whole. Rather than
		// poison the gold set with a false not_dup (which dataset-backfill would
		// then *dismiss*, pulling a real duplicate out of review), route to unsure.
		// Boilerplate idents (e.g. "Big Finish Ident") are excluded: they are
		// embedding-identical to every copy of themselves but are legitimately
		// not_dup at the book level, so they keep the not_dup label.
		if sharesNonBoilerplateTitleAtHighSim(ex) {
			return "unsure", fmt.Sprintf("duration ratio %.3f but same title at high similarity — suspected same work", ex.DurationRatio), true
		}
		return "not_dup", fmt.Sprintf("duration ratio %.3f — part vs whole", ex.DurationRatio), true
	}
	return "", "", false
}

// sharesNonBoilerplateTitleAtHighSim reports whether the pair has identical
// normalized (non-empty) titles, a candidate similarity at/above
// sameTitleHighSimThreshold, and a title that is NOT a compiled-in boilerplate
// ident. It is the predicate for partVsWhole's same-work guard. Similarity is
// unknown (nil) on some rows; treat unknown as "not high" so the guard stays
// conservative and only fires on positive similarity evidence.
func sharesNonBoilerplateTitleAtHighSim(ex database.LabeledExample) bool {
	ta := util.NormalizeTitle(util.CollapseSpaces(ex.A.Title))
	tb := util.NormalizeTitle(util.CollapseSpaces(ex.B.Title))
	if ta == "" || ta != tb {
		return false
	}
	if ex.Similarity == nil || *ex.Similarity < sameTitleHighSimThreshold {
		return false
	}
	// Both titles are equal here, so checking either side is sufficient.
	return !boilerplate.IsBoilerplateTitle(ex.A.Title)
}
