// file: internal/server/books_t7_shared_test.go
// version: 1.0.0
// guid: 5d0b8e47-92c1-4a63-b7f5-3e8a1c4d6029
// last-edited: 2026-11-28

package server

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// booksT7SpyStore is a store that records the methods the adapter calls on it,
// and answers each with a value named for the method, so a result proves WHICH
// store method an adapter method reached (a same-signature neighbour would give
// another name: the #5037 SF-1 pattern). The embedded interface is nil: a method
// the spy does not override panics, which a cell reports as a failure.
type booksT7SpyStore struct {
	database.Store
	mu    sync.Mutex
	calls []string
	args  map[string]any
}

func (s *booksT7SpyStore) rec(method string, arg any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, method)
	if s.args == nil {
		s.args = map[string]any{}
	}
	s.args[method] = arg
}

func (s *booksT7SpyStore) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *booksT7SpyStore) GetAllAuthors() ([]database.Author, error) {
	s.rec("GetAllAuthors", nil)
	return []database.Author{{ID: 1, Name: "GetAllAuthors"}}, nil
}

func (s *booksT7SpyStore) GetAllImportPaths() ([]database.ImportPath, error) {
	s.rec("GetAllImportPaths", nil)
	return []database.ImportPath{{ID: 1, Path: "GetAllImportPaths"}}, nil
}

func (s *booksT7SpyStore) GetAuthorByID(id int) (*database.Author, error) {
	s.rec("GetAuthorByID", id)
	return &database.Author{ID: id, Name: "GetAuthorByID"}, nil
}

func (s *booksT7SpyStore) GetAuthorByName(name string) (*database.Author, error) {
	s.rec("GetAuthorByName", name)
	return &database.Author{ID: 2, Name: "GetAuthorByName"}, nil
}

func (s *booksT7SpyStore) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	s.rec("GetBookAuthors", bookID)
	return []database.BookAuthor{{BookID: "GetBookAuthors"}}, nil
}

func (s *booksT7SpyStore) GetBookByID(id string) (*database.Book, error) {
	s.rec("GetBookByID", id)
	return &database.Book{ID: "GetBookByID"}, nil
}

func (s *booksT7SpyStore) GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error) {
	s.rec("GetBooksByAuthorIDWithRoleCore", authorID)
	return []database.BookCore{{ID: "GetBooksByAuthorIDWithRoleCore"}}, nil
}

func (s *booksT7SpyStore) CreateAuthor(name string) (*database.Author, error) {
	s.rec("CreateAuthor", name)
	return &database.Author{ID: 3, Name: "CreateAuthor"}, nil
}

// SaveOperationState and DeleteOperationState are the opstate: writes the tasks
// flip retires; a cell fails when a run reaches either.
func (s *booksT7SpyStore) SaveOperationState(opID string, _ []byte) error {
	s.rec("SaveOperationState", opID)
	return nil
}

func (s *booksT7SpyStore) DeleteOperationState(opID string) error {
	s.rec("DeleteOperationState", opID)
	return nil
}

// booksT7MissingBooksStore answers every book read with "not found", so the
// bulk write-back loop counts each book failed without reaching the file work.
type booksT7MissingBooksStore struct{ booksT7SpyStore }

func (s *booksT7MissingBooksStore) GetBookByID(id string) (*database.Book, error) {
	s.rec("GetBookByID", id)
	return nil, nil
}

