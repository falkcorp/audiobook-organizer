// file: internal/server/audiobook_update_service_test.go
// version: 1.0.1
// guid: a1b2c3d4-e5f6-g7h8-i9j0-k1l2m3n4o5p6

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/util"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func TestAudiobookUpdateService_ExtractStringField(t *testing.T) {
	payload := map[string]any{
		"title": "New Title",
	}

	result, ok := util.ExtractStringField(payload, "title")

	if !ok || result != "New Title" {
		t.Errorf("expected 'New Title', got %q (ok=%v)", result, ok)
	}
}

func TestAudiobookUpdateService_ExtractIntField(t *testing.T) {
	payload := map[string]any{
		"author_id": float64(42),
	}

	result, ok := util.ExtractIntField(payload, "author_id")

	if !ok || result != 42 {
		t.Errorf("expected 42, got %d (ok=%v)", result, ok)
	}
}

func TestAudiobookUpdateService_ExtractStringField_NotFound(t *testing.T) {
	payload := map[string]any{}

	_, ok := util.ExtractStringField(payload, "missing")

	if ok {
		t.Error("expected ok=false for missing field")
	}
}

func TestAudiobookUpdateService_ExtractOverrides_Success(t *testing.T) {
	service := NewAudiobookUpdateService(&database.MockStore{})

	payload := map[string]any{
		"overrides": map[string]any{
			"key1": "value1",
			"key2": "value2",
		},
	}

	result, ok := service.ExtractOverrides(payload)

	if !ok || len(result) != 2 {
		t.Errorf("expected 2 overrides, got %d (ok=%v)", len(result), ok)
	}
}
