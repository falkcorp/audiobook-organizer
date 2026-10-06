// file: internal/server/version_twin_metadata.go
// version: 1.1.0
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

// isbnIndexFlag is the completion flag of the isbn-index-build op. It is not
// on database.Store, so it is resolved through the decorator chain
// (database.AsCapability), as the op itself does; it is a read of one
// setting, so bypassing indexedStore's write overrides costs nothing.
type isbnIndexFlag interface{ IsISBNIndexBuilt() bool }

// BookIDsWithASIN implements maintenanceplugin.VersionTwinMetadata. indexed
// is false until the isbn-index-build op has run (IsISBNIndexBuilt): before
// that the index misses every book created before it existed.
func (s *Server) BookIDsWithASIN(asin string) ([]string, bool, error) {
	if s.store == nil {
		return nil, false, fmt.Errorf("database not initialized")
	}
	flag, ok := database.AsCapability[isbnIndexFlag](s.store)
	if !ok || !flag.IsISBNIndexBuilt() {
		return nil, false, nil
	}
	ids, err := s.store.GetBookIDsByISBNASIN("", "", asin)
	return ids, true, err
}