// Cell: each shared read reaches the store method of the same name with its own
// argument, and returns that method's answer.
// Mutants, each killed by the named FAIL: GetAuthorByID calls GetAuthorByName's
// store method (TestBooksT7Shared_ReadsReachTheStoreMethodOfTheSameName/
// GetAuthorByID: "store method reached"); GetBooksByAuthorIDWithRoleCore drops
// its argument (".../GetBooksByAuthorIDWithRoleCore: argument reaches the store").
func TestBooksT7Shared_ReadsReachTheStoreMethodOfTheSameName(t *testing.T) {
	spy := &booksT7SpyStore{}
	deps := (&Server{store: spy}).booksT7Deps("books.library.scan")
	for _, tc := range []struct {
		method string
		arg    any
		call   func(t7Store) (marker string, err error)
	}{
		{"GetAllAuthors", nil, func(s t7Store) (string, error) {
			v, err := s.GetAllAuthors()
			return v[0].Name, err
		}},
		{"GetAllImportPaths", nil, func(s t7Store) (string, error) {
			v, err := s.GetAllImportPaths()
			return v[0].Path, err
		}},
		{"GetAuthorByID", 41, func(s t7Store) (string, error) {
			v, err := s.GetAuthorByID(41)
			return v.Name, err
		}},
		{"GetAuthorByName", "Barbara Gordon", func(s t7Store) (string, error) {
			v, err := s.GetAuthorByName("Barbara Gordon")
			return v.Name, err
		}},
		{"GetBookAuthors", "book-killing-joke", func(s t7Store) (string, error) {
			v, err := s.GetBookAuthors("book-killing-joke")
			return v[0].BookID, err
		}},
		{"GetBookByID", "book-year-one", func(s t7Store) (string, error) {
			v, err := s.GetBookByID("book-year-one")
			return v.ID, err
		}},
		{"GetBooksByAuthorIDWithRoleCore", 77, func(s t7Store) (string, error) {
			v, err := s.GetBooksByAuthorIDWithRoleCore(77)
			return v[0].ID, err
		}},
	} {
		t.Run(tc.method, func(t *testing.T) {
			marker, err := tc.call(deps.Store)
			require.NoError(t, err)
			require.Equal(t, tc.method, marker, "store method reached")
			spy.mu.Lock()
			defer spy.mu.Unlock()
			require.Equal(t, tc.arg, spy.args[tc.method], "argument reaches the store")
		})
	}
}

// Cell: with no store on the server a shared read refuses by name and field,
// without panicking, and the writer's store-backed methods too.
// Mutant: drop the need check from GetBookByID (killed by FAIL
// TestBooksT7Shared_RefuseByNameWithoutTheirBacking/GetBookByID: panic on the
// nil store).
func TestBooksT7Shared_RefuseByNameWithoutTheirBacking(t *testing.T) {
	deps := (&Server{}).booksT7Deps("books.library.scan")
	w := deps.NewWriter(&t7GateWriter{})
	for name, call := range map[string]func() error{
		"GetAllAuthors":                  func() error { _, err := deps.Store.GetAllAuthors(); return err },
		"GetAllImportPaths":              func() error { _, err := deps.Store.GetAllImportPaths(); return err },
		"GetAuthorByID":                  func() error { _, err := deps.Store.GetAuthorByID(1); return err },
		"GetAuthorByName":                func() error { _, err := deps.Store.GetAuthorByName("x"); return err },
		"GetBookAuthors":                 func() error { _, err := deps.Store.GetBookAuthors("x"); return err },
		"GetBookByID":                    func() error { _, err := deps.Store.GetBookByID("x"); return err },
		"GetBooksByAuthorIDWithRoleCore": func() error { _, err := deps.Store.GetBooksByAuthorIDWithRoleCore(1); return err },
		"CreateAuthor":                   func() error { _, err := w.CreateAuthor("x"); return err },
		"WriteBackBooks": func() error {
			return w.WriteBackBooks(t.Context(), "op", nil, false, 0, nil, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.Error(t, err)
			require.Contains(t, err.Error(), name, "refuses by name")
			require.Contains(t, err.Error(), "Server.", "names the unset field")
			var nb *ErrKindNotBuilt
			require.False(t, errors.As(err, &nb), "a built method does not refuse as a stub")
		})
	}
}

// booksT7LiveWriter is the run's writer for a kind: the executor's fence
// without the executor (writertest), declared for def's write set.
func booksT7LiveWriter(t *testing.T, kind tasks.Kind, mode tasks.Mode, declared ...ops.Resource) *writertest.Writer {
	t.Helper()
	return writertest.ForSet(kind, t7Res(declared...), mode)
}

