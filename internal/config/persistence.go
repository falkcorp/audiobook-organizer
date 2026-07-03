// file: internal/config/persistence.go
// version: 1.29.0
// guid: 9c8d7e6f-5a4b-3c2d-1e0f-9a8b7c6d5e4f
// last-edited: 2026-07-03

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
		"dedup_book_high_threshold":            "book_high_threshold",
		"dedup_book_low_threshold":             "book_low_threshold",
		"dedup_author_high_threshold":          "author_high_threshold",
		"dedup_author_low_threshold":           "author_low_threshold",
		"dedup_auto_merge_enabled":             "auto_merge_enabled",
		"dedup_embeddings_enabled":             "embeddings_enabled",
		"dedup_llm_auto_merge_high_confidence": "llm_auto_merge_high_confidence",
		"dedup_on_import_via_scheduler":        "on_import_via_scheduler",
		"dedup_review_model":                   "review_model",
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

// migrateAIBackendBlob derives the nested ai_backend object (backend-mode
// toggle) from the legacy flat/nested AI signal fields (openai_api_key,
// embedding.enabled/base_url/model, enable_ai_parsing, metadata_scoring.llm_enabled).
// It mirrors Config.EffectiveEmbeddingMode / Config.EffectiveLLMMode so the
// persisted modes match what those helpers would resolve at runtime. Safe to
// call repeatedly: returns (blob, false) once ai_backend is present, or when no
// legacy AI signal fields exist to derive from.
//
// Chained AFTER migrateMetadataScoringBlob so the embedding / metadata_scoring
// sub-objects are already in their nested shape when we read them here.
func migrateAIBackendBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	// Idempotent: already migrated.
	if _, ok := raw["ai_backend"]; ok {
		return blob, false
	}
	// Need at least one legacy signal to derive a mode from; otherwise leave the
	// blob untouched and let runtime effective-mode resolution handle it.
	_, hasKey := raw["openai_api_key"]
	_, hasEmbedding := raw["embedding"]
	_, hasParsing := raw["enable_ai_parsing"]
	_, hasMetaScoring := raw["metadata_scoring"]
	if !hasKey && !hasEmbedding && !hasParsing && !hasMetaScoring {
		return blob, false
	}

	apiKey, _ := raw["openai_api_key"].(string)
	enableAIParsing, _ := raw["enable_ai_parsing"].(bool)

	// Embedding.Enabled defaults true; an absent nested object counts as enabled.
	embEnabled := true
	embBaseURL := ""
	embModel := ""
	if emb, ok := raw["embedding"].(map[string]any); ok {
		if v, ok := emb["enabled"].(bool); ok {
			embEnabled = v
		}
		embBaseURL, _ = emb["base_url"].(string)
		embModel, _ = emb["model"].(string)
	}

	llmEnabled := false
	if ms, ok := raw["metadata_scoring"].(map[string]any); ok {
		llmEnabled, _ = ms["llm_enabled"].(bool)
	}

	// Derive embedding mode (mirrors Config.EffectiveEmbeddingMode).
	embeddingMode := AIBackendModeDisabled
	switch {
	case !embEnabled:
		embeddingMode = AIBackendModeDisabled
	case embBaseURL != "":
		embeddingMode = AIBackendModeLocal
	case apiKey != "":
		embeddingMode = AIBackendModeOpenAI
	}

	// Derive LLM mode (mirrors Config.EffectiveLLMMode).
	llmMode := AIBackendModeDisabled
	if apiKey != "" && (enableAIParsing || llmEnabled) {
		llmMode = AIBackendModeOpenAI
	}

	aiBackend := map[string]any{
		"embedding_mode": embeddingMode,
		"llm_mode":       llmMode,
	}
	// When a local embedding backend was configured via the legacy
	// embedding.base_url field, carry those coordinates onto the new fields so a
	// future explicit toggle (or the local register branch) has them. Safe to
	// mutate: we're building a fresh map, not the shared AppConfig.
	if embBaseURL != "" {
		aiBackend["local_base_url"] = embBaseURL
		if embModel != "" {
			aiBackend["local_embedding_model"] = embModel
		}
	}
	raw["ai_backend"] = aiBackend

	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}

