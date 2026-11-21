// file: internal/books/searchindex/searchindextest/cutover.go
// version: 0.1.0
// guid: 95fc3dbc-2737-4132-846a-e98c87df8f70
// last-edited: 2026-11-20

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

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/storage/index/indextest"

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

// RunBooksIndexRebuiltAtCutover is the native half of the spec test (05-search):
// the first boot of the R2 build finds no index of the new engine, builds books'
// at <db parent>/index/books/books.bleve while search is degraded, and then
// answers every recorded R0 query with the book set the HEAD build answered.
//
// Degraded meanwhile means what the contract says for Rebuilding: the index does
// not yet cover the library, so a miss is not "no such document" and the caller
// serves its fallback. The harness holds the rebuild open and checks exactly
// that, then releases it.
//
// It is exported with no caller (F7-09 wires it at the fold). Queries whose text
// is a regular expression are the filter layer's, not the index's (HEAD's
// querygrammar applies them in Go after the index answers); the harness skips
// those by name and counts them.
func RunBooksIndexRebuiltAtCutover(t *testing.T, o Oracle, regex func(text string) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A booted store the way the kernel boots one; the index lives beside it.
	fx := indextest.Open(t, storage.FSOf(vfs.NewMem()), filepath.Join(t.TempDir(), "db"))
	dbParent := filepath.Dir(fx.Path)

	hold := make(chan struct{})
	op := &Opener{DBParent: dbParent, Module: searchindex.Module, HoldRebuild: hold}
	spec := searchindex.NewSpec(o.Source(), searchindex.Options{Workers: 4, PageSize: 5, ChunkSize: 2})
	ix, err := op.Open(ctx, spec)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := ix.Close(); err != nil {
			t.Errorf("close the opened index: %v", err)
		}
	})

	want := IndexDir(dbParent, searchindex.Module, searchindex.Name)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the index is not at %s: %v", want, err)
	}

	// Degraded meanwhile: not Ready, and holding nothing of the library.
	if st := ix.State(); st != index.Rebuilding {
		t.Fatalf("state while the rebuild runs = %v, want rebuilding", st)
	}
	if n, err := ix.Count(ctx, searchindex.MatchAllQuery()); err != nil || n != 0 {
		t.Fatalf("a rebuilding index holds %d documents (err %v); it must not look complete", n, err)
	}

	close(hold)
	if err := ix.(Rebuilt).WaitRebuilt(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if st := ix.State(); st != index.Ready {
		t.Fatalf("state after the rebuild = %v, want ready", st)
	}
	if n, err := ix.Count(ctx, searchindex.MatchAllQuery()); err != nil || int(n) != len(o.Books) {
		t.Fatalf("documents after the rebuild = %d (err %v), want %d", n, err, len(o.Books))
	}

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
			res, err := ix.Search(ctx, iq, index.Page{Size: 1000, SortBy: []string{"_id"}})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			got := make([]string, 0, len(res.Hits))
			for _, h := range res.Hits {
				got = append(got, h.ID)
			}
			slices.Sort(got)
			if !slices.Equal(got, q.Want) || int(res.Total) != len(q.Want) {
				t.Errorf("got %d %v, want %d %v", res.Total, got, len(q.Want), q.Want)
			}
		})
	}
	if skipped == len(o.Queries) {
		t.Fatal("every recorded query was skipped")
	}
}
