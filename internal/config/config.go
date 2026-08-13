// file: internal/config/config.go
// version: 1.76.0
// guid: 7b8c9d0e-1f2a-3b4c-5d6e-7f8a9b0c1d2e
// last-edited: 2026-08-12

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/tools"
	"github.com/spf13/viper"
)

// ITunesPathMap defines a bidirectional path prefix mapping between iTunes and local paths.
// From is the iTunes prefix (e.g. "file://localhost/W:/itunes/iTunes%20Media"),
// To is the local prefix (e.g. "file://localhost/mnt/bigdata/books/itunes/iTunes Media").
type ITunesPathMap struct {
	From string `json:"from"` // iTunes path prefix
	To   string `json:"to"`   // Local path prefix
}

// MetadataSource represents a metadata provider configuration
type MetadataSource struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Enabled      bool              `json:"enabled"`
	Priority     int               `json:"priority"`
	RequiresAuth bool              `json:"requires_auth"`
	Credentials  map[string]string `json:"credentials"`
}

// DownloadClientConfig represents download client connection settings.
type DownloadClientConfig struct {
	Torrent TorrentClientConfig `json:"torrent"`
	Usenet  UsenetClientConfig  `json:"usenet"`
}

// TorrentClientConfig holds torrent client configuration.
type TorrentClientConfig struct {
	Type        string            `json:"type"`
	Deluge      DelugeConfig      `json:"deluge"`
	QBittorrent QBittorrentConfig `json:"qbittorrent"`
}

// UsenetClientConfig holds Usenet client configuration.
type UsenetClientConfig struct {
	Type    string        `json:"type"`
	SABnzbd SABnzbdConfig `json:"sabnzbd"`
}

// DelugeConfig holds Deluge RPC configuration.
type DelugeConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// QBittorrentConfig holds qBittorrent Web API configuration.
type QBittorrentConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	UseHTTPS bool   `json:"use_https"`
}

// SABnzbdConfig holds SABnzbd API configuration.
type SABnzbdConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	APIKey   string `json:"api_key"`
	UseHTTPS bool   `json:"use_https"`
}

// PluginConfig holds per-plugin configuration.
type PluginConfig struct {
	Enabled  bool              `json:"enabled"`
	Settings map[string]string `json:"settings"` // plugin-specific key-value pairs
}

// EmbeddingConfig holds all settings for the local/remote embedding pipeline.
type EmbeddingConfig struct {
	Enabled       bool   `json:"enabled"        mapstructure:"enabled"`
	Model         string `json:"model"          mapstructure:"model"`
	Dimensions    int    `json:"dimensions"     mapstructure:"dimensions"`
	BaseURL       string `json:"base_url"       mapstructure:"base_url"`
	VectorBackend string `json:"vector_backend" mapstructure:"vector_backend"`
}

// DedupSignalConfig holds the band-threshold values for the unified scoring
// system (SPEC 1 §3). Persisting these in the config blob means they survive
// across restarts without needing to fall back to Viper every time.
type DedupSignalConfig struct {
	// BandCertainMin is the minimum score to classify a pair as CERTAIN (default 97).
	BandCertainMin float64 `json:"band_certain_min" mapstructure:"band_certain_min"`
	// BandHighMin is the minimum score to classify a pair as HIGH (default 90).
	BandHighMin float64 `json:"band_high_min" mapstructure:"band_high_min"`
	// BandMediumMin is the minimum score to classify a pair as MEDIUM (default 75).
	BandMediumMin float64 `json:"band_medium_min" mapstructure:"band_medium_min"`
	// BandReviewMin is the minimum score to classify a pair as REVIEW (default 60).
	// Pairs scoring below this floor are not persisted.
	BandReviewMin float64 `json:"band_review_min" mapstructure:"band_review_min"`
	// Confidence holds per-signal-kind confidence-bound overrides, keyed by
	// signal kind (e.g. "exact_file", "isbn_asin", "embedding_high" — see
	// unified.SignalKind for the full set). A kind absent from this map —
	// including a nil/unset map, the zero value — falls back to
	// unified.DefaultScoreConfig()'s compiled-in Min/MaxConfidence for that
	// kind, so existing configs are byte-for-byte unchanged in behaviour.
	//
	// INIT-1 T05 follow-up (TODO item 6): this is the persistence surface
	// dedup.calibrate-composite's Round 2 advisory confidence sweep was
	// missing — see internal/plugins/dedup/calibrate_composite.go's "Two
	// calibration surfaces" doc comment. Adding this field lets a per-kind
	// confidence bound survive a config update / restart via the normal
	// UpdateConfig JSON round-trip instead of being silently dropped.
	//
	// NOTE: as of this change, populating this map has NO effect on live
	// scoring — unified.ComposeScore reads each Signal's Confidence directly
	// and does not clamp it against these bounds (see
	// docs/plans/DECISIONS-PENDING.md row 10 for the open decision on
	// whether ComposeScore should start doing so). This field is currently
	// consumed only by unified.LoadScoreConfig (so it round-trips correctly)
	// and is available for a human-reviewed follow-up to wire into scoring.
	Confidence map[string]DedupKindConfidence `json:"confidence,omitempty" mapstructure:"confidence"`
}

// DedupKindConfidence is a per-signal-kind confidence-bound override, the
// value type of DedupSignalConfig.Confidence. Both fields are optional —
// zero means "not set", falling back to the unified package's compiled-in
// default for that bound — because those are the two bounds
// dedup.calibrate-composite's Round 2 sweep (the "confidence round")
// recommends adjusting.
type DedupKindConfidence struct {
	// MinConfidence overrides the kind's confidence floor. Zero = not set.
	MinConfidence float64 `json:"min_confidence,omitempty" mapstructure:"min_confidence"`
	// MaxConfidence overrides the kind's confidence ceiling. Zero = not set.
	MaxConfidence float64 `json:"max_confidence,omitempty" mapstructure:"max_confidence"`
}

// DedupConfig holds all deduplication settings that were previously flat fields
// in Config. Nesting these here keeps the config blob organised and lets the
// API shim (remapDedupKeys) translate legacy PUT payloads transparently.
type DedupConfig struct {
	// BookHighThreshold is the cosine-similarity floor for "high confidence" book pairs (default 0.95).
	BookHighThreshold float64 `json:"book_high_threshold" mapstructure:"book_high_threshold"`
	// BookLowThreshold is the cosine-similarity floor for "low confidence" book pairs (default 0.85).
	BookLowThreshold float64 `json:"book_low_threshold" mapstructure:"book_low_threshold"`
	// AuthorHighThreshold is the cosine-similarity floor for "high confidence" author pairs (default 0.92).
	AuthorHighThreshold float64 `json:"author_high_threshold" mapstructure:"author_high_threshold"`
	// AuthorLowThreshold is the cosine-similarity floor for "low confidence" author pairs (default 0.80).
	AuthorLowThreshold float64 `json:"author_low_threshold" mapstructure:"author_low_threshold"`
	// AutoMergeEnabled controls whether high-confidence pairs are merged automatically (default true).
	AutoMergeEnabled bool `json:"auto_merge_enabled" mapstructure:"auto_merge_enabled"`
	// EmbeddingsEnabled controls whether the embedding layer (Layer 2) is active (default true).
	// Set false on air-gapped / no-internet boxes to skip all embedding API calls.
	EmbeddingsEnabled bool `json:"embeddings_enabled" mapstructure:"embeddings_enabled"`
	// LLMAutoMergeHighConfidence, when true, auto-applies merges when the LLM
	// review returns a "duplicate" verdict with confidence "high" (default false — opt-in).
	LLMAutoMergeHighConfidence bool `json:"llm_auto_merge_high_confidence" mapstructure:"llm_auto_merge_high_confidence"`
	// AutoResolveEnabled is the global kill switch for the dedup.auto-resolve
	// Tier-1 (Band CERTAIN) auto-merge op. When false (the default), an
	// apply=true auto-resolve run is refused with an error and performs zero
	// merges; the dry-run report is always producible regardless of this flag.
	// Flipping this to true is an owner-greenlight action taken out-of-band
	// after reviewing a dry-run report — it is never defaulted true.
	AutoResolveEnabled bool `json:"auto_resolve_enabled" mapstructure:"auto_resolve_enabled"`
	// OnImportViaScheduler routes the post-import dedup check through the UOS
	// dependency scheduler instead of an eager goroutine (default false — opt-in).
	OnImportViaScheduler bool `json:"on_import_via_scheduler" mapstructure:"on_import_via_scheduler"`
	// ReviewModel is the OpenAI model used for LLM-layer dedup review (default "gpt-5-mini").
	ReviewModel string `json:"review_model" mapstructure:"review_model"`
	// Signals holds the unified scoring band thresholds.
	Signals DedupSignalConfig `json:"signals" mapstructure:"signals"`
	// EmbeddingThresholdsByModel holds per-embedding-model overrides for the
	// book high/low cosine thresholds, keyed by the producing model name
	// (e.g. "text-embedding-3-large" or "bge-m3"). DEDUP-2/3: the flat
	// BookHighThreshold/BookLowThreshold above were calibrated for OpenAI
	// text-embedding-3-large's cosine distribution; a different embedding model
	// (bge-m3) has a different distribution and needs its own thresholds.
	// Any model NOT present in this map falls back to the flat
	// BookHighThreshold/BookLowThreshold — so behaviour is byte-for-byte
	// unchanged for the legacy model and any not-yet-calibrated model. Populate
	// via the dedup.calibrate-embedding-thresholds report (owner-reviewed).
	EmbeddingThresholdsByModel map[string]EmbeddingModelThresholds `json:"embedding_thresholds_by_model,omitempty" mapstructure:"embedding_thresholds_by_model"`
}

// EmbeddingModelThresholds is a per-model override of the book cosine-similarity
// high/low thresholds. Used as the value type of
// DedupConfig.EmbeddingThresholdsByModel.
type EmbeddingModelThresholds struct {
	// High is the cosine-similarity floor for the "high confidence" band.
	High float64 `json:"high" mapstructure:"high"`
	// Low is the cosine-similarity floor for the "low confidence" band.
	Low float64 `json:"low" mapstructure:"low"`
}

// ThresholdsForModel resolves the book high/low cosine thresholds for the given
// embedding model name. If the model has a calibrated entry in
// EmbeddingThresholdsByModel, that entry is returned; otherwise the flat
// BookHighThreshold/BookLowThreshold values are returned as the default
// fallback. This guarantees zero behaviour change for any model not explicitly
// calibrated (DEDUP-2/3).
func (c DedupConfig) ThresholdsForModel(model string) (high, low float64) {
	if t, ok := c.EmbeddingThresholdsByModel[model]; ok {
		return t.High, t.Low
	}
	return c.BookHighThreshold, c.BookLowThreshold
}

// DedupBoilerplateConfig holds extension-only additions to the compiled-in
// publisher-boilerplate title blocklist (internal/dedup/boilerplate.go,
// INIT-4 T5). Config entries are ALWAYS additive to the built-in
// Audible/publisher patterns — there is intentionally no replace/override
// field: that would let a misconfigured deployment silently drop the entire
// compiled-in list and re-open the dedup false-positive bug the list exists
// to prevent (spec docs/specs/2026-07-10-filtering-search-design.md Decision
// 8). Empty (the default) is byte-identical to the pre-config hardcoded
// behavior.
type DedupBoilerplateConfig struct {
	// ExtraTitlePatterns are additional exact-match boilerplate titles,
	// appended to (never replacing) the compiled-in defaults.
	ExtraTitlePatterns []string `json:"extra_title_patterns" mapstructure:"extra_title_patterns"`
	// ExtraPrefixPatterns are additional anchored-prefix boilerplate
	// patterns, appended to (never replacing) the compiled-in defaults.
	ExtraPrefixPatterns []string `json:"extra_prefix_patterns" mapstructure:"extra_prefix_patterns"`
}

// ITunesConfig holds all settings for the iTunes sync and write-back subsystem.
type ITunesConfig struct {
	SyncEnabled      bool            `json:"sync_enabled"       mapstructure:"sync_enabled"`
	SyncInterval     int             `json:"sync_interval"      mapstructure:"sync_interval"`
	WriteBackEnabled bool            `json:"write_back_enabled" mapstructure:"write_back_enabled"`
	LibraryWritePath string          `json:"library_write_path" mapstructure:"library_write_path"`
	LibraryReadPath  string          `json:"library_read_path"  mapstructure:"library_read_path"`
	AutoWriteBack    bool            `json:"auto_write_back"    mapstructure:"auto_write_back"`
	PathTrimEnabled  bool            `json:"path_trim_enabled"  mapstructure:"path_trim_enabled"`
	WindowsRootPath  string          `json:"windows_root_path"  mapstructure:"windows_root_path"`
	MediaRoot        string          `json:"media_root"         mapstructure:"media_root"`
	PathMappings     []ITunesPathMap `json:"path_mappings"      mapstructure:"path_mappings"`

	// Libraries is the explicit 4-state library model (Original/AO x .itl/.xml)
	// plus the PointedAt/ImportSource mode facts. Inert until populated: when empty,
	// the legacy LibraryReadPath/LibraryWritePath fields are used as-is. See
	// itunes_libraries.go and docs/specs/2026-07-23-itunes-2way-sync-system-design.md.
	Libraries LibrarySet `json:"libraries" mapstructure:"libraries"`
}

// MaintenanceConfig holds settings for the nightly maintenance window.
type MaintenanceConfig struct {
	Enabled              bool `json:"enabled"                mapstructure:"enabled"`
	WindowStart          int  `json:"window_start"           mapstructure:"window_start"`
	WindowEnd            int  `json:"window_end"             mapstructure:"window_end"`
	DedupRefresh         bool `json:"dedup_refresh"          mapstructure:"dedup_refresh"`
	SeriesPrune          bool `json:"series_prune"           mapstructure:"series_prune"`
	AuthorSplit          bool `json:"author_split"           mapstructure:"author_split"`
	TombstoneCleanup     bool `json:"tombstone_cleanup"      mapstructure:"tombstone_cleanup"`
	Reconcile            bool `json:"reconcile"              mapstructure:"reconcile"`
	PurgeDeleted         bool `json:"purge_deleted"          mapstructure:"purge_deleted"`
	PurgeOldLogs         bool `json:"purge_old_logs"         mapstructure:"purge_old_logs"`
	DbOptimize           bool `json:"db_optimize"            mapstructure:"db_optimize"`
	LibraryScan          bool `json:"library_scan"           mapstructure:"library_scan"`
	LibraryOrganize      bool `json:"library_organize"       mapstructure:"library_organize"`
	MetadataRefresh      bool `json:"metadata_refresh"       mapstructure:"metadata_refresh"`
	LibrarySizeRefresh   bool `json:"library_size_refresh"   mapstructure:"library_size_refresh"`
	AcoustIDOnlineLookup bool `json:"acoustid_online_lookup" mapstructure:"acoustid_online_lookup"`
	AcoustIDNightlyLimit int  `json:"acoustid_nightly_limit" mapstructure:"acoustid_nightly_limit"`
	// AcoustIDBackfill gates the nightly acoustid.backfill op (local fpcalc/
	// ffmpeg fingerprint generation). OFF by default — see the SetDefault
	// comment for why.
	AcoustIDBackfill bool `json:"acoustid_backfill" mapstructure:"acoustid_backfill"`
}