// migrateITunesBlob rewrites flat itunes_* / itl_* config fields to the nested
// ITunesConfig format. Returns the (possibly modified) blob and whether a migration
// occurred. Safe to call repeatedly: returns (blob, false) if already nested or no
// flat iTunes keys are present.
func migrateITunesBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	if _, isFlat := raw["itunes_sync_enabled"]; !isFlat {
		return blob, false
	}

	type flatShape struct {
		ITunesSyncEnabled      bool   `json:"itunes_sync_enabled"`
		ITunesSyncInterval     int    `json:"itunes_sync_interval"`
		ITLWriteBackEnabled    bool   `json:"itl_write_back_enabled"`
		ITunesLibraryWritePath string `json:"itunes_library_write_path"`
		ITunesLibraryReadPath  string `json:"itunes_library_read_path"`
		ITunesAutoWriteBack    bool   `json:"itunes_auto_write_back"`
		ITunesPathTrimEnabled  bool   `json:"itunes_path_trim_enabled"`
		ITunesWindowsRootPath  string `json:"itunes_windows_root_path"`
		ITunesMediaRoot        string `json:"itunes_media_root"`
	}
	var old flatShape
	json.Unmarshal([]byte(blob), &old) //nolint:errcheck — already parsed above

	raw["itunes"] = map[string]any{
		"sync_enabled":       old.ITunesSyncEnabled,
		"sync_interval":      old.ITunesSyncInterval,
		"write_back_enabled": old.ITLWriteBackEnabled,
		"library_write_path": old.ITunesLibraryWritePath,
		"library_read_path":  old.ITunesLibraryReadPath,
		"auto_write_back":    old.ITunesAutoWriteBack,
		"path_trim_enabled":  old.ITunesPathTrimEnabled,
		"windows_root_path":  old.ITunesWindowsRootPath,
		"media_root":         old.ITunesMediaRoot,
	}
	delete(raw, "itunes_sync_enabled")
	delete(raw, "itunes_sync_interval")
	delete(raw, "itl_write_back_enabled")
	delete(raw, "itunes_library_write_path")
	delete(raw, "itunes_library_read_path")
	delete(raw, "itunes_auto_write_back")
	delete(raw, "itunes_path_trim_enabled")
	delete(raw, "itunes_windows_root_path")
	delete(raw, "itunes_media_root")

	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}

