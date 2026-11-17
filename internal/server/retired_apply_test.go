// file: internal/server/retired_apply_test.go
// version: 1.0.1
// guid: 3f0c8e52-7a14-4b69-9d27-5c1e6a8b0f43
// last-edited: 2026-11-17

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// A retired book is one the library has put away: in the trash
// (MarkedForDeletion) or absorbed into another book (MergedIntoBookID). No
// unattended metadata apply may write a candidate onto one, and no candidate
// fetch may spend provider quota on one. Every test here uses one retirement
// field at a time so a check that reads only the other field fails.

// retiredShape is one way a book can be retired.
type retiredShape struct {
	name   string
	retire func(b *database.Book)
}

var retiredShapes = []retiredShape{
	{"soft_deleted", func(b *database.Book) { yes := true; b.MarkedForDeletion = &yes }},
	{"merged_into", func(b *database.Book) { into := "survivor-book-id"; b.MergedIntoBookID = &into }},
}

// liveShape is the control: the same fixture, not retired.
var liveShape = retiredShape{"live", func(*database.Book) {}}

const (
	fixtureTitle  = "The Long Halloween"
	fixtureAuthor = "Alfred Pennyworth"
)

// retiredBook is book id in the given shape, with a duration and a folder
// that names its title and author so the certainty gate passes it on its own
// (a refusal can then only be the retirement check's).
func retiredBook(id string, shape retiredShape) *database.Book {
	dur := 36000
	b := &database.Book{ID: id, Title: fixtureTitle,
		FilePath: "/lib/" + fixtureAuthor + "/" + fixtureTitle + "/" + fixtureTitle + ".m4b", Duration: &dur}
	shape.retire(b)
	return b
}

// retiredBooks is fakeBooks holding b1 in the given shape.
func retiredBooks(shape retiredShape) fakeBooks {
	return fakeBooks{"b1": retiredBook("b1", shape)}
}

// retiredCandidateRows is the cache row for retiredBook: one candidate the
// gate passes, and that candidate.
func retiredCandidateRows(t *testing.T) ([]json.RawMessage, metafetch.MetadataCandidate) {
	t.Helper()
	c := metafetch.MetadataCandidate{Title: fixtureTitle, Author: fixtureAuthor, Score: 0.95, DurationSec: 36000}
	blob, err := json.Marshal(c)
	require.NoError(t, err)
	return []json.RawMessage{blob}, c
}

// reviewBulkPin is the pin the review page's bulk buttons send: a hash-checked
// owner-review pin that is not a single row.
func reviewBulkPin(c metafetch.MetadataCandidate) *metafetch.CandidatePin {
	p := metafetch.PinOf(c)
	p.Origin = metafetch.PinOriginReviewBulk
	return &p
}

// Control: a live book with the same fixture applies, so the refusals below
// are the retirement check's doing.
func TestRetiredApply_ControlLiveBookApplies(t *testing.T) {
	raw, _ := retiredCandidateRows(t)
	svc := &fakeApplySvc{candidates: raw}
	out := applyCachedCandidateForBookTimed(context.Background(), svc, retiredBooks(liveShape), "b1", false, nil,
		metafetch.NewApplyPhaseTimings(), nil, nil, "")
	require.True(t, out.Applied, "control did not apply: %+v", out)
	require.Len(t, svc.appliedIDs, 1)
}

// The cached bulk apply (batch-apply-cached): no pin, a bulk review pin and
// the hashless select-all marker are all unattended for the book, so none
// may write.
func TestRetiredApply_CachedBulkApplyRefusesRetiredBook(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			raw, cand := retiredCandidateRows(t)
			pins := map[string]*metafetch.CandidatePin{
				"no_pin":   nil,
				"bulk_pin": reviewBulkPin(cand),
				"marker":   {Origin: metafetch.PinOriginReviewBulk}, // hashless owner marker
			}
			for label, pin := range pins {
				t.Run(label, func(t *testing.T) {
					svc := &fakeApplySvc{candidates: raw}
					out := applyCachedCandidateForBookTimed(context.Background(), svc, retiredBooks(shape), "b1", true, nil,
						metafetch.NewApplyPhaseTimings(), nil, pin, "")
					assert.False(t, out.Applied, "retired book was applied: %+v", out)
					assert.Empty(t, svc.appliedIDs, "ApplyMetadataCandidateWithOptions ran")
					assert.Empty(t, svc.finishCalls, "file work ran")
					assert.Empty(t, svc.preflightIDs, "rename preflight ran")
				})
			}
		})
	}
}

