// file: internal/server/handlers/duplicates/handler_test.go
// version: 1.13.0
// guid: 62637af9-347f-4f38-b42b-d90ff3ab3654
// last-edited: 2026-10-22

// Tests for the duplicates-domain handlers. The store / merge-service /
// metadata-fetch-service / operations-registry deps are generated mocks; the
// injected helper funcs (getMergeService, computeSeriesPrunePreview) are stub
// closures that record their invocations or return canned payloads. There is at
// least one test per public method (15 methods). The five retired
// /audiobooks/duplicates handlers went with 03-PR12; their 410 stubs are
// tested in package server (wire_dedup_routes_gone_test.go).

package duplicates_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	metadatamocks "github.com/falkcorp/audiobook-organizer/internal/metadata/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates"
	duplicatesmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates/mocks"
)

func init() { gin.SetMode(gin.TestMode) }

// recorders captures the side effects of the injected helper closures so tests
// can assert they fired.
type recorders struct {
	prunePreviewCalls  int
	prunePreviewResult any
	prunePreviewErr    error
}

type testDeps struct {
	store    *duplicatesmocks.MockDuplicatesStore
	reg      *duplicatesmocks.MockOperationsRegistry
	merge    *duplicatesmocks.MockMergeService
	metaSvc  *duplicatesmocks.MockMetadataFetchService
	cache    *cache.Cache[gin.H]
	rec      *recorders
}

type cfg struct {
	hasReg, hasMerge, hasMeta bool
}

func noReg(c *cfg) { c.hasReg = false }

// newHandler builds a Handler with fresh mocks. The dedupCache is a real
// in-memory cache.Cache[gin.H]. Any dep can be left out via the no* options to
// exercise the in-method nil guards.
func newHandler(t *testing.T, opts ...func(*cfg)) (*duplicates.Handler, testDeps) {
	t.Helper()
	cf := &cfg{hasReg: true, hasMerge: true, hasMeta: true}
	for _, o := range opts {
		o(cf)
	}

	store := duplicatesmocks.NewMockDuplicatesStore(t)
	reg := duplicatesmocks.NewMockOperationsRegistry(t)
	mergeMock := duplicatesmocks.NewMockMergeService(t)
	metaMock := duplicatesmocks.NewMockMetadataFetchService(t)
	dc := cache.New[gin.H]("dedup-test", time.Minute)
	rec := &recorders{}

	var regArg duplicates.OperationsRegistry
	if cf.hasReg {
		regArg = reg
	}
	var metaArg duplicates.MetadataFetchService
	if cf.hasMeta {
		metaArg = metaMock
	}

	h := duplicates.New(
		store,
		dc,
		regArg,
		metaArg,
		func() duplicates.MergeService {
			if cf.hasMerge {
				return mergeMock
			}
			return nil
		},
		func() (any, error) {
			rec.prunePreviewCalls++
			return rec.prunePreviewResult, rec.prunePreviewErr
		},
	)
	return h, testDeps{store, reg, mergeMock, metaMock, dc, rec}
}

// doReq drives a single handler with the supplied method/url/body.
func doReq(t *testing.T, fn gin.HandlerFunc, method, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, rdr)
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	fn(c)
	return w
}

// assertReturnsV2OpID checks that an async duplicates trigger responded with the
// id EnqueueOp returned -- the v2 operation id -- and not the id of a legacy v1
// operation row.
//
// This is the whole bug these endpoints had. The client polls the returned id
// against GET /operations/v2/:id, which reads the v2 store and does not fall
// back to the retired v1 table, so a v1 id 404s and the UI reports failure for
// work that ran fine. The previous version of these tests asserted only the 202
// and so could not see it: the mock returned "rid" from EnqueueOp and "op-1"
// from CreateOperation, the handler sent "op-1", and the test passed.
func assertReturnsV2OpID(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body.String())
	}
	if body.Data.ID != "rid" {
		t.Fatalf("want the v2 op id %q from EnqueueOp, got %q -- body %s",
			"rid", body.Data.ID, w.Body.String())
	}
}

// --- CombineBooks ---

func combineBody(keep string, merge []string) map[string]any {
	return map[string]any{"keep_id": keep, "merge_ids": merge}
}

