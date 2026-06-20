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

// ExternalIDStore covers ExternalIDMapping CRUD + tombstones.
type ExternalIDStore interface {
	CreateExternalIDMapping(mapping *ExternalIDMapping) error
	GetBookByExternalID(source, externalID string) (string, error)
	GetExternalIDsForBook(bookID string) ([]ExternalIDMapping, error)
	IsExternalIDTombstoned(source, externalID string) (bool, error)
	TombstoneExternalID(source, externalID string) error
	ReassignExternalIDs(oldBookID, newBookID string) error
	ReassignExternalID(source, externalID, newBookID string) error
	BulkCreateExternalIDMappings(mappings []ExternalIDMapping) error
	MarkExternalIDRemoved(source, externalID string) error
	SetExternalIDProvenance(source, externalID, provenance string) error
	GetRemovedExternalIDs(source string) ([]ExternalIDMapping, error)
}

// PathHistoryStore covers file rename/move history.
type PathHistoryStore interface {
	RecordPathChange(change *BookPathChange) error
	GetBookPathHistory(bookID string) ([]BookPathChange, error)
}
