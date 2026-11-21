// file: internal/dedup/unified/score_json_golden_test.go
// version: 0.1.0
// guid: 3f0c8a52-6d17-4b9e-a1c4-7e52d9b60f18
// last-edited: 2026-11-21

package unified

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/models"
)

// goldenScoreJSON is the byte-exact JSON of goldenScore. It was captured from
// the code BEFORE internal/models was merged into this package (R3-01b) and
// must never be regenerated to make a change pass: stored dedup rows
// (candRec.sb, DedupCandidate.score_breakdown) decode and encode these bytes,
// so any difference means a stored row no longer round-trips.
const goldenScoreJSON = `{"pair":["book-batman-1","book-batman-2"],"score":96.25,"band":"HIGH","signals":[{"kind":"exact_file","raw":1,"confidence":0.99,"evidence":"whole-file hash 9af3 both sides"},{"kind":"lsh_acoustid","raw":0.93,"confidence":0.9,"evidence":"hamming 0.93","fp_version":"fp-v2"},{"kind":"duration","raw":0.004,"confidence":0,"evidence":"duration within 2%"},{"kind":"exact_rule","raw":0,"confidence":0,"evidence":"isbn 9780000000001","rule":"isbn_asin"},{"kind":"same_path","raw":0,"confidence":0,"evidence":"same_path"}],"suppressors":["series_volume_differs"],"formula":"unified_v1","computed_at":"2026-11-21T03:04:05.123456789Z"}`

// goldenMergedJSON is MergeProvenance(goldenScore, goldenIncoming) and
// goldenCarriedJSON is CarryProvenance(goldenScore, goldenNext), captured the
// same way, so moving the helpers cannot change what they return.
const goldenMergedJSON = `{"pair":["book-batman-1","book-batman-2"],"score":96.25,"band":"HIGH","signals":[{"kind":"exact_file","raw":1,"confidence":0.99,"evidence":"whole-file hash 9af3 both sides"},{"kind":"lsh_acoustid","raw":0.93,"confidence":0.9,"evidence":"hamming 0.93","fp_version":"fp-v2"},{"kind":"duration","raw":0.004,"confidence":0,"evidence":"duration within 2%"},{"kind":"exact_rule","raw":0,"confidence":0,"evidence":"isbn 9780000000002 (refreshed)","rule":"isbn_asin"},{"kind":"same_path","raw":0,"confidence":0,"evidence":"same_path"},{"kind":"exact_rule","raw":0,"confidence":0,"evidence":"title+author","rule":"title_author"}],"suppressors":["series_volume_differs"],"formula":"unified_v1","computed_at":"2026-11-21T03:04:05.123456789Z"}`

const goldenCarriedJSON = `{"pair":["book-batman-1","book-batman-2"],"score":91,"band":"HIGH","signals":[{"kind":"metadata_fuzzy","raw":0.88,"confidence":0.85,"evidence":"fuzzy 0.88"},{"kind":"exact_rule","raw":0,"confidence":0,"evidence":"isbn 9780000000001","rule":"isbn_asin"},{"kind":"same_path","raw":0,"confidence":0,"evidence":"same_path"}],"suppressors":[],"formula":"unified_v2","computed_at":"2026-11-22T00:00:00Z"}`

func goldenScore() *UnifiedDedupScore {
	return &UnifiedDedupScore{
		Pair:  [2]string{"book-batman-1", "book-batman-2"},
		Score: 96.25,
		Band:  BandHigh,
		Signals: []Signal{
			{Kind: SigExactFile, Raw: 1, Confidence: 0.99, Evidence: "whole-file hash 9af3 both sides"},
			{Kind: SigLSHAcoustID, Raw: 0.93, Confidence: 0.9, Evidence: "hamming 0.93", FPVersion: "fp-v2"},
			{Kind: SigDuration, Raw: 0.004, Confidence: 0, Evidence: "duration within 2%"},
			{Kind: SigExactRule, Raw: 0, Confidence: 0, Evidence: "isbn 9780000000001", Rule: "isbn_asin"},
			{Kind: SigSamePath, Raw: 0, Confidence: 0, Evidence: "same_path"},
		},
		Suppressors: []string{"series_volume_differs"},
		Formula:     "unified_v1",
		ComputedAt:  time.Date(2026, 11, 21, 3, 4, 5, 123456789, time.UTC),
	}
}

