// file: internal/server/handlers/metadata/handler_test.go
// version: 1.21.2
// guid: 1d31ef73-7c7a-4c3b-a840-01b0865023d7
// last-edited: 2026-10-31

// Tests for the metadata-domain handlers. The store / metadata-fetch-service /
// write-back-enqueuer / operations-registry / file-io-pool deps are generated
// mocks; the injected helper funcs (enrichBook, isProtectedPath,
// publishEvent) are stub closures that record their
// invocations or return canned payloads. There is at least one
// test per public method (18 methods) covering happy paths and key branches.

package metadatahandler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
	metadatamocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata/mocks"
)

func init() { gin.SetMode(gin.TestMode) }

// recorders captures the side effects of the injected helper closures so tests
// can assert they fired.
type recorders struct {
	enrichCalls     int
	protectedPaths  map[string]bool
	publishedEvents []plugin.Event
}

type testDeps struct {
	store *metadatamocks.MockMetadataStore
	mfs   *metadatamocks.MockMetadataFetchService
	reg   *metadatamocks.MockOperationsRegistry
	pool  *metadatamocks.MockFileIOPool
	cache *cache.Cache[gin.H]
	rec   *recorders
}

type cfg struct {
	hasReg, hasPool, hasMFS bool
}

func noReg(c *cfg) { c.hasReg = false }

// newHandler builds a Handler with fresh mocks. listCache is a real in-memory
// cache. Any provider/snapshot dep can be left out via the no* options to
// exercise the in-method nil guards.
func newHandler(t *testing.T, opts ...func(*cfg)) (*metadatahandler.Handler, testDeps) {
	t.Helper()
	cf := &cfg{hasReg: true, hasPool: true, hasMFS: true}
	for _, o := range opts {
		o(cf)
	}

	store := metadatamocks.NewMockMetadataStore(t)
	// No owner rejections unless a test says otherwise (applygate.ReasonOwnerRejected).
	store.EXPECT().ScanPrefix(mock.Anything).Return(nil, nil).Maybe()
	mfs := metadatamocks.NewMockMetadataFetchService(t)
	reg := metadatamocks.NewMockOperationsRegistry(t)
	pool := metadatamocks.NewMockFileIOPool(t)
	lc := cache.New[gin.H]("meta-test", time.Minute)
	rec := &recorders{protectedPaths: map[string]bool{}}

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
		regArg,
		poolArg,
		lc,
		func(b *database.Book) any {
			rec.enrichCalls++
			return gin.H{"id": b.ID, "title": b.Title}
		},
		func(p string) bool { return rec.protectedPaths[p] },
		func(ctx context.Context, e plugin.Event) { rec.publishedEvents = append(rec.publishedEvents, e) },
	)
	return h, testDeps{store: store, mfs: mfs, reg: reg, pool: pool, cache: lc, rec: rec}
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

// A fetch whose only match is review-only (owner decision 2026-10-06) is
// not a 404: a match was found and left for review, the book is unchanged
// and nothing is queued for write-back.
func TestFetchAudiobookMetadata_ReviewOnlyMatchIsLeftForReview(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().FetchMetadataForBook(mock.Anything, "b1").
		Return(nil, &metafetch.ReviewOnlyNotAppliedError{Title: "T", Sources: []string{"Open Library"}})
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "T"}, nil)
	w := doReq(h.FetchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/fetch-metadata", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"review_only":true`) || !strings.Contains(body, "Match found, left for review: Open Library") {
		t.Fatalf("body %s, want review_only and the left-for-review message", body)
	}
}

