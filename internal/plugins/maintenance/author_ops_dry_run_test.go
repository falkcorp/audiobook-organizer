// file: internal/plugins/maintenance/author_ops_dry_run_test.go
// version: 1.1.0
// guid: 5c1e4571-8bcf-4190-ac2d-ae141026d139
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// author-split-scan and resolve-production-authors ignored their params and
// wrote on every run until 2026-09-25. Both now default to a dry run.

func newDryRunSplitStore(t *testing.T) (*database.PebbleStore, int, string) {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	st.WaitForWarmup()
	t.Cleanup(func() { _ = st.Close() })
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	b, err := st.CreateBook(&database.Book{
		Title:            "dry-run-split",
		FilePath:         "/dry-run-split/book.m4b",
		AuthorID:         new(composite.ID),
		IsPrimaryVersion: new(true),
	})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: composite.ID, Role: "author"}}))
	return st, composite.ID, b.ID
}

func TestAuthorSplitScan_DefaultIsDryRun(t *testing.T) {
	for _, params := range []string{"", `{}`, `{"dry_run":true}`, `{"dryRun":true}`} {
		t.Run("params="+params, func(t *testing.T) {
			st, compositeID, bookID := newDryRunSplitStore(t)
			p := &Plugin{deps: &fakeDeps{store: st}}
			rep := &fakeReporter{}
			var raw json.RawMessage
			if params != "" {
				raw = json.RawMessage(params)
			}
			require.NoError(t, p.runAuthorSplitScan(context.Background(), raw, rep))

			still, err := st.GetAuthorByID(compositeID)
			require.NoError(t, err)
			require.NotNil(t, still, "dry run deleted the composite author")
			alice, err := st.GetAuthorByName("Alice Smith")
			require.NoError(t, err)
			require.Nil(t, alice, "dry run created a split author")
			credits, err := st.GetBookAuthors(bookID)
			require.NoError(t, err)
			require.Len(t, credits, 1)
			require.Equal(t, compositeID, credits[0].AuthorID, "dry run relinked the book")
			require.Contains(t, strings.Join(rep.logs, "\n"), "DRY RUN: would split 1 composite authors, create 2 authors, relink 1 books")
		})
	}
}

// The camelCase alias applies too, so an operator using either spelling gets
// the write they asked for.
func TestAuthorSplitScan_ExplicitApplyWrites(t *testing.T) {
	st, compositeID, _ := newDryRunSplitStore(t)
	p := &Plugin{deps: &fakeDeps{store: st}}
	require.NoError(t, p.runAuthorSplitScan(context.Background(), json.RawMessage(`{"dryRun":false}`), &fakeReporter{}))
	gone, err := st.GetAuthorByID(compositeID)
	require.NoError(t, err)
	require.Nil(t, gone, "apply should delete the composite")
}

func TestAuthorOpDryRun_DisagreeingKeysAreAnError(t *testing.T) {
	st, compositeID, _ := newDryRunSplitStore(t)
	p := &Plugin{deps: &fakeDeps{store: st}}
	err := p.runAuthorSplitScan(context.Background(), json.RawMessage(`{"dry_run":false,"dryRun":true}`), &fakeReporter{})
	require.ErrorContains(t, err, "disagree")
	still, gerr := st.GetAuthorByID(compositeID)
	require.NoError(t, gerr)
	require.NotNil(t, still)
	err = p.runResolveProductionAuthors(context.Background(), json.RawMessage(`{"dry_run":true,"dryRun":false}`), &fakeReporter{})
	require.ErrorContains(t, err, "disagree")
}

// guardOpsStore stands in for OpsStore in the resolve-production-authors
// tests. It answers the three reads the op makes and nothing else: the embedded
// OpsStore is nil, so any other call -- every write -- panics, and the tests
// below recover that into a failure that names the offending call.
type guardOpsStore struct {
	OpsStore
	authors []database.Author
	books   map[int][]database.BookCore
	byID    map[int]*database.Author
}

func (s guardOpsStore) GetAllAuthors() ([]database.Author, error) { return s.authors, nil }
func (s guardOpsStore) GetBooksByAuthorIDWithRoleCore(id int) ([]database.BookCore, error) {
	return s.books[id], nil
}
func (s guardOpsStore) GetAuthorByID(id int) (*database.Author, error) { return s.byID[id], nil }

// resolveDeps drives runResolveProductionAuthors: a guard store, a scripted
// metadata fetch, and a count of the dedup cache invalidations (the op's one
// non-store write).
type resolveDeps struct {
	fakeDeps
	guard         guardOpsStore
	wired         bool
	fetch         func(bookID string) (*int, error)
	fetched       *[]string
	invalidations *int
}

func (d resolveDeps) OpsStore() OpsStore            { return d.guard }
func (d resolveDeps) HasMetadataFetchService() bool { return d.wired }
func (d resolveDeps) InvalidateDedupCache()         { *d.invalidations++ }
func (d resolveDeps) FetchMetadataByTitle(id string) (*int, error) {
	*d.fetched = append(*d.fetched, id)
	return d.fetch(id)
}

