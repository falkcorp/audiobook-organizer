// file: internal/server/version_twin_metadata.go
// version: 1.2.0
// guid: 8d3e1a57-6c2b-4f90-b4e8-71a9c05d2f36
// last-edited: 2026-10-06

package server

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
)

// This file implements maintenanceplugin.VersionTwinMetadata for the version
// twin fixer (maintenance.version-twin-metadata): it copies a version group
// twin's applied metadata or cached candidates onto the group's primary
// through the metadata fetch service the rest of the server uses.

// VersionTwinMetadataService implements maintenanceplugin.VersionTwinMetadata.
// nil when the metadata fetch service is not wired. The nil check is explicit:
// returning a nil *metafetch.Service as the interface would be a non-nil
// interface holding a nil pointer.
func (s *Server) VersionTwinMetadataService() maintenanceplugin.VersionTwinMetadataService {
	if s.metadataFetchService == nil {
		return nil
	}
	return s.metadataFetchService
}

// BooksWithMetadataSourceHash implements maintenanceplugin.VersionTwinMetadata.
func (s *Server) BooksWithMetadataSourceHash(hash string) ([]database.Book, error) {
	if s.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return s.store.GetBooksByMetadataSourceHash(hash)
}

// metadataHashInMemory is PebbleStore's memdb-only hash lookup. It is not on
// database.Store (398 methods), so it is resolved through the decorator chain
// (database.AsCapability); it is a read, so bypassing indexedStore's write
// overrides costs nothing.
type metadataHashInMemory interface {
	GetBooksByMetadataSourceHashInMemory(hash string) ([]database.Book, error)
}

// BooksWithMetadataSourceHashInMemory implements
// maintenanceplugin.VersionTwinMetadata: the memdb-only lookup, which never
// scans. A store without memdb answers ErrMemDBNotReady, so the caller
// fails closed.
func (s *Server) BooksWithMetadataSourceHashInMemory(hash string) ([]database.Book, error) {
	if s.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	m, ok := database.AsCapability[metadataHashInMemory](s.store)
	if !ok {
		return nil, fmt.Errorf("%w: this store has no in-memory metadata_source_hash index", database.ErrMemDBNotReady)
	}
	return m.GetBooksByMetadataSourceHashInMemory(hash)
}

// isbnIndexFlag is the completion flag of the isbn-index-build op. It is not
// on database.Store, so it is resolved through the decorator chain
// (database.AsCapability), as the op itself does; it is a read of one
// setting, so bypassing indexedStore's write overrides costs nothing.
type isbnIndexFlag interface{ IsISBNIndexBuilt() bool }

// BookIDsWithIdentifiers implements maintenanceplugin.VersionTwinMetadata:
// the books whose ISBN-10, ISBN-13 or ASIN is one of the given (exact, as
// stored; empty values are not looked up). indexed is false until the
// isbn-index-build op has run (IsISBNIndexBuilt): before that the index
// misses every book created before it existed.
func (s *Server) BookIDsWithIdentifiers(isbn10, isbn13, asin string) ([]string, bool, error) {
	if s.store == nil {
		return nil, false, fmt.Errorf("database not initialized")
	}
	flag, ok := database.AsCapability[isbnIndexFlag](s.store)
	if !ok || !flag.IsISBNIndexBuilt() {
		return nil, false, nil
	}
	ids, err := s.store.GetBookIDsByISBNASIN(isbn10, isbn13, asin)
	return ids, true, err
}
