// file: internal/metafetch/score_breakdown_fixture_test.go
// version: 1.0.0
// guid: 6a1f8c05-42d7-4b93-8e10-9c5b3f7a2e64
// last-edited: 2026-08-20

package metafetch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The wire seam between the Go scorer and the React evidence panel.
//
// Every link in the chain -- the recorder, the JSON tags, the TS interfaces, the
// adapter, the waterfall renderer -- is unit-tested on its own, and that proves
// nothing about whether they AGREE. `MetadataScoreStep` in web/src/services/api.ts
// is a hand-written mirror of `ScoreStep`, kept in step by a comment asking future
// editors to keep it in step. That is not a mechanism.
//
// The specific failure this closes: rename a tag here (or drop a field) and the
// frontend receives steps whose `operand` is undefined. Those replay to NaN, and
// NaN compares false against every threshold -- so a consistency check phrased as
// "flag it when the numbers diverge" reports agreement and the panel presents
// unverifiable rows as a verified derivation. The reviewer sees a confident,
// empty explanation. See web/src/components/review/evidence/types.ts.
//
// So this test writes a REAL serialized candidate -- produced by the real search
// path, not hand-authored -- into the web fixture directory, and the frontend
// suite reads that exact file. Drift on either side now fails loudly:
//
//   - Go changes a tag without regenerating -> this test fails on the diff.
//   - Go changes a tag and regenerates      -> the TS test fails on the new shape.
//
// Regenerate deliberately with:
//
//	UPDATE_FIXTURE=1 go test ./internal/metafetch/ -run Fixture
var updateFixture = os.Getenv("UPDATE_FIXTURE") != ""

const fixtureRelPath = "../../web/src/components/review/evidence/__fixtures__/score_breakdown.json"

func TestScoreBreakdownFixture_MatchesTheShippedWireFormat(t *testing.T) {
	book := &database.Book{ID: "fixture", Title: "Mistborn", Duration: intPtr(86400)}
	svc := NewService(&database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) { return book, nil },
	})
	svc.SetOverrideSources([]metadata.MetadataSource{
		&mockMetadataSource{name: "test-source", results: []metadata.BookMetadata{
			// Chosen to exercise several distinct step kinds in one payload:
			// a base tier, an author multiplier, a narrator multiplier, a
			// duration multiplier, and the rich-metadata additive bonus.
			{
				Title:       "Mistborn",
				Author:      "Brandon Sanderson",
				Narrator:    "Michael Kramer",
				Description: "The first book of the Mistborn trilogy.",
				CoverURL:    "https://example.invalid/cover.jpg",
				ISBN:        "9780765311788",
				DurationSec: 86000,
			},
		}},
	})

	resp, err := svc.SearchMetadataForBook(book.ID, book.Title)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected a candidate to serialize")
	}
	got := resp.Results[0]

	// A fixture that does not itself satisfy the invariant would teach the
	// frontend test to accept a broken payload.
	assertBreakdownExplainsScore(t, got)
	if len(got.ScoreBreakdown.Steps) < 3 {
		t.Fatalf("fixture is too thin to be worth checking: %d steps",
			len(got.ScoreBreakdown.Steps))
	}

	encoded, err := json.MarshalIndent(got.ScoreBreakdown, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	encoded = append(encoded, '\n')

	path := filepath.Clean(fixtureRelPath)
	if updateFixture {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("wrote %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture missing (%v).\nRegenerate with: UPDATE_FIXTURE=1 go test ./internal/metafetch/ -run Fixture", err)
	}
	if string(want) != string(encoded) {
		t.Fatalf("the serialized breakdown no longer matches the fixture the frontend reads.\n"+
			"If this change is intended, regenerate AND check that web/src/services/api.ts still\n"+
			"describes the new shape:\n  UPDATE_FIXTURE=1 go test ./internal/metafetch/ -run Fixture\n\n"+
			"--- fixture on disk ---\n%s\n--- produced now ---\n%s", want, encoded)
	}
}

// The tags are asserted by name as well as by round-trip, because a rename that
// happened to be applied to BOTH sides at once would keep the fixture green
// while silently changing the published API for every other consumer.
func TestScoreStep_JSONKeysAreStable(t *testing.T) {
	encoded, err := json.Marshal(ScoreStep{
		ID: "base", Label: "Title/author match", Op: ScoreOpBase,
		Operand: 0.8, Running: 0.8, Detail: "d", Capped: true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []string{"id", "label", "op", "operand", "running", "detail", "capped"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("ScoreStep no longer serializes %q; web/src/services/api.ts expects it", want)
		}
	}
	if len(keys) != 7 {
		t.Errorf("ScoreStep gained or lost a field (%d keys: %v); update MetadataScoreStep too",
			len(keys), keys)
	}
}
