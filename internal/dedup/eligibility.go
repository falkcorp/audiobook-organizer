// file: internal/dedup/eligibility.go
// version: 1.0.0
// guid: b8c2f3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d
// last-edited: 2026-06-10

// Package dedup — PairEligibility pre-filter (fable5 T014).
//
// # Design
//
// PairEligibility consolidates the three negative-evidence guards that were
// scattered across checkExactTitle (lines 511-531), checkDurationMatch
// (lines 647-651), and findSimilarBooks (lines 895-922) in engine.go into a
// single, table-driven function that runs BEFORE any collector.
//
// Suppressors are the identifiers listed in UnifiedDedupScore.Suppressors and
// match the SPEC 1 §4 labels ("series_volume_differs", "version_group_same",
// "same_dir_multi_file").  Non-empty suppressors means the pair is dropped;
// the collector loop should never run for suppressed pairs.
//
// # Extraction criteria (verbatim from engine.go guards)
//
//  1. version_group_same    — both books share a non-empty VersionGroupID.
//     Source: findSimilarBooks:895-898.
//  2. series_volume_differs — both books carry distinct, non-empty series
//     positions (from SeriesSequence or title extraction) AND their normalized
//     titles differ only in digits OR both explicit series numbers mismatch.
//     Source: checkExactTitle:516-530, findSimilarBooks:907-913.
//  3. same_dir_multi_file   — both books have a non-empty FilePath and share
//     the same parent directory.
//     Source: findSimilarBooks:920-922.
//
// WHY here and not in the collectors: each collector would have to replicate
// all three checks independently.  A single call before the collector loop is
// cheaper (runs once) and produces an auditable Suppressors slice that is
// stored on the UnifiedDedupScore for downstream explanation.

package dedup

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// PairEligibility reports whether books a and b are eligible for dedup
// candidate comparison.  If ok is false, suppressors is a non-empty slice of
// human-readable suppressor IDs explaining why the pair was dropped.
//
// The function is a pure predicate — it performs no I/O and does not modify
// either book.  The caller (engine orchestrator) is responsible for deciding
// whether to skip or record the suppressors.
//
// Guards evaluated (in order):
//  1. version_group_same    — same non-empty VersionGroupID on both books.
//  2. series_volume_differs — both books identify as distinct volumes of the
//     same series via structured metadata (SeriesSequence) or title extraction,
//     OR their normalized titles differ only in digit characters.
//  3. same_dir_multi_file   — both books have non-empty FilePaths that share
//     the same parent directory.
//
// WHY the ordering: version-group check is O(1) and the most common skip
// (tens of thousands of version-group pairs in a typical library), so it
// fires first.  Series-volume is next because it catches numbered series where
// the embedding can't distinguish "Book 3" from "Book 4".  Same-dir is last
// because filepath.Dir is cheap but only meaningful when both paths are known.
func PairEligibility(a, b *database.Book) (ok bool, suppressors []string) {
	// Guard 1 — version_group_same
	// Extracted verbatim from findSimilarBooks (engine.go:895-898).
	// Both books in the same version group are already known to be the same
	// logical title in different formats; surfacing them as dedup candidates
	// is noise, not signal.
	if a.VersionGroupID != nil && *a.VersionGroupID != "" &&
		b.VersionGroupID != nil &&
		*b.VersionGroupID == *a.VersionGroupID {
		suppressors = append(suppressors, "version_group_same")
	}

	// Guard 2 — series_volume_differs
	// Extracted verbatim from checkExactTitle (engine.go:511-531) and
	// findSimilarBooks (engine.go:907-913).
	//
	// Sub-check 2a: structured series numbers — both must be non-empty and
	// different for the guard to fire.  A book with no detected series number
	// passes through (it might be a standalone re-release of a series entry).
	aSeriesNum := seriesNumberOf(a)
	bSeriesNum := seriesNumberOf(b)
	if aSeriesNum != "" && bSeriesNum != "" && aSeriesNum != bSeriesNum {
		suppressors = append(suppressors, "series_volume_differs")
	}

	// Sub-check 2b: digit-structure guard — if normalized titles differ only
	// in digit characters the pair is almost certainly two series volumes
	// without an explicit marker ("Series Name 3" vs "Series Name 4").
	// Only fires when both sub-check 2a passed (we don't double-count) AND
	// the digit-only-difference condition holds.
	if aSeriesNum == "" || bSeriesNum == "" || aSeriesNum == bSeriesNum {
		if titlesDifferOnlyInDigits(normalizeTitle(a.Title), normalizeTitle(b.Title)) {
			suppressors = append(suppressors, "series_volume_differs")
		}
	}

	// Guard 3 — same_dir_multi_file
	// Originally from findSimilarBooks (engine.go:920-922): chapters split into
	// multiple files in ONE folder produce identical embeddings. Extended to also
	// catch the "shattered" layout where each chapter is its own "<prefix> - N"
	// subdir (different parent dirs, same grandparent + prefix) — the layout that
	// the old same-parent-only guard missed (purge-stale cleared only ~8%).
	if sameMultiFileBook(a, b) {
		suppressors = append(suppressors, "same_dir_multi_file")
	}

	return len(suppressors) == 0, suppressors
}
