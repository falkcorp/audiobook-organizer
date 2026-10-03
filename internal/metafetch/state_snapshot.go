// file: internal/metafetch/state_snapshot.go
// version: 1.1.0
// guid: 3a682c02-4109-4286-bcd5-ad66eecac28e
// last-edited: 2026-10-03

package metafetch

import (
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
)

// The ONE read-modify-write of a book's whole field-state snapshot. Four
// copies (this package's Service and MetadataStateService,
// audiobooks.AudiobookService and server.Server) each read the rows into a
// map, let the caller change it, and wrote every entry back while deleting
// every row missing from the map. Read outside any lock, that erased
// whatever was written between the read and the save: a person's override, a
// clear (resurrected), a Repairs lock (repairs.Writer.LockFields). They now
// all go through WithStateSnapshot, which holds the book's field-state
// stripe (database.LockMetadataState) from the read through the save. Every
// other writer of the rows takes the same stripe: LockFields,
// database.RecordUserOverrides, database.ClaimRepairLocks and the revert's
// lock lift.

// StateFromRows turns a book's field-state rows into the snapshot map. For
// read-only callers; a caller that saves uses WithStateSnapshot.
func StateFromRows(rows []database.MetadataFieldState) map[string]MetadataFieldState {
	state := make(map[string]MetadataFieldState, len(rows))
	for _, entry := range rows {
		state[entry.Field] = MetadataFieldState{
			FetchedValue:   metastate.Decode(entry.FetchedValue),
			OverrideValue:  metastate.Decode(entry.OverrideValue),
			OverrideLocked: entry.OverrideLocked,
			LockSource:     entry.LockSource,
			UpdatedAt:      entry.UpdatedAt,
		}
	}
	return state
}

// StateSnapshotStore is what WithStateSnapshot reads and writes through: the
// rows, the pre-migration blob (migrated on the way in), and row deletes.
type StateSnapshotStore interface {
	database.LegacyMetadataStateStore
	DeleteMetadataFieldState(bookID, field string) error
}

// ErrNoStateChange, returned by a WithStateSnapshot callback, skips the save.
var ErrNoStateChange = errors.New("metadata state: nothing to save")

// WithStateSnapshot reads bookID's field state, hands it to fn, and saves what
// fn leaves as the book's complete state, all under the book's field-state
// stripe. fn must do in-memory work only (and quick Pebble writes such as a
// history row): nothing slow or networked runs under the stripe.
//
// A book whose state is still in the pre-migration blob is migrated to rows
// first (database.MigrateLegacyMetadataState), inside the same hold. The save
// writes every entry and deletes every row fn removed from the map. An entry
// with an override value is a person's, so its LockSource is cleared: a lock
// a person touched is theirs.
func WithStateSnapshot(store StateSnapshotStore, bookID string, fn func(state map[string]MetadataFieldState) error) error {
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	unlock := database.LockMetadataState(bookID)
	defer unlock()
	if _, err := database.MigrateLegacyMetadataState(store, bookID); err != nil {
		return err
	}
	rows, err := store.GetMetadataFieldStates(bookID)
	if err != nil {
		return err
	}
	state := StateFromRows(rows)
	if err := fn(state); err != nil {
		if errors.Is(err, ErrNoStateChange) {
			return nil
		}
		return err
	}
	return saveStateLocked(store, bookID, rows, state)
}

// saveStateLocked writes state as the book's complete field state. The
// caller holds the stripe; rows are the rows state was read from.
func saveStateLocked(store StateSnapshotStore, bookID string, rows []database.MetadataFieldState, state map[string]MetadataFieldState) error {
	now := time.Now()
	for field, entry := range state {
		fetched, err := metastate.Encode(entry.FetchedValue)
		if err != nil {
			return fmt.Errorf("failed to encode fetched metadata for %s: %w", field, err)
		}
		override, err := metastate.Encode(entry.OverrideValue)
		if err != nil {
			return fmt.Errorf("failed to encode override metadata for %s: %w", field, err)
		}
		if entry.UpdatedAt.IsZero() {
			entry.UpdatedAt = now
		}
		source := entry.LockSource
		if override != nil {
			source = ""
		}
		if err := store.UpsertMetadataFieldState(&database.MetadataFieldState{
			BookID: bookID, Field: field, FetchedValue: fetched, OverrideValue: override,
			OverrideLocked: entry.OverrideLocked, LockSource: source, UpdatedAt: entry.UpdatedAt,
		}); err != nil {
			return fmt.Errorf("failed to persist metadata state for %s: %w", field, err)
		}
	}
	for _, row := range rows {
		if _, kept := state[row.Field]; kept {
			continue
		}
		if err := store.DeleteMetadataFieldState(bookID, row.Field); err != nil {
			return fmt.Errorf("failed to clean up metadata state for %s: %w", row.Field, err)
		}
	}
	return nil
}

// LoadStateSnapshot is the read-only load the services share: the rows, or a
// blob-only book's pre-migration blob (not migrated: a read writes nothing;
// the next WithStateSnapshot migrates it).
func LoadStateSnapshot(reader database.MetadataFieldStateReader, bookID string) (map[string]MetadataFieldState, error) {
	if reader == nil {
		return map[string]MetadataFieldState{}, fmt.Errorf("database not initialized")
	}
	rows, err := reader.GetMetadataFieldStates(bookID)
	if err != nil {
		return map[string]MetadataFieldState{}, err
	}
	if len(rows) > 0 {
		return StateFromRows(rows), nil
	}
	legacy, err := database.ParseLegacyMetadataState(reader, bookID)
	if err != nil {
		return map[string]MetadataFieldState{}, err
	}
	state := make(map[string]MetadataFieldState, len(legacy))
	for field, e := range legacy {
		state[field] = MetadataFieldState{FetchedValue: e.FetchedValue, OverrideValue: e.OverrideValue,
			OverrideLocked: e.OverrideLocked, UpdatedAt: e.UpdatedAt}
	}
	return state, nil
}
