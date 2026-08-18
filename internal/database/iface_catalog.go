// file: internal/database/iface_catalog.go
// version: 1.0.0
// guid: 76cf7dcf-546c-424d-8d2b-26c8a4354506
// last-edited: 2026-08-18

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

// BookVersionStore covers version CRUD, lifecycle, and lookups.
type BookVersionStore interface {
	CreateBookVersion(v *BookVersion) (*BookVersion, error)
	GetBookVersion(id string) (*BookVersion, error)
	GetBookVersionsByBookID(bookID string) ([]BookVersion, error)
	GetActiveVersionForBook(bookID string) (*BookVersion, error)
	UpdateBookVersion(v *BookVersion) error
	DeleteBookVersion(id string) error
	GetBookVersionByTorrentHash(hash string) (*BookVersion, error)
	ListTrashedBookVersions() ([]BookVersion, error)
	ListPurgedBookVersions() ([]BookVersion, error)
}