// Cell: CreateAuthor is one gated write of the authors row. Inside Effects it
// writes and reaches the store's CreateAuthor with the name; a kind that does
// not declare authors is refused with the store untouched; a Preview run records
// the intent and writes nothing.
// Mutants, each killed by the named FAIL: call the store without the gate
// (TestBooksT7Shared_CreateAuthorIsGated/outside_effects: "store untouched");
// stamp books instead of authors (.../inside_effects: "stamped the authors row");
// ignore Preview (.../preview: "store untouched").
func TestBooksT7Shared_CreateAuthorIsGated(t *testing.T) {
	const kind = tasks.Kind("books.entities.author-reclassify")
	t.Run("inside effects", func(t *testing.T) {
		spy := &booksT7SpyStore{}
		fence := booksT7LiveWriter(t, kind, tasks.ModeLive, ops.ResAuthors)
		a, err := (&Server{store: spy}).booksT7Deps("").NewWriter(fence).CreateAuthor("Selina Kyle")
		require.NoError(t, err)
		require.Equal(t, "CreateAuthor", a.Name, "store method reached")
		require.Equal(t, "Selina Kyle", spy.args["CreateAuthor"])
		require.Equal(t, map[string]bool{"books/authors": true}, fence.Stamped(), "stamped the authors row")
		require.NoError(t, fence.RequireClean())
	})
	t.Run("outside effects", func(t *testing.T) {
		spy := &booksT7SpyStore{}
		fence := booksT7LiveWriter(t, kind, tasks.ModeLive, ops.ResBooks)
		_, err := (&Server{store: spy}).booksT7Deps("").NewWriter(fence).CreateAuthor("Selina Kyle")
		require.ErrorIs(t, err, tasks.ErrOutsideEffects)
		require.Empty(t, spy.called(), "store untouched")
	})
	t.Run("preview", func(t *testing.T) {
		spy := &booksT7SpyStore{}
		fence := booksT7LiveWriter(t, kind, tasks.ModePreview, ops.ResAuthors)
		_, err := (&Server{store: spy}).booksT7Deps("").NewWriter(fence).CreateAuthor("Selina Kyle")
		require.ErrorIs(t, err, tasks.ErrPreview)
		require.Empty(t, spy.called(), "store untouched")
	})
	t.Run("no run writer", func(t *testing.T) {
		spy := &booksT7SpyStore{}
		_, err := (&Server{store: spy}).booksT7Deps("").NewWriter(nil).CreateAuthor("Selina Kyle")
		require.ErrorIs(t, err, tasks.ErrOutsideEffects)
		require.Empty(t, spy.called(), "store untouched")
	})
}

// Cell: the rows of the writer's gated methods resolve, and WriteBackBooks's is
// the union of the shared rows of the store writes listed for it, with none of
// the resources of a retired family's own: every listed store write has a shared
// row, and the row is pinned so a change to it is a decision.
// Mutants, each killed by the named FAIL: list a store write the shared table
// has no row for (TestBooksT7WriterRows_Resolve: "has no shared row"); drop
// DeleteBook from the list (".../the row of WriteBackBooks: the pinned set",
// authors and field_states are no longer in it).
func TestBooksT7WriterRows_Resolve(t *testing.T) {
	rows := booksT7WriterRows()
	for _, m := range booksT7SharedRowMethods {
		require.NotEmpty(t, rows[m], "%s has no row", m)
	}
	shared := writer.SharedRows()
	for _, m := range booksT7WriteBackBooksStoreWrites {
		require.NotEmpty(t, shared[m], "%s has no shared row", m)
	}
	var got []string
	for _, r := range rows["WriteBackBooks"] {
		got = append(got, string(r))
	}
	slices.Sort(got)
	require.Equal(t, []string{
		"authors", "book_files", "books", "embeddings", "external_ids", "field_states",
		"files", "indexes", "metadata_cache", "operations", "sync_identity", "user_state",
	}, got, "the pinned set")
}

