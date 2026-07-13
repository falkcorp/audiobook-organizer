// file: internal/dedup/collectors_acoustid_test.go
// version: 1.1.1
// guid: b8c4d952-e0f3-5a26-99b1-6cd23f458g7e
// last-edited: 2026-07-12

package dedup

import (
	"encoding/binary"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// ─── test doubles ──────────────────────────────────────────────────────────────

// stubExactStore satisfies ExactAcoustIDStore for CollectExactAcoustID tests.
type stubExactStore struct {
	// m maps seg string → *BookFile; nil value means "not found".
	m map[string]*database.BookFile
}

func (s *stubExactStore) GetBookFileByAcoustID(fp string) (*database.BookFile, error) {
	f, ok := s.m[fp]
	if !ok {
		return nil, nil
	}
	return f, nil
}

// stubLSHStore satisfies LSHAcoustIDStore for CollectLSHAcoustID tests.
type stubLSHStore struct {
	indexBuilt bool
	// probeResult is returned verbatim from LSHProbe.
	probeResult map[string]int
	// filesByID maps fileID → BookFile.
	filesByID map[string]*database.BookFile
}

func (s *stubLSHStore) IsLSHIndexBuilt() bool { return s.indexBuilt }

func (s *stubLSHStore) LSHProbe(_ []fingerprint.Subprint, _ []byte, _ int) (map[string]int, error) {
	return s.probeResult, nil
}

func (s *stubLSHStore) GetBookFileByID(_, fileID string) (*database.BookFile, error) {
	f, ok := s.filesByID[fileID]
	if !ok {
		return nil, nil
	}
	return f, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// makeFP returns a synthetic whole-file fingerprint of n uint32 frames
// whose content is all zeros (a perfectly matching fingerprint for testing).
func makeFP(n int) []byte {
	b := make([]byte, n*4)
	return b
}

// makeLowHammingFP returns a fingerprint whose Hamming similarity against
// makeFP(n) will be exactly (n-flipCount)/n*32/32 ≈ 1-(flipCount/n).
// We flip every bit in the first flipCount frames, yielding:
//
//	similarity = (totalBits - flippedBits) / totalBits
//	           = (n*32 - flipCount*32) / (n*32)
//	           = (n - flipCount) / n
//
// For n=100, flipCount=20 → similarity = 0.80 (below MinHamming 0.85).
func makeLowHammingFP(n, flipCount int) []byte {
	b := make([]byte, n*4)
	for i := 0; i < flipCount*4; i += 4 {
		binary.LittleEndian.PutUint32(b[i:], 0xFFFFFFFF)
	}
	return b
}

// ─── CollectExactAcoustID tests ───────────────────────────────────────────────

// validFP80 is a base64-encoded chromaprint with 80 uint32 frames and a
// non-zero pattern, satisfying IsUsefulFingerprint (>= MinUsefulFingerprintFrames).
// Generated deterministically with Python (seed 42) for reproducibility.
const validFP80 = "AQAAAKQdB75HPzokvRuuvuWMF5htCQgYODyCmweQM7intIxsOXOXSNDfAsPPKbNtWEgoOPbEVxsYYhlc2VmbRM8Mu3aKIPrtYRWOTNWhn+PdXZQytRIMqjvGS/0V2zzeGmJIdaPWXipfWzasRbTwr6YTnKMsibs/KndiRv7tpLGPObBU2MXHDzvTCc9RZ0URN+rykuG4UTeogGbj66V2JUQkQL+QikTAlm7mlmddOSSDfxjCDd0dKKEpy69tmRFjYpl4iEH5jt3yA6+5Hq/jisFFxaVYHUxwKXUB9bnhuUT5gcQuguoc36FN2KSCnDMoYMQqi/XI7YjsAZpTfgUd7l3h/dXPTz4PPuGS8xUWvH3REvvDicUhIal6840rRIjgnG33N+6LwruxNLdQZ/+sp2Bx54V0H0A6EVcGl447lzkCE7ai"

func TestCollectExactAcoustID_HitEmitsSignal(t *testing.T) {
	queryFile := &database.BookFile{
		ID:           "qfile1",
		BookID:       "bookA",
		AcoustIDSeg0: validFP80,
	}
	candidateFile := &database.BookFile{
		ID:     "cfile1",
		BookID: "bookB",
	}
	store := &stubExactStore{m: map[string]*database.BookFile{validFP80: candidateFile}}

	sigs, err := CollectExactAcoustID(store, queryFile, "bookA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(sigs))
	}
	if sigs[0].Kind != unified.SigExactAcoustID {
		t.Errorf("expected SigExactAcoustID, got %q", sigs[0].Kind)
	}
	if sigs[0].Confidence != 0.99 {
		t.Errorf("expected confidence 0.99, got %v", sigs[0].Confidence)
	}
}

func TestCollectExactAcoustID_ShortQuerySkipped(t *testing.T) {
	queryFile := &database.BookFile{
		ID:                             "qfile1",
		BookID:                         "bookA",
		AcoustIDSeg0:                   validFP80,
		AcoustIDFingerprintDurationSec: 20,
	}
	candidateFile := &database.BookFile{
		ID:                             "cfile1",
		BookID:                         "bookB",
		AcoustIDFingerprintDurationSec: 7200,
	}
	store := &stubExactStore{m: map[string]*database.BookFile{validFP80: candidateFile}}

	sigs, err := CollectExactAcoustID(store, queryFile, "bookA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("expected 0 signals for short query fingerprint, got %d", len(sigs))
	}
}

func TestCollectExactAcoustID_ShortHitSkipped(t *testing.T) {
	queryFile := &database.BookFile{
		ID:                             "qfile1",
		BookID:                         "bookA",
		AcoustIDSeg0:                   validFP80,
		AcoustIDFingerprintDurationSec: 7200,
	}
	candidateFile := &database.BookFile{
		ID:                             "cfile1",
		BookID:                         "bookB",
		AcoustIDFingerprintDurationSec: 20,
	}
	store := &stubExactStore{m: map[string]*database.BookFile{validFP80: candidateFile}}

	sigs, err := CollectExactAcoustID(store, queryFile, "bookA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("expected 0 signals for short hit fingerprint, got %d", len(sigs))
	}
}

func TestCollectExactAcoustID_SelfMatchSkipped(t *testing.T) {
	// The candidate lives in the same book as the query — must not emit.
	queryFile := &database.BookFile{
		ID:           "qfile1",
		BookID:       "bookA",
		AcoustIDSeg0: validFP80,
	}
	selfFile := &database.BookFile{ID: "other-file", BookID: "bookA"}
	store := &stubExactStore{m: map[string]*database.BookFile{validFP80: selfFile}}

	sigs, err := CollectExactAcoustID(store, queryFile, "bookA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals for self-match, got %d", len(sigs))
	}
}

func TestCollectExactAcoustID_NoSegments(t *testing.T) {
	queryFile := &database.BookFile{ID: "qfile1", BookID: "bookA"}
	store := &stubExactStore{m: map[string]*database.BookFile{}}

	sigs, err := CollectExactAcoustID(store, queryFile, "bookA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals for empty file, got %d", len(sigs))
	}
}

