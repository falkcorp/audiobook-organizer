// file: internal/server/handlers/audiobooks/handler_test.go
// version: 1.4.0
// guid: 5cd764d5-8036-425c-842e-c49d0d44acec
// last-edited: 2026-07-11

// Tests for the audiobooks-domain handlers (main library list / CRUD). The
// store / audiobook-service / updater / write-back / metadata-state /
// metadata-fetch / batch / changelog / external-id deps are generated mocks; the
// injected helper funcs (buildListResponse, buildFacetsResponse, isProtectedPath,
// enrichBook, getFieldStates, getExternalIDStore, publishEvent) are stub closures
// returning canned payloads or recording invocations. There is at least one test
// per public method (36 methods) plus key branches.

package audiobookshandler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	audiobookshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks"
	audiobooksmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks/mocks"
)

func init() { gin.SetMode(gin.TestMode) }

// recorders captures side effects of injected closures so tests can assert.
type recorders struct {
	protectedPaths   []string
	protectedReturn  bool
	enrichCalls      int
	fieldStatesCalls int
	fieldStatesVal   any
	fieldStatesErr   error
	publishedEvents  []plugin.Event
	listResp         gin.H
	listErr          error
	facetsResp       gin.H
	facetsErr        error
	facetsCalls      int
	extIDStore       audiobookshandler.ExternalIDStore
}

type testDeps struct {
	store      *audiobooksmocks.MockAudiobooksStore
	svc        *audiobooksmocks.MockAudiobookService
	updater    *audiobooksmocks.MockAudiobookUpdater
	writeBack  *audiobooksmocks.MockWriteBackEnqueuer
	metaState  *audiobooksmocks.MockMetadataStateService
	metaFetch  *audiobooksmocks.MockMetadataFetchService
	batchSvc   *audiobooksmocks.MockBatchService
	changelog  *audiobooksmocks.MockChangelogService
	listCache  *cache.Cache[gin.H]
	facetCache *cache.Cache[gin.H]
	authCache  *cache.Cache[*audiobookspkg.AuthorWithCountListResponse]
	serCache   *cache.Cache[*audiobookspkg.SeriesWithCountsResponse]
	rec        *recorders
}

// newHandler wires a Handler with fresh mocks + stub injected funcs.
func newHandler(t *testing.T) (*audiobookshandler.Handler, testDeps) {
	t.Helper()
	store := audiobooksmocks.NewMockAudiobooksStore(t)
	svc := audiobooksmocks.NewMockAudiobookService(t)
	updater := audiobooksmocks.NewMockAudiobookUpdater(t)
	writeBack := audiobooksmocks.NewMockWriteBackEnqueuer(t)
	metaState := audiobooksmocks.NewMockMetadataStateService(t)
	metaFetch := audiobooksmocks.NewMockMetadataFetchService(t)
	batchSvc := audiobooksmocks.NewMockBatchService(t)
	changelog := audiobooksmocks.NewMockChangelogService(t)

	lc := cache.New[gin.H]("list-test", 0)
	fc := cache.New[gin.H]("facets-test", 0)
	ac := cache.New[*audiobookspkg.AuthorWithCountListResponse]("authors-test", 0)
	sc := cache.New[*audiobookspkg.SeriesWithCountsResponse]("series-test", 0)

	rec := &recorders{}

	h := audiobookshandler.New(
		store,
		svc,
		updater,
		func() audiobookshandler.WriteBackEnqueuer { return writeBack },
		metaState,
		metaFetch,
		batchSvc,
		changelog,
		lc, fc, ac, sc,
		func(ctx context.Context, limit, offset int, search string, authorID, seriesID *int, filters audiobookspkg.ListFilters, showQuarantined bool) (gin.H, error) {
			if rec.listResp == nil {
				rec.listResp = gin.H{"items": []any{}, "count": 0, "limit": limit, "offset": offset}
			}
			return rec.listResp, rec.listErr
		},
		func(ctx context.Context) (gin.H, error) {
			rec.facetsCalls++
			if rec.facetsResp == nil && rec.facetsErr == nil {
				rec.facetsResp = gin.H{"genres": []string{}, "languages": []string{}}
			}
			return rec.facetsResp, rec.facetsErr
		},
		func(filePath string) bool {
			rec.protectedPaths = append(rec.protectedPaths, filePath)
			return rec.protectedReturn
		},
		func(b *database.Book) any {
			rec.enrichCalls++
			return gin.H{"id": b.ID, "title": b.Title}
		},
		func(id string) (any, error) {
			rec.fieldStatesCalls++
			return rec.fieldStatesVal, rec.fieldStatesErr
		},
		func() audiobookshandler.ExternalIDStore { return rec.extIDStore },
		func(ctx context.Context, event plugin.Event) {
			rec.publishedEvents = append(rec.publishedEvents, event)
		},
	)
	return h, testDeps{store, svc, updater, writeBack, metaState, metaFetch, batchSvc, changelog, lc, fc, ac, sc, rec}
}

