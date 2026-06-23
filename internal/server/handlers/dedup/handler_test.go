// file: internal/server/handlers/dedup/handler_test.go
// version: 1.4.0
// guid: 6d8011eb-bed6-430b-959e-2a2b0738ffbc
// last-edited: 2026-06-23

// Tests for the dedup-domain handlers. The embedding store is exercised through
// a REAL pebble-backed *database.EmbeddingStore (it is a concrete db type the
// handler holds by pointer, not an interface — it cannot be mocked). The
// surrounding deps (DedupStore, MergeService, DedupEngine, OperationsRegistry)
// are generated mocks; publishEvent / markDuplicatesFlaggedDirty are stub funcs
// that record their invocations. There is at least one test per public method.

package deduphandler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	unifiedpkg "github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	deduphandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/dedup"
	dedupmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/dedup/mocks"
)

func init() { gin.SetMode(gin.TestMode) }

// testDeps bundles the mocks + the real embedding store + the injected-func
// recorders so each test can wire exactly what it needs.
type testDeps struct {
	es           *database.EmbeddingStore
	store        *dedupmocks.MockDedupStore
	merge        *dedupmocks.MockMergeService
	engine       *dedupmocks.MockDedupEngine
	reg          *dedupmocks.MockOperationsRegistry
	publishedN   *int
	dirtyReasons *[]string
}

// newHandler builds a Handler backed by a real pebble EmbeddingStore and fresh
// mocks. Any of opReg/merge/engine can be left out of expectations; the typed
// nils are only created when requested via the with* args so the in-method nil
// guards can be exercised.
func newHandler(t *testing.T, opts ...func(*handlerCfg)) (*deduphandler.Handler, testDeps) {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	es := database.NewEmbeddingStore(db)

	cfg := &handlerCfg{hasReg: true, hasMerge: true, hasEngine: true, hasEmbed: true}
	for _, o := range opts {
		o(cfg)
	}

	store := dedupmocks.NewMockDedupStore(t)
	mergeMock := dedupmocks.NewMockMergeService(t)
	engineMock := dedupmocks.NewMockDedupEngine(t)
	regMock := dedupmocks.NewMockOperationsRegistry(t)

	publishedN := 0
	dirtyReasons := []string{}

	var embedArg *database.EmbeddingStore
	if cfg.hasEmbed {
		embedArg = es
	}

	var regArg deduphandler.OperationsRegistry
	if cfg.hasReg {
		regArg = regMock
	}
	var mergeArg deduphandler.MergeService
	if cfg.hasMerge {
		mergeArg = mergeMock
	}
	var engineArg deduphandler.DedupEngine
	if cfg.hasEngine {
		engineArg = engineMock
	}

	h := deduphandler.New(
		store,
		embedArg,
		regArg,
		mergeArg,
		engineArg,
		func(ctx context.Context, event plugin.Event) { publishedN++ },
		func(reason string) { dirtyReasons = append(dirtyReasons, reason) },
	)
	return h, testDeps{es, store, mergeMock, engineMock, regMock, &publishedN, &dirtyReasons}
}

type handlerCfg struct {
	hasReg, hasMerge, hasEngine, hasEmbed bool
}

func noReg(c *handlerCfg)    { c.hasReg = false }
func noMerge(c *handlerCfg)  { c.hasMerge = false }
func noEngine(c *handlerCfg) { c.hasEngine = false }
func noEmbed(c *handlerCfg)  { c.hasEmbed = false }

// doReq drives a single handler with the supplied method/url/body/params.
func doReq(t *testing.T, fn gin.HandlerFunc, method, url string, body any, params gin.Params) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.Request = req
	c.Params = params
	fn(c)
	return w
}

// insertCandidate creates a pending book dedup candidate; returns its id + the
// canonicalised A/B ids (UpsertCandidate swaps so smaller ID is A).
func insertCandidate(t *testing.T, es *database.EmbeddingStore, aID, bID string) (int64, string, string) {
	t.Helper()
	sim := 0.95
	if err := es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book",
		EntityAID:  aID,
		EntityBID:  bID,
		Layer:      "embedding",
		Similarity: &sim,
		Status:     "pending",
	}); err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
	cA, cB := aID, bID
	if cA > cB {
		cA, cB = cB, cA
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 100})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	for _, c := range cands {
		if c.EntityAID == cA && c.EntityBID == cB {
			return c.ID, cA, cB
		}
	}
	t.Fatalf("inserted candidate not found")
	return 0, "", ""
}