// MetadataScoringConfig holds settings for the AI-assisted metadata scoring pipeline.
type MetadataScoringConfig struct {
	EmbeddingEnabled   bool    `json:"embedding_enabled"    mapstructure:"embedding_enabled"`
	EmbeddingMinScore  float64 `json:"embedding_min_score"  mapstructure:"embedding_min_score"`
	EmbeddingBestMatch float64 `json:"embedding_best_match" mapstructure:"embedding_best_match"`
	LLMEnabled         bool    `json:"llm_enabled"          mapstructure:"llm_enabled"`
	LLMRerankEpsilon   float64 `json:"llm_rerank_epsilon"   mapstructure:"llm_rerank_epsilon"`
	LLMRerankTopK      int     `json:"llm_rerank_top_k"     mapstructure:"llm_rerank_top_k"`
	WriteBackupBefore  bool    `json:"write_backup_before"  mapstructure:"write_backup_before"`

	// --- new: transcription boosts (defaults 2.0 / 1.4 / 1.6 / 1.4) ---
	TranscriptionTitleExactBoost  float64 `json:"transcription_title_exact_boost"  mapstructure:"transcription_title_exact_boost"`
	TranscriptionTitleSubstrBoost float64 `json:"transcription_title_substr_boost" mapstructure:"transcription_title_substr_boost"`
	TranscriptionAuthorBoost      float64 `json:"transcription_author_boost"       mapstructure:"transcription_author_boost"`
	TranscriptionNarratorBoost    float64 `json:"transcription_narrator_boost"     mapstructure:"transcription_narrator_boost"`

	// --- new: base-score adjustments (defaults 0.15 / 0.05 / 0.15 / 0.35).
	// POINTER knobs: 0 is a legitimate operator value for CompilationPenalty,
	// RichMetadataBonusCap, and F1MinScore, so "unset" is nil, NOT 0. ---
	CompilationPenalty     *float64 `json:"compilation_penalty"       mapstructure:"compilation_penalty"`
	RichMetadataFieldBonus float64  `json:"rich_metadata_field_bonus" mapstructure:"rich_metadata_field_bonus"`
	RichMetadataBonusCap   *float64 `json:"rich_metadata_bonus_cap"   mapstructure:"rich_metadata_bonus_cap"`
	F1MinScore             *float64 `json:"f1_min_score"              mapstructure:"f1_min_score"`

	// --- new: series boosts (defaults 1.4 / 2.0 / 0.5) ---
	SeriesNameMatchBoost     float64 `json:"series_name_match_boost"     mapstructure:"series_name_match_boost"`
	SeriesNumberExactBoost   float64 `json:"series_number_exact_boost"   mapstructure:"series_number_exact_boost"`
	SeriesNumberWrongPenalty float64 `json:"series_number_wrong_penalty" mapstructure:"series_number_wrong_penalty"`

	// --- new: duration tier VALUES (defaults = the multiplier/score columns of
	// the durationTiers table in internal/metafetch/service_scoring.go). Tier
	// STRUCTURE (edges + count) stays fixed in code. ---
	DurationTierMultipliers []float64 `json:"duration_tier_multipliers" mapstructure:"duration_tier_multipliers"`
	DurationTierScores      []float64 `json:"duration_tier_scores"      mapstructure:"duration_tier_scores"`

	// --- new: bulk-fetch concurrency (default 4; consumed by TASK-05) ---
	BulkFetchWorkers int `json:"bulk_fetch_workers" mapstructure:"bulk_fetch_workers"`
}

// f64Ptr returns a pointer to v. Used to populate the pointer-typed scoring
// knobs (CompilationPenalty, RichMetadataBonusCap, F1MinScore) from a
// viper.GetFloat64 result, which can't have its address taken inline.
func f64Ptr(v float64) *float64 {
	return &v
}

// getFloat64Slice reads a []float64 out of viper. Viper has no
// GetFloat64Slice; SetDefault stores our []float64 default verbatim, but a
// value loaded from YAML/JSON typically comes back as []any with each
// element as float64 (or int for whole numbers), so this normalizes both
// shapes defensively. Any other shape (missing key, wrong type) returns nil
// — the metafetch-side resolver treats nil/mismatched-length as "unset" and
// falls back to the built-in duration tier table.
func getFloat64Slice(key string) []float64 {
	switch v := viper.Get(key).(type) {
	case []float64:
		return v
	case []any:
		out := make([]float64, 0, len(v))
		for _, item := range v {
			switch n := item.(type) {
			case float64:
				out = append(out, n)
			case int:
				out = append(out, float64(n))
			default:
				return nil
			}
		}
		return out
	default:
		return nil
	}
}

// AIBackend mode constants. They control, independently for embeddings and
// LLM/chat, which backend the corresponding client is constructed against.
//
//   - AIBackendModeDisabled: no client is constructed.
//   - AIBackendModeOpenAI: the real OpenAI cloud API (requires OpenAIAPIKey).
//   - AIBackendModeLocal: a local OpenAI-compatible endpoint (e.g. Ollama) at
//     AIBackendConfig.LocalBaseURL; the API key is ignored by the backend.
//   - AIBackendModeOpenAIFallbackLocal: primary OpenAI with a local fallback.
//     The fallback *trigger* is not implemented by this config; it is wired by
//     the error-classification layer (retry.go). At construction time this mode
//     behaves like OpenAI (real key required).
const (
	AIBackendModeDisabled            = "disabled"
	AIBackendModeOpenAI              = "openai"
	AIBackendModeLocal               = "local"
	AIBackendModeOpenAIFallbackLocal = "openai-fallback-local"
)

// AIBackendConfig holds the backend-mode toggle for the AI cluster. It nests
// independent embedding / LLM mode enums plus the local endpoint coordinates so
// operators can point each pipeline at OpenAI, a local Ollama-style backend, or
// disable it — without the previous coupling of everything to a single
// OpenAIAPIKey / OPENAI_BASE_URL pair.
//
// EmbeddingMode / LLMMode may be empty at rest; the effective mode is then
// derived from the legacy flat fields (Embedding.BaseURL, OpenAIAPIKey,
// EnableAIParsing, MetadataScoring.LLMEnabled) via Config.EffectiveEmbeddingMode
// / Config.EffectiveLLMMode. Migration (migrateAIBackendBlob) fills these in on
// load; the effective-mode helpers make even an un-migrated blob safe.
type AIBackendConfig struct {
	EmbeddingMode       string `json:"embedding_mode"        mapstructure:"embedding_mode"`
	LLMMode             string `json:"llm_mode"              mapstructure:"llm_mode"`
	LocalBaseURL        string `json:"local_base_url"        mapstructure:"local_base_url"`
	LocalEmbeddingModel string `json:"local_embedding_model" mapstructure:"local_embedding_model"`
	LocalLLMModel       string `json:"local_llm_model"       mapstructure:"local_llm_model"`
}

// EffectiveEmbeddingMode resolves the embedding backend mode. When
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
func (c *Config) EffectiveEmbeddingMode() string {
	if c.AIBackend.EmbeddingMode != "" {
		return c.AIBackend.EmbeddingMode
	}
	if !c.Embedding.Enabled {
		return AIBackendModeDisabled
	}
	if c.Embedding.BaseURL != "" {
		return AIBackendModeLocal
	}
	if c.OpenAIAPIKey != "" {
		return AIBackendModeOpenAI
	}
	return AIBackendModeDisabled
}

// EffectiveLLMMode resolves the LLM/chat backend mode. When AIBackend.LLMMode is
// set explicitly it wins. Otherwise: openai when a key is configured and any LLM
// consumer is enabled (EnableAIParsing or MetadataScoring.LLMEnabled), else
// disabled. There is no legacy flat field for a local LLM endpoint, so local LLM
// use must be selected explicitly via AIBackend.LLMMode. Pure (see
// EffectiveEmbeddingMode).
func (c *Config) EffectiveLLMMode() string {
	if c.AIBackend.LLMMode != "" {
		return c.AIBackend.LLMMode
	}
	if c.OpenAIAPIKey != "" && (c.EnableAIParsing || c.MetadataScoring.LLMEnabled) {
		return AIBackendModeOpenAI
	}
	return AIBackendModeDisabled
}

// ScheduledTaskConfig holds per-task scheduler settings.
type ScheduledTaskConfig struct {
	Enabled   bool `json:"enabled"    mapstructure:"enabled"`
	Interval  int  `json:"interval"   mapstructure:"interval"`
	OnStartup bool `json:"on_startup" mapstructure:"on_startup"`
}

// AutoUpdateConfig holds settings for the automatic update checker.
type AutoUpdateConfig struct {
	Enabled      bool   `json:"enabled"       mapstructure:"enabled"`
	Channel      string `json:"channel"       mapstructure:"channel"`
	CheckMinutes int    `json:"check_minutes" mapstructure:"check_minutes"`
	WindowStart  int    `json:"window_start"  mapstructure:"window_start"`
	WindowEnd    int    `json:"window_end"    mapstructure:"window_end"`
}

// ScheduledTasksConfig holds settings for all background scheduled tasks.
type ScheduledTasksConfig struct {
	DedupRefresh             ScheduledTaskConfig `json:"dedup_refresh"               mapstructure:"dedup_refresh"`
	LabelRefinement          ScheduledTaskConfig `json:"label_refinement"            mapstructure:"label_refinement"`
	AuthorSplit              ScheduledTaskConfig `json:"author_split"                mapstructure:"author_split"`
	DbOptimize               ScheduledTaskConfig `json:"db_optimize"                 mapstructure:"db_optimize"`
	MetadataRefresh          ScheduledTaskConfig `json:"metadata_refresh"            mapstructure:"metadata_refresh"`
	ResolveProductionAuthors ScheduledTaskConfig `json:"resolve_production_authors"  mapstructure:"resolve_production_authors"`
	SeriesPrune              ScheduledTaskConfig `json:"series_prune"                mapstructure:"series_prune"`
	AIDedupBatch             ScheduledTaskConfig `json:"ai_dedup_batch"              mapstructure:"ai_dedup_batch"`
	Reconcile                ScheduledTaskConfig `json:"reconcile"                   mapstructure:"reconcile"`
}