func goldenIncoming() *UnifiedDedupScore {
	return &UnifiedDedupScore{
		Pair: [2]string{"book-batman-1", "book-batman-2"},
		Signals: []Signal{
			{Kind: SigExactRule, Evidence: "isbn 9780000000002 (refreshed)", Rule: "isbn_asin"},
			{Kind: SigExactRule, Evidence: "title+author", Rule: "title_author"},
			{Kind: SigEmbedHigh, Raw: 0.96, Confidence: 0.95, Evidence: "not provenance, must be ignored"},
		},
	}
}

func goldenNext() *UnifiedDedupScore {
	return &UnifiedDedupScore{
		Pair:        [2]string{"book-batman-1", "book-batman-2"},
		Score:       91,
		Band:        BandHigh,
		Signals:     []Signal{{Kind: SigMetaFuzzy, Raw: 0.88, Confidence: 0.85, Evidence: "fuzzy 0.88"}},
		Suppressors: []string{},
		Formula:     "unified_v2",
		ComputedAt:  time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC),
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestUnifiedDedupScoreJSONRoundTrip pins the persisted shape of the score:
// the golden bytes decode into the same value and re-encode to the same bytes,
// and the provenance helpers return the same bytes they returned before the
// type moved here from internal/models.
func TestUnifiedDedupScoreJSONRoundTrip(t *testing.T) {
	if got := mustJSON(t, goldenScore()); got != goldenScoreJSON {
		t.Fatalf("score JSON changed\n got: %s\nwant: %s", got, goldenScoreJSON)
	}

	var decoded UnifiedDedupScore
	if err := json.Unmarshal([]byte(goldenScoreJSON), &decoded); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	want := goldenScore()
	if !decoded.ComputedAt.Equal(want.ComputedAt) {
		t.Fatalf("ComputedAt %v, want %v", decoded.ComputedAt, want.ComputedAt)
	}
	decoded.ComputedAt, want.ComputedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(&decoded, want) {
		t.Fatalf("decoded golden differs from the source value\n got: %+v\nwant: %+v", decoded, *want)
	}

	var again UnifiedDedupScore
	if err := json.Unmarshal([]byte(goldenScoreJSON), &again); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if got := mustJSON(t, &again); got != goldenScoreJSON {
		t.Fatalf("round trip changed the bytes\n got: %s\nwant: %s", got, goldenScoreJSON)
	}
}

func TestProvenanceHelpersGoldenBytes(t *testing.T) {
	if got := mustJSON(t, models.MergeProvenance(goldenScore(), goldenIncoming())); got != goldenMergedJSON {
		t.Fatalf("MergeProvenance output changed\n got: %s\nwant: %s", got, goldenMergedJSON)
	}
	if got := mustJSON(t, models.CarryProvenance(goldenScore(), goldenNext())); got != goldenCarriedJSON {
		t.Fatalf("CarryProvenance output changed\n got: %s\nwant: %s", got, goldenCarriedJSON)
	}

	// IsProvenanceOnly: a provenance-only breakdown is recognised, a scored one
	// and a nil one are not.
	prov := &UnifiedDedupScore{Signals: []Signal{{Kind: SigExactRule, Rule: "file_hash"}, {Kind: SigSamePath}}}
	if !prov.IsProvenanceOnly() {
		t.Fatal("provenance-only breakdown not recognised")
	}
	if goldenScore().IsProvenanceOnly() {
		t.Fatal("scored breakdown reported provenance-only")
	}
	var nilScore *UnifiedDedupScore
	if nilScore.IsProvenanceOnly() {
		t.Fatal("nil breakdown reported provenance-only")
	}
	if !models.IsProvenanceKind(SigExactRule) || !models.IsProvenanceKind(SigSamePath) || models.IsProvenanceKind(SigExactFile) {
		t.Fatal("IsProvenanceKind classification changed")
	}
}
