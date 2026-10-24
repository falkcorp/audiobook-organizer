// file: internal/server/external_id_backfill.go
// version: 1.12.0
// guid: a3b4c5d6-e7f8-4a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-10-24

package server

import (
	"context"
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
// coordinates book-level, file-level, and track-level PID registration. It
// backs maintenance.external-id-backfill and runs on that op's ctx.
//
// ifNeeded selects the entry point. true (boot enqueues the op with
// {"if_needed": true}) goes through itunes.BackfillExternalIDsOnce, which
// skips the whole full-library scan when a previous run recorded completion
// under itunes.ExternalIDBackfillDoneKey -- before that gate existed the scan
// ran at every server start (SQ-04) -- and reports skipped=true when it did.
// false (an operator's run) calls itunes.BackfillExternalIDs directly so an
// explicit request is never silently skipped; the writes are idempotent
// either way.
//
// ctx is the op's run context, not s.bgCtx: the registry cancels it at the
// start of its shutdown drain, which runs BEFORE bgCtx is cancelled, so a run
// that watched bgCtx would keep writing CreateExternalIDMapping through the
// whole drain and could outlive it into CloseStore ("pebble: closed").
//
// progress is forwarded straight to itunes.BackfillExternalIDs so the
// whole-library pagination reports live progress (H7). The domain error is
// returned to the caller, so a failure (e.g. a write error mid-backfill)
// fails the op.
func (s *Server) backfillExternalIDs(ctx context.Context, progress func(processed, total int, msg string), ifNeeded bool) (skipped bool, err error) {
	store := s.Ops()
	if store == nil {
		return false, nil
	}

	eidStore := asExternalIDStore(store)
	if eidStore == nil {
		slog.Debug("backfillExternalIDs store does not implement ExternalIDStore, skipping")
		return false, nil
	}

	adapter := &externalIDStoreAdapter{eidStore: eidStore, store: store}
	if ifNeeded {
		skipped, err = itunes.BackfillExternalIDsOnce(ctx, adapter, progress)
	} else {
		err = itunes.BackfillExternalIDs(ctx, adapter, progress)
	}
	if err != nil {
		slog.Warn("backfillExternalIDs", "err", err)
		return false, err
	}
	// itunes.BackfillExternalIDs returns nil when ctx is cancelled (it stops
	// early and leaves the done flag unset); report the cancel so the op does
	// not read as a completed run.
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return skipped, nil
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

// GetAllBookFilesCoreIncludingRetired reads every book_file row, retired ones
// kept, through the store's retired reader (resolved through decorators). A
// store without the reader answers with its plain row list only when it
// declares it never holds a retired row (database.NoRetiredBookFiles);
// any other store returns database.ErrRetiredReaderUnavailable.
func (a *externalIDStoreAdapter) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return database.AllBookFilesCoreIncludingRetired(a.store)
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