// migrateMaintenanceBlob rewrites flat maintenance_window_* fields to the nested
// MaintenanceConfig format. Safe to call repeatedly.
func migrateMaintenanceBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	if _, isFlat := raw["maintenance_window_enabled"]; !isFlat {
		return blob, false
	}
	type flatShape struct {
		Enabled              bool `json:"maintenance_window_enabled"`
		WindowStart          int  `json:"maintenance_window_start"`
		WindowEnd            int  `json:"maintenance_window_end"`
		DedupRefresh         bool `json:"maintenance_window_dedup_refresh"`
		SeriesPrune          bool `json:"maintenance_window_series_prune"`
		AuthorSplit          bool `json:"maintenance_window_author_split"`
		TombstoneCleanup     bool `json:"maintenance_window_tombstone_cleanup"`
		Reconcile            bool `json:"maintenance_window_reconcile"`
		PurgeDeleted         bool `json:"maintenance_window_purge_deleted"`
		PurgeOldLogs         bool `json:"maintenance_window_purge_old_logs"`
		DbOptimize           bool `json:"maintenance_window_db_optimize"`
		LibraryScan          bool `json:"maintenance_window_library_scan"`
		LibraryOrganize      bool `json:"maintenance_window_library_organize"`
		MetadataRefresh      bool `json:"maintenance_window_metadata_refresh"`
		LibrarySizeRefresh   bool `json:"maintenance_window_library_size_refresh"`
		AcoustIDOnlineLookup bool `json:"maintenance_window_acoustid_online_lookup"`
		AcoustIDNightlyLimit int  `json:"acoustid_online_lookup_nightly_limit"`
	}
	var old flatShape
	json.Unmarshal([]byte(blob), &old) //nolint:errcheck — already parsed above
	raw["maintenance"] = map[string]any{
		"enabled":                old.Enabled,
		"window_start":           old.WindowStart,
		"window_end":             old.WindowEnd,
		"dedup_refresh":          old.DedupRefresh,
		"series_prune":           old.SeriesPrune,
		"author_split":           old.AuthorSplit,
		"tombstone_cleanup":      old.TombstoneCleanup,
		"reconcile":              old.Reconcile,
		"purge_deleted":          old.PurgeDeleted,
		"purge_old_logs":         old.PurgeOldLogs,
		"db_optimize":            old.DbOptimize,
		"library_scan":           old.LibraryScan,
		"library_organize":       old.LibraryOrganize,
		"metadata_refresh":       old.MetadataRefresh,
		"library_size_refresh":   old.LibrarySizeRefresh,
		"acoustid_online_lookup": old.AcoustIDOnlineLookup,
		"acoustid_nightly_limit": old.AcoustIDNightlyLimit,
	}
	// delete all flat keys
	for _, k := range []string{
		"maintenance_window_enabled", "maintenance_window_start", "maintenance_window_end",
		"maintenance_window_dedup_refresh", "maintenance_window_series_prune",
		"maintenance_window_author_split", "maintenance_window_tombstone_cleanup",
		"maintenance_window_reconcile", "maintenance_window_purge_deleted",
		"maintenance_window_purge_old_logs", "maintenance_window_db_optimize",
		"maintenance_window_library_scan", "maintenance_window_library_organize",
		"maintenance_window_metadata_refresh", "maintenance_window_library_size_refresh",
		"maintenance_window_acoustid_online_lookup", "acoustid_online_lookup_nightly_limit",
	} {
		delete(raw, k)
	}
	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}

// migrateScheduledBlob rewrites flat scheduled_* fields to the nested
// ScheduledTasksConfig format. Safe to call repeatedly (idempotent).
// Sentinel key: "scheduled_dedup_refresh_enabled".
func migrateScheduledBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	if _, isFlat := raw["scheduled_dedup_refresh_enabled"]; !isFlat {
		return blob, false
	}
	raw = remapScheduledKeys(raw)
	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}

