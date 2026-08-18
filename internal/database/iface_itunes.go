// file: internal/database/iface_itunes.go
// version: 1.1.0
// last-edited: 2026-06-20
// guid: f3bad9f9-8dd9-47af-9148-e20545dc15f2

package database

import "time"

// ITunesStateStore covers iTunes library fingerprints and deferred updates.
type ITunesStateStore interface {
	SaveLibraryFingerprint(path string, size int64, modTime time.Time, crc32 uint32) error
	GetLibraryFingerprint(path string) (*LibraryFingerprintRecord, error)
	CreateDeferredITunesUpdate(bookID, persistentID, oldPath, newPath, updateType string) error
	GetPendingDeferredITunesUpdates() ([]DeferredITunesUpdate, error)
	MarkDeferredITunesUpdateApplied(id int) error
	GetDeferredITunesUpdatesByBookID(bookID string) ([]DeferredITunesUpdate, error)
}

// ExternalIDReader reads external ID mappings.
type ExternalIDReader interface {
	GetBookByExternalID(source, externalID string) (string, error)
	GetExternalIDsForBook(bookID string) ([]ExternalIDMapping, error)
	GetRemovedExternalIDs(source string) ([]ExternalIDMapping, error)
}

// ExternalIDWriter creates and annotates external ID mappings.
type ExternalIDWriter interface {
	CreateExternalIDMapping(mapping *ExternalIDMapping) error
	BulkCreateExternalIDMappings(mappings []ExternalIDMapping) error
	SetExternalIDProvenance(source, externalID, provenance string) error
	MarkExternalIDRemoved(source, externalID string) error
}

// ExternalIDLifecycle covers tombstoning and reassignment.
type ExternalIDLifecycle interface {
	IsExternalIDTombstoned(source, externalID string) (bool, error)
	TombstoneExternalID(source, externalID string) error
	ReassignExternalIDs(oldBookID, newBookID string) error
	ReassignExternalID(source, externalID, newBookID string) error
}

// ExternalIDStore covers ExternalIDMapping CRUD + tombstones.
//
// Split into the 3 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it, because every implementation -- PebbleStore (496 methods)
// and database.MockStore (399) among them -- fails to compile on a dropped or
// re-signatured method.
type ExternalIDStore interface {
	ExternalIDReader
	ExternalIDWriter
	ExternalIDLifecycle
}

// PathHistoryStore covers file rename/move history.
type PathHistoryStore interface {
	RecordPathChange(change *BookPathChange) error
	GetBookPathHistory(bookID string) ([]BookPathChange, error)
}
