// file: internal/config/update_service.go
// version: 3.5.0
// guid: f6g7h8i9-j0k1-l2m3-n4o5-p6q7r8s9t0u1
// last-edited: 2026-06-16

package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// UpdateService handles applying and persisting config changes.
type UpdateService struct {
	DB database.Store
}

// NewUpdateService creates a new UpdateService.
func NewUpdateService(db database.Store) *UpdateService {
	return &UpdateService{DB: db}
}

// ValidateUpdate checks that the payload is non-empty.
func (us *UpdateService) ValidateUpdate(payload map[string]any) error {
	if len(payload) == 0 {
		return fmt.Errorf("no configuration updates provided")
	}
	return nil
}

// MaskSecrets returns a copy of cfg with all secret fields masked for API responses.
func (us *UpdateService) MaskSecrets(cfg Config) Config {
	masked := cfg
	if masked.OpenAIAPIKey != "" {
		masked.OpenAIAPIKey = database.MaskSecret(masked.OpenAIAPIKey)
	}
	if masked.AcoustIDAPIKey != "" {
		masked.AcoustIDAPIKey = database.MaskSecret(masked.AcoustIDAPIKey)
	}
	if masked.GoogleBooksAPIKey != "" {
		masked.GoogleBooksAPIKey = database.MaskSecret(masked.GoogleBooksAPIKey)
	}
	if masked.HardcoverAPIToken != "" {
		masked.HardcoverAPIToken = database.MaskSecret(masked.HardcoverAPIToken)
	}
	if masked.BasicAuthPassword != "" {
		masked.BasicAuthPassword = database.MaskSecret(masked.BasicAuthPassword)
	}
	return masked
}

// secretFieldKeys are extracted and applied explicitly, then removed before the
// JSON round-trip so they are never stored in plaintext in the config blob.
var secretFieldKeys = []string{
	"openai_api_key",
	"acoustid_api_key",
	"google_books_api_key",
	"hardcover_api_token",
	"basic_auth_password",
}

// immutableFieldKeys cannot be changed at runtime and are rejected if present.
var immutableFieldKeys = []string{"database_type", "enable_sqlite"}

// remapEmbeddingKeys translates legacy flat embedding keys in a config update
// payload to the nested EmbeddingConfig format. Merges into any existing
// "embedding" sub-object to avoid zeroing sibling fields.
// Remove this shim once the frontend sends nested keys.
func remapEmbeddingKeys(payload map[string]any) map[string]any {
	flatToNested := map[string]string{
		"embedding_enabled":    "enabled",
		"embedding_model":      "model",
		"embedding_dimensions": "dimensions",
		"embedding_base_url":   "base_url",
		"vector_index_backend": "vector_backend",
	}
	nested := make(map[string]any)
	for flat, short := range flatToNested {
		if v, ok := payload[flat]; ok {
			nested[short] = v
			delete(payload, flat)
		}
	}
	if len(nested) == 0 {
		return payload
	}
	if existing, ok := payload["embedding"].(map[string]any); ok {
		for k, v := range nested {
			existing[k] = v
		}
	} else {
		payload["embedding"] = nested
	}
	return payload
}

// remapDedupKeys translates legacy flat dedup keys in a config update payload
// to the nested DedupConfig format. Merges into any existing "dedup" sub-object
// to avoid zeroing sibling fields.
// Remove this shim once the frontend sends nested keys.
func remapDedupKeys(payload map[string]any) map[string]any {
	flatToNested := map[string]string{
		"dedup_book_high_threshold":             "book_high_threshold",
		"dedup_book_low_threshold":              "book_low_threshold",
		"dedup_author_high_threshold":           "author_high_threshold",
		"dedup_author_low_threshold":            "author_low_threshold",
		"dedup_auto_merge_enabled":              "auto_merge_enabled",
		"dedup_embeddings_enabled":              "embeddings_enabled",
		"dedup_llm_auto_merge_high_confidence": "llm_auto_merge_high_confidence",
		"dedup_on_import_via_scheduler":         "on_import_via_scheduler",
		"dedup_review_model":                    "review_model",
	}
	nested := make(map[string]any)
	for flat, short := range flatToNested {
		if v, ok := payload[flat]; ok {
			nested[short] = v
			delete(payload, flat)
		}
	}
	if len(nested) == 0 {
		return payload
	}
	if existing, ok := payload["dedup"].(map[string]any); ok {
		for k, v := range nested {
			existing[k] = v
		}
	} else {
		payload["dedup"] = nested
	}
	return payload
}

