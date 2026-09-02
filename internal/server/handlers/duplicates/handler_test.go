// file: internal/server/handlers/duplicates/handler_test.go
// version: 1.4.0
// guid: 62637af9-347f-4f38-b42b-d90ff3ab3654
// last-edited: 2026-09-02

// Tests for the duplicates-domain handlers. The store / merge-service /
// audiobook-service / metadata-fetch-service / operations-registry deps are
// generated mocks; the injected helper funcs (getMergeService,
// dismissDedupGroup, computeSeriesPrunePreview, seriesNormalizePreview) are stub
// closures that record their invocations or return canned payloads. There is at
// least one test per public method (17 methods).

package duplicates_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	metadatamocks "github.com/falkcorp/audiobook-organizer/internal/metadata/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates"
	duplicatesmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates/mocks"
)

func init() { gin.SetMode(gin.TestMode) }

// recorders captures the side effects of the injected helper closures so tests
// can assert they fired.
type recorders struct {
	dismissedKeys      []string
	prunePreviewCalls  int
	prunePreviewResult any
	prunePreviewErr    error
	normalizeCalls     int
	normalizeResult    any
}

type testDeps struct {
	store    *duplicatesmocks.MockDuplicatesStore
	reg      *duplicatesmocks.MockOperationsRegistry
	merge    *duplicatesmocks.MockMergeService
	audSvc   *duplicatesmocks.MockAudiobookService
	metaSvc  *duplicatesmocks.MockMetadataFetchService
	dedupEng *duplicatesmocks.MockDedupEngine
	cache    *cache.Cache[gin.H]
	rec      *recorders
}

type cfg struct {
	hasReg, hasMerge, hasAud, hasMeta, hasDedupEng bool
}

func noReg(c *cfg) { c.hasReg = false }

