// file: internal/metafetch/metadata_state_service.go
// version: 1.8.0
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-10-03

package metafetch

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// metadataStateStore is the narrow slice of database.Store that
// MetadataStateService needs: metadata field state + change history
// plus the user preference used as the cache key for state blobs.
// metadataStateStore is what this package actually calls, measured by emptying the
// interface and reading the compiler's enumeration: 5 methods. It was a
// pure pass-through of database.* embeds — 16 methods, none of them declared
// here and almost none of them used.
type metadataStateStore interface {
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	UpsertMetadataFieldState(state *database.MetadataFieldState) error
	DeleteMetadataFieldState(bookID, field string) error
	RecordMetadataChange(record *database.MetadataChangeRecord) error
	GetUserPreference(key string) (*database.UserPreference, error)
	DeleteUserPreference(key string) error
}

// MetadataStateService handles metadata field state operations
type MetadataStateService struct {
	db metadataStateStore
}

// NewMetadataStateService creates a new metadata state service
func NewMetadataStateService(db metadataStateStore) *MetadataStateService {
	return &MetadataStateService{db: db}
}

// LoadMetadataState loads the complete metadata state for a book, read-only
// (LoadStateSnapshot). A change goes through WithStateSnapshot.
func (mss *MetadataStateService) LoadMetadataState(bookID string) (map[string]metadataFieldState, error) {
	if mss.db == nil {
		return map[string]metadataFieldState{}, fmt.Errorf("database not initialized")
	}
	return LoadStateSnapshot(mss.db, bookID)
}

// modify runs fn on the book's state under its field-state stripe and saves
// the result (WithStateSnapshot).
func (mss *MetadataStateService) modify(bookID string, fn func(state map[string]metadataFieldState) error) error {
	if mss.db == nil {
		return fmt.Errorf("database not initialized")
	}
	return WithStateSnapshot(mss.db, bookID, fn)
}

// recordChange is a helper that records a metadata change for undo/audit.
func (mss *MetadataStateService) recordChange(bookID, field, changeType, source string, previousValue, newValue any) {
	if mss.db == nil {
		return
	}
	prev, _ := metastate.Encode(previousValue)
	next, _ := metastate.Encode(newValue)
	record := &database.MetadataChangeRecord{
		BookID:        bookID,
		Field:         field,
		PreviousValue: prev,
		NewValue:      next,
		ChangeType:    changeType,
		Source:        source,
		ChangedAt:     time.Now(),
	}
	if err := mss.db.RecordMetadataChange(record); err != nil {
		slog.Warn("failed to record metadata change for /", "id", logger.SanitizeLogValue(bookID), "field", logger.SanitizeLogValue(field), "error", err)
	}
}

// UpdateFetchedMetadata updates the fetched values in metadata state
func (mss *MetadataStateService) UpdateFetchedMetadata(bookID string, values map[string]any) error {
	return mss.modify(bookID, func(state map[string]metadataFieldState) error {
		now := time.Now()
		for field, value := range values {
			entry := state[field]
			oldValue := entry.FetchedValue
			entry.FetchedValue = value
			entry.UpdatedAt = now
			state[field] = entry
			mss.recordChange(bookID, field, "fetched", "", oldValue, value)
		}
		return nil
	})
}

// SetOverride sets an override value for a metadata field
func (mss *MetadataStateService) SetOverride(bookID string, field string, value any, locked bool) error {
	return mss.modify(bookID, func(state map[string]metadataFieldState) error {
		entry := state[field]
		oldValue := entry.OverrideValue
		entry.OverrideValue = value
		entry.OverrideLocked = locked
		entry.LockSource = "" // a person set it
		entry.UpdatedAt = time.Now()
		state[field] = entry
		mss.recordChange(bookID, field, "override", "manual", oldValue, value)
		return nil
	})
}

// UnlockOverride unlocks an override without changing its value
func (mss *MetadataStateService) UnlockOverride(bookID string, field string) error {
	return mss.modify(bookID, func(state map[string]metadataFieldState) error {
		entry, exists := state[field]
		if !exists {
			return fmt.Errorf("field %s not found in metadata state", field)
		}
		entry.OverrideLocked = false
		entry.LockSource = ""
		entry.UpdatedAt = time.Now()
		state[field] = entry
		return nil
	})
}

// ClearOverride removes an override for a metadata field
func (mss *MetadataStateService) ClearOverride(bookID string, field string) error {
	return mss.modify(bookID, func(state map[string]metadataFieldState) error {
		entry, exists := state[field]
		if !exists {
			return fmt.Errorf("field %s not found in metadata state", field)
		}
		mss.recordChange(bookID, field, "clear", "manual", entry.OverrideValue, nil)
		delete(state, field)
		return nil
	})
}

// GetEffectiveValue returns the effective value for a field (override > fetched > empty)
func (mss *MetadataStateService) GetEffectiveValue(bookID string, field string) (any, error) {
	state, err := mss.LoadMetadataState(bookID)
	if err != nil {
		return nil, err
	}

	if entry, exists := state[field]; exists {
		if entry.OverrideValue != nil {
			return entry.OverrideValue, nil
		}
		if entry.FetchedValue != nil {
			return entry.FetchedValue, nil
		}
	}

	return nil, nil
}
