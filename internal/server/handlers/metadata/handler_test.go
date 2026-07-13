// file: internal/server/handlers/metadata/handler_test.go
// version: 1.4.1
// guid: 1d31ef73-7c7a-4c3b-a840-01b0865023d7
// last-edited: 2026-07-12

// Tests for the metadata-domain handlers. The store / metadata-fetch-service /
// write-back-enqueuer / operations-registry / file-io-pool deps are generated
// mocks; the injected helper funcs (enrichBook, isProtectedPath,
// loadMetadataState, updateFetchedMetadataState, publishEvent) are stub closures
// that record their invocations or return canned payloads. There is at least one
// test per public method (19 methods) covering happy paths and key branches.

package metadatahandler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
	metadatamocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata/mocks"
)

func init() { gin.SetMode(gin.TestMode) }

// recorders captures the side effects of the injected helper closures so tests
// can assert they fired.
type recorders struct {
	enrichCalls          int
	protectedPaths       map[string]bool
	loadState            map[string]metafetch.MetadataFieldState
	loadStateErr         error
	updatedFetchedValues map[string]any
	updateFetchedErr     error
	publishedEvents      []plugin.Event
}

type testDeps struct {
	store *metadatamocks.MockMetadataStore
	mfs   *metadatamocks.MockMetadataFetchService
	wb    *metadatamocks.MockWriteBackEnqueuer
	reg   *metadatamocks.MockOperationsRegistry
	pool  *metadatamocks.MockFileIOPool
	cache *cache.Cache[gin.H]
	rec   *recorders
}

type cfg struct {
	hasWB, hasReg, hasPool, hasMFS bool
}

func noReg(c *cfg) { c.hasReg = false }

// newHandler builds a Handler with fresh mocks. listCache is a real in-memory
// cache. Any provider/snapshot dep can be left out via the no* options to
// exercise the in-method nil guards.
func newHandler(t *testing.T, opts ...func(*cfg)) (*metadatahandler.Handler, testDeps) {
	t.Helper()
	cf := &cfg{hasWB: true, hasReg: true, hasPool: true, hasMFS: true}
	for _, o := range opts {
		o(cf)
	}

	store := metadatamocks.NewMockMetadataStore(t)
	mfs := metadatamocks.NewMockMetadataFetchService(t)
	wb := metadatamocks.NewMockWriteBackEnqueuer(t)
	reg := metadatamocks.NewMockOperationsRegistry(t)
	pool := metadatamocks.NewMockFileIOPool(t)
	lc := cache.New[gin.H]("meta-test", time.Minute)
	rec := &recorders{protectedPaths: map[string]bool{}, updatedFetchedValues: map[string]any{}}

	var mfsArg metadatahandler.MetadataFetchService
	if cf.hasMFS {
		mfsArg = mfs
	}
	var regArg metadatahandler.OperationsRegistry
	if cf.hasReg {
		regArg = reg
	}
	var poolArg metadatahandler.FileIOPool
	if cf.hasPool {
		poolArg = pool
	}

	h := metadatahandler.New(
		store,
		mfsArg,
		func() metadatahandler.WriteBackEnqueuer {
			if cf.hasWB {
				return wb
			}
			return nil
		},
		regArg,
		poolArg,
		lc,
		func(b *database.Book) any {
			rec.enrichCalls++
			return gin.H{"id": b.ID, "title": b.Title}
		},
		func(p string) bool { return rec.protectedPaths[p] },
		func(bookID string) (map[string]metafetch.MetadataFieldState, error) {
			return rec.loadState, rec.loadStateErr
		},
		func(bookID string, values map[string]any) error {
			rec.updatedFetchedValues = values
			return rec.updateFetchedErr
		},
		func(ctx context.Context, e plugin.Event) { rec.publishedEvents = append(rec.publishedEvents, e) },
	)
	return h, testDeps{store: store, mfs: mfs, wb: wb, reg: reg, pool: pool, cache: lc, rec: rec}
}

// doReq runs a single handler against a synthetic gin context with the given
// method/target/body and optional path params, returning the recorder.
func doReq(h gin.HandlerFunc, method, target string, body any, params gin.Params) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	c.Request = httptest.NewRequest(method, target, rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	h(c)
	return w
}

func idParam(id string) gin.Params { return gin.Params{{Key: "id", Value: id}} }

// ── /metadata/* library endpoints ───────────────────────────────────────────

