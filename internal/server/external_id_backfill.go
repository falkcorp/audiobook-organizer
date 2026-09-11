// file: internal/server/external_id_backfill.go
// version: 1.8.0
// guid: a3b4c5d6-e7f8-4a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-09-11

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

// backfillExternalIDs delegates to the itunes domain package, which
// coordinates book-level, file-level, and track-level PID registration.
//
// force selects the entry point. false (the boot path in startBackfills)
// goes through itunes.BackfillExternalIDsOnce, which skips the whole
// full-library scan when a previous run recorded completion under
// itunes.ExternalIDBackfillDoneKey — before that gate existed the scan ran
// at every server start (SQ-04). true (the manual maintenance op) calls
// itunes.BackfillExternalIDs directly so an operator's explicit request is
// never silently skipped; the writes are idempotent either way.
//
// progress is forwarded straight to itunes.BackfillExternalIDs so the
// whole-library pagination reports live progress (H7). The domain error is
// now returned to the caller instead of being demoted to a Warn log — a
// failure here (e.g. a write error mid-backfill) used to leave the op
// reporting success unconditionally.
func (s *Server) backfillExternalIDs(progress func(processed, total int, msg string), force bool) error {
	store := s.Ops()
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
	adapter := &externalIDStoreAdapter{eidStore: eidStore, store: store}
	run := itunes.BackfillExternalIDsOnce
	if force {
		run = itunes.BackfillExternalIDs
	}
	if err := run(s.bgCtx, adapter, progress); err != nil {
		slog.Warn("backfillExternalIDs", "err", err)
		return err
	}
	return nil
}

// externalIDStoreAdapter adapts ExternalIDStore and database.Store to
// itunes.ExternalIDBackfillStore interface.
type externalIDStoreAdapter struct {
	eidStore ExternalIDStore
	store    externalIDBackfillStore
}

func (a *externalIDStoreAdapter) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return a.store.GetAllBooksCore(limit, offset)
}

func (a *externalIDStoreAdapter) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return a.store.GetAllBookFilesCore()
}

func (a *externalIDStoreAdapter) GetSetting(key string) (*database.Setting, error) {
	return a.store.GetSetting(key)
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
