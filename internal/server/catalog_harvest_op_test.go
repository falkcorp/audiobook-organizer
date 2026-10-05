// file: internal/server/catalog_harvest_op_test.go
// version: 1.1.1
// guid: 2d7e9b41-5c3a-4f86-9e10-4b8a6c2d1f57
// last-edited: 2026-10-05

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

type catalogTestReporter struct {
	mu   sync.Mutex
	logs []string
}

func (r *catalogTestReporter) UpdateProgress(int, int, string) error { return nil }
func (r *catalogTestReporter) Log(_ slog.Level, m string, _ ...slog.Attr) error {
	r.mu.Lock()
	r.logs = append(r.logs, m)
	r.mu.Unlock()
	return nil
}
func (r *catalogTestReporter) Logger() *slog.Logger                       { return slog.Default() }
func (r *catalogTestReporter) Checkpoint(any) error                       { return nil }
func (r *catalogTestReporter) IsCanceled() bool                           { return false }
func (r *catalogTestReporter) Trigger(context.Context, string, any) error { return nil }
func (r *catalogTestReporter) SetCurrentItem(string)                      {}
func (r *catalogTestReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, opsregistry.Reporter) error) error {
	return fn(ctx, r)
}

// catalogFakeLister lists two products per author name.
type catalogFakeLister struct {
	mu    sync.Mutex
	names []string
}

func (l *catalogFakeLister) ProviderID() string { return "audible" }
func (l *catalogFakeLister) ListByAuthor(_ context.Context, name string, page, _ int) (metadata.AuthorPage, error) {
	l.mu.Lock()
	l.names = append(l.names, name)
	l.mu.Unlock()
	if page > 0 {
		return metadata.AuthorPage{TotalResults: 2}, nil
	}
	a := metadata.CatalogContributor{Name: name, ASIN: "AU-" + strings.ToUpper(strings.ReplaceAll(name, " ", ""))}
	return metadata.AuthorPage{TotalResults: 2, Products: []metadata.CatalogProduct{
		{ASIN: "P1-" + a.ASIN, Title: "First", Language: "english", FormatType: "unabridged", Authors: []metadata.CatalogContributor{a}},
		{ASIN: "P2-" + a.ASIN, Title: "Second", Language: "english", FormatType: "unabridged", Authors: []metadata.CatalogContributor{a}},
	}}, nil
}
func (l *catalogFakeLister) LookupProduct(context.Context, string) (*metadata.CatalogProduct, error) {
	// Not-found, never a generic error: the harvest feeds provider errors to
	// the process-wide throttle registry, and a hold recorded here would leak
	// into every later test in this binary.
	return nil, metadata.ErrCatalogProductNotFound
}

func withCatalogEnabled(t *testing.T, on bool) {
	t.Helper()
	prev := config.AppConfig.Catalog
	config.AppConfig.Catalog = config.CatalogConfig{Enabled: on, Language: "english", Marketplace: "us", HarvestRateFraction: 1, MaxProductsPerAuthor: 2000}
	t.Cleanup(func() { config.AppConfig.Catalog = prev })
}

// seedCatalogLibrary: "Ann Author" has a live book with a present file;
// "Ghost Writer" only a soft-deleted book; "Missing Person" only missing
// files; a filename-shaped credit is a junk name; one live book has no
// author.
func seedCatalogLibrary(t *testing.T) *database.PebbleStore {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	mk := func(title, author string, deleted, missing bool) {
		t.Helper()
		b := &database.Book{Title: title, FilePath: "/lib/" + title}
		if deleted {
			yes := true
			b.MarkedForDeletion = &yes
		}
		book, err := ps.CreateBook(b)
		if err != nil {
			t.Fatal(err)
		}
		if err := ps.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: "/lib/" + title + "/a.m4b", Missing: missing}); err != nil {
			t.Fatal(err)
		}
		if author == "" {
			return
		}
		a, err := ps.CreateAuthor(author)
		if err != nil {
			t.Fatal(err)
		}
		if err := ps.SetBookAuthors(book.ID, []database.BookAuthor{{BookID: book.ID, AuthorID: a.ID, Role: "author"}}); err != nil {
			t.Fatal(err)
		}
	}
	mk("Live Book", "Ann Author", false, false)
	mk("Gone Book", "Ghost Writer", true, false)
	mk("Lost Book", "Missing Person", false, true)
	mk("Junk Book", "Brandon Sanderson - Mistborn 01", false, false)
	mk("Orphan Book", "", false, false)
	return ps
}

func TestCatalogHarvestOp_RefusesWhenFlagOff(t *testing.T) {
	withCatalogEnabled(t, false)
	s := &Server{}
	err := s.runCatalogHarvestOp(context.Background(), json.RawMessage(`{"dry_run":false}`), &catalogTestReporter{})
	if !errors.Is(err, errCatalogDisabled) {
		t.Fatalf("err = %v; want errCatalogDisabled", err)
	}
}