// Cell: the bulk-write-back kind declares exactly the row of its one gated call,
// no resource more and none fewer (the effects admission cell of the kind that
// reaches WriteBackBooks).
// Mutants, each killed by FAIL TestBooksT7Shared_BulkWriteBackEffectsAreTheRow:
// leave the old three-resource Effects ("declares the row of WriteBackBooks");
// add a thirteenth resource ("declares the row of WriteBackBooks").
func TestBooksT7Shared_BulkWriteBackEffectsAreTheRow(t *testing.T) {
	s := &Server{}
	var def tasks.Definition
	for _, d := range t7KindList(s, s.booksT7Deps(""), nil) {
		if d.Kind() == "books.library.bulk-write-back" {
			def = d
		}
	}
	require.NotNil(t, def)
	want := t7Res(booksT7WriteBackBooksRow()...)
	got := tasks.CommonOf(def).Effects.WriteSet()
	require.ElementsMatch(t, want, got, "declares the row of WriteBackBooks")
}

// booksT7SpyReporter is the progress reporter a bulk write-back is handed.
func booksT7SpyReporter() t7ProgressReporter { return t7ProgressReporter{env: t7Env{}} }

// Cell: WriteBackBooks is one gated write of its whole row: inside Effects it
// runs HEAD's loop (the store is read for each book); outside them nothing runs;
// and the loop writes no v1 checkpoint, because opstate: is a retired family
// under the tasks flip.
// Mutants, each killed by the named FAIL: restore the SaveCheckpoint call in
// runBulkWriteBack (TestBooksT7Shared_WriteBackBooksIsGated/live:
// "no opstate: write (retired under the flip)"); call runBulkWriteBack without
// the gate (.../outside effects: "the loop never ran"); stamp only books
// (.../live: the declared set of the kind refuses the first write: "clean").
func TestBooksT7Shared_WriteBackBooksIsGated(t *testing.T) {
	const kind = tasks.Kind("books.library.bulk-write-back")
	type spyT = booksT7MissingBooksStore
	mk := func(spy *spyT) *Server {
		return &Server{store: spy, metadataFetchService: metafetch.NewService(spy)}
	}
	// twelve books: the old checkpoint fired every ten.
	var ids []string
	for i := range 12 {
		ids = append(ids, fmt.Sprintf("book-%02d", i))
	}
	t.Run("live", func(t *testing.T) {
		spy := &spyT{}
		fence := writertest.ForSet(kind, tasks.CommonOf(mustKind(t, kind)).Effects.WriteSet(), tasks.ModeLive)
		w := mk(spy).booksT7Deps("").NewWriter(fence)
		require.NoError(t, w.WriteBackBooks(t.Context(), "op-write-back", ids, false, 0, booksT7SpyReporter(), nil))
		require.NoError(t, fence.RequireClean())
		require.Contains(t, spy.called(), "GetBookByID", "the loop ran")
		for _, c := range spy.called() {
			require.NotContains(t, []string{"SaveOperationState", "DeleteOperationState"}, c, "no opstate: write (retired under the flip)")
		}
		require.Len(t, fence.Writes(), 1, "one gated write for the whole call")
		require.Equal(t, "books.WriteBackBooks", fence.Writes()[0].Name)
	})
	t.Run("outside effects", func(t *testing.T) {
		spy := &spyT{}
		fence := writertest.ForSet(kind, t7Res(ops.ResBooks, ops.ResBookFiles, ops.ResFiles), tasks.ModeLive)
		w := mk(spy).booksT7Deps("").NewWriter(fence)
		err := w.WriteBackBooks(t.Context(), "op-write-back", ids, false, 0, booksT7SpyReporter(), nil)
		require.ErrorIs(t, err, tasks.ErrOutsideEffects)
		require.Empty(t, spy.called(), "the loop never ran")
	})
	t.Run("preview", func(t *testing.T) {
		spy := &spyT{}
		fence := writertest.ForSet(kind, tasks.CommonOf(mustKind(t, kind)).Effects.WriteSet(), tasks.ModePreview)
		err := mk(spy).booksT7Deps("").NewWriter(fence).WriteBackBooks(t.Context(), "op-write-back", ids, false, 0, booksT7SpyReporter(), nil)
		require.ErrorIs(t, err, tasks.ErrPreview)
		require.Empty(t, spy.called(), "the loop never ran")
	})
}

