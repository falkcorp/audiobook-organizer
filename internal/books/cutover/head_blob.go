// file: internal/books/cutover/head_blob.go
// version: 1.0.0
// guid: e39cb0ac-723d-4ee7-889c-aea084c9d4e6
// last-edited: 2026-11-11

// FROZEN COPY of HEAD (748fd787a) config_blob read path (the migrate*Blob transforms and the blob decode); read-only; deleted in R3 with the other HEAD-format readers (05-config §1.12.2, 03 §3.8).

package cutover

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Constants HEAD's transforms read. The head AI-backend mode strings are
// internal/config/config.go AIBackendMode*; the endpoint row ids and OpenAI
// defaults are ai_endpoints.go; the capability ids, protocol names, feature
// names and the migration baseline are internal/aidispatch (capabilities.go,
// dispatch.go), frozen here as plain strings so this package imports nothing
// from them.
const (
	headAIBackendModeDisabled            = "disabled"
	headAIBackendModeOpenAI              = "openai"
	headAIBackendModeLocal               = "local"
	headAIBackendModeOpenAIFallbackLocal = "openai-fallback-local"

	headAIEndpointIDLocalLLM = "local-llm"
	headAIEndpointIDOpenAI   = "openai"
	headAIEndpointIDLocalUV  = "local-uv"

	headDefaultOpenAIBaseURL   = "https://api.openai.com/v1"
	headDefaultOpenAIChatModel = "gpt-5-mini"
	headLocalUVEndpointURL     = "local:uv-openai-whisper"

	headProtocolOpenAICompat  = "openai_compat"
	headProtocolWhisperServer = "whisper_server"
	headProtocolLocalProcess  = "local_process"

	headFeatureBatchAPI = "batch_api"
	headFeatureVision   = "vision"

	headCapLLMFilenameParse    = "llm.filename_parse"
	headCapLLMAudiobookParse   = "llm.audiobook_parse"
	headCapLLMCoverArtVision   = "llm.cover_art_vision"
	headCapLLMAuthorReview     = "llm.author_review"
	headCapLLMAuthorDiscovery  = "llm.author_discovery"
	headCapLLMMetadataRerank   = "llm.metadata_rerank"
	headCapLLMDedupReview      = "llm.dedup_review"
	headCapLLMAuthorDedupBatch = "llm.author_dedup_batch"
	headCapLLMDiagnostics      = "llm.diagnostics"
	headCapEmbedText           = "embed.text"
	headCapEmbedTextBatch      = "embed.text_batch"
	headCapTranscribeBatch     = "transcribe.batch"
	headCapTranscribeIntroClip = "transcribe.intro_clip"

	// headLogRetentionZeroMigratedKey and headEmbedCoverArtMigratedKey are
	// the settings rows whose presence means the matching one-shot already ran.
	headLogRetentionZeroMigratedKey = "log_retention_zero_migrated"
	headEmbedCoverArtMigratedKey    = "embed_cover_art_default_on_migrated"
)

// headMigrationBaseline is aidispatch.MigrationBaseline, frozen: the only
// capabilities the ai_endpoints migration may tick.
var headMigrationBaseline = []string{
	headCapLLMFilenameParse,
	headCapLLMAudiobookParse,
	headCapLLMCoverArtVision,
	headCapLLMAuthorReview,
	headCapLLMAuthorDiscovery,
	headCapLLMMetadataRerank,
	headCapLLMDedupReview,
	headCapLLMAuthorDedupBatch,
	headCapLLMDiagnostics,
	headCapEmbedText,
	headCapEmbedTextBatch,
	headCapTranscribeBatch,
	headCapTranscribeIntroClip,
}

// headInMigrationBaseline is aidispatch.InMigrationBaseline over ids.
func headInMigrationBaseline(c string) bool {
	return slices.Contains(headMigrationBaseline, c)
}

// headAIEndpointsSeed carries what the ai_endpoints migration needs beyond
// the blob itself (HEAD: aiEndpointsSeed). HEAD's Base field, the live
// Snapshot the blob is unmarshalled onto, is gone: environment and config
// file are not inputs of the cutover read, so the base is always the zero
// headConfig and the migration starts from &headConfig{}. The two Env
// fields are likewise never set by headDecodeBlob (nil), because they come
// from the environment; they stay so the migration body is HEAD's.
type headAIEndpointsSeed struct {
	// HasOpenAIKey reports a stored or env-supplied OpenAI key.
	HasOpenAIKey bool
	// EnvWhisperEndpoints / EnvWhisperRemoteURL are non-nil when the env
	// sets them; always nil in the cutover read.
	EnvWhisperEndpoints *[]headWhisperEndpoint
	EnvWhisperRemoteURL *string
}