func TestCatalogHarvest_DryRunThenLive(t *testing.T) {
	withCatalogEnabled(t, true)
	ps := seedCatalogLibrary(t)
	cat := database.NewCatalogStoreFromStore(ps)
	if cat == nil {
		t.Fatal("no catalog store")
	}
	lister := &catalogFakeLister{}
	rep := &catalogTestReporter{}
	zero := 0

	// Dry run (the default): census only, nothing written.
	if err := runCatalogHarvest(context.Background(), rep, ps, cat, lister, catalogHarvestParams{SampleAuthors: &zero}, true, config.AppConfig.Catalog); err != nil {
		t.Fatal(err)
	}
	if n, _ := cat.CountEntries(); n != 0 {
		t.Fatalf("dry run wrote %d entries", n)
	}
	if st, _ := cat.GetAuthorState(catalog.HarvestKey("Ann Author")); st != nil {
		t.Fatal("dry run wrote author state")
	}
	joined := strings.Join(rep.logs, "\n")
	if !strings.Contains(joined, "scope: 1 authors") || !strings.Contains(joined, "1 without an author") || !strings.Contains(joined, "skipped: 1 junk,") {
		t.Errorf("census log:\n%s", joined)
	}
	if len(lister.names) != 0 {
		t.Errorf("sample_authors=0 still made %d requests", len(lister.names))
	}

	// Live: only Ann Author is in scope.
	if err := runCatalogHarvest(context.Background(), rep, ps, cat, lister, catalogHarvestParams{}, false, config.AppConfig.Catalog); err != nil {
		t.Fatal(err)
	}
	if strings.Join(lister.names, ",") != "Ann Author" {
		t.Errorf("harvested %v; want only Ann Author", lister.names)
	}
	if n, _ := cat.CountEntries(); n != 2 {
		t.Errorf("entries = %d; want 2", n)
	}
	st, _ := cat.GetAuthorState(catalog.HarvestKey("Ann Author"))
	if st == nil || st.State != database.CatalogHarvestComplete {
		t.Fatalf("state = %+v", st)
	}

	// Second live run inside the interval: nothing due, no requests.
	lister.names = nil
	if err := runCatalogHarvest(context.Background(), rep, ps, cat, lister, catalogHarvestParams{}, false, config.AppConfig.Catalog); err != nil {
		t.Fatal(err)
	}
	if len(lister.names) != 0 {
		t.Errorf("complete author refetched inside the interval: %v", lister.names)
	}
	// Manual trigger bypasses the interval.
	if err := runCatalogHarvest(context.Background(), rep, ps, cat, lister, catalogHarvestParams{Authors: []string{"ann  author"}}, false, config.AppConfig.Catalog); err != nil {
		t.Fatal(err)
	}
	if len(lister.names) != 1 {
		t.Errorf("manual trigger made %d listings; want 1", len(lister.names))
	}
	// An out-of-scope manual trigger is refused, not silently a no-op.
	if err := runCatalogHarvest(context.Background(), rep, ps, cat, lister, catalogHarvestParams{Authors: []string{"Ghost Writer"}}, false, config.AppConfig.Catalog); err == nil {
		t.Error("out-of-scope author accepted")
	}
}

func TestCatalogRoutes_FlagGatesAndLists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ps := seedCatalogLibrary(t)
	cat := database.NewCatalogStoreFromStore(ps)
	if _, err := cat.UpsertEntries([]database.CatalogUpsert{{Entry: catalog.BuildEntry(metadata.CatalogProduct{
		ASIN: "B1", Title: "First", Authors: []metadata.CatalogContributor{{Name: "Ann Author", ASIN: "AX"}},
		Series: []metadata.CatalogProductSeries{{Title: "Saga", Sequence: "1"}},
	}, "audible", "us")}}, "annauthor"); err != nil {
		t.Fatal(err)
	}
	h := &catalogHandler{store: func() *database.CatalogStore { return cat }}
	r := gin.New()
	r.GET("/catalog/entries", h.listEntries)
	r.GET("/catalog/entries/:id", h.getEntry)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	withCatalogEnabled(t, false)
	if w := get("/catalog/entries"); w.Code != http.StatusNotFound {
		t.Errorf("flag off: %d", w.Code)
	}
	withCatalogEnabled(t, true)
	for _, q := range []string{"?author=ann%20author", "?author_asin=ax", "?series=SAGA", ""} {
		w := get("/catalog/entries" + q)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"provider_id":"B1"`) {
			t.Errorf("list %q: %d %s", q, w.Code, w.Body.String())
		}
	}
	if w := get("/catalog/entries?author=nobody"); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "B1") {
		t.Errorf("other author: %d %s", w.Code, w.Body.String())
	}
	if w := get("/catalog/entries?author=a&series=b"); w.Code != http.StatusBadRequest {
		t.Errorf("two filters: %d", w.Code)
	}
	if w := get("/catalog/entries?limit=0"); w.Code != http.StatusBadRequest {
		t.Errorf("limit=0: %d", w.Code)
	}
	e, _ := cat.GetEntryByProviderID("audible", "us", "B1")
	if w := get("/catalog/entries/" + e.ID); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "edition_group_entry_ids") {
		t.Errorf("get: %d %s", w.Code, w.Body.String())
	}
	if w := get("/catalog/entries/nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", w.Code)
	}
}
