// file: internal/books/searchindex/searchindextest/cutover.go
// version: 0.1.0
// guid: 95fc3dbc-2737-4132-846a-e98c87df8f70
// last-edited: 2026-11-20

// Package searchindextest is test support for internal/books/searchindex: a
// fixed-library Source, the recorded R0 answer oracle, the harness that opens
// books' index on core's real engine (coretest.IndexEngine) including the
// restart simulation, and the native half of TestBooksIndexRebuiltAtCutover.
package searchindextest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"

	"github.com/falkcorp/audiobook-organizer/internal/books/searchindex"
)

// Query is one recorded R0 search: the text the client sent and the book IDs the
// HEAD build answered.
type Query struct {
	Text string
	Want []string // sorted
}

// Oracle is a recorded R0 search answer set and the library it was taken over.
type Oracle struct {
	Books   []database.Book
	Authors map[int]*database.Author
	Series  map[int]*database.Series
	Queries []Query
}

type exchangeFile struct {
	Exchanges []struct {
		Request struct {
			Path string `json:"path"`
		} `json:"request"`
		Response struct {
			Body struct {
				Data struct {
					Items []map[string]any `json:"items"`
				} `json:"data"`
			} `json:"body"`
		} `json:"response"`
	} `json:"exchanges"`
}

// LoadOracle reads an R0 search answer file (r2corpus/answers/search.json). The
// library is rebuilt from the items of its empty search, which lists every book;
// volatile placeholders are dropped before a row is decoded.
func LoadOracle(t testing.TB, path string) Oracle {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	var f exchangeFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("oracle: %v", err)
	}
	o := Oracle{Authors: map[int]*database.Author{}, Series: map[int]*database.Series{}}
	for _, ex := range f.Exchanges {
		u, err := url.Parse(ex.Request.Path)
		if err != nil {
			t.Fatalf("oracle: path %q: %v", ex.Request.Path, err)
		}
		text := u.Query().Get("search")
		var ids []string
		for _, it := range ex.Response.Body.Data.Items {
			ids = append(ids, fmt.Sprint(it["id"]))
		}
		slices.Sort(ids)
		o.Queries = append(o.Queries, Query{Text: text, Want: ids})
		if text != "" || len(o.Books) > 0 {
			continue
		}
		for _, it := range ex.Response.Body.Data.Items {
			for k, v := range it {
				if s, ok := v.(string); ok && strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
					delete(it, k)
				}
			}
			b, err := json.Marshal(it)
			if err != nil {
				t.Fatalf("oracle: %v", err)
			}
			var book database.Book
			if err := json.Unmarshal(b, &book); err != nil {
				t.Fatalf("oracle: decode book: %v", err)
			}
			if id, ok := it["author_id"].(float64); ok {
				if name, _ := it["author_name"].(string); name != "" {
					o.Authors[int(id)] = &database.Author{ID: int(id), Name: name}
				}
			}
			if id, ok := it["series_id"].(float64); ok {
				if name, _ := it["series_name"].(string); name != "" {
					o.Series[int(id)] = &database.Series{ID: int(id), Name: name}
				}
			}
			o.Books = append(o.Books, book)
		}
	}
	if len(o.Books) == 0 {
		t.Fatal("oracle: no empty-search exchange to take the library from")
	}
	slices.SortFunc(o.Books, func(a, b database.Book) int { return strings.Compare(a.ID, b.ID) })
	return o
}

// FakeSource is a searchindex.Source over a fixed library.
type FakeSource struct {
	Books   []database.Book // sorted by ID
	Authors map[int]*database.Author
	Series  map[int]*database.Series
	Tags    map[string][]string
}

var _ searchindex.Source = (*FakeSource)(nil)