// ─── lshConfidenceScale tests ─────────────────────────────────────────────────

func TestLSHConfidenceScale_Boundaries(t *testing.T) {
	tests := []struct {
		name    string
		hamming float64
		want    float64
	}{
		{"at_min", 0.85, 0.90},
		{"at_max", 1.00, 0.97},
		{"below_min_clamp", 0.70, 0.90},
		{"above_max_clamp", 1.10, 0.97},
		{"midpoint", 0.925, 0.935},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lshConfidenceScale(tc.hamming, 0.85, 1.0, 0.90, 0.97)
			// Allow small floating-point epsilon.
			const eps = 1e-9
			diff := got - tc.want
			if diff < -eps || diff > eps {
				t.Errorf("lshConfidenceScale(%.3f) = %.6f, want %.6f",
					tc.hamming, got, tc.want)
			}
		})
	}
}

// ─── CollectLSHAcoustID tests ─────────────────────────────────────────────────

// TestCollectLSHAcoustID_TruePositive exercises the happy path:
// two files with >= 2 band hits and a high Hamming similarity emit a signal.
func TestCollectLSHAcoustID_TruePositive(t *testing.T) {
	// Use a large fingerprint so Subprints can produce >= LSHBandCount samples.
	// 1000 uint32 frames at 8 fps ≈ 125s — well above minFramesForLSH.
	queryFP := makeFP(1000)
	candFP := makeFP(1000) // identical → Hamming 1.0

	queryFile := &database.BookFile{
		ID:                  "qfile1",
		BookID:              "bookA",
		AcoustIDFingerprint: queryFP,
	}
	candFile := &database.BookFile{
		ID:                  "cfile1",
		BookID:              "bookB",
		AcoustIDFingerprint: candFP,
	}

	store := &stubLSHStore{
		indexBuilt:  true,
		probeResult: map[string]int{"cfile1": 3}, // 3 band hits >= LSHMinBandHits(2)
		filesByID:   map[string]*database.BookFile{"cfile1": candFile},
	}

	sigs, err := CollectLSHAcoustID(store, queryFile, "bookA", DefaultLSHAcoustIDConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signal, got %d (sigs: %+v)", len(sigs), sigs)
	}
	sig := sigs[0]
	if sig.Kind != unified.SigLSHAcoustID {
		t.Errorf("expected SigLSHAcoustID, got %q", sig.Kind)
	}
	// Identical fingerprints → Hamming = 1.0 → MaxConfidence = 0.97.
	if sig.Raw != 1.0 {
		t.Errorf("expected raw hamming 1.0, got %v", sig.Raw)
	}
	if sig.Confidence != 0.97 {
		t.Errorf("expected confidence 0.97 for hamming 1.0, got %v", sig.Confidence)
	}
}