// Config holds application configuration
type Config struct {
	// Core paths
	RootDir       string `json:"root_dir"`
	DatabasePath  string `json:"database_path"`
	DatabaseType  string `json:"database_type"` // "pebble" (default) or "sqlite"
	EnableSQLite  bool   `json:"enable_sqlite"` // Must be true to use SQLite (safety flag)
	PlaylistDir   string `json:"playlist_dir"`
	SetupComplete bool   `json:"setup_complete"`

	// Library organization
	OrganizationStrategy    string `json:"organization_strategy"` // 'auto', 'copy', 'hardlink', 'reflink', 'symlink'
	ScanOnStartup           bool   `json:"scan_on_startup"`
	AutoOrganize            bool   `json:"auto_organize"`
	AutoScanEnabled         bool   `json:"auto_scan_enabled"`
	AutoScanDebounceSeconds int    `json:"auto_scan_debounce_seconds"`
	FolderNamingPattern     string `json:"folder_naming_pattern"`
	FileNamingPattern       string `json:"file_naming_pattern"`
	CreateBackups           bool   `json:"create_backups"`

	// Storage quotas
	EnableDiskQuota    bool `json:"enable_disk_quota"`
	DiskQuotaPercent   int  `json:"disk_quota_percent"`
	EnableUserQuotas   bool `json:"enable_user_quotas"`
	DefaultUserQuotaGB int  `json:"default_user_quota_gb"`

	// Metadata
	AutoFetchMetadata         bool             `json:"auto_fetch_metadata"`
	WriteBackMetadata         bool             `json:"write_back_metadata"`
	EmbedCoverArt             bool             `json:"embed_cover_art"`
	MetadataSources           []MetadataSource `json:"metadata_sources"`
	Language                  string           `json:"language"`
	MetadataReviewDefaultView string           `json:"metadata_review_default_view"`

	// Open Library data dumps
	OpenLibraryDumpEnabled bool   `json:"openlibrary_dump_enabled"`
	OpenLibraryDumpDir     string `json:"openlibrary_dump_dir"`

	// Hardcover.app API
	HardcoverAPIToken string `json:"hardcover_api_token"`

	// Google Books API
	GoogleBooksAPIKey string `json:"google_books_api_key"`

	// AI-powered parsing
	EnableAIParsing bool   `json:"enable_ai_parsing"`
	OpenAIAPIKey    string `json:"openai_api_key"`

	// AcoustIDAPIKey is the acoustid.org client ID used by the
	// acoustid.lookup-online op. Persisted to the settings DB (masked
	// in API responses). Falls back to the ACOUSTID_API_KEY env var
	// when empty, for compatibility with the original env-only setup.
	AcoustIDAPIKey string `json:"acoustid_api_key"`

	// Per-feature OpenAI model selection. Default to gpt-5-mini for all three
	// to preserve historical behavior. See spec docs/superpowers/specs/2026-04-27-per-feature-llm-model-knob-design.md.
	// Note: DedupReviewModel has moved to Dedup.ReviewModel.
	MetadataReviewModel string `json:"metadata_review_model" mapstructure:"metadata_review_model"`
	FilenameParseModel  string `json:"filename_parse_model"  mapstructure:"filename_parse_model"`
	CoverArtModel       string `json:"cover_art_model"       mapstructure:"cover_art_model"`

	// WhisperRemoteURL points batch transcription at a remote faster-whisper
	// server (e.g. "http://192.168.1.x:8000"); empty uses the local uv path.
	// Environment-authoritative (WHISPER_REMOTE_URL) — see applyEnvAuthoritativeConfig.
	WhisperRemoteURL string `json:"whisper_remote_url" mapstructure:"whisper_remote_url"`

	// WhisperEndpoints declares a pool of remote faster-whisper servers. When
	// non-empty it takes precedence over WhisperRemoteURL. Plain structs only —
	// this package must never import internal/transcribe; the conversion to
	// transcribe.Endpoint happens at the TranscribeBatch seam.
	// Environment-authoritative (WHISPER_ENDPOINTS, a JSON array string, e.g.
	// `[{"url":"http://whisper-1.local:8000","concurrency":2,"priority":1,"kind":"gpu","label":"gpu-box"}]`).
	WhisperEndpoints []WhisperEndpoint `json:"whisper_endpoints" mapstructure:"whisper_endpoints"`

	// Performance
	ConcurrentScans int `json:"concurrent_scans"`
	// ChapterConsolidationThresholdMin is the per-file duration threshold (minutes)
	// used during scanning to detect chapter-named files. If a group of ≥ 3 files
	// sharing the same base title (e.g. "01 - My Book", "02 - My Book") each
	// averages below this duration, they are consolidated into one book record.
	// Default 10. Set to 0 to disable consolidation.
	ChapterConsolidationThresholdMin int `json:"chapter_consolidation_threshold_min"`
	// CoalesceShatteredSiblings enables a scan-time post-pass that merges
	// single-file books shattered across "<prefix> - N" sibling chapter subdirs
	// (the layout that produced the 380K dedup-candidate explosion) into ONE
	// multi-file book — the scan-time analogue of maintenance.fs-regroup-xml.
	// DEFAULT OFF: the existing library is already healed; enable on a canary
	// before turning on by default. Path-based + the prefix⊆parent precision
	// guard (excludes flat dumps and series volumes); no extra tag I/O.
	CoalesceShatteredSiblings bool `json:"coalesce_shattered_siblings"`
	// Background operation timeout in minutes (0 disables timeout)
	OperationTimeoutMinutes int `json:"operation_timeout_minutes"`
	// MinBookSizeBytes: single-file books below this size are flagged as suspicious and
	// skipped for heavy processing. Set to -1 to disable. Defaults to 5242880 (5 MB).
	MinBookSizeBytes int64 `json:"min_book_size_bytes"`
	// Log retention in days (0 = keep forever)
	LogRetentionDays int `json:"log_retention_days"`
	// Operation log retention in days (0 = keep forever; default 90)
	OperationLogRetentionDays int `json:"operation_log_retention_days"`
	// Activity log retention (separate from operation log retention)
	ActivityLogRetentionChangeDays int `json:"activity_log_retention_change_days"` // default 90
	ActivityLogRetentionDebugDays  int `json:"activity_log_retention_debug_days"`  // default 30
	ActivityLogCompactionDays      int `json:"activity_log_compaction_days"`       // default 14

	// Embedding holds configuration for the embedding pipeline (model, provider, vector backend).
	Embedding EmbeddingConfig `json:"embedding" mapstructure:"embedding"`

	// Dedup holds all deduplication settings (thresholds, auto-merge, LLM model, signal bands).
	Dedup DedupConfig `json:"dedup" mapstructure:"dedup"`

	// DedupBoilerplate holds extension-only additions to the compiled-in
	// publisher-boilerplate title blocklist (internal/dedup/boilerplate.go,
	// INIT-4 T5). Empty (default) is byte-identical to the pre-config
	// hardcoded behavior.
	DedupBoilerplate DedupBoilerplateConfig `json:"dedup_boilerplate" mapstructure:"dedup_boilerplate"`

	// MetadataScoring holds all AI-assisted metadata candidate scoring settings
	// (embedding scoring, LLM rerank tier, and tag-write backup policy).
	// Previously these were 7 flat fields; Wave 3 nests them here.
	MetadataScoring MetadataScoringConfig `json:"metadata_scoring" mapstructure:"metadata_scoring"`

	// AIBackend holds the backend-mode toggle for the embedding + LLM clients
	// (independent EmbeddingMode / LLMMode enums, local endpoint coordinates).
	// See AIBackendConfig and EffectiveEmbeddingMode / EffectiveLLMMode.
	AIBackend AIBackendConfig `json:"ai_backend" mapstructure:"ai_backend"`

	// API limits
	APIRateLimitPerMinute  int  `json:"api_rate_limit_per_minute"`
	AuthRateLimitPerMinute int  `json:"auth_rate_limit_per_minute"`
	JSONBodyLimitMB        int  `json:"json_body_limit_mb"`
	UploadBodyLimitMB      int  `json:"upload_body_limit_mb"`
	EnableAuth             bool `json:"enable_auth"`
	EnableRateLimit        bool `json:"enable_rate_limit"`

	// ReviewApplyEnabled is the GLOBAL "big switch" for the review-queue apply path.
	// When false (the default), approving a review hold records the decision but NEVER
	// executes the real-world action (e.g. a regroup CombineBooks merge) — every hold
	// stays visible in the review pane for human eyes. Flip to true only when you want
	// approvals to actually apply. Producer-agnostic: gates ALL registered apply
	// handlers, not just regroup.
	ReviewApplyEnabled bool `json:"review_apply_enabled"`

	// Basic HTTP auth (lightweight single-user alternative)
	BasicAuthEnabled  bool   `json:"basic_auth_enabled"`
	BasicAuthUsername string `json:"basic_auth_username"`
	BasicAuthPassword string `json:"basic_auth_password"`

	// OAuth2 / OIDC single sign-on (GitHub + Google) and Cloudflare Access identity
	// passthrough. All off unless explicitly configured. An OAuth/Access login only
	// succeeds for an email on OAuthAllowedEmails — a valid IdP login by a
	// non-allowlisted account is rejected (verified ≠ authorized).
	OAuthEnabled            bool   `json:"oauth_enabled"`
	OAuthGithubClientID     string `json:"oauth_github_client_id"`
	OAuthGithubClientSecret string `json:"oauth_github_client_secret"`
	OAuthGoogleClientID     string `json:"oauth_google_client_id"`
	OAuthGoogleClientSecret string `json:"oauth_google_client_secret"`
	// OAuthRedirectBaseURL is the public origin used to build provider redirect URIs
	// (e.g. https://books.example.com → https://books.example.com/api/v1/auth/oauth/google/callback).
	OAuthRedirectBaseURL string `json:"oauth_redirect_base_url"`
	// OAuthAllowedEmails is the comma-separated allowlist. Only these emails may log in
	// via OAuth or Cloudflare Access (case-insensitive). Empty = no OAuth logins allowed.
	OAuthAllowedEmails string `json:"oauth_allowed_emails"`
	// OAuthDefaultRole is the role assigned to a newly auto-created OAuth user
	// (default "viewer" — least privilege; promote individual accounts as needed).
	OAuthDefaultRole string `json:"oauth_default_role"`
	// Cloudflare Access: when both are set, the middleware trusts a verified
	// Cf-Access-Jwt-Assertion header (checked against the team JWKS + AUD) as an
	// identity, resolving/creating a session without a second app-level login.
	CFAccessTeamDomain string `json:"cf_access_team_domain"` // e.g. myteam.cloudflareaccess.com
	CFAccessAUD        string `json:"cf_access_aud"`         // Access application AUD tag

	// ── Audiobookshelf-compatible sync API (design spec Phase 1) ─────────────
	//
	// Environment-authoritative, like the OAuth/Cloudflare keys above: they are set
	// from a systemd Environment= drop-in and re-applied on top of the DB config blob
	// by applyEnvAuthoritativeConfig. The ABS secret must never live in the blob.
	//
	// ABSAPIEnabled is the master feature flag and defaults to FALSE: with it unset,
	// no ABS route is registered at all and the server behaves exactly as it did
	// before this surface existed.
	ABSAPIEnabled bool `json:"abs_api_enabled"`
	// ABSAuthModes selects which identity resolvers are ACTIVE — a comma-separated
	// subset of {cf,jwt}, default "cf,jwt" (spec §3.0.1). "cf" alone hardens the
	// server to Cloudflare-Access identity only (no password login, no bearer);
	// "jwt" alone allows local/LAN testing with no Cloudflare at all. Both resolvers
	// are always built and tested; this only gates which run.
	ABSAuthModes string `json:"abs_auth_modes"`
	// ABSJWTSecret signs ABS access tokens and derives refresh tokens. REQUIRED when
	// ABSAPIEnabled — the server FAILS CLOSED at boot without it, and it is never
	// auto-generated, because an ephemeral secret would invalidate every client's
	// stored token on every restart. Deliberately excluded from the config blob and
	// from any API response.
	ABSJWTSecret string `json:"-"`
	// ABSAccessTokenTTL defaults to 720h = 30 DAYS, not one hour (§1.6 item 1): many
	// ABS clients implement no refresh at all, and a short access token logs them out
	// on that cadence. 30d is empirically proven by a known-working ABS shim.
	ABSAccessTokenTTL string `json:"abs_access_token_ttl"`
	// ABSRefreshTokenTTL defaults to 720h (30d).
	ABSRefreshTokenTTL string `json:"abs_refresh_token_ttl"`
	// ABSRefreshGrace is how long a rotated-out refresh token keeps working so a
	// concurrent or replayed refresh from the same device is answered idempotently
	// rather than orphaning the session (§3.4). Default 10m.
	ABSRefreshGrace string `json:"abs_refresh_grace"`
	// ABSServerVersion is reported by /status and serverSettings.version. Default
	// 2.36.0; at or above 2.22.0 suppresses AudioBooth's nag banner (§1.8.8 item 6).
	ABSServerVersion string `json:"abs_server_version"`
	// ABSDefaultLibraryID is returned as userDefaultLibraryId. It MUST be a non-null
	// 36-char UUID: AudioBooth decodes the field non-optionally, so null makes the app
	// unable to log in at all (§1.8.2), and Absorb splits ids at a fixed offset of 36
	// (§1.7.1). Placeholder until Phase 3 owns real libraries.
	ABSDefaultLibraryID string `json:"abs_default_library_id"`

	// Memory management
	MemoryLimitType string `json:"memory_limit_type"` // 'items', 'percent', 'absolute'
	CacheSize       int    `json:"cache_size"`        // number of items
	// CacheInvalidateOnBookUpdate controls whether updating a book's metadata
	// invalidates the list/facets caches. Defaults to false so caches stay
	// warm across metadata fetches and write-back operations.
	CacheInvalidateOnBookUpdate bool `json:"cache_invalidate_on_book_update"`
	// MetadataFetchCacheTTLDays is how long (in days) the DB-backed metadata
	// fetch cache (Audible/Audnexus/etc. API results) is considered fresh.
	// 0 means never expire. Default 7.
	MetadataFetchCacheTTLDays int `json:"metadata_fetch_cache_ttl_days"`
	// BootstrapKeyTTLDays is how long (in days) a bootstrap-issued, full-scope
	// API key remains valid before the auth middleware's existing expiry check
	// rejects it. Unlike MetadataFetchCacheTTLDays, 0 (or any non-positive
	// value) does NOT mean "never expire" — bootstrap keys are full-scope
	// admin credentials and must always expire, so a non-positive value falls
	// back to the default of 30. (SEC-1/PROC-6)
	BootstrapKeyTTLDays int `json:"bootstrap_key_ttl_days"`
	MemoryLimitPercent  int `json:"memory_limit_percent"` // % of system memory
	MemoryLimitMB       int `json:"memory_limit_mb"`      // absolute MB

	// Lifecycle / retention
	PurgeSoftDeletedAfterDays   int  `json:"purge_soft_deleted_after_days"`
	PurgeSoftDeletedDeleteFiles bool `json:"purge_soft_deleted_delete_files"`

	// Logging
	LogLevel          string `json:"log_level"`  // 'debug', 'info', 'warn', 'error'
	LogFormat         string `json:"log_format"` // 'text' or 'json'
	EnableJsonLogging bool   `json:"enable_json_logging"`

	// ITunes holds all iTunes sync and write-back settings.
	// Previously these were 10 flat fields; Wave 4 nests them here.
	ITunes ITunesConfig `json:"itunes" mapstructure:"itunes"`

	// Deluge integration
	DelugeWebURL           string `json:"deluge_web_url"`           // e.g. "http://<deluge-host>:8112"
	DelugeWebPassword      string `json:"deluge_web_password"`      // Web UI password (default: "deluge")
	DelugeDiscoveryLabel   string `json:"deluge_discovery_label"`   // label to filter for discovery (e.g. "audiobooks")
	DelugeDiscoveryEnabled bool   `json:"deluge_discovery_enabled"` // enable /discover endpoint (identify unimported torrents)
	DelugeMoveEnabled      bool   `json:"deluge_move_enabled"`      // enable MoveStorage calls when books are reorganized
	// ProtectedPaths is an explicit list of filesystem path prefixes that must
	// never be edited in-place (tag writes, renames, deletes). These are merged
	// with the Deluge save_path set at runtime. iTunes media paths belong here.
	ProtectedPaths []string `json:"protected_paths"` // default: empty

	// Auto-update
	AutoUpdate AutoUpdateConfig `json:"auto_update" mapstructure:"auto_update"`

	// Maintenance window (unified — replaces separate auto-update window)
	Maintenance MaintenanceConfig `json:"maintenance" mapstructure:"maintenance"`

	// Download client integration
	DownloadClient DownloadClientConfig `json:"download_client"`

	// API Keys (kept for backward compatibility, Goodreads deprecated Dec 2020)
	APIKeys struct {
	} `json:"api_keys"`

	// Path formatting & apply pipeline
	PathFormat           string `json:"path_format"`
	SegmentTitleFormat   string `json:"segment_title_format"`
	AutoRenameOnApply    bool   `json:"auto_rename_on_apply"`
	AutoWriteTagsOnApply bool   `json:"auto_write_tags_on_apply"`
	VerifyAfterWrite     bool   `json:"verify_after_write"`

	// Scheduled holds settings for all background scheduled tasks.
	// Previously 23 flat Scheduled* fields (Wave 6 nests them here).
	Scheduled ScheduledTasksConfig `json:"scheduled" mapstructure:"scheduled"`

	// Plugin system
	Plugins map[string]PluginConfig `json:"plugins"`

	SupportedExtensions []string `json:"supported_extensions"`
	ExcludePatterns     []string `json:"exclude_patterns"`

	// Tools configures the managed external-tool lifecycle (Ollama, fpcalc).
	Tools tools.ToolsConfig `json:"tools"`

	// WriteStartupReadOnlyKey controls whether a 24-hour read-only API key is
	// written to <data-dir>/.readonly-key on every server startup (SEC-2).
	// Default true preserves existing behaviour; set false in hardened deployments
	// where file-system credential files are unwanted. The bootstrap recovery
	// token (.bootstrap-token) is always generated — it is emergency access
	// infrastructure and is not affected by this flag.
	WriteStartupReadOnlyKey bool `json:"write_startup_readonly_key" mapstructure:"write_startup_readonly_key"`

	// DisablePerUserSearchFilters, when true, makes searchWithBleve skip
	// per-user DSL post-filtering (read_status/progress_pct/last_played)
	// and warn — the pre-fix drop-and-warn behavior. Ops escape hatch for
	// the up-to-10K sequential state reads per per-user-filtered request
	// (spec Decision 11); NOT a feature flag. Default false = filters ON.
	DisablePerUserSearchFilters bool `json:"disable_per_user_search_filters"`

	// EnabledSortIndexes names the library sort fields that get a memdb
	// sorted secondary index, turning "sort by X" from a
	// materialise-the-whole-filtered-set-and-sort into a streaming walk.
	//
	// Recognised: author, narrator, series, year, created_at, updated_at,
	// duration, file_size, bitrate. ("title" is always indexed and is not
	// configurable.) Unknown names are ignored with a warning.
	//
	// ⚠️ DEFAULT IS EMPTY, AND THAT IS DELIBERATE. Each index costs real
	// memory, measured rather than estimated at 100,000 books:
	//
	//	all nine enabled: 6,395 B/book vs 2,645 B/book — +142% (+3,750 B)
	//	extrapolated to ~48,900 books: ~+175 MB
	//	insert throughput: 2.8x slower
	//
	// That is ~19 MB per sort key. go-memdb's radix tree is immutable and
	// path-copies nodes on every insert, so cost tracks node count rather
	// than key length — short keys do not make it cheap.
	//
	// 🚨 This comment previously said "+1,312 MB" and "~146 MB per sort key",
	// extrapolating to 366,916 books. That was never a book count — it was
	// Pebble KEYS under the `book:` prefix, ~7.5 per row. The per-book
	// measurement was always right; only the multiplier was wrong. See
	// memdb_sort_index_cost_test.go for the full correction. It matters
	// because it changes the answer: +175 MB is a very different proposition
	// from +1.3 GB.
	//
	// Enable the fields that are actually
	// sorted by, and measure warmup afterwards.
	//
	// Empty (the default) reproduces today's behaviour exactly: only title
	// streams, everything else takes the existing heavy path.
	EnabledSortIndexes []string `json:"enabled_sort_indexes" mapstructure:"enabled_sort_indexes"`
}

