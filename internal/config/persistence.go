// file: internal/config/persistence.go
// version: 1.23.0
// guid: 9c8d7e6f-5a4b-3c2d-1e0f-9a8b7c6d5e4f
// last-edited: 2026-06-16

package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// ConfigFilePath returns the path to the YAML config file next to the database.
// WHY Snapshot: reads two fields together; Snapshot ensures a consistent view.
func ConfigFilePath() string {
	c := Snapshot()
	if c.DatabasePath != "" {
		return filepath.Join(filepath.Dir(c.DatabasePath), "config.yaml")
	}
	if c.RootDir != "" {
		return filepath.Join(c.RootDir, "config.yaml")
	}
	return ""
}

// LoadConfigFromFile loads settings from the YAML config file as a fallback.
// Called after LoadConfigFromDatabase so file values only fill in gaps.
func LoadConfigFromFile() error {
	path := ConfigFilePath()
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var fileConfig map[string]any
	if err := yaml.Unmarshal(data, &fileConfig); err != nil {
		slog.Warn("Failed to parse config file", "path", path, "err", err)
		return nil
	}

	// WHY Mutate: these writes race with any goroutine reading AppConfig.
	// The whole block runs under a single write lock so the fallback is atomic.
	applied := 0
	Mutate(func(c *Config) {
		type sf struct {
			key string
			ptr *string
		}
		for _, s := range []sf{
			{"openai_api_key", &c.OpenAIAPIKey},
			{"google_books_api_key", &c.GoogleBooksAPIKey},
			{"hardcover_api_token", &c.HardcoverAPIToken},
			{"root_dir", &c.RootDir},
			{"language", &c.Language},
		} {
			if *s.ptr == "" {
				if val, ok := fileConfig[s.key].(string); ok && val != "" {
					*s.ptr = val
					applied++
					slog.Info("Loaded from config file", "key", s.key)
				}
			}
		}
		if !c.EnableAIParsing {
			if val, ok := fileConfig["enable_ai_parsing"].(bool); ok && val {
				c.EnableAIParsing = true
				applied++
				slog.Info("Loaded enable_ai_parsing from config file")
			}
		}
	})

	if applied > 0 {
		slog.Info("Applied settings from config file", "applied", applied, "path", path)
	}
	return nil
}

// SaveConfigToFile writes key settings to a YAML config file next to the database.
// Secrets are stored in plaintext here — file permissions restrict access.
func SaveConfigToFile() error {
	path := ConfigFilePath()
	if path == "" {
		return fmt.Errorf("cannot determine config file path")
	}

	// WHY Snapshot: consistent read of many fields under a single read lock.
	c := Snapshot()
	fileConfig := map[string]any{
		"root_dir":              c.RootDir,
		"database_path":         c.DatabasePath,
		"playlist_dir":          c.PlaylistDir,
		"setup_complete":        c.SetupComplete,
		"organization_strategy": c.OrganizationStrategy,
		"scan_on_startup":       c.ScanOnStartup,
		"auto_organize":         c.AutoOrganize,
		"folder_naming_pattern": c.FolderNamingPattern,
		"file_naming_pattern":   c.FileNamingPattern,
		"auto_fetch_metadata":   c.AutoFetchMetadata,
		"language":              c.Language,
		"enable_ai_parsing":     c.EnableAIParsing,
		"concurrent_scans":      c.ConcurrentScans,
		"log_level":             c.LogLevel,
	}

	// Only write secrets if they're set (plaintext in file, file permissions protect them)
	if c.OpenAIAPIKey != "" {
		fileConfig["openai_api_key"] = c.OpenAIAPIKey
	}
	if c.GoogleBooksAPIKey != "" {
		fileConfig["google_books_api_key"] = c.GoogleBooksAPIKey
	}
	if c.HardcoverAPIToken != "" {
		fileConfig["hardcover_api_token"] = c.HardcoverAPIToken
	}

	data, err := yaml.Marshal(fileConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	// Write with restrictive permissions since it may contain secrets
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	slog.Info("Configuration saved to file", "path", path)
	return nil
}

// migrateEmbeddingBlob rewrites a flat-format config blob to the nested EmbeddingConfig
// format. Returns the (possibly modified) blob and whether a migration occurred.
// Safe to call repeatedly: returns (blob, false) if already nested.
func migrateEmbeddingBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	if _, isFlat := raw["embedding_enabled"]; !isFlat {
		return blob, false
	}

	type flatShape struct {
		EmbeddingEnabled    bool   `json:"embedding_enabled"`
		EmbeddingModel      string `json:"embedding_model"`
		EmbeddingDimensions int    `json:"embedding_dimensions"`
		EmbeddingBaseURL    string `json:"embedding_base_url"`
		VectorIndexBackend  string `json:"vector_index_backend"`
	}
	var old flatShape
	json.Unmarshal([]byte(blob), &old) //nolint:errcheck — already parsed above

	raw["embedding"] = map[string]any{
		"enabled":        old.EmbeddingEnabled,
		"model":          old.EmbeddingModel,
		"dimensions":     old.EmbeddingDimensions,
		"base_url":       old.EmbeddingBaseURL,
		"vector_backend": old.VectorIndexBackend,
	}
	delete(raw, "embedding_enabled")
	delete(raw, "embedding_model")
	delete(raw, "embedding_dimensions")
	delete(raw, "embedding_base_url")
	delete(raw, "vector_index_backend")

	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}