// TestCollectLSHAcoustID_BelowBandThreshold: LSHProbe returns no candidates
// (empty map because the stub only returns hits that passed band filtering).
// Simulates a file that did not accumulate enough band hits.
func TestCollectLSHAcoustID_BelowBandThreshold(t *testing.T) {
	queryFP := makeFP(1000)
	queryFile := &database.BookFile{
		ID:                  "qfile1",
		BookID:              "bookA",
		AcoustIDFingerprint: queryFP,
	}

	store := &stubLSHStore{
		indexBuilt:  true,
		probeResult: map[string]int{}, // no candidates passed the band threshold
		filesByID:   map[string]*database.BookFile{},
	}

	sigs, err := CollectLSHAcoustID(store, queryFile, "bookA", DefaultLSHAcoustIDConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals when no band-hit candidates, got %d", len(sigs))
	}
}

// TestCollectLSHAcoustID_HammingRefineRejection: a candidate survives band
// filtering (>= 2 hits) but its Hamming similarity is below MinHamming (0.85)
// and must therefore be dropped by the Hamming refinement step.
func TestCollectLSHAcoustID_HammingRefineRejection(t *testing.T) {
	// n=100 frames, flipCount=20 → Hamming similarity = (100-20)/100 = 0.80 < 0.85.
	queryFP := makeFP(100)
	candFP := makeLowHammingFP(100, 20) // similarity 0.80

	queryFile := &database.BookFile{
		ID:                  "qfile1",
		BookID:              "bookA",
		AcoustIDFingerprint: queryFP,
	}
	candFile := &database.BookFile{
		ID:                  "cfile_low",
		BookID:              "bookC",
		AcoustIDFingerprint: candFP,
	}

	store := &stubLSHStore{
		indexBuilt:  true,
		probeResult: map[string]int{"cfile_low": 4}, // passed band filter
		filesByID:   map[string]*database.BookFile{"cfile_low": candFile},
	}

	sigs, err := CollectLSHAcoustID(store, queryFile, "bookA", DefaultLSHAcoustIDConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals after Hamming rejection, got %d (hamming would be ~0.80)", len(sigs))
	}
}

// TestCollectLSHAcoustID_UnbuiltIndexSkip verifies that when IsLSHIndexBuilt
// returns false the collector returns (nil, nil) without calling LSHProbe.
func TestCollectLSHAcoustID_UnbuiltIndexSkip(t *testing.T) {
	queryFP := makeFP(1000)
	queryFile := &database.BookFile{
		ID:                  "qfile1",
		BookID:              "bookA",
		AcoustIDFingerprint: queryFP,
	}

	store := &stubLSHStore{
		indexBuilt: false, // LSH index has not been built
		// probeResult deliberately non-empty to catch spurious probe calls.
		probeResult: map[string]int{"some-file": 5},
		filesByID: map[string]*database.BookFile{
			"some-file": {ID: "some-file", BookID: "bookB"},
		},
	}

	sigs, err := CollectLSHAcoustID(store, queryFile, "bookA", DefaultLSHAcoustIDConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals when index unbuilt, got %d", len(sigs))
	}
}

// TestCollectLSHAcoustID_SelfMatchSkipped ensures a candidate whose BookID
// equals queryBookID is never emitted.
func TestCollectLSHAcoustID_SelfMatchSkipped(t *testing.T) {
	queryFP := makeFP(1000)
	queryFile := &database.BookFile{
		ID:                  "qfile1",
		BookID:              "bookA",
		AcoustIDFingerprint: queryFP,
	}
	// Candidate is in the same book.
	selfFile := &database.BookFile{
		ID:                  "other-qfile",
		BookID:              "bookA",
		AcoustIDFingerprint: queryFP,
	}

	store := &stubLSHStore{
		indexBuilt:  true,
		probeResult: map[string]int{"other-qfile": 10},
		filesByID:   map[string]*database.BookFile{"other-qfile": selfFile},
	}

	sigs, err := CollectLSHAcoustID(store, queryFile, "bookA", DefaultLSHAcoustIDConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals for self-book candidate, got %d", len(sigs))
	}
}

// TestCollectLSHAcoustID_NoFingerprint: queryFile has no whole-file fingerprint
// → immediately returns nil without touching the store.
func TestCollectLSHAcoustID_NoFingerprint(t *testing.T) {
	queryFile := &database.BookFile{
		ID:     "qfile1",
		BookID: "bookA",
		// AcoustIDFingerprint intentionally left nil
	}

	store := &stubLSHStore{
		indexBuilt:  true,
		probeResult: map[string]int{"some-file": 5},
	}

	sigs, err := CollectLSHAcoustID(store, queryFile, "bookA", DefaultLSHAcoustIDConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals for no-fingerprint file, got %d", len(sigs))
	}
}
