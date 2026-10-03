// file: internal/metafetch/state_snapshot.go
// version: 1.0.0
// guid: 3a682c02-4109-4286-bcd5-ad66eecac28e
// last-edited: 2026-10-03

package metafetch

import (
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
)

// The ONE load and save of a book's whole field-state snapshot. Four copies
// (this package's Service and MetadataStateService, audiobooks.AudiobookService
// and server.Server) each read the rows into a map, let the caller change it,
// and wrote every entry back while deleting every row missing from the map.
// That read-modify-write erased anything written between the read and the
// save, the Repairs lane's field locks in particular (repairs.Writer.LockFields),
// and each copy would have had to learn LockSource separately. They now all
// call StateFromRows and SaveStateSnapshot.

// StateFromRows turns a book's field-state rows into the snapshot map,
// remembering each field's lock as read so SaveStateSnapshot can tell a lock
// written since the read from one the caller changed.
func StateFromRows(rows []database.MetadataFieldState) map[string]MetadataFieldState {
	state := make(map[string]MetadataFieldState, len(rows))
	for _, entry := range rows {
		state[entry.Field] = MetadataFieldState{
			FetchedValue:     metastate.Decode(entry.FetchedValue),
			OverrideValue:    metastate.Decode(entry.OverrideValue),
			OverrideLocked:   entry.OverrideLocked,
			LockSource:       entry.LockSource,
			UpdatedAt:        entry.UpdatedAt,
			loadedLocked:     entry.OverrideLocked,
			loadedLockSource: entry.LockSource,
		}
	}
	return state
}

// StateSnapshotStore is what SaveStateSnapshot writes through.
type StateSnapshotStore interface {
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	UpsertMetadataFieldState(state *database.MetadataFieldState) error
	DeleteMetadataFieldState(bookID, field string) error
	DeleteUserPreference(key string) error
}

// SaveStateSnapshot writes state as the book's complete field state, under
// the book's field-state stripe (database.LockMetadataState), which
// repairs.Writer.LockFields also takes:
//
//   - A lock written since the snapshot was read (the row's lock or source no
//     longer what the caller read) is kept when the caller did not change
//     that field's lock: the caller never saw it.
//   - An entry with an override value is a person's, so its LockSource is
//     cleared: a lock a person touched is theirs.
//   - A row missing from the snapshot is deleted (a cleared field), except a
//     repair's bare lock the caller never read, which appeared since.
//
// The pre-migration blob is deleted afterwards: the rows are authoritative
// (database.DeleteLegacyMetadataState).
func SaveStateSnapshot(store StateSnapshotStore, bookID string, state map[string]MetadataFieldState) error {
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	unlock := database.LockMetadataState(bookID)
	defer unlock()

	existing, err := store.GetMetadataFieldStates(bookID)
	if err != nil {
		return err
	}
	current := make(map[string]database.MetadataFieldState, len(existing))
	for _, entry := range existing {
		current[entry.Field] = entry
	}

	now := time.Now()
	for field, entry := range state {
		cur, had := current[field]
		delete(current, field)
		if entry.cleared {
			if had {
				if err := store.DeleteMetadataFieldState(bookID, field); err != nil {
					return fmt.Errorf("failed to clean up metadata state for %s: %w", field, err)
				}
			}
			continue
		}
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
		locked, source := entry.OverrideLocked, entry.LockSource
		if override != nil {
			source = ""
		}
		lockUntouched := entry.OverrideLocked == entry.loadedLocked && entry.LockSource == entry.loadedLockSource
		lockMovedSince := had && (cur.OverrideLocked != entry.loadedLocked || cur.LockSource != entry.loadedLockSource)
		if override == nil && lockUntouched && lockMovedSince {
			locked, source = cur.OverrideLocked, cur.LockSource
		}
		dbState := database.MetadataFieldState{
			BookID:         bookID,
			Field:          field,
			FetchedValue:   fetched,
			OverrideValue:  override,
			OverrideLocked: locked,
			LockSource:     source,
			UpdatedAt:      entry.UpdatedAt,
		}
		if err := store.UpsertMetadataFieldState(&dbState); err != nil {
			return fmt.Errorf("failed to persist metadata state for %s: %w", field, err)
		}
	}

	for field, cur := range current {
		if cur.IsRepairLock() {
			continue
		}
		if err := store.DeleteMetadataFieldState(bookID, field); err != nil {
			return fmt.Errorf("failed to clean up metadata state for %s: %w", field, err)
		}
	}

	if err := database.DeleteLegacyMetadataState(store, bookID); err != nil {
		return fmt.Errorf("failed to retire legacy metadata state: %w", err)
	}
	return nil
}

// ClearField marks field for deletion by SaveStateSnapshot (the person's
// "clear override"). Deleting the key from the map would read as "not in the
// snapshot", which keeps a repair lock the caller never read.
func ClearField(state map[string]MetadataFieldState, field string) {
	e := state[field]
	e.cleared = true
	state[field] = e
}