// migrateDedupBlob rewrites a flat-format config blob to the nested DedupConfig
// format. Returns the (possibly modified) blob and whether a migration occurred.
// Safe to call repeatedly: returns (blob, false) if already nested or no flat
// dedup keys are present.
func migrateDedupBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}

	// Check whether any flat dedup keys exist.
	flatDedupKeys := []string{
		"dedup_book_high_threshold",
		"dedup_book_low_threshold",
		"dedup_author_high_threshold",
		"dedup_author_low_threshold",
		"dedup_auto_merge_enabled",
		"dedup_embeddings_enabled",
		"dedup_llm_auto_merge_high_confidence",
		"dedup_on_import_via_scheduler",
		"dedup_review_model",
	}
	hasFlat := false
	for _, k := range flatDedupKeys {
		if _, ok := raw[k]; ok {
			hasFlat = true
			break
		}
	}
	if !hasFlat {
		return blob, false
	}

	// Build the nested "dedup" object, starting from any already-present sub-object.
	nested, _ := raw["dedup"].(map[string]any)
	if nested == nil {
		nested = make(map[string]any)
	}

	flatToNested := map[string]string{
		"dedup_book_high_threshold":           "book_high_threshold",
		"dedup_book_low_threshold":            "book_low_threshold",
		"dedup_author_high_threshold":         "author_high_threshold",
		"dedup_author_low_threshold":          "author_low_threshold",
		"dedup_auto_merge_enabled":            "auto_merge_enabled",
		"dedup_embeddings_enabled":            "embeddings_enabled",
		"dedup_llm_auto_merge_high_confidence": "llm_auto_merge_high_confidence",
		"dedup_on_import_via_scheduler":       "on_import_via_scheduler",
		"dedup_review_model":                  "review_model",
	}
	for flat, short := range flatToNested {
		if v, ok := raw[flat]; ok {
			nested[short] = v
			delete(raw, flat)
		}
	}
	raw["dedup"] = nested

	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}

// migrateMetadataScoringBlob rewrites flat metadata_embedding_* and write_backup_before_tag_write
// fields to the nested MetadataScoringConfig format. Safe to call repeatedly.
// Returns (blob, false) if already nested or no flat keys present.
func migrateMetadataScoringBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	if _, isFlat := raw["metadata_embedding_scoring_enabled"]; !isFlat {
		return blob, false
	}
	type flatShape struct {
		MetadataEmbeddingScoringEnabled bool    `json:"metadata_embedding_scoring_enabled"`
		MetadataEmbeddingMinScore       float64 `json:"metadata_embedding_min_score"`
		MetadataEmbeddingBestMatchMin   float64 `json:"metadata_embedding_best_match_min"`
		MetadataLLMScoringEnabled       bool    `json:"metadata_llm_scoring_enabled"`
		MetadataLLMRerankEpsilon        float64 `json:"metadata_llm_rerank_epsilon"`
		MetadataLLMRerankTopK           int     `json:"metadata_llm_rerank_top_k"`
		WriteBackupBeforeTagWrite       bool    `json:"write_backup_before_tag_write"`
	}
	var old flatShape
	json.Unmarshal([]byte(blob), &old) //nolint:errcheck
	raw["metadata_scoring"] = map[string]any{
		"embedding_enabled":    old.MetadataEmbeddingScoringEnabled,
		"embedding_min_score":  old.MetadataEmbeddingMinScore,
		"embedding_best_match": old.MetadataEmbeddingBestMatchMin,
		"llm_enabled":          old.MetadataLLMScoringEnabled,
		"llm_rerank_epsilon":   old.MetadataLLMRerankEpsilon,
		"llm_rerank_top_k":     old.MetadataLLMRerankTopK,
		"write_backup_before":  old.WriteBackupBeforeTagWrite,
	}
	delete(raw, "metadata_embedding_scoring_enabled")
	delete(raw, "metadata_embedding_min_score")
	delete(raw, "metadata_embedding_best_match_min")
	delete(raw, "metadata_llm_scoring_enabled")
	delete(raw, "metadata_llm_rerank_epsilon")
	delete(raw, "metadata_llm_rerank_top_k")
	delete(raw, "write_backup_before_tag_write")
	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}