// remapScheduledKeys translates flat scheduled_* keys in a config map to the
// nested ScheduledTasksConfig format. Merges into any existing "scheduled"
// sub-object to avoid zeroing sibling fields.
func remapScheduledKeys(payload map[string]any) map[string]any {
	type mapping struct{ group, field string }
	flatMap := map[string]mapping{
		"scheduled_dedup_refresh_enabled":               {group: "dedup_refresh", field: "enabled"},
		"scheduled_dedup_refresh_interval":              {group: "dedup_refresh", field: "interval"},
		"scheduled_dedup_refresh_on_startup":            {group: "dedup_refresh", field: "on_startup"},
		"scheduled_author_split_enabled":                {group: "author_split", field: "enabled"},
		"scheduled_author_split_interval":               {group: "author_split", field: "interval"},
		"scheduled_author_split_on_startup":             {group: "author_split", field: "on_startup"},
		"scheduled_db_optimize_enabled":                 {group: "db_optimize", field: "enabled"},
		"scheduled_db_optimize_interval":                {group: "db_optimize", field: "interval"},
		"scheduled_db_optimize_on_startup":              {group: "db_optimize", field: "on_startup"},
		"scheduled_metadata_refresh_enabled":            {group: "metadata_refresh", field: "enabled"},
		"scheduled_metadata_refresh_interval":           {group: "metadata_refresh", field: "interval"},
		"scheduled_metadata_refresh_on_startup":         {group: "metadata_refresh", field: "on_startup"},
		"scheduled_resolve_production_authors_enabled":  {group: "resolve_production_authors", field: "enabled"},
		"scheduled_resolve_production_authors_interval": {group: "resolve_production_authors", field: "interval"},
		"scheduled_series_prune_enabled":                {group: "series_prune", field: "enabled"},
		"scheduled_series_prune_interval":               {group: "series_prune", field: "interval"},
		"scheduled_series_prune_on_startup":             {group: "series_prune", field: "on_startup"},
		"scheduled_ai_dedup_batch_enabled":              {group: "ai_dedup_batch", field: "enabled"},
		"scheduled_ai_dedup_batch_interval":             {group: "ai_dedup_batch", field: "interval"},
		"scheduled_ai_dedup_batch_on_startup":           {group: "ai_dedup_batch", field: "on_startup"},
		"scheduled_reconcile_enabled":                   {group: "reconcile", field: "enabled"},
		"scheduled_reconcile_interval":                  {group: "reconcile", field: "interval"},
		"scheduled_reconcile_on_startup":                {group: "reconcile", field: "on_startup"},
	}
	groups := make(map[string]map[string]any)
	for flat, m := range flatMap {
		if v, ok := payload[flat]; ok {
			if groups[m.group] == nil {
				groups[m.group] = make(map[string]any)
			}
			groups[m.group][m.field] = v
			delete(payload, flat)
		}
	}
	if len(groups) == 0 {
		return payload
	}
	existing, _ := payload["scheduled"].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}
	for group, fields := range groups {
		if existingGroup, ok := existing[group].(map[string]any); ok {
			for k, v := range fields {
				existingGroup[k] = v
			}
		} else {
			existing[group] = fields
		}
	}
	payload["scheduled"] = existing
	return payload
}

