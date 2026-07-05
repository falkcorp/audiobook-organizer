// file: internal/database/iface_author.go
// version: 1.2.1
// guid: 2e3b78c0-c989-48c0-a324-b88ea52b1ccd
// last-edited: 2026-07-05

package database

import "context"

// AuthorReader is the read-only author slice (authors + aliases + book-author joins).
type AuthorReader interface {
	GetAllAuthors() ([]Author, error)
	GetAuthorByID(id int) (*Author, error)
	GetAuthorByName(name string) (*Author, error)
	GetAuthorAliases(authorID int) ([]AuthorAlias, error)
	GetAllAuthorAliases() ([]AuthorAlias, error)
	FindAuthorByAlias(aliasName string) (*Author, error)
	GetBookAuthors(bookID string) ([]BookAuthor, error)
	// GetBooksByAuthorIDWithRole is SLIM (memdb projection) — see
	// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
	GetBooksByAuthorIDWithRole(authorID int) ([]Book, error)
	GetAllAuthorBookCounts() (map[int]int, error)
	GetAllAuthorFileCounts() (map[int]int, error)
	GetAuthorTombstone(oldID int) (int, error)
	// GetAuthorsByBookIDs returns a map from bookID → []Author for all given book IDs.
	// Returns an empty map (not nil) if bookIDs is empty.
	GetAuthorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]Author, error)
	// GetAuthorsByIDs returns a map from authorID → *Author for the given IDs.
	// Missing IDs are absent from the map. Returns empty map (not nil) for empty input.
	GetAuthorsByIDs(ids []int) (map[int]*Author, error)
}

// AuthorWriter is the write-only author slice.
type AuthorWriter interface {
	CreateAuthor(name string) (*Author, error)
	DeleteAuthor(id int) error
	UpdateAuthorName(id int, name string) error
	CreateAuthorAlias(authorID int, aliasName string, aliasType string) (*AuthorAlias, error)
	DeleteAuthorAlias(id int) error
	SetBookAuthors(bookID string, authors []BookAuthor) error
	CreateAuthorTombstone(oldID, canonicalID int) error
	ResolveTombstoneChains() (int, error)
}

// AuthorStore combines both halves.
type AuthorStore interface {
	AuthorReader
	AuthorWriter
}
