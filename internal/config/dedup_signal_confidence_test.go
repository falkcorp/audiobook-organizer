// file: internal/config/dedup_signal_confidence_test.go
// version: 1.0.0
// guid: 9a1c4e7d-2b6f-4a83-9d0e-5c7b1f3a8e42
// last-edited: 2026-07-18

package config

import (
	"encoding/json"
	"testing"
)

// TestDedupSignalConfig_ConfidenceRoundTripsThroughJSON verifies that
// DedupSignalConfig.Confidence — the INIT-1 T05 follow-up field (TODO item
// 6) — survives a JSON marshal/unmarshal round trip, which is exactly the
// path UpdateConfig/SaveConfigToDatabase use to persist the config blob. This
// is the behavior the "confidence round" was missing: before this field
// existed, per-kind confidence bounds had nowhere to live in DedupSignalConfig
// and were silently dropped by that same round trip.
func TestDedupSignalConfig_ConfidenceRoundTripsThroughJSON(t *testing.T) {
	orig := DedupSignalConfig{
		BandCertainMin: 97.0,
		BandHighMin:    90.0,
		BandMediumMin:  75.0,
		BandReviewMin:  60.0,
		Confidence: map[string]DedupKindConfidence{
			"embedding_med": {MinConfidence: 0.72, MaxConfidence: 0.83},
			"lsh_acoustid":  {MinConfidence: 0.91},
		},
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var round DedupSignalConfig
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(round.Confidence) != 2 {
		t.Fatalf("expected 2 confidence entries after round trip, got %d", len(round.Confidence))
	}
	em, ok := round.Confidence["embedding_med"]
	if !ok {
		t.Fatal("expected embedding_med entry after round trip")
	}
	if em.MinConfidence != 0.72 || em.MaxConfidence != 0.83 {
		t.Errorf("embedding_med: expected 0.72/0.83, got %.4f/%.4f", em.MinConfidence, em.MaxConfidence)
	}
	lsh, ok := round.Confidence["lsh_acoustid"]
	if !ok {
		t.Fatal("expected lsh_acoustid entry after round trip")
	}
	if lsh.MinConfidence != 0.91 {
		t.Errorf("lsh_acoustid: expected MinConfidence 0.91, got %.4f", lsh.MinConfidence)
	}
	if lsh.MaxConfidence != 0 {
		t.Errorf("lsh_acoustid: expected MaxConfidence 0 (unset), got %.4f", lsh.MaxConfidence)
	}
}

// TestDedupSignalConfig_ConfidenceOmittedByDefault verifies that a
// DedupSignalConfig with no Confidence set at all (the zero value — every
// existing persisted config predating this field) marshals with the
// "confidence" key entirely absent (omitempty) and round-trips to a nil map,
// which unified.LoadScoreConfig/SetKindConfidenceOverrides both treat as a
// complete no-op. This is the backward-compatibility guarantee: existing
// configs are byte-for-byte unaffected by this field's addition.
func TestDedupSignalConfig_ConfidenceOmittedByDefault(t *testing.T) {
	orig := DedupSignalConfig{
		BandCertainMin: 97.0,
		BandHighMin:    90.0,
		BandMediumMin:  75.0,
		BandReviewMin:  60.0,
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) == "" {
		t.Fatal("expected non-empty JSON")
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, present := decoded["confidence"]; present {
		t.Errorf("expected \"confidence\" key to be omitted for zero-value Confidence, got present: %v", decoded["confidence"])
	}

	var round DedupSignalConfig
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.Confidence != nil {
		t.Errorf("expected nil Confidence map after round trip of a config without it, got %v", round.Confidence)
	}
}
