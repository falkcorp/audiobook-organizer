// file: internal/dedup/dataset/pair_dedupe.go
// version: 1.0.0
// guid: 3f8b1a90-2c47-4d1e-9a6b-7e0c5d84f2a1
// last-edited: 2026-07-11

// Package dataset — pair-level dedupe of labeled examples (INIT-1 T3).
//
// The dedup:label store keys rows by candidateID, and one book-pair produces
// multiple candidates across layers, so a labeled dataset holds ~2.7× more rows
// than unique pairs (6,926 rows for 2,564 pairs on 2026-07-08). Calibration and
// export must collapse those to ONE row per book-pair so a pair is never
// double-counted. This is a consumption-time collapse only — the store keyspace
// is untouched.
package dataset

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// sourceRank orders LabelSource by trust: human > llm_judge > itunes_attr >
// rule. An unknown source ranks as rule (lowest) — non-disqualifying, but never
// preferred over a recognized higher source.
func sourceRank(src string) int {
	switch src {
	case "human":
		return 3
	case "llm_judge":
		return 2
	case "itunes_attr":
		return 1
	default: // "rule" and any unknown source
		return 0
	}
}

// PairKey returns the canonical identity of a labeled example's book pair:
// the two entity IDs sorted lexicographically, joined with "|". A/B ordering
// therefore never splits a pair.
func PairKey(ex database.LabeledExample) string {
	a, b := ex.EntityAID, ex.EntityBID
	if a <= b {
		return a + "|" + b
	}
	return b + "|" + a
}

// prefers reports whether candidate cand should displace the current best row
// for a pair. Preference order:
//  1. a labeled row (Label != "") always beats an unlabeled row;
//  2. higher source rank (human > llm_judge > itunes_attr > rule);
//  3. latest DecidedAt (RFC3339 strings compare lexicographically; "" loses);
//  4. highest CandidateID.
func prefers(cand, best database.LabeledExample) bool {
	candLabeled := cand.Label != ""
	bestLabeled := best.Label != ""
	if candLabeled != bestLabeled {
		return candLabeled // labeled beats unlabeled regardless of source
	}
	if cr, br := sourceRank(cand.LabelSource), sourceRank(best.LabelSource); cr != br {
		return cr > br
	}
	if cand.DecidedAt != best.DecidedAt {
		return cand.DecidedAt > best.DecidedAt
	}
	return cand.CandidateID > best.CandidateID
}

// DedupeByPair collapses examples to ONE per canonical pair.
// Preference: human > llm_judge > itunes_attr > rule; ties by latest
// DecidedAt (string compare), then highest CandidateID. Labeled rows
// always beat unlabeled (Label == "") rows. Order of the result follows
// first-seen pair order (stable for tests).
func DedupeByPair(examples []database.LabeledExample) []database.LabeledExample {
	if len(examples) == 0 {
		return examples
	}
	// idx maps a pair key to the slot in out currently holding its best row, so
	// first-seen pair order is preserved and a higher-preference row replaces in
	// place rather than appending.
	idx := make(map[string]int, len(examples))
	out := make([]database.LabeledExample, 0, len(examples))
	for _, ex := range examples {
		key := PairKey(ex)
		if slot, seen := idx[key]; seen {
			if prefers(ex, out[slot]) {
				out[slot] = ex
			}
			continue
		}
		idx[key] = len(out)
		out = append(out, ex)
	}
	return out
}