// ───────────────────────── listing / stats / export ─────────────────────────

func TestListDedupCandidates(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "book-a", "book-b")
	// Both books exist → candidate survives the dead-book filter.
	d.store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{ID: "x"}, nil).Maybe()

	w := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestListDedupCandidates_IncludeBooks(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "book-a", "book-b")
	d.store.EXPECT().GetBookByID(mock.Anything).
		Return(&database.Book{ID: "x", Title: "Enriched Title"}, nil).Maybe()

	decode := func(w *httptest.ResponseRecorder) []map[string]any {
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

	// Without include_books: rows carry no book_a/book_b.
	wOff := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates", nil, nil)
	if wOff.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", wOff.Code, wOff.Body.String())
	}
	off := decode(wOff)
	if len(off) == 0 {
		t.Fatalf("expected at least one candidate row")
	}
	if _, ok := off[0]["book_a"]; ok {
		t.Fatalf("book_a should be absent without include_books; got %v", off[0])
	}

	// With include_books=true: rows carry enriched book objects.
	wOn := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates?include_books=true", nil, nil)
	if wOn.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", wOn.Code, wOn.Body.String())
	}
	on := decode(wOn)
	if len(on) == 0 {
		t.Fatalf("expected at least one candidate row")
	}
	bookA, ok := on[0]["book_a"].(map[string]any)
	if !ok {
		t.Fatalf("book_a should be present with include_books; got %v", on[0])
	}
	if bookA["title"] != "Enriched Title" {
		t.Fatalf("book_a.title=%v want %q", bookA["title"], "Enriched Title")
	}
	if _, ok := on[0]["book_b"].(map[string]any); !ok {
		t.Fatalf("book_b should be present with include_books; got %v", on[0])
	}
}

func TestListDedupCandidates_BothUnmatched(t *testing.T) {
	h, d := newHandler(t)
	matched := "matched"
	asin := "B00ABC1234"
	insertCandidate(t, d.es, "u1", "u2")    // both unmatched → should appear
	insertCandidate(t, d.es, "u3", "m1")    // m1 matched (review status) → filtered out
	insertCandidate(t, d.es, "u4", "asin1") // asin1 matched (external ID) → filtered out

	d.store.EXPECT().GetBookByID("u1").Return(&database.Book{ID: "u1"}, nil).Maybe()
	d.store.EXPECT().GetBookByID("u2").Return(&database.Book{ID: "u2"}, nil).Maybe()
	d.store.EXPECT().GetBookByID("u3").Return(&database.Book{ID: "u3"}, nil).Maybe()
	d.store.EXPECT().GetBookByID("u4").Return(&database.Book{ID: "u4"}, nil).Maybe()
	d.store.EXPECT().GetBookByID("m1").
		Return(&database.Book{ID: "m1", MetadataReviewStatus: &matched}, nil).Maybe()
	d.store.EXPECT().GetBookByID("asin1").
		Return(&database.Book{ID: "asin1", ASIN: &asin}, nil).Maybe()

	w := doReq(t, h.ListDedupCandidates, http.MethodGet,
		"/api/v1/dedup/candidates?both_unmatched=true&include_books=true", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Candidates []map[string]any `json:"candidates"`
			Total      int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Data.Total != 1 || len(resp.Data.Candidates) != 1 {
		t.Fatalf("want exactly 1 both-unmatched pair; got total=%d len=%d body=%s",
			resp.Data.Total, len(resp.Data.Candidates), w.Body.String())
	}
	row := resp.Data.Candidates[0]
	a, _ := row["entity_a_id"].(string)
	b, _ := row["entity_b_id"].(string)
	if a == "m1" || b == "m1" || a == "asin1" || b == "asin1" {
		t.Fatalf("matched pair leaked through the both_unmatched filter: %v", row)
	}
}

func TestListDedupCandidates_NoEmbedStore(t *testing.T) {
	h, _ := newHandler(t, noEmbed)
	w := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates", nil, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", w.Code, w.Body.String())
	}
}

func TestExportDedupCandidates_CSV(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "book-a", "book-b")
	d.store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{ID: "x", Title: "T"}, nil).Maybe()
	w := doReq(t, h.ExportDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates/export?format=csv", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv" {
		t.Fatalf("content-type=%q want text/csv", ct)
	}
}