// mu guards AppConfig against concurrent writes.
//
// WHY: AppConfig is a package-level struct value read by hundreds of sites and
// mutated at runtime by the config-update HTTP handler and test set-ups.  The
// -race detector flagged a concurrent write (update_service.go:130) vs
// background readers.  Rather than a 500-site rewrite we introduce a narrow
// RWMutex used by ALL write paths (via Mutate) and by the handful of hot read
// paths that need a guaranteed-consistent snapshot (via Snapshot).
//
// Direct reads of AppConfig fields that are set once at startup are tolerated
// with residual risk — they see a worst-case value from the previous Mutate
// call and are no worse than the pre-fix behaviour. The observable races
// (concurrent test setup + HTTP update service) are eliminated by routing all
// write sites through Mutate.
var mu sync.RWMutex

// AppConfig is the global application configuration.
//
// Convention:
//   - All WRITES must go through Mutate — never assign AppConfig directly
//     outside this package.
//   - Callers that need a consistent snapshot (concurrent-safe read of multiple
//     fields together) should call Snapshot().
//   - Single-field reads at hot paths that were set once at startup may access
//     AppConfig directly; they carry residual risk documented above.
var AppConfig Config

// Snapshot returns a value copy of AppConfig under a read lock.
// Use this when you need a consistent multi-field view of the config
// (e.g. inside background goroutines or HTTP handlers that read many fields).
func Snapshot() Config {
	mu.RLock()
	defer mu.RUnlock()
	return AppConfig
}

// Mutate applies fn to AppConfig under a write lock.
// ALL write sites (startup init, update service, test setups) must use this.
func Mutate(fn func(*Config)) {
	mu.Lock()
	defer mu.Unlock()
	fn(&AppConfig)
}

// applyEnvAuthoritativeConfig re-applies the environment-authoritative config keys on
// top of an already-populated Config, so a systemd Environment= value wins over a
// persisted config_blob. It exists because LoadConfigFromDatabase restores the whole
// Config from the DB blob (*c = loaded), which overwrites the env-derived values that
// InitConfig set. Call it LAST in the DB-load path (after the blob and secret rows are
// applied) via Mutate.
//
// This is pure viper: each key has a BindEnv binding (see InitConfig), so viper.GetX
// honors the environment — no os.Getenv here. viper.IsSet is true only when a real
// override layer (env/flag/config-file) supplied the key, NOT for SetDefault, so a
// UI-set value that lives only in the blob is left untouched when no env var is present.
// Env-authoritative keys ONLY: OAuth / Cloudflare Access / Whisper. UI-managed keys
// (itunes.*, scheduled.*, etc.) are intentionally excluded — they belong to the blob.
// WhisperEndpoint declares one remote Whisper server for the dispatch pool.
// Mirror of transcribe.Endpoint kept as a plain config struct so this package
// never imports internal/transcribe.
type WhisperEndpoint struct {
	URL         string `json:"url"         mapstructure:"url"`
	Concurrency int    `json:"concurrency" mapstructure:"concurrency"`
	Label       string `json:"label"       mapstructure:"label"`
	// Priority: lower = preferred (GPU box 1, CPU box 100).
	Priority int `json:"priority" mapstructure:"priority"`
	// Kind is informational only ("gpu", "cpu", or "").
	Kind string `json:"kind" mapstructure:"kind"`
}

// ParseWhisperEndpoints decodes the WHISPER_ENDPOINTS JSON array string.
// Empty input or malformed JSON yields nil, which falls back to the
// single-URL (or local) path rather than failing startup — the endpoint list
// is an optimization, not a correctness requirement.
func ParseWhisperEndpoints(s string) []WhisperEndpoint {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var endpoints []WhisperEndpoint
	if err := json.Unmarshal([]byte(s), &endpoints); err != nil {
		fmt.Fprintf(os.Stderr, "config: ignoring malformed whisper_endpoints (%v)\n", err)
		return nil
	}
	return endpoints
}

func applyEnvAuthoritativeConfig(c *Config) {
	if viper.IsSet("oauth_enabled") {
		c.OAuthEnabled = viper.GetBool("oauth_enabled")
	}
	if viper.IsSet("oauth_github_client_id") {
		c.OAuthGithubClientID = viper.GetString("oauth_github_client_id")
	}
	if viper.IsSet("oauth_github_client_secret") {
		c.OAuthGithubClientSecret = viper.GetString("oauth_github_client_secret")
	}
	if viper.IsSet("oauth_google_client_id") {
		c.OAuthGoogleClientID = viper.GetString("oauth_google_client_id")
	}
	if viper.IsSet("oauth_google_client_secret") {
		c.OAuthGoogleClientSecret = viper.GetString("oauth_google_client_secret")
	}
	if viper.IsSet("oauth_redirect_base_url") {
		c.OAuthRedirectBaseURL = viper.GetString("oauth_redirect_base_url")
	}
	if viper.IsSet("oauth_allowed_emails") {
		c.OAuthAllowedEmails = viper.GetString("oauth_allowed_emails")
	}
	if viper.IsSet("oauth_default_role") {
		c.OAuthDefaultRole = viper.GetString("oauth_default_role")
	}
	if viper.IsSet("cf_access_team_domain") {
		c.CFAccessTeamDomain = viper.GetString("cf_access_team_domain")
	}
	if viper.IsSet("cf_access_aud") {
		c.CFAccessAUD = viper.GetString("cf_access_aud")
	}
	if viper.IsSet("whisper_remote_url") {
		c.WhisperRemoteURL = viper.GetString("whisper_remote_url")
	}
	if viper.IsSet("whisper_endpoints") {
		c.WhisperEndpoints = ParseWhisperEndpoints(viper.GetString("whisper_endpoints"))
	}
	// Audiobookshelf sync API. Env-authoritative for the same reason as the OAuth keys:
	// LoadConfigFromDatabase replaces the whole struct with the blob, so without these
	// a systemd Environment=ABS_JWT_SECRET would be silently dropped and the ABS API
	// would fail closed at boot despite being configured.
	if viper.IsSet("abs_api_enabled") {
		c.ABSAPIEnabled = viper.GetBool("abs_api_enabled")
	}
	if viper.IsSet("abs_auth_modes") {
		c.ABSAuthModes = viper.GetString("abs_auth_modes")
	}
	if viper.IsSet("abs_jwt_secret") {
		c.ABSJWTSecret = viper.GetString("abs_jwt_secret")
	}
	if viper.IsSet("abs_access_token_ttl") {
		c.ABSAccessTokenTTL = viper.GetString("abs_access_token_ttl")
	}
	if viper.IsSet("abs_refresh_token_ttl") {
		c.ABSRefreshTokenTTL = viper.GetString("abs_refresh_token_ttl")
	}
	if viper.IsSet("abs_refresh_grace") {
		c.ABSRefreshGrace = viper.GetString("abs_refresh_grace")
	}
	if viper.IsSet("abs_server_version") {
		c.ABSServerVersion = viper.GetString("abs_server_version")
	}
	if viper.IsSet("abs_default_library_id") {
		c.ABSDefaultLibraryID = viper.GetString("abs_default_library_id")
	}
}

// ApplyEnvAuthoritativeConfig applies the environment-authoritative overrides to the
// global AppConfig under the write lock. Exported for the DB-load path in persistence.go.
func ApplyEnvAuthoritativeConfig() {
	Mutate(applyEnvAuthoritativeConfig)
}

// InitConfig initializes the application configuration

