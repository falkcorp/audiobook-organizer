// file: internal/database/iface_author.go
// version: 1.3.0
// guid: 2e3b78c0-c989-48c0-a324-b88ea52b1ccd
// last-edited: 2026-07-05

package database

import "context"

// AuthorLookupReader resolves authors by identifier or name.
type AuthorLookupReader interface {
	GetAllAuthors() ([]Author, error)
	GetAuthorByID(id int) (*Author, error)
	GetAuthorByName(name string) (*Author, error)
	// GetAuthorsByIDs returns a map from authorID → *Author for the given IDs.
	// Missing IDs are absent from the map. Returns empty map (not nil) for empty input.
	GetAuthorsByIDs(ids []int) (map[int]*Author, error)
	GetAuthorTombstone(oldID int) (int, error)
}

// AuthorAliasReader reads author aliases.
type AuthorAliasReader interface {
	GetAuthorAliases(authorID int) ([]AuthorAlias, error)
	GetAllAuthorAliases() ([]AuthorAlias, error)
	FindAuthorByAlias(aliasName string) (*Author, error)
}

// AuthorBookReader reads the author-book relationship.
type AuthorBookReader interface {
	GetBookAuthors(bookID string) ([]BookAuthor, error)
	// GetBooksByAuthorIDWithRoleCore is Core-typed (STOREFID P3-W2b): the
	// return type is BookCore, not Book, so the nine heavy fields
	// (Description, VersionNotes, BookSigV1, BookSigV1Mask, BookSigSegments,
	// BookSigBuiltAt, BookSigCoveragePct, Author, Series) being absent is
	// compiler-enforced rather than silently nil'd. See
	// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]BookCore, error)
	// GetAuthorsByBookIDs returns a map from bookID → []Author for all given book IDs.
	// Returns an empty map (not nil) if bookIDs is empty.
	GetAuthorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]Author, error)
}

// AuthorCountReader reports per-author aggregates.
type AuthorCountReader interface {
	GetAllAuthorBookCounts() (map[int]int, error)
	GetAllAuthorFileCounts() (map[int]int, error)
}

// AuthorReader is the read-only author slice (authors + aliases + book-author joins).
//
// Split into the 4 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it, because every implementation -- PebbleStore (496 methods)
// and database.MockStore (399) among them -- fails to compile on a dropped or
// re-signatured method.
type AuthorReader interface {
	AuthorLookupReader
	AuthorAliasReader
	AuthorBookReader
	AuthorCountReader
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