// saveRawBlob writes a pre-marshaled JSON string directly as the config blob.
// Used only by startup migration to persist migrated blobs without re-marshaling.
func saveRawBlob(store database.SettingsStore, rawJSON string) error {
	return store.SetSetting("config_blob", rawJSON, "json", false)
}

// LoadConfigFromDatabase loads settings from database and applies them to AppConfig.
//
// Load order (blob-first):
//  1. If "config_blob" exists: unmarshal the full non-secret Config JSON directly onto
//     AppConfig — every field is restored automatically, no registration needed.
//  2. Always load individual secret rows (they are NOT included in the blob).
//  3. If no blob is found (existing install): fall back to individual applySetting keys
//     so existing data is preserved.
func LoadConfigFromDatabase(store database.SettingsStore) error {
	if store == nil {
		return fmt.Errorf("store is nil")
	}

	slog.Info("Loading configuration from database...")

	settings, err := store.GetAllSettings()
	if err != nil {
		slog.Info("Note Could not load settings from database", "err", err)
		return nil
	}

	// Index settings for O(1) lookup
	settingsMap := make(map[string]*database.Setting, len(settings))
	for i := range settings {
		settingsMap[settings[i].Key] = &settings[i]
	}

	var corruptSecrets []string

	// --- Blob path (new installs and post-first-save upgrades) ---
	blobFound := false
	if blob, ok := settingsMap["config_blob"]; ok && blob.Value != "" {
		// Preserve immutable fields — they must come from the runtime environment,
		// not from a stored blob that could have been created under different flags.
		// WHY Snapshot: reads AppConfig.DatabaseType under the read lock.
		savedDBType := Snapshot().DatabaseType

		// Migrate flat embedding keys → nested EmbeddingConfig format (idempotent).
		blobStr := blob.Value
		if migrated, changed := migrateEmbeddingBlob(blobStr); changed {
			slog.Info("config: migrated embedding fields to nested format")
			blobStr = migrated
			if saveErr := saveRawBlob(store, migrated); saveErr != nil {
				slog.Warn("config: failed to persist migrated blob", "err", saveErr)
			}
		}

		// Migrate flat dedup_* keys → nested DedupConfig format (idempotent).
		if migrated, changed := migrateDedupBlob(blobStr); changed {
			slog.Info("config: migrated dedup fields to nested format")
			blobStr = migrated
			if saveErr := saveRawBlob(store, migrated); saveErr != nil {
				slog.Warn("config: failed to persist migrated dedup blob", "err", saveErr)
			}
		}

		// Migrate flat metadata_embedding_* / write_backup_before_tag_write keys →
		// nested MetadataScoringConfig format (idempotent).
		if migrated, changed := migrateMetadataScoringBlob(blobStr); changed {
			slog.Info("config: migrated metadata_scoring fields to nested format")
			blobStr = migrated
			if saveErr := saveRawBlob(store, migrated); saveErr != nil {
				slog.Warn("config: failed to persist migrated metadata_scoring blob", "err", saveErr)
			}
		}

		var loaded Config
		if err := json.Unmarshal([]byte(blobStr), &loaded); err == nil {
			// WHY Mutate: whole-struct assignment races with HTTP readers.
			Mutate(func(c *Config) {
				*c = loaded
				c.DatabaseType = savedDBType
			})
			blobFound = true
			slog.Info("Loaded config from blob ( bytes)", "count", len(blob.Value))
		} else {
			slog.Warn("Failed to parse config_blob — falling back to individual keys", "err", err)
		}
	}

	// --- Secret loading (always, blob or legacy) ---
	// Secrets are never stored in the blob; they live as individually encrypted rows.
	for _, setting := range settings {
		if !setting.IsSecret {
			continue
		}
		decrypted, err := database.DecryptValue(setting.Value)
		if err != nil {
			slog.Info("WARNING Failed to decrypt setting — will try config file fallback", "setting", setting.Key, "err", err)
			corruptSecrets = append(corruptSecrets, setting.Key)
			continue
		}
		if err := applySetting(setting.Key, decrypted, setting.Type); err != nil {
			slog.Warn("Failed to apply secret setting", "setting", setting.Key, "err", err)
		}
		slog.Debug("LoadConfigFromDatabase found setting", "setting", setting.Key, "decrypted_count", len(decrypted))
	}

	// --- Legacy path (existing installs without a blob) ---
	if !blobFound {
		applied := 0
		for _, setting := range settings {
			if setting.Key == "config_blob" || setting.IsSecret {
				continue // blob already handled; secrets handled above
			}
			if err := applySetting(setting.Key, setting.Value, setting.Type); err != nil {
				slog.Warn("Failed to apply setting", "setting", setting.Key, "err", err)
				continue
			}
			applied++
		}
		slog.Info("Applied settings from database (legacy individual keys)", "applied", applied)
	}

	// Fall back to config file for anything not yet loaded (e.g. corrupted secrets)
	if err := LoadConfigFromFile(); err != nil {
		slog.Warn("Config file fallback failed", "err", err)
	}

	// Re-encrypt secrets that failed to decrypt but were recovered from the config file
	if len(corruptSecrets) > 0 {
		slog.Info("Re-encrypting corrupt secret(s) recovered from config file...", "corruptSecrets_count", len(corruptSecrets))
		for _, key := range corruptSecrets {
			var plaintext string
			// WHY Snapshot: read multiple secret fields under a consistent lock.
			snapSecrets := Snapshot()
			switch key {
			case "openai_api_key":
				plaintext = snapSecrets.OpenAIAPIKey
			case "google_books_api_key":
				plaintext = snapSecrets.GoogleBooksAPIKey
			case "hardcover_api_token":
				plaintext = snapSecrets.HardcoverAPIToken
			case "basic_auth_password":
				plaintext = snapSecrets.BasicAuthPassword
			}
			if plaintext != "" {
				if err := store.SetSetting(key, plaintext, "string", true); err != nil {
					slog.Warn("Failed to re-encrypt setting", "key", key, "err", err)
				} else {
					slog.Info("Re-encrypted setting successfully", "key", key)
				}
			} else {
				if err := store.DeleteSetting(key); err != nil {
					slog.Warn("Could not clear corrupt secret from DB", "key", key, "err", err)
				} else {
					slog.Info("Cleared corrupt secret — re-enter via Settings", "key", key)
				}
			}
		}
	}

	{
		// WHY Snapshot: multi-field read for the debug log.
		snap := Snapshot()
		slog.Debug("After config load EnableAIParsing, OpenAIAPIKey length", "appConfig", snap.EnableAIParsing, "count", len(snap.OpenAIAPIKey))
	}

	// Migrate auto-update window → maintenance window (idempotent)
	MigrateMaintenanceWindow(store)

	// Re-derive defaults that depend on RootDir; use Mutate so the update is
	// visible to concurrent readers via Snapshot().
	Mutate(func(c *Config) {
		if c.OpenLibraryDumpDir == "" && c.RootDir != "" {
			c.OpenLibraryDumpDir = filepath.Join(c.RootDir, "openlibrary-dumps")
		}
	})

	return nil
}