func InitConfig() {
	// Set core defaults
	viper.SetDefault("database_type", "pebble")
	viper.SetDefault("enable_sqlite3_i_know_the_risks", false)
	viper.SetDefault("setup_complete", false)

	// Set library organization defaults
	viper.SetDefault("organization_strategy", "auto")
	viper.SetDefault("scan_on_startup", false)
	viper.SetDefault("auto_organize", true)
	viper.SetDefault("auto_scan_enabled", false)
	viper.SetDefault("auto_scan_debounce_seconds", 30)
	viper.SetDefault("folder_naming_pattern", "{author}/{series}/{title} ({print_year})")
	viper.SetDefault("file_naming_pattern", "{title} - {author} - read by {narrator}")
	viper.SetDefault("create_backups", true)

	// Set storage quota defaults
	viper.SetDefault("enable_disk_quota", false)
	viper.SetDefault("disk_quota_percent", 80)
	viper.SetDefault("enable_user_quotas", false)
	viper.SetDefault("default_user_quota_gb", 100)

	// Set metadata defaults
	viper.SetDefault("auto_fetch_metadata", true)
	viper.SetDefault("write_back_metadata", false)
	viper.SetDefault("embed_cover_art", false)
	viper.SetDefault("language", "en")
	viper.SetDefault("metadata_review_default_view", "compact")

	// Open Library dump defaults
	viper.SetDefault("openlibrary_dump_enabled", false)
	viper.SetDefault("openlibrary_dump_dir", "")

	// Hardcover.app defaults
	viper.SetDefault("hardcover_api_token", "")

	// Set AI parsing defaults
	viper.SetDefault("enable_ai_parsing", true)
	viper.SetDefault("openai_api_key", "")
	viper.SetDefault("acoustid_api_key", "")

	// Per-feature model defaults — gpt-5-mini preserves historical behavior.
	// dedup_review_model has moved to dedup.review_model (nested DedupConfig).
	viper.SetDefault("dedup.review_model", "gpt-5-mini")
	viper.SetDefault("metadata_review_model", "gpt-5-mini")
	viper.SetDefault("filename_parse_model", "gpt-5-mini")
	viper.SetDefault("cover_art_model", "gpt-5-mini")

	// Set performance defaults — scale with available CPUs
	defaultWorkers := runtime.NumCPU()
	if defaultWorkers < 4 {
		defaultWorkers = 4
	}
	viper.SetDefault("concurrent_scans", defaultWorkers)
	viper.SetDefault("chapter_consolidation_threshold_min", 10)
	viper.SetDefault("operation_timeout_minutes", 30)
	viper.SetDefault("log_retention_days", 90)

	// API security/runtime limits
	viper.SetDefault("api_rate_limit_per_minute", 0)
	viper.SetDefault("auth_rate_limit_per_minute", 10)
	viper.SetDefault("json_body_limit_mb", 1)
	viper.SetDefault("upload_body_limit_mb", 10)
	viper.SetDefault("enable_auth", true)
	viper.SetDefault("enable_rate_limit", true)
	viper.SetDefault("basic_auth_enabled", false)
	viper.SetDefault("basic_auth_username", "")
	viper.SetDefault("basic_auth_password", "")

	// OAuth / OIDC + Cloudflare Access defaults (all off / empty unless configured).
	viper.SetDefault("oauth_enabled", false)
	viper.SetDefault("oauth_github_client_id", "")
	viper.SetDefault("oauth_github_client_secret", "")
	viper.SetDefault("oauth_google_client_id", "")
	viper.SetDefault("oauth_google_client_secret", "")
	viper.SetDefault("oauth_redirect_base_url", "")
	viper.SetDefault("oauth_allowed_emails", "")
	viper.SetDefault("oauth_default_role", "viewer")
	viper.SetDefault("cf_access_team_domain", "")
	viper.SetDefault("cf_access_aud", "")
	viper.BindEnv("oauth_enabled", "OAUTH_ENABLED")                           //nolint:errcheck
	viper.BindEnv("oauth_github_client_id", "OAUTH_GITHUB_CLIENT_ID")         //nolint:errcheck
	viper.BindEnv("oauth_github_client_secret", "OAUTH_GITHUB_CLIENT_SECRET") //nolint:errcheck
	viper.BindEnv("oauth_google_client_id", "OAUTH_GOOGLE_CLIENT_ID")         //nolint:errcheck
	viper.BindEnv("oauth_google_client_secret", "OAUTH_GOOGLE_CLIENT_SECRET") //nolint:errcheck
	viper.BindEnv("oauth_redirect_base_url", "OAUTH_REDIRECT_BASE_URL")       //nolint:errcheck
	viper.BindEnv("oauth_allowed_emails", "OAUTH_ALLOWED_EMAILS")             //nolint:errcheck
	viper.BindEnv("oauth_default_role", "OAUTH_DEFAULT_ROLE")                 //nolint:errcheck
	viper.BindEnv("cf_access_team_domain", "CF_ACCESS_TEAM_DOMAIN")           //nolint:errcheck
	viper.BindEnv("cf_access_aud", "CF_ACCESS_AUD")                           //nolint:errcheck

	registerABSDefaults()

	// Transcription: remote faster-whisper server URL (env-authoritative).
	viper.SetDefault("whisper_remote_url", "")
	viper.BindEnv("whisper_remote_url", "WHISPER_REMOTE_URL") //nolint:errcheck
	// Multi-endpoint pool, JSON array string (env-authoritative; wins over
	// whisper_remote_url when non-empty).
	viper.SetDefault("whisper_endpoints", "")
	viper.BindEnv("whisper_endpoints", "WHISPER_ENDPOINTS") //nolint:errcheck

	// Set memory management defaults
	viper.SetDefault("memory_limit_type", "items")
	viper.SetDefault("cache_size", 1000)
	viper.SetDefault("metadata_fetch_cache_ttl_days", 180)
	viper.SetDefault("bootstrap_key_ttl_days", 30)
	viper.SetDefault("memory_limit_percent", 25)
	viper.SetDefault("memory_limit_mb", 512)

	// Lifecycle / retention defaults
	viper.SetDefault("purge_soft_deleted_after_days", 30)
	viper.SetDefault("purge_soft_deleted_delete_files", false)

	// Set logging defaults
	viper.SetDefault("log_level", "info")
	viper.SetDefault("log_format", "text")
	viper.SetDefault("enable_json_logging", false)

	// Scheduled task defaults (nested under "scheduled.*.*")
	viper.SetDefault("scheduled.dedup_refresh.enabled", false)
	viper.SetDefault("scheduled.dedup_refresh.interval", 360)
	viper.SetDefault("scheduled.dedup_refresh.on_startup", false)
	// label_refinement ships DISABLED (INIT-1 T6): the scheduled dry-run chain
	// (dedup.rebuild-gold-labels → dedup.calibrate-composite) only runs when an
	// owner flips enabled=true. Interval is weekly (10080 min).
	viper.SetDefault("scheduled.label_refinement.enabled", false)
	viper.SetDefault("scheduled.label_refinement.interval", 10080)
	viper.SetDefault("scheduled.label_refinement.on_startup", false)
	viper.SetDefault("scheduled.author_split.enabled", false)
	viper.SetDefault("scheduled.author_split.interval", 0)
	viper.SetDefault("scheduled.author_split.on_startup", false)
	viper.SetDefault("scheduled.db_optimize.enabled", false)
	viper.SetDefault("scheduled.db_optimize.interval", 1440)
	viper.SetDefault("scheduled.db_optimize.on_startup", false)
	viper.SetDefault("scheduled.metadata_refresh.enabled", false)
	viper.SetDefault("scheduled.metadata_refresh.interval", 0)
	viper.SetDefault("scheduled.metadata_refresh.on_startup", false)
	viper.SetDefault("scheduled.resolve_production_authors.enabled", false)
	viper.SetDefault("scheduled.resolve_production_authors.interval", 0)
	viper.SetDefault("scheduled.series_prune.enabled", false)
	viper.SetDefault("scheduled.series_prune.interval", 0)
	viper.SetDefault("scheduled.series_prune.on_startup", false)
	viper.SetDefault("scheduled.ai_dedup_batch.enabled", false)
	viper.SetDefault("scheduled.ai_dedup_batch.interval", 1440)
	viper.SetDefault("scheduled.ai_dedup_batch.on_startup", false)
	viper.SetDefault("scheduled.reconcile.enabled", false)
	viper.SetDefault("scheduled.reconcile.interval", 0)
	viper.SetDefault("scheduled.reconcile.on_startup", false)
	// BindEnv maps env vars so SCHEDULED_* overrides work even without AutomaticEnv.
	viper.BindEnv("scheduled.dedup_refresh.enabled", "SCHEDULED_DEDUP_REFRESH_ENABLED")                             //nolint:errcheck
	viper.BindEnv("scheduled.dedup_refresh.interval", "SCHEDULED_DEDUP_REFRESH_INTERVAL")                           //nolint:errcheck
	viper.BindEnv("scheduled.dedup_refresh.on_startup", "SCHEDULED_DEDUP_REFRESH_ON_STARTUP")                       //nolint:errcheck
	viper.BindEnv("scheduled.label_refinement.enabled", "SCHEDULED_LABEL_REFINEMENT_ENABLED")                       //nolint:errcheck
	viper.BindEnv("scheduled.label_refinement.interval", "SCHEDULED_LABEL_REFINEMENT_INTERVAL")                     //nolint:errcheck
	viper.BindEnv("scheduled.label_refinement.on_startup", "SCHEDULED_LABEL_REFINEMENT_ON_STARTUP")                 //nolint:errcheck
	viper.BindEnv("scheduled.author_split.enabled", "SCHEDULED_AUTHOR_SPLIT_ENABLED")                               //nolint:errcheck
	viper.BindEnv("scheduled.author_split.interval", "SCHEDULED_AUTHOR_SPLIT_INTERVAL")                             //nolint:errcheck
	viper.BindEnv("scheduled.author_split.on_startup", "SCHEDULED_AUTHOR_SPLIT_ON_STARTUP")                         //nolint:errcheck
	viper.BindEnv("scheduled.db_optimize.enabled", "SCHEDULED_DB_OPTIMIZE_ENABLED")                                 //nolint:errcheck
	viper.BindEnv("scheduled.db_optimize.interval", "SCHEDULED_DB_OPTIMIZE_INTERVAL")                               //nolint:errcheck
	viper.BindEnv("scheduled.db_optimize.on_startup", "SCHEDULED_DB_OPTIMIZE_ON_STARTUP")                           //nolint:errcheck
	viper.BindEnv("scheduled.metadata_refresh.enabled", "SCHEDULED_METADATA_REFRESH_ENABLED")                       //nolint:errcheck
	viper.BindEnv("scheduled.metadata_refresh.interval", "SCHEDULED_METADATA_REFRESH_INTERVAL")                     //nolint:errcheck
	viper.BindEnv("scheduled.metadata_refresh.on_startup", "SCHEDULED_METADATA_REFRESH_ON_STARTUP")                 //nolint:errcheck
	viper.BindEnv("scheduled.resolve_production_authors.enabled", "SCHEDULED_RESOLVE_PRODUCTION_AUTHORS_ENABLED")   //nolint:errcheck
	viper.BindEnv("scheduled.resolve_production_authors.interval", "SCHEDULED_RESOLVE_PRODUCTION_AUTHORS_INTERVAL") //nolint:errcheck
	viper.BindEnv("scheduled.series_prune.enabled", "SCHEDULED_SERIES_PRUNE_ENABLED")                               //nolint:errcheck
	viper.BindEnv("scheduled.series_prune.interval", "SCHEDULED_SERIES_PRUNE_INTERVAL")                             //nolint:errcheck
	viper.BindEnv("scheduled.series_prune.on_startup", "SCHEDULED_SERIES_PRUNE_ON_STARTUP")                         //nolint:errcheck
	viper.BindEnv("scheduled.ai_dedup_batch.enabled", "SCHEDULED_AI_DEDUP_BATCH_ENABLED")                           //nolint:errcheck
	viper.BindEnv("scheduled.ai_dedup_batch.interval", "SCHEDULED_AI_DEDUP_BATCH_INTERVAL")                         //nolint:errcheck
	viper.BindEnv("scheduled.ai_dedup_batch.on_startup", "SCHEDULED_AI_DEDUP_BATCH_ON_STARTUP")                     //nolint:errcheck
	viper.BindEnv("scheduled.reconcile.enabled", "SCHEDULED_RECONCILE_ENABLED")                                     //nolint:errcheck
	viper.BindEnv("scheduled.reconcile.interval", "SCHEDULED_RECONCILE_INTERVAL")                                   //nolint:errcheck
	viper.BindEnv("scheduled.reconcile.on_startup", "SCHEDULED_RECONCILE_ON_STARTUP")                               //nolint:errcheck

	// iTunes sync defaults (nested under "itunes.*").
	// BindEnv maps env vars so ITUNES_SYNC_ENABLED etc. override even without AutomaticEnv.
	viper.SetDefault("itunes.sync_enabled", true)
	viper.SetDefault("itunes.sync_interval", 30)
	viper.SetDefault("itunes.write_back_enabled", false)
	viper.SetDefault("itunes.library_write_path", "")
	viper.SetDefault("itunes.library_read_path", "")
	viper.SetDefault("itunes.auto_write_back", false)
	viper.SetDefault("itunes.path_trim_enabled", false)
	viper.SetDefault("itunes.windows_root_path", "")
	viper.SetDefault("itunes.media_root", "")
	viper.BindEnv("itunes.sync_enabled", "ITUNES_SYNC_ENABLED")             //nolint:errcheck
	viper.BindEnv("itunes.sync_interval", "ITUNES_SYNC_INTERVAL")           //nolint:errcheck
	viper.BindEnv("itunes.write_back_enabled", "ITUNES_WRITE_BACK_ENABLED") //nolint:errcheck
	viper.BindEnv("itunes.auto_write_back", "ITUNES_AUTO_WRITE_BACK")       //nolint:errcheck

	// Auto-update defaults
	viper.SetDefault("auto_update.enabled", false)
	viper.SetDefault("auto_update.channel", "stable")
	viper.SetDefault("auto_update.check_minutes", 60)
	viper.SetDefault("auto_update.window_start", 2)
	viper.SetDefault("auto_update.window_end", 5)
	viper.BindEnv("auto_update.enabled", "AUTO_UPDATE_ENABLED")             //nolint:errcheck
	viper.BindEnv("auto_update.channel", "AUTO_UPDATE_CHANNEL")             //nolint:errcheck
	viper.BindEnv("auto_update.check_minutes", "AUTO_UPDATE_CHECK_MINUTES") //nolint:errcheck
	viper.BindEnv("auto_update.window_start", "AUTO_UPDATE_WINDOW_START")   //nolint:errcheck
	viper.BindEnv("auto_update.window_end", "AUTO_UPDATE_WINDOW_END")       //nolint:errcheck

	// Maintenance window defaults
	viper.SetDefault("maintenance.enabled", true)
	viper.SetDefault("maintenance.window_start", 1)
	viper.SetDefault("maintenance.window_end", 4)
	// Per-task defaults — most maintenance tasks default true
	viper.SetDefault("maintenance.dedup_refresh", true)
	viper.SetDefault("maintenance.series_prune", true)
	viper.SetDefault("maintenance.author_split", true)
	viper.SetDefault("maintenance.tombstone_cleanup", true)
	viper.SetDefault("maintenance.reconcile", true)
	viper.SetDefault("maintenance.purge_deleted", true)
	viper.SetDefault("maintenance.purge_old_logs", true)
	viper.SetDefault("maintenance.db_optimize", true)
	// Non-maintenance tasks default false
	viper.SetDefault("maintenance.library_scan", false)
	viper.SetDefault("maintenance.library_organize", false)
	viper.SetDefault("maintenance.metadata_refresh", false)
	// FS-walk-based on-disk size refresh — true by default (cheap, runs nightly,
	// keeps the FS-side cache fresh for any caller that queries physical sizes).
	viper.SetDefault("maintenance.library_size_refresh", true)
	// AcoustID online lookup is OFF by default — uses third-party quota
	viper.SetDefault("maintenance.acoustid_online_lookup", false)
	viper.SetDefault("maintenance.acoustid_nightly_limit", 5000)
	// AcoustID (local) fingerprint backfill is OFF by default as of 2026-08-11.
	// The nightly op spawns fpcalc/ffmpeg per book file and loads the entire
	// book table into memory before it can start; in production it held ~862 MB
	// of live heap and was implicated in three OOM kills in one night. Opt in
	// deliberately once the memory profile of the load phase is fixed.
	viper.SetDefault("maintenance.acoustid_backfill", false)
	// BindEnv maps env vars so MAINTENANCE_ENABLED etc. override even without AutomaticEnv.
	viper.BindEnv("maintenance.enabled", "MAINTENANCE_ENABLED")                               //nolint:errcheck
	viper.BindEnv("maintenance.window_start", "MAINTENANCE_WINDOW_START")                     //nolint:errcheck
	viper.BindEnv("maintenance.window_end", "MAINTENANCE_WINDOW_END")                         //nolint:errcheck
	viper.BindEnv("maintenance.acoustid_online_lookup", "MAINTENANCE_ACOUSTID_ONLINE_LOOKUP") //nolint:errcheck
	viper.BindEnv("maintenance.acoustid_nightly_limit", "MAINTENANCE_ACOUSTID_NIGHTLY_LIMIT") //nolint:errcheck
	viper.BindEnv("maintenance.acoustid_backfill", "MAINTENANCE_ACOUSTID_BACKFILL")           //nolint:errcheck

	// Download client defaults
	viper.SetDefault("download_client.torrent.type", "")
	viper.SetDefault("download_client.torrent.deluge.host", "")
	viper.SetDefault("download_client.torrent.deluge.port", 0)
	viper.SetDefault("download_client.torrent.deluge.username", "")
	viper.SetDefault("download_client.torrent.deluge.password", "")
	viper.SetDefault("download_client.torrent.qbittorrent.host", "")
	viper.SetDefault("download_client.torrent.qbittorrent.port", 0)
	viper.SetDefault("download_client.torrent.qbittorrent.username", "")
	viper.SetDefault("download_client.torrent.qbittorrent.password", "")
	viper.SetDefault("download_client.torrent.qbittorrent.use_https", false)
	viper.SetDefault("download_client.usenet.type", "")
	viper.SetDefault("download_client.usenet.sabnzbd.host", "")
	viper.SetDefault("download_client.usenet.sabnzbd.port", 0)
	viper.SetDefault("download_client.usenet.sabnzbd.api_key", "")
	viper.SetDefault("download_client.usenet.sabnzbd.use_https", false)
	// Path formatting & apply pipeline defaults
	viper.SetDefault("path_format", "{author}/{series_prefix}{title}/{track_title}.{ext}")
	viper.SetDefault("segment_title_format", "{title} - {track}/{total_tracks}")
	viper.SetDefault("auto_rename_on_apply", true)
	viper.SetDefault("auto_write_tags_on_apply", true)
	viper.SetDefault("verify_after_write", true)
	viper.SetDefault("write_startup_readonly_key", true) // SEC-2: opt-out to disable file write

	// Embedding + vector index defaults (nested under "embedding.*").
	// BindEnv maps each dot-notation key to its uppercase env var name so that
	// EMBEDDING_ENABLED, EMBEDDING_MODEL, etc. override the defaults even when
	// AutomaticEnv() is not active (e.g. in unit tests that call InitConfig directly).
	viper.SetDefault("embedding.enabled", true)
	viper.SetDefault("embedding.model", "text-embedding-3-large")
	viper.SetDefault("embedding.dimensions", 3072)
	viper.SetDefault("embedding.base_url", "")
	viper.SetDefault("embedding.vector_backend", "chromem")
	viper.BindEnv("embedding.enabled", "EMBEDDING_ENABLED")           //nolint:errcheck
	viper.BindEnv("embedding.model", "EMBEDDING_MODEL")               //nolint:errcheck
	viper.BindEnv("embedding.dimensions", "EMBEDDING_DIMENSIONS")     //nolint:errcheck
	viper.BindEnv("embedding.base_url", "EMBEDDING_BASE_URL")         //nolint:errcheck
	viper.BindEnv("embedding.vector_backend", "VECTOR_INDEX_BACKEND") //nolint:errcheck

	// Dedup threshold + behaviour defaults (nested under "dedup.*").
	viper.SetDefault("dedup.book_high_threshold", 0.95)
	viper.SetDefault("dedup.book_low_threshold", 0.85)
	viper.SetDefault("dedup.author_high_threshold", 0.92)
	viper.SetDefault("dedup.author_low_threshold", 0.80)
	viper.SetDefault("dedup.auto_merge_enabled", true)
	viper.SetDefault("dedup.embeddings_enabled", true)              // opt-out: set false on no-internet boxes
	viper.SetDefault("dedup.llm_auto_merge_high_confidence", false) // opt-in
	viper.SetDefault("dedup.on_import_via_scheduler", false)        // opt-in — keep eager path until M4 confirmed

	// Dedup boilerplate-blocklist extras (nested under "dedup_boilerplate.*",
	// INIT-4 T5). Empty by default — the compiled-in blocklist in
	// internal/dedup/boilerplate.go is always active regardless.
	viper.SetDefault("dedup_boilerplate.extra_title_patterns", []string{})
	viper.SetDefault("dedup_boilerplate.extra_prefix_patterns", []string{})

	// Metadata candidate scoring defaults (nested under "metadata_scoring.*").
	// BindEnv maps env vars so METADATA_SCORING_* overrides even without AutomaticEnv.
	viper.SetDefault("metadata_scoring.embedding_enabled", false)
	viper.SetDefault("metadata_scoring.embedding_min_score", 0.82)
	viper.SetDefault("metadata_scoring.embedding_best_match", 0.88)
	viper.SetDefault("metadata_scoring.llm_enabled", false)
	viper.SetDefault("metadata_scoring.llm_rerank_epsilon", 0.05)
	viper.SetDefault("metadata_scoring.llm_rerank_top_k", 5)
	viper.SetDefault("metadata_scoring.write_backup_before", true)
	// Scoring literals extracted into config (INIT-3-T1) — defaults equal
	// today's hardcoded literals so behavior is bit-identical until an
	// operator tunes a knob. See MetadataScoringConfig for field docs.
	viper.SetDefault("metadata_scoring.transcription_title_exact_boost", 2.0)
	viper.SetDefault("metadata_scoring.transcription_title_substr_boost", 1.4)
	viper.SetDefault("metadata_scoring.transcription_author_boost", 1.6)
	viper.SetDefault("metadata_scoring.transcription_narrator_boost", 1.4)
	viper.SetDefault("metadata_scoring.compilation_penalty", 0.15)
	viper.SetDefault("metadata_scoring.rich_metadata_field_bonus", 0.05)
	viper.SetDefault("metadata_scoring.rich_metadata_bonus_cap", 0.15)
	viper.SetDefault("metadata_scoring.f1_min_score", 0.35)
	viper.SetDefault("metadata_scoring.series_name_match_boost", 1.4)
	viper.SetDefault("metadata_scoring.series_number_exact_boost", 2.0)
	viper.SetDefault("metadata_scoring.series_number_wrong_penalty", 0.5)
	viper.SetDefault("metadata_scoring.duration_tier_multipliers", []float64{1.30, 1.20, 1.10, 1.00, 0.75, 0.50})
	viper.SetDefault("metadata_scoring.duration_tier_scores", []float64{20, 15, 10, 0, -10, -20})
	viper.SetDefault("metadata_scoring.bulk_fetch_workers", 4)

	// AI backend-mode toggle. Modes default empty (resolved from legacy fields
	// by EffectiveEmbeddingMode / EffectiveLLMMode). LocalBaseURL uses a
	// placeholder host; real endpoints live in gitignored local config.
	viper.SetDefault("ai_backend.embedding_mode", "")
	viper.SetDefault("ai_backend.llm_mode", "")
	viper.SetDefault("ai_backend.local_base_url", "http://192.168.0.20:11434/v1")
	viper.SetDefault("ai_backend.local_embedding_model", "bge-m3")
	viper.SetDefault("ai_backend.local_llm_model", "qwen2.5:7b-instruct")
	viper.BindEnv("ai_backend.embedding_mode", "AI_BACKEND_EMBEDDING_MODE")               //nolint:errcheck
	viper.BindEnv("ai_backend.llm_mode", "AI_BACKEND_LLM_MODE")                           //nolint:errcheck
	viper.BindEnv("ai_backend.local_base_url", "AI_BACKEND_LOCAL_BASE_URL")               //nolint:errcheck
	viper.BindEnv("ai_backend.local_embedding_model", "AI_BACKEND_LOCAL_EMBEDDING_MODEL") //nolint:errcheck
	viper.BindEnv("ai_backend.local_llm_model", "AI_BACKEND_LOCAL_LLM_MODEL")             //nolint:errcheck

	viper.BindEnv("metadata_scoring.embedding_enabled", "METADATA_SCORING_EMBEDDING_ENABLED")                               //nolint:errcheck
	viper.BindEnv("metadata_scoring.embedding_min_score", "METADATA_SCORING_EMBEDDING_MIN_SCORE")                           //nolint:errcheck
	viper.BindEnv("metadata_scoring.embedding_best_match", "METADATA_SCORING_EMBEDDING_BEST_MATCH")                         //nolint:errcheck
	viper.BindEnv("metadata_scoring.llm_enabled", "METADATA_SCORING_LLM_ENABLED")                                           //nolint:errcheck
	viper.BindEnv("metadata_scoring.llm_rerank_epsilon", "METADATA_SCORING_LLM_RERANK_EPSILON")                             //nolint:errcheck
	viper.BindEnv("metadata_scoring.llm_rerank_top_k", "METADATA_SCORING_LLM_RERANK_TOP_K")                                 //nolint:errcheck
	viper.BindEnv("metadata_scoring.write_backup_before", "METADATA_SCORING_WRITE_BACKUP_BEFORE")                           //nolint:errcheck
	viper.BindEnv("metadata_scoring.transcription_title_exact_boost", "METADATA_SCORING_TRANSCRIPTION_TITLE_EXACT_BOOST")   //nolint:errcheck
	viper.BindEnv("metadata_scoring.transcription_title_substr_boost", "METADATA_SCORING_TRANSCRIPTION_TITLE_SUBSTR_BOOST") //nolint:errcheck
	viper.BindEnv("metadata_scoring.transcription_author_boost", "METADATA_SCORING_TRANSCRIPTION_AUTHOR_BOOST")             //nolint:errcheck
	viper.BindEnv("metadata_scoring.transcription_narrator_boost", "METADATA_SCORING_TRANSCRIPTION_NARRATOR_BOOST")         //nolint:errcheck
	viper.BindEnv("metadata_scoring.compilation_penalty", "METADATA_SCORING_COMPILATION_PENALTY")                           //nolint:errcheck
	viper.BindEnv("metadata_scoring.rich_metadata_field_bonus", "METADATA_SCORING_RICH_METADATA_FIELD_BONUS")               //nolint:errcheck
	viper.BindEnv("metadata_scoring.rich_metadata_bonus_cap", "METADATA_SCORING_RICH_METADATA_BONUS_CAP")                   //nolint:errcheck
	viper.BindEnv("metadata_scoring.f1_min_score", "METADATA_SCORING_F1_MIN_SCORE")                                         //nolint:errcheck
	viper.BindEnv("metadata_scoring.series_name_match_boost", "METADATA_SCORING_SERIES_NAME_MATCH_BOOST")                   //nolint:errcheck
	viper.BindEnv("metadata_scoring.series_number_exact_boost", "METADATA_SCORING_SERIES_NUMBER_EXACT_BOOST")               //nolint:errcheck
	viper.BindEnv("metadata_scoring.series_number_wrong_penalty", "METADATA_SCORING_SERIES_NUMBER_WRONG_PENALTY")           //nolint:errcheck
	viper.BindEnv("metadata_scoring.bulk_fetch_workers", "METADATA_SCORING_BULK_FETCH_WORKERS")                             //nolint:errcheck

	// Unified dedup scoring defaults (SPEC 1 §3–4, T011).
	// These are consumed by internal/dedup/unified.LoadScoreConfig via Viper.
	// Overridable per-kind in config.yaml under dedup.signals.<kind>.*
	viper.SetDefault("dedup.signals.band_certain_min", 97.0)
	viper.SetDefault("dedup.signals.band_high_min", 90.0)
	viper.SetDefault("dedup.signals.band_medium_min", 75.0)
	viper.SetDefault("dedup.signals.band_review_min", 60.0)
	// Per-kind boost defaults for supporting signals.
	viper.SetDefault("dedup.signals.duration.boost", 4.0)
	viper.SetDefault("dedup.signals.folder_path.boost", 3.0)

	viper.SetDefault("supported_extensions", []string{
		".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma",
		".opus", ".oga", ".wav", ".aiff", ".aif", ".mka", ".aax", ".aaxc",
	})
	viper.SetDefault("exclude_patterns", []string{})

	supportedExtensions := []string{
		".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma",
		".opus", ".oga", ".wav", ".aiff", ".aif", ".mka", ".aax", ".aaxc",
	}
	if viper.IsSet("supported_extensions") {
		supportedExtensions = viper.GetStringSlice("supported_extensions")
	}
	excludePatterns := viper.GetStringSlice("exclude_patterns")

	// WHY Mutate: whole-struct init; correct even if tests call InitConfig concurrently.
	Mutate(func(c *Config) {
		*c = Config{
			// Core paths
			RootDir:       viper.GetString("root_dir"),
			DatabasePath:  viper.GetString("database_path"),
			DatabaseType:  viper.GetString("database_type"),
			EnableSQLite:  viper.GetBool("enable_sqlite3_i_know_the_risks"),
			PlaylistDir:   viper.GetString("playlist_dir"),
			SetupComplete: viper.GetBool("setup_complete"),

			// Library organization
			OrganizationStrategy:    viper.GetString("organization_strategy"),
			ScanOnStartup:           viper.GetBool("scan_on_startup"),
			AutoOrganize:            viper.GetBool("auto_organize"),
			AutoScanEnabled:         viper.GetBool("auto_scan_enabled"),
			AutoScanDebounceSeconds: viper.GetInt("auto_scan_debounce_seconds"),
			FolderNamingPattern:     viper.GetString("folder_naming_pattern"),
			FileNamingPattern:       viper.GetString("file_naming_pattern"),
			CreateBackups:           viper.GetBool("create_backups"),

			// Storage quotas
			EnableDiskQuota:    viper.GetBool("enable_disk_quota"),
			DiskQuotaPercent:   viper.GetInt("disk_quota_percent"),
			EnableUserQuotas:   viper.GetBool("enable_user_quotas"),
			DefaultUserQuotaGB: viper.GetInt("default_user_quota_gb"),

			// Metadata
			AutoFetchMetadata: viper.GetBool("auto_fetch_metadata"),
			WriteBackMetadata: viper.GetBool("write_back_metadata"),
			EmbedCoverArt:     viper.GetBool("embed_cover_art"),
			Language:          viper.GetString("language"),

			// Open Library dumps
			OpenLibraryDumpEnabled: viper.GetBool("openlibrary_dump_enabled"),
			OpenLibraryDumpDir:     viper.GetString("openlibrary_dump_dir"),

			// Hardcover.app
			HardcoverAPIToken: viper.GetString("hardcover_api_token"),

			// Google Books
			GoogleBooksAPIKey: viper.GetString("google_books_api_key"),

			// AI parsing
			EnableAIParsing:     viper.GetBool("enable_ai_parsing"),
			OpenAIAPIKey:        viper.GetString("openai_api_key"),
			AcoustIDAPIKey:      viper.GetString("acoustid_api_key"),
			MetadataReviewModel: viper.GetString("metadata_review_model"),
			FilenameParseModel:  viper.GetString("filename_parse_model"),
			CoverArtModel:       viper.GetString("cover_art_model"),

			// Performance
			ConcurrentScans:                  viper.GetInt("concurrent_scans"),
			ChapterConsolidationThresholdMin: viper.GetInt("chapter_consolidation_threshold_min"),
			CoalesceShatteredSiblings:        viper.GetBool("coalesce_shattered_siblings"),
			OperationTimeoutMinutes:          viper.GetInt("operation_timeout_minutes"),
			MinBookSizeBytes:                 viper.GetInt64("min_book_size_bytes"),
			APIRateLimitPerMinute:            viper.GetInt("api_rate_limit_per_minute"),
			AuthRateLimitPerMinute:           viper.GetInt("auth_rate_limit_per_minute"),
			JSONBodyLimitMB:                  viper.GetInt("json_body_limit_mb"),
			UploadBodyLimitMB:                viper.GetInt("upload_body_limit_mb"),
			EnableAuth:                       viper.GetBool("enable_auth"),
			EnableRateLimit:                  viper.GetBool("enable_rate_limit"),
			ReviewApplyEnabled:               viper.GetBool("review_apply_enabled"),
			BasicAuthEnabled:                 viper.GetBool("basic_auth_enabled"),
			BasicAuthUsername:                viper.GetString("basic_auth_username"),
			BasicAuthPassword:                viper.GetString("basic_auth_password"),

			// OAuth / Cloudflare Access. These are environment-authoritative in prod
			// (systemd Environment= drop-in) but read here via viper like everything
			// else — the BindEnv bindings above make viper.GetX honor the env. The DB
			// config-blob load later overwrites this whole struct, so the env values are
			// re-applied on top of the blob by applyEnvAuthoritativeConfig (persistence.go).
			OAuthEnabled:            viper.GetBool("oauth_enabled"),
			OAuthGithubClientID:     viper.GetString("oauth_github_client_id"),
			OAuthGithubClientSecret: viper.GetString("oauth_github_client_secret"),
			OAuthGoogleClientID:     viper.GetString("oauth_google_client_id"),
			OAuthGoogleClientSecret: viper.GetString("oauth_google_client_secret"),
			OAuthRedirectBaseURL:    viper.GetString("oauth_redirect_base_url"),
			OAuthAllowedEmails:      viper.GetString("oauth_allowed_emails"),
			OAuthDefaultRole:        viper.GetString("oauth_default_role"),
			CFAccessTeamDomain:      viper.GetString("cf_access_team_domain"),
			CFAccessAUD:             viper.GetString("cf_access_aud"),
			WhisperRemoteURL:        viper.GetString("whisper_remote_url"),
			WhisperEndpoints:        ParseWhisperEndpoints(viper.GetString("whisper_endpoints")),

			// Audiobookshelf sync API (feature-flagged OFF by default).
			ABSAPIEnabled:       viper.GetBool("abs_api_enabled"),
			ABSAuthModes:        viper.GetString("abs_auth_modes"),
			ABSJWTSecret:        viper.GetString("abs_jwt_secret"),
			ABSAccessTokenTTL:   viper.GetString("abs_access_token_ttl"),
			ABSRefreshTokenTTL:  viper.GetString("abs_refresh_token_ttl"),
			ABSRefreshGrace:     viper.GetString("abs_refresh_grace"),
			ABSServerVersion:    viper.GetString("abs_server_version"),
			ABSDefaultLibraryID: viper.GetString("abs_default_library_id"),

			// Memory management
			MemoryLimitType:           viper.GetString("memory_limit_type"),
			CacheSize:                 viper.GetInt("cache_size"),
			MetadataFetchCacheTTLDays: viper.GetInt("metadata_fetch_cache_ttl_days"),
			BootstrapKeyTTLDays:       viper.GetInt("bootstrap_key_ttl_days"),
			MemoryLimitPercent:        viper.GetInt("memory_limit_percent"),
			MemoryLimitMB:             viper.GetInt("memory_limit_mb"),

			// Lifecycle / retention
			PurgeSoftDeletedAfterDays:   viper.GetInt("purge_soft_deleted_after_days"),
			PurgeSoftDeletedDeleteFiles: viper.GetBool("purge_soft_deleted_delete_files"),

			// Logging
			LogLevel:          viper.GetString("log_level"),
			LogFormat:         viper.GetString("log_format"),
			EnableJsonLogging: viper.GetBool("enable_json_logging"),

			// Auto-update
			AutoUpdate: AutoUpdateConfig{
				Enabled:      viper.GetBool("auto_update.enabled"),
				Channel:      viper.GetString("auto_update.channel"),
				CheckMinutes: viper.GetInt("auto_update.check_minutes"),
				WindowStart:  viper.GetInt("auto_update.window_start"),
				WindowEnd:    viper.GetInt("auto_update.window_end"),
			},

			// Maintenance window (nested sub-struct)
			Maintenance: MaintenanceConfig{
				Enabled:              viper.GetBool("maintenance.enabled"),
				WindowStart:          viper.GetInt("maintenance.window_start"),
				WindowEnd:            viper.GetInt("maintenance.window_end"),
				DedupRefresh:         viper.GetBool("maintenance.dedup_refresh"),
				SeriesPrune:          viper.GetBool("maintenance.series_prune"),
				AuthorSplit:          viper.GetBool("maintenance.author_split"),
				TombstoneCleanup:     viper.GetBool("maintenance.tombstone_cleanup"),
				Reconcile:            viper.GetBool("maintenance.reconcile"),
				PurgeDeleted:         viper.GetBool("maintenance.purge_deleted"),
				PurgeOldLogs:         viper.GetBool("maintenance.purge_old_logs"),
				DbOptimize:           viper.GetBool("maintenance.db_optimize"),
				LibraryScan:          viper.GetBool("maintenance.library_scan"),
				LibraryOrganize:      viper.GetBool("maintenance.library_organize"),
				MetadataRefresh:      viper.GetBool("maintenance.metadata_refresh"),
				LibrarySizeRefresh:   viper.GetBool("maintenance.library_size_refresh"),
				AcoustIDOnlineLookup: viper.GetBool("maintenance.acoustid_online_lookup"),
				AcoustIDNightlyLimit: viper.GetInt("maintenance.acoustid_nightly_limit"),
			},

			// iTunes sync (nested sub-struct)
			ITunes: ITunesConfig{
				SyncEnabled:      viper.GetBool("itunes.sync_enabled"),
				SyncInterval:     viper.GetInt("itunes.sync_interval"),
				WriteBackEnabled: viper.GetBool("itunes.write_back_enabled"),
				LibraryWritePath: viper.GetString("itunes.library_write_path"),
				LibraryReadPath:  viper.GetString("itunes.library_read_path"),
				AutoWriteBack:    viper.GetBool("itunes.auto_write_back"),
				PathTrimEnabled:  viper.GetBool("itunes.path_trim_enabled"),
				WindowsRootPath:  viper.GetString("itunes.windows_root_path"),
				MediaRoot:        viper.GetString("itunes.media_root"),
				// PathMappings loaded from DB blob, not viper
				Libraries: LibrarySet{
					Original: LibraryRef{
						ITLPath: viper.GetString("itunes.libraries.original.itl_path"),
						XMLPath: viper.GetString("itunes.libraries.original.xml_path"),
						Frozen:  viper.GetBool("itunes.libraries.original.frozen"),
					},
					AO: LibraryRef{
						ITLPath: viper.GetString("itunes.libraries.ao.itl_path"),
						XMLPath: viper.GetString("itunes.libraries.ao.xml_path"),
						Frozen:  viper.GetBool("itunes.libraries.ao.frozen"),
					},
					PointedAt:    viper.GetString("itunes.libraries.pointed_at"),
					ImportSource: viper.GetString("itunes.libraries.import_source"),
				},
			},

			// Download client integration
			DownloadClient: DownloadClientConfig{
				Torrent: TorrentClientConfig{
					Type: viper.GetString("download_client.torrent.type"),
					Deluge: DelugeConfig{
						Host:     viper.GetString("download_client.torrent.deluge.host"),
						Port:     viper.GetInt("download_client.torrent.deluge.port"),
						Username: viper.GetString("download_client.torrent.deluge.username"),
						Password: viper.GetString("download_client.torrent.deluge.password"),
					},
					QBittorrent: QBittorrentConfig{
						Host:     viper.GetString("download_client.torrent.qbittorrent.host"),
						Port:     viper.GetInt("download_client.torrent.qbittorrent.port"),
						Username: viper.GetString("download_client.torrent.qbittorrent.username"),
						Password: viper.GetString("download_client.torrent.qbittorrent.password"),
						UseHTTPS: viper.GetBool("download_client.torrent.qbittorrent.use_https"),
					},
				},
				Usenet: UsenetClientConfig{
					Type: viper.GetString("download_client.usenet.type"),
					SABnzbd: SABnzbdConfig{
						Host:     viper.GetString("download_client.usenet.sabnzbd.host"),
						Port:     viper.GetInt("download_client.usenet.sabnzbd.port"),
						APIKey:   viper.GetString("download_client.usenet.sabnzbd.api_key"),
						UseHTTPS: viper.GetBool("download_client.usenet.sabnzbd.use_https"),
					},
				},
			},

			// Path formatting & apply pipeline
			PathFormat:              viper.GetString("path_format"),
			SegmentTitleFormat:      viper.GetString("segment_title_format"),
			AutoRenameOnApply:       viper.GetBool("auto_rename_on_apply"),
			AutoWriteTagsOnApply:    viper.GetBool("auto_write_tags_on_apply"),
			VerifyAfterWrite:        viper.GetBool("verify_after_write"),
			WriteStartupReadOnlyKey: viper.GetBool("write_startup_readonly_key"),

			SupportedExtensions: supportedExtensions,
			ExcludePatterns:     excludePatterns,

			// Embedding pipeline (nested sub-struct)
			Embedding: EmbeddingConfig{
				Enabled:       viper.GetBool("embedding.enabled"),
				Model:         viper.GetString("embedding.model"),
				Dimensions:    viper.GetInt("embedding.dimensions"),
				BaseURL:       viper.GetString("embedding.base_url"),
				VectorBackend: viper.GetString("embedding.vector_backend"),
			},

			// Dedup thresholds + behaviour (nested sub-struct)
			Dedup: DedupConfig{
				BookHighThreshold:          viper.GetFloat64("dedup.book_high_threshold"),
				BookLowThreshold:           viper.GetFloat64("dedup.book_low_threshold"),
				AuthorHighThreshold:        viper.GetFloat64("dedup.author_high_threshold"),
				AuthorLowThreshold:         viper.GetFloat64("dedup.author_low_threshold"),
				AutoMergeEnabled:           viper.GetBool("dedup.auto_merge_enabled"),
				EmbeddingsEnabled:          viper.GetBool("dedup.embeddings_enabled"),
				LLMAutoMergeHighConfidence: viper.GetBool("dedup.llm_auto_merge_high_confidence"),
				AutoResolveEnabled:         viper.GetBool("dedup.auto_resolve_enabled"),
				OnImportViaScheduler:       viper.GetBool("dedup.on_import_via_scheduler"),
				ReviewModel:                viper.GetString("dedup.review_model"),
				Signals: DedupSignalConfig{
					BandCertainMin: viper.GetFloat64("dedup.signals.band_certain_min"),
					BandHighMin:    viper.GetFloat64("dedup.signals.band_high_min"),
					BandMediumMin:  viper.GetFloat64("dedup.signals.band_medium_min"),
					BandReviewMin:  viper.GetFloat64("dedup.signals.band_review_min"),
				},
			},

			// Dedup boilerplate-blocklist extras (nested sub-struct, INIT-4 T5).
			// Empty by default — see DedupBoilerplateConfig.
			DedupBoilerplate: DedupBoilerplateConfig{
				ExtraTitlePatterns:  viper.GetStringSlice("dedup_boilerplate.extra_title_patterns"),
				ExtraPrefixPatterns: viper.GetStringSlice("dedup_boilerplate.extra_prefix_patterns"),
			},

			// Metadata candidate scoring + tag-write backup policy (nested sub-struct)
			MetadataScoring: MetadataScoringConfig{
				EmbeddingEnabled:   viper.GetBool("metadata_scoring.embedding_enabled"),
				EmbeddingMinScore:  viper.GetFloat64("metadata_scoring.embedding_min_score"),
				EmbeddingBestMatch: viper.GetFloat64("metadata_scoring.embedding_best_match"),
				LLMEnabled:         viper.GetBool("metadata_scoring.llm_enabled"),
				LLMRerankEpsilon:   viper.GetFloat64("metadata_scoring.llm_rerank_epsilon"),
				LLMRerankTopK:      viper.GetInt("metadata_scoring.llm_rerank_top_k"),
				WriteBackupBefore:  viper.GetBool("metadata_scoring.write_backup_before"),

				TranscriptionTitleExactBoost:  viper.GetFloat64("metadata_scoring.transcription_title_exact_boost"),
				TranscriptionTitleSubstrBoost: viper.GetFloat64("metadata_scoring.transcription_title_substr_boost"),
				TranscriptionAuthorBoost:      viper.GetFloat64("metadata_scoring.transcription_author_boost"),
				TranscriptionNarratorBoost:    viper.GetFloat64("metadata_scoring.transcription_narrator_boost"),

				CompilationPenalty:     f64Ptr(viper.GetFloat64("metadata_scoring.compilation_penalty")),
				RichMetadataFieldBonus: viper.GetFloat64("metadata_scoring.rich_metadata_field_bonus"),
				RichMetadataBonusCap:   f64Ptr(viper.GetFloat64("metadata_scoring.rich_metadata_bonus_cap")),
				F1MinScore:             f64Ptr(viper.GetFloat64("metadata_scoring.f1_min_score")),

				SeriesNameMatchBoost:     viper.GetFloat64("metadata_scoring.series_name_match_boost"),
				SeriesNumberExactBoost:   viper.GetFloat64("metadata_scoring.series_number_exact_boost"),
				SeriesNumberWrongPenalty: viper.GetFloat64("metadata_scoring.series_number_wrong_penalty"),

				DurationTierMultipliers: getFloat64Slice("metadata_scoring.duration_tier_multipliers"),
				DurationTierScores:      getFloat64Slice("metadata_scoring.duration_tier_scores"),

				BulkFetchWorkers: viper.GetInt("metadata_scoring.bulk_fetch_workers"),
			},

			// AI backend-mode toggle (nested sub-struct). Modes default empty
			// (resolved by EffectiveEmbeddingMode / EffectiveLLMMode); local
			// endpoint coordinates carry Ollama-style defaults.
			AIBackend: AIBackendConfig{
				EmbeddingMode:       viper.GetString("ai_backend.embedding_mode"),
				LLMMode:             viper.GetString("ai_backend.llm_mode"),
				LocalBaseURL:        viper.GetString("ai_backend.local_base_url"),
				LocalEmbeddingModel: viper.GetString("ai_backend.local_embedding_model"),
				LocalLLMModel:       viper.GetString("ai_backend.local_llm_model"),
			},

			// Scheduled background tasks (nested sub-struct)
			Scheduled: ScheduledTasksConfig{
				DedupRefresh: ScheduledTaskConfig{
					Enabled:   viper.GetBool("scheduled.dedup_refresh.enabled"),
					Interval:  viper.GetInt("scheduled.dedup_refresh.interval"),
					OnStartup: viper.GetBool("scheduled.dedup_refresh.on_startup"),
				},
				LabelRefinement: ScheduledTaskConfig{
					Enabled:   viper.GetBool("scheduled.label_refinement.enabled"),
					Interval:  viper.GetInt("scheduled.label_refinement.interval"),
					OnStartup: viper.GetBool("scheduled.label_refinement.on_startup"),
				},
				AuthorSplit: ScheduledTaskConfig{
					Enabled:   viper.GetBool("scheduled.author_split.enabled"),
					Interval:  viper.GetInt("scheduled.author_split.interval"),
					OnStartup: viper.GetBool("scheduled.author_split.on_startup"),
				},
				DbOptimize: ScheduledTaskConfig{
					Enabled:   viper.GetBool("scheduled.db_optimize.enabled"),
					Interval:  viper.GetInt("scheduled.db_optimize.interval"),
					OnStartup: viper.GetBool("scheduled.db_optimize.on_startup"),
				},
				MetadataRefresh: ScheduledTaskConfig{
					Enabled:   viper.GetBool("scheduled.metadata_refresh.enabled"),
					Interval:  viper.GetInt("scheduled.metadata_refresh.interval"),
					OnStartup: viper.GetBool("scheduled.metadata_refresh.on_startup"),
				},
				ResolveProductionAuthors: ScheduledTaskConfig{
					Enabled:  viper.GetBool("scheduled.resolve_production_authors.enabled"),
					Interval: viper.GetInt("scheduled.resolve_production_authors.interval"),
				},
				SeriesPrune: ScheduledTaskConfig{
					Enabled:   viper.GetBool("scheduled.series_prune.enabled"),
					Interval:  viper.GetInt("scheduled.series_prune.interval"),
					OnStartup: viper.GetBool("scheduled.series_prune.on_startup"),
				},
				AIDedupBatch: ScheduledTaskConfig{
					Enabled:   viper.GetBool("scheduled.ai_dedup_batch.enabled"),
					Interval:  viper.GetInt("scheduled.ai_dedup_batch.interval"),
					OnStartup: viper.GetBool("scheduled.ai_dedup_batch.on_startup"),
				},
				Reconcile: ScheduledTaskConfig{
					Enabled:   viper.GetBool("scheduled.reconcile.enabled"),
					Interval:  viper.GetInt("scheduled.reconcile.interval"),
					OnStartup: viper.GetBool("scheduled.reconcile.on_startup"),
				},
			},
		}

		// Managed external-tool lifecycle
		c.Tools = tools.ToolsConfig{
			ManagedDir:          "/var/lib/audiobook-organizer/tools",
			Ollama:              tools.ToolConfig{Mode: tools.ToolModeSystem},
			Fpcalc:              tools.ToolConfig{Mode: tools.ToolModeSystem},
			AllowPeriodicOllama: false,
			OllamaDebounceMin:   10,
		}

		// Default Open Library dump dir to {RootDir}/openlibrary-dumps if not set
		if c.OpenLibraryDumpDir == "" && c.RootDir != "" {
			c.OpenLibraryDumpDir = filepath.Join(c.RootDir, "openlibrary-dumps")
		}

		// API Keys (Goodreads deprecated Dec 2020, removed)

		// Load metadata sources from config or use defaults
		if viper.IsSet("metadata_sources") {
			viper.UnmarshalKey("metadata_sources", &c.MetadataSources)
		} else {
			// Set default metadata sources
			c.MetadataSources = []MetadataSource{
				{
					ID:           "audible",
					Name:         "Audible",
					Enabled:      true,
					Priority:     1,
					RequiresAuth: false,
					Credentials:  make(map[string]string),
				},
				{
					ID:           "openlibrary",
					Name:         "Open Library",
					Enabled:      true,
					Priority:     2,
					RequiresAuth: false,
					Credentials:  make(map[string]string),
				},
				{
					ID:           "audnexus",
					Name:         "Audnexus",
					Enabled:      true,
					Priority:     3,
					RequiresAuth: false,
					Credentials:  make(map[string]string),
				},
				{
					ID:           "google-books",
					Name:         "Google Books",
					Enabled:      false,
					Priority:     4,
					RequiresAuth: true,
					Credentials: map[string]string{
						"apiKey": "",
					},
				},
				{
					ID:           "hardcover",
					Name:         "Hardcover",
					Enabled:      false,
					Priority:     5,
					RequiresAuth: true,
					Credentials:  make(map[string]string),
				},
				{
					ID:           "wikipedia",
					Name:         "Wikipedia",
					Enabled:      false, // Disabled by default — Wikipedia API returns 403
					Priority:     6,
					RequiresAuth: false,
					Credentials:  make(map[string]string),
				},
			}
		}

		// Backward compatibility: map old flat viper key names to the nested struct.
		// These keys were set directly (e.g. via viper.Set in tests or old config files)
		// using the pre-Wave-4 flat names, which are no longer read in the struct literal.
		if c.ITunes.LibraryWritePath == "" {
			if v := viper.GetString("itunes_library_write_path"); v != "" {
				c.ITunes.LibraryWritePath = v
			}
		}
		if c.ITunes.LibraryWritePath == "" {
			c.ITunes.LibraryWritePath = viper.GetString("itunes_library_itl_path")
		}
		if c.ITunes.LibraryReadPath == "" {
			if v := viper.GetString("itunes_library_read_path"); v != "" {
				c.ITunes.LibraryReadPath = v
			}
		}
		if c.ITunes.LibraryReadPath == "" {
			c.ITunes.LibraryReadPath = viper.GetString("itunes_library_xml_path")
		}
		// Also pick up the flat write_back and sync keys if set via old viper keys
		if !c.ITunes.WriteBackEnabled {
			if viper.IsSet("itl_write_back_enabled") {
				c.ITunes.WriteBackEnabled = viper.GetBool("itl_write_back_enabled")
			}
		}

		// Auto-enable ITL write-back when a write path is configured
		if c.ITunes.LibraryWritePath != "" && !c.ITunes.WriteBackEnabled {
			c.ITunes.WriteBackEnabled = true
		}

		// Normalize database type
		if c.DatabaseType == "sqlite3" {
			c.DatabaseType = "sqlite"
		}
		if c.DatabaseType == "" {
			c.DatabaseType = "pebble"
		}
	}) // end Mutate
}