// remapMetadataScoringKeys translates legacy flat metadata scoring keys to nested format.
// Remove this shim once the frontend sends nested keys.
func remapMetadataScoringKeys(payload map[string]any) map[string]any {
	flatToNested := map[string]string{
		"metadata_embedding_scoring_enabled": "embedding_enabled",
		"metadata_embedding_min_score":       "embedding_min_score",
		"metadata_embedding_best_match_min":  "embedding_best_match",
		"metadata_llm_scoring_enabled":       "llm_enabled",
		"metadata_llm_rerank_epsilon":        "llm_rerank_epsilon",
		"metadata_llm_rerank_top_k":          "llm_rerank_top_k",
		"write_backup_before_tag_write":      "write_backup_before",
	}
	nested := make(map[string]any)
	for flat, short := range flatToNested {
		if v, ok := payload[flat]; ok {
			nested[short] = v
			delete(payload, flat)
		}
	}
	if len(nested) == 0 {
		return payload
	}
	if existing, ok := payload["metadata_scoring"].(map[string]any); ok {
		for k, v := range nested {
			existing[k] = v
		}
	} else {
		payload["metadata_scoring"] = nested
	}
	return payload
}

// remapITunesKeys translates legacy flat iTunes keys in a config update payload
// to the nested ITunesConfig format. Merges into any existing "itunes" sub-object
// to avoid zeroing sibling fields.
// Remove this shim once the frontend sends nested keys.
func remapITunesKeys(payload map[string]any) map[string]any {
	flatToNested := map[string]string{
		"itunes_sync_enabled":       "sync_enabled",
		"itunes_sync_interval":      "sync_interval",
		"itl_write_back_enabled":    "write_back_enabled",
		"itunes_library_write_path": "library_write_path",
		"itunes_library_read_path":  "library_read_path",
		"itunes_auto_write_back":    "auto_write_back",
		"itunes_path_trim_enabled":  "path_trim_enabled",
		"itunes_windows_root_path":  "windows_root_path",
		"itunes_media_root":         "media_root",
	}
	nested := make(map[string]any)
	for flat, short := range flatToNested {
		if v, ok := payload[flat]; ok {
			nested[short] = v
			delete(payload, flat)
		}
	}
	if len(nested) == 0 {
		return payload
	}
	if existing, ok := payload["itunes"].(map[string]any); ok {
		for k, v := range nested {
			existing[k] = v
		}
	} else {
		payload["itunes"] = nested
	}
	return payload
}