func TestSearchAudiobookMetadata_AltQuery(t *testing.T) {
	h, d := newHandler(t)
	// Non-plain fetch (query set) → SearchMetadataForBchWithOptions path.
	d.mfs.EXPECT().SearchMetadataForBookWithOptions("b1", "dune", "", "", "", mock.Anything).
		Return(&metafetch.SearchMetadataResponse{Query: "dune", Results: []metafetch.MetadataCandidate{{Title: "Dune"}}}, nil)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
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
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	w := doReq(h.SearchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/search-metadata", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// A cache row marked Stale (the book was retitled after its candidates were
// fetched) is a cache miss for the plain fetch: the dialog searches the
// current title instead of serving the old title's candidates, and does not
// fall back to the 60-second listCache entry written before the retitle.
func TestSearchAudiobookMetadata_StaleRowIsACacheMiss(t *testing.T) {
	h, d := newHandler(t)
	old := &metafetch.MetadataCandidateCache{BookID: "b1", FetchedAt: time.Now(),
		Candidates: []json.RawMessage{json.RawMessage(`{"title":"Title 000001","author":"Author 07","score":0.9}`)}}
	// First open, before the retitle: served from the row, which also fills
	// the 60-second listCache.
	d.mfs.EXPECT().GetCachedCandidates("b1").Return(old, true, nil).Once()
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Title 000001"}, nil)
	w := doReq(h.SearchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/search-metadata", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("first open: want 200, got %d: %s", w.Code, w.Body.String())
	}

	// The retitle marks the row Stale; the next open searches again.
	stale := *old
	stale.Stale = true
	stale.StaleQuestionFP = "fp-old-question"
	d.mfs.EXPECT().GetCachedCandidates("b1").Return(&stale, true, nil).Once()
	d.mfs.EXPECT().FetchAndCache(mock.Anything, "b1", "", "", "", "", mock.Anything).
		Return(&metafetch.MetadataCandidateCache{BookID: "b1", FetchedAt: time.Now(),
			Candidates: []json.RawMessage{json.RawMessage(`{"title":"Title 000002","author":"Author 07","score":0.9}`)}}, nil).Once()
	w = doReq(h.SearchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/search-metadata", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("after retitle: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			FromCache bool `json:"from_cache"`
			Results   []struct {
				Title string `json:"title"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	body := env.Data
	if body.FromCache {
		t.Fatalf("a stale row was served from cache: %s", w.Body.String())
	}
	if len(body.Results) != 1 || body.Results[0].Title != "Title 000002" {
		t.Fatalf("want the fresh search's candidate, got %s", w.Body.String())
	}
}

// Every provider failing is an outage, not a missing book: 503, not 404.
func TestSearchAudiobookMetadata_NoSourceAnsweredIs503(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().GetCachedCandidates("b1").Return(nil, false, nil)
	// The dialog reads the book to rank the row it writes (MergeRank).
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.mfs.EXPECT().FetchAndCache(mock.Anything, "b1", "", "", "", "", mock.Anything).
		Return(nil, fmt.Errorf("%w (audible: timeout)", metafetch.ErrNoSourceAnswered))
	w := doReq(h.SearchAudiobookMetadata, http.MethodPost, "/audiobooks/b1/search-metadata", nil, idParam("b1"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", w.Code, w.Body.String())
	}
}

func noPool(c *cfg) { c.hasPool = false }

// A rename known to fail refuses the apply before any write: 409 with the
// shared reason and the preflight's detail. ApplyMetadataCandidate,
// the write-back enqueue, the pool submit and the
// event are never set up, so the strict mocks fail the test if any runs.
func TestApplyAudiobookMetadata_RenamePreflightRefusesBeforeAnyWrite(t *testing.T) {
	h, d := newHandler(t)
	blocked := fmt.Errorf("%w: two files of one book plan the same target path", metafetch.ErrApplyFileWorkWouldFail)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.mfs.EXPECT().RenamePreflight("b1", mock.Anything, []string{"title"}).Return(blocked)
	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		map[string]any{"candidate": map[string]any{"title": "X"}, "fields": []string{"title"}}, idParam("b1"))
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Reason != metafetch.ApplyRefusedReasonFileWorkWouldFail {
		t.Fatalf("reason = %q, want %q", body.Reason, metafetch.ApplyRefusedReasonFileWorkWouldFail)
	}
	if !strings.Contains(body.Error, "same target path") {
		t.Fatalf("error must carry the preflight detail, got %q", body.Error)
	}
	if len(d.rec.publishedEvents) != 0 {
		t.Fatalf("a refused apply must publish no event, got %d", len(d.rec.publishedEvents))
	}
}

// With no file-IO pool there is no file sequel, so no rename to check: the
// preflight is not called (the strict mock has no expectation for it).
func TestApplyAudiobookMetadata_NoFileSequelSkipsRenamePreflight(t *testing.T) {
	h, d := newHandler(t, noPool)
	d.mfs.EXPECT().ApplyMetadataCandidate("b1", mock.Anything, mock.Anything).
		Return(&metafetch.FetchMetadataResponse{Message: "applied", Source: "audible", Book: &database.Book{ID: "b1"}}, nil)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "T"}, nil)
	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		map[string]any{"candidate": map[string]any{"title": "X"}}, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// write_back=false still runs the file I/O (and so the rename) in the
// background job, so the preflight still runs.
func TestApplyAudiobookMetadata_WriteBackOffStillRunsRenamePreflight(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.mfs.EXPECT().RenamePreflight("b1", mock.Anything, mock.Anything).
		Return(fmt.Errorf("%w: planned target held by another file", metafetch.ErrApplyFileWorkWouldFail))
	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		map[string]any{"candidate": map[string]any{"title": "X"}, "write_back": false}, idParam("b1"))
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestApplyAudiobookMetadata(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().RenamePreflight("b1", mock.Anything, []string{"title"}).Return(nil)
	d.mfs.EXPECT().ApplyMetadataCandidate("b1", mock.Anything, mock.Anything).
		Return(&metafetch.FetchMetadataResponse{Message: "applied", Source: "audible", Book: &database.Book{ID: "b1"}}, nil)
	// Background pool submit fires synchronously in test (we don't run fn).
	d.pool.EXPECT().Submit("b1", mock.Anything).Return(true)
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

// TestMarkAudiobookNoMatch also pins the A3#14 reject rule: marking a book "no
// match" deletes its provider fetch-cache rows, because the rejected result is
// wrong for the book's UNCHANGED identity and its stamp would still match.
// The mockery mock fails the test if the call is missing (AssertExpectations).
func TestMarkAudiobookNoMatch(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().MarkNoMatch("b1").Return(nil)
	d.mfs.EXPECT().InvalidateFetchCacheForBook("b1").Return(nil).Once()
	d.store.EXPECT().AddMetadataRejection(mock.Anything).Return(nil)
	w := doReq(h.MarkAudiobookNoMatch, http.MethodPost, "/audiobooks/b1/mark-no-match", nil, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestMarkAudiobookNoMatch_FetchCacheWipeFailureStillSucceeds: the rejection
// itself is recorded; a failed cache delete is logged, not surfaced as a 500.
func TestMarkAudiobookNoMatch_FetchCacheWipeFailureStillSucceeds(t *testing.T) {
	h, d := newHandler(t)
	d.mfs.EXPECT().MarkNoMatch("b1").Return(nil)
	d.mfs.EXPECT().InvalidateFetchCacheForBook("b1").Return(errors.New("scan failed")).Once()
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

// The whole-row revert runs under the merge lock a retire holds (review S1 of
// #4144): while the store call is in flight another merge-family holder must be
// kept out, and the lock must be released once the handler returns. A holder
// that gets in during the call is what fails this; without the lock in the
// handler it does, immediately.
func TestRevertAudiobookMetadata_HoldsTheMergeLockAroundTheStoreCall(t *testing.T) {
	h, d := newHandler(t)
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	var rival atomic.Bool
	var rivalGotInDuringCall bool
	d.store.EXPECT().RevertBookToVersion("b1", mock.Anything).RunAndReturn(func(string, time.Time) (*database.Book, error) {
		go func() {
			merge.LockMergeRMW()
			rival.Store(true)
			merge.UnlockMergeRMW()
		}()
		time.Sleep(150 * time.Millisecond)
		rivalGotInDuringCall = rival.Load()
		return &database.Book{ID: "b1"}, nil
	})
	d.mfs.EXPECT().InvalidateCachedCandidates("b1").Return(nil)
	w := doReq(h.RevertAudiobookMetadata, http.MethodPost, "/audiobooks/b1/revert-metadata",
		map[string]any{"timestamp": ts}, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if rivalGotInDuringCall {
		t.Fatal("another merge-lock holder ran while RevertBookToVersion was in flight")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !rival.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the merge lock was never released after the handler returned")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A retired shell is refused with 409 RETIRED_SHELL, not a 500, and the cached
// candidates are left alone (nothing changed).
func TestRevertAudiobookMetadata_RetiredShellIs409(t *testing.T) {
	h, d := newHandler(t)
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	d.store.EXPECT().RevertBookToVersion("b1", mock.Anything).
		Return(nil, fmt.Errorf("revert book b1: %w", database.ErrBookIsRetiredShell))
	w := doReq(h.RevertAudiobookMetadata, http.MethodPost, "/audiobooks/b1/revert-metadata",
		map[string]any{"timestamp": ts}, idParam("b1"))
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "RETIRED_SHELL") {
		t.Errorf("body lacks the RETIRED_SHELL code: %s", w.Body.String())
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

// ── bulk / batch write-back enqueue ──────────────────────────────────────────

func TestHandleBulkWriteBack_DryRun(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAllBooksCore(0, 0).Return([]database.BookCore{
		{ID: "b1", FilePath: "/x/a.m4b", LibraryState: new("organized")},
	}, nil)
	w := doReq(h.HandleBulkWriteBack, http.MethodPost, "/audiobooks/bulk-write-back",
		map[string]any{"dry_run": true}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleBulkWriteBack_Enqueue202(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAllBooksCore(0, 0).Return([]database.BookCore{
		{ID: "b1", FilePath: "/x/a.m4b", LibraryState: new("organized")},
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

// The handler must hand back the id EnqueueOp minted, NOT one it minted itself.
// The absence of a CreateOperation expectation is half the assertion: the mock
// is strict, so a reintroduced v1 write fails this test rather than passing
// silently. The returned id is the other half — a handler that enqueued
// correctly but still returned its own id is the #2747 bug, and it would
// satisfy the mock while leaving the client polling an id that resolves
// nowhere.
func TestBatchWriteBackAudiobooks_ReturnsEnqueuedOpID(t *testing.T) {
	h, d := newHandler(t)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "metadata.batch-save", mock.Anything).Return("op-1", nil)
	w := doReq(h.BatchWriteBackAudiobooks, http.MethodPost, "/audiobooks/batch-write-back",
		map[string]any{"book_ids": []string{"b1", "b2"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"operation_id":"op-1"`) {
		t.Fatalf("must return the enqueued id op-1, got %s", w.Body.String())
	}
}

// The params must not carry a legacy_op_id. batchSaveOpParams here is a
// cross-package MIRROR of server.batchSaveOpParams coupled only by JSON tags —
// the compiler cannot see the two drift apart, and they already did once: the
// server side dropped LegacyOpID while this copy kept it, silently marshalling
// "legacy_op_id":"" on every request. Only a marshalled check catches that, so
// capture the params the handler actually passed rather than mock.Anything.
func TestBatchWriteBackAudiobooks_ParamsCarryNoLegacyOpID(t *testing.T) {
	h, d := newHandler(t)
	var gotParams any
	d.reg.EXPECT().EnqueueOp(mock.Anything, "metadata.batch-save", mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, params any,
			_ ...opsregistry.EnqueueOption) (string, error) {
			gotParams = params
			return "op-1", nil
		})
	doReq(h.BatchWriteBackAudiobooks, http.MethodPost, "/audiobooks/batch-write-back",
		map[string]any{"book_ids": []string{"b1", "b2"}}, nil)

	raw, err := json.Marshal(gotParams)
	if err != nil {
		t.Fatalf("params must marshal: %v", err)
	}
	if bytes.Contains(raw, []byte("legacy_op_id")) {
		t.Fatalf("params must not carry legacy_op_id, got %s", raw)
	}
	if !bytes.Contains(raw, []byte("book_ids")) {
		t.Fatalf("params lost book_ids: %s", raw)
	}
}

// An enqueue failure must surface, not be swallowed into a 200 with no run
// behind it.
func TestBatchWriteBackAudiobooks_EnqueueFailureIs500(t *testing.T) {
	h, d := newHandler(t)
	d.reg.EXPECT().EnqueueOp(mock.Anything, "metadata.batch-save", mock.Anything).
		Return("", errors.New("queue down"))
	w := doReq(h.BatchWriteBackAudiobooks, http.MethodPost, "/audiobooks/batch-write-back",
		map[string]any{"book_ids": []string{"b1", "b2"}}, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", w.Code, w.Body.String())
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

type stringError string

func (e stringError) Error() string { return string(e) }

func assertErr(s string) error { return stringError(s) }