// MigrateMaintenanceWindow migrates auto-update window fields to maintenance window.
// Idempotent — safe to call multiple times.
func MigrateMaintenanceWindow(store database.SettingsStore) {
	migrated, _ := store.GetSetting("maintenance_window_migrated")
	if migrated != nil && migrated.Value == "true" {
		return
	}

	// WHY Mutate: writes to multiple maintenance window fields race with readers.
	var logStart, logEnd int
	Mutate(func(c *Config) {
		// Migrate auto-update window start/end if maintenance window not yet configured
		if c.MaintenanceWindowStart == 0 && c.AutoUpdateWindowStart > 0 {
			c.MaintenanceWindowStart = c.AutoUpdateWindowStart
		}
		if c.MaintenanceWindowEnd == 0 && c.AutoUpdateWindowEnd > 0 {
			c.MaintenanceWindowEnd = c.AutoUpdateWindowEnd
		}
		// Ensure sensible defaults
		if c.MaintenanceWindowStart == 0 && c.MaintenanceWindowEnd == 0 {
			c.MaintenanceWindowStart = 1
			c.MaintenanceWindowEnd = 4
		}
		logStart, logEnd = c.MaintenanceWindowStart, c.MaintenanceWindowEnd
	})

	_ = store.SetSetting("maintenance_window_migrated", "true", "bool", false)
	slog.Info("Maintenance window migration complete (start, end)", "appConfig", logStart, "appConfig", logEnd)
}