var validPatternPlaceholder = regexp.MustCompile(`\{[A-Za-z0-9_]+\}`)

func hasBalancedBraces(value string) bool {
	return strings.Count(value, "{") == strings.Count(value, "}")
}

func validateNamingPattern(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("pattern cannot be empty")
	}
	if !hasBalancedBraces(trimmed) {
		return fmt.Errorf("unbalanced braces in pattern")
	}
	withoutPlaceholders := validPatternPlaceholder.ReplaceAllString(trimmed, "")
	if strings.Contains(withoutPlaceholders, "{") || strings.Contains(withoutPlaceholders, "}") {
		return fmt.Errorf("invalid placeholder format in pattern")
	}
	return nil
}

func validateParentDirExists(path string, field string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("%s parent directory %q does not exist", field, parent)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s parent path %q is not a directory", field, parent)
	}
	return nil
}

func validateParentDirWritable(path string, field string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	parent := filepath.Dir(path)
	testFile, err := os.CreateTemp(parent, ".ao-write-test-*")
	if err != nil {
		return fmt.Errorf("%s parent directory %q is not writable", field, parent)
	}
	testFile.Close()
	_ = os.Remove(testFile.Name())
	return nil
}

// Validate performs structural checks on runtime configuration values.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}

	var errs []string

	// iTunes 4-state model: derive the legacy path shims, then run the fail-closed
	// library assertions (inert unless itunes.libraries is populated). See
	// itunes_libraries.go / spec §2.
	c.ITunes.Resolve()
	errs = append(errs, c.ITunes.ValidateLibraries(c.ProtectedPaths)...)

	switch c.DatabaseType {
	case "pebble", "sqlite":
	default:
		errs = append(errs, "database_type must be 'pebble' or 'sqlite'")
	}

	if err := validateParentDirExists(c.DatabasePath, "database_path"); err != nil {
		errs = append(errs, err.Error())
	} else if err := validateParentDirWritable(c.DatabasePath, "database_path"); err != nil {
		errs = append(errs, err.Error())
	}

	if err := validateParentDirExists(c.PlaylistDir, "playlist_dir"); err != nil {
		errs = append(errs, err.Error())
	}

	if c.ConcurrentScans < 0 {
		errs = append(errs, "concurrent_scans must be >= 0")
	}
	if c.MinBookSizeBytes == 0 {
		c.MinBookSizeBytes = 5 * 1024 * 1024
	}
	if c.AutoScanDebounceSeconds < 0 {
		errs = append(errs, "auto_scan_debounce_seconds must be >= 0")
	}
	if c.OperationTimeoutMinutes < 0 {
		errs = append(errs, "operation_timeout_minutes must be >= 0")
	}
	if c.APIRateLimitPerMinute < 0 {
		errs = append(errs, "api_rate_limit_per_minute must be >= 0")
	}
	if c.AuthRateLimitPerMinute < 0 {
		errs = append(errs, "auth_rate_limit_per_minute must be >= 0")
	}
	if c.JSONBodyLimitMB < 0 {
		errs = append(errs, "json_body_limit_mb must be >= 0")
	}
	if c.UploadBodyLimitMB < 0 {
		errs = append(errs, "upload_body_limit_mb must be >= 0")
	}
	if c.EnableDiskQuota && (c.DiskQuotaPercent < 1 || c.DiskQuotaPercent > 100) {
		errs = append(errs, "disk_quota_percent must be between 1 and 100")
	}

	validStrategies := map[string]struct{}{
		"auto": {}, "copy": {}, "hardlink": {}, "reflink": {}, "symlink": {},
	}
	if c.OrganizationStrategy != "" {
		if _, ok := validStrategies[c.OrganizationStrategy]; !ok {
			errs = append(errs, "organization_strategy must be one of: auto, copy, hardlink, reflink, symlink")
		}
	}

	if strings.TrimSpace(c.FolderNamingPattern) != "" {
		if err := validateNamingPattern(c.FolderNamingPattern); err != nil {
			errs = append(errs, "folder_naming_pattern "+err.Error())
		}
	}
	if strings.TrimSpace(c.FileNamingPattern) != "" {
		if err := validateNamingPattern(c.FileNamingPattern); err != nil {
			errs = append(errs, "file_naming_pattern "+err.Error())
		}
	}

	for _, ext := range c.SupportedExtensions {
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			errs = append(errs, fmt.Sprintf("supported extension %q must start with '.'", ext))
			break
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ResetToDefaults resets the AppConfig to factory defaults.
// WHY Mutate: whole-struct reset; concurrent readers must not see a torn state.
func ResetToDefaults() {
	// Snapshot current paths before acquiring the write lock to avoid a
	// deadlock (Mutate is not re-entrant).
	cur := Snapshot()
	Mutate(func(c *Config) {
		*c = Config{
			// Core paths — preserve existing paths; reset everything else
			RootDir:       cur.RootDir,
			DatabasePath:  cur.DatabasePath,
			DatabaseType:  "pebble",
			EnableSQLite:  false,
			PlaylistDir:   cur.PlaylistDir,
			SetupComplete: false,

			// Library organization
			OrganizationStrategy:    "auto",
			ScanOnStartup:           false,
			AutoOrganize:            true,
			AutoScanEnabled:         false,
			AutoScanDebounceSeconds: 30,
			FolderNamingPattern:     "{author}/{series}/{title} ({print_year})",
			FileNamingPattern:       "{title} - {author} - read by {narrator}",
			CreateBackups:           true,

			// Storage quotas
			EnableDiskQuota:    false,
			DiskQuotaPercent:   80,
			EnableUserQuotas:   false,
			DefaultUserQuotaGB: 100,

			// Metadata
			AutoFetchMetadata: true,
			EmbedCoverArt:     false,
			Language:          "en",

			// Open Library dumps
			OpenLibraryDumpEnabled: false,
			OpenLibraryDumpDir:     "",

			// AI parsing
			EnableAIParsing:     true,
			OpenAIAPIKey:        "",
			AcoustIDAPIKey:      "",
			MetadataReviewModel: "gpt-5-mini",
			FilenameParseModel:  "gpt-5-mini",
			CoverArtModel:       "gpt-5-mini",

			// Performance
			ConcurrentScans:         max(runtime.NumCPU(), 4),
			OperationTimeoutMinutes: 30,
			MinBookSizeBytes:        5 * 1024 * 1024,
			APIRateLimitPerMinute:   100,
			AuthRateLimitPerMinute:  10,
			JSONBodyLimitMB:         1,
			UploadBodyLimitMB:       10,
			EnableAuth:              true,
			EnableRateLimit:         true,
			ReviewApplyEnabled:      false, // OFF by default — review-only until explicitly enabled
			BasicAuthEnabled:        false,
			BasicAuthUsername:       "",
			BasicAuthPassword:       "",

			// Memory management
			MemoryLimitType:    "items",
			CacheSize:          1000,
			MemoryLimitPercent: 25,
			MemoryLimitMB:      512,

			// Lifecycle / retention
			PurgeSoftDeletedAfterDays:      30,
			PurgeSoftDeletedDeleteFiles:    false,
			ActivityLogRetentionChangeDays: 90,
			ActivityLogRetentionDebugDays:  30,
			ActivityLogCompactionDays:      14,

			// Embedding pipeline
			Embedding: EmbeddingConfig{
				Enabled:       true,
				Model:         "text-embedding-3-large",
				Dimensions:    3072,
				BaseURL:       "",
				VectorBackend: "chromem",
			},

			// Dedup thresholds + behaviour (nested sub-struct)
			Dedup: DedupConfig{
				BookHighThreshold:          0.95,
				BookLowThreshold:           0.85,
				AuthorHighThreshold:        0.92,
				AuthorLowThreshold:         0.80,
				AutoMergeEnabled:           true,
				EmbeddingsEnabled:          true, // opt-out: set false on no-internet boxes
				LLMAutoMergeHighConfidence: false,
				AutoResolveEnabled:         false, // owner-greenlight kill switch; never defaulted true
				OnImportViaScheduler:       false, // opt-in
				ReviewModel:                "gpt-5-mini",
				Signals: DedupSignalConfig{
					BandCertainMin: 97.0,
					BandHighMin:    90.0,
					BandMediumMin:  75.0,
					BandReviewMin:  60.0,
				},
			},

			// Dedup boilerplate-blocklist extras (nested sub-struct, INIT-4 T5).
			// Empty by default — the compiled-in blocklist in
			// internal/dedup/boilerplate.go is always active regardless.
			DedupBoilerplate: DedupBoilerplateConfig{
				ExtraTitlePatterns:  nil,
				ExtraPrefixPatterns: nil,
			},

			// Metadata candidate scoring + tag-write backup policy (nested sub-struct)
			MetadataScoring: MetadataScoringConfig{
				EmbeddingEnabled:   false,
				EmbeddingMinScore:  0.82,
				EmbeddingBestMatch: 0.88,
				LLMEnabled:         false,
				LLMRerankEpsilon:   0.05,
				LLMRerankTopK:      5,
				WriteBackupBefore:  true,

				TranscriptionTitleExactBoost:  2.0,
				TranscriptionTitleSubstrBoost: 1.4,
				TranscriptionAuthorBoost:      1.6,
				TranscriptionNarratorBoost:    1.4,

				CompilationPenalty:     f64Ptr(0.15),
				RichMetadataFieldBonus: 0.05,
				RichMetadataBonusCap:   f64Ptr(0.15),
				F1MinScore:             f64Ptr(0.35),

				SeriesNameMatchBoost:     1.4,
				SeriesNumberExactBoost:   2.0,
				SeriesNumberWrongPenalty: 0.5,

				DurationTierMultipliers: []float64{1.30, 1.20, 1.10, 1.00, 0.75, 0.50},
				DurationTierScores:      []float64{20, 15, 10, 0, -10, -20},

				BulkFetchWorkers: 4,
			},

			// AI backend-mode toggle. Modes empty at rest (derived from legacy
			// fields by EffectiveEmbeddingMode / EffectiveLLMMode); local
			// endpoint coordinates carry Ollama defaults. LocalBaseURL uses a
			// placeholder host — real endpoints live in gitignored local config.
			AIBackend: AIBackendConfig{
				EmbeddingMode:       "",
				LLMMode:             "",
				LocalBaseURL:        "http://192.168.0.20:11434/v1",
				LocalEmbeddingModel: "bge-m3",
				LocalLLMModel:       "qwen2.5:7b-instruct",
			},

			// Logging
			LogLevel:          "info",
			LogFormat:         "text",
			EnableJsonLogging: false,

			// Auto-update
			AutoUpdate: AutoUpdateConfig{
				Enabled:      false,
				Channel:      "stable",
				CheckMinutes: 60,
				WindowStart:  2,
				WindowEnd:    5,
			},

			// Maintenance window
			Maintenance: MaintenanceConfig{
				Enabled:            true,
				WindowStart:        1,
				WindowEnd:          4,
				DedupRefresh:       true,
				SeriesPrune:        true,
				AuthorSplit:        true,
				TombstoneCleanup:   true,
				Reconcile:          true,
				PurgeDeleted:       true,
				PurgeOldLogs:       true,
				DbOptimize:         true,
				LibraryScan:        false,
				LibraryOrganize:    false,
				MetadataRefresh:    false,
				LibrarySizeRefresh: true,
				// AcoustID online lookup is OFF by default — uses third-party
				// quota and only helps users who set ACOUSTID_API_KEY. Opt-in
				// via setting + env key.
				AcoustIDOnlineLookup: false,
				AcoustIDNightlyLimit: 5000,
			},

			// iTunes sync (nested sub-struct)
			ITunes: ITunesConfig{
				SyncEnabled:  true,
				SyncInterval: 30,
			},

			// Download client integration
			DownloadClient: DownloadClientConfig{
				Torrent: TorrentClientConfig{
					Type: "",
					Deluge: DelugeConfig{
						Host:     "",
						Port:     0,
						Username: "",
						Password: "",
					},
					QBittorrent: QBittorrentConfig{
						Host:     "",
						Port:     0,
						Username: "",
						Password: "",
						UseHTTPS: false,
					},
				},
				Usenet: UsenetClientConfig{
					Type: "",
					SABnzbd: SABnzbdConfig{
						Host:     "",
						Port:     0,
						APIKey:   "",
						UseHTTPS: false,
					},
				},
			},

			// Path formatting & apply pipeline
			PathFormat:              "{author}/{series_prefix}{title}/{track_title}.{ext}",
			SegmentTitleFormat:      "{title} - {track}/{total_tracks}",
			AutoRenameOnApply:       true,
			AutoWriteTagsOnApply:    true,
			VerifyAfterWrite:        true,
			WriteStartupReadOnlyKey: true,

			SupportedExtensions: []string{
				".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma",
				".opus", ".oga", ".wav", ".aiff", ".aif", ".mka", ".aax", ".aaxc",
			},
			ExcludePatterns: []string{},

			// Default metadata sources
			MetadataSources: []MetadataSource{
				{
					ID:           "audible",
					Name:         "Audible",
					Enabled:      true,
					Priority:     1,
					RequiresAuth: false,
					Credentials:  make(map[string]string),
				},
				{
					ID:           "openlibrary",
					Name:         "Open Library",
					Enabled:      true,
					Priority:     2,
					RequiresAuth: false,
					Credentials:  make(map[string]string),
				},
				{
					ID:           "audnexus",
					Name:         "Audnexus",
					Enabled:      true,
					Priority:     3,
					RequiresAuth: false,
					Credentials:  make(map[string]string),
				},
				{
					ID:           "google-books",
					Name:         "Google Books",
					Enabled:      false,
					Priority:     4,
					RequiresAuth: true,
					Credentials: map[string]string{
						"apiKey": "",
					},
				},
				{
					ID:           "hardcover",
					Name:         "Hardcover",
					Enabled:      false,
					Priority:     5,
					RequiresAuth: true,
					Credentials:  make(map[string]string),
				},
				{
					ID:           "wikipedia",
					Name:         "Wikipedia",
					Enabled:      false, // Disabled by default — Wikipedia API returns 403
					Priority:     6,
					RequiresAuth: false,
					Credentials:  make(map[string]string),
				},
			},

			// Managed external-tool lifecycle
			Tools: tools.ToolsConfig{
				ManagedDir:          "/var/lib/audiobook-organizer/tools",
				Ollama:              tools.ToolConfig{Mode: tools.ToolModeSystem},
				Fpcalc:              tools.ToolConfig{Mode: tools.ToolModeSystem},
				AllowPeriodicOllama: false,
				OllamaDebounceMin:   10,
			},
		}
	}) // end Mutate
}
