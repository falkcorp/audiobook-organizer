// file: internal/server/handlers/metadata/handler_rank_order_test.go
// version: 1.0.0
// guid: a628796c-ecbd-4442-8944-e88a2628321a
// last-edited: 2026-10-10

package metadatahandler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The interactive dialog's list is ordered by RankScore (the score without the
// missing-author / missing-narrator penalties) while Score, the stored row and
// every gate keep HEAD's values. Names are synthetic.
//
//	X: every field, wrong volume          Score 1.05    RankScore 1.05
//	Y: no narrator, the right volume      Score 0.9775  RankScore 1.15
//
// Stored (and gated) order is [X, Y], as at HEAD. The dialog shows [Y, X].
func rankPair() (x, y metafetch.MetadataCandidate) {
	x = metafetch.MetadataCandidate{Title: "Sample Saga 2", Author: "Author 07", Narrator: "Narrator 03",
		Source: "Audible", Score: 1.05, RankScore: 1.05, Publisher: "Pub X"}
	y = metafetch.MetadataCandidate{Title: "Sample Saga 1", Author: "Author 07",
		Source: "Audible", Score: 0.9775, RankScore: 1.15, Publisher: "Pub Y"}
	return x, y
}

func searchResults(t *testing.T, body []byte) []json.RawMessage {
	t.Helper()
	var resp struct {
		Data struct {
			Results []json.RawMessage `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Data.Results
}

func titleOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var c struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c.Title
}

// The search response is ordered by RankScore; the cache row it came from is
// not touched (the review lane, the snapshot and the batch apply read it).
func TestSearchAudiobookMetadata_DisplayOrderIsRankScore_StoredRowUntouched(t *testing.T) {
	h, d := newHandler(t)
	x, y := rankPair()
	entry := &metafetch.MetadataCandidateCache{BookID: "b1", FetchedAt: time.Now(),
		Candidates: rawCandidates(t, x, y)}
	before := [][]byte{bytes.Clone(entry.Candidates[0]), bytes.Clone(entry.Candidates[1])}
	d.mfs.EXPECT().GetCachedCandidates("b1").Return(entry, true, nil)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Sample Saga 1"}, nil)

	w := doReq(h.SearchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/search-metadata", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	res := searchResults(t, w.Body.Bytes())
	if len(res) != 2 || titleOf(t, res[0]) != "Sample Saga 1" || titleOf(t, res[1]) != "Sample Saga 2" {
		t.Fatalf("display order = %s, want [Sample Saga 1, Sample Saga 2] (by rank_score)", w.Body.String())
	}
	if !bytes.Equal(entry.Candidates[0], before[0]) || !bytes.Equal(entry.Candidates[1], before[1]) {
		t.Fatal("the stored cache row's candidate order must not change")
	}
	// Score is HEAD's penalised value, not the ranking score.
	var first struct {
		Score     float64 `json:"score"`
		RankScore float64 `json:"rank_score"`
	}
	if err := json.Unmarshal(res[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.Score != 0.9775 || first.RankScore != 1.15 {
		t.Fatalf("score/rank_score = %v/%v, want 0.9775/1.15", first.Score, first.RankScore)
	}
}

// The dialog applies the candidate the person clicked: it posts that
// candidate's own fields back, and the handler hands exactly that candidate to
// the apply, never an index into the stored order (which is the other row).
func TestApplyAudiobookMetadata_AppliesTheClickedRowOfTheReorderedList(t *testing.T) {
	h, d := newHandler(t)
	x, y := rankPair()
	entry := &metafetch.MetadataCandidateCache{BookID: "b1", FetchedAt: time.Now(),
		Candidates: rawCandidates(t, x, y)}
	d.mfs.EXPECT().GetCachedCandidates("b1").Return(entry, true, nil)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Sample Saga 1"}, nil)

	w := doReq(h.SearchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/search-metadata", nil, idParam("b1"))
	res := searchResults(t, w.Body.Bytes())
	if len(res) != 2 {
		t.Fatalf("results = %d: %s", len(res), w.Body.String())
	}
	// Click the SECOND row of the displayed list: the stored index-1 row is Y,
	// the displayed index-1 row is X. Applying by stored index would apply Y.
	var clicked metafetch.MetadataCandidate
	if err := json.Unmarshal(res[1], &clicked); err != nil || clicked.Title != "Sample Saga 2" {
		t.Fatalf("clicked row = %+v (%v), want Sample Saga 2", clicked, err)
	}

	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Sample Saga 1"}, nil)
	d.mfs.EXPECT().RenamePreflight("b1", mock.Anything, mock.Anything).Return(nil)
	var applied metafetch.MetadataCandidate
	d.mfs.EXPECT().ApplyMetadataCandidate("b1", mock.Anything, mock.Anything).
		Run(func(_ string, c metafetch.MetadataCandidate, _ []string) { applied = c }).
		Return(&metafetch.FetchMetadataResponse{Message: "applied", Book: &database.Book{ID: "b1"}}, nil)
	d.pool.EXPECT().Submit("b1", mock.Anything).Return(true)
	w = doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		map[string]any{"candidate": json.RawMessage(res[1]), "fields": []string{"title"}}, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if applied.Title != "Sample Saga 2" || applied.Publisher != "Pub X" || applied.Score != 1.05 {
		t.Fatalf("applied %+v, want the clicked Sample Saga 2 candidate unchanged", applied)
	}
}

// Bulk fetch applies the first non-review-only Result of the search, in the
// order the search returned them. RankScore does not enter: Y outranks X for the
// dialog, but the pick is X, as at HEAD. (It also applies with no score floor;
// that is todo BULK-FETCH-NO-GATE.)
func TestBulkFetchMetadata_PickIgnoresRankScore(t *testing.T) {
	h, d := newHandler(t)
	expectNoLocks(d.store)
	x, y := rankPair()
	// Author and narrator rows make the apply read people this test does not mock.
	x.Narrator, x.Author, y.Author = "", "", ""
	review := metafetch.MetadataCandidate{Title: "Sample Saga 1", Source: "Open Library", Score: 2, RankScore: 2.5}
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Old"}, nil)
	d.store.EXPECT().GetBookAuthors("b1").Return(nil, nil).Maybe()
	d.mfs.EXPECT().SearchMetadataForBookWithOptions("b1", "", "", "", "", mock.Anything).
		Return(&metafetch.SearchMetadataResponse{Results: []metafetch.MetadataCandidate{review, x, y}}, nil)
	var saved *database.Book
	d.mfs.EXPECT().CommitApply("b1", mock.Anything, mock.Anything, mock.Anything, "Audible").
		Run(func(_ string, _ *database.Book, b *database.Book, _ *metafetch.AuthorCredits, _ string) { saved = b }).
		Return(&database.Book{ID: "b1"}, nil)
	d.mfs.EXPECT().ApplyMetadataSystemTags("b1", "Audible", "").Return()

	w := doReq(h.BulkFetchMetadata, http.MethodPost, "/metadata/bulk-fetch",
		map[string]any{"book_ids": []string{"b1"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if saved == nil || saved.Publisher == nil || *saved.Publisher != "Pub X" {
		t.Fatalf("bulk fetch applied %+v, want the first non-review-only result (Pub X)", saved)
	}
}
