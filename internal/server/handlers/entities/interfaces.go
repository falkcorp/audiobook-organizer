// file: internal/server/handlers/entities/interfaces.go
// version: 1.1.0
// guid: 43710377-fdb3-490c-872e-fd03309163be
// last-edited: 2026-07-05

// Narrow dependency interfaces for the entities domain handlers (authors,
// series, narrators, works). Each interface lists only the methods the
// handlers actually call so package entities stays decoupled from the concrete
// service / store / registry implementations and avoids importing package
// server (which would create an import cycle).

package entities

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/work"
)

// EntitiesStore is the narrow database.Store subset the entities handlers
// require. The concrete database.Store implementations satisfy it.
type EntitiesStore interface {
	// Authors
	CountAuthors() (int, error)
	CreateAuthor(name string) (*database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	UpdateAuthorName(id int, name string) error
	DeleteAuthor(id int) error
	GetAuthorAliases(authorID int) ([]database.AuthorAlias, error)
	CreateAuthorAlias(authorID int, aliasName string, aliasType string) (*database.AuthorAlias, error)
	DeleteAuthorAlias(id int) error
	// GetBooksByAuthorIDCore is Core-typed (STOREFID P3-W2) — see
	// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
	GetBooksByAuthorIDCore(authorID int) ([]database.BookCore, error)
	GetBooksByAuthorIDWithRole(authorID int) ([]database.Book, error)

	// Book authors / narrators join tables
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	SetBookNarrators(bookID string, narrators []database.BookNarrator) error
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)

	// Narrators
	CreateNarrator(name string) (*database.Narrator, error)
	GetNarratorByName(name string) (*database.Narrator, error)
	ListNarrators() ([]database.Narrator, error)

	// Series
	CountSeries() (int, error)
	CreateSeries(name string, authorID *int) (*database.Series, error)
	GetSeriesByID(id int) (*database.Series, error)
	GetBooksBySeriesID(seriesID int) ([]database.Book, error)
	UpdateSeriesName(id int, name string) error
	DeleteSeries(id int) error

	// Works
	GetAllWorks() ([]database.Work, error)
	GetAllWorkBookCounts() (map[string]int, error)
	GetBooksByWorkID(workID string) ([]database.Book, error)

	// Operations (legacy operation row creation for author-merge /
	// resolve-production-author).
	CreateOperation(id, opType string, folderPath *string) (*database.Operation, error)
}

// WorkService is the narrow audiobook *work.WorkService subset used by the work
// CRUD handlers.
type WorkService interface {
	ListWorks() (*work.WorkListResponse, error)
	CreateWork(w *database.Work) (*database.Work, error)
	GetWork(id string) (*database.Work, error)
	UpdateWork(id string, w *database.Work) (*database.Work, error)
	DeleteWork(id string) error
}

// AuthorSeriesService is the narrow *audiobooks.AuthorSeriesService subset used
// by the cached author/series list handlers.
type AuthorSeriesService interface {
	ListAuthorsWithCounts() (*audiobooks.AuthorWithCountListResponse, error)
	ListSeriesWithCounts() (*audiobooks.SeriesWithCountsResponse, error)
}

// OperationsRegistry is the narrow operations-registry subset the entities
// handlers require. Only EnqueueOp is called (author-merge and
// resolve-production-author). The variadic opts param is preserved so the
// concrete *opsregistry.Registry satisfies the interface.
type OperationsRegistry interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}
