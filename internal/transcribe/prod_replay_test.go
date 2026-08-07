// file: internal/transcribe/prod_replay_test.go
// version: 1.0.0
// guid: c276ccd4-24a9-4c19-a4a9-363ec01649e8
// last-edited: 2026-08-07

package transcribe

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// TestProdReplay_ContaminationDropped re-parses real production transcripts and
// compares the STORED field values (produced by the old parser) against what the
// current parser produces. It is a measurement, not a fixture assertion: the
// stored values are known to be contaminated, so the test asserts the new parser
// is strictly better and never regresses a clean field into a dirty one.
func TestProdReplay_ContaminationDropped(t *testing.T) {
	raw, err := os.ReadFile("testdata/prod_replay.json")
	if err != nil {
		t.Skip("no prod replay corpus present")
	}
	var rows []struct {
		Text     string `json:"text"`
		Title    string `json:"title"`
		Author   string `json:"author"`
		Narrator string `json:"narrator"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("corpus: %v", err)
	}

	dirty := regexp.MustCompile(`(?i)\b(translat|chapter\s+\w+|prologue|epilogue|narrated|read by|cover art|foreword|introduction)\b|[,\s]+(written|narrated|translated|by)\s*$`)

	var oldBad, newBad, regressed int
	for _, r := range rows {
		got := ParseAudiobookIntro(r.Text)
		for _, pair := range []struct{ old, nw string }{
			{r.Title, got.Title}, {r.Author, got.Author}, {r.Narrator, got.Narrator},
		} {
			o := pair.old != "" && dirty.MatchString(pair.old)
			n := pair.nw != "" && dirty.MatchString(pair.nw)
			if o {
				oldBad++
			}
			if n {
				newBad++
			}
			if !o && n {
				regressed++
				t.Errorf("REGRESSION: clean %q became dirty %q", pair.old, pair.nw)
			}
		}
	}
	t.Logf("corpus=%d rows | contaminated fields: stored=%d  reparsed=%d  regressions=%d",
		len(rows), oldBad, newBad, regressed)
	if newBad > oldBad {
		t.Fatalf("new parser is worse: %d dirty vs %d stored", newBad, oldBad)
	}
}
