// file: internal/server/books_t7_adapter.go
// version: 1.1.0
// guid: 7571e5c9-96d7-4d5e-b964-9cd8c8e6b193
// last-edited: 2026-11-28

package server

// The host adapter that builds t7Deps for the server-t7 kinds (F5-18,
// TODO-F5-18-A6-SERVER-T7): every kind t7KindList builds, forty-five of them. It
// is one adapter over *Server, as registerAccountLifecycle is for the account
// lifecycle: it reuses the head* adapters' methods on the server and keeps every
// unexported server type unexported.
//
// DARK. Nothing outside tests calls the door (BooksT7Kinds, books_t7_door.go),
// and every method refuses until a group fills it in: books_t7_library.go is
// group A and books_t7_catalog.go is group B, each holding the methods only its
// kinds reach. This file holds the types, the deps and the methods both groups'
// kinds reach, or no kind does.
//
// Nothing here panics. A method with an error result returns an ErrKindNotBuilt
// that names it and the kinds that reach it. A method without one is
// unreachable by construction: the door builds a kind only when every method of
// its row in booksT7Reaches is built, so such a body never runs; it records the
// entry in booksT7StubEntries and returns zero values, which a cell asserts
// stays at zero through the door.
//
// Why every method is written out and none is promoted from an embedded
// interface: a nil embedded interface satisfies t7Store and t7Writer at
// compile time and then panics, unnamed, at the first call.

import (
	"context"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/mediacore/tasks"
)

// booksT7Adapter is the one host adapter: the server both views read. A method
// that needs a field of s which is unset refuses by name rather than
// dereferencing it (the fixture for that is &Server{}).
type booksT7Adapter struct{ s *Server }

// booksT7Store is the read side of the adapter, the t7Store of t7Deps.
type booksT7Store struct{ a *booksT7Adapter }

// booksT7Writer is the write side of the adapter for one run, the t7Writer a
// kind's core gets from t7NewWriter. w is the run's kernel-gated tasks.Writer,
// the one every write must go through (R10 item 4).
type booksT7Writer struct {
	a *booksT7Adapter
	w tasks.Writer
}

var (
	_ t7Store  = (*booksT7Store)(nil)
	_ t7Writer = (*booksT7Writer)(nil)
)

// booksT7Deps is the t7Deps of the adapter over s. standDownKey is the
// concurrency key the library scan kinds run under, the same single source as
// Maintenance.StandDownKey in the registration inputs.
func (s *Server) booksT7Deps(standDownKey tasks.Key) t7Deps {
	a := &booksT7Adapter{s: s}
	return t7Deps{
		Store:        &booksT7Store{a: a},
		StandDownKey: standDownKey,
		NewWriter:    func(w tasks.Writer) t7Writer { return &booksT7Writer{a: a, w: w} },
	}
}

func (a *booksT7Store) GetAllAuthors() ([]database.Author, error) {
	return nil, booksT7NotBuilt("GetAllAuthors")
}

func (a *booksT7Store) GetAllImportPaths() ([]database.ImportPath, error) {
	return nil, booksT7NotBuilt("GetAllImportPaths")
}

func (a *booksT7Store) GetAuthorAliases(_ int) ([]database.AuthorAlias, error) {
	return nil, booksT7NotBuilt("GetAuthorAliases")
}

func (a *booksT7Store) GetAuthorByID(_ int) (*database.Author, error) {
	return nil, booksT7NotBuilt("GetAuthorByID")
}

func (a *booksT7Store) GetAuthorByName(_ string) (*database.Author, error) {
	return nil, booksT7NotBuilt("GetAuthorByName")
}

func (a *booksT7Store) GetBookAuthors(_ string) ([]database.BookAuthor, error) {
	return nil, booksT7NotBuilt("GetBookAuthors")
}

func (a *booksT7Store) GetBookByID(_ string) (*database.Book, error) {
	return nil, booksT7NotBuilt("GetBookByID")
}

func (a *booksT7Store) GetBookTagsDetailed(_ string) ([]database.BookTag, error) {
	return nil, booksT7NotBuilt("GetBookTagsDetailed")
}

func (a *booksT7Store) GetBooksByAuthorIDWithRoleCore(_ int) ([]database.BookCore, error) {
	return nil, booksT7NotBuilt("GetBooksByAuthorIDWithRoleCore")
}

func (a *booksT7Writer) CreateAuthor(_ string) (*database.Author, error) {
	return nil, booksT7NotBuilt("CreateAuthor")
}

func (a *booksT7Writer) EmitActivityInfo(_ string, _ string, _ string, _ string, _ ...string) {
	booksT7Entered("EmitActivityInfo")
}

func (a *booksT7Writer) FlushActivityOp(_ string) {
	booksT7Entered("FlushActivityOp")
}

func (a *booksT7Writer) InvalidateDedupCache() {
	booksT7Entered("InvalidateDedupCache")
}

func (a *booksT7Writer) WriteBackBooks(_ context.Context, _ string, _ []string, _ bool, _ int, _ operations.ProgressReporter, _ func(bookID string)) error {
	return booksT7NotBuilt("WriteBackBooks")
}
