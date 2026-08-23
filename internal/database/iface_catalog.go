// file: internal/database/iface_catalog.go
// version: 1.2.0
// guid: 76cf7dcf-546c-424d-8d2b-26c8a4354506
// last-edited: 2026-08-22

package database

import (
	"context"
)

// Catalog entities beyond books: collections, works, narrators, versions.
//
// Split out of iface_misc.go on 2026-08-18, which held 27 interface
// declarations in one file. A file named `misc` is where wide interfaces go to
// avoid review: BookFileStore reached 27 methods while living there.

// CollectionStore covers server-wide collections: static book lists and
// dynamic (live-evaluated) queries.
//
// There is no ForUser variant, unlike UserPlaylistStore. Collections are shared
// by every user by design, so a per-user list would be wrong rather than
// missing.
type CollectionStore interface {
	CreateCollection(col *Collection) (*Collection, error)
	GetCollection(id string) (*Collection, error)
	GetCollectionByName(name string) (*Collection, error)
	ListCollections(collectionType string, limit, offset int) ([]Collection, int, error)
	// UpdateCollection is compare-and-swap on col.Version: the implementation
	// rejects the write with an error satisfying
	// errors.Is(err, ErrCollectionVersionConflict) when col.Version does not
	// match the currently-stored row's Version, rather than always succeeding.
	// A caller that read the row first (the normal case — every production
	// call site does) is unaffected; a caller that constructs a Collection
	// without reading it first can now get a conflict error where it
	// previously always got nil. Test fakes implementing this interface must
	// wrap the same sentinel, not reproduce its wording. See
	// pebble_store_collections.go for the check and its rationale.
	UpdateCollection(col *Collection) error
	DeleteCollection(id string) error
}

// WorkStore covers Work CRUD.
type WorkStore interface {
	GetAllWorks() ([]Work, error)
	GetWorkByID(id string) (*Work, error)
	CreateWork(work *Work) (*Work, error)
	UpdateWork(id string, work *Work) (*Work, error)
	DeleteWork(id string) error
	GetBooksByWorkID(workID string) ([]Book, error)
	GetAllWorkBookCounts() (map[string]int, error)
}

// NarratorStore covers narrators + book-narrator joins.
type NarratorStore interface {
	CreateNarrator(name string) (*Narrator, error)
	GetNarratorByID(id int) (*Narrator, error)
	GetNarratorByName(name string) (*Narrator, error)
	ListNarrators() ([]Narrator, error)
	GetBookNarrators(bookID string) ([]BookNarrator, error)
	SetBookNarrators(bookID string, narrators []BookNarrator) error
	// GetNarratorsByBookIDs returns a map from bookID → []Narrator for all given book IDs.
	// Returns an empty map (not nil) if bookIDs is empty.
	GetNarratorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]Narrator, error)
}

// BookVersionReader looks up book versions by id, book, or torrent hash.
type BookVersionReader interface {
	GetBookVersion(id string) (*BookVersion, error)
	GetBookVersionsByBookID(bookID string) ([]BookVersion, error)
	GetActiveVersionForBook(bookID string) (*BookVersion, error)
	GetBookVersionByTorrentHash(hash string) (*BookVersion, error)
}

// BookVersionWriter creates, updates and deletes book versions.
type BookVersionWriter interface {
	CreateBookVersion(v *BookVersion) (*BookVersion, error)
	UpdateBookVersion(v *BookVersion) error
	DeleteBookVersion(id string) error
}

// BookVersionDispositionReader lists versions by trash/purge disposition.
type BookVersionDispositionReader interface {
	ListTrashedBookVersions() ([]BookVersion, error)
	ListPurgedBookVersions() ([]BookVersion, error)
}

// BookVersionStore covers version CRUD, lifecycle, and lookups.
//
// Split into the 3 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it.
type BookVersionStore interface {
	BookVersionReader
	BookVersionWriter
	BookVersionDispositionReader
}
