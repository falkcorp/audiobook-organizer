// file: internal/server/handlers/dedup/search_test.go
// version: 1.0.1
// guid: 2f7a9d14-6c83-4e50-b927-8d1e5a3c07b6
// last-edited: 2026-09-02

// Tests for the ?q= search path: the book-ID resolver's matching rules, and the
// handler wiring that feeds it. Both were entirely untested when the feature
// landed -- including the refusal branch, which is the whole reason the design
// does not fall back to a row-only match.

package deduphandler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// searchCorpus is deliberately built so each book is reachable by exactly ONE
// field. A book matching on two fields could not tell a working branch from a
// dead one.
func searchCorpus() ([]database.BookCore, []database.Author) {
	books := []database.BookCore{
		// Title only. Mixed case on purpose: a lowercase-only fixture cannot
		// observe a dropped strings.ToLower.
		{ID: "b-title", Title: "Norse MYTHOLOGY", FilePath: "/lib/aaa/1.m4b"},
		// Path only -- neither title nor author contains the needle.
		{ID: "b-path", Title: "Untitled", FilePath: "/lib/Discworld/Mort.m4b"},
		// Author only, resolved through the author table.
		{ID: "b-author", Title: "Untitled", FilePath: "/lib/bbb/2.m4b", AuthorID: new(7)},
		// No author at all: the nil-AuthorID branch must not panic.
		{ID: "b-noauthor", Title: "Untitled", FilePath: "/lib/ccc/3.m4b"},
		// AuthorID pointing at a row GetAllAuthors does not return -- a
		// dangling ref, of which production has a documented population.
		{ID: "b-dangling", Title: "Untitled", FilePath: "/lib/ddd/4.m4b", AuthorID: new(999)},
	}
	authors := []database.Author{{ID: 7, Name: "Neil GAIMAN"}}
	return books, authors
}

func TestSearchResolvesBooksByTitleAuthorAndPath(t *testing.T) {
	books, authors := searchCorpus()

	cases := []struct {
		name    string
		needle  string
		wantIDs []string
	}{
		{"title", "norse", []string{"b-title"}},
		{"title is case-insensitive both ways", "MYTHOLOGY", []string{"b-title"}},
		{"file path", "discworld", []string{"b-path"}},
		{"author name via the author table", "gaiman", []string{"b-author"}},
		{"author match is case-insensitive", "GAIMAN", []string{"b-author"}},
		{"needle matching nothing", "zzz-no-such-needle", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, d := newHandler(t)
			insertCandidate(t, d.es, "b-title", "b-path")
			d.store.EXPECT().GetAllAuthors().Return(authors, nil).Maybe()
			d.store.EXPECT().GetAllBooksCore(0, 0).Return(books, nil).Maybe()
			d.store.EXPECT().GetBookByID(mock.Anything).
				Return(&database.Book{ID: "x"}, nil).Maybe()

			w := doReq(t, h.ListDedupCandidates, http.MethodGet,
				"/api/v1/dedup/candidates?q="+tc.needle, nil, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// TestSearchMatchesTheRightBooks drives the resolver through the handler and
// asserts on WHICH candidate comes back, not merely that a 200 did.
func TestSearchMatchesTheRightBooks(t *testing.T) {
	books, authors := searchCorpus()

	cases := []struct {
		name   string
		needle string
		wantA  string
	}{
		{"title", "norse", "b-title"},
		{"path", "discworld", "b-path"},
		{"author", "gaiman", "b-author"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, d := newHandler(t)
			// One candidate per book so a match is attributable.
			insertCandidate(t, d.es, tc.wantA, "other-side")
			insertCandidate(t, d.es, "b-noauthor", "b-dangling")
			d.store.EXPECT().GetAllAuthors().Return(authors, nil).Once()
			d.store.EXPECT().GetAllBooksCore(0, 0).Return(books, nil).Once()
			d.store.EXPECT().GetBookByID(mock.Anything).
				Return(&database.Book{ID: "x"}, nil).Maybe()

			w := doReq(t, h.ListDedupCandidates, http.MethodGet,
				"/api/v1/dedup/candidates?q="+tc.needle, nil, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
			}
			rows := decodeCandidates(t, w)
			if len(rows) != 1 {
				t.Fatalf("got %d rows, want exactly 1; body=%s", len(rows), w.Body.String())
			}
			if got := rows[0]["entity_a_id"]; got != tc.wantA {
				t.Fatalf("entity_a_id=%v want %s", got, tc.wantA)
			}
		})
	}
}

// TestSearchWithNoNeedleNeverReadsTheLibrary pins that an absent or blank q
// does not pay for a full-library walk. Setting no expectation on the bulk
// readers makes the mock's own assertion fail if either is called.
func TestSearchWithNoNeedleNeverReadsTheLibrary(t *testing.T) {
	for _, q := range []string{"", "%20%20"} {
		h, d := newHandler(t)
		insertCandidate(t, d.es, "book-a", "book-b")
		d.store.EXPECT().GetBookByID(mock.Anything).
			Return(&database.Book{ID: "x"}, nil).Maybe()

		w := doReq(t, h.ListDedupCandidates, http.MethodGet,
			"/api/v1/dedup/candidates?q="+q, nil, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("q=%q status=%d want 200", q, w.Code)
		}
	}
}

// TestSearchRefusesWhenTheLibraryReadFails is the branch the whole design rests
// on. Falling back to a row-only match would return a SHORT list under a 200,
// which reads as "nothing matched" and is indistinguishable from a correct
// empty result.
func TestSearchRefusesWhenTheLibraryReadFails(t *testing.T) {
	t.Run("author read fails", func(t *testing.T) {
		h, d := newHandler(t)
		insertCandidate(t, d.es, "book-a", "book-b")
		// GetAllAuthors runs FIRST, so GetAllBooksCore is never reached; no
		// expectation is set for it on purpose.
		d.store.EXPECT().GetAllAuthors().Return(nil, errors.New("boom")).Once()

		w := doReq(t, h.ListDedupCandidates, http.MethodGet,
			"/api/v1/dedup/candidates?q=dune", nil, nil)
		if w.Code == http.StatusOK {
			t.Fatalf("a failed resolve must NOT return 200; body=%s", w.Body.String())
		}
	})

	t.Run("book read fails", func(t *testing.T) {
		h, d := newHandler(t)
		insertCandidate(t, d.es, "book-a", "book-b")
		d.store.EXPECT().GetAllAuthors().Return(nil, nil).Once()
		d.store.EXPECT().GetAllBooksCore(0, 0).Return(nil, errors.New("boom")).Once()

		w := doReq(t, h.ListDedupCandidates, http.MethodGet,
			"/api/v1/dedup/candidates?q=dune", nil, nil)
		if w.Code == http.StatusOK {
			t.Fatalf("a failed resolve must NOT return 200; body=%s", w.Body.String())
		}
	})
}

func decodeCandidates(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var resp struct {
		Data struct {
			Candidates []map[string]any `json:"candidates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
	}
	return resp.Data.Candidates
}