// applySetting applies a single setting to AppConfig.
// WHY Mutate: every case here is a write to the global; Mutate serialises the
// write under the write lock so concurrent Snapshot() callers see atomic updates.
func applySetting(key, value, typ string) error {
	// Internal-state keys are not mapped to Config fields; skip Mutate entirely.
	switch key {
	case "maintenance_window_migrated", "maintenance_window_last_run", "maintenance_window_update_completed":
		return nil
	}

	var applyErr error
	Mutate(func(c *Config) {
		switch key {
		// Core paths
		case "root_dir":
			c.RootDir = value
		case "database_path":
			c.DatabasePath = value
		case "playlist_dir":
			c.PlaylistDir = value
		case "setup_complete":
			if b, err := strconv.ParseBool(value); err == nil {
				c.SetupComplete = b
			}

		// Organization
		case "organization_strategy":
			c.OrganizationStrategy = value
		case "scan_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScanOnStartup = b
			}
		case "auto_organize":
			if b, err := strconv.ParseBool(value); err == nil {
				c.AutoOrganize = b
			}
		case "folder_naming_pattern":
			c.FolderNamingPattern = value
		case "file_naming_pattern":
			c.FileNamingPattern = value
		case "create_backups":
			if b, err := strconv.ParseBool(value); err == nil {
				c.CreateBackups = b
			}
		case "supported_extensions":
			var extensions []string
			if err := json.Unmarshal([]byte(value), &extensions); err == nil {
				if len(extensions) > 0 {
					c.SupportedExtensions = extensions
				}
			}
		case "exclude_patterns":
			var patterns []string
			if err := json.Unmarshal([]byte(value), &patterns); err == nil {
				c.ExcludePatterns = patterns
			}

		// Storage quotas
		case "enable_disk_quota":
			if b, err := strconv.ParseBool(value); err == nil {
				c.EnableDiskQuota = b
			}
		case "disk_quota_percent":
			if i, err := strconv.Atoi(value); err == nil {
				c.DiskQuotaPercent = i
			}
		case "enable_user_quotas":
			if b, err := strconv.ParseBool(value); err == nil {
				c.EnableUserQuotas = b
			}
		case "default_user_quota_gb":
			if i, err := strconv.Atoi(value); err == nil {
				c.DefaultUserQuotaGB = i
			}

		// Metadata
		case "auto_fetch_metadata":
			if b, err := strconv.ParseBool(value); err == nil {
				c.AutoFetchMetadata = b
			}
		case "language":
			c.Language = value
		case "metadata_review_default_view":
			c.MetadataReviewDefaultView = value
		case "metadata_sources":
			var sources []MetadataSource
			if err := json.Unmarshal([]byte(value), &sources); err == nil && len(sources) > 0 {
				c.MetadataSources = sources
			}

		// Open Library dumps
		case "openlibrary_dump_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.OpenLibraryDumpEnabled = b
			}
		case "openlibrary_dump_dir":
			c.OpenLibraryDumpDir = value

		// Hardcover.app
		case "hardcover_api_token":
			c.HardcoverAPIToken = value

		// AI parsing
		case "enable_ai_parsing":
			if b, err := strconv.ParseBool(value); err == nil {
				c.EnableAIParsing = b
			}
		case "openai_api_key":
			c.OpenAIAPIKey = value
		case "google_books_api_key":
			c.GoogleBooksAPIKey = value

		// Performance
		case "concurrent_scans":
			if i, err := strconv.Atoi(value); err == nil {
				c.ConcurrentScans = i
			}
		case "operation_timeout_minutes":
			if i, err := strconv.Atoi(value); err == nil {
				c.OperationTimeoutMinutes = i
			}
		case "api_rate_limit_per_minute":
			if i, err := strconv.Atoi(value); err == nil {
				c.APIRateLimitPerMinute = i
			}
		case "auth_rate_limit_per_minute":
			if i, err := strconv.Atoi(value); err == nil {
				c.AuthRateLimitPerMinute = i
			}
		case "json_body_limit_mb":
			if i, err := strconv.Atoi(value); err == nil {
				c.JSONBodyLimitMB = i
			}
		case "upload_body_limit_mb":
			if i, err := strconv.Atoi(value); err == nil {
				c.UploadBodyLimitMB = i
			}
		case "enable_auth":
			if b, err := strconv.ParseBool(value); err == nil {
				c.EnableAuth = b
			}
		case "write_back_metadata":
			if b, err := strconv.ParseBool(value); err == nil {
				c.WriteBackMetadata = b
			}
		case "embed_cover_art":
			if b, err := strconv.ParseBool(value); err == nil {
				c.EmbedCoverArt = b
			}
		case "auto_scan_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.AutoScanEnabled = b
			}
		case "auto_scan_debounce_seconds":
			if i, err := strconv.Atoi(value); err == nil {
				c.AutoScanDebounceSeconds = i
			}

		// Memory management
		case "memory_limit_type":
			c.MemoryLimitType = value
		case "cache_size":
			if i, err := strconv.Atoi(value); err == nil {
				c.CacheSize = i
			}
		case "cache_invalidate_on_book_update":
			c.CacheInvalidateOnBookUpdate = value == "true"
		case "metadata_fetch_cache_ttl_days":
			if i, err := strconv.Atoi(value); err == nil {
				c.MetadataFetchCacheTTLDays = i
			}
		case "memory_limit_percent":
			if i, err := strconv.Atoi(value); err == nil {
				c.MemoryLimitPercent = i
			}
		case "memory_limit_mb":
			if i, err := strconv.Atoi(value); err == nil {
				c.MemoryLimitMB = i
			}

		// Logging
		case "log_level":
			c.LogLevel = value
		case "log_format":
			c.LogFormat = value
		case "enable_json_logging":
			if b, err := strconv.ParseBool(value); err == nil {
				c.EnableJsonLogging = b
			}

		// Auto-update
		case "auto_update_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.AutoUpdateEnabled = b
			}
		case "auto_update_channel":
			c.AutoUpdateChannel = value
		case "auto_update_check_minutes":
			if i, err := strconv.Atoi(value); err == nil {
				c.AutoUpdateCheckMinutes = i
			}
		case "auto_update_window_start":
			if i, err := strconv.Atoi(value); err == nil {
				c.AutoUpdateWindowStart = i
			}
		case "auto_update_window_end":
			if i, err := strconv.Atoi(value); err == nil {
				c.AutoUpdateWindowEnd = i
			}

		// Lifecycle / retention
		case "purge_soft_deleted_after_days":
			if i, err := strconv.Atoi(value); err == nil {
				c.PurgeSoftDeletedAfterDays = i
			}
		case "purge_soft_deleted_delete_files":
			if b, err := strconv.ParseBool(value); err == nil {
				c.PurgeSoftDeletedDeleteFiles = b
			}

		// iTunes sync
		case "itunes_sync_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ITunesSyncEnabled = b
			}
		case "itunes_sync_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.ITunesSyncInterval = i
			}
		case "itl_write_back_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ITLWriteBackEnabled = b
			}
		case "itunes_library_write_path", "itunes_library_itl_path":
			c.ITunesLibraryWritePath = value
		case "itunes_library_read_path", "itunes_library_xml_path":
			c.ITunesLibraryReadPath = value
		case "itunes_auto_write_back":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ITunesAutoWriteBack = b
			}
		case "itunes_path_trim_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ITunesPathTrimEnabled = b
			}
		case "itunes_windows_root_path":
			c.ITunesWindowsRootPath = value
		case "itunes_media_root":
			c.ITunesMediaRoot = value
		case "itunes_path_mappings":
			var mappings []ITunesPathMap
			if err := json.Unmarshal([]byte(value), &mappings); err == nil {
				c.ITunesPathMappings = mappings
			}

		// Maintenance window
		case "maintenance_window_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowEnabled = b
			}
		case "maintenance_window_start":
			if i, err := strconv.Atoi(value); err == nil {
				c.MaintenanceWindowStart = i
			}
		case "maintenance_window_end":
			if i, err := strconv.Atoi(value); err == nil {
				c.MaintenanceWindowEnd = i
			}
		case "maintenance_window_dedup_refresh":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowDedupRefresh = b
			}
		case "maintenance_window_series_prune":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowSeriesPrune = b
			}
		case "maintenance_window_author_split":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowAuthorSplit = b
			}
		case "maintenance_window_tombstone_cleanup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowTombstoneCleanup = b
			}
		case "maintenance_window_reconcile":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowReconcile = b
			}
		case "maintenance_window_purge_deleted":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowPurgeDeleted = b
			}
		case "maintenance_window_purge_old_logs":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowPurgeOldLogs = b
			}
		case "maintenance_window_db_optimize":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowDbOptimize = b
			}
		case "maintenance_window_library_scan":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowLibraryScan = b
			}
		case "maintenance_window_library_organize":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowLibraryOrganize = b
			}
		case "maintenance_window_metadata_refresh":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MaintenanceWindowMetadataRefresh = b
			}

		// Scheduled maintenance tasks
		case "scheduled_dedup_refresh_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledDedupRefreshEnabled = b
			}
		case "scheduled_dedup_refresh_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.ScheduledDedupRefreshInterval = i
			}
		case "scheduled_dedup_refresh_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledDedupRefreshOnStartup = b
			}
		case "scheduled_author_split_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledAuthorSplitEnabled = b
			}
		case "scheduled_author_split_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.ScheduledAuthorSplitInterval = i
			}
		case "scheduled_author_split_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledAuthorSplitOnStartup = b
			}
		case "scheduled_db_optimize_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledDbOptimizeEnabled = b
			}
		case "scheduled_db_optimize_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.ScheduledDbOptimizeInterval = i
			}
		case "scheduled_db_optimize_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledDbOptimizeOnStartup = b
			}
		case "scheduled_metadata_refresh_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledMetadataRefreshEnabled = b
			}
		case "scheduled_metadata_refresh_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.ScheduledMetadataRefreshInterval = i
			}
		case "scheduled_metadata_refresh_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledMetadataRefreshOnStartup = b
			}

		case "scheduled_resolve_production_authors_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledResolveProductionAuthorsEnabled = b
			}
		case "scheduled_resolve_production_authors_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.ScheduledResolveProductionAuthorsInterval = i
			}

		case "scheduled_series_prune_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledSeriesPruneEnabled = b
			}
		case "scheduled_series_prune_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.ScheduledSeriesPruneInterval = i
			}
		case "scheduled_series_prune_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ScheduledSeriesPruneOnStartup = b
			}

		// Basic auth
		case "basic_auth_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.BasicAuthEnabled = b
			}
		case "basic_auth_username":
			c.BasicAuthUsername = value
		case "basic_auth_password":
			c.BasicAuthPassword = value

		// Dedup thresholds + behaviour (legacy flat keys — new installs use the blob).
		// These cases handle pre-Wave-2 installs that stored settings as individual rows.
		case "dedup_book_high_threshold":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.Dedup.BookHighThreshold = f
			}
		case "dedup_book_low_threshold":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.Dedup.BookLowThreshold = f
			}
		case "dedup_author_high_threshold":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.Dedup.AuthorHighThreshold = f
			}
		case "dedup_author_low_threshold":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.Dedup.AuthorLowThreshold = f
			}
		case "dedup_auto_merge_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Dedup.AutoMergeEnabled = b
			}
		case "dedup_embeddings_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Dedup.EmbeddingsEnabled = b
			}
		case "dedup_llm_auto_merge_high_confidence":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Dedup.LLMAutoMergeHighConfidence = b
			}
		case "dedup_on_import_via_scheduler":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Dedup.OnImportViaScheduler = b
			}
		case "dedup_review_model":
			c.Dedup.ReviewModel = value

		// Metadata scoring (legacy flat keys — new installs use the blob).
		// These cases handle pre-Wave-3 installs that stored settings as individual rows.
		case "metadata_embedding_scoring_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MetadataScoring.EmbeddingEnabled = b
			}
		case "metadata_embedding_min_score":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.MetadataScoring.EmbeddingMinScore = f
			}
		case "metadata_embedding_best_match_min":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.MetadataScoring.EmbeddingBestMatch = f
			}
		case "metadata_llm_scoring_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MetadataScoring.LLMEnabled = b
			}
		case "metadata_llm_rerank_epsilon":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.MetadataScoring.LLMRerankEpsilon = f
			}
		case "metadata_llm_rerank_top_k":
			if n, err := strconv.Atoi(value); err == nil {
				c.MetadataScoring.LLMRerankTopK = n
			}
		case "write_backup_before_tag_write":
			if b, err := strconv.ParseBool(value); err == nil {
				c.MetadataScoring.WriteBackupBefore = b
			}

		default:
			applyErr = fmt.Errorf("unknown setting key: %s", key)
		}
	}) // end Mutate
	return applyErr
}