// migrateAutoUpdateBlob rewrites flat auto_update_* fields to the nested AutoUpdateConfig format.
func migrateAutoUpdateBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	if _, isFlat := raw["auto_update_enabled"]; !isFlat {
		return blob, false
	}
	type flatShape struct {
		Enabled      bool   `json:"auto_update_enabled"`
		Channel      string `json:"auto_update_channel"`
		CheckMinutes int    `json:"auto_update_check_minutes"`
		WindowStart  int    `json:"auto_update_window_start"`
		WindowEnd    int    `json:"auto_update_window_end"`
	}
	var old flatShape
	json.Unmarshal([]byte(blob), &old) //nolint:errcheck
	raw["auto_update"] = map[string]any{
		"enabled":       old.Enabled,
		"channel":       old.Channel,
		"check_minutes": old.CheckMinutes,
		"window_start":  old.WindowStart,
		"window_end":    old.WindowEnd,
	}
	delete(raw, "auto_update_enabled")
	delete(raw, "auto_update_channel")
	delete(raw, "auto_update_check_minutes")
	delete(raw, "auto_update_window_start")
	delete(raw, "auto_update_window_end")
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

		// Derive the nested ai_backend object (backend-mode toggle) from the
		// legacy AI signal fields (idempotent). Runs after the metadata_scoring
		// migration so metadata_scoring.llm_enabled is in nested shape.
		if migrated, changed := migrateAIBackendBlob(blobStr); changed {
			slog.Info("config: derived ai_backend modes from legacy fields")
			blobStr = migrated
			if saveErr := saveRawBlob(store, migrated); saveErr != nil {
				slog.Warn("config: failed to persist migrated ai_backend blob", "err", saveErr)
			}
		}

		// Migrate flat itunes_* / itl_* keys → nested ITunesConfig format (idempotent).
		if migrated, changed := migrateITunesBlob(blobStr); changed {
			slog.Info("config: migrated iTunes fields to nested format")
			blobStr = migrated
			if saveErr := saveRawBlob(store, migrated); saveErr != nil {
				slog.Warn("config: failed to persist migrated iTunes blob", "err", saveErr)
			}
		}

		// Migrate flat maintenance_window_* keys → nested MaintenanceConfig format (idempotent).
		if migrated, changed := migrateMaintenanceBlob(blobStr); changed {
			slog.Info("config: migrated maintenance fields to nested format")
			blobStr = migrated
			if saveErr := saveRawBlob(store, migrated); saveErr != nil {
				slog.Warn("config: failed to persist migrated maintenance blob", "err", saveErr)
			}
		}

		// Migrate flat scheduled_* keys → nested ScheduledTasksConfig format (idempotent).
		if migrated, changed := migrateScheduledBlob(blobStr); changed {
			slog.Info("config: migrated scheduled task fields to nested format")
			blobStr = migrated
			if saveErr := saveRawBlob(store, migrated); saveErr != nil {
				slog.Warn("config: failed to persist migrated scheduled blob", "err", saveErr)
			}
		}

		// Migrate flat auto_update_* keys → nested AutoUpdateConfig format (idempotent).
		if migrated, changed := migrateAutoUpdateBlob(blobStr); changed {
			slog.Info("config: migrated auto-update fields to nested format")
			blobStr = migrated
			if saveErr := saveRawBlob(store, migrated); saveErr != nil {
				slog.Warn("config: failed to persist migrated auto-update blob", "err", saveErr)
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
		if c.Maintenance.WindowStart == 0 && c.AutoUpdate.WindowStart > 0 {
			c.Maintenance.WindowStart = c.AutoUpdate.WindowStart
		}
		if c.Maintenance.WindowEnd == 0 && c.AutoUpdate.WindowEnd > 0 {
			c.Maintenance.WindowEnd = c.AutoUpdate.WindowEnd
		}
		// Ensure sensible defaults
		if c.Maintenance.WindowStart == 0 && c.Maintenance.WindowEnd == 0 {
			c.Maintenance.WindowStart = 1
			c.Maintenance.WindowEnd = 4
		}
		logStart, logEnd = c.Maintenance.WindowStart, c.Maintenance.WindowEnd
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
				c.AutoUpdate.Enabled = b
			}
		case "auto_update_channel":
			c.AutoUpdate.Channel = value
		case "auto_update_check_minutes":
			if i, err := strconv.Atoi(value); err == nil {
				c.AutoUpdate.CheckMinutes = i
			}
		case "auto_update_window_start":
			if i, err := strconv.Atoi(value); err == nil {
				c.AutoUpdate.WindowStart = i
			}
		case "auto_update_window_end":
			if i, err := strconv.Atoi(value); err == nil {
				c.AutoUpdate.WindowEnd = i
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

		// iTunes sync (legacy flat keys — new installs use the blob).
		// These cases handle pre-Wave-4 installs that stored settings as individual rows.
		case "itunes_sync_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ITunes.SyncEnabled = b
			}
		case "itunes_sync_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.ITunes.SyncInterval = i
			}
		case "itl_write_back_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ITunes.WriteBackEnabled = b
			}
		case "itunes_library_write_path", "itunes_library_itl_path":
			c.ITunes.LibraryWritePath = value
		case "itunes_library_read_path", "itunes_library_xml_path":
			c.ITunes.LibraryReadPath = value
		case "itunes_auto_write_back":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ITunes.AutoWriteBack = b
			}
		case "itunes_path_trim_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.ITunes.PathTrimEnabled = b
			}
		case "itunes_windows_root_path":
			c.ITunes.WindowsRootPath = value
		case "itunes_media_root":
			c.ITunes.MediaRoot = value
		case "itunes_path_mappings":
			var mappings []ITunesPathMap
			if err := json.Unmarshal([]byte(value), &mappings); err == nil {
				c.ITunes.PathMappings = mappings
			}

		// Maintenance window
		case "maintenance_window_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.Enabled = b
			}
		case "maintenance_window_start":
			if i, err := strconv.Atoi(value); err == nil {
				c.Maintenance.WindowStart = i
			}
		case "maintenance_window_end":
			if i, err := strconv.Atoi(value); err == nil {
				c.Maintenance.WindowEnd = i
			}
		case "maintenance_window_dedup_refresh":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.DedupRefresh = b
			}
		case "maintenance_window_series_prune":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.SeriesPrune = b
			}
		case "maintenance_window_author_split":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.AuthorSplit = b
			}
		case "maintenance_window_tombstone_cleanup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.TombstoneCleanup = b
			}
		case "maintenance_window_reconcile":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.Reconcile = b
			}
		case "maintenance_window_purge_deleted":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.PurgeDeleted = b
			}
		case "maintenance_window_purge_old_logs":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.PurgeOldLogs = b
			}
		case "maintenance_window_db_optimize":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.DbOptimize = b
			}
		case "maintenance_window_library_scan":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.LibraryScan = b
			}
		case "maintenance_window_library_organize":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.LibraryOrganize = b
			}
		case "maintenance_window_metadata_refresh":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.MetadataRefresh = b
			}
		case "maintenance_window_library_size_refresh":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.LibrarySizeRefresh = b
			}
		case "maintenance_window_acoustid_online_lookup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Maintenance.AcoustIDOnlineLookup = b
			}
		case "acoustid_online_lookup_nightly_limit":
			if i, err := strconv.Atoi(value); err == nil {
				c.Maintenance.AcoustIDNightlyLimit = i
			}

		// Scheduled maintenance tasks
		case "scheduled_dedup_refresh_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.DedupRefresh.Enabled = b
			}
		case "scheduled_dedup_refresh_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.Scheduled.DedupRefresh.Interval = i
			}
		case "scheduled_dedup_refresh_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.DedupRefresh.OnStartup = b
			}
		case "scheduled_author_split_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.AuthorSplit.Enabled = b
			}
		case "scheduled_author_split_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.Scheduled.AuthorSplit.Interval = i
			}
		case "scheduled_author_split_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.AuthorSplit.OnStartup = b
			}
		case "scheduled_db_optimize_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.DbOptimize.Enabled = b
			}
		case "scheduled_db_optimize_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.Scheduled.DbOptimize.Interval = i
			}
		case "scheduled_db_optimize_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.DbOptimize.OnStartup = b
			}
		case "scheduled_metadata_refresh_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.MetadataRefresh.Enabled = b
			}
		case "scheduled_metadata_refresh_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.Scheduled.MetadataRefresh.Interval = i
			}
		case "scheduled_metadata_refresh_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.MetadataRefresh.OnStartup = b
			}

		case "scheduled_resolve_production_authors_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.ResolveProductionAuthors.Enabled = b
			}
		case "scheduled_resolve_production_authors_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.Scheduled.ResolveProductionAuthors.Interval = i
			}

		case "scheduled_series_prune_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.SeriesPrune.Enabled = b
			}
		case "scheduled_series_prune_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.Scheduled.SeriesPrune.Interval = i
			}
		case "scheduled_series_prune_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.SeriesPrune.OnStartup = b
			}

		case "scheduled_ai_dedup_batch_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.AIDedupBatch.Enabled = b
			}
		case "scheduled_ai_dedup_batch_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.Scheduled.AIDedupBatch.Interval = i
			}
		case "scheduled_ai_dedup_batch_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.AIDedupBatch.OnStartup = b
			}

		case "scheduled_reconcile_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.Reconcile.Enabled = b
			}
		case "scheduled_reconcile_interval":
			if i, err := strconv.Atoi(value); err == nil {
				c.Scheduled.Reconcile.Interval = i
			}
		case "scheduled_reconcile_on_startup":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Scheduled.Reconcile.OnStartup = b
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
		case "dedup_auto_resolve_enabled":
			if b, err := strconv.ParseBool(value); err == nil {
				c.Dedup.AutoResolveEnabled = b
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