// newResolveFixture: two production companies credited with two and one books
// (synthetic names), one real author the fetch can resolve to, and one
// non-production author who is not touched.
func newResolveFixture() (resolveDeps, *[]string, *int) {
	var fetched []string
	inv := 0
	realID := 30
	d := resolveDeps{
		guard: guardOpsStore{
			authors: []database.Author{
				{ID: 1, Name: "Tantor Media"}, {ID: 2, Name: "Audible Studios"}, {ID: 3, Name: "Jane Roe"},
			},
			books: map[int][]database.BookCore{
				1: {{ID: "b1"}, {ID: "b2"}},
				2: {{ID: "b3"}},
			},
			byID: map[int]*database.Author{
				realID: {ID: realID, Name: "John Roe"},
				1:      {ID: 1, Name: "Tantor Media"},
			},
		},
		wired:         true,
		fetched:       &fetched,
		invalidations: &inv,
		fetch:         func(string) (*int, error) { return &realID, nil },
	}
	return d, &fetched, &inv
}

// runGuarded runs the op and turns a panic from the guard store (a write, or any
// read the op is not meant to make) into a test failure.
func runGuarded(t *testing.T, p *Plugin, raw json.RawMessage, rep *fakeReporter) error {
	t.Helper()
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("resolve-production-authors touched the store beyond its three reads: %v", r)
			}
		}()
		err = p.runResolveProductionAuthors(context.Background(), raw, rep)
	}()
	return err
}

// An empty or mode-less param body is a preview: zero fetches (a fetch applies
// metadata), zero store writes, no dedup cache invalidation. This is what the
// scheduled resolve_production_authors task sends.
func TestResolveProductionAuthors_DefaultsToPreview(t *testing.T) {
	for _, body := range []string{"", `{}`, `null`, `{"dry_run":true}`, `{"dryRun":true}`} {
		t.Run("body="+body, func(t *testing.T) {
			d, fetched, inv := newResolveFixture()
			p := &Plugin{deps: d}
			var raw json.RawMessage
			if body != "" {
				raw = json.RawMessage(body)
			}
			rep := &fakeReporter{}
			require.NoError(t, runGuarded(t, p, raw, rep))
			require.Empty(t, *fetched, "preview fetched metadata, which applies it")
			require.Zero(t, *inv, "preview invalidated the dedup cache")
			require.Contains(t, strings.Join(rep.logs, "\n"),
				"DRY RUN: would re-fetch metadata for 3 books across 2 production companies")
		})
	}
}

// The apply is reachable only by an explicit dry_run=false (either spelling),
// fetches every book credited to a production company, counts only the books
// that landed on a non-production author, and invalidates the dedup cache once.
func TestResolveProductionAuthors_ApplyFetchesAndCounts(t *testing.T) {
	for _, body := range []string{`{"dry_run":false}`, `{"dryRun":false}`} {
		t.Run(body, func(t *testing.T) {
			d, fetched, inv := newResolveFixture()
			stillProd := 1
			d.fetch = func(id string) (*int, error) {
				switch id {
				case "b1":
					realID := 30
					return &realID, nil // resolved
				case "b2":
					return &stillProd, nil // still the production company
				default:
					return nil, errors.New("no match") // b3: fetch failed
				}
			}
			p := &Plugin{deps: d}
			rep := &fakeReporter{}
			require.NoError(t, runGuarded(t, p, json.RawMessage(body), rep))
			require.Equal(t, []string{"b1", "b2", "b3"}, *fetched)
			require.Equal(t, 1, *inv)
			require.Contains(t, strings.Join(rep.logs, "\n"),
				"Resolved 1 books across 2 production companies (1 fetches failed")
		})
	}
}

// Without the fetch service the apply would resolve nothing and still report
// success; it refuses up front instead. The preview does not need the service.
func TestResolveProductionAuthors_ApplyRefusesWithoutFetchService(t *testing.T) {
	d, fetched, inv := newResolveFixture()
	d.wired = false
	p := &Plugin{deps: d}
	err := runGuarded(t, p, json.RawMessage(`{"dry_run":false}`), &fakeReporter{})
	require.ErrorContains(t, err, "metadata fetch service not initialized")
	require.Empty(t, *fetched)
	require.Zero(t, *inv)

	require.NoError(t, runGuarded(t, p, nil, &fakeReporter{}), "preview must not need the fetch service")
}

// A cancelled context stops the apply between books, before the dedup cache is
// invalidated for work that did not finish.
func TestResolveProductionAuthors_ApplyStopsOnCancel(t *testing.T) {
	d, fetched, inv := newResolveFixture()
	ctx, cancel := context.WithCancel(context.Background())
	d.fetch = func(string) (*int, error) {
		cancel()
		id := 30
		return &id, nil
	}
	p := &Plugin{deps: d}
	err := p.runResolveProductionAuthors(ctx, json.RawMessage(`{"dry_run":false}`), &fakeReporter{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{"b1"}, *fetched)
	require.Zero(t, *inv)
}
