// file: internal/config/update_service.go
// version: 3.11.0
// guid: f6g7h8i9-j0k1-l2m3-n4o5-p6q7r8s9t0u1
// last-edited: 2026-07-16

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

	// remapScheduledKeys handles the deeper two-level scheduled_* nesting that the
	// retired generic shim never covered (owned by INIT-6/WF-3); it stays.
	filtered = remapScheduledKeys(filtered)

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
