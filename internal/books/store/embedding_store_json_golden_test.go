// file: internal/books/store/embedding_store_json_golden_test.go
// version: 0.1.0
// guid: 8a4d1e07-52c9-4f36-b8a0-1d6e93c7f254
// last-edited: 2026-11-21

package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// Golden bytes captured BEFORE internal/models was merged into
// internal/dedup/unified (R3-01b). The first is the stored candidate row
// (candRec, keys "sb" etc.), the second the API/JSON form (DedupCandidate, key
// "score_breakdown"). They must never be regenerated to make a change pass: a
// difference means a stored row or an API payload changed shape.
const goldenCandRecJSON = `{"et":"book","a":"book-batman-1","b":"book-batman-2","l":"exact","sim":0.97,"s":"pending","c":1000,"u":2000,"sb":{"pair":["book-batman-1","book-batman-2"],"score":96.25,"band":"HIGH","signals":[{"kind":"exact_file","raw":1,"confidence":0.99,"evidence":"whole-file hash 9af3 both sides"},{"kind":"lsh_acoustid","raw":0.93,"confidence":0.9,"evidence":"hamming 0.93","fp_version":"fp-v2"},{"kind":"exact_rule","raw":0,"confidence":0,"evidence":"isbn 9780000000001","rule":"isbn_asin"},{"kind":"same_path","raw":0,"confidence":0,"evidence":"same_path"}],"suppressors":["series_volume_differs"],"formula":"unified_v1","computed_at":"2026-11-21T03:04:05.123456789Z"},"band":"HIGH","fv":"unified_v1"}`

const goldenDedupCandidateJSON = `{"id":7,"entity_type":"book","entity_a_id":"book-batman-1","entity_b_id":"book-batman-2","layer":"exact","similarity":0.97,"status":"pending","created_at":"2026-11-20T01:02:03Z","updated_at":"2026-11-20T01:02:03Z","score_breakdown":{"pair":["book-batman-1","book-batman-2"],"score":96.25,"band":"HIGH","signals":[{"kind":"exact_file","raw":1,"confidence":0.99,"evidence":"whole-file hash 9af3 both sides"},{"kind":"lsh_acoustid","raw":0.93,"confidence":0.9,"evidence":"hamming 0.93","fp_version":"fp-v2"},{"kind":"exact_rule","raw":0,"confidence":0,"evidence":"isbn 9780000000001","rule":"isbn_asin"},{"kind":"same_path","raw":0,"confidence":0,"evidence":"same_path"}],"suppressors":["series_volume_differs"],"formula":"unified_v1","computed_at":"2026-11-21T03:04:05.123456789Z"},"band":"HIGH","formula_version":"unified_v1"}`

func goldenStoreScore() *unified.UnifiedDedupScore {
	return &unified.UnifiedDedupScore{
		Pair:  [2]string{"book-batman-1", "book-batman-2"},
		Score: 96.25,
		Band:  "HIGH",
		Signals: []unified.Signal{
			{Kind: "exact_file", Raw: 1, Confidence: 0.99, Evidence: "whole-file hash 9af3 both sides"},
			{Kind: "lsh_acoustid", Raw: 0.93, Confidence: 0.9, Evidence: "hamming 0.93", FPVersion: "fp-v2"},
			{Kind: unified.SigExactRule, Evidence: "isbn 9780000000001", Rule: "isbn_asin"},
			{Kind: unified.SigSamePath, Evidence: "same_path"},
		},
		Suppressors: []string{"series_volume_differs"},
		Formula:     "unified_v1",
		ComputedAt:  time.Date(2026, 11, 21, 3, 4, 5, 123456789, time.UTC),
	}
}

func TestCandidateScoreBreakdownGoldenBytes(t *testing.T) {
	sim := 0.97
	rec := candRec{
		EntityType: "book", EntityAID: "book-batman-1", EntityBID: "book-batman-2",
		Layer: "exact", Similarity: &sim, Status: "pending",
		CreatedAt: 1000, UpdatedAt: 2000,
		ScoreBreakdown: goldenStoreScore(), Band: "HIGH", FormulaVersion: "unified_v1",
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != goldenCandRecJSON {
		t.Fatalf("candRec JSON changed\n got: %s\nwant: %s", got, goldenCandRecJSON)
	}
	var back candRec
	if err := json.Unmarshal([]byte(goldenCandRecJSON), &back); err != nil {
		t.Fatal(err)
	}
	b2, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(b2) != goldenCandRecJSON {
		t.Fatalf("candRec round trip changed the bytes\n got: %s\nwant: %s", b2, goldenCandRecJSON)
	}

	created := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	cand := DedupCandidate{
		ID: 7, EntityType: "book", EntityAID: "book-batman-1", EntityBID: "book-batman-2",
		Layer: "exact", Similarity: &sim, Status: "pending",
		CreatedAt: created, UpdatedAt: created,
		ScoreBreakdown: goldenStoreScore(), Band: "HIGH", FormulaVersion: "unified_v1",
	}
	cb, err := json.Marshal(cand)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(cb); got != goldenDedupCandidateJSON {
		t.Fatalf("DedupCandidate JSON changed\n got: %s\nwant: %s", got, goldenDedupCandidateJSON)
	}
	var cback DedupCandidate
	if err := json.Unmarshal([]byte(goldenDedupCandidateJSON), &cback); err != nil {
		t.Fatal(err)
	}
	cb2, err := json.Marshal(cback)
	if err != nil {
		t.Fatal(err)
	}
	if string(cb2) != goldenDedupCandidateJSON {
		t.Fatalf("DedupCandidate round trip changed the bytes\n got: %s\nwant: %s", cb2, goldenDedupCandidateJSON)
	}
}