// headMigrateEmbeddingBlob rewrites a flat-format config blob to the nested EmbeddingConfig
// format. Returns the (possibly modified) blob and whether a migration occurred.
// Safe to call repeatedly: returns (blob, false) if already nested.
func headMigrateEmbeddingBlob(blob string) (string, bool) {
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

// headMigrateDedupBlob rewrites a flat-format config blob to the nested DedupConfig
// format. Returns the (possibly modified) blob and whether a migration occurred.
// Safe to call repeatedly: returns (blob, false) if already nested or no flat
// dedup keys are present.
func headMigrateDedupBlob(blob string) (string, bool) {
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

// headMigrateMetadataScoringBlob rewrites flat metadata_embedding_* fields to the
// nested MetadataScoringConfig format and drops the flat
// write_backup_before_tag_write key (that setting was removed 2026-10-10;
// create_backups is the one backup switch). Safe to call repeatedly.
// Returns (blob, false) if already nested or no flat keys present.
func headMigrateMetadataScoringBlob(blob string) (string, bool) {
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

// headMigrateAIBackendBlob derives the nested ai_backend object (backend-mode
// toggle) from the legacy flat/nested AI signal fields (openai_api_key,
// embedding.enabled/base_url/model, enable_ai_parsing, metadata_scoring.llm_enabled).
// It mirrors Config.EffectiveEmbeddingMode / Config.EffectiveLLMMode so the
// persisted modes match what those helpers would resolve at runtime. Safe to
// call repeatedly: returns (blob, false) once ai_backend is present, or when no
// legacy AI signal fields exist to derive from.
//
// Chained AFTER headMigrateMetadataScoringBlob so the embedding / metadata_scoring
// sub-objects are already in their nested shape when we read them here.
func headMigrateAIBackendBlob(blob string) (string, bool) {
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
	embeddingMode := headAIBackendModeDisabled
	switch {
	case !embEnabled:
		embeddingMode = headAIBackendModeDisabled
	case embBaseURL != "":
		embeddingMode = headAIBackendModeLocal
	case apiKey != "":
		embeddingMode = headAIBackendModeOpenAI
	}

	// Derive LLM mode (mirrors Config.EffectiveLLMMode).
	llmMode := headAIBackendModeDisabled
	if apiKey != "" && (enableAIParsing || llmEnabled) {
		llmMode = headAIBackendModeOpenAI
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

// headMigrateAIEndpointsBlob writes the ai_endpoints list derived from the legacy
// fields (PLAN section 5) into a blob that has none. Idempotent: a blob that
// already has ai_endpoints (even an empty list the operator saved) is
// returned unchanged. A null value, which a save from before this field
// existed cannot produce but a hand edit can, is treated as absent.
//
// The legacy fields are left in place and keep being written, and nothing
// reads ai_endpoints for dispatch in this PR.
func headMigrateAIEndpointsBlob(blob string, seed headAIEndpointsSeed) (string, bool) {
	// Every failure below is logged: a migration that silently never runs
	// leaves ai_endpoints empty with nothing in the boot log to say why.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &raw); err != nil || raw == nil {
		return blob, false
	}
	if v, ok := raw["ai_endpoints"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return blob, false
	}

	cfg := &headConfig{}
	if err := json.Unmarshal([]byte(blob), cfg); err != nil {
		return blob, false
	}
	if seed.EnvWhisperEndpoints != nil {
		cfg.WhisperEndpoints = *seed.EnvWhisperEndpoints
	}
	if seed.EnvWhisperRemoteURL != nil {
		cfg.WhisperRemoteURL = *seed.EnvWhisperRemoteURL
	}
	hasKey := seed.HasOpenAIKey || cfg.OpenAIAPIKey != ""
	if hasKey && cfg.OpenAIAPIKey == "" {
		// The effective-mode helpers look at the key field itself.
		cfg.OpenAIAPIKey = "present"
	}

	enc, err := json.Marshal(headBuildMigratedAIEndpoints(cfg, hasKey))
	if err != nil {
		return blob, false
	}
	raw["ai_endpoints"] = enc
	out, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(out), true
}

// headMigrateITunesBlob rewrites flat itunes_* / itl_* config fields to the nested
// ITunesConfig format. Returns the (possibly modified) blob and whether a migration
// occurred. Safe to call repeatedly: returns (blob, false) if already nested or no
// flat iTunes keys are present.
func headMigrateITunesBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	if _, isFlat := raw["itunes_sync_enabled"]; !isFlat {
		return blob, false
	}

	type flatShape struct {
		ITunesLibraryWritePath string `json:"itunes_library_write_path"`
		ITunesLibraryReadPath  string `json:"itunes_library_read_path"`
		ITunesPathTrimEnabled  bool   `json:"itunes_path_trim_enabled"`
		ITunesWindowsRootPath  string `json:"itunes_windows_root_path"`
		ITunesMediaRoot        string `json:"itunes_media_root"`
	}
	var old flatShape
	json.Unmarshal([]byte(blob), &old) //nolint:errcheck — already parsed above

	// itunes_sync_enabled / itunes_sync_interval are only the flat-shape
	// marker now: the sync they configured was removed on 2026-10-08, so
	// they are dropped rather than carried into the nested blob.
	raw["itunes"] = map[string]any{
		"library_write_path": old.ITunesLibraryWritePath,
		"library_read_path":  old.ITunesLibraryReadPath,
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

// headMigrateMaintenanceBlob rewrites flat maintenance_window_* fields to the nested
// MaintenanceConfig format. Safe to call repeatedly.
func headMigrateMaintenanceBlob(blob string) (string, bool) {
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

// headMigrateScheduledBlob rewrites flat scheduled_* fields to the nested
// ScheduledTasksConfig format. Safe to call repeatedly (idempotent).
// Sentinel key: "scheduled_dedup_refresh_enabled".
func headMigrateScheduledBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	if _, isFlat := raw["scheduled_dedup_refresh_enabled"]; !isFlat {
		return blob, false
	}
	raw = headRemapScheduledKeys(raw)
	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}

// headRemapScheduledKeys translates flat scheduled_* keys in a config map to the
// nested ScheduledTasksConfig format. Merges into any existing "scheduled"
// sub-object to avoid zeroing sibling fields.
func headRemapScheduledKeys(payload map[string]any) map[string]any {
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
			maps.Copy(existingGroup, fields)
		} else {
			existing[group] = fields
		}
	}
	payload["scheduled"] = existing
	return payload
}

// headMigrateAutoUpdateBlob rewrites flat auto_update_* fields to the nested AutoUpdateConfig format.
func headMigrateAutoUpdateBlob(blob string) (string, bool) {
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

// headMigrateLogRetentionBlob drops a stored "log_retention_days": 0 from the
// config blob, so the key is absent and the startup value (the default 90, or
// a config-file/environment value) applies. Run exactly once per install; the
// caller gates it on headLogRetentionZeroMigratedKey and persists the result.
//
// A stored 0 is almost certainly not an operator's choice to keep logs
// forever: before 2026-10-11 InitConfig never read the key, so
// AppConfig.LogRetentionDays was 0 on every boot and every settings save
// serialised that 0 into the blob, and no UI ever wrote the key. It cannot be
// ruled out, though: from 2026-08-22 (1f66acfc7) the scheduler admin API's
// purge_old_logs hint told operators "set that key via PUT /config, 0 disables
// the purge", so an operator could have PUT a deliberate 0, which looks
// exactly like the accidental one. The owner-approved intent is that logs
// purge at the default window, so the 0 is dropped anyway, and the caller logs
// it at Warn saying what 0 used to mean and that -1 now keeps logs forever.
// A stored non-zero value, and an absent key, are left alone.
func headMigrateLogRetentionBlob(blob string) (string, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	v, ok := raw["log_retention_days"]
	if !ok || strings.TrimSpace(string(v)) != "0" {
		return blob, false
	}
	delete(raw, "log_retention_days")
	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}

// headMigrateEmbedCoverArtBlob drops a stored "embed_cover_art": false so the new
// default (true) applies. Run it exactly once per install (the caller gates it
// on headEmbedCoverArtMigratedKey).
//
// No stored false can be an operator choice: before 2026-10-09 (01-P79a) the
// embed_cover_art key did nothing, the cover embed ran unconditionally, and
// every settings save serialised the old default (false) into the blob. A
// stored true is an explicit choice and is left alone, as is an absent key.
func headMigrateEmbedCoverArtBlob(blob string) (string, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	v, ok := raw["embed_cover_art"]
	if !ok || strings.TrimSpace(string(v)) != "false" {
		return blob, false
	}
	delete(raw, "embed_cover_art")
	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}

// headCheckConfigBlob reports whether a stored config_blob value is a JSON
// object. Empty, "null", a non-object, and a syntax error are all rejected:
// json.Unmarshal of "null" into a struct succeeds and changes nothing, so it
// would otherwise load as an all-defaults config. No writer stores an empty
// blob (SaveConfigToDatabase and saveRawBlob both write marshalled JSON).
func headCheckConfigBlob(value string) error {
	if value == "" {
		return fmt.Errorf("value is empty")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &obj); err != nil {
		return fmt.Errorf("not a JSON object: %w", err)
	}
	if obj == nil {
		return fmt.Errorf("value is JSON null")
	}
	return nil
}

// headEffectiveEmbeddingMode resolves the embedding backend mode. When
// AIBackend.EmbeddingMode is set explicitly it wins. Otherwise the mode is
// derived from the legacy flat fields, mirroring the pre-toggle behavior of the
// embedclient registration:
//
//   - Embedding.Enabled == false -> disabled (master off-switch preserved).
//   - Embedding.BaseURL != ""     -> local  (a local endpoint is configured).
//   - OpenAIAPIKey != ""          -> openai.
//   - otherwise                   -> disabled.
//
// This method is pure: it reads config and returns a string, mutating nothing,
// so it is safe to call from concurrent readers (e.g. the dedup engine reading
// the global AppConfig).
func headEffectiveEmbeddingMode(c *headConfig) string {
	if c.AIBackend.EmbeddingMode != "" {
		return c.AIBackend.EmbeddingMode
	}
	if !c.Embedding.Enabled {
		return headAIBackendModeDisabled
	}
	if c.Embedding.BaseURL != "" {
		return headAIBackendModeLocal
	}
	if c.OpenAIAPIKey != "" {
		return headAIBackendModeOpenAI
	}
	return headAIBackendModeDisabled
}

// headEffectiveLLMMode resolves the LLM/chat backend mode. When AIBackend.LLMMode is
// set explicitly it wins. Otherwise: openai when a key is configured and any LLM
// consumer is enabled (EnableAIParsing or MetadataScoring.LLMEnabled), else
// disabled. There is no legacy flat field for a local LLM endpoint, so local LLM
// use must be selected explicitly via AIBackend.LLMMode. Pure (see
// EffectiveEmbeddingMode).
func headEffectiveLLMMode(c *headConfig) string {
	if c.AIBackend.LLMMode != "" {
		return c.AIBackend.LLMMode
	}
	// A blank llm_mode used to resolve straight to OpenAI whenever an API key
	// happened to be present. That made an empty config field silently choose a
	// paid external service, and nothing logged the choice — on 2026-08-16 it
	// ran a whole library scan against OpenAI until the account hit
	// credit_balance_exhausted, whereupon 77 consecutive batches failed and the
	// stuck-op watchdog killed the scan.
	//
	// Prefer the local backend when one is configured. Falling back to OpenAI is
	// still possible, but only when there is no local option at all.
	if c.AIBackend.LocalBaseURL != "" || c.Embedding.BaseURL != "" {
		return headAIBackendModeLocal
	}
	// DERIVATION NEVER YIELDS A PAID BACKEND.
	//
	// This used to return headAIBackendModeOpenAI whenever a key was present and
	// any LLM consumer was enabled (enable_ai_parsing defaults to TRUE), which
	// is the exact shape of the incident described above. It survived that
	// incident only because ai_backend.local_base_url shipped a hardcoded LAN
	// address, so the local branch above always won and this branch was
	// effectively unreachable on a default install. TASK-018 removed that
	// address -- correctly, it was one developer's own host -- which would
	// have re-armed this branch for every install that has a key and no local
	// endpoint.
	//
	// Opting in to a paid backend is now explicit only: set
	// ai_backend.llm_mode = "openai", which returns from the top of this
	// function before any derivation runs. A bare API key is not consent to
	// spend money.
	return headAIBackendModeDisabled
}

// tick adds c to row's capabilities if the migration is allowed to tick it.
// The migration may tick ONLY capabilities in aidispatch.MigrationBaseline; a
// capability registered later starts with no servers until an operator ticks
// it. This helper is the single enforcement point for that rule.
func headTick(row *headAIEndpoint, c string) {
	if !headInMigrationBaseline(c) {
		return
	}
	if !slices.Contains(row.Capabilities, c) {
		row.Capabilities = append(row.Capabilities, c)
	}
}

// headBuildMigratedAIEndpoints derives the rows from resolved legacy config so
// that, for every call site in PLAN section 1, selection over the rows picks
// the endpoint the site uses today. Known divergences are listed in the PR 2
// description and in ai_endpoints_migration_test.go. Never returns nil, so
// the migrated blob holds [] rather than null and the migration cannot re-run.
func headBuildMigratedAIEndpoints(cfg *headConfig, hasOpenAIKey bool) []headAIEndpoint {
	rows := []headAIEndpoint{}
	llmMode := headEffectiveLLMMode(cfg)
	embedMode := headEffectiveEmbeddingMode(cfg)
	llmLocal := llmMode == headAIBackendModeLocal || llmMode == headAIBackendModeOpenAIFallbackLocal
	llmCloud := llmMode == headAIBackendModeOpenAI || llmMode == headAIBackendModeOpenAIFallbackLocal
	embedLocal := embedMode == headAIBackendModeLocal || embedMode == headAIBackendModeOpenAIFallbackLocal
	embedCloud := embedMode == headAIBackendModeOpenAI || embedMode == headAIBackendModeOpenAIFallbackLocal

	// One local OpenAI-compatible row: chat and embeddings share the base URL
	// today (LocalBaseURL, falling back to Embedding.BaseURL).
	localURL := cfg.AIBackend.LocalBaseURL
	if localURL == "" {
		localURL = cfg.Embedding.BaseURL
	}
	if localURL != "" {
		embedModel := cfg.AIBackend.LocalEmbeddingModel
		if embedModel == "" {
			embedModel = cfg.Embedding.Model
		}
		row := headAIEndpoint{
			ID:         headAIEndpointIDLocalLLM,
			Label:      "Local LLM (migrated from ai_backend.local_base_url)",
			Protocol:   headProtocolOpenAICompat,
			URL:        localURL,
			ChatModel:  cfg.AIBackend.LocalLLMModel,
			EmbedModel: embedModel,
			Priority:   10,
			// Not enforced until the call sites move onto the dispatcher.
			Concurrency:  4,
			Enabled:      true,
			Capabilities: []string{},
		}
		if llmLocal {
			// The chat sites that honour llm_mode today. Batch-API work
			// (dedup_review, author_dedup_batch) is never ticked here: it
			// requires the batch_api feature, which Ollama does not have.
			headTick(&row, headCapLLMFilenameParse)
			headTick(&row, headCapLLMAuthorReview)
			headTick(&row, headCapLLMAuthorDiscovery)
			headTick(&row, headCapLLMMetadataRerank)
		}
		if embedLocal {
			headTick(&row, headCapEmbedText)
		}
		rows = append(rows, row)
	}

	if hasOpenAIKey {
		base := cfg.OpenAIBaseURL
		if base == "" {
			base = headDefaultOpenAIBaseURL
		}
		prio := 20
		if llmMode == headAIBackendModeOpenAIFallbackLocal {
			prio = 5 // cloud first, local second
		}
		models := map[string]string{}
		for id, m := range map[string]string{
			headCapLLMFilenameParse:  cfg.FilenameParseModel,
			headCapLLMAudiobookParse: cfg.FilenameParseModel,
			headCapLLMCoverArtVision: cfg.CoverArtModel,
			headCapLLMMetadataRerank: cfg.MetadataReviewModel,
			headCapLLMDedupReview:    cfg.Dedup.ReviewModel,
		} {
			if m != "" {
				models[id] = m
			}
		}
		if len(models) == 0 {
			models = nil
		}
		row := headAIEndpoint{
			ID:               headAIEndpointIDOpenAI,
			Label:            "OpenAI (migrated from openai_api_key)",
			Protocol:         headProtocolOpenAICompat,
			URL:              base,
			ChatModel:        headDefaultOpenAIChatModel,
			EmbedModel:       cfg.Embedding.Model,
			CapabilityModels: models,
			Priority:         prio,
			Concurrency:      4,
			Enabled:          true,
			Capabilities:     []string{},
			Features:         []string{headFeatureBatchAPI, headFeatureVision},
			AuthRef:          "openai_api_key",
		}
		// Owner decision 1: pre-tick the sites that call OpenAI directly
		// whenever a key exists, regardless of llm_mode.
		headTick(&row, headCapLLMFilenameParse)
		headTick(&row, headCapLLMAudiobookParse)
		// Owner decision 5 ("vision is cloud only") was REVERSED on
		// 2026-09-26: vision runs on the local LLM pool rows, which an
		// operator ticks for llm.cover_art_vision together with the "vision"
		// feature and a capability_models entry. The local row above is not
		// ticked here because the migration cannot know its model can see.
		// This cloud tick is kept so an install that already relied on it
		// keeps working; with a higher priority number than the local rows it
		// is only a fallback, and unticking it makes vision local-only.
		headTick(&row, headCapLLMCoverArtVision)
		headTick(&row, headCapLLMAuthorReview)
		headTick(&row, headCapLLMAuthorDiscovery)
		headTick(&row, headCapLLMAuthorDedupBatch)
		headTick(&row, headCapLLMDiagnostics)
		// The intro-clip fallback is whisper-1 over OpenAI today. The
		// dispatcher refuses it on openai_compat until PR 3 adds the protocol
		// mapping; the tick records the intent.
		headTick(&row, headCapTranscribeIntroClip)
		if llmCloud {
			headTick(&row, headCapLLMMetadataRerank)
			headTick(&row, headCapLLMDedupReview)
		}
		if embedCloud {
			headTick(&row, headCapEmbedText)
		}
		if embedMode == headAIBackendModeOpenAI {
			headTick(&row, headCapEmbedTextBatch)
		}
		rows = append(rows, row)
	}

	// Whisper servers, with poolEndpoints' precedence: a non-empty
	// whisper_endpoints list, else whisper_remote_url as one server.
	whisper := make([]headAIEndpoint, 0, len(cfg.WhisperEndpoints))
	seen := map[string]bool{}
	for _, w := range cfg.WhisperEndpoints {
		if w.URL == "" || seen[w.URL] {
			continue
		}
		seen[w.URL] = true
		row := headAIEndpoint{
			Label:        w.Label,
			Protocol:     headProtocolWhisperServer,
			URL:          w.URL,
			Priority:     w.Priority,
			Concurrency:  max(w.Concurrency, 1),
			Enabled:      true,
			Capabilities: []string{},
			Labels:       slices.Clone(w.Capabilities),
			RequireGPU:   w.RequireGPU,
		}
		headTick(&row, headCapTranscribeBatch)
		whisper = append(whisper, row)
	}
	if len(whisper) == 0 && cfg.WhisperRemoteURL != "" {
		row := headAIEndpoint{
			Label:        "Whisper server (migrated from whisper_remote_url)",
			Protocol:     headProtocolWhisperServer,
			URL:          cfg.WhisperRemoteURL,
			Concurrency:  1,
			Enabled:      true,
			Capabilities: []string{},
		}
		headTick(&row, headCapTranscribeBatch)
		whisper = append(whisper, row)
	}
	headAssignWhisperIDs(whisper)
	rows = append(rows, whisper...)

	// The in-process uv runner: today's intro-clip path when uv is on PATH,
	// and the batch path only when no whisper server is configured.
	uv := headAIEndpoint{
		ID:           headAIEndpointIDLocalUV,
		Label:        "Local uv/openai-whisper",
		Protocol:     headProtocolLocalProcess,
		URL:          headLocalUVEndpointURL,
		Priority:     100,
		Concurrency:  1,
		Enabled:      true,
		Capabilities: []string{},
	}
	headTick(&uv, headCapTranscribeIntroClip)
	if len(whisper) == 0 {
		headTick(&uv, headCapTranscribeBatch)
	}
	rows = append(rows, uv)
	return rows
}

// headAssignWhisperIDs names each row whisper-<port>, or whisper-<n> (1-based
// position) when the URL has no port or two rows share one.
func headAssignWhisperIDs(rows []headAIEndpoint) {
	ports := map[string]int{}
	portOf := make([]string, len(rows))
	for i, r := range rows {
		if u, err := url.Parse(r.URL); err == nil {
			portOf[i] = u.Port()
		}
		if portOf[i] != "" {
			ports[portOf[i]]++
		}
	}
	for i := range rows {
		if p := portOf[i]; p != "" && ports[p] == 1 {
			rows[i].ID = "whisper-" + p
		} else {
			rows[i].ID = "whisper-" + strconv.Itoa(i+1)
		}
	}
}

// headSeed is the AI-endpoints transform's only blob-external input.
type headSeed struct{ HasOpenAIKey bool }

// headMarkers says which one-shot marker rows exist. true means the marker
// row exists, so the one-shot is SKIPPED (HEAD gates on the settings rows
// "log_retention_zero_migrated" and "embed_cover_art_default_on_migrated").
// The two are the only marker-guarded blob one-shots in HEAD's
// LoadConfigFromDatabase; MigrateMaintenanceWindow's
// "maintenance_window_migrated" marker guards a Config-level rewrite after
// the blob is decoded, not a blob transform, and is not part of this read.
type headMarkers struct{ LogRetentionZero, EmbedCoverArt bool }

// headBlob is a stored config_blob after HEAD's read transforms.
type headBlob struct {
	// Config is the transformed blob decoded onto a ZERO headConfig with
	// encoding/json (HEAD decodes onto Snapshot(); absent fields stage
	// nothing here).
	Config headConfig
	// Fields holds the transformed blob's top-level JSON members, what
	// Config was decoded from.
	Fields map[string]json.RawMessage
}

// headDecodeBlob replays HEAD's LoadConfigFromDatabase blob path in HEAD's
// order: checkConfigBlob; the nine chained transforms (embedding, dedup,
// metadata_scoring, ai_backend, ai_endpoints, itunes, maintenance,
// scheduled, auto_update); the type probe; the marker-guarded log_retention
// then embed_cover_art one-shots; the final decode. It writes nothing and
// logs nothing. Errors carry only what encoding/json says about types, never
// the blob.
func headDecodeBlob(raw []byte, seed headSeed, markers headMarkers) (headBlob, error) {
	blobStr := string(raw)
	if err := headCheckConfigBlob(blobStr); err != nil {
		return headBlob{}, fmt.Errorf("stored config_blob is unreadable: %w", err)
	}

	if migrated, changed := headMigrateEmbeddingBlob(blobStr); changed {
		blobStr = migrated
	}
	if migrated, changed := headMigrateDedupBlob(blobStr); changed {
		blobStr = migrated
	}
	if migrated, changed := headMigrateMetadataScoringBlob(blobStr); changed {
		blobStr = migrated
	}
	if migrated, changed := headMigrateAIBackendBlob(blobStr); changed {
		blobStr = migrated
	}
	if migrated, changed := headMigrateAIEndpointsBlob(blobStr, headAIEndpointsSeed{HasOpenAIKey: seed.HasOpenAIKey}); changed {
		blobStr = migrated
	}
	if migrated, changed := headMigrateITunesBlob(blobStr); changed {
		blobStr = migrated
	}
	if migrated, changed := headMigrateMaintenanceBlob(blobStr); changed {
		blobStr = migrated
	}
	if migrated, changed := headMigrateScheduledBlob(blobStr); changed {
		blobStr = migrated
	}
	if migrated, changed := headMigrateAutoUpdateBlob(blobStr); changed {
		blobStr = migrated
	}

	var probe headConfig
	if err := json.Unmarshal([]byte(blobStr), &probe); err != nil {
		return headBlob{}, fmt.Errorf("stored config_blob does not match the config schema: %w", err)
	}

	if !markers.LogRetentionZero {
		if migrated, changed := headMigrateLogRetentionBlob(blobStr); changed {
			blobStr = migrated
		}
	}
	if !markers.EmbedCoverArt {
		if migrated, changed := headMigrateEmbedCoverArtBlob(blobStr); changed {
			blobStr = migrated
		}
	}

	var out headBlob
	if err := json.Unmarshal([]byte(blobStr), &out.Config); err != nil {
		return headBlob{}, fmt.Errorf("stored config_blob does not match the config schema: %w", err)
	}
	if err := json.Unmarshal([]byte(blobStr), &out.Fields); err != nil {
		return headBlob{}, fmt.Errorf("stored config_blob is unreadable: %w", err)
	}
	return out, nil
}
