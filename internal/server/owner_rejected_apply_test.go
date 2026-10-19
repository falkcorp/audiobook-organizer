// file: internal/server/owner_rejected_apply_test.go
// version: 1.2.0
// guid: d5304ea1-88e0-43c3-9c2b-1b81c01fe9eb
// last-edited: 2026-10-19

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
)

// Regression tests for owner-rejected candidates reaching an apply
// (2026-10-10). A candidate the owner rejected (POST
// /metadata/batch-reject-candidates, stored under rejected_candidate:) must
// never be applied by any path: no pin overrides it.

// rejKV is the owner's rejection keyspace as handleRejectCandidates writes
// it: rejected_candidate:{bookID}:{source}|{title}.
type rejKV map[string]bool

func (k rejKV) ScanPrefix(prefix string) ([]database.KVPair, error) {
	var out []database.KVPair
	for key := range k {
		if strings.HasPrefix(key, prefix) {
			out = append(out, database.KVPair{Key: key, Value: []byte("1")})
		}
	}
	return out, nil
}

// rejectingBooks is fakeBooks whose store also holds the owner's rejections.
type rejectingBooks struct {
	fakeBooks
	kv rejKV
}

func (r rejectingBooks) ScanPrefix(prefix string) ([]database.KVPair, error) {
	return r.kv.ScanPrefix(prefix)
}

func ownerRejectedFixture(t *testing.T) (rejectingBooks, metafetch.MetadataCandidate) {
	t.Helper()
	tenHours := 36000
	books := fakeBooks{"b1": {ID: "b1", Title: "Synthetic Field Notes 1", FilePath: "/lib/A/Synthetic Field Notes/Synthetic Field Notes 1.m4b", Duration: &tenHours}}
	cand := metafetch.MetadataCandidate{Title: "Synthetic Field Notes 1", SeriesPosition: "1", Score: 0.95, DurationSec: tenHours, Source: "Audible"}
	// Stored in another case than the candidate carries: a rejection matches
	// whatever case it was written in (mergeCandidateRows folds case too).
	kv := rejKV{"rejected_candidate:b1:audible|SYNTHETIC FIELD NOTES 1": true}
	return rejectingBooks{fakeBooks: books, kv: kv}, cand
}

// A pinless batch-apply-cached (a script or API call) refused the top
// candidate only on certainty legs; the owner's rejection was never read.
func TestCachedApply_PinlessRefusesOwnerRejectedTop(t *testing.T) {
	books, cand := ownerRejectedFixture(t)
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	out := applyCachedCandidateForBook(svc, books, "b1", false, nil)
	require.False(t, out.Applied, "owner-rejected candidate was applied")
	require.Equal(t, applySkipGateBlocked, out.Reason)
	require.NotNil(t, out.Gate)
	require.Equal(t, "owner_rejected", out.Gate.Reason)
	require.Empty(t, svc.appliedIDs)
}

// Every owner-review pin shape -- the review page's hashless bulk marker, a
// hashed bulk pin and a single-row approval -- lifts the certainty gate. None
// lifts an owner rejection: the owner un-rejects the candidate to apply it.
func TestCachedApply_NoPinOverridesOwnerRejection(t *testing.T) {
	cases := map[string]func(c metafetch.MetadataCandidate) *metafetch.CandidatePin{
		"hashless bulk marker": func(metafetch.MetadataCandidate) *metafetch.CandidatePin {
			return &metafetch.CandidatePin{Origin: metafetch.PinOriginReviewBulk}
		},
		"hashed bulk pin": func(c metafetch.MetadataCandidate) *metafetch.CandidatePin {
			p := metafetch.PinOf(c)
			p.Origin = metafetch.PinOriginReviewBulk
			return &p
		},
		"row approval": func(c metafetch.MetadataCandidate) *metafetch.CandidatePin {
			p := metafetch.PinOf(c)
			p.Origin = metafetch.PinOriginRow
			return &p
		},
	}
	for name, mkPin := range cases {
		t.Run(name, func(t *testing.T) {
			books, cand := ownerRejectedFixture(t)
			cand.Score = 0.50 // below the floor: only an owner override would land it
			svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
			out := applyCachedCandidateForBookTimed(context.Background(), svc, books, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, mkPin(cand), "")
			require.False(t, out.Applied, "owner-rejected candidate was applied: reason=%q err=%v", out.Reason, out.Err)
			require.False(t, out.OwnerReviewed)
			require.Equal(t, applySkipGateBlocked, out.Reason)
			require.NotNil(t, out.Gate)
			require.Equal(t, "owner_rejected", out.Gate.Reason)
			require.Empty(t, svc.appliedIDs)
		})
	}
}