// UpdateConfig applies a config update payload to AppConfig and persists it.
//
// Architecture: non-secret fields are applied via JSON round-trip onto AppConfig.
// json.Unmarshal only overwrites keys present in the JSON, so absent keys leave
// AppConfig unchanged. This means any new field added to Config is
// automatically handled here with no registration required.
func (us *UpdateService) UpdateConfig(payload map[string]any) (int, map[string]any) {
	if us.DB == nil {
		return http.StatusInternalServerError, map[string]any{"error": "database not initialized"}
	}
	if payload == nil {
		return http.StatusBadRequest, map[string]any{"error": "configuration payload is required"}
	}

	// Reject immutable fields
	for _, field := range immutableFieldKeys {
		if _, ok := payload[field]; ok {
			return http.StatusBadRequest, map[string]any{"error": field + " cannot be changed at runtime"}
		}
	}

	// Apply secrets explicitly — they need masking/debug logging and must not
	// flow through the JSON round-trip to avoid plaintext exposure.
	// WHY Mutate: each assignment here is a write to the global AppConfig that
	// races with concurrent HTTP readers; Mutate serialises under the write lock.
	if val, ok := payloadString(payload, "openai_api_key"); ok {
		slog.Debug("UpdateConfig updating OpenAI API key (len)", "val_count", len(val))
		Mutate(func(c *Config) { c.OpenAIAPIKey = val })
	}
	if val, ok := payloadString(payload, "acoustid_api_key"); ok {
		slog.Debug("UpdateConfig updating AcoustID API key (len)", "val_count", len(val))
		Mutate(func(c *Config) { c.AcoustIDAPIKey = val })
	}
	if val, ok := payloadString(payload, "google_books_api_key"); ok {
		Mutate(func(c *Config) { c.GoogleBooksAPIKey = val })
	}
	if val, ok := payloadString(payload, "hardcover_api_token"); ok {
		Mutate(func(c *Config) { c.HardcoverAPIToken = val })
	}
	if val, ok := payloadString(payload, "basic_auth_password"); ok {
		Mutate(func(c *Config) { c.BasicAuthPassword = val })
	}

	// Build filtered payload without secrets (already applied above)
	filtered := make(map[string]any, len(payload))
	for k, v := range payload {
		filtered[k] = v
	}
	for _, k := range secretFieldKeys {
		delete(filtered, k)
	}

	// Translate any legacy flat embedding keys to the nested EmbeddingConfig format.
	filtered = remapEmbeddingKeys(filtered)
	// Translate any legacy flat dedup keys to the nested DedupConfig format.
	filtered = remapDedupKeys(filtered)
	// Translate any legacy flat metadata scoring keys to the nested MetadataScoringConfig format.
	filtered = remapMetadataScoringKeys(filtered)
	// Translate any legacy flat iTunes keys to the nested ITunesConfig format.
	filtered = remapITunesKeys(filtered)

	// Apply all remaining fields via JSON round-trip.
	// Any field in Config with a matching json tag is set automatically.
	// WHY Mutate: json.Unmarshal writes multiple fields in sequence; without the
	// write lock a concurrent Snapshot() call could observe a half-written struct.
	payloadJSON, err := json.Marshal(filtered)
	if err != nil {
		return http.StatusBadRequest, map[string]any{"error": "failed to encode payload: " + err.Error()}
	}
	var unmarshalErr error
	Mutate(func(c *Config) {
		if err := json.Unmarshal(payloadJSON, c); err != nil {
			unmarshalErr = err
			return
		}
		// Post-process inside the lock: trim root_dir whitespace, derive setup_complete
		c.RootDir = strings.TrimSpace(c.RootDir)
		c.SetupComplete = c.RootDir != ""
	})
	if unmarshalErr != nil {
		return http.StatusBadRequest, map[string]any{"error": "failed to apply config: " + unmarshalErr.Error()}
	}

	if err := SaveConfigToDatabase(us.DB); err != nil {
		slog.Error("failed to persist config", "err", err)
		return http.StatusInternalServerError, map[string]any{
			"error":   "failed to save configuration",
			"details": err.Error(),
		}
	}

	slog.Info("Configuration saved successfully")

	return http.StatusOK, map[string]any{
		"message": "configuration updated and saved to database",
		"config":  us.MaskSecrets(Snapshot()),
	}
}

// payloadString extracts a string value from the payload if present and non-empty.
func payloadString(payload map[string]any, key string) (string, bool) {
	v, ok := payload[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// ApplyUpdates applies config updates and persists them.
// Deprecated: prefer UpdateConfig directly.
func (us *UpdateService) ApplyUpdates(payload map[string]any) error {
	status, resp := us.UpdateConfig(payload)
	if status >= 400 {
		if errMsg, ok := resp["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}
		return fmt.Errorf("config update failed with status %d", status)
	}
	return nil
}