// SaveConfigToDatabase persists current AppConfig to database AND config file.
//
// Storage format (v2, blob-based):
//   - "config_blob": full Config JSON with secrets zeroed — automatically includes
//     every field in config.Config with no manual registration.
//   - Individual encrypted rows for each secret (openai_api_key, etc.).
//
// Existing installs that have never saved under v2 still load correctly via the
// legacy applySetting fallback in LoadConfigFromDatabase.
func SaveConfigToDatabase(store database.SettingsStore) error {
	if store == nil {
		return fmt.Errorf("store is nil")
	}

	slog.Info("Saving configuration to database...")

	// WHY Snapshot: consistent read of all fields under a read lock before we
	// build the blob; a concurrent Mutate could otherwise see a torn read.
	// Build a safe copy with secrets zeroed — they are saved separately (encrypted).
	safeConfig := Snapshot()
	safeConfig.OpenAIAPIKey = ""
	safeConfig.GoogleBooksAPIKey = ""
	safeConfig.HardcoverAPIToken = ""
	safeConfig.BasicAuthPassword = ""

	blobJSON, err := json.Marshal(safeConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal config blob: %w", err)
	}
	if err := store.SetSetting("config_blob", string(blobJSON), "json", false); err != nil {
		return fmt.Errorf("failed to save config blob: %w", err)
	}

	// Persist secrets individually (encrypted).
	// If the current AppConfig value is empty, preserve the existing DB entry
	// so a page-load that clears the field doesn't wipe a previously saved key.
	type secretEntry struct {
		key   string
		value string
	}
	snap := Snapshot()
	secrets := []secretEntry{
		{"openai_api_key", snap.OpenAIAPIKey},
		{"google_books_api_key", snap.GoogleBooksAPIKey},
		{"hardcover_api_token", snap.HardcoverAPIToken},
		{"basic_auth_password", snap.BasicAuthPassword},
	}
	for _, s := range secrets {
		if s.value == "" {
			existing, err := store.GetSetting(s.key)
			if err == nil && existing != nil && existing.Value != "" {
				slog.Debug("Preserving existing secret (current value empty)", "s", s.key)
				continue
			}
		}
		if err := store.SetSetting(s.key, s.value, "string", true); err != nil {
			slog.Warn("Failed to save secret", "s", s.key, "err", err)
		}
	}

	slog.Info("Configuration saved to database (blob + secrets)", "secrets_count", len(secrets))

	// Also save to config file as a reliable fallback
	if err := SaveConfigToFile(); err != nil {
		slog.Warn("Failed to save config file", "err", err)
	}

	return nil
}