func TestValidateMetadata(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(h.ValidateMetadata, http.MethodPost, "/metadata/validate",
		map[string]any{"updates": map[string]any{"title": "Valid Title"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestValidateMetadata_BadBody(t *testing.T) {
	h, _ := newHandler(t)
	// missing required "updates" → bad request
	w := doReq(h.ValidateMetadata, http.MethodPost, "/metadata/validate", map[string]any{}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestExportMetadata(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAllBooksCore(0, 0).Return([]database.BookCore{{ID: "b1", Title: "T"}}, nil)
	w := doReq(h.ExportMetadata, http.MethodGet, "/metadata/export", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBatchUpdateMetadata_EmptyUpdates(t *testing.T) {
	h, _ := newHandler(t)
	// updates present but empty list → success, 0/0
	w := doReq(h.BatchUpdateMetadata, http.MethodPost, "/metadata/batch-update",
		map[string]any{"updates": []any{}, "validate": false}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestImportMetadata_BadBody(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(h.ImportMetadata, http.MethodPost, "/metadata/import", map[string]any{}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestSearchMetadata_MissingTitle(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(h.SearchMetadata, http.MethodGet, "/metadata/search", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestGetMetadataFields(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(h.GetMetadataFields, http.MethodGet, "/metadata/fields", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Fields []map[string]any `json:"fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Fields) != 10 {
		t.Fatalf("want 10 fields, got %d", len(resp.Data.Fields))
	}
}

// ── per-book fetch / search / apply / no-match / revert ──────────────────────

func TestFetchAudiobookMetadata(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().FetchMetadataForBook(mock.Anything, "b1").Return(&metafetch.FetchMetadataResponse{
		Message: "ok", Source: "audible", Book: &database.Book{ID: "b1", Title: "T"},
	}, nil)
	d.mfs.EXPECT().InvalidateCachedCandidates("b1").Return(nil)
	d.wb.EXPECT().Enqueue("b1").Return()
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "T2"}, nil)
	w := doReq(h.FetchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/fetch-metadata", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if d.rec.enrichCalls != 1 {
		t.Fatalf("expected enrichBook called once, got %d", d.rec.enrichCalls)
	}
}

func TestFetchAudiobookMetadata_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().FetchMetadataForBook(mock.Anything, "bx").Return(nil, assertErr("nope"))
	w := doReq(h.FetchAudiobookMetadata, http.MethodPost, "/audiobooks/bx/fetch-metadata", nil, idParam("bx"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestSearchAudiobookMetadata_AltQuery(t *testing.T) {
	h, d := newHandler(t)
	// Non-plain fetch (query set) → SearchMetadataForBchWithOptions path.
	d.mfs.EXPECT().SearchMetadataForBookWithOptions("b1", "dune", "", "", "", mock.Anything).
		Return(&metafetch.SearchMetadataResponse{Query: "dune", Results: []metafetch.MetadataCandidate{{Title: "Dune"}}}, nil)
	w := doReq(h.SearchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/search-metadata",
		map[string]any{"query": "dune"}, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSearchAudiobookMetadata_PlainFetchAndCache(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().GetCachedCandidates("b1").Return(nil, false, nil)
	d.mfs.EXPECT().FetchAndCache(mock.Anything, "b1", "", "", "", "", mock.Anything).
		Return(&metafetch.MetadataCandidateCache{FetchedAt: time.Now()}, nil)
	w := doReq(h.SearchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/search-metadata", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestApplyAudiobookMetadata(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().ApplyMetadataCandidate("b1", mock.Anything, mock.Anything).
		Return(&metafetch.FetchMetadataResponse{Message: "applied", Source: "audible", Book: &database.Book{ID: "b1"}}, nil)
	d.mfs.EXPECT().InvalidateCachedCandidates("b1").Return(nil)
	d.wb.EXPECT().Enqueue("b1").Return()
	// Background pool submit fires synchronously in test (we don't run fn).
	d.pool.EXPECT().Submit("b1", mock.Anything).Return()
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "T"}, nil)
	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		map[string]any{"candidate": map[string]any{"title": "X"}, "fields": []string{"title"}}, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(d.rec.publishedEvents) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(d.rec.publishedEvents))
	}
}

func TestMarkAudiobookNoMatch(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().MarkNoMatch("b1").Return(nil)
	d.store.EXPECT().AddMetadataRejection(mock.Anything).Return(nil)
	w := doReq(h.MarkAudiobookNoMatch, http.MethodPost, "/audiobooks/b1/mark-no-match", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleGetMetadataRejections(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetMetadataRejections("b1").Return([]database.MetadataRejection{{ID: "r1", BookID: "b1"}}, nil)
	w := doReq(h.HandleGetMetadataRejections, http.MethodGet, "/audiobooks/b1/metadata-rejections", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRevertAudiobookMetadata(t *testing.T) {
	h, d := newHandler(t)
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	d.store.EXPECT().RevertBookToVersion("b1", mock.Anything).Return(&database.Book{ID: "b1"}, nil)
	d.mfs.EXPECT().InvalidateCachedCandidates("b1").Return(nil)
	w := doReq(h.RevertAudiobookMetadata, http.MethodPost, "/audiobooks/b1/revert-metadata",
		map[string]any{"timestamp": ts}, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRevertAudiobookMetadata_MissingTimestamp(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(h.RevertAudiobookMetadata, http.MethodPost, "/audiobooks/b1/revert-metadata",
		map[string]any{}, idParam("b1"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// ── CoW versions ─────────────────────────────────────────────────────────────

func TestListBookCOWVersions(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookSnapshots("b1", mock.Anything).Return([]database.BookSnapshot{{}}, nil)
	w := doReq(h.ListBookCOWVersions, http.MethodGet, "/audiobooks/b1/cow-versions", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPruneBookCOWVersions(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().PruneBookSnapshots("b1", 3).Return(5, nil)
	w := doReq(h.PruneBookCOWVersions, http.MethodPost, "/audiobooks/b1/cow-versions/prune",
		map[string]any{"keep_count": 3}, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPruneBookCOWVersions_BadKeepCount(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(h.PruneBookCOWVersions, http.MethodPost, "/audiobooks/b1/cow-versions/prune",
		map[string]any{"keep_count": 0}, idParam("b1"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// ── write-back ───────────────────────────────────────────────────────────────

func TestWriteBackAudiobookMetadata(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "T"}, nil)
	d.mfs.EXPECT().WriteBackMetadataForBook("b1").Return(2, nil)
	w := doReq(h.WriteBackAudiobookMetadata, http.MethodPost, "/audiobooks/b1/write-back", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWriteBackAudiobookMetadata_MissingID(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(h.WriteBackAudiobookMetadata, http.MethodPost, "/audiobooks//write-back", nil, idParam(""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// ── bulk fetch ───────────────────────────────────────────────────────────────

func TestBulkFetchMetadata_MissingBookIDs(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(h.BulkFetchMetadata, http.MethodPost, "/metadata/bulk-fetch",
		map[string]any{"book_ids": []string{}}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestBulkFetchMetadata_Updates(t *testing.T) {
	h, d := newHandler(t)
	d.rec.loadState = map[string]metafetch.MetadataFieldState{}
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Old"}, nil)
	d.mfs.EXPECT().SearchMetadataForBookWithOptions("b1", "", "", "", "", mock.Anything).
		Return(&metafetch.SearchMetadataResponse{Results: []metafetch.MetadataCandidate{
			{Title: "New Title", Source: "audible", Publisher: "Pub"},
		}}, nil)
	// onlyMissing defaults true; title has a value so it's only fetched (not applied),
	// publisher is missing so it gets applied → didUpdate true.
	d.mfs.EXPECT().RecordChangeHistory(mock.Anything, mock.Anything, "audible").Return()
	d.store.EXPECT().UpdateBook("b1", mock.Anything).Return(&database.Book{ID: "b1"}, nil)
	d.mfs.EXPECT().ApplyMetadataSystemTags("b1", "audible", "").Return()
	w := doReq(h.BulkFetchMetadata, http.MethodPost, "/metadata/bulk-fetch",
		map[string]any{"book_ids": []string{"b1"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestBulkFetchMetadata_ParallelPreservesOrderAndCounts exercises
// bulkFetchMetadataImpl's registry.RunItems-based parallel pass (CONC-12)
// with 6 book IDs against a concurrency of 4 (bulkFetchMetadataConcurrency),
// so multiple workers are guaranteed to run at once. It asserts the response
// is byte-for-byte what the old serial `for` loop would have produced:
// results in the exact input order (written via results[i], not append) with
// the expected per-book status, and the correct aggregate updated_count. Run
// under `go test -race`, this also exercises the mutex guarding the shared
// results slice / updatedCount (the "shared mutable state" named in the
// CONC-12 brief) — the store/service mocks are testify mocks, which are
// internally mutex-guarded and safe to call concurrently.
//
// The Handler here is built directly (not via the shared newHandler/recorders
// helper used by the rest of this file) because recorders.updatedFetchedValues
// is a plain unguarded field write — fine for the existing single-book,
// strictly-sequential tests, but multiple of the book IDs below have
// non-empty fetched values, so concurrent workers would race on that shared
// field. The closures below are either pure (loadMetadataState) or
// mutex-guarded (updateFetchedMetadataState) instead.
func TestBulkFetchMetadata_ParallelPreservesOrderAndCounts(t *testing.T) {
	store := metadatamocks.NewMockMetadataStore(t)
	mfs := metadatamocks.NewMockMetadataFetchService(t)

	// b0: book not found.
	store.EXPECT().GetBookByID("b0").Return(nil, nil)

	// b1: found but title is blank/whitespace-only → "missing title", status
	// stays the zero-value "skipped".
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "  "}, nil)

	// b2: search errors out.
	store.EXPECT().GetBookByID("b2").Return(&database.Book{ID: "b2", Title: "T2"}, nil)
	mfs.EXPECT().SearchMetadataForBookWithOptions("b2", "", "", "", "", mock.Anything).
		Return(nil, errors.New("boom"))

	// b3: search returns no candidates.
	store.EXPECT().GetBookByID("b3").Return(&database.Book{ID: "b3", Title: "T3"}, nil)
	mfs.EXPECT().SearchMetadataForBookWithOptions("b3", "", "", "", "", mock.Anything).
		Return(&metafetch.SearchMetadataResponse{Results: nil}, nil)

	// b4: candidate title fetched but not applied (book already has a title
	// and onlyMissing defaults true) → status "fetched", no write-back.
	store.EXPECT().GetBookByID("b4").Return(&database.Book{ID: "b4", Title: "Old4"}, nil)
	mfs.EXPECT().SearchMetadataForBookWithOptions("b4", "", "", "", "", mock.Anything).
		Return(&metafetch.SearchMetadataResponse{Results: []metafetch.MetadataCandidate{
			{Title: "New4", Source: "google"},
		}}, nil)

	// b5: publisher is missing on the book, so the fetched publisher gets
	// applied → didUpdate → status "updated" (the only one counted in
	// updated_count).
	store.EXPECT().GetBookByID("b5").Return(&database.Book{ID: "b5", Title: "Old5"}, nil)
	mfs.EXPECT().SearchMetadataForBookWithOptions("b5", "", "", "", "", mock.Anything).
		Return(&metafetch.SearchMetadataResponse{Results: []metafetch.MetadataCandidate{
			{Title: "Old5", Source: "audible", Publisher: "Pub5"},
		}}, nil)
	mfs.EXPECT().RecordChangeHistory(mock.Anything, mock.Anything, "audible").Return()
	store.EXPECT().UpdateBook("b5", mock.Anything).Return(&database.Book{ID: "b5"}, nil)
	mfs.EXPECT().ApplyMetadataSystemTags("b5", "audible", "").Return()

	var utMu sync.Mutex
	utValues := map[string]map[string]any{}

	h := metadatahandler.New(
		store,
		mfs,
		func() metadatahandler.WriteBackEnqueuer { return nil },
		nil, // opRegistry: unused by bulkFetchMetadataImpl
		nil, // fileIOPool: unused by bulkFetchMetadataImpl
		cache.New[gin.H]("meta-test-parallel", time.Minute),
		func(b *database.Book) any { return gin.H{"id": b.ID} },
		func(p string) bool { return false },
		func(bookID string) (map[string]metafetch.MetadataFieldState, error) {
			return map[string]metafetch.MetadataFieldState{}, nil // pure — safe for concurrent calls
		},
		func(bookID string, values map[string]any) error {
			utMu.Lock()
			defer utMu.Unlock()
			utValues[bookID] = values
			return nil
		},
		func(ctx context.Context, e plugin.Event) {},
	)

	bookIDs := []string{"b0", "b1", "b2", "b3", "b4", "b5"}
	w := doReq(h.BulkFetchMetadata, http.MethodPost, "/metadata/bulk-fetch",
		map[string]any{"book_ids": bookIDs}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data struct {
			UpdatedCount int `json:"updated_count"`
			TotalCount   int `json:"total_count"`
			Results      []struct {
				BookID string `json:"book_id"`
				Status string `json:"status"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v: %s", err, w.Body.String())
	}
	resp := envelope.Data

	if resp.TotalCount != len(bookIDs) {
		t.Fatalf("total_count = %d, want %d", resp.TotalCount, len(bookIDs))
	}
	if resp.UpdatedCount != 1 {
		t.Fatalf("updated_count = %d, want 1", resp.UpdatedCount)
	}
	if len(resp.Results) != len(bookIDs) {
		t.Fatalf("len(results) = %d, want %d", len(resp.Results), len(bookIDs))
	}

	wantStatus := map[string]string{
		"b0": "not_found",
		"b1": "skipped",
		"b2": "error",
		"b3": "not_found",
		"b4": "fetched",
		"b5": "updated",
	}
	for i, id := range bookIDs {
		if resp.Results[i].BookID != id {
			t.Fatalf("results[%d].book_id = %q, want %q (order not preserved)", i, resp.Results[i].BookID, id)
		}
		if want := wantStatus[id]; resp.Results[i].Status != want {
			t.Fatalf("results[%d] (%s).status = %q, want %q", i, id, resp.Results[i].Status, want)
		}
	}

	utMu.Lock()
	if len(utValues["b4"]) == 0 {
		t.Fatalf("expected fetched-metadata-state write for b4 (title fetched but not applied)")
	}
	if len(utValues["b5"]) == 0 {
		t.Fatalf("expected fetched-metadata-state write for b5")
	}
	utMu.Unlock()
}

// ── bulk / batch write-back enqueue ──────────────────────────────────────────

func TestHandleBulkWriteBack_DryRun(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAllBooksCore(1_000_000, 0).Return([]database.BookCore{
		{ID: "b1", FilePath: "/x/a.m4b", LibraryState: strptr("organized")},
	}, nil)
	w := doReq(h.HandleBulkWriteBack, http.MethodPost, "/audiobooks/bulk-write-back",
		map[string]any{"dry_run": true}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleBulkWriteBack_Enqueue202(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAllBooksCore(1_000_000, 0).Return([]database.BookCore{
		{ID: "b1", FilePath: "/x/a.m4b", LibraryState: strptr("organized")},
	}, nil)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "library.bulk-write-back", mock.Anything).Return("op-123", nil)
	w := doReq(h.HandleBulkWriteBack, http.MethodPost, "/audiobooks/bulk-write-back",
		map[string]any{}, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleBulkWriteBack_NoRegistry(t *testing.T) {
	h, d := newHandler(t, noReg)
	_ = d
	w := doReq(h.HandleBulkWriteBack, http.MethodPost, "/audiobooks/bulk-write-back",
		map[string]any{}, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
}

func TestBatchWriteBackAudiobooks(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().CreateOperation(mock.Anything, "batch_save_to_files", mock.Anything).
		Return(&database.Operation{}, nil)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "metadata.batch-save", mock.Anything).Return("op-1", nil)
	w := doReq(h.BatchWriteBackAudiobooks, http.MethodPost, "/audiobooks/batch-write-back",
		map[string]any{"book_ids": []string{"b1", "b2"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBatchWriteBackAudiobooks_MissingBookIDs(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(h.BatchWriteBackAudiobooks, http.MethodPost, "/audiobooks/batch-write-back",
		map[string]any{"book_ids": []string{}}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// ── rating PATCH ─────────────────────────────────────────────────────────────

func TestHandleUpdateBookRating(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().UpdateBookRating("b1", mock.Anything).Return(nil)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	w := doReq(h.HandleUpdateBookRating, http.MethodPatch, "/audiobooks/b1/rating",
		map[string]any{"overall": 4.5}, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleUpdateBookRating_InvalidRating(t *testing.T) {
	h, _ := newHandler(t)
	// 4.3 is not a 0.5 step → 400 before any store call.
	w := doReq(h.HandleUpdateBookRating, http.MethodPatch, "/audiobooks/b1/rating",
		map[string]any{"overall": 4.3}, idParam("b1"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHandleUpdateBookRating_MissingID(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(h.HandleUpdateBookRating, http.MethodPatch, "/audiobooks//rating", nil, idParam(""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func strptr(s string) *string { return &s }

type stringError string

func (e stringError) Error() string { return string(e) }

func assertErr(s string) error { return stringError(s) }