// batch-apply-candidates against an OLDER op: the rejection flipped the
// status only in the op the owner had open, so another op's result for the
// same candidate is still "matched". The plan must still refuse it.
func TestOpResultApply_RefusesCandidateRejectedInAnotherOp(t *testing.T) {
	tenHours := 36000
	books := rejectingBooks{
		fakeBooks: fakeBooks{"b1": {ID: "b1", Title: "Synthetic Field Notes 1", Author: &database.Author{Name: "Synthetic Author A"}, Duration: &tenHours}},
		kv:        rejKV{"rejected_candidate:b1:Audible|Synthetic Field Notes 1": true},
	}
	c := metafetch.MetadataCandidate{Title: "Synthetic Field Notes 1", SeriesPosition: "1", Score: 0.95, DurationSec: tenHours, Source: "Audible"}
	older := CandidateResult{Status: "matched", Candidate: &c}
	older.Book.Title, older.Book.Author = "Synthetic Field Notes 1", "Synthetic Author A"
	p := planOpResultApply(books, "b1", older, nil)
	require.Equal(t, applySkipGateBlocked, p.Reason, "err=%v", p.Err)
	require.NotNil(t, p.Gate)
	require.Equal(t, "owner_rejected", p.Gate.Reason)
}

// maintenance.auto-match-transcribed: the search half offers no rejected
// candidate and the apply half refuses one.
func TestTranscription_RefusesOwnerRejectedTop(t *testing.T) {
	bookID := "book-rej"
	book := &database.Book{ID: bookID, Title: ""}
	cand := metafetch.MetadataCandidate{Title: "The Stable Book", Author: "Stable Author", Score: 0.9, Source: "test"}
	entry := mustCandidateCache(t, bookID, cand)
	store, updateCalls := newTOCTOUCacheStore(t, book, entry, entry)
	withCreatableAuthors(store)
	store.ScanPrefixFunc = rejKV{"rejected_candidate:" + bookID + ":test|The Stable Book": true}.ScanPrefix

	s := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	_, found, err := s.SearchTranscriptionCandidate(context.Background(), bookID, "", "")
	require.NoError(t, err)
	require.False(t, found, "owner-rejected candidate offered to the auto-match gate")
	err = s.ApplyTranscriptionCandidate(context.Background(), bookID, cand.Title, cand.Author)
	require.ErrorIs(t, err, errTranscriptionOwnerRejected)
	require.Empty(t, *updateCalls, "owner-rejected candidate was written")
}