func mustKind(t *testing.T, kind tasks.Kind) tasks.Definition {
	t.Helper()
	s := &Server{}
	for _, d := range t7KindList(s, s.booksT7Deps(""), nil) {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("t7KindList builds no %s", kind)
	return nil
}

// Cell: InvalidateDedupCache drops every cached duplicate list, as HEAD's
// headAIMergeApply and headDedupOps do, not the author list alone
// (Server.InvalidateDedupCache).
// Mutant: call s.InvalidateDedupCache instead of dedupCache.InvalidateAll
// (killed by FAIL TestBooksT7Shared_InvalidateDedupCacheDropsEveryList,
// "the book duplicate list survives").
func TestBooksT7Shared_InvalidateDedupCacheDropsEveryList(t *testing.T) {
	c := cache.New[gin.H]("books-t7-shared-test", time.Hour)
	c.Set("author-duplicates", gin.H{"n": 1})
	c.Set("book-duplicates", gin.H{"n": 2})
	w := (&Server{dedupCache: c}).booksT7Deps("").NewWriter(&t7GateWriter{})
	w.InvalidateDedupCache()
	_, ok := c.Get("author-duplicates")
	require.False(t, ok, "the author duplicate list survives")
	_, ok = c.Get("book-duplicates")
	require.False(t, ok, "the book duplicate list survives")
}

// Cell: the activity pair writes the server's activity writer as HEAD's
// headT7Activity does (the flush closes the run's batch, the info row carries
// the run id and summary) and drops the call when the server has none.
// Mutants, each killed by the named FAIL: EmitActivityInfo swapped with
// FlushActivityOp (TestBooksT7Shared_ActivityReachesTheServerWriter: "info row
// missing"); nil writer dereferenced (".../no activity writer: panics").
func TestBooksT7Shared_ActivityReachesTheServerWriter(t *testing.T) {
	aw := activity.NewWriter(nil, 16)
	w := (&Server{activityWriter: aw}).booksT7Deps("").NewWriter(&t7GateWriter{})
	w.EmitActivityInfo("run-1", "library.bulk-write-back", "library", "Bulk tag write-back completed for 2 books", activity.AlwaysShow)
	w.FlushActivityOp("run-1")
	var found bool
	for _, e := range drainActivity(aw) {
		if e.OperationID == "run-1" && strings.Contains(e.Summary, "completed for 2 books") {
			found = true
		}
	}
	require.True(t, found, "info row missing")

	require.NotPanics(t, func() {
		nw := (&Server{}).booksT7Deps("").NewWriter(&t7GateWriter{})
		nw.EmitActivityInfo("run-1", "x", "y", "z")
		nw.FlushActivityOp("run-1")
	}, "no activity writer: panics")
}

// Cell: the door builds the kinds whose every method is built, and no stub body
// runs for them. With only the shared twelve built, those are
// library.size-refresh, ai.author-review and library.bulk-write-back.
// Mutant: leave a shared method out of booksT7SharedBuilt (killed by FAIL
// TestBooksT7Shared_DoorBuildsTheKindsTheSharedTwelveComplete: "the door leaves
// out ...").
func TestBooksT7Shared_DoorBuildsTheKindsTheSharedTwelveComplete(t *testing.T) {
	booksT7OnlyBuilt(t, booksT7SharedBuilt...)
	spy := &booksT7SpyStore{}
	s := &Server{store: spy, metadataFetchService: metafetch.NewService(spy)}
	before := booksT7StubEntries.Load()
	res, err := s.BooksT7Kinds("books.library.scan", nil)
	require.NoError(t, err)
	require.Equal(t, before, booksT7StubEntries.Load(), "the door enters no stub body")
	require.Equal(t, []tasks.Kind{"books.ai.author-review", "books.library.bulk-write-back", "books.library.size-refresh"},
		booksT7KindsOf(res.Defs), "the door leaves out a kind the shared twelve complete")
	for _, d := range res.Defs {
		for _, m := range booksT7Reaches[d.Kind()] {
			require.True(t, slices.Contains(booksT7SharedBuilt, m), "%s reaches %s, which is not shared", d.Kind(), m)
		}
	}
}