// SyncConfigFromEnv loads env vars from viper and overrides AppConfig (without saving to DB).
// Only non-empty env values override DB-loaded values. This prevents empty env vars or
// viper defaults from wiping out API keys that were loaded from the database.
// WHY Mutate: each assignment here is a concurrent write to the global; use Mutate
// so callers of Snapshot() see a consistent post-sync view.
func SyncConfigFromEnv() {
	Mutate(func(c *Config) {
		if viper.IsSet("root_dir") {
			if val := viper.GetString("root_dir"); val != "" {
				c.RootDir = val
			}
		}
		if viper.IsSet("openai_api_key") {
			if val := viper.GetString("openai_api_key"); val != "" {
				c.OpenAIAPIKey = val
				slog.Debug("SyncConfigFromEnv overriding OpenAI API key from env/config (length )", "val_count", len(val))
			}
		}
		if viper.IsSet("google_books_api_key") {
			if val := viper.GetString("google_books_api_key"); val != "" {
				c.GoogleBooksAPIKey = val
			}
		}
		if viper.IsSet("enable_ai_parsing") {
			c.EnableAIParsing = viper.GetBool("enable_ai_parsing")
		}
		if viper.IsSet("embedding.base_url") {
			if val := viper.GetString("embedding.base_url"); val != "" {
				c.Embedding.BaseURL = val
			}
		}
		if viper.IsSet("acoustid_api_key") {
			if val := viper.GetString("acoustid_api_key"); val != "" {
				c.AcoustIDAPIKey = val
			}
		}
	})
}