// retiredOpResultFixture is a gate-passing op-result candidate for book b1, so a
// refusal can only come from the retirement check.
func retiredOpResultFixture(retire func(b *database.Book)) (fakeBooks, CandidateResult) {
	tenHours := 36000
	b := &database.Book{ID: "b1", Title: "Hush 1", Author: &database.Author{Name: fixtureAuthor}, Duration: &tenHours}
	if retire != nil {
		retire(b)
	}
	c := metafetch.MetadataCandidate{Title: "Hush 1", SeriesPosition: "1", Score: 0.95, DurationSec: tenHours}
	cr := CandidateResult{Status: "matched", Candidate: &c}
	cr.Book.Title, cr.Book.Author = "Hush 1", fixtureAuthor
	return fakeBooks{"b1": b}, cr
}

// Control: the live book passes the plan, so the refusals below are the
// retirement check's doing.
func TestRetiredApply_ControlOpResultPlanAllowsLiveBook(t *testing.T) {
	books, cr := retiredOpResultFixture(nil)
	plan := planOpResultApply(books, "b1", cr, nil)
	require.Empty(t, plan.Reason, "control refused: %+v err=%v", plan.Reason, plan.Err)
}

// batch-apply-candidates (and metadata.apply-when-scanned for it) applies only
// when planOpResultApply says so.
func TestRetiredApply_OpResultPlanRefusesRetiredBook(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			books, cr := retiredOpResultFixture(shape.retire)
			plan := planOpResultApply(books, "b1", cr, nil)
			assert.Equal(t, "book_retired", plan.Reason, "the plan would apply a retired book: %+v", plan)
		})
	}
}

// The dry run must not report a retired book as one that would apply.
func TestRetiredApply_PreviewDoesNotSayApply(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			raw, _ := retiredCandidateRows(t)
			svc := fakePreviewSvc{&fakeApplySvc{candidates: raw}}
			plan := planCachedApply(svc, retiredBooks(shape), "b1", nil, nil)
			row := previewBulkApplyRow(svc, "b1", plan, false, false)
			assert.NotEqual(t, previewVerdictApply, row.Verdict, "preview row %+v", row)
		})
	}
}

// A single review row (a row pin) is the owner picking this candidate for this
// one book; that is not unattended and stays as it is.
func TestRetiredApply_RowPinStaysManual(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			raw, cand := retiredCandidateRows(t)
			svc := &fakeApplySvc{candidates: raw}
			out := applyCachedCandidateForBookTimed(context.Background(), svc, retiredBooks(shape), "b1", false, nil,
				metafetch.NewApplyPhaseTimings(), nil, rowPin(cand), "")
			assert.True(t, out.Applied, "the owner's single-row apply changed: %+v", out)
		})
	}
}

// retiredServerFixture creates a live book and one retired book per shape,
// each with a cache row stale past the TTL, so every fetch-list builder would
// pick it up.
type retiredServerFixture struct {
	live    string
	retired map[string]string // shape name -> book id
}

func newRetiredServerFixture(t *testing.T, s *Server) retiredServerFixture {
	t.Helper()
	store := s.storeForWiring()
	old := time.Now().Add(-2 * database.MetadataCacheTTL)
	cand, err := json.Marshal(map[string]any{"title": "Hush", "score": 0.9})
	require.NoError(t, err)
	mk := func(title string, retire func(*database.Book)) string {
		t.Helper()
		b := database.Book{Title: title, FilePath: "/lib/" + title + "/b.m4b"}
		if retire != nil {
			retire(&b)
		}
		got, err := store.CreateBook(&b)
		require.NoError(t, err)
		require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: got.ID, Candidates: []json.RawMessage{cand}, FetchedAt: old, SourceHash: "h", SearchFingerprint: "f",
		}))
		return got.ID
	}
	fx := retiredServerFixture{retired: map[string]string{}}
	fx.live = mk("Batman Year One", nil)
	for _, shape := range retiredShapes {
		fx.retired[shape.name] = mk("Batman "+shape.name, shape.retire)
	}
	return fx
}

