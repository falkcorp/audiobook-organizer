// file: internal/dedup/unified/cover_text_signal_test.go
// version: 1.0.0
// guid: 7b2e5d91-0c4f-4a38-9e16-3f8a6c2d1b57
// last-edited: 2026-09-26

package unified

import "testing"

// SigCoverText is registered evidence but non-scoring: even at a high
// confidence it neither creates a candidate on its own nor moves the score of
// one that primary evidence made.
func TestCoverTextSignalIsNonScoring(t *testing.T) {
	cfg := DefaultScoreConfig()
	if !isSupportingKind(SigCoverText) {
		t.Fatal("cover_text must be a supporting (non-product) kind")
	}
	alone := ComposeScore([]Signal{sig(SigCoverText, 0.99)}, nil, cfg, defaultPair)
	if alone.Score != 0 || alone.Band != "" {
		t.Fatalf("cover_text alone scored %.2f band %q", alone.Score, alone.Band)
	}
	base := ComposeScore([]Signal{sig(SigEmbedHigh, 0.9)}, nil, cfg, defaultPair)
	with := ComposeScore([]Signal{sig(SigEmbedHigh, 0.9), sig(SigCoverText, 0.99)}, nil, cfg, defaultPair)
	if with.Score != base.Score || with.Band != base.Band {
		t.Fatalf("cover_text moved a score: %.2f/%s -> %.2f/%s", base.Score, base.Band, with.Score, with.Band)
	}
}
