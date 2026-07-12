// file: internal/dedup/dataset/pair_dedupe_test.go
// version: 1.0.0
// guid: 8d2e6c14-95af-4b73-8f1c-6a0d3e29b7c4
// last-edited: 2026-07-11

package dataset

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestDedupeByPairPrefersHuman verifies a human-sourced row survives over a
// rule-sourced row for the same pair.
func TestDedupeByPairPrefersHuman(t *testing.T) {
	in := []database.LabeledExample{
		{CandidateID: 1, EntityAID: "a", EntityBID: "b", Label: "not_dup", LabelSource: "rule"},
		{CandidateID: 2, EntityAID: "a", EntityBID: "b", Label: "true_dup", LabelSource: "human"},
	}
	out := DedupeByPair(in)
	if len(out) != 1 {
		t.Fatalf("want 1 row, got %d", len(out))
	}
	if out[0].LabelSource != "human" || out[0].CandidateID != 2 {
		t.Fatalf("want human row (id 2), got source=%q id=%d", out[0].LabelSource, out[0].CandidateID)
	}
}

// TestDedupeByPairCrossClass verifies dedup across both label classes: a pair
// holding a rule not_dup and a human true_dup collapses to the single human
// true_dup row (not one per class).
func TestDedupeByPairCrossClass(t *testing.T) {
	in := []database.LabeledExample{
		{CandidateID: 10, EntityAID: "x", EntityBID: "y", Label: "not_dup", LabelSource: "rule"},
		{CandidateID: 11, EntityAID: "x", EntityBID: "y", Label: "true_dup", LabelSource: "human"},
	}
	out := DedupeByPair(in)
	if len(out) != 1 {
		t.Fatalf("cross-class pair must yield exactly 1 row, got %d", len(out))
	}
	if out[0].Label != "true_dup" || out[0].LabelSource != "human" {
		t.Fatalf("want human true_dup, got label=%q source=%q", out[0].Label, out[0].LabelSource)
	}
}

// TestDedupeByPairLatestDecidedAt verifies that among same-source rows the
// latest DecidedAt wins.
func TestDedupeByPairLatestDecidedAt(t *testing.T) {
	in := []database.LabeledExample{
		{CandidateID: 1, EntityAID: "a", EntityBID: "b", Label: "true_dup", LabelSource: "human", DecidedAt: "2026-07-01T00:00:00Z"},
		{CandidateID: 2, EntityAID: "a", EntityBID: "b", Label: "not_dup", LabelSource: "human", DecidedAt: "2026-07-05T00:00:00Z"},
	}
	out := DedupeByPair(in)
	if len(out) != 1 {
		t.Fatalf("want 1 row, got %d", len(out))
	}
	if out[0].CandidateID != 2 {
		t.Fatalf("want latest-decided row (id 2), got id=%d", out[0].CandidateID)
	}
}

// TestDedupeByPairHighestCandidateID verifies the final tie-break: same source,
// same DecidedAt → highest CandidateID wins.
func TestDedupeByPairHighestCandidateID(t *testing.T) {
	in := []database.LabeledExample{
		{CandidateID: 7, EntityAID: "a", EntityBID: "b", Label: "true_dup", LabelSource: "rule", DecidedAt: "2026-07-01T00:00:00Z"},
		{CandidateID: 42, EntityAID: "a", EntityBID: "b", Label: "true_dup", LabelSource: "rule", DecidedAt: "2026-07-01T00:00:00Z"},
	}
	out := DedupeByPair(in)
	if len(out) != 1 {
		t.Fatalf("want 1 row, got %d", len(out))
	}
	if out[0].CandidateID != 42 {
		t.Fatalf("want highest CandidateID (42), got %d", out[0].CandidateID)
	}
}

// TestDedupeByPairKeepsSingletons verifies distinct pairs pass through
// unchanged and in first-seen order (anti-over-suppression).
func TestDedupeByPairKeepsSingletons(t *testing.T) {
	in := []database.LabeledExample{
		{CandidateID: 1, EntityAID: "a", EntityBID: "b", Label: "true_dup", LabelSource: "rule"},
		{CandidateID: 2, EntityAID: "c", EntityBID: "d", Label: "not_dup", LabelSource: "rule"},
		{CandidateID: 3, EntityAID: "e", EntityBID: "f", Label: "unsure", LabelSource: "human"},
	}
	out := DedupeByPair(in)
	if len(out) != 3 {
		t.Fatalf("distinct pairs must not be dropped: want 3, got %d", len(out))
	}
	for i, want := range []int64{1, 2, 3} {
		if out[i].CandidateID != want {
			t.Fatalf("first-seen order broken at %d: want id %d, got %d", i, want, out[i].CandidateID)
		}
	}
}

// TestDedupeByPairUnlabeledNeverDisplaces verifies a labeled row is never
// displaced by an unlabeled (Label == "") row for the same pair — even when the
// unlabeled row carries a higher-trust source — but a pair with ONLY unlabeled
// rows keeps one of them.
func TestDedupeByPairUnlabeledNeverDisplaces(t *testing.T) {
	in := []database.LabeledExample{
		// Labeled rule row must beat an unlabeled human row for the same pair.
		{CandidateID: 1, EntityAID: "a", EntityBID: "b", Label: "true_dup", LabelSource: "rule"},
		{CandidateID: 2, EntityAID: "a", EntityBID: "b", Label: "", LabelSource: "human"},
		// Pair with only unlabeled rows keeps one.
		{CandidateID: 3, EntityAID: "c", EntityBID: "d", Label: "", LabelSource: "rule"},
	}
	out := DedupeByPair(in)
	if len(out) != 2 {
		t.Fatalf("want 2 rows, got %d", len(out))
	}
	byKey := map[string]database.LabeledExample{}
	for _, ex := range out {
		byKey[PairKey(ex)] = ex
	}
	if got := byKey["a|b"]; got.CandidateID != 1 || got.Label != "true_dup" {
		t.Fatalf("labeled row must survive unlabeled: got id=%d label=%q", got.CandidateID, got.Label)
	}
	if got := byKey["c|d"]; got.CandidateID != 3 {
		t.Fatalf("only-unlabeled pair must keep its row, got id=%d", got.CandidateID)
	}
}

// TestPairKeyOrderInsensitive verifies (A,B) and (B,A) collapse to one key.
func TestPairKeyOrderInsensitive(t *testing.T) {
	ab := PairKey(database.LabeledExample{EntityAID: "alpha", EntityBID: "beta"})
	ba := PairKey(database.LabeledExample{EntityAID: "beta", EntityBID: "alpha"})
	if ab != ba {
		t.Fatalf("PairKey not order-insensitive: %q != %q", ab, ba)
	}
	if ab != "alpha|beta" {
		t.Fatalf("want canonical key alpha|beta, got %q", ab)
	}

	in := []database.LabeledExample{
		{CandidateID: 1, EntityAID: "alpha", EntityBID: "beta", Label: "true_dup", LabelSource: "rule"},
		{CandidateID: 2, EntityAID: "beta", EntityBID: "alpha", Label: "true_dup", LabelSource: "human"},
	}
	out := DedupeByPair(in)
	if len(out) != 1 {
		t.Fatalf("A,B and B,A must collapse to one pair, got %d rows", len(out))
	}
}