func (s *FakeSource) GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error) {
	var out []database.Book
	for _, b := range s.Books {
		if b.ID > afterID {
			out = append(out, b)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (s *FakeSource) GetBookByID(id string) (*database.Book, error) {
	for i := range s.Books {
		if s.Books[i].ID == id {
			b := s.Books[i]
			return &b, nil
		}
	}
	return nil, nil
}

func (s *FakeSource) GetAuthorsByIDs(ids []int) (map[int]*database.Author, error) {
	out := map[int]*database.Author{}
	for _, id := range ids {
		if a := s.Authors[id]; a != nil {
			out[id] = a
		}
	}
	return out, nil
}

func (s *FakeSource) GetSeriesByIDs(ids []int) (map[int]*database.Series, error) {
	out := map[int]*database.Series{}
	for _, id := range ids {
		if a := s.Series[id]; a != nil {
			out[id] = a
		}
	}
	return out, nil
}

func (s *FakeSource) GetBookTagsByBookIDs(ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, id := range ids {
		if t := s.Tags[id]; len(t) > 0 {
			out[id] = t
		}
	}
	return out, nil
}

// Source is the oracle's library as a store.
func (o Oracle) Source() *FakeSource {
	return &FakeSource{Books: o.Books, Authors: o.Authors, Series: o.Series}
}

// OwnerSpec is the books owner as a test store boots it: a keyspace and the four
// derived indexes books declares (books, memdb, hnsw, chromem), so the engine
// can open searchindex's "books" index through coretest.IndexEngine.
func OwnerSpec() storage.OwnerSpec {
	return storage.OwnerSpec{
		Owner:   storage.Owner(searchindex.Module),
		Enabled: true,
		Spec:    storage.Spec{Keyspace: true, Derived: searchindex.DerivedIndexes()},
	}
}

// Engine is core's real index engine over a booted test store, with books'
// search index opened on it.
type Engine struct {
	Store  *coretest.Store
	Engine *coretest.IndexEngine
	Index  index.Index
}

// OpenEngine boots a store with OwnerSpec and coretest.IndexOwner, builds the
// real engine over it (nothing runs in the background) and opens books' index
// from src. The index is Rebuilding until the caller runs Engine.Rebuild.
// engineOpts sets the engine's own options (the zero value is production's):
// a test of index.ErrTooBroad lowers MaxTermSearchers.
func OpenEngine(t testing.TB, src searchindex.Source, opts searchindex.Options, engineOpts ...coretest.IndexEngineOptions) *Engine {
	t.Helper()
	var eo coretest.IndexEngineOptions
	if len(engineOpts) > 0 {
		eo = engineOpts[0]
	}
	st := coretest.NewStore(t, OwnerSpec(), coretest.IndexOwner())
	x := coretest.NewIndexEngine(t, st, eo)
	return &Engine{Store: st, Engine: x, Index: open(t, x, src, opts)}
}

func open(t testing.TB, x *coretest.IndexEngine, src searchindex.Source, opts searchindex.Options) index.Index {
	t.Helper()
	ix, err := x.Opener(searchindex.Module).Open(t.Context(), searchindex.NewSpec(src, opts))
	if err != nil {
		t.Fatalf("open the books index: %v", err)
	}
	return ix
}

// Rebuild runs the full rebuild the kernel queues for a fresh index and returns
// the number of documents it indexed.
func (e *Engine) Rebuild(t testing.TB) int64 {
	t.Helper()
	n, err := e.Engine.Rebuild(t.Context(), searchindex.Module, searchindex.Name)
	if err != nil {
		t.Fatalf("rebuild the books index: %v", err)
	}
	return n
}

// Restart simulates a process restart over the same files: the engine closes,
// the store reboots with the same owners, and a new engine opens the same
// directory and the books index again from src. The old Index is closed.
func (e *Engine) Restart(t testing.TB, src searchindex.Source, opts searchindex.Options) {
	t.Helper()
	dir := e.Engine.Dir()
	if err := e.Engine.Close(); err != nil {
		t.Fatalf("close the engine before the restart: %v", err)
	}
	e.Store.Reboot(OwnerSpec(), coretest.IndexOwner())
	e.Engine = coretest.NewIndexEngine(t, e.Store, coretest.IndexEngineOptions{Dir: dir})
	e.Index = open(t, e.Engine, src, opts)
}

// Search runs q and returns the matching IDs in the engine's order, with the
// total.
func Search(t testing.TB, ix index.Index, q index.Query, sortBy ...string) ([]string, uint64) {
	t.Helper()
	res, err := ix.Search(t.Context(), q, index.Page{Size: 1000, SortBy: sortBy})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	ids := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		ids = append(ids, h.ID)
	}
	return ids, res.Total
}

// RunBooksIndexRebuiltAtCutover is the native half of the spec test (05-search):
// the first boot of the R2 build finds no index of the new engine, builds books'
// at <db parent>/index/books/books.bleve while search is degraded, and then
// answers every recorded R0 query with the book set the HEAD build answered.
// It runs on core's real engine (coretest.IndexEngine), and then again after a
// restart over the same files, which must find the index Ready with nothing to
// rebuild.
//
// Degraded meanwhile means what the contract says for Rebuilding: the index does
// not yet cover the library, so a miss is not "no such document" and the caller
// serves its fallback. The harness checks exactly that (Rebuilding, queued for a
// rebuild, holding nothing), then runs the rebuild.
//
// It has no caller outside tests (F7-09 wires it at the fold). Queries whose text
// is a regular expression are the filter layer's, not the index's (HEAD's
// querygrammar applies them in Go after the index answers); the harness skips
// those by name and counts them.
func RunBooksIndexRebuiltAtCutover(t *testing.T, o Oracle, regex func(text string) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	opts := searchindex.Options{Workers: 4, PageSize: 5, ChunkSize: 2}
	e := OpenEngine(t, o.Source(), opts)

	want := e.Engine.Path(searchindex.Module, searchindex.Name)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the index is not at %s: %v", want, err)
	}
	if want != filepath.Join(e.Engine.Dir(), "books", "books.bleve") {
		t.Fatalf("the index is at %s, want <index dir>/books/books.bleve", want)
	}

	// Degraded meanwhile: Rebuilding, queued for a rebuild, and holding nothing
	// of the library.
	if st := e.Index.State(); st != index.Rebuilding {
		t.Fatalf("state while the rebuild is pending = %v, want rebuilding", st)
	}
	if got := e.Engine.PendingRebuilds(); !slices.Equal(got, []string{"books/books"}) {
		t.Fatalf("pending rebuilds = %v, want [books/books]", got)
	}
	if n, err := e.Index.Count(ctx, searchindex.MatchAllQuery()); err != nil || n != 0 {
		t.Fatalf("a rebuilding index holds %d documents (err %v); it must not look complete", n, err)
	}

	if n := e.Rebuild(t); int(n) != len(o.Books) {
		t.Fatalf("the rebuild indexed %d documents, want %d", n, len(o.Books))
	}
	if err := e.Engine.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if st := e.Index.State(); st != index.Ready {
		t.Fatalf("state after the rebuild = %v, want ready", st)
	}
	if n, err := e.Index.Count(ctx, searchindex.MatchAllQuery()); err != nil || int(n) != len(o.Books) {
		t.Fatalf("documents after the rebuild = %d (err %v), want %d", n, err, len(o.Books))
	}
	skipped := answersMatch(t, e.Index, o, regex)
	if skipped == len(o.Queries) {
		t.Fatal("every recorded query was skipped")
	}

	t.Run("after a restart", func(t *testing.T) {
		e.Restart(t, o.Source(), opts)
		if st := e.Index.State(); st != index.Ready {
			t.Fatalf("state after the restart = %v, want ready (the files were intact)", st)
		}
		if got := e.Engine.PendingRebuilds(); len(got) != 0 {
			t.Fatalf("pending rebuilds after the restart = %v, want none", got)
		}
		answersMatch(t, e.Index, o, regex)
	})
}

// answersMatch checks every recorded query against ix and returns how many it
// skipped as the filter layer's.
func answersMatch(t *testing.T, ix index.Index, o Oracle, regex func(text string) bool) int {
	t.Helper()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	skipped := 0
	for _, q := range o.Queries {
		t.Run("search="+q.Text, func(t *testing.T) {
			if regex != nil && regex(q.Text) {
				skipped++
				t.Skip("a regular expression is the filter layer's, not the index's")
			}
			tr, err := searchindex.Translate(q.Text, now)
			if err != nil {
				t.Fatalf("translate: %v", err)
			}
			if len(tr.PerUser) > 0 || len(tr.Tags) > 0 {
				t.Fatalf("recorded search has per-user or tag terms the harness does not apply: %+v", tr)
			}
			iq := tr.Query
			if tr.MatchAll {
				iq = searchindex.MatchAllQuery()
			}
			got, total := Search(t, ix, iq, "_id")
			slices.Sort(got)
			if !slices.Equal(got, q.Want) || int(total) != len(q.Want) {
				t.Errorf("got %d %v, want %d %v", total, got, len(q.Want), q.Want)
			}
		})
	}
	return skipped
}
