// file: internal/metafetch/metadata_state_service_test.go
// version: 1.0.0
// guid: 8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e

package metafetch

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestMetadataStateService_LoadMetadataState_Empty(t *testing.T) {
	mockDB := &database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{}, nil
		},
	}

	service := NewMetadataStateService(mockDB)
	state, err := service.LoadMetadataState("book1")

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if state == nil {
		t.Error("expected state map, got nil")
	}
	if len(state) != 0 {
		t.Errorf("expected empty state, got %d entries", len(state))
	}
}

func TestMetadataStateService_Modify_Success(t *testing.T) {
	mockDB := &database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{}, nil
		},
		UpsertMetadataFieldStateFunc: func(state *database.MetadataFieldState) error {
			return nil
		},
	}

	service := NewMetadataStateService(mockDB)
	err := service.modify("book1", func(state map[string]metadataFieldState) error {
		state["title"] = metadataFieldState{
			FetchedValue:  "Test Title",
			OverrideValue: "Custom Title",
			UpdatedAt:     time.Now(),
		}
		return nil
	})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestMetadataStateService_SetOverride_Success(t *testing.T) {
	mockDB := &database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{}, nil
		},
		UpsertMetadataFieldStateFunc: func(state *database.MetadataFieldState) error {
			return nil
		},
	}

	service := NewMetadataStateService(mockDB)
	err := service.SetOverride("book1", "author", "New Author", false)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestMetadataStateService_UnlockOverride_Success(t *testing.T) {
	overrideVal := `"Custom Title"`
	mockDB := &database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{
				{
					BookID:         "book1",
					Field:          "title",
					OverrideValue:  &overrideVal,
					OverrideLocked: true,
					UpdatedAt:      time.Now(),
				},
			}, nil
		},
		UpsertMetadataFieldStateFunc: func(state *database.MetadataFieldState) error {
			return nil
		},
	}

	service := NewMetadataStateService(mockDB)
	err := service.UnlockOverride("book1", "title")

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestMetadataStateService_GetEffectiveValue_PreferOverride(t *testing.T) {
	overrideVal := `"Override Title"`
	fetchedVal := `"Fetched Title"`

	mockDB := &database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{
				{
					BookID:        "book1",
					Field:         "title",
					FetchedValue:  &fetchedVal,
					OverrideValue: &overrideVal,
					UpdatedAt:     time.Now(),
				},
			}, nil
		},
	}

	service := NewMetadataStateService(mockDB)
	value, err := service.GetEffectiveValue("book1", "title")

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if value == nil {
		t.Error("expected non-nil value")
	}
}

func TestMetadataStateService_GetEffectiveValue_FallbackToFetched(t *testing.T) {
	fetchedVal := `"Fetched Title"`

	mockDB := &database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{
				{
					BookID:       "book1",
					Field:        "title",
					FetchedValue: &fetchedVal,
					UpdatedAt:    time.Now(),
				},
			}, nil
		},
	}

	service := NewMetadataStateService(mockDB)
	value, err := service.GetEffectiveValue("book1", "title")

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if value == nil {
		t.Error("expected non-nil value")
	}
}

func TestMetadataStateService_ClearOverride_Success(t *testing.T) {
	overrideVal := `"Override Title"`

	mockDB := &database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{
				{
					BookID:        "book1",
					Field:         "title",
					OverrideValue: &overrideVal,
					UpdatedAt:     time.Now(),
				},
			}, nil
		},
		DeleteMetadataFieldStateFunc: func(bookID, field string) error {
			return nil
		},
	}

	service := NewMetadataStateService(mockDB)
	err := service.ClearOverride("book1", "title")

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
