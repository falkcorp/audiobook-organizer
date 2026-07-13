// file: internal/dedup/rescore_test.go
// version: 1.0.0
// guid: c4a70f81-92db-4e35-8a16-0d5f7c2e9b41
// last-edited: 2026-07-12

package dedup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newRescoreTestEngine builds an Engine backed by a real PebbleStore so the
// collectors run their real store queries.
func newRescoreTestEngine(t *testing.T) (*Engine, *database.PebbleStore) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "rescore-test"))
	if err != nil {
		t.Fatalf("open PebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	es := database.NewEmbeddingStore(store.DB())
	return NewEngine(es, store, nil, nil, nil), store
}

func mkBookWithHash(t *testing.T, store *database.PebbleStore, title, hash string, authorID *int, durationSec int) string {
	t.Helper()
	dur := durationSec
	b := &database.Book{Title: title, AuthorID: authorID, Duration: &dur, FilePath: "/audio/" + title + ".m4b"}
	created, err := store.CreateBook(b)
	if err != nil {
		t.Fatalf("CreateBook %q: %v", title, err)
	}
	if err := store.CreateBookFile(&database.BookFile{
		BookID:   created.ID,
		FilePath: "/audio/" + title + ".m4b",
		FileHash: hash,
		FileSize: 5 << 20,
		Duration: durationSec,
	}); err != nil {
		t.Fatalf("CreateBookFile %q: %v", title, err)
	}
	return created.ID
}

// TestScorePairsForBook_InjectsAndSkipsZeroSignal proves the injected-work-list
// path: a signal-bearing pair (near-identical titles → SigMetaFuzzy) returns a
// non-nil composed score, while a pair sharing no evidence returns a nil Score
// (unscorable — never persisted as a zero, which would poison calibration math).
func TestScorePairsForBook_InjectsAndSkipsZeroSignal(t *testing.T) {
	eng, store := newRescoreTestEngine(t)

	author, err := store.CreateAuthor("Melville")
	if err != nil {
		t.Fatalf("CreateAuthor: %v", err)
	}
	// Near-identical titles + same author → a primary SigMetaFuzzy signal fires.
	aID := mkBookWithHash(t, store, "Moby Dick Unabridged", "aaaa000000000001", &author.ID, 3600)
	bID := mkBookWithHash(t, store, "Moby Dick Unabridgd", "bbbb000000000002", &author.ID, 3600)
	// A book with nothing in common (different author, title, hash, duration).
	other, err := store.CreateAuthor("Nobody")
	if err != nil {
		t.Fatalf("CreateAuthor: %v", err)
	}
	cID := mkBookWithHash(t, store, "Zxqw Vfpl Ktrn", "cccc000000000003", &other.ID, 1234)

	// canonical A must be the smaller ID for a stable pair identity.
	lo, hi := aID, bID
	if lo > hi {
		lo, hi = hi, lo
	}

	res, err := eng.ScorePairsForBook(context.Background(), lo, []RescorePairInput{
		{OtherID: hi},
		{OtherID: cID},
	})
	if err != nil {
		t.Fatalf("ScorePairsForBook: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 results, got %d", len(res))
	}

	byOther := map[string]RescorePairResult{}
	for _, r := range res {
		byOther[r.OtherID] = r
	}

	scored := byOther[hi]
	if scored.NumSignals < 1 || scored.Score == nil {
		t.Fatalf("metadata pair: want >=1 signal + non-nil score, got n=%d score=%v", scored.NumSignals, scored.Score)
	}
	if scored.Score.Score <= 0 {
		t.Fatalf("metadata pair: want composite > 0, got %.4f", scored.Score.Score)
	}

	none := byOther[cID]
	if none.NumSignals != 0 || none.Score != nil {
		t.Fatalf("no-signal pair: want 0 signals + nil score, got n=%d score=%v", none.NumSignals, none.Score)
	}
}

// TestScorePairsForBook_BelowBandNotDropped is the core-behavior proof: a pair
// whose ONLY signal is a supporting duration match composes to a below-band score
// (band==""), which the operational scan DROPS. ScorePairsForBook must instead
// return that composed score (non-nil) so the caller can persist it — the whole
// point of the op. The two books share an author + duration (duration fires) but
// have titles far enough apart that no primary metadata-fuzzy signal fires.
func TestScorePairsForBook_BelowBandNotDropped(t *testing.T) {
	eng, store := newRescoreTestEngine(t)

	author, err := store.CreateAuthor("Shared Author")
	if err != nil {
		t.Fatalf("CreateAuthor: %v", err)
	}

	// Short, dissimilar titles: absolute Levenshtein distance 6 (<= duration's
	// LevenshteinMax=6, so SigDuration fires) but normalized similarity 0.25, so
	// metaTitleAuthorSimilarity = 0.70*0.25 + 0.30*1.0 = 0.475 < 0.50 → no
	// SigMetaFuzzy. Distinct file hashes → no exact-file signal; no embedding
	// cosine supplied → no embedding signal. Result: duration-only, below band.
	aID := mkBookWithHash(t, store, "aardvark", "1111111111111111", &author.ID, 3600)
	bID := mkBookWithHash(t, store, "zzzzzzrk", "2222222222222222", &author.ID, 3600)

	lo, hi := aID, bID
	if lo > hi {
		lo, hi = hi, lo
	}

	res, err := eng.ScorePairsForBook(context.Background(), lo, []RescorePairInput{{OtherID: hi}})
	if err != nil {
		t.Fatalf("ScorePairsForBook: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	r := res[0]
	if r.NumSignals < 1 || r.Score == nil {
		t.Fatalf("below-band pair: want >=1 signal + non-nil score (NOT dropped), got n=%d score=%v", r.NumSignals, r.Score)
	}
	if r.Score.Band != "" {
		t.Fatalf("below-band pair: expected empty band (score %.4f), got band=%q — fixture no longer below-band", r.Score.Score, r.Score.Band)
	}
	// Precise bit-identical math: with no primary signal the noisy-OR product is
	// 0 and the only contribution is the SigDuration supporting boost (4.0 in
	// DefaultScoreConfig), well under the 60.0 review floor.
	if r.Score.Score != 4.0 {
		t.Fatalf("below-band pair: expected composite == duration boost 4.0, got %.4f", r.Score.Score)
	}
}