// The handler only checks the request and queues the op: it answers 202 with
// the v2 op id and never calls the combine itself (the merge mock has no
// CombineBooks method, so a call would not compile).
func TestCombineBooks_Enqueues202WithoutRunningTheCombine(t *testing.T) {
	h, d := newHandler(t)
	d.merge.EXPECT().PreflightCombine("a", []string{"b", "c"}).Return(nil)
	var got dedup.BookCombineParams
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.book-combine", mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, params any, _ ...opsregistry.EnqueueOption) (string, error) {
			got = params.(dedup.BookCombineParams)
			return "rid", nil
		})
	w := doReq(t, h.CombineBooks, http.MethodPost, "/audiobooks/combine",
		map[string]any{"keep_id": "a", "merge_ids": []string{"b", "c"}, "override": map[string]string{"title": "T"}})
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	assertReturnsV2OpID(t, w)
	if got.KeepID != "a" || len(got.MergeIDs) != 2 || got.Override == nil || got.Override.Title != "T" {
		t.Fatalf("op params = %+v, want keep a, two merge ids and the override", got)
	}
	if !strings.Contains(w.Body.String(), `"type":"book-combine"`) {
		t.Fatalf("response does not name the operation type: %s", w.Body.String())
	}
}

func TestCombineBooks_TooFew(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.CombineBooks, http.MethodPost, "/audiobooks/combine", combineBody("a", []string{}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// A repeated id, or keep_id repeated in merge_ids, is a malformed request: 400,
// nothing queued, no store read.
func TestCombineBooks_RepeatedIDIs400(t *testing.T) {
	for name, merge := range map[string][]string{"keep in merge_ids": {"a", "b"}, "repeated loser": {"b", "b"}} {
		t.Run(name, func(t *testing.T) {
			h, _ := newHandler(t)
			w := doReq(t, h.CombineBooks, http.MethodPost, "/audiobooks/combine", combineBody("a", merge))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCombineBooks_MergeServiceMissingIs500(t *testing.T) {
	h, _ := newHandler(t, func(c *cfg) { c.hasMerge = false })
	w := doReq(t, h.CombineBooks, http.MethodPost, "/audiobooks/combine", combineBody("a", []string{"b"}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCombineBooks_RegistryMissingIs500(t *testing.T) {
	h, d := newHandler(t, noReg)
	d.merge.EXPECT().PreflightCombine("a", []string{"b"}).Return(nil)
	w := doReq(t, h.CombineBooks, http.MethodPost, "/audiobooks/combine", combineBody("a", []string{"b"}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", w.Code, w.Body.String())
	}
}

// The refusals the service gives before it writes anything answer the HTTP
// call, with the service's reason, and queue nothing (the registry mock has no
// EnqueueOp expectation, so an enqueue would fail the test).
func TestCombineBooks_PreflightRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"missing book is 404", &merge.BookNotFoundError{BookID: "b"}, http.StatusNotFound, ""},
		{"soft-deleted participant is 409", &merge.SoftDeletedInputError{BookID: "b"}, http.StatusConflict, "b"},
		{"soft-deleted keep is 409", &merge.SoftDeletedInputError{BookID: "a", AsPrimary: true}, http.StatusConflict, "a"},
		{"iTunes-protected is 409", &merge.ITunesProtectedError{BookID: "b", Reason: "in the iTunes library"}, http.StatusConflict, "iTunes"},
		// A not-found that is only a substring of some other error's text is
		// not a 404: respondMergeError maps the typed error, not the wording.
		{"untyped not-found text is 500", errString("index shard not found while loading"), http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, d := newHandler(t)
			d.merge.EXPECT().PreflightCombine("a", []string{"b"}).Return(tc.err)
			w := doReq(t, h.CombineBooks, http.MethodPost, "/audiobooks/combine", combineBody("a", []string{"b"}))
			if w.Code != tc.code {
				t.Fatalf("want %d, got %d: %s", tc.code, w.Code, w.Body.String())
			}
			if tc.body != "" && !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("response must carry the reason (%q), got %s", tc.body, w.Body.String())
			}
		})
	}
}

// --- LinkBooks ---

func TestLinkBooks_Enqueues202(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("keep").Return(&database.Book{ID: "keep"}, nil)
	d.merge.EXPECT().PreflightLink("keep", []string{"m1"}).Return(nil)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.book-merge", mock.Anything).Return("rid", nil)
	w := doReq(t, h.LinkBooks, http.MethodPost, "/audiobooks/link",
		map[string]any{"keep_id": "keep", "merge_ids": []string{"m1"}})
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	assertReturnsV2OpID(t, w)
}

func TestLinkBooks_KeepNotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("keep").Return(nil, nil)
	w := doReq(t, h.LinkBooks, http.MethodPost, "/audiobooks/link",
		map[string]any{"keep_id": "keep", "merge_ids": []string{"m1"}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}

// A merge the operation would refuse after running is refused here with a 409
// carrying the reason, and nothing is queued (no EnqueueOp expectation: an
// unexpected call fails the test).
func TestLinkBooks_PreflightRefusalIs409AndQueuesNothing(t *testing.T) {
	cases := map[string]error{
		"trashed keep":   &merge.SoftDeletedInputError{BookID: "keep", AsPrimary: true},
		"iTunes sibling": &merge.ITunesProtectedError{BookID: "sibling", Reason: "its file is inside the iTunes library"},
	}
	for name, refusal := range cases {
		t.Run(name, func(t *testing.T) {
			h, d := newHandler(t)
			d.store.EXPECT().GetBookByID("keep").Return(&database.Book{ID: "keep"}, nil)
			d.merge.EXPECT().PreflightLink("keep", []string{"m1"}).Return(refusal)
			w := doReq(t, h.LinkBooks, http.MethodPost, "/audiobooks/link",
				map[string]any{"keep_id": "keep", "merge_ids": []string{"m1"}})
			if w.Code != http.StatusConflict {
				t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), refusal.Error()) {
				t.Fatalf("409 must carry the reason %q, got %s", refusal.Error(), w.Body.String())
			}
		})
	}
}

// A store failure loading keep_id is not "the book is gone": the store returns
// (nil, nil) for a missing row and an error only for I/O or corruption.
func TestLinkBooks_KeepLoadFailureIs500(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("keep").Return(nil, errors.New("sstable block not found"))
	w := doReq(t, h.LinkBooks, http.MethodPost, "/audiobooks/link",
		map[string]any{"keep_id": "keep", "merge_ids": []string{"m1"}})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", w.Code, w.Body.String())
	}
}

// --- ListDuplicateAuthors ---

func TestListDuplicateAuthors_EmptyNeedsRefresh(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.ListDuplicateAuthors, http.MethodGet, "/authors/duplicates", nil)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("needs_refresh")) {
		t.Fatalf("want 200 + needs_refresh, got %d: %s", w.Code, w.Body.String())
	}
}

// --- RefreshDuplicateAuthors ---

func TestRefreshDuplicateAuthors_Enqueues202(t *testing.T) {
	h, d := newHandler(t)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.author-scan", mock.Anything).Return("rid", nil)
	w := doReq(t, h.RefreshDuplicateAuthors, http.MethodPost, "/authors/duplicates/refresh", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	assertReturnsV2OpID(t, w)
}

// launchOp's nil-registry guard answers 500 rather than panicking. (Ported
// from the retired book-scan trigger's test, 03-PR12.)
func TestRefreshDuplicateAuthors_NoRegistry(t *testing.T) {
	h, _ := newHandler(t, noReg)
	w := doReq(t, h.RefreshDuplicateAuthors, http.MethodPost, "/authors/duplicates/refresh", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
}

// --- ListSeriesDuplicates ---

func TestListSeriesDuplicates_EmptyNeedsRefresh(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.ListSeriesDuplicates, http.MethodGet, "/series/duplicates", nil)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("needs_refresh")) {
		t.Fatalf("want 200 + needs_refresh, got %d: %s", w.Code, w.Body.String())
	}
}

// --- RefreshSeriesDuplicates ---

func TestRefreshSeriesDuplicates_Enqueues202(t *testing.T) {
	h, d := newHandler(t)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.series-scan", mock.Anything).Return("rid", nil)
	w := doReq(t, h.RefreshSeriesDuplicates, http.MethodPost, "/series/duplicates/refresh", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	assertReturnsV2OpID(t, w)
}

// --- ValidateDedupEntry ---

func TestValidateDedupEntry_WithSeriesMatch(t *testing.T) {
	h, d := newHandler(t)
	src := metadatamocks.NewMockMetadataSource(t)
	src.EXPECT().Name().Return("openlibrary")
	src.EXPECT().SearchByTitle(mock.Anything, "Mistborn").Return([]metadata.BookMetadata{
		{Title: "Mistborn", Author: "Sanderson", Series: "Mistborn", SeriesPosition: "1"},
		{Title: "NoSeries", Author: "X"}, // dropped for type=series
	}, nil)
	d.metaSvc.EXPECT().BuildSourceChain().Return([]metadata.MetadataSource{src})
	w := doReq(t, h.ValidateDedupEntry, http.MethodPost, "/dedup/validate",
		map[string]any{"query": "Mistborn", "type": "series"})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("openlibrary")) {
		t.Fatalf("expected source in body, got %s", w.Body.String())
	}
}

func TestValidateDedupEntry_NoSources(t *testing.T) {
	h, d := newHandler(t)
	d.metaSvc.EXPECT().BuildSourceChain().Return(nil)
	w := doReq(t, h.ValidateDedupEntry, http.MethodPost, "/dedup/validate",
		map[string]any{"query": "x"})
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("no metadata sources")) {
		t.Fatalf("want 200 + no-sources msg, got %d: %s", w.Code, w.Body.String())
	}
}

// --- DeduplicateSeriesHandler ---

func TestDeduplicateSeriesHandler_Enqueues202(t *testing.T) {
	h, d := newHandler(t)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.series-dedup", mock.Anything).Return("rid", nil)
	w := doReq(t, h.DeduplicateSeriesHandler, http.MethodPost, "/series/deduplicate", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	assertReturnsV2OpID(t, w)
}

// --- SeriesPrunePreview ---

func TestSeriesPrunePreview_OK(t *testing.T) {
	h, d := newHandler(t)
	d.rec.prunePreviewResult = gin.H{"duplicate_groups": 3}
	w := doReq(t, h.SeriesPrunePreview, http.MethodGet, "/series/prune/preview", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if d.rec.prunePreviewCalls != 1 {
		t.Fatalf("expected prune preview closure called once, got %d", d.rec.prunePreviewCalls)
	}
}

// --- SeriesPrune (apply) ---

func TestSeriesPrune_Enqueues202(t *testing.T) {
	h, d := newHandler(t)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.series-prune", mock.Anything).Return("rid", nil)
	w := doReq(t, h.SeriesPrune, http.MethodPost, "/series/prune", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	assertReturnsV2OpID(t, w)
}

// --- MergeSeriesGroup ---

func TestMergeSeriesGroup_Enqueues202(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetSeriesByID(7).Return(&database.Series{ID: 7}, nil)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.series-merge", mock.Anything).Return("rid", nil)
	w := doReq(t, h.MergeSeriesGroup, http.MethodPost, "/series/merge",
		map[string]any{"keep_id": 7, "merge_ids": []int{8, 9}})
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	assertReturnsV2OpID(t, w)
}

// honor_locks is optional on the wire and forwarded as given: absent stays nil
// (the op reads nil as true), an explicit false reaches the op.
func TestMergeSeriesGroup_ForwardsHonorLocks(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
		want *bool
	}{
		{"absent", map[string]any{"keep_id": 7, "merge_ids": []int{8}}, nil},
		{"false", map[string]any{"keep_id": 7, "merge_ids": []int{8}, "honor_locks": false}, new(false)},
		{"true", map[string]any{"keep_id": 7, "merge_ids": []int{8}, "honor_locks": true}, new(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, d := newHandler(t)
			d.store.EXPECT().GetSeriesByID(7).Return(&database.Series{ID: 7}, nil)
			var got dedup.SeriesMergeParams
			d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.series-merge", mock.Anything).
				RunAndReturn(func(_ context.Context, _ string, params any, _ ...opsregistry.EnqueueOption) (string, error) {
					got = params.(dedup.SeriesMergeParams)
					return "rid", nil
				})
			w := doReq(t, h.MergeSeriesGroup, http.MethodPost, "/series/merge", tc.body)
			if w.Code != http.StatusAccepted {
				t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
			}
			switch {
			case tc.want == nil && got.HonorLocks != nil:
				t.Fatalf("absent honor_locks must stay nil, got %v", *got.HonorLocks)
			case tc.want != nil && (got.HonorLocks == nil || *got.HonorLocks != *tc.want):
				t.Fatalf("honor_locks not forwarded: want %v got %v", *tc.want, got.HonorLocks)
			}
		})
	}
}

func TestMergeSeriesGroup_KeepNotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetSeriesByID(7).Return(nil, nil)
	w := doReq(t, h.MergeSeriesGroup, http.MethodPost, "/series/merge",
		map[string]any{"keep_id": 7, "merge_ids": []int{8}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}

// errString is a tiny error helper so tests can return sentinel errors.
type errString string

func (e errString) Error() string { return string(e) }

var _ = context.Background
