// file: internal/server/handlers/entities/handler_test.go
// version: 1.6.0
// guid: 163bc668-0761-43eb-9d85-f4983e8b014b
// last-edited: 2026-08-24

package entities_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	entitiesmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/work"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// deps bundles the mocks + real caches used to construct a Handler under test.
type deps struct {
	store        *entitiesmocks.MockEntitiesStore
	workSvc      *entitiesmocks.MockWorkService
	authorSeries *entitiesmocks.MockAuthorSeriesService
	registry     *entitiesmocks.MockOperationsRegistry
	authorsCache *cache.Cache[*audiobooks.AuthorWithCountListResponse]
	seriesCache  *cache.Cache[*audiobooks.SeriesWithCountsResponse]
	dedupCache   *cache.Cache[gin.H]
	enrichCalls  int
	// seriesRefCounts backs the UNFILTERED series reference counter the delete
	// handlers consult. nil means "no series is referenced by anything", which
	// is what every test that does not care about deletion wants. Set it to
	// assert that a referenced series survives.
	seriesRefCounts map[int]int
	// authorRefCounts is the author-side twin, backing the counter DeleteAuthor
	// and BulkDeleteAuthors guard on. Same nil semantics.
	authorRefCounts map[int]int
}

// entitiesStoreWithRefs adds GetAllSeriesBookRefCounts and
// GetAllAuthorBookRefCounts to the generated EntitiesStore mock.
// SeriesBookRefStore and AuthorBookRefStore are deliberately kept out of the
// store interfaces so widening them does not force every implementation and
// generated mock to grow, and the delete handlers reach them via
// database.AsSeriesBookRefStore / database.AsAuthorBookRefStore and FAIL CLOSED
// when they are missing — so a bare generated mock cannot exercise them at all
// (which is exactly what TestDeleteAuthor_FailsClosedWhenStoreLacksRefCounter
// and its bulk twin rely on).
//
// It reads through to deps so a test can set seriesRefCounts after the handler
// has already been constructed.
type entitiesStoreWithRefs struct {
	*entitiesmocks.MockEntitiesStore
	d *deps
}

func (s entitiesStoreWithRefs) GetAllSeriesBookRefCounts() (map[int]int, error) {
	if s.d.seriesRefCounts == nil {
		return map[int]int{}, nil
	}
	return s.d.seriesRefCounts, nil
}

func (s entitiesStoreWithRefs) GetAllAuthorBookRefCounts() (map[int]int, error) {
	if s.d.authorRefCounts == nil {
		return map[int]int{}, nil
	}
	return s.d.authorRefCounts, nil
}

// newHandler builds a Handler backed by fresh mocks and real (non-nil) caches.
// The enrichBooks stub returns one fixed entry per input book so the items/count
// JSON shape is exercised without depending on server-private enrichment.
func newHandler(t *testing.T) (*entities.Handler, *deps) {
	t.Helper()
	d := &deps{
		store:        entitiesmocks.NewMockEntitiesStore(t),
		workSvc:      entitiesmocks.NewMockWorkService(t),
		authorSeries: entitiesmocks.NewMockAuthorSeriesService(t),
		registry:     entitiesmocks.NewMockOperationsRegistry(t),
		authorsCache: cache.NewWithLimit[*audiobooks.AuthorWithCountListResponse]("authors-test", time.Hour, 1),
		seriesCache:  cache.NewWithLimit[*audiobooks.SeriesWithCountsResponse]("series-test", time.Hour, 1),
		dedupCache:   cache.NewWithLimit[gin.H]("dedup-test", time.Hour, 16),
	}
	enrich := func(books []database.Book) []any {
		d.enrichCalls++
		out := make([]any, len(books))
		for i := range books {
			out[i] = gin.H{"id": books[i].ID}
		}
		return out
	}
	h := entities.New(
		entitiesStoreWithRefs{MockEntitiesStore: d.store, d: d},
		d.workSvc,
		d.authorSeries,
		d.registry,
		d.authorsCache,
		d.seriesCache,
		d.dedupCache,
		enrich,
	)
	return h, d
}

// newHandlerWithoutRefCounters builds a Handler over the BARE generated mock,
// i.e. a store that implements neither SeriesBookRefStore nor
// AuthorBookRefStore. That is the shape a narrow test double or an
// unsupported backend presents in production behind the Bleve decorator, and
// the delete handlers must FAIL CLOSED on it rather than fall back to the
// filtered getter. newHandler cannot express this: it always wraps the mock.
func newHandlerWithoutRefCounters(t *testing.T) (*entities.Handler, *deps) {
	t.Helper()
	d := &deps{
		store:        entitiesmocks.NewMockEntitiesStore(t),
		workSvc:      entitiesmocks.NewMockWorkService(t),
		authorSeries: entitiesmocks.NewMockAuthorSeriesService(t),
		registry:     entitiesmocks.NewMockOperationsRegistry(t),
		authorsCache: cache.NewWithLimit[*audiobooks.AuthorWithCountListResponse]("authors-test", time.Hour, 1),
		seriesCache:  cache.NewWithLimit[*audiobooks.SeriesWithCountsResponse]("series-test", time.Hour, 1),
		dedupCache:   cache.NewWithLimit[gin.H]("dedup-test", time.Hour, 16),
	}
	h := entities.New(
		d.store,
		d.workSvc,
		d.authorSeries,
		d.registry,
		d.authorsCache,
		d.seriesCache,
		d.dedupCache,
		func(books []database.Book) []any { return make([]any, len(books)) },
	)
	return h, d
}