// POST /metadata/bulk-fetch skips a candidate the owner rejected.
func TestBulkFetch_SkipsOwnerRejected(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	aud := testutil.MockAudibleServer(t, func(title string) []testutil.AudibleTestProduct {
		return []testutil.AudibleTestProduct{{
			ASIN: "B0TESTREJ1", Title: title, Authors: []string{"Meta Author"},
			Publisher: "Synthetic Rejected Pub", Language: "eng", ReleaseDate: "2020-01-01",
		}}
	})
	useOnlyAudible(t, aud.URL)

	tempFile := filepath.Join(t.TempDir(), "rej.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	store := database.GetGlobalStore()
	book, err := store.CreateBook(&database.Book{Title: "Book One", FilePath: tempFile, Format: "m4b"})
	require.NoError(t, err)
	// The owner has rejected every candidate this search returns.
	pre, serr := server.metadataFetchService.SearchMetadataForBookWithOptions(book.ID, "", "", "", "", metafetch.SearchOptions{})
	require.NoError(t, serr)
	require.NotEmpty(t, pre.Results)
	for _, c := range pre.Results {
		require.NoError(t, store.SetRaw("rejected_candidate:"+book.ID+":"+c.Source+"|"+c.Title, []byte("1")))
	}

	body, _ := json.Marshal(map[string]any{"book_ids": []string{book.ID}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/bulk-fetch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"status":"owner_rejected"`, "refused for the rejection, not for another reason")

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	if got.Publisher != nil {
		require.NotEqual(t, "Synthetic Rejected Pub", *got.Publisher, "owner-rejected candidate was applied")
	}
}

// candidateTitles decodes a cache row's candidate titles in order.
func candidateTitles(t *testing.T, rows []json.RawMessage) []string {
	t.Helper()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var c metafetch.MetadataCandidate
		require.NoError(t, json.Unmarshal(r, &c))
		out = append(out, c.Title)
	}
	return out
}

// Rejecting a candidate re-orders the book's cached row, so the review lane
// and every slot-0 reader stop offering it; un-rejecting re-orders it back.
func TestRejectCandidates_ReordersCachedRow(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	store := database.GetGlobalStore()
	book, err := store.CreateBook(&database.Book{Title: "Synthetic Field Notes 3", Format: "m4b"})
	require.NoError(t, err)

	rejected := metafetch.MetadataCandidate{Title: "Synthetic Field Notes 3", Source: "Audible", Score: 0.97}
	other := metafetch.MetadataCandidate{Title: "Synthetic Field Notes 3 (Unabridged)", Source: "Audible", Score: 0.93}
	rows := append(candidateJSON(t, rejected), candidateJSON(t, other)...)
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: book.ID, Candidates: rows, FetchedAt: time.Now(), SourceHash: "h1",
	}))
	cr := CandidateResult{Status: "matched", Candidate: &rejected}
	raw, _ := json.Marshal(cr)
	require.NoError(t, store.CreateOperationResult(&database.OperationResult{
		OperationID: "op-rej", BookID: book.ID, ResultJSON: string(raw), Status: "matched",
	}))

	post := func(path string) {
		body, _ := json.Marshal(map[string]any{"operation_id": "op-rej", "book_ids": []string{book.ID}})
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}

	post("/api/v1/metadata/batch-reject-candidates")
	entry, err := store.GetMetadataCache(book.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"Synthetic Field Notes 3 (Unabridged)", "Synthetic Field Notes 3"}, candidateTitles(t, entry.Candidates),
		"the rejected candidate must leave slot 0, and must not be dropped")
	require.Equal(t, "h1", entry.SourceHash, "a re-order touches nothing but the order")

	post("/api/v1/metadata/batch-unreject-candidates")
	entry, err = store.GetMetadataCache(book.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"Synthetic Field Notes 3", "Synthetic Field Notes 3 (Unabridged)"}, candidateTitles(t, entry.Candidates),
		"un-rejecting ranks the candidate by score again")
	keys, err := store.ScanPrefix("rejected_candidate:" + book.ID + ":")
	require.NoError(t, err)
	require.Empty(t, keys)
}

// The review lane marks an owner-rejected candidate, so the owner sees why
// its apply buttons refuse it. The mark rides on the lane's ids= detail rows.
func TestReviewLane_MarksOwnerRejectedCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	book, err := store.CreateBook(&database.Book{Title: "Synthetic Field Notes 3", FilePath: "/lib/B/Synthetic Field Notes 3.m4b", Format: "m4b"})
	require.NoError(t, err)
	cand := metafetch.MetadataCandidate{Title: "Synthetic Field Notes 3", Author: "Synthetic Penwright", Source: "Audible", Score: 0.95}
	raw, err := json.Marshal(cand)
	require.NoError(t, err)
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: book.ID, Candidates: []json.RawMessage{raw}, FetchedAt: time.Now(),
		SearchFingerprint: metafetch.FingerprintPrefix + "x",
	}))
	require.NoError(t, store.SetRaw("rejected_candidate:"+book.ID+":Audible|Synthetic Field Notes 3", []byte("1")))

	h := handlers.NewMetadataCacheHandler(store, metafetch.NewService(store), nil, nil, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// The lane's detail fetch for the rows it shows (ids=), which is where a
	// row's owner_rejected mark comes from now that view=page serves lean rows.
	c.Request = httptest.NewRequest(http.MethodGet, "/x?ids="+book.ID, nil)
	h.GetCacheReviewResults(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data struct {
			Results []struct {
				Book struct {
					ID string `json:"id"`
				} `json:"book"`
				OwnerRejected bool `json:"owner_rejected"`
			} `json:"results"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data.Results, 1)
	require.True(t, body.Data.Results[0].OwnerRejected, "the lane does not mark the rejected candidate")
}

// The Review > Metadata rail's "owner rejected" chip counts page rows, and
// view=page is the only list the lane loads: its rows must carry the mark
// themselves (the lane's ids= detail merge copies only the candidate). A row
// whose candidate the owner did not reject must not carry it.
func TestReviewPage_MarksOwnerRejectedCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	seed := func(title string) *database.Book {
		book, cerr := store.CreateBook(&database.Book{Title: title, FilePath: "/lib/B/" + title + ".m4b", Format: "m4b"})
		require.NoError(t, cerr)
		raw, merr := json.Marshal(metafetch.MetadataCandidate{Title: title, Author: "Synthetic Penwright", Source: "Audible", Score: 0.95})
		require.NoError(t, merr)
		require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: book.ID, Candidates: []json.RawMessage{raw}, FetchedAt: time.Now(),
			SearchFingerprint: metafetch.FingerprintPrefix + "x",
		}))
		return book
	}
	rejected := seed("Synthetic Field Notes 3")
	kept := seed("Synthetic Field Notes 4")
	require.NoError(t, store.SetRaw("rejected_candidate:"+rejected.ID+":Audible|Synthetic Field Notes 3", []byte("1")))

	h := handlers.NewMetadataCacheHandler(store, metafetch.NewService(store), nil, nil, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/x?view=page&limit=50", nil)
	h.GetCacheReviewResults(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data struct {
			Results []struct {
				Book struct {
					ID string `json:"id"`
				} `json:"book"`
				OwnerRejected bool `json:"owner_rejected"`
			} `json:"results"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	got := map[string]bool{}
	for _, r := range body.Data.Results {
		got[r.Book.ID] = r.OwnerRejected
	}
	require.Equal(t, map[string]bool{rejected.ID: true, kept.ID: false}, got,
		"a view=page row must carry owner_rejected for exactly the rejected candidate")
}

// The search dialog's plain fetch replaces the cached row. With the owner's
// rejection stored, the rejected candidate must not come back in slot 0,
// where every bulk apply and the review lane read.
func TestDialogFetch_DoesNotPutOwnerRejectedInSlotZero(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	aud := testutil.MockAudibleServer(t, func(string) []testutil.AudibleTestProduct {
		return []testutil.AudibleTestProduct{
			{ASIN: "B0TESTREJ2", Title: "Synthetic Field Notes 3", Authors: []string{"Synthetic Penwright"}, Language: "eng", ReleaseDate: "2020-01-01"},
			{ASIN: "B0TESTREJ3", Title: "Synthetic Field Notes 3 Returns", Authors: []string{"Synthetic Penwright"}, Language: "eng", ReleaseDate: "2021-01-01"},
		}
	})
	useOnlyAudible(t, aud.URL)
	tempFile := filepath.Join(t.TempDir(), "qh.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	store := database.GetGlobalStore()
	book, err := store.CreateBook(&database.Book{Title: "Synthetic Field Notes 3", FilePath: tempFile, Format: "m4b"})
	require.NoError(t, err)

	fetch := func() []string {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/search-metadata?refresh=true", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		entry, gerr := store.GetMetadataCache(book.ID)
		require.NoError(t, gerr)
		require.NotNil(t, entry)
		return candidateTitles(t, entry.Candidates)
	}
	first := fetch()
	require.GreaterOrEqual(t, len(first), 2, "the fixture needs two candidates: %v", first)
	require.NoError(t, store.SetRaw("rejected_candidate:"+book.ID+":Audible|"+first[0], []byte("1")))

	again := fetch()
	require.Len(t, again, len(first), "a refetch drops no candidate")
	require.NotEqual(t, first[0], again[0], "the owner-rejected candidate is back in slot 0")
	require.Equal(t, first[0], again[len(again)-1], "the owner-rejected candidate ranks last")
}

// The single-book apply (the search dialog's Apply, POST
// /audiobooks/:id/apply-metadata) refuses a candidate the owner rejected
// with 409 owner_rejected, and the dialog's search marks it.
func TestSingleApply_RefusesOwnerRejected(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	store := database.GetGlobalStore()
	book, err := store.CreateBook(&database.Book{Title: "Synthetic Field Notes 3", Format: "m4b"})
	require.NoError(t, err)
	cand := metafetch.MetadataCandidate{Title: "Synthetic Field Notes 3 Returns", Author: "Synthetic Penwright", Source: "Audible", Publisher: "Synthetic Rejected Pub", Score: 0.95}
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: book.ID, Candidates: candidateJSON(t, cand), FetchedAt: time.Now(),
	}))
	require.NoError(t, store.SetRaw("rejected_candidate:"+book.ID+":Audible|Synthetic Field Notes 3 Returns", []byte("1")))

	// The dialog's cached search marks the rejected candidate.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/search-metadata", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var search struct {
		Data struct {
			Results []struct {
				Title      string `json:"title"`
				ApplyCheck *struct {
					OwnerRejected bool `json:"owner_rejected"`
				} `json:"apply_check"`
			} `json:"results"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &search))
	require.Len(t, search.Data.Results, 1)
	require.NotNil(t, search.Data.Results[0].ApplyCheck, "the dialog does not mark the rejected candidate")
	require.True(t, search.Data.Results[0].ApplyCheck.OwnerRejected)

	body, _ := json.Marshal(map[string]any{"candidate": cand, "write_back": false})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/apply-metadata", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "owner_rejected")
	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, "Synthetic Field Notes 3", got.Title)
	require.Nil(t, got.Publisher, "owner-rejected candidate was applied")
}