func fetchHandlerParams(t *testing.T, s *Server, body string) (metadataCandidateFetchOpParams, int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/metadata/batch-fetch-candidates", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchFetchCandidates(c)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var resp struct {
		Data struct {
			OperationID string `json:"operation_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	row, err := s.storeForWiring().GetOperationV2(resp.Data.OperationID)
	require.NoError(t, err)
	require.NotNil(t, row)
	var params metadataCandidateFetchOpParams
	require.NoError(t, json.Unmarshal([]byte(row.Params), &params))
	return params, w.Code
}

func quietFetchService(s *Server) {
	mfs := metafetch.NewService(s.storeForWiring())
	mfs.SetOverrideSources([]metadata.MetadataSource{&countingSource{name: "SrcA"}})
	s.metadataFetchService = mfs
}

// handleBatchFetchCandidates with an explicit book_ids list. One server per
// shape: a second request for the live book would find it already being
// fetched by the first and enqueue nothing.
func TestRetiredFetch_ExplicitIDListDropsRetiredBooks(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			quietFetchService(s)
			fx := newRetiredServerFixture(t, s)
			ids, _ := json.Marshal([]string{fx.live, fx.retired[shape.name]})
			params, _ := fetchHandlerParams(t, s, `{"book_ids":`+string(ids)+`,"force":true}`)
			assert.ElementsMatch(t, []string{fx.live}, params.BookIDs)
		})
	}
}

// A person's "Search again" on one retired book is the owner working on that
// book by hand: it is still searched, and the op does not skip it.
func TestRetiredFetch_InteractiveSingleBookSearchStillRuns(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			quietFetchService(s)
			fx := newRetiredServerFixture(t, s)
			params, _ := fetchHandlerParams(t, s, `{"book_ids":["`+fx.retired[shape.name]+`"],"force":true,"interactive":true}`)
			assert.Equal(t, []string{fx.retired[shape.name]}, params.BookIDs)
			assert.True(t, params.Interactive)
		})
	}
}

// handleBatchFetchCandidates with the stale flag (StaleCachedBookIDs).
func TestRetiredFetch_StaleListDropsRetiredBooks(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	quietFetchService(s)
	fx := newRetiredServerFixture(t, s)
	params, _ := fetchHandlerParams(t, s, `{"stale":true}`)
	assert.ElementsMatch(t, []string{fx.live}, params.BookIDs, "stale refetch list")
}

// The review rail's stale count and the refetch-all-stale set come from one
// rule (handlers.StaleCachedBookIDs); a retired book is in neither.
func TestRetiredFetch_StaleRuleSkipsRetiredBooks(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	quietFetchService(s)
	fx := newRetiredServerFixture(t, s)
	ids, err := handlers.StaleCachedBookIDs(t.Context(), s.storeForWiring(), s.metadataFetchService)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{fx.live}, ids)
}

// handleBatchFetchCandidates with a selection filter (resolveFilterToBookIDs).
func TestRetiredFetch_SelectionListDropsRetiredBooks(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	quietFetchService(s)
	fx := newRetiredServerFixture(t, s)
	params, _ := fetchHandlerParams(t, s,
		`{"selection":{"filter":{"field_filters":[{"field":"title","value":"Batman"}]}},"force":true}`)
	assert.Contains(t, params.BookIDs, fx.live, "fixture: the selection must reach the live book")
	for _, shape := range retiredShapes {
		assert.NotContains(t, params.BookIDs, fx.retired[shape.name], shape.name)
	}
}

// The scheduled candidate fetch's own selection (unfetched).
func TestRetiredFetch_UnfetchedSelectionSkipsRetiredBooks(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{&countingSource{name: "SrcA"}})
	s.metadataFetchService = mfs
	mk := func(title string, retire func(*database.Book)) string {
		b := database.Book{Title: title, FilePath: "/lib/" + title + "/b.m4b"}
		if retire != nil {
			retire(&b)
		}
		got, err := store.CreateBook(&b)
		require.NoError(t, err)
		return got.ID
	}
	live := mk("Batman Hush", nil)
	retired := map[string]string{}
	for _, shape := range retiredShapes {
		retired[shape.name] = mk("Batman "+shape.name, shape.retire)
	}
	sel, err := unfetchedCandidateBookIDs(context.Background(), store, mfs, s.newFolderMemo(store), nil, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{live}, sel.IDs)
	for _, shape := range retiredShapes {
		assert.NotContains(t, sel.IDs, retired[shape.name], shape.name)
	}
}

// The op itself: whatever list reaches it (a queued run, a resumed one), a
// retired book costs no provider call and gets a "skipped" result.
func TestRetiredFetch_OpSpendsNoQuotaOnRetiredBooks(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()
	src := &countingSource{name: "SrcA"}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})
	s.metadataFetchService = mfs
	fx := newRetiredServerFixture(t, s)
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			before := sourceCalls(src)
			got := runCandidateFetch(t, s, "op-retired-"+shape.name, []string{fx.retired[shape.name]}, true)
			require.Contains(t, got, fx.retired[shape.name])
			assert.Equal(t, "skipped", got[fx.retired[shape.name]].Status)
			assert.Equal(t, before, sourceCalls(src), "a provider was asked about a retired book")
		})
	}
}

// The op's per-book skip does not apply to a person's own single-book search
// (metabatch.FetchOpParams.Interactive).
func TestRetiredFetch_OpSearchesInteractiveRetiredBook(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()
	src := &countingSource{name: "SrcA"}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})
	s.metadataFetchService = mfs
	fx := newRetiredServerFixture(t, s)
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			before := sourceCalls(src)
			params, err := json.Marshal(metadataCandidateFetchOpParams{BookIDs: []string{fx.retired[shape.name]}, Force: true, Interactive: true})
			require.NoError(t, err)
			require.NoError(t, s.runMetadataCandidateFetchOp(context.Background(), params, &resumeRecorder{opID: "op-interactive-" + shape.name}))
			assert.Greater(t, sourceCalls(src), before, "the owner's own search was skipped")
		})
	}
}

// dropRetiredBookIDs keeps order, drops both retirement shapes, and keeps an
// id the store cannot read (the op re-reads it).
func TestDropRetiredBookIDs(t *testing.T) {
	trash := true
	into := "survivor-book-id"
	empty := ""
	books := fakeBooks{
		"live":     {ID: "live"},
		"trash":    {ID: "trash", MarkedForDeletion: &trash},
		"merged":   {ID: "merged", MergedIntoBookID: &into},
		"unmerged": {ID: "unmerged", MergedIntoBookID: &empty}, // an un-merge clears to ""; not retired
	}
	kept, n := dropRetiredBookIDs(books, []string{"live", "trash", "other", "merged", "unmerged"})
	assert.Equal(t, []string{"live", "other", "unmerged"}, kept)
	assert.Equal(t, 2, n)
}

// The all_cached dry run lists a retired book as a skipped book_retired row
// and counts it in the summary; it is never counted as would-apply.
func TestRetiredApply_PreviewRunReportsRetiredRow(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			books := retiredBooks(shape)
			books["b2"] = retiredBook("b2", liveShape)
			raw, _ := retiredCandidateRows(t)
			svc := listingPreviewSvc{fakePreviewSvc: fakePreviewSvc{&fakeApplySvc{candidates: raw}}, ids: []string{"b1", "b2"}}
			rep := &previewTestReporter{}
			res := &previewResults{}
			require.NoError(t, runBulkApplyPreview(context.Background(), rep, svc, books, res, "op-preview", bulkApplyPreviewParams{AllCached: true}))
			rows := map[string]bulkApplyPreviewRow{}
			for _, r := range res.rows {
				if r.BookID == previewIndexRowID {
					continue
				}
				var row bulkApplyPreviewRow
				require.NoError(t, json.Unmarshal([]byte(r.ResultJSON), &row))
				rows[r.BookID] = row
			}
			assert.Equal(t, previewVerdictSkipped, rows["b1"].Verdict)
			assert.Equal(t, applySkipBookRetired, rows["b1"].Reason)
			assert.Equal(t, previewVerdictApply, rows["b2"].Verdict, "control: the live book")
			last := rep.msgs[len(rep.msgs)-1]
			assert.Contains(t, last, "1 would apply")
			assert.Contains(t, last, "skipped as retired: 1")
		})
	}
}
