// file: internal/server/handlers/dedup/book_names_test.go
// version: 1.0.0
// guid: 37540837-ee05-4c3d-b1a6-f85414974da2
// last-edited: 2026-10-30

package deduphandler_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func intPtr(i int) *int { return &i }

// TestListDedupCandidates_IncludeBooks_AuthorName pins book_a.author_name /
// book_b.author_name (and series_name): the TypeScript Book type reads them
// and the bare database.Book never carried them. Two pairs share one author,
// so the lookup is also pinned to one read per distinct author.
func TestListDedupCandidates_IncludeBooks_AuthorName(t *testing.T) {
	h, d := newHandler(t)
	allowITunesFlagReads(d)
	insertCandidate(t, d.es, "a-year-one", "b-year-one")
	insertCandidate(t, d.es, "a-long-halloween", "b-no-author")

	d.store.EXPECT().GetBookByID("a-year-one").Return(&database.Book{ID: "a-year-one", Title: "Year One", AuthorID: intPtr(7), SeriesID: intPtr(3)}, nil).Maybe()
	d.store.EXPECT().GetBookByID("b-year-one").Return(&database.Book{ID: "b-year-one", Title: "Year One", AuthorID: intPtr(7)}, nil).Maybe()
	d.store.EXPECT().GetBookByID("a-long-halloween").Return(&database.Book{ID: "a-long-halloween", Title: "The Long Halloween", AuthorID: intPtr(7)}, nil).Maybe()
	d.store.EXPECT().GetBookByID("b-no-author").Return(&database.Book{ID: "b-no-author", Title: "No Author"}, nil).Maybe()
	d.store.EXPECT().GetAuthorByID(7).Return(&database.Author{ID: 7, Name: "Bruce Wayne"}, nil).Once()
	d.store.EXPECT().GetSeriesByID(3).Return(&database.Series{ID: 3, Name: "Gotham Knights"}, nil).Once()

	w := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates?include_books=true", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Candidates []struct {
				BookA map[string]any `json:"book_a"`
				BookB map[string]any `json:"book_b"`
			} `json:"candidates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	got := map[string]map[string]any{}
	for _, row := range resp.Data.Candidates {
		for _, b := range []map[string]any{row.BookA, row.BookB} {
			if b != nil {
				got[b["id"].(string)] = b
			}
		}
	}
	if len(got) != 4 {
		t.Fatalf("decoded %d books, want 4: %s", len(got), w.Body.String())
	}
	for _, id := range []string{"a-year-one", "b-year-one", "a-long-halloween"} {
		if got[id]["author_name"] != "Bruce Wayne" {
			t.Errorf("%s author_name=%v want Bruce Wayne", id, got[id]["author_name"])
		}
	}
	if got["a-year-one"]["series_name"] != "Gotham Knights" {
		t.Errorf("series_name=%v want Gotham Knights", got["a-year-one"]["series_name"])
	}
	if _, present := got["b-no-author"]["author_name"]; present {
		t.Errorf("a book with no author must omit author_name, got %v", got["b-no-author"]["author_name"])
	}
	if _, present := got["b-year-one"]["is_itunes"]; !present {
		t.Errorf("is_itunes must survive the view change")
	}
}

// TestGetDedupCandidateBreakdown_AuthorName pins the drawer's book_a / book_b
// author_name, which FileInfoCompare and AudioSamplePair read.
func TestGetDedupCandidateBreakdown_AuthorName(t *testing.T) {
	h, d := newHandler(t)
	id, aID, bID := insertCandidateWithBand(t, d.es, "book-ba", "book-bb", "HIGH")
	d.store.EXPECT().GetBookByID(aID).Return(&database.Book{ID: aID, Title: "Year One", AuthorID: intPtr(7)}, nil).Once()
	d.store.EXPECT().GetBookByID(bID).Return(&database.Book{ID: bID, Title: "Year One"}, nil).Once()
	d.store.EXPECT().GetBookFiles(mock.Anything).Return([]database.BookFile{}, nil).Twice()
	d.store.EXPECT().GetAuthorByID(7).Return(&database.Author{ID: 7, Name: "Bruce Wayne"}, nil).Once()

	w := doReq(t, h.GetDedupCandidateBreakdown, http.MethodGet,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/breakdown",
		nil, gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			BookA map[string]any `json:"book_a"`
			BookB map[string]any `json:"book_b"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.BookA["author_name"] != "Bruce Wayne" {
		t.Errorf("book_a.author_name=%v want Bruce Wayne", resp.Data.BookA["author_name"])
	}
	if _, present := resp.Data.BookB["author_name"]; present {
		t.Errorf("book_b has no author, author_name must be omitted")
	}
}