// S1b (2026-10-10): the owner un-rejects a candidate by book and candidate
// alone (no operation id), from the metadata search windows. The rejection
// stops matching, the cached row ranks the candidate by score again, and a
// rejection of another record of the same title (its own ASIN) stays.
func TestUnrejectCandidate_BookScoped(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	store := database.GetGlobalStore()
	book, err := store.CreateBook(&database.Book{Title: "Synthetic Field Notes 3", Format: "m4b"})
	require.NoError(t, err)

	abridged := metafetch.MetadataCandidate{Title: "Synthetic Field Notes 3", Source: "Audible", ASIN: "B0SYNABR01", Score: 0.97}
	unabridged := metafetch.MetadataCandidate{Title: "Synthetic Field Notes 3", Source: "Audible", ASIN: "B0SYNUNA02", Score: 0.95}
	other := metafetch.MetadataCandidate{Title: "Synthetic Field Notes 3 (Companion)", Source: "Audible", Score: 0.93}
	rows := append(append(candidateJSON(t, other), candidateJSON(t, abridged)...), candidateJSON(t, unabridged)...)
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: book.ID, Candidates: rows, FetchedAt: time.Now()}))
	require.NoError(t, metafetch.RecordRejection(store, book.ID, &abridged))
	require.NoError(t, metafetch.RecordRejection(store, book.ID, &unabridged))

	post := func(body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/unreject-candidate", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		return w
	}

	w := post(map[string]any{"book_id": book.ID, "candidate": unabridged})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"unrejected":true`)
	require.Contains(t, w.Body.String(), `"still_rejected":false`)

	rejected, err := metafetch.LoadRejectedCandidates(store, book.ID)
	require.NoError(t, err)
	require.False(t, rejected.HasCandidate(&unabridged), "the un-rejected candidate still matches a rejection")
	require.True(t, rejected.HasCandidate(&abridged), "un-rejecting one record removed another record's rejection")

	entry, err := store.GetMetadataCache(book.ID)
	require.NoError(t, err)
	require.Len(t, entry.Candidates, 3)
	var top metafetch.MetadataCandidate
	require.NoError(t, json.Unmarshal(entry.Candidates[0], &top))
	require.Equal(t, "B0SYNUNA02", top.ASIN, "the un-rejected candidate ranks by score again, ahead of the still-rejected one")
	var last metafetch.MetadataCandidate
	require.NoError(t, json.Unmarshal(entry.Candidates[2], &last))
	require.Equal(t, "B0SYNABR01", last.ASIN, "the still-rejected candidate stays last")

	// Nothing left to un-reject for it: unrejected is false.
	w = post(map[string]any{"book_id": book.ID, "candidate": unabridged})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"unrejected":false`)

	// S4: an Open Library record rejected on its own shares the ISBN of an
	// Audible record. Undoing the Audible record leaves the Open Library
	// rejection, which still refuses it: still_rejected.
	withISBN := metafetch.MetadataCandidate{Title: "Synthetic Field Notes 3", Source: "Audible", ASIN: "B0SYNISB03", ISBN13: "9780306406157"}
	olRecord := metafetch.MetadataCandidate{Title: "Synthetic Field Notes Three", Source: "Open Library", ISBN10: "0306406152"}
	require.NoError(t, metafetch.RecordRejection(store, book.ID, &withISBN))
	require.NoError(t, metafetch.RecordRejection(store, book.ID, &olRecord))
	w = post(map[string]any{"book_id": book.ID, "candidate": withISBN})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"unrejected":true`)
	require.Contains(t, w.Body.String(), `"still_rejected":true`)
	rejected, err = metafetch.LoadRejectedCandidates(store, book.ID)
	require.NoError(t, err)
	require.True(t, rejected.HasCandidate(&olRecord), "another source's rejection sharing the ISBN was removed")

	require.Equal(t, http.StatusBadRequest, post(map[string]any{"book_id": book.ID}).Code)
	require.Equal(t, http.StatusNotFound, post(map[string]any{"book_id": "no-such-book", "candidate": unabridged}).Code)
}
