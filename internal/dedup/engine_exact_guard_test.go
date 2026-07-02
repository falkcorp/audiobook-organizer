// file: internal/dedup/engine_exact_guard_test.go
// version: 1.0.0
// guid: 4a1e6d3f-9c72-4a0b-8e35-1f6b2c7d9e40
// last-edited: 2026-07-01

// Regression guard for DEDUP-INTRO-1 (residual): upsertExactCandidate is the
// shared chokepoint for every exact-family emitter. It must reject pairs
// where either book has a boilerplate publisher-intro/outro title, and pairs
// where either book has a known, positive Duration under
// minFingerprintMatchSeconds — while still allowing genuine duplicates
// (normal titles, durations well above the threshold) through.
package dedup

import (
	"testing"
)

func TestUpsertExactCandidate_BoilerplateTitleGuard(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "This is Audible")
	b := primaryBook("B", "This is Audible")

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates for boilerplate-title pair, got %d: %+v", len(cands), cands)
	}
}

func TestUpsertExactCandidate_MinDurationGuard(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "Real Book Title")
	shortDur := 30
	a.Duration = &shortDur
	b := primaryBook("B", "Real Book Title")

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates for short-duration pair, got %d: %+v", len(cands), cands)
	}
}

func TestUpsertExactCandidate_UnknownDurationNotSuppressed(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "Real Book Title")
	a.Duration = nil
	b := primaryBook("B", "Real Book Title")
	b.Duration = nil

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 1 {
		t.Errorf("expected 1 candidate for unknown-duration pair (no over-suppression), got %d: %+v", len(cands), cands)
	}
}

func TestUpsertExactCandidate_GenuineDuplicateStillPersisted(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "Genuine Duplicate Book")
	b := primaryBook("B", "Genuine Duplicate Book")

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 1 {
		t.Fatalf("expected exactly 1 candidate for genuine duplicate pair, got %d: %+v", len(cands), cands)
	}
	if cands[0].EntityAID != "A" || cands[0].EntityBID != "B" {
		t.Errorf("unexpected candidate pair: %+v", cands[0])
	}
}

// TestUpsertExactCandidate_ZeroDurationNotSuppressed locks in the "<=0 means
// unknown" convention: a zero Duration must not be mistaken for a genuinely
// short clip.
func TestUpsertExactCandidate_ZeroDurationNotSuppressed(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "Real Book Title")
	zero := 0
	a.Duration = &zero
	b := primaryBook("B", "Real Book Title")

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 1 {
		t.Errorf("expected 1 candidate for zero-duration pair (treated as unknown), got %d: %+v", len(cands), cands)
	}
}