// newCtx builds a gin test context for the given method/target with optional
// JSON body and route params.
func newCtx(method, target string, body any, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, target, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	c.Request = r
	c.Params = params
	return c, w
}

func p(key, val string) gin.Params { return gin.Params{{Key: key, Value: val}} }

func errString(s string) error { return errors.New(s) }

// ---- list / count / facets ----

func TestListAudiobooks_CacheMiss(t *testing.T) {
	h, d := newHandler(t)
	d.rec.listResp = gin.H{"items": []any{}, "count": 0, "limit": 50, "offset": 0}
	c, w := newCtx("GET", "/audiobooks", nil, nil)
	h.ListAudiobooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestListAudiobooks_FileErrorsFastPath(t *testing.T) {
	h, d := newHandler(t)
	// store does not implement ListBooksWithFileErrors/Unwrap → bookIDs nil → empty set
	c, w := newCtx("GET", "/audiobooks?has_file_errors=true", nil, nil)
	_ = d
	h.ListAudiobooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
}

func TestListAudiobooks_QuickQueryEmpty(t *testing.T) {
	h, _ := newHandler(t)
	// store doesn't implement GetAllBookIDsForQuickQuery → empty set
	c, w := newCtx("GET", "/audiobooks?missing_covers=true", nil, nil)
	h.ListAudiobooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestCountAudiobooks(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().CountAudiobooks(mock.Anything).Return(42, nil)
	c, w := newCtx("GET", "/audiobooks/count", nil, nil)
	h.CountAudiobooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

// TestAudiobookFacets_NilIndexFallbackShape pins the INIT-4 T4 nil-index
// fallback contract: when buildFacetsResponse (the shared handler+warmer
// builder in package server) can't reach the Bleve index, the response
// deep-equals the pre-task shape exactly — the genre_counts/language_counts/
// tag_counts keys are OMITTED, not present-but-empty.
func TestAudiobookFacets_NilIndexFallbackShape(t *testing.T) {
	h, d := newHandler(t)
	d.rec.facetsResp = gin.H{"genres": []string{"Fantasy"}, "languages": []string{"en"}}
	c, w := newCtx("GET", "/audiobooks/facets", nil, nil)
	h.AudiobookFacets(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]any{
		"genres":    []any{"Fantasy"},
		"languages": []any{"en"},
	}
	if !reflect.DeepEqual(resp.Data, want) {
		t.Errorf("body = %#v, want %#v (no *_counts keys)", resp.Data, want)
	}
	if d.rec.facetsCalls != 1 {
		t.Errorf("facetsCalls = %d, want 1", d.rec.facetsCalls)
	}
}

// TestAudiobookFacets_WithIndexShape pins the with-index shape: the
// additive genre_counts/language_counts/tag_counts keys are present
// alongside the unchanged genres/languages lists.
func TestAudiobookFacets_WithIndexShape(t *testing.T) {
	h, d := newHandler(t)
	d.rec.facetsResp = gin.H{
		"genres":          []string{"Fantasy"},
		"languages":       []string{"en"},
		"genre_counts":    map[string]int{"Fantasy": 3},
		"language_counts": map[string]int{"en": 3},
		"tag_counts":      map[string]int{},
	}
	c, w := newCtx("GET", "/audiobooks/facets", nil, nil)
	h.AudiobookFacets(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, key := range []string{"genre_counts", "language_counts", "tag_counts"} {
		if !strings.Contains(body, key) {
			t.Errorf("body missing %q: %s", key, body)
		}
	}
}

// TestAudiobookFacets_CacheHitSkipsBuilder confirms a warm cache entry is
// served without invoking buildFacetsResponse again.
func TestAudiobookFacets_CacheHitSkipsBuilder(t *testing.T) {
	h, d := newHandler(t)
	// newHandler's caches use a 0 default TTL (always-expired, so every other
	// test here forces a cache miss on purpose) — this test needs an entry
	// that is still live when AudiobookFacets reads it, hence SetWithTTL.
	d.facetCache.SetWithTTL("all", gin.H{"genres": []string{"Fantasy"}, "languages": []string{"en"}}, time.Minute)
	c, w := newCtx("GET", "/audiobooks/facets", nil, nil)
	h.AudiobookFacets(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if d.rec.facetsCalls != 0 {
		t.Errorf("facetsCalls = %d, want 0 (cache hit should skip the builder)", d.rec.facetsCalls)
	}
}

// TestAudiobookFacets_BuilderError covers the pre-existing (unrelated to
// INIT-4 T4) DB-fetch-failure path: buildFacetsResponse returning an error
// (e.g. store not initialized, GetDistinctGenres/Languages erroring) is
// still a 500 — only a FacetCounts-specific error fails open, per the
// builder's own doc comment (audiobooks_helpers.go).
func TestAudiobookFacets_BuilderError(t *testing.T) {
	h, d := newHandler(t)
	d.rec.facetsErr = errString("database not initialized")
	c, w := newCtx("GET", "/audiobooks/facets", nil, nil)
	h.AudiobookFacets(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
}

// ---- soft-delete / restore / purge ----

func TestListSoftDeletedAudiobooks(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().GetSoftDeletedBooks(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]database.Book{{ID: "b1"}}, nil)
	c, w := newCtx("GET", "/audiobooks/soft-deleted", nil, nil)
	h.ListSoftDeletedAudiobooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestPurgeSoftDeletedAudiobooks(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().PurgeSoftDeletedBooks(mock.Anything, false, mock.Anything).
		Return(&audiobookspkg.PurgeResult{Purged: 1}, nil)
	c, w := newCtx("DELETE", "/audiobooks/purge-soft-deleted", nil, nil)
	h.PurgeSoftDeletedAudiobooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestRestoreAudiobook(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().RestoreAudiobook(mock.Anything, "b1").Return(&database.Book{ID: "b1"}, nil)
	c, w := newCtx("POST", "/audiobooks/b1/restore", nil, p("id", "b1"))
	h.RestoreAudiobook(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestRestoreAudiobook_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().RestoreAudiobook(mock.Anything, "x").Return(nil, errString("not found"))
	c, w := newCtx("POST", "/audiobooks/x/restore", nil, p("id", "x"))
	h.RestoreAudiobook(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestRescanAudiobook_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("x").Return(nil, errString("nope"))
	c, w := newCtx("POST", "/audiobooks/x/rescan", nil, p("id", "x"))
	h.RescanAudiobook(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestRescanAudiobook_NoFiles(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.store.EXPECT().GetBookFiles("b1").Return([]database.BookFile{}, nil)
	c, w := newCtx("POST", "/audiobooks/b1/rescan", nil, p("id", "b1"))
	h.RescanAudiobook(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

// ---- cover / get ----

func TestServeAudiobookCover_NoRootDir(t *testing.T) {
	// config.AppConfig.RootDir is empty in the test environment; SanitizeFilename
	// maps "" → "_" so the id check is bypassed and the handler returns 500 on the
	// unconfigured root_dir, matching the original behavior.
	h, _ := newHandler(t)
	c, w := newCtx("GET", "/audiobooks/b1/cover", nil, p("id", "b1"))
	h.ServeAudiobookCover(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
}

func TestGetAudiobook(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().GetAudiobook(mock.Anything, "b1").Return(&database.Book{ID: "b1", Title: "T"}, nil)
	c, w := newCtx("GET", "/audiobooks/b1", nil, p("id", "b1"))
	h.GetAudiobook(c)
	if w.Code != http.StatusOK || d.rec.enrichCalls != 1 {
		t.Fatalf("want 200 + enrich, got %d enrich=%d", w.Code, d.rec.enrichCalls)
	}
}

func TestGetAudiobook_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().GetAudiobook(mock.Anything, "x").Return(nil, errString("not found"))
	c, w := newCtx("GET", "/audiobooks/x", nil, p("id", "x"))
	h.GetAudiobook(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// ---- files / segments ----

func TestListAudiobookSegments(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.store.EXPECT().GetBookFiles("b1").Return([]database.BookFile{{ID: "f1", BookID: "b1"}}, nil)
	c, w := newCtx("GET", "/audiobooks/b1/segments", nil, p("id", "b1"))
	h.ListAudiobookSegments(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestListBookFiles(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookFiles("b1").Return([]database.BookFile{{ID: "f1", BookID: "b1"}}, nil)
	c, w := newCtx("GET", "/audiobooks/b1/files", nil, p("id", "b1"))
	h.ListBookFiles(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestPatchBookFile_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookFileByID("b1", "f1").Return(nil, nil)
	c, w := newCtx("PATCH", "/audiobooks/b1/files/f1", map[string]any{"skip_scan": true},
		gin.Params{{Key: "id", Value: "b1"}, {Key: "file_id", Value: "f1"}})
	h.PatchBookFile(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestPatchBookFile_Success(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookFileByID("b1", "f1").Return(&database.BookFile{ID: "f1"}, nil)
	d.store.EXPECT().UpsertBookFile(mock.Anything).Return(nil)
	c, w := newCtx("PATCH", "/audiobooks/b1/files/f1", map[string]any{"skip_scan": true},
		gin.Params{{Key: "id", Value: "b1"}, {Key: "file_id", Value: "f1"}})
	h.PatchBookFile(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestPatchBookFile_SetsDownloadHash(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookFileByID("b1", "f1").Return(&database.BookFile{ID: "f1"}, nil)
	d.store.EXPECT().UpsertBookFile(mock.MatchedBy(func(f *database.BookFile) bool {
		return f.DownloadHash == "abc123"
	})).Return(nil)
	c, w := newCtx("PATCH", "/audiobooks/b1/files/f1", map[string]any{"download_hash": "abc123"},
		gin.Params{{Key: "id", Value: "b1"}, {Key: "file_id", Value: "f1"}})
	h.PatchBookFile(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestExtractTrackInfo(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.store.EXPECT().GetBookFiles("b1").Return([]database.BookFile{}, nil)
	c, w := newCtx("POST", "/audiobooks/b1/extract-track-info", nil, p("id", "b1"))
	h.ExtractTrackInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestRelocateBookFiles_BadRequest(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.store.EXPECT().GetBookFiles("b1").Return([]database.BookFile{}, nil)
	c, w := newCtx("POST", "/audiobooks/b1/relocate", map[string]any{}, p("id", "b1"))
	h.RelocateBookFiles(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// TestRelocateBookFiles_RejectsDisallowedPath verifies the relocate target is
// gated by the allow-list: a clean absolute path outside the allowed roots
// (RootDir empty + default prefixes in tests) is rejected (go/path-injection).
func TestRelocateBookFiles_RejectsDisallowedPath(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.store.EXPECT().GetBookFiles("b1").Return([]database.BookFile{{ID: "s1", BookID: "b1"}}, nil)
	c, w := newCtx("POST", "/audiobooks/b1/relocate",
		map[string]any{"segment_id": "s1", "new_path": "/etc/cron.d/evil"}, p("id", "b1"))
	h.RelocateBookFiles(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not in an allowed directory") {
		t.Fatalf("expected allow-list rejection, got: %s", w.Body.String())
	}
}

func TestGetSegmentTags_SegmentNotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.store.EXPECT().GetBookFileByID("b1", "s1").Return(nil, nil)
	c, w := newCtx("GET", "/audiobooks/b1/segments/s1/tags", nil,
		gin.Params{{Key: "id", Value: "b1"}, {Key: "segmentId", Value: "s1"}})
	h.GetSegmentTags(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// ---- metadata history / undo / field states ----

func TestGetBookMetadataHistory(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookChangeHistory("b1", 100).Return([]database.MetadataChangeRecord{}, nil)
	c, w := newCtx("GET", "/audiobooks/b1/metadata-history", nil, p("id", "b1"))
	h.GetBookMetadataHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestGetAudiobookFieldStates(t *testing.T) {
	h, d := newHandler(t)
	d.rec.fieldStatesVal = map[string]any{"title": "locked"}
	c, w := newCtx("GET", "/audiobooks/b1/field-states", nil, p("id", "b1"))
	h.GetAudiobookFieldStates(c)
	if w.Code != http.StatusOK || d.rec.fieldStatesCalls != 1 {
		t.Fatalf("want 200 + 1 call, got %d calls=%d", w.Code, d.rec.fieldStatesCalls)
	}
}

func TestGetFieldMetadataHistory(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetMetadataChangeHistory("b1", "title", 50).Return([]database.MetadataChangeRecord{}, nil)
	c, w := newCtx("GET", "/audiobooks/b1/metadata-history/title", nil,
		gin.Params{{Key: "id", Value: "b1"}, {Key: "field", Value: "title"}})
	h.GetFieldMetadataHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestUndoMetadataChange_NoHistory(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetMetadataChangeHistory("b1", "title", 1).Return([]database.MetadataChangeRecord{}, nil)
	c, w := newCtx("POST", "/audiobooks/b1/metadata-history/title/undo", nil,
		gin.Params{{Key: "id", Value: "b1"}, {Key: "field", Value: "title"}})
	h.UndoMetadataChange(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestUndoLastApply_NoHistory(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookChangeHistory("b1", 50).Return([]database.MetadataChangeRecord{}, nil)
	c, w := newCtx("POST", "/audiobooks/b1/undo-last-apply", nil, p("id", "b1"))
	h.UndoLastApply(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestGetBookPathHistory(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookPathHistory("b1").Return([]database.BookPathChange{}, nil)
	c, w := newCtx("GET", "/audiobooks/b1/path-history", nil, p("id", "b1"))
	h.GetBookPathHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestGetAudiobookExternalIDs_NoStore(t *testing.T) {
	h, _ := newHandler(t)
	// rec.extIDStore is nil by default
	c, w := newCtx("GET", "/audiobooks/b1/external-ids", nil, p("id", "b1"))
	h.GetAudiobookExternalIDs(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestGetAudiobookExternalIDs_WithStore(t *testing.T) {
	h, d := newHandler(t)
	eid := audiobooksmocks.NewMockExternalIDStore(t)
	eid.EXPECT().GetExternalIDsForBook("b1").Return([]database.ExternalIDMapping{{Source: "itunes"}}, nil)
	d.rec.extIDStore = eid
	c, w := newCtx("GET", "/audiobooks/b1/external-ids", nil, p("id", "b1"))
	h.GetAudiobookExternalIDs(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

// ---- changelog / changes ----

func TestGetBookChangelog(t *testing.T) {
	h, d := newHandler(t)
	d.changelog.EXPECT().GetBookChangelog("b1").Return(nil, nil)
	c, w := newCtx("GET", "/audiobooks/b1/changelog", nil, p("id", "b1"))
	h.GetBookChangelog(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestGetBookChanges(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookChanges("b1").Return(nil, nil)
	c, w := newCtx("GET", "/audiobooks/b1/changes", nil, p("id", "b1"))
	h.GetBookChanges(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

// ---- tags ----

func TestGetAudiobookTags(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().GetAudiobookTags(mock.Anything, "b1", "", "").Return(map[string]any{"ok": true}, nil)
	c, w := newCtx("GET", "/audiobooks/b1/tags", nil, p("id", "b1"))
	h.GetAudiobookTags(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestGetAudiobookTags_BadSnapshot(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx("GET", "/audiobooks/b1/tags?snapshot_ts=notatime", nil, p("id", "b1"))
	h.GetAudiobookTags(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestListAllUserTags(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().ListAllUserTags().Return(nil, nil)
	c, w := newCtx("GET", "/tags", nil, nil)
	h.ListAllUserTags(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestGetBookUserTags(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().GetBookUserTags("b1").Return(nil, nil)
	c, w := newCtx("GET", "/audiobooks/b1/user-tags", nil, p("id", "b1"))
	h.GetBookUserTags(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestGetBookTagsDetailed(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookTagsDetailed("b1").Return(nil, nil)
	c, w := newCtx("GET", "/audiobooks/b1/tags-detailed", nil, p("id", "b1"))
	h.GetBookTagsDetailed(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestBatchUpdateTags(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().BatchUpdateUserTags([]string{"b1"}, []string{"new"}, []string{}).Return(1, nil)
	c, w := newCtx("POST", "/audiobooks/batch-tags",
		map[string]any{"book_ids": []string{"b1"}, "add_tags": []string{"new"}}, nil)
	h.BatchUpdateTags(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestBatchUpdateTags_MissingBookIDs(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx("POST", "/audiobooks/batch-tags", map[string]any{"add_tags": []string{"x"}}, nil)
	h.BatchUpdateTags(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// ---- alternative titles ----

func TestGetBookAlternativeTitles(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookAlternativeTitles("b1").Return(nil, nil)
	c, w := newCtx("GET", "/audiobooks/b1/alternative-titles", nil, p("id", "b1"))
	h.GetBookAlternativeTitles(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestAddBookAlternativeTitle_Success(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.store.EXPECT().AddBookAlternativeTitle("b1", "Alt", "user", "en").Return(nil)
	d.store.EXPECT().GetBookAlternativeTitles("b1").Return(nil, nil)
	c, w := newCtx("POST", "/audiobooks/b1/alternative-titles",
		map[string]any{"title": "Alt", "source": "user", "language": "en"}, p("id", "b1"))
	h.AddBookAlternativeTitle(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestAddBookAlternativeTitle_MissingTitle(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx("POST", "/audiobooks/b1/alternative-titles", map[string]any{}, p("id", "b1"))
	h.AddBookAlternativeTitle(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestAddBookAlternativeTitle_BookNotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(nil, nil)
	c, w := newCtx("POST", "/audiobooks/b1/alternative-titles",
		map[string]any{"title": "Alt"}, p("id", "b1"))
	h.AddBookAlternativeTitle(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestRemoveBookAlternativeTitle(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().RemoveBookAlternativeTitle("b1", "Alt").Return(nil)
	d.store.EXPECT().GetBookAlternativeTitles("b1").Return(nil, nil)
	c, w := newCtx("DELETE", "/audiobooks/b1/alternative-titles",
		map[string]any{"title": "Alt"}, p("id", "b1"))
	h.RemoveBookAlternativeTitle(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestRemoveBookAlternativeTitle_MissingTitle(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx("DELETE", "/audiobooks/b1/alternative-titles", map[string]any{}, p("id", "b1"))
	h.RemoveBookAlternativeTitle(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// ---- CRUD / batch ----

func TestUpdateAudiobook_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("x").Return(nil, nil)
	d.updater.EXPECT().UpdateAudiobook(mock.Anything, "x", mock.Anything).Return(nil, errString("not found"))
	c, w := newCtx("PUT", "/audiobooks/x", map[string]any{"title": "T"}, p("id", "x"))
	h.UpdateAudiobook(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestUpdateAudiobook_Success(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Old"}, nil)
	d.updater.EXPECT().UpdateAudiobook(mock.Anything, "b1", mock.Anything).
		Return(&database.Book{ID: "b1", Title: "New"}, nil)
	d.store.EXPECT().RecordMetadataChange(mock.Anything).Return(nil).Maybe()
	d.svc.EXPECT().InvalidateBookCaches().Return()
	d.writeBack.EXPECT().Enqueue("b1").Return()
	c, w := newCtx("PUT", "/audiobooks/b1", map[string]any{"title": "New"}, p("id", "b1"))
	h.UpdateAudiobook(c)
	if w.Code != http.StatusOK || d.rec.enrichCalls != 1 {
		t.Fatalf("want 200 + enrich, got %d enrich=%d (%s)", w.Code, d.rec.enrichCalls, w.Body.String())
	}
}

func TestUpdateAudiobook_ProtectedPathSkipsWriteBack(t *testing.T) {
	h, d := newHandler(t)
	d.rec.protectedReturn = true // isProtectedPath returns true → write-back skipped
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Old"}, nil)
	d.updater.EXPECT().UpdateAudiobook(mock.Anything, "b1", mock.Anything).
		Return(&database.Book{ID: "b1", Title: "New", FilePath: "/protected/book.m4b"}, nil)
	d.store.EXPECT().RecordMetadataChange(mock.Anything).Return(nil).Maybe()
	// The write-back tagMap gets "title" but no "artist"/"album_artist", so the
	// handler probes the author/narrator join tables; return ≤1 each so the
	// multi-value join branch is skipped. (Protected-path short-circuits before
	// any metadata.WriteMetadataToFile / SetLastWrittenAt.)
	d.store.EXPECT().GetBookAuthors("b1").Return([]database.BookAuthor{{AuthorID: 1}}, nil).Maybe()
	d.store.EXPECT().GetBookNarrators("b1").Return([]database.BookNarrator{}, nil).Maybe()
	d.svc.EXPECT().InvalidateBookCaches().Return()
	d.writeBack.EXPECT().Enqueue("b1").Return()
	c, w := newCtx("PUT", "/audiobooks/b1", map[string]any{"title": "New"}, p("id", "b1"))
	h.UpdateAudiobook(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	// isProtectedPath must have been consulted with the updated file path, and the
	// protected branch must have prevented a metadata.WriteMetadataToFile call
	// (asserted implicitly: no SetLastWrittenAt expectation was registered).
	if len(d.rec.protectedPaths) != 1 || d.rec.protectedPaths[0] != "/protected/book.m4b" {
		t.Fatalf("expected isProtectedPath called once with the file path, got %v", d.rec.protectedPaths)
	}
}

func TestDeleteAudiobook(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().DeleteAudiobook(mock.Anything, "b1", mock.Anything).Return(map[string]any{"deleted": true}, nil)
	c, w := newCtx("DELETE", "/audiobooks/b1", nil, p("id", "b1"))
	h.DeleteAudiobook(c)
	if w.Code != http.StatusOK || len(d.rec.publishedEvents) != 1 {
		t.Fatalf("want 200 + 1 event, got %d events=%d", w.Code, len(d.rec.publishedEvents))
	}
}

func TestDeleteAudiobook_Conflict(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().DeleteAudiobook(mock.Anything, "b1", mock.Anything).Return(nil, errString("already soft deleted"))
	c, w := newCtx("DELETE", "/audiobooks/b1", nil, p("id", "b1"))
	h.DeleteAudiobook(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", w.Code)
	}
}

func TestBatchUpdateAudiobooks(t *testing.T) {
	h, d := newHandler(t)
	d.batchSvc.EXPECT().UpdateAudiobooks(mock.Anything).Return(&batch.BatchResponse{})
	c, w := newCtx("POST", "/audiobooks/batch", map[string]any{"ids": []string{}}, nil)
	h.BatchUpdateAudiobooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestBatchOperations_NoOps(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx("POST", "/audiobooks/batch-operations", map[string]any{"operations": []any{}}, nil)
	h.BatchOperations(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestBatchOperations_Success(t *testing.T) {
	h, d := newHandler(t)
	d.batchSvc.EXPECT().ExecuteOperations(mock.Anything).Return(&batch.BatchResponse{})
	c, w := newCtx("POST", "/audiobooks/batch-operations",
		map[string]any{"operations": []map[string]any{{"id": "b1", "action": "update"}}}, nil)
	h.BatchOperations(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
}
