// file: internal/config/update_service_test.go
// version: 1.3.0
// guid: e5f6g7h8-i9j0-k1l2-m3n4-o5p6q7r8s9t0
// last-edited: 2026-07-10

package config

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/util"

	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/mock"
)

func TestUpdateService_ValidateUpdate_EmptyPayload(t *testing.T) {
	service := NewUpdateService(nil)

	err := service.ValidateUpdate(map[string]any{})

	if err == nil {
		t.Error("expected error for empty payload")
	}
}

func TestUpdateService_ExtractStringField(t *testing.T) {
	payload := map[string]any{
		"root_dir": "/library",
	}

	result, ok := util.ExtractStringField(payload, "root_dir")

	if !ok || result != "/library" {
		t.Errorf("expected '/library', got %q (ok=%v)", result, ok)
	}
}

func TestUpdateService_ExtractBoolField(t *testing.T) {
	payload := map[string]any{
		"auto_organize": true,
	}

	result, ok := util.ExtractBoolField(payload, "auto_organize")

	if !ok || result != true {
		t.Errorf("expected true, got %v (ok=%v)", result, ok)
	}
}

func TestUpdateService_ExtractIntField(t *testing.T) {
	payload := map[string]any{
		"concurrent_scans": float64(4),
	}

	result, ok := util.ExtractIntField(payload, "concurrent_scans")

	if !ok || result != 4 {
		t.Errorf("expected 4, got %d (ok=%v)", result, ok)
	}
}

func TestUpdateService_ApplyUpdates_Success(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockStore.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	service := NewUpdateService(mockStore)

	updates := map[string]any{
		"root_dir": "/new/library",
	}

	originalDir := AppConfig.RootDir
	defer func() {
		AppConfig.RootDir = originalDir
	}()

	if err := service.ApplyUpdates(updates); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if AppConfig.RootDir != "/new/library" {
		t.Errorf("expected '/new/library', got %q", AppConfig.RootDir)
	}
}

// TestUpdateService_FlatKeysDropped proves the retired CFG-2 Phase D shim
// (#1536/CONS-13) is gone: a payload carrying ONLY a formerly-remapped flat key
// (dedup_embeddings_enabled) no longer touches the nested Dedup.EmbeddingsEnabled
// field — it is dropped by the JSON round-trip (no top-level json tag).
func TestUpdateService_FlatKeysDropped(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockStore.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	service := NewUpdateService(mockStore)

	original := AppConfig.Dedup.EmbeddingsEnabled
	defer func() { AppConfig.Dedup.EmbeddingsEnabled = original }()

	// Seed a known value, then send the flat key with the OPPOSITE value.
	Mutate(func(c *Config) { c.Dedup.EmbeddingsEnabled = true })
	if err := service.ApplyUpdates(map[string]any{"dedup_embeddings_enabled": false}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !AppConfig.Dedup.EmbeddingsEnabled {
		t.Error("flat key dedup_embeddings_enabled should be dropped, leaving nested field unchanged (true)")
	}
}

// TestUpdateService_NestedKeysStillApply is the anti-regression proof that the
// kept JSON round-trip path still applies the nested form of the same key.
func TestUpdateService_NestedKeysStillApply(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockStore.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	service := NewUpdateService(mockStore)

	original := AppConfig.Dedup.EmbeddingsEnabled
	defer func() { AppConfig.Dedup.EmbeddingsEnabled = original }()

	Mutate(func(c *Config) { c.Dedup.EmbeddingsEnabled = true })
	updates := map[string]any{"dedup": map[string]any{"embeddings_enabled": false}}
	if err := service.ApplyUpdates(updates); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if AppConfig.Dedup.EmbeddingsEnabled {
		t.Error("nested key dedup.embeddings_enabled should apply, setting field to false")
	}
}