func TestExportDedupCandidates_BadFormat(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.ExportDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates/export?format=xml", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestGetDedupStats(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.GetDedupStats, http.MethodGet, "/api/v1/dedup/stats", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestListDedupCandidateSeries(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "book-a", "book-b")
	sid := 7
	d.store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{ID: "x", SeriesID: &sid}, nil).Maybe()
	d.store.EXPECT().GetSeriesByID(mock.Anything).Return(&database.Series{ID: sid, Name: "S"}, nil).Maybe()
	w := doReq(t, h.ListDedupCandidateSeries, http.MethodGet, "/api/v1/dedup/candidates/series-summary", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestMergeDedupCandidateSeries(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "book-a", "book-b")
	sid := 7
	d.store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{ID: "x", SeriesID: &sid}, nil).Maybe()
	d.merge.EXPECT().MergeBooks(mock.Anything, mock.Anything).Return(&merge.Result{PrimaryID: "book-a"}, nil).Maybe()
	w := doReq(t, h.MergeDedupCandidateSeries, http.MethodPost, "/api/v1/dedup/candidates/merge-series", map[string]int{"series_id": sid}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestMergeDedupCandidateSeries_BadSeriesID(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.MergeDedupCandidateSeries, http.MethodPost, "/api/v1/dedup/candidates/merge-series", map[string]int{"series_id": 0}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestMergeDedupCandidateSeries_NoMergeSvc(t *testing.T) {
	h, _ := newHandler(t, noMerge)
	w := doReq(t, h.MergeDedupCandidateSeries, http.MethodPost, "/api/v1/dedup/candidates/merge-series", map[string]int{"series_id": 1}, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", w.Code, w.Body.String())
	}
}

// ───────────────────────── bulk / cluster merges ─────────────────────────

func TestBulkMergeDedupCandidates(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	insertCandidate(t, d.es, "book-a", "book-b")
	d.merge.EXPECT().MergeBooks(mock.Anything, mock.Anything).Return(&merge.Result{PrimaryID: "book-a"}, nil).Once()
	w := doReq(t, h.BulkMergeDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-merge", map[string]any{}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestBulkMergeDedupCandidates_NonBookRejected(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.BulkMergeDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-merge", map[string]string{"entity_type": "author"}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestBulkMergeDedupCandidates_NoMergeSvc(t *testing.T) {
	h, _ := newHandler(t, noMerge)
	w := doReq(t, h.BulkMergeDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-merge", map[string]any{}, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", w.Code, w.Body.String())
	}
}

func TestMergeDedupCluster(t *testing.T) {
	h, d := newHandler(t)
	d.merge.EXPECT().MergeBooks(mock.Anything, mock.Anything).Return(&merge.Result{PrimaryID: "id1"}, nil).Once()
	w := doReq(t, h.MergeDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/merge-cluster",
		map[string][]string{"book_ids": {"id1", "id2"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestMergeDedupCluster_TooFew(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.MergeDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/merge-cluster",
		map[string][]string{"book_ids": {"id1"}}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestDismissDedupCluster(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	insertCandidate(t, d.es, "id1", "id2")
	w := doReq(t, h.DismissDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/dismiss-cluster",
		map[string][]string{"book_ids": {"id1", "id2"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	if len(*d.dirtyReasons) != 1 || (*d.dirtyReasons)[0] != "dismiss_cluster" {
		t.Fatalf("markDuplicatesFlaggedDirty not called with dismiss_cluster: %v", *d.dirtyReasons)
	}
}

func TestRemoveFromDedupCluster(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "id1", "id2")
	w := doReq(t, h.RemoveFromDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/remove-from-cluster",
		map[string]any{"cluster_book_ids": []string{"id1", "id2"}, "remove_book_id": "id1"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestRemoveFromDedupCluster_NoRemoveSet(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.RemoveFromDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/remove-from-cluster",
		map[string]any{"cluster_book_ids": []string{"id1", "id2"}}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", w.Code, w.Body.String())
	}
}

// ───────────────────────── single candidate merge / dismiss ──────────────────

func TestMergeDedupCandidate_NotFound(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.MergeDedupCandidate, http.MethodPost, "/api/v1/dedup/candidates/999/merge", nil,
		gin.Params{{Key: "id", Value: "999"}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestMergeDedupCandidate_KeepIDInvalid(t *testing.T) {
	h, d := newHandler(t)
	id, _, _ := insertCandidate(t, d.es, "book-aaa", "book-bbb")
	w := doReq(t, h.MergeDedupCandidate, http.MethodPost,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/merge",
		map[string]string{"keep_id": "not-in-pair"},
		gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestMergeDedupCandidate_MergeSuccess(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	id, aID, bID := insertCandidate(t, d.es, "book-aaa", "book-bbb")
	d.merge.EXPECT().MergeBooks([]string{aID, bID}, "").Return(&merge.Result{PrimaryID: aID}, nil).Once()
	d.engine.EXPECT().CleanupCandidatesAfterMerge(mock.Anything).Return(0).Once()
	w := doReq(t, h.MergeDedupCandidate, http.MethodPost,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/merge", nil,
		gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	if *d.publishedN == 0 {
		t.Fatalf("expected publishEvent to be called on merge")
	}
}

// TestMergeDedupCandidate_KeepIDEcho asserts keep_id=A and keep_id=B are each
// accepted and echoed back in the response (parity with the old server-package
// keep_id test). mergeService is wired so the book path runs through MergeBooks.
func TestMergeDedupCandidate_KeepIDEcho(t *testing.T) {
	for _, side := range []string{"A", "B"} {
		t.Run(side, func(t *testing.T) {
			h, d := newHandler(t)
			allowLabelCaptureReads(d)
			id, aID, bID := insertCandidate(t, d.es, "book-aaa", "book-bbb")
			keep := aID
			if side == "B" {
				keep = bID
			}
			d.merge.EXPECT().MergeBooks([]string{aID, bID}, keep).Return(&merge.Result{PrimaryID: keep}, nil).Once()
			d.engine.EXPECT().CleanupCandidatesAfterMerge(mock.Anything).Return(0).Once()
			w := doReq(t, h.MergeDedupCandidate, http.MethodPost,
				"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/merge",
				map[string]string{"keep_id": keep},
				gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
			}
			var resp struct {
				Data struct {
					KeepID string `json:"keep_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v; body=%s", err, w.Body.String())
			}
			if resp.Data.KeepID != keep {
				t.Errorf("keep_id echoed=%q want %q", resp.Data.KeepID, keep)
			}
		})
	}
}

func TestMergeDedupCandidate_AlreadyMergedConflict(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	id, aID, bID := insertCandidate(t, d.es, "book-aaa", "book-bbb")
	d.merge.EXPECT().MergeBooks([]string{aID, bID}, "").
		Return(nil, errNotFound{}).Once()
	w := doReq(t, h.MergeDedupCandidate, http.MethodPost,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/merge", nil,
		gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409; body=%s", w.Code, w.Body.String())
	}
}

type errNotFound struct{}

func (errNotFound) Error() string { return "book book-aaa not found" }

func TestDismissDedupCandidate(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	id, _, _ := insertCandidate(t, d.es, "book-aaa", "book-bbb")
	w := doReq(t, h.DismissDedupCandidate, http.MethodPost,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/dismiss", nil,
		gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	if len(*d.dirtyReasons) != 1 || (*d.dirtyReasons)[0] != "dismiss_candidate" {
		t.Fatalf("dirty reasons=%v want [dismiss_candidate]", *d.dirtyReasons)
	}
}

func TestDismissDedupCandidate_BadID(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.DismissDedupCandidate, http.MethodPost, "/api/v1/dedup/candidates/abc/dismiss", nil,
		gin.Params{{Key: "id", Value: "abc"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", w.Code, w.Body.String())
	}
}

// ───────────────────────── scan triggers (enqueue 202) ──────────────────────

func expectEnqueue(d testDeps) {
	d.reg.EXPECT().EnqueueOp(mock.Anything, mock.Anything, mock.Anything).Return("op-123", nil).Maybe()
}

func TestTriggerDedupScan(t *testing.T) {
	h, d := newHandler(t)
	expectEnqueue(d)
	w := doReq(t, h.TriggerDedupScan, http.MethodPost, "/api/v1/dedup/scan", nil, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestTriggerDedupScan_NoRegistry(t *testing.T) {
	h, _ := newHandler(t, noReg)
	w := doReq(t, h.TriggerDedupScan, http.MethodPost, "/api/v1/dedup/scan", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500; body=%s", w.Code, w.Body.String())
	}
}

func TestTriggerDedupLLM(t *testing.T) {
	h, d := newHandler(t)
	expectEnqueue(d)
	w := doReq(t, h.TriggerDedupLLM, http.MethodPost, "/api/v1/dedup/scan-llm", nil, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestTriggerDedupRefresh(t *testing.T) {
	h, d := newHandler(t)
	expectEnqueue(d)
	w := doReq(t, h.TriggerDedupRefresh, http.MethodPost, "/api/v1/dedup/refresh", nil, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestTriggerDedupAcoustID(t *testing.T) {
	h, d := newHandler(t)
	expectEnqueue(d)
	w := doReq(t, h.TriggerDedupAcoustID, http.MethodPost, "/api/v1/dedup/scan-acoustid", nil, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestResetAcoustIDFingerprints(t *testing.T) {
	h, d := newHandler(t)
	// Two enqueues: reset-all then fingerprint-rescan.
	d.reg.EXPECT().EnqueueOp(mock.Anything, "acoustid.reset-all", mock.Anything).Return("reset-1", nil).Once()
	d.reg.EXPECT().EnqueueOp(mock.Anything, "acoustid.fingerprint-rescan", mock.Anything).Return("rescan-1", nil).Once()
	w := doReq(t, h.ResetAcoustIDFingerprints, http.MethodPost, "/api/v1/dedup/reset-acoustid", nil, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestPurgeStaleCandidates(t *testing.T) {
	h, d := newHandler(t)
	expectEnqueue(d)
	w := doReq(t, h.PurgeStaleCandidates, http.MethodPost, "/api/v1/dedup/purge-stale", nil, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestTriggerEmbedScan(t *testing.T) {
	h, d := newHandler(t)
	expectEnqueue(d)
	w := doReq(t, h.TriggerEmbedScan, http.MethodPost, "/api/v1/dedup/embed", nil, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestTriggerEmbedAsync(t *testing.T) {
	h, d := newHandler(t)
	expectEnqueue(d)
	w := doReq(t, h.TriggerEmbedAsync, http.MethodPost, "/api/v1/dedup/embed-async", nil, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestTriggerBookSignatureScan(t *testing.T) {
	h, d := newHandler(t)
	expectEnqueue(d)
	w := doReq(t, h.TriggerBookSignatureScan, http.MethodPost, "/api/v1/dedup/scan-book-signature", nil, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

// ───────────────────────── compare-acoustid ─────────────────────────

func TestHandleCompareAcoustID(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("a").Return(&database.Book{ID: "a"}, nil).Once()
	d.store.EXPECT().GetBookByID("b").Return(&database.Book{ID: "b"}, nil).Once()
	d.store.EXPECT().GetBookFiles("a").Return([]database.BookFile{{AcoustIDSeg0: "h"}}, nil).Once()
	d.store.EXPECT().GetBookFiles("b").Return([]database.BookFile{{AcoustIDSeg0: "h"}}, nil).Once()
	w := doReq(t, h.HandleCompareAcoustID, http.MethodPost, "/api/v1/audiobooks/a/compare-acoustid?other=b", nil,
		gin.Params{{Key: "id", Value: "a"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleCompareAcoustID_MissingOther(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.HandleCompareAcoustID, http.MethodPost, "/api/v1/audiobooks/a/compare-acoustid", nil,
		gin.Params{{Key: "id", Value: "a"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", w.Code, w.Body.String())
	}
}

// ──────────────── T015: purge-legacy-fp endpoint ─────────────────────────────

// TestPurgeLegacyFPCandidates verifies the happy path: a POST with apply=true
// body enqueues the correct op and returns 202 Accepted with an op_id.
func TestPurgeLegacyFPCandidates(t *testing.T) {
	h, d := newHandler(t)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.purge-legacy-fp-candidates", mock.Anything).Return("op-fp-1", nil).Once()
	w := doReq(t, h.PurgeLegacyFPCandidates, http.MethodPost, "/api/v1/dedup/purge-legacy-fp",
		map[string]bool{"apply": true}, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

// TestPurgeLegacyFPCandidates_DryRun verifies that a request with no body
// (dry-run mode) also enqueues successfully and returns 202.
func TestPurgeLegacyFPCandidates_DryRun(t *testing.T) {
	h, d := newHandler(t)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "dedup.purge-legacy-fp-candidates", mock.Anything).Return("op-fp-dry", nil).Once()
	w := doReq(t, h.PurgeLegacyFPCandidates, http.MethodPost, "/api/v1/dedup/purge-legacy-fp", nil, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", w.Code, w.Body.String())
	}
}

// TestPurgeLegacyFPCandidates_NoRegistry verifies that a missing op registry
// returns 500.
func TestPurgeLegacyFPCandidates_NoRegistry(t *testing.T) {
	h, _ := newHandler(t, noReg)
	w := doReq(t, h.PurgeLegacyFPCandidates, http.MethodPost, "/api/v1/dedup/purge-legacy-fp", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500; body=%s", w.Code, w.Body.String())
	}
}

// ──────────────── T016: band filter, breakdown, rescore ─────────────────────

// insertCandidateWithBand creates a pending book candidate with the given band
// set directly in the store via UpsertCandidate. Uses the same canonicalisation
// logic as insertCandidate.
func insertCandidateWithBand(t *testing.T, es *database.EmbeddingStore, aID, bID, band string) (int64, string, string) {
	t.Helper()
	sim := 0.95
	score := 95.0
	if err := es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book",
		EntityAID:  aID,
		EntityBID:  bID,
		Layer:      "embedding",
		Similarity: &sim,
		Status:     "pending",
		Band:       band,
		ScoreBreakdown: &unifiedpkg.UnifiedDedupScore{
			Score:   score,
			Band:    band,
			Pair:    [2]string{aID, bID},
			Formula: unifiedpkg.FormulaVersion,
			Signals: []unifiedpkg.Signal{
				{Kind: unifiedpkg.SigEmbedHigh, Raw: 0.95, Confidence: 0.90, Evidence: "test"},
			},
		},
		FormulaVersion: unifiedpkg.FormulaVersion,
	}); err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
	cA, cB := aID, bID
	if cA > cB {
		cA, cB = cB, cA
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 100})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	for _, c := range cands {
		if c.EntityAID == cA && c.EntityBID == cB {
			return c.ID, cA, cB
		}
	}
	t.Fatalf("inserted candidate not found")
	return 0, "", ""
}

// TestListDedupCandidates_BandFilter verifies that band=CERTAIN filters correctly:
// a CERTAIN and a HIGH candidate are inserted; only the CERTAIN one is returned.
func TestListDedupCandidates_BandFilter(t *testing.T) {
	h, d := newHandler(t)
	insertCandidateWithBand(t, d.es, "book-c1", "book-c2", "CERTAIN")
	insertCandidateWithBand(t, d.es, "book-h1", "book-h2", "HIGH")

	// Both books must "exist" to pass the dead-book filter.
	d.store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{ID: "x"}, nil).Maybe()

	w := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates?band=CERTAIN", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Total      int              `json:"total"`
			Candidates []map[string]any `json:"candidates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Data.Total != 1 {
		t.Errorf("total=%d want 1 (only CERTAIN band)", resp.Data.Total)
	}
	if len(resp.Data.Candidates) != 1 {
		t.Errorf("candidates len=%d want 1", len(resp.Data.Candidates))
		return
	}
	if band, _ := resp.Data.Candidates[0]["band"].(string); band != "CERTAIN" {
		t.Errorf("returned candidate band=%q want CERTAIN", band)
	}
}

// TestListDedupCandidates_BreakdownOmitted verifies that score_breakdown is NOT
// included by default (include_breakdown not set).
func TestListDedupCandidates_BreakdownOmitted(t *testing.T) {
	h, d := newHandler(t)
	insertCandidateWithBand(t, d.es, "book-x1", "book-x2", "HIGH")
	d.store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{ID: "x"}, nil).Maybe()

	w := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Candidates []map[string]any `json:"candidates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if len(resp.Data.Candidates) == 0 {
		t.Fatal("want at least one candidate")
	}
	if _, hasBreakdown := resp.Data.Candidates[0]["score_breakdown"]; hasBreakdown {
		t.Error("score_breakdown should be omitted without include_breakdown=true")
	}
}

// TestListDedupCandidates_BreakdownIncluded verifies that score_breakdown is
// present when include_breakdown=true.
func TestListDedupCandidates_BreakdownIncluded(t *testing.T) {
	h, d := newHandler(t)
	insertCandidateWithBand(t, d.es, "book-y1", "book-y2", "HIGH")
	d.store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{ID: "x"}, nil).Maybe()

	w := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates?include_breakdown=true", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Candidates []map[string]any `json:"candidates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if len(resp.Data.Candidates) == 0 {
		t.Fatal("want at least one candidate")
	}
	if _, hasBreakdown := resp.Data.Candidates[0]["score_breakdown"]; !hasBreakdown {
		t.Error("score_breakdown should be present with include_breakdown=true")
	}
}

// TestGetDedupCandidateBreakdown_OK verifies the breakdown endpoint returns
// candidate + both books.
func TestGetDedupCandidateBreakdown_OK(t *testing.T) {
	h, d := newHandler(t)
	id, aID, bID := insertCandidateWithBand(t, d.es, "book-ba", "book-bb", "HIGH")
	d.store.EXPECT().GetBookByID(aID).Return(&database.Book{ID: aID, Title: "Book A"}, nil).Once()
	d.store.EXPECT().GetBookByID(bID).Return(&database.Book{ID: bID, Title: "Book B"}, nil).Once()
	d.store.EXPECT().GetBookFiles(aID).Return([]database.BookFile{}, nil).Once()
	d.store.EXPECT().GetBookFiles(bID).Return([]database.BookFile{}, nil).Once()

	w := doReq(t, h.GetDedupCandidateBreakdown, http.MethodGet,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/breakdown",
		nil, gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Candidate map[string]any `json:"candidate"`
			BookA     map[string]any `json:"book_a"`
			BookB     map[string]any `json:"book_b"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Data.Candidate == nil {
		t.Error("candidate should be present in breakdown response")
	}
	if resp.Data.BookA == nil || resp.Data.BookB == nil {
		t.Error("book_a and book_b should be present in breakdown response")
	}
}

// TestGetDedupCandidateBreakdown_NotFound verifies 404 for unknown candidate IDs.
func TestGetDedupCandidateBreakdown_NotFound(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.GetDedupCandidateBreakdown, http.MethodGet, "/api/v1/dedup/candidates/99999/breakdown",
		nil, gin.Params{{Key: "id", Value: "99999"}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404; body=%s", w.Code, w.Body.String())
	}
}

// TestGetDedupCandidateBreakdown_NoEmbedStore verifies 503 when the embedding
// store is unavailable.
func TestGetDedupCandidateBreakdown_NoEmbedStore(t *testing.T) {
	h, _ := newHandler(t, noEmbed)
	w := doReq(t, h.GetDedupCandidateBreakdown, http.MethodGet, "/api/v1/dedup/candidates/1/breakdown",
		nil, gin.Params{{Key: "id", Value: "1"}})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", w.Code, w.Body.String())
	}
}

// TestRescoreDedupCandidates_DryRun verifies that a dry-run rescore returns 200
// with the result summary and does not persist any changes.
func TestRescoreDedupCandidates_DryRun(t *testing.T) {
	h, d := newHandler(t)
	expected := dedupengine.RescoreResult{Inspected: 5, Skipped: 1, Changed: 2, Applied: false,
		BandDeltas: map[string]int{"HIGH→CERTAIN": 2}}
	d.engine.EXPECT().Rescore(mock.Anything, false).Return(expected, nil).Once()

	w := doReq(t, h.RescoreDedupCandidates, http.MethodPost, "/api/v1/dedup/rescore", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

// TestRescoreDedupCandidates_Apply verifies that apply=true is forwarded to
// the engine.
func TestRescoreDedupCandidates_Apply(t *testing.T) {
	h, d := newHandler(t)
	expected := dedupengine.RescoreResult{Inspected: 10, Changed: 3, Applied: true,
		BandDeltas: map[string]int{"MEDIUM→HIGH": 3}}
	d.engine.EXPECT().Rescore(mock.Anything, true).Return(expected, nil).Once()

	w := doReq(t, h.RescoreDedupCandidates, http.MethodPost, "/api/v1/dedup/rescore",
		map[string]bool{"apply": true}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

// TestRescoreDedupCandidates_NoEngine verifies 503 when the dedup engine is
// unavailable.
func TestRescoreDedupCandidates_NoEngine(t *testing.T) {
	h, _ := newHandler(t, noEngine)
	w := doReq(t, h.RescoreDedupCandidates, http.MethodPost, "/api/v1/dedup/rescore", nil, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", w.Code, w.Body.String())
	}
}