// newHandler builds a Handler with fresh mocks. The dedupCache is a real
// in-memory cache.Cache[gin.H]. Any dep can be left out via the no* options to
// exercise the in-method nil guards.
func newHandler(t *testing.T, opts ...func(*cfg)) (*duplicates.Handler, testDeps) {
	t.Helper()
	cf := &cfg{hasReg: true, hasMerge: true, hasAud: true, hasMeta: true, hasDedupEng: true}
	for _, o := range opts {
		o(cf)
	}

	store := duplicatesmocks.NewMockDuplicatesStore(t)
	reg := duplicatesmocks.NewMockOperationsRegistry(t)
	mergeMock := duplicatesmocks.NewMockMergeService(t)
	audMock := duplicatesmocks.NewMockAudiobookService(t)
	metaMock := duplicatesmocks.NewMockMetadataFetchService(t)
	dedupEngMock := duplicatesmocks.NewMockDedupEngine(t)
	dc := cache.New[gin.H]("dedup-test", time.Minute)
	rec := &recorders{}

	var regArg duplicates.OperationsRegistry
	if cf.hasReg {
		regArg = reg
	}
	var audArg duplicates.AudiobookService
	if cf.hasAud {
		audArg = audMock
	}
	var metaArg duplicates.MetadataFetchService
	if cf.hasMeta {
		metaArg = metaMock
	}
	var dedupEngArg duplicates.DedupEngine
	if cf.hasDedupEng {
		dedupEngArg = dedupEngMock
	}

	h := duplicates.New(
		store,
		dc,
		regArg,
		audArg,
		metaArg,
		func() duplicates.MergeService {
			if cf.hasMerge {
				return mergeMock
			}
			return nil
		},
		dedupEngArg,
		func(groupKey string) { rec.dismissedKeys = append(rec.dismissedKeys, groupKey) },
		func() (any, error) {
			rec.prunePreviewCalls++
			return rec.prunePreviewResult, rec.prunePreviewErr
		},
		func() any {
			rec.normalizeCalls++
			return rec.normalizeResult
		},
	)
	return h, testDeps{store, reg, mergeMock, audMock, metaMock, dedupEngMock, dc, rec}
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

// --- ListDuplicateAudiobooks ---

func TestListDuplicateAudiobooks_CacheMiss(t *testing.T) {
	h, d := newHandler(t)
	d.audSvc.EXPECT().GetDuplicateBooks(mock.Anything).Return(&audiobookspkg.DuplicatesResult{
		Groups:         [][]database.Book{{{ID: "a"}, {ID: "b"}}},
		GroupCount:     1,
		DuplicateCount: 2,
	}, nil)
	w := doReq(t, h.ListDuplicateAudiobooks, http.MethodGet, "/audiobooks/duplicates", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	// second call should hit the cache (no further mock calls expected)
	w2 := doReq(t, h.ListDuplicateAudiobooks, http.MethodGet, "/audiobooks/duplicates", nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("cached call want 200, got %d", w2.Code)
	}
}

// --- ListBookDuplicateScanResults ---

func TestListBookDuplicateScanResults_EmptyNeedsRefresh(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.ListBookDuplicateScanResults, http.MethodGet, "/audiobooks/duplicates/scan-results", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("needs_refresh")) {
		t.Fatalf("expected needs_refresh in body, got %s", w.Body.String())
	}
}

// --- ScanBookDuplicates ---

func TestScanBookDuplicates_Enqueues202(t *testing.T) {
	h, d := newHandler(t)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.book-scan", mock.Anything).Return("rid", nil)
	w := doReq(t, h.ScanBookDuplicates, http.MethodPost, "/audiobooks/duplicates/scan", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	assertReturnsV2OpID(t, w)
}

func TestScanBookDuplicates_NoRegistry(t *testing.T) {
	h, _ := newHandler(t, noReg)
	w := doReq(t, h.ScanBookDuplicates, http.MethodPost, "/audiobooks/duplicates/scan", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
}

// --- MergeBookDuplicatesAsVersions ---

func TestMergeBookDuplicatesAsVersions_OK(t *testing.T) {
	h, d := newHandler(t)
	d.merge.EXPECT().MergeBooks([]string{"a", "b"}, "").Return(&merge.Result{
		PrimaryID: "a", VersionGroupID: "vg1", MergedCount: 2,
	}, nil)
	d.dedupEng.EXPECT().CleanupCandidatesAfterMerge([]string{"b"}).Return(1)
	w := doReq(t, h.MergeBookDuplicatesAsVersions, http.MethodPost, "/audiobooks/duplicates/merge",
		map[string]any{"book_ids": []string{"a", "b"}})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMergeBookDuplicatesAsVersions_TooFew(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.MergeBookDuplicatesAsVersions, http.MethodPost, "/audiobooks/duplicates/merge",
		map[string]any{"book_ids": []string{"a"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestMergeBookDuplicatesAsVersions_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.merge.EXPECT().MergeBooks(mock.Anything, "").Return(nil, &merge.BookNotFoundError{BookID: "b"})
	w := doReq(t, h.MergeBookDuplicatesAsVersions, http.MethodPost, "/audiobooks/duplicates/merge",
		map[string]any{"book_ids": []string{"a", "b"}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "b") {
		t.Fatalf("404 must name the missing book, got %s", w.Body.String())
	}
}

// A "not found" that is only a substring of some other error's text is NOT a
// 404: the handler maps the typed error, not the wording. Before 2026-09-02 a
// store failure mentioning a not-found index would have been reported as a
// missing book.
func TestMergeBookDuplicatesAsVersions_UntypedNotFoundTextIs500(t *testing.T) {
	h, d := newHandler(t)
	d.merge.EXPECT().MergeBooks(mock.Anything, "").Return(nil, errString("index shard not found while loading"))
	w := doReq(t, h.MergeBookDuplicatesAsVersions, http.MethodPost, "/audiobooks/duplicates/merge",
		map[string]any{"book_ids": []string{"a", "b"}})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", w.Code, w.Body.String())
	}
}

// The service refusing the merge because a participant is soft-deleted is the
// caller's problem to see, not a server fault: 409 with the service's reason.
func TestMergeBookDuplicatesAsVersions_SoftDeletedInputIs409(t *testing.T) {
	h, d := newHandler(t)
	d.merge.EXPECT().MergeBooks(mock.Anything, "").Return(nil, &merge.SoftDeletedInputError{BookID: "b", AsPrimary: true})
	w := doReq(t, h.MergeBookDuplicatesAsVersions, http.MethodPost, "/audiobooks/duplicates/merge",
		map[string]any{"book_ids": []string{"a", "b"}})
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "b") {
		t.Fatalf("409 must carry the service's reason naming the book, got %s", w.Body.String())
	}
}

// --- CombineBooks ---

func TestCombineBooks_OK(t *testing.T) {
	h, d := newHandler(t)
	d.merge.EXPECT().CombineBooks([]string{"b", "c", "a"}, "a", (*merge.CombineOverride)(nil)).Return(&merge.CombineResult{
		PrimaryID: "a", FilesMoved: 3, BooksDeleted: 2,
	}, nil)
	d.dedupEng.EXPECT().CleanupCandidatesAfterMerge([]string{"b", "c"}).Return(2)
	w := doReq(t, h.CombineBooks, http.MethodPost, "/audiobooks/combine",
		map[string]any{"keep_id": "a", "merge_ids": []string{"b", "c"}})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCombineBooks_TooFew(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.CombineBooks, http.MethodPost, "/audiobooks/combine",
		map[string]any{"keep_id": "a", "merge_ids": []string{}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestCombineBooks_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.merge.EXPECT().CombineBooks(mock.Anything, "a", mock.Anything).Return(nil, &merge.BookNotFoundError{BookID: "a"})
	w := doReq(t, h.CombineBooks, http.MethodPost, "/audiobooks/combine",
		map[string]any{"keep_id": "a", "merge_ids": []string{"b"}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}

// The user picked a keep_id with no audio route while a merged book has one.
// The service refuses; the user must see WHY their pick lost, so this is a
// 409 carrying the service's message, not a generic 500.
func TestCombineBooks_FilelessPrimaryIs409WithReason(t *testing.T) {
	h, d := newHandler(t)
	d.merge.EXPECT().CombineBooks(mock.Anything, "a", mock.Anything).
		Return(nil, &merge.FilelessPrimaryError{PrimaryID: "a", FileBearing: []string{"b"}})
	w := doReq(t, h.CombineBooks, http.MethodPost, "/audiobooks/combine",
		map[string]any{"keep_id": "a", "merge_ids": []string{"b"}})
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "refusing to keep the file-less book") {
		t.Fatalf("409 must carry the service's reason, got %s", w.Body.String())
	}
}

// --- DismissBookDuplicateGroup ---

func TestDismissBookDuplicateGroup_OK(t *testing.T) {
	h, d := newHandler(t)
	w := doReq(t, h.DismissBookDuplicateGroup, http.MethodPost, "/audiobooks/duplicates/dismiss",
		map[string]any{"group_key": "k1"})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(d.rec.dismissedKeys) != 1 || d.rec.dismissedKeys[0] != "k1" {
		t.Fatalf("expected dismiss closure called with k1, got %v", d.rec.dismissedKeys)
	}
}

// --- MergeBooks ---

func TestMergeBooks_Enqueues202(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("keep").Return(&database.Book{ID: "keep"}, nil)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.book-merge", mock.Anything).Return("rid", nil)
	w := doReq(t, h.MergeBooks, http.MethodPost, "/audiobooks/merge",
		map[string]any{"keep_id": "keep", "merge_ids": []string{"m1"}})
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	assertReturnsV2OpID(t, w)
}

func TestMergeBooks_KeepNotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("keep").Return(nil, nil)
	w := doReq(t, h.MergeBooks, http.MethodPost, "/audiobooks/merge",
		map[string]any{"keep_id": "keep", "merge_ids": []string{"m1"}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
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

func TestMergeSeriesGroup_KeepNotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetSeriesByID(7).Return(nil, nil)
	w := doReq(t, h.MergeSeriesGroup, http.MethodPost, "/series/merge",
		map[string]any{"keep_id": 7, "merge_ids": []int{8}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}

// --- SeriesNormalizePreview ---

func TestSeriesNormalizePreview_OK(t *testing.T) {
	h, d := newHandler(t)
	d.rec.normalizeResult = gin.H{"total_series_affected": 2}
	w := doReq(t, h.SeriesNormalizePreview, http.MethodGet, "/series/normalize/preview", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if d.rec.normalizeCalls != 1 {
		t.Fatalf("expected normalize preview closure called once, got %d", d.rec.normalizeCalls)
	}
}

// --- SeriesNormalize (apply) ---

func TestSeriesNormalize_Enqueues202(t *testing.T) {
	h, d := newHandler(t)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.series-normalize", mock.Anything).Return("rid", nil)
	w := doReq(t, h.SeriesNormalize, http.MethodPost, "/series/normalize", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	assertReturnsV2OpID(t, w)
}

// errString is a tiny error helper so tests can return sentinel errors.
type errString string

func (e errString) Error() string { return string(e) }

var _ = context.Background
