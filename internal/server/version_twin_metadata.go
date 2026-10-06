// file: internal/server/version_twin_metadata.go
// version: 1.0.0
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
