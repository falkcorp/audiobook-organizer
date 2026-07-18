// file: internal/server/external_id_backfill.go
// version: 1.6.0
// guid: a3b4c5d6-e7f8-4a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-07-18

package server

import (
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// ExternalIDStore defines the external ID mapping operations.
// Store implementations should satisfy this interface. Integration code
// performs a runtime type assertion: if the underlying store does not yet
// implement these methods the calls gracefully no-op.
type ExternalIDStore interface {
	CreateExternalIDMapping(mapping *database.ExternalIDMapping) error
	GetBookByExternalID(source, externalID string) (string, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	IsExternalIDTombstoned(source, externalID string) (bool, error)
	TombstoneExternalID(source, externalID string) error
	ReassignExternalIDs(oldBookID, newBookID string) error
	BulkCreateExternalIDMappings(mappings []database.ExternalIDMapping) error
}

// asExternalIDStore returns the ExternalIDStore if the given store implements
// it, or nil otherwise. Callers should check for nil before using. Accepts
// `any` so callers holding a narrow sub-interface of database.Store (e.g.
// audiobookStore) can still pass through — the type assertion checks the
// underlying concrete type regardless of the static handle type.
func asExternalIDStore(s any) ExternalIDStore {
	if s == nil {
		return nil
	}
	if eid, ok := s.(ExternalIDStore); ok {
		return eid
	}
	return nil
}

// backfillExternalIDs delegates to itunes.BackfillExternalIDs.
// The domain package handles idempotency checks and coordinates book-level,
// file-level, and track-level PID registration.
//
// progress is forwarded straight to itunes.BackfillExternalIDs so the
// whole-library pagination reports live progress (H7). The domain error is
// now returned to the caller instead of being demoted to a Warn log — a
// failure here (e.g. a write error mid-backfill) used to leave the op
// reporting success unconditionally.
func (s *Server) backfillExternalIDs(progress func(processed, total int, msg string)) error {
	store := s.Store()
	if store == nil {
		return nil
	}

	eidStore := asExternalIDStore(store)
	if eidStore == nil {
		slog.Debug("backfillExternalIDs store does not implement ExternalIDStore, skipping")
		return nil
	}

	// Delegate to the itunes domain package. s.bgCtx aborts the backfill
	// on shutdown so it can't outlive the store and crash on
	// "pebble: closed" in CreateExternalIDMapping.
	if err := itunes.BackfillExternalIDs(s.bgCtx, &externalIDStoreAdapter{eidStore: eidStore, store: store}, progress); err != nil {
		slog.Warn("backfillExternalIDs", "err", err)
		return err
	}
	return nil
}

// externalIDStoreAdapter adapts ExternalIDStore and database.Store to
// itunes.ExternalIDBackfillStore interface.
type externalIDStoreAdapter struct {
	eidStore ExternalIDStore
	store    database.Store
}

func (a *externalIDStoreAdapter) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return a.store.GetAllBooksCore(limit, offset)
}

func (a *externalIDStoreAdapter) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return a.store.GetBookFiles(bookID)
}

func (a *externalIDStoreAdapter) CreateExternalIDMapping(mapping *database.ExternalIDMapping) error {
	return a.eidStore.CreateExternalIDMapping(mapping)
}

func (a *externalIDStoreAdapter) BulkCreateExternalIDMappings(mappings []database.ExternalIDMapping) error {
	return a.eidStore.BulkCreateExternalIDMappings(mappings)
}

func (a *externalIDStoreAdapter) SetSetting(key, value, dataType string, internal bool) error {
	return a.store.SetSetting(key, value, dataType, internal)
}
