// file: internal/config/update_service_test.go
// version: 1.5.0
// guid: e5f6g7h8-i9j0-k1l2-m3n4-o5p6q7r8s9t0
// last-edited: 2026-09-12

package config

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
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

	if err := service.ApplyUpdates(context.Background(), updates); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if AppConfig.RootDir != "/new/library" {
		t.Errorf("expected '/new/library', got %q", AppConfig.RootDir)
	}
}

// TestUpdateService_FlatKeysRejected proves a payload carrying a flat key that
// has no top-level json tag (dedup_embeddings_enabled, retired with the CFG-2
// Phase D shim in #1536/CONS-13) is refused with a 400 naming the key and
// changes nothing. It used to answer 200 and silently do nothing, which is
// how a flat dedup_auto_merge_enabled PUT "saved" on 2026-09-12 while the
// setting stayed on.
func TestUpdateService_FlatKeysRejected(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockStore.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	service := NewUpdateService(mockStore)

	originalEmb := AppConfig.Dedup.EmbeddingsEnabled
	originalDir := AppConfig.RootDir
	defer func() {
		AppConfig.Dedup.EmbeddingsEnabled = originalEmb
		AppConfig.RootDir = originalDir
	}()

	// Seed a known value, then send the flat key with the OPPOSITE value next to
	// a valid key: the whole request must be refused, the valid key included.
	Mutate(func(c *Config) {
		c.Dedup.EmbeddingsEnabled = true
		c.RootDir = "/before"
	})
	status, resp := service.UpdateConfig(context.Background(), map[string]any{
		"dedup_embeddings_enabled": false,
		"root_dir":                 "/after",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (resp %v)", status, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "dedup_embeddings_enabled") {
		t.Errorf("error %q does not name the unknown key", msg)
	}
	if !AppConfig.Dedup.EmbeddingsEnabled {
		t.Error("rejected flat key changed Dedup.EmbeddingsEnabled")
	}
	if AppConfig.RootDir != "/before" {
		t.Errorf("rejected request still applied root_dir: %q", AppConfig.RootDir)
	}
}

// TestUpdateService_NestedTypoRejected covers the depth the flat-key test does
// not: a misspelled key INSIDE a known object is reported by its dotted path.
func TestUpdateService_NestedTypoRejected(t *testing.T) {
	service := NewUpdateService(mocks.NewMockStore(t))

	original := AppConfig.Dedup.AutoMergeEnabled
	defer func() { AppConfig.Dedup.AutoMergeEnabled = original }()
	Mutate(func(c *Config) { c.Dedup.AutoMergeEnabled = true })

	status, resp := service.UpdateConfig(context.Background(), map[string]any{
		"dedup": map[string]any{"auto_merge_enabld": false},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (resp %v)", status, resp)
	}
	if got, _ := resp["unknown_keys"].([]string); !slices.Equal(got, []string{"dedup.auto_merge_enabld"}) {
		t.Errorf("unknown_keys = %v, want [dedup.auto_merge_enabld]", resp["unknown_keys"])
	}
	if !AppConfig.Dedup.AutoMergeEnabled {
		t.Error("rejected nested typo changed Dedup.AutoMergeEnabled")
	}
}

// TestUpdateService_ReadOnlyAnnotationsIgnored proves the three keys GET
// /config adds (withEnvLocks) do not make a PUT of a GET response fail.
func TestUpdateService_ReadOnlyAnnotationsIgnored(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockStore.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	service := NewUpdateService(mockStore)

	originalDir := AppConfig.RootDir
	defer func() { AppConfig.RootDir = originalDir }()

	err := service.ApplyUpdates(context.Background(), map[string]any{
		"root_dir":                  "/annotated",
		"env_locked":                []any{"database_path"},
		"setting_locks":             map[string]any{"database_path": "flag --db"},
		"activity_db_resolved_path": "/somewhere/activity.db",
	})
	if err != nil {
		t.Fatalf("expected the read-only annotations to be ignored, got %v", err)
	}
	if AppConfig.RootDir != "/annotated" {
		t.Errorf("root_dir = %q, want /annotated", AppConfig.RootDir)
	}
}

// TestUnknownConfigKeys_Paths pins the walker's reporting: flat keys at the top
// level, typos inside objects and slice elements by full path, map-typed fields
// open to any key, and encoding/json's case-insensitive field match.
func TestUnknownConfigKeys_Paths(t *testing.T) {
	payload := map[string]any{
		"dedup_auto_merge_enabled": false,
		"dedup":                    map[string]any{"auto_merge_enabld": true, "auto_merge_enabled": false},
		"metadata_sources": []any{map[string]any{
			"id":          "x",
			"bogus":       1,
			"credentials": map[string]any{"anything": "ok"},
		}},
		"ROOT_DIR": "/case-insensitive-match",
	}
	got := unknownConfigKeys(payload)
	want := []string{"dedup.auto_merge_enabld", "dedup_auto_merge_enabled", "metadata_sources[0].bogus"}
	if !slices.Equal(got, want) {
		t.Errorf("unknownConfigKeys = %v, want %v", got, want)
	}
}

// TestUnknownConfigKeys_MarshalledConfigIsClean is the round-trip guarantee:
// every key a marshalled Config carries is one UpdateConfig accepts, so the
// walker can never reject a GET response (or an exported settings file).
func TestUnknownConfigKeys_MarshalledConfigIsClean(t *testing.T) {
	cfg := Snapshot()
	cfg.MetadataSources = []MetadataSource{{ID: "s", Credentials: map[string]string{"k": "v"}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var flat map[string]any
	if err := json.Unmarshal(raw, &flat); err != nil {
		t.Fatal(err)
	}
	if len(flat) < 20 {
		t.Fatalf("marshalled Config has only %d keys; the fixture is not exercising the walker", len(flat))
	}
	if got := unknownConfigKeys(flat); len(got) != 0 {
		t.Errorf("marshalled Config reported unknown keys: %v", got)
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
	if err := service.ApplyUpdates(context.Background(), updates); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if AppConfig.Dedup.EmbeddingsEnabled {
		t.Error("nested key dedup.embeddings_enabled should apply, setting field to false")
	}
}