// newCtx builds a gin test context with optional JSON body + path params.
func newCtx(method, path, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = params
	return c, w
}

func idParam(v string) gin.Params { return gin.Params{{Key: "id", Value: v}} }

// ── Works ──────────────────────────────────────────────────────────────────

func TestListWorks(t *testing.T) {
	h, d := newHandler(t)
	d.workSvc.EXPECT().ListWorks().Return(&work.WorkListResponse{Items: []database.Work{{ID: "w1"}}, Count: 1}, nil)
	c, w := newCtx(http.MethodGet, "/works", "", nil)
	h.ListWorks(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListWorks_Error(t *testing.T) {
	h, d := newHandler(t)
	d.workSvc.EXPECT().ListWorks().Return(nil, assert.AnError)
	c, w := newCtx(http.MethodGet, "/works", "", nil)
	h.ListWorks(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestCreateWork(t *testing.T) {
	h, d := newHandler(t)
	d.workSvc.EXPECT().CreateWork(mock.Anything).Return(&database.Work{ID: "w1", Title: "T"}, nil)
	c, w := newCtx(http.MethodPost, "/works", `{"title":"T"}`, nil)
	h.CreateWork(c)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestCreateWork_BadJSON(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx(http.MethodPost, "/works", `{bad`, nil)
	h.CreateWork(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetWork(t *testing.T) {
	h, d := newHandler(t)
	d.workSvc.EXPECT().GetWork("w1").Return(&database.Work{ID: "w1"}, nil)
	c, w := newCtx(http.MethodGet, "/works/w1", "", idParam("w1"))
	h.GetWork(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGetWork_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.workSvc.EXPECT().GetWork("w1").Return(nil, assert.AnError)
	c, w := newCtx(http.MethodGet, "/works/w1", "", idParam("w1"))
	h.GetWork(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestUpdateWork(t *testing.T) {
	h, d := newHandler(t)
	d.workSvc.EXPECT().UpdateWork("w1", mock.Anything).Return(&database.Work{ID: "w1", Title: "T"}, nil)
	c, w := newCtx(http.MethodPut, "/works/w1", `{"title":"T"}`, idParam("w1"))
	h.UpdateWork(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestUpdateWork_EmptyTitle(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx(http.MethodPut, "/works/w1", `{"title":"  "}`, idParam("w1"))
	h.UpdateWork(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateWork_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.workSvc.EXPECT().UpdateWork("w1", mock.Anything).Return(nil, errString("work not found"))
	c, w := newCtx(http.MethodPut, "/works/w1", `{"title":"T"}`, idParam("w1"))
	h.UpdateWork(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteWork(t *testing.T) {
	h, d := newHandler(t)
	d.workSvc.EXPECT().DeleteWork("w1").Return(nil)
	c, w := newCtx(http.MethodDelete, "/works/w1", "", idParam("w1"))
	h.DeleteWork(c)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestDeleteWork_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.workSvc.EXPECT().DeleteWork("w1").Return(errString("work not found"))
	c, w := newCtx(http.MethodDelete, "/works/w1", "", idParam("w1"))
	h.DeleteWork(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestListWorkBooks(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBooksByWorkID("w1").Return([]database.Book{{ID: "b1"}}, nil)
	c, w := newCtx(http.MethodGet, "/works/w1/books", "", idParam("w1"))
	h.ListWorkBooks(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListWork(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAllWorks().Return([]database.Work{{ID: "w1", Title: "T"}}, nil)
	d.store.EXPECT().GetAllWorkBookCounts().Return(map[string]int{"w1": 2}, nil)
	d.store.EXPECT().GetBooksByWorkID("w1").Return([]database.Book{{ID: "b1"}}, nil)
	c, w := newCtx(http.MethodGet, "/work", "", nil)
	h.ListWork(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGetWorkStats(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAllWorks().Return([]database.Work{{ID: "w1"}, {ID: "w2"}}, nil)
	d.store.EXPECT().GetAllWorkBookCounts().Return(map[string]int{"w1": 3, "w2": 1}, nil)
	c, w := newCtx(http.MethodGet, "/work/stats", "", nil)
	h.GetWorkStats(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── Authors ──────────────────────────────────────────────────────────────

func TestListAuthors(t *testing.T) {
	h, d := newHandler(t)
	d.authorSeries.EXPECT().ListAuthorsWithCounts().Return(&audiobooks.AuthorWithCountListResponse{Count: 1}, nil)
	c, w := newCtx(http.MethodGet, "/authors", "", nil)
	h.ListAuthors(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListAuthors_Cached(t *testing.T) {
	h, d := newHandler(t)
	d.authorsCache.Set("all", &audiobooks.AuthorWithCountListResponse{Count: 7})
	c, w := newCtx(http.MethodGet, "/authors", "", nil)
	h.ListAuthors(c) // no service call expected
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestCountAuthors(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().CountAuthors().Return(42, nil)
	c, w := newCtx(http.MethodGet, "/authors/count", "", nil)
	h.CountAuthors(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRenameAuthor(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().UpdateAuthorName(5, "New Name").Return(nil)
	c, w := newCtx(http.MethodPut, "/authors/5/name", `{"name":"New Name"}`, idParam("5"))
	h.RenameAuthor(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRenameAuthor_BadID(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx(http.MethodPut, "/authors/x/name", `{"name":"N"}`, idParam("x"))
	h.RenameAuthor(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSplitCompositeAuthor(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAuthorByID(5).Return(&database.Author{ID: 5, Name: "A / B"}, nil)
	d.store.EXPECT().GetAuthorByName("A").Return(nil, errString("not found"))
	d.store.EXPECT().CreateAuthor("A").Return(&database.Author{ID: 10, Name: "A"}, nil)
	d.store.EXPECT().GetAuthorByName("B").Return(nil, errString("not found"))
	d.store.EXPECT().CreateAuthor("B").Return(&database.Author{ID: 11, Name: "B"}, nil)
	d.store.EXPECT().GetBooksByAuthorIDWithRoleCore(5).Return([]database.BookCore{}, nil)
	d.store.EXPECT().DeleteAuthor(5).Return(nil)
	// Provide explicit names so the split is deterministic (not dependent on the
	// dedup auto-detect heuristic).
	c, w := newCtx(http.MethodPost, "/authors/5/split", `{"names":["A","B"]}`, idParam("5"))
	h.SplitCompositeAuthor(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSplitCompositeAuthor_NotComposite(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAuthorByID(5).Return(&database.Author{ID: 5, Name: "Solo"}, nil)
	c, w := newCtx(http.MethodPost, "/authors/5/split", `{}`, idParam("5"))
	h.SplitCompositeAuthor(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestMergeAuthors(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAuthorByID(1).Return(&database.Author{ID: 1, Name: "Keep"}, nil)
	d.registry.EXPECT().EnqueueOp(mock.Anything, "entities.author-merge", mock.Anything).Return("v2op1", nil)
	c, w := newCtx(http.MethodPost, "/authors/merge", `{"keep_id":1,"merge_ids":[2,3]}`, nil)
	h.MergeAuthors(c)
	assert.Equal(t, http.StatusAccepted, w.Code)

	// The id in the body must be the id EnqueueOp returned. The UI feeds it
	// straight to getOperationStatus, which reads /operations/v2/:id — returning
	// anything else 404s there and surfaces as "Failed to merge" for a merge
	// that actually succeeded.
	assert.Contains(t, w.Body.String(), `"id":"v2op1"`)
}

func TestMergeAuthors_EmptyMergeIDs(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx(http.MethodPost, "/authors/merge", `{"keep_id":1,"merge_ids":[]}`, nil)
	h.MergeAuthors(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestDeleteAuthor is the POSITIVE CONTROL for the unfiltered guard: an author
// referenced by nothing must STILL be deletable. Without it, a guard that
// refuses every delete would pass every negative test below while silently
// turning DELETE /authors/:id into a no-op.
func TestDeleteAuthor(t *testing.T) {
	h, d := newHandler(t)
	// Referenced by nothing in any state.
	d.store.EXPECT().DeleteAuthor(5).Return(nil)
	c, w := newCtx(http.MethodDelete, "/authors/5", "", idParam("5"))
	h.DeleteAuthor(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestDeleteAuthor_HasBooks(t *testing.T) {
	h, d := newHandler(t)
	// One reference refuses the delete. It deliberately does not matter whether
	// that book is live, trashed, a non-primary version, or attached only
	// through the book_authors junction — blindness to those is the whole bug:
	// the author's name lives only in the row being deleted.
	// No DeleteAuthor expectation: calling it at all fails the mock.
	d.authorRefCounts = map[int]int{5: 1}
	c, w := newCtx(http.MethodDelete, "/authors/5", "", idParam("5"))
	h.DeleteAuthor(c)
	assert.Equal(t, http.StatusConflict, w.Code)
}

// TestDeleteAuthor_FailsClosedWhenStoreLacksRefCounter pins the fail-closed
// contract. A store that cannot answer the UNFILTERED question must make the
// delete refuse — never fall back to the filtered getter, because that fallback
// IS the bug and it fails silently, reporting success while stranding books.
//
// It is built WITHOUT the entitiesStoreWithRefs wrapper on purpose: newHandler
// always supplies the capability, so a test written through it could never
// reach this path.
func TestDeleteAuthor_FailsClosedWhenStoreLacksRefCounter(t *testing.T) {
	h, _ := newHandlerWithoutRefCounters(t)
	// No DeleteAuthor expectation: a fallback to the filtered count would call
	// it and fail the mock.
	c, w := newCtx(http.MethodDelete, "/authors/5", "", idParam("5"))
	h.DeleteAuthor(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestBulkDeleteAuthors_FailsClosedWhenStoreLacksRefCounter — the bulk path is
// an independent call site and gets its own assertion.
func TestBulkDeleteAuthors_FailsClosedWhenStoreLacksRefCounter(t *testing.T) {
	h, _ := newHandlerWithoutRefCounters(t)
	c, w := newCtx(http.MethodPost, "/authors/bulk-delete", `{"ids":[1,2]}`, nil)
	h.BulkDeleteAuthors(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestBulkDeleteAuthors(t *testing.T) {
	h, d := newHandler(t)
	d.authorRefCounts = map[int]int{2: 1} // author 1 unreferenced, author 2 referenced
	d.store.EXPECT().DeleteAuthor(1).Return(nil)
	// No DeleteAuthor(2) expectation: calling it at all fails the mock.
	c, w := newCtx(http.MethodPost, "/authors/bulk-delete", `{"ids":[1,2]}`, nil)
	h.BulkDeleteAuthors(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"deleted":1`)
	assert.Contains(t, w.Body.String(), `"skipped":1`)
}

// TestBulkDeleteAuthors_SkipsReferencedAuthor is the regression case: an author
// the display counter calls empty because its only book is trashed,
// non-primary, or a junction-only co-author credit must NOT be deleted.
func TestBulkDeleteAuthors_SkipsReferencedAuthor(t *testing.T) {
	h, d := newHandler(t)
	d.authorRefCounts = map[int]int{1: 1}
	// No DeleteAuthor expectation: calling it at all fails the mock.
	c, w := newCtx(http.MethodPost, "/authors/bulk-delete", `{"ids":[1]}`, nil)
	h.BulkDeleteAuthors(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"skipped":1`)
	assert.Contains(t, w.Body.String(), `"deleted":0`)
}

func TestBulkDeleteAuthors_BadJSON(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx(http.MethodPost, "/authors/bulk-delete", `{`, nil)
	h.BulkDeleteAuthors(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetAuthorBooks(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBooksByAuthorIDCore(5).Return([]database.BookCore{{ID: "b1"}, {ID: "b2"}}, nil)
	c, w := newCtx(http.MethodGet, "/authors/5/books", "", idParam("5"))
	h.GetAuthorBooks(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, d.enrichCalls)
}

func TestGetAuthorAliases(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAuthorAliases(5).Return([]database.AuthorAlias{{ID: 1}}, nil)
	c, w := newCtx(http.MethodGet, "/authors/5/aliases", "", idParam("5"))
	h.GetAuthorAliases(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestCreateAuthorAlias(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().CreateAuthorAlias(5, "Pen Name", "alias").Return(&database.AuthorAlias{ID: 9}, nil)
	c, w := newCtx(http.MethodPost, "/authors/5/aliases", `{"alias_name":"Pen Name"}`, idParam("5"))
	h.CreateAuthorAlias(c)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestCreateAuthorAlias_MissingName(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx(http.MethodPost, "/authors/5/aliases", `{"alias_name":""}`, idParam("5"))
	h.CreateAuthorAlias(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeleteAuthorAlias(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().DeleteAuthorAlias(9).Return(nil)
	c, w := newCtx(http.MethodDelete, "/authors/5/aliases/9", "", gin.Params{{Key: "aliasId", Value: "9"}})
	h.DeleteAuthorAlias(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestReclassifyAuthorAsNarrator(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAuthorByID(5).Return(&database.Author{ID: 5, Name: "Reader"}, nil)
	d.store.EXPECT().GetNarratorByName("Reader").Return(nil, errString("not found"))
	d.store.EXPECT().CreateNarrator("Reader").Return(&database.Narrator{ID: 3, Name: "Reader"}, nil)
	d.store.EXPECT().GetBooksByAuthorIDWithRoleCore(5).Return([]database.BookCore{}, nil)
	d.store.EXPECT().DeleteAuthor(5).Return(nil)
	c, w := newCtx(http.MethodPost, "/authors/5/reclassify-as-narrator", "", idParam("5"))
	h.ReclassifyAuthorAsNarrator(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestResolveProductionAuthor_NotProduction(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAuthorByID(5).Return(&database.Author{ID: 5, Name: "Jane Real Author"}, nil)
	c, w := newCtx(http.MethodPost, "/authors/5/resolve-production", "", idParam("5"))
	h.ResolveProductionAuthor(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestResolveProductionAuthor_NotFound(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAuthorByID(5).Return(nil, errString("missing"))
	c, w := newCtx(http.MethodPost, "/authors/5/resolve-production", "", idParam("5"))
	h.ResolveProductionAuthor(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ── Series ──────────────────────────────────────────────────────────────

func TestCountSeries(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().CountSeries().Return(11, nil)
	c, w := newCtx(http.MethodGet, "/series/count", "", nil)
	h.CountSeries(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListSeries(t *testing.T) {
	h, d := newHandler(t)
	d.authorSeries.EXPECT().ListSeriesWithCounts().Return(&audiobooks.SeriesWithCountsResponse{Count: 2}, nil)
	c, w := newCtx(http.MethodGet, "/series", "", nil)
	h.ListSeries(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListSeries_Cached(t *testing.T) {
	h, d := newHandler(t)
	d.seriesCache.Set("all", &audiobooks.SeriesWithCountsResponse{Count: 9})
	c, w := newCtx(http.MethodGet, "/series", "", nil)
	h.ListSeries(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGetSeriesBooks(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBooksBySeriesIDCore(5).Return([]database.BookCore{{ID: "b1"}}, nil)
	c, w := newCtx(http.MethodGet, "/series/5/books", "", idParam("5"))
	h.GetSeriesBooks(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, d.enrichCalls)
}

func TestRenameSeries(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().UpdateSeriesName(5, "New").Return(nil)
	d.store.EXPECT().GetSeriesByID(5).Return(&database.Series{ID: 5, Name: "New"}, nil)
	c, w := newCtx(http.MethodPut, "/series/5/name", `{"name":"New"}`, idParam("5"))
	h.RenameSeries(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRenameSeries_BadID(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx(http.MethodPut, "/series/0/name", `{"name":"N"}`, idParam("0"))
	h.RenameSeries(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSplitSeries(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetSeriesByID(5).Return(&database.Series{ID: 5, Name: "S"}, nil)
	d.store.EXPECT().CreateSeries("S (Split)", (*int)(nil)).Return(&database.Series{ID: 6, Name: "S (Split)"}, nil)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", SeriesID: new(5)}, nil)
	d.store.EXPECT().UpdateBook("b1", mock.Anything).Return(&database.Book{ID: "b1"}, nil)
	c, w := newCtx(http.MethodPost, "/series/5/split", `{"book_ids":["b1"]}`, idParam("5"))
	h.SplitSeries(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSplitSeries_EmptyBookIDs(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx(http.MethodPost, "/series/5/split", `{"book_ids":[]}`, idParam("5"))
	h.SplitSeries(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeleteEmptySeries(t *testing.T) {
	h, d := newHandler(t)
	// Referenced by nothing in any state.
	d.store.EXPECT().DeleteSeries(5).Return(nil)
	c, w := newCtx(http.MethodDelete, "/series/5", "", idParam("5"))
	h.DeleteEmptySeries(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestDeleteEmptySeries_HasBooks(t *testing.T) {
	h, d := newHandler(t)
	// One reference refuses the delete. It deliberately does not matter whether
	// that book is live, trashed or a non-primary version — blindness to the
	// last two is what stranded 13,322 books behind deleted series.
	d.seriesRefCounts = map[int]int{5: 1}
	c, w := newCtx(http.MethodDelete, "/series/5", "", idParam("5"))
	h.DeleteEmptySeries(c)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestBulkDeleteSeries(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().DeleteSeries(1).Return(nil)
	c, w := newCtx(http.MethodPost, "/series/bulk-delete", `{"ids":[1]}`, nil)
	h.BulkDeleteSeries(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestBulkDeleteSeries_SkipsReferencedSeries is the regression case: a series
// the display counter calls empty because its only book is trashed or a
// non-primary version must NOT be deleted.
func TestBulkDeleteSeries_SkipsReferencedSeries(t *testing.T) {
	h, d := newHandler(t)
	d.seriesRefCounts = map[int]int{1: 1}
	// No DeleteSeries expectation: calling it at all fails the mock.
	c, w := newCtx(http.MethodPost, "/series/bulk-delete", `{"ids":[1]}`, nil)
	h.BulkDeleteSeries(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"skipped":1`)
	assert.Contains(t, w.Body.String(), `"deleted":0`)
}

func TestUpdateSeriesName(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().UpdateSeriesName(5, "New").Return(nil)
	d.store.EXPECT().GetSeriesByID(5).Return(&database.Series{ID: 5, Name: "New"}, nil)
	c, w := newCtx(http.MethodPatch, "/series/5", `{"name":"New"}`, idParam("5"))
	h.UpdateSeriesName(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestUpdateSeriesName_BadID(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx(http.MethodPatch, "/series/x", `{"name":"N"}`, idParam("x"))
	h.UpdateSeriesName(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── Narrators ──────────────────────────────────────────────────────────────

func TestListNarrators(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().ListNarrators().Return([]database.Narrator{{ID: 1}}, nil)
	c, w := newCtx(http.MethodGet, "/narrators", "", nil)
	h.ListNarrators(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestCountNarrators(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().ListNarrators().Return([]database.Narrator{{ID: 1}, {ID: 2}}, nil)
	c, w := newCtx(http.MethodGet, "/narrators/count", "", nil)
	h.CountNarrators(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListAudiobookNarrators(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookNarrators("b1").Return([]database.BookNarrator{{BookID: "b1"}}, nil)
	c, w := newCtx(http.MethodGet, "/audiobooks/b1/narrators", "", idParam("b1"))
	h.ListAudiobookNarrators(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSetAudiobookNarrators(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().SetBookNarrators("b1", mock.Anything).Return(nil)
	c, w := newCtx(http.MethodPut, "/audiobooks/b1/narrators", `[{"book_id":"b1","narrator_id":3}]`, idParam("b1"))
	h.SetAudiobookNarrators(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSetAudiobookNarrators_BadJSON(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx(http.MethodPut, "/audiobooks/b1/narrators", `not-json`, idParam("b1"))
	h.SetAudiobookNarrators(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── helpers ──────────────────────────────────────────────────────────────

type errString string

func (e errString) Error() string { return string(e) }

//go:fix inline
func intptr(i int) *int { return new(i) }

// ── Authors paging ───────────────────────────────────────────────────────
//
// 🔴 GET /authors ignored limit and offset entirely: on the production library
// `?limit=5` returned all 9,203 authors — about 765 KB — exactly like
// `?limit=1000`. Every page view paid for the whole list.

// authorsFixture builds a response with n synthetic authors.
func authorsFixture(n int) *audiobooks.AuthorWithCountListResponse {
	items := make([]audiobooks.AuthorWithCount, n)
	for i := range items {
		items[i].ID = i + 1
		items[i].Name = fmt.Sprintf("Author %03d", i+1)
	}
	return &audiobooks.AuthorWithCountListResponse{Items: items, Count: n}
}

func decodeAuthors(t *testing.T, w *httptest.ResponseRecorder) (items []map[string]any, count int) {
	t.Helper()
	var body struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Count int              `json:"count"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Data.Items, body.Data.Count
}

func TestListAuthors_HonoursLimit(t *testing.T) {
	h, d := newHandler(t)
	d.authorsCache.Set("all", authorsFixture(50))
	c, w := newCtx(http.MethodGet, "/authors?limit=5", "", nil)
	h.ListAuthors(c)
	assert.Equal(t, http.StatusOK, w.Code)

	items, count := decodeAuthors(t, w)
	assert.Len(t, items, 5, "limit=5 must return 5 authors, not the whole list")
	assert.Equal(t, 50, count, "count must stay the FULL total so a client knows how much is left")
	assert.Equal(t, "Author 001", items[0]["name"])
}

func TestListAuthors_HonoursOffset(t *testing.T) {
	h, d := newHandler(t)
	d.authorsCache.Set("all", authorsFixture(50))
	c, w := newCtx(http.MethodGet, "/authors?limit=3&offset=10", "", nil)
	h.ListAuthors(c)

	items, count := decodeAuthors(t, w)
	require.Len(t, items, 3)
	assert.Equal(t, 50, count)
	assert.Equal(t, "Author 011", items[0]["name"], "offset=10 must skip the first ten")
}

// 🔑 BACKWARD COMPATIBILITY. The current UI fetches this endpoint with no
// parameters and expects every author. Defaulting to a page size here would
// silently truncate it, so an absent limit must still return the whole list.
func TestListAuthors_NoParamsStillReturnsEverything(t *testing.T) {
	h, d := newHandler(t)
	d.authorsCache.Set("all", authorsFixture(50))
	c, w := newCtx(http.MethodGet, "/authors", "", nil)
	h.ListAuthors(c)

	items, count := decodeAuthors(t, w)
	assert.Len(t, items, 50, "an unpaged request must still return everything")
	assert.Equal(t, 50, count)
}

// An offset past the end is an empty page, not a panic or a wrapped slice.
func TestListAuthors_OffsetBeyondEndIsEmpty(t *testing.T) {
	h, d := newHandler(t)
	d.authorsCache.Set("all", authorsFixture(10))
	c, w := newCtx(http.MethodGet, "/authors?offset=999&limit=5", "", nil)
	h.ListAuthors(c)

	items, count := decodeAuthors(t, w)
	assert.Empty(t, items)
	assert.Equal(t, 10, count)
}

// A limit larger than the set returns the set, not a slice-out-of-range panic.
func TestListAuthors_LimitBeyondEndIsClamped(t *testing.T) {
	h, d := newHandler(t)
	d.authorsCache.Set("all", authorsFixture(4))
	c, w := newCtx(http.MethodGet, "/authors?limit=100", "", nil)
	h.ListAuthors(c)

	items, _ := decodeAuthors(t, w)
	assert.Len(t, items, 4)
}

// Garbage values are ignored rather than erroring, matching the other list
// endpoints — a bad limit must not turn a working page into a 400.
func TestListAuthors_IgnoresUnparseableParams(t *testing.T) {
	h, d := newHandler(t)
	d.authorsCache.Set("all", authorsFixture(6))
	c, w := newCtx(http.MethodGet, "/authors?limit=abc&offset=-3", "", nil)
	h.ListAuthors(c)
	assert.Equal(t, http.StatusOK, w.Code)
	items, _ := decodeAuthors(t, w)
	assert.Len(t, items, 6)
}

// TestSplitCompositeAuthor_RepointsPrimaryAuthorID pins the fix for the dangling
// book.AuthorID leak.
//
// TestSplitCompositeAuthor above returns an EMPTY book list, so it exercises the
// junction rewrite and the DeleteAuthor and nothing in between -- which is
// exactly why this shipped. The bug only exists for books whose PRIMARY author is
// the composite, and a zero-book fixture has none. Any future edit that drops the
// repoint will still pass that test; it must fail this one.
//
// DeleteAuthor sweeps book_authors but never touches book.AuthorID, so without
// the repoint every book here keeps a pointer into a deleted row. On the memdb
// list path that renders as NO AUTHOR (GetAuthorsByIDs omits the miss and
// service_query.go leaves AuthorName nil); on the full-row path it renders the
// stale name. Same book, two answers.
func TestSplitCompositeAuthor_RepointsPrimaryAuthorID(t *testing.T) {
	h, d := newHandler(t)
	composite := 5

	d.store.EXPECT().GetAuthorByID(5).Return(&database.Author{ID: 5, Name: "A / B"}, nil)
	d.store.EXPECT().GetAuthorByName("A").Return(nil, errString("not found"))
	d.store.EXPECT().CreateAuthor("A").Return(&database.Author{ID: 10, Name: "A"}, nil)
	d.store.EXPECT().GetAuthorByName("B").Return(nil, errString("not found"))
	d.store.EXPECT().CreateAuthor("B").Return(&database.Author{ID: 11, Name: "B"}, nil)

	d.store.EXPECT().GetBooksByAuthorIDWithRoleCore(5).
		Return([]database.BookCore{{ID: "b1", AuthorID: &composite}}, nil)
	d.store.EXPECT().GetBookAuthors("b1").
		Return([]database.BookAuthor{{BookID: "b1", AuthorID: 5, Role: "author"}}, nil)
	d.store.EXPECT().SetBookAuthors("b1", mock.Anything).Return(nil)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{
		ID:       "b1",
		AuthorID: &composite,
		Author:   &database.Author{ID: 5, Name: "A / B"},
	}, nil)

	var wrote *database.Book
	d.store.EXPECT().UpdateBook("b1", mock.Anything).
		RunAndReturn(func(_ string, b *database.Book) (*database.Book, error) {
			wrote = b
			return b, nil
		})
	d.store.EXPECT().DeleteAuthor(5).Return(nil)

	c, w := newCtx(http.MethodPost, "/authors/5/split", `{"names":["A","B"]}`, idParam("5"))
	h.SplitCompositeAuthor(c)
	assert.Equal(t, http.StatusOK, w.Code)

	require.NotNil(t, wrote,
		"the book row must be written: leaving AuthorID on the about-to-be-deleted "+
			"composite is the leak itself")
	require.NotNil(t, wrote.AuthorID, "AuthorID must not be cleared")
	assert.Equal(t, 10, *wrote.AuthorID,
		"AuthorID must move to the first individual author, not stay on the deleted composite")
	require.NotNil(t, wrote.Author,
		"Author must be written alongside AuthorID -- UpdateBook's preserve-on-nil guard "+
			"(pebble_store.go:2407) would otherwise keep the stale composite snapshot")
	assert.Equal(t, 10, wrote.Author.ID,
		"the denormalized Author must agree with AuthorID, not lag it")
}

// TestSplitCompositeAuthor_LeavesNonPrimaryBooksAlone is the negative half: a book
// that merely LISTS the composite author (not as its primary) needs only the
// junction rewrite. Writing the book row for it would be a needless write and
// would silently promote a secondary contributor to primary. No GetBookByID or
// UpdateBook is EXPECTed here, so the mock fails the test if either is called.
func TestSplitCompositeAuthor_LeavesNonPrimaryBooksAlone(t *testing.T) {
	h, d := newHandler(t)
	otherPrimary := 99

	d.store.EXPECT().GetAuthorByID(5).Return(&database.Author{ID: 5, Name: "A / B"}, nil)
	d.store.EXPECT().GetAuthorByName("A").Return(nil, errString("not found"))
	d.store.EXPECT().CreateAuthor("A").Return(&database.Author{ID: 10, Name: "A"}, nil)
	d.store.EXPECT().GetAuthorByName("B").Return(nil, errString("not found"))
	d.store.EXPECT().CreateAuthor("B").Return(&database.Author{ID: 11, Name: "B"}, nil)

	d.store.EXPECT().GetBooksByAuthorIDWithRoleCore(5).
		Return([]database.BookCore{{ID: "b2", AuthorID: &otherPrimary}}, nil)
	d.store.EXPECT().GetBookAuthors("b2").
		Return([]database.BookAuthor{{BookID: "b2", AuthorID: 5, Role: "contributor"}}, nil)
	d.store.EXPECT().SetBookAuthors("b2", mock.Anything).Return(nil)
	d.store.EXPECT().DeleteAuthor(5).Return(nil)

	c, w := newCtx(http.MethodPost, "/authors/5/split", `{"names":["A","B"]}`, idParam("5"))
	h.SplitCompositeAuthor(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestReclassifyAuthorAsNarrator_PromotesSurvivingAuthor covers the reclassify
// half of the dangling-AuthorID leak, where the book keeps a second author.
//
// Reclassify has no designated successor the way a split does -- the person
// becomes a NARRATOR, so nothing replaces them as author. When the junction
// still lists someone else, that someone is promoted to primary.
//
// Like TestSplitCompositeAuthor, the pre-existing TestReclassifyAuthorAsNarrator
// passes an EMPTY book list and so cannot observe any of this.
func TestReclassifyAuthorAsNarrator_PromotesSurvivingAuthor(t *testing.T) {
	h, d := newHandler(t)
	reclassified := 5

	d.store.EXPECT().GetAuthorByID(5).Return(&database.Author{ID: 5, Name: "Reader"}, nil)
	d.store.EXPECT().GetNarratorByName("Reader").Return(nil, errString("not found"))
	d.store.EXPECT().CreateNarrator("Reader").Return(&database.Narrator{ID: 3, Name: "Reader"}, nil)

	d.store.EXPECT().GetBooksByAuthorIDWithRoleCore(5).
		Return([]database.BookCore{{ID: "b1", AuthorID: &reclassified}}, nil)
	d.store.EXPECT().GetBookAuthors("b1").Return([]database.BookAuthor{
		{BookID: "b1", AuthorID: 5, Role: "author", Position: 0},
		{BookID: "b1", AuthorID: 7, Role: "author", Position: 1},
	}, nil)
	d.store.EXPECT().SetBookAuthors("b1", mock.Anything).Return(nil)
	// The surviving junction entry is looked up so a real Author object can be
	// written next to the new AuthorID.
	d.store.EXPECT().GetAuthorByID(7).Return(&database.Author{ID: 7, Name: "Real Author"}, nil)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{
		ID:       "b1",
		AuthorID: &reclassified,
		Author:   &database.Author{ID: 5, Name: "Reader"},
	}, nil)

	var wrote *database.Book
	d.store.EXPECT().UpdateBook("b1", mock.Anything).
		RunAndReturn(func(_ string, b *database.Book) (*database.Book, error) {
			wrote = b
			return b, nil
		})
	d.store.EXPECT().GetBookNarrators("b1").Return(nil, nil)
	d.store.EXPECT().SetBookNarrators("b1", mock.Anything).Return(nil)
	d.store.EXPECT().DeleteAuthor(5).Return(nil)

	c, w := newCtx(http.MethodPost, "/authors/5/reclassify-as-narrator", "", idParam("5"))
	h.ReclassifyAuthorAsNarrator(c)
	assert.Equal(t, http.StatusOK, w.Code)

	require.NotNil(t, wrote, "the book row must be written; otherwise AuthorID still points at the deleted author")
	require.NotNil(t, wrote.AuthorID, "a surviving author exists, so AuthorID must be repointed, not cleared")
	assert.Equal(t, 7, *wrote.AuthorID, "the surviving junction author must become primary")
	require.NotNil(t, wrote.Author)
	assert.Equal(t, 7, wrote.Author.ID, "the denormalized Author must agree with the new AuthorID")
}

// TestReclassifyAuthorAsNarrator_ClearsWhenNoAuthorSurvives covers the case with
// no successor at all: the reclassified person was the book's ONLY author.
//
// AuthorID is cleared. book.Author is deliberately NOT cleared -- UpdateBook's
// preserve-on-nil guard makes that impossible without a store-level sentinel --
// so this asserts the reachable half and pins the residual rather than pretending
// it is fixed. Display is unaffected either way: on the list path memdb has
// already stripped Author, so a cleared AuthorID and a dangling one both render
// as no author. What changes is that nothing dangles.
func TestReclassifyAuthorAsNarrator_ClearsWhenNoAuthorSurvives(t *testing.T) {
	h, d := newHandler(t)
	reclassified := 5

	d.store.EXPECT().GetAuthorByID(5).Return(&database.Author{ID: 5, Name: "Reader"}, nil)
	d.store.EXPECT().GetNarratorByName("Reader").Return(nil, errString("not found"))
	d.store.EXPECT().CreateNarrator("Reader").Return(&database.Narrator{ID: 3, Name: "Reader"}, nil)

	d.store.EXPECT().GetBooksByAuthorIDWithRoleCore(5).
		Return([]database.BookCore{{ID: "b1", AuthorID: &reclassified}}, nil)
	d.store.EXPECT().GetBookAuthors("b1").Return([]database.BookAuthor{
		{BookID: "b1", AuthorID: 5, Role: "author", Position: 0},
	}, nil)
	d.store.EXPECT().SetBookAuthors("b1", mock.Anything).Return(nil)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{
		ID:       "b1",
		AuthorID: &reclassified,
		Author:   &database.Author{ID: 5, Name: "Reader"},
	}, nil)

	var wrote *database.Book
	d.store.EXPECT().UpdateBook("b1", mock.Anything).
		RunAndReturn(func(_ string, b *database.Book) (*database.Book, error) {
			wrote = b
			return b, nil
		})
	d.store.EXPECT().GetBookNarrators("b1").Return(nil, nil)
	d.store.EXPECT().SetBookNarrators("b1", mock.Anything).Return(nil)
	d.store.EXPECT().DeleteAuthor(5).Return(nil)

	c, w := newCtx(http.MethodPost, "/authors/5/reclassify-as-narrator", "", idParam("5"))
	h.ReclassifyAuthorAsNarrator(c)
	assert.Equal(t, http.StatusOK, w.Code)

	require.NotNil(t, wrote, "the book row must still be written to drop the dangling pointer")
	assert.Nil(t, wrote.AuthorID,
		"with no surviving author, AuthorID must be cleared rather than left pointing at the deleted row")
}
