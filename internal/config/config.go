// file: internal/config/config.go
// version: 1.178.0
// guid: 7b8c9d0e-1f2a-3b4c-5d6e-7f8a9b0c1d2e
// last-edited: 2026-11-19

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audioext"
	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/tools"
)

// Defaults for the exact db census op (maintenance.db-census-exact).
const (
	DefaultDBCensusExactReadMBPerSec  = 50
	DefaultDBCensusExactCooldownHours = 6
)

// ITunesPathMap defines a bidirectional path prefix mapping between iTunes and local paths.
// From is the iTunes prefix (e.g. "file://localhost/W:/itunes/iTunes%20Media"),
// To is the local prefix (e.g. "file://localhost/mnt/bigdata/books/itunes/iTunes Media").
type ITunesPathMap struct {
	From string `json:"from"` // iTunes path prefix
	To   string `json:"to"`   // Local path prefix
}

// PathAlias renders a server-side filesystem path in a form a remote client can
// act on: the Windows drive form, the UNC form, and an smb:// URL. Aliases are
// matched most-specific-first, the same ordering contract pathutil.PathVars
// uses, so a nested root wins over its parent.
//
// Separator contract: whenever Windows or UNC is non-empty, every separator in
// the remainder of that rendering is flipped to a backslash, unconditionally.
// There is deliberately no Separator field -- a root that must keep forward
// slashes would populate neither.
//
// This is presentation config. It is intentionally NOT the same field as
// ITunes.PathMappings, which is owned by import healing (reconcile.TranslateITunesPath);
// see docs/design/2026-08-20-dual-path-display.md Decision 1.
type PathAlias struct {
	Root    string `json:"root"`    // "/library/books"
	Windows string `json:"windows"` // "W:"            -> W:\Author\Title\
	UNC     string `json:"unc"`     // `\\host\books`  -> \\host\books\Author\Title\
	SMBURL  string `json:"smb_url"` // "smb://host/books"
}

// MetadataSource represents a metadata provider configuration
type MetadataSource struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Enabled      bool              `json:"enabled"`
	Priority     int               `json:"priority"`
	RequiresAuth bool              `json:"requires_auth"`
	Credentials  map[string]string `json:"credentials"`
	// BaseURL overrides the provider client's default API base URL, e.g. for
	// self-hosted mirrors or test doubles. Empty means "use the client's built-in default."
	BaseURL string `json:"base_url,omitempty" mapstructure:"base_url"`

	// RateLimit tunes how hard this provider may be hit. Zero value means
	// "use the built-in budget", which is what every provider did before this
	// existed.
	RateLimit MetadataSourceRateLimit `json:"rate_limit,omitzero" mapstructure:"rate_limit"`
}

// RateLimitTier is the coarse, non-expert control: a multiplier over a
// provider's built-in budget rather than an absolute number.
//
// It is deliberately relative. The built-in numbers differ per provider for
// real reasons -- Hardcover documents 60 requests/minute, Audible is an
// unofficial surface -- so a tier expressed as an absolute RPS would be
// simultaneously reckless for one provider and pointlessly slow for another.
// A multiplier keeps each provider's own known-good figure as the anchor.
type RateLimitTier string

const (
	RateLimitTierLow    RateLimitTier = "low"
	RateLimitTierMedium RateLimitTier = "medium"
	RateLimitTierHigh   RateLimitTier = "high"
)

// Multiplier maps a tier onto its scaling factor. Medium is 1.0 -- the built-in
// budget, which is the documented or deliberately-polite figure. An unknown or
// empty tier is also 1.0 so a typo slows nothing down and speeds nothing up.
func (t RateLimitTier) Multiplier() float64 {
	switch t {
	case RateLimitTierLow:
		return 0.5
	case RateLimitTierHigh:
		return 2.0
	default:
		return 1.0
	}
}

// MetadataSourceRateLimit is the per-provider request budget.
//
// Two levels by design. Tier is the control most people want. The explicit
// fields are the advanced escape hatch for someone who knows the provider's
// documented limit and wants to enter it directly; any explicit field wins over
// the tier for that one value, so entering only RPS keeps tier-derived burst.
//
// Every field is optional and zero means "not set" -- NOT "zero requests".
// A zero RPS on a token bucket blocks forever, so the resolver treats zero as
// absent rather than passing it through.
type MetadataSourceRateLimit struct {
	Tier RateLimitTier `json:"tier,omitempty" mapstructure:"tier"`

	// RPS is sustained requests per second. Advanced override.
	RPS float64 `json:"rps,omitempty" mapstructure:"rps"`
	// Burst is how many requests may go back-to-back before pacing applies.
	Burst int `json:"burst,omitempty" mapstructure:"burst"`
	// MaxRetries bounds retries of throttling responses.
	MaxRetries int `json:"max_retries,omitempty" mapstructure:"max_retries"`
	// TimeoutSeconds is the per-request client timeout.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" mapstructure:"timeout_seconds"`

	// MaxConcurrent bounds simultaneous in-flight searches to this provider
	// during a bulk fetch. This replaces the former process-wide
	// perProviderFetchCap constant, which applied one number to every provider
	// regardless of what that provider could take.
	MaxConcurrent int `json:"max_concurrent,omitempty" mapstructure:"max_concurrent"`
}

// DownloadClientConfig represents download client connection settings.
type DownloadClientConfig struct {
	Torrent TorrentClientConfig `json:"torrent"`
}

// TorrentClientConfig holds torrent client configuration.
type TorrentClientConfig struct {
	Type   string       `json:"type"`
	Deluge DelugeConfig `json:"deluge"`
}

// DelugeConfig holds Deluge RPC configuration.
type DelugeConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
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

	// RequestTimeoutSeconds bounds ONE attempt of ONE embeddings request
	// (ai.EmbeddingClient retries up to three times per batch). 0 means
	// DefaultEmbeddingRequestTimeout, which is what the client hardcoded before
	// this became configurable; values above EmbeddingRequestTimeoutCeiling are
	// clamped. See ResolveEmbeddingRequestTimeout for the bounds and why.
	//
	// The reason this exists is a cold model load, not steady-state cost. A warm
	// 64-input bge-m3 batch costs ~1 s on the Mac and ~5.6 s on a CPU-only host,
	// so the 30 s default is never the limit once the model is resident. The
	// FIRST request after Ollama evicts the model (keep-alive default 5m) has to
	// read the weights from disk, measured at 25.28 s cold on the Mac on
	// 2026-09-09 -- 84% of the default budget for a request doing almost no
	// work. A slower disk or a larger model tips that over, and every retry
	// then re-triggers the same load. Raise this for such a backend instead of
	// raising the default for the hosted-API path, which does not have the
	// problem.
	RequestTimeoutSeconds int `json:"request_timeout_seconds" mapstructure:"request_timeout_seconds"`
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
	// whether ComposeScore should start doing so). Since 2026-09-02 this map
	// DOES reach the live engine's ScoreConfig.Signals — via
	// DedupSignalConfig.ScoreConfig (dedup_score_config.go) → dedup.NewEngine
	// at startup, and → Engine.SetScoreConfig through the dedup plugin's
	// config Facade UpdateHook on every PUT /api/v1/config (which then queues
	// the dedup.rescore op to re-band stored rows) or → Engine.ReloadScoreConfig
	// on a calibrate apply, which is already inside an op and so re-bands
	// inline — the same channel the band_*_min fields use. So once ComposeScore starts
	// clamping there is nothing left to wire; until then the bounds are
	// carried, not applied. A kind name not seeded by DefaultScoreConfig is
	// REJECTED (400 on PUT, refused at startup), not skipped.
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
	// ChromemLazy skips the eager HydrateChromem at startup when true (default
	// false / eager). FindSimilar then falls back to the SQLite linear-scan
	// path instead of the chromem ANN index — trades ~6GB heap for slower
	// (50-200ms vs <10ms) similarity queries. See internal/dedup/lifecycle.go.
	ChromemLazy bool `json:"chromem_lazy" mapstructure:"chromem_lazy"`
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

// ITunesConfig holds all settings for the iTunes import subsystem.
//
// iTunes is an import-only source (owner decision 2026-10-07): nothing here
// enables a write to the iTunes library. The write-back keys a stored config
// may still hold (write_back_enabled, auto_write_back, write_back_dry_run)
// have no field and are ignored on load.
//
// Import is also manual only (owner decision 2026-10-08): there is no
// incremental or scheduled sync. The sync keys a stored config or the
// environment may still hold (sync_enabled, sync_interval,
// ITUNES_SYNC_ENABLED, ITUNES_SYNC_INTERVAL) have no field and are ignored
// on load.
type ITunesConfig struct {
	// LibraryITLPath is the iTunes library (.itl) file. The app reads it (ITL
	// parse for PID census, stale-path checks, download) and protects its
	// directory from deletion; it never writes it. The stored key keeps its
	// historical name, library_write_path, so existing configs load unchanged.
	LibraryITLPath  string          `json:"library_write_path" mapstructure:"library_write_path"`
	LibraryReadPath string          `json:"library_read_path"  mapstructure:"library_read_path"`
	PathTrimEnabled bool            `json:"path_trim_enabled"  mapstructure:"path_trim_enabled"`
	WindowsRootPath string          `json:"windows_root_path"  mapstructure:"windows_root_path"`
	MediaRoot       string          `json:"media_root"         mapstructure:"media_root"`
	PathMappings    []ITunesPathMap `json:"path_mappings"      mapstructure:"path_mappings"`

	// Libraries is the explicit 4-state library model (Original/AO x .itl/.xml)
	// plus the PointedAt/ImportSource mode facts. Inert until populated: when empty,
	// the legacy LibraryReadPath/LibraryITLPath fields are used as-is. See
	// itunes_libraries.go and docs/specs/2026-07-23-itunes-2way-sync-system-design.md.
	Libraries LibrarySet `json:"libraries" mapstructure:"libraries"`
}

// MaxActivityLogFullDetailDays bounds ActivityLogFullDetailDays. It equals
// maintenance.MaxCompactDays (a test in that package pins the two together);
// config cannot import the plugin package, so the value is repeated here.
const MaxActivityLogFullDetailDays = 36500

// MaxAIJournalRetentionDays bounds AIJournalRetentionDays (100 years), well
// inside what a time.Duration of days can hold.
const MaxAIJournalRetentionDays = 36500

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
	// write_backup_before was removed 2026-10-10: create_backups is the one
	// switch for pre-write backups (removedConfigKeys).

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

	// WriteBackWorkers is the worker-pool size for the bulk write-back /
	// batch-save paths that write tags into audio files (see
	// internal/server/metadata_ops.go writeBackWorkers()). Distinct from
	// BulkFetchWorkers: that pool is network-bound against metadata providers,
	// this one is disk/TagLib-bound against the library. Default 4; 0 or
	// negative falls back to the compiled-in default.
	WriteBackWorkers int `json:"write_back_workers" mapstructure:"write_back_workers"`

	// SourceFanoutWorkers bounds how many metadata SOURCES are queried
	// concurrently for a single book (see internal/metafetch/service_search.go
	// sourceFanoutLimit()). This is a third, distinct axis from the two above:
	// BulkFetchWorkers is how many BOOKS are in flight, this is how many
	// PROVIDERS are in flight per book, and the two multiply. Keep it small —
	// each provider has its own token bucket in internal/metadata/providerhttp,
	// so exceeding the source count buys nothing and only deepens the queue.
	// Default 4; 0 or negative falls back to the compiled-in default.
	SourceFanoutWorkers int `json:"source_fanout_workers" mapstructure:"source_fanout_workers"`
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

	// ParseBatchSize is how many filenames go into one LLM call during the AI
	// filename-parsing phase, and ParseBatchTimeoutSeconds is how long that one
	// call may take. They are a PAIR and must be tuned together: the cost of a
	// batch is roughly linear in its size, so raising one without the other just
	// moves which side of the deadline the call lands on.
	//
	// 0 means use the defaults (DefaultAIParseBatchSize /
	// DefaultAIParseBatchTimeout), which are what this code did unconditionally
	// before 2026-09-09 and remain correct for a hosted API or a GPU backend.
	//
	// They exist for the same reason ScanProgressEvery does: the right value
	// depends entirely on the hardware underneath, and the previous hardcoded
	// pair silently excluded a whole class of it. Measured on a CPU-only Ollama
	// (Ryzen 7 3800X, no usable GPU), one 20-filename batch emits ~1,350
	// completion tokens and takes 105s on qwen2.5:3b and 201s on qwen2.5:7b --
	// against a 30s deadline. Every batch failed, so the phase parsed 0 books
	// while reporting itself complete, and no amount of retrying helped because
	// the deadline was never the problem's cause.
	//
	// Prod moved to a Mac M1 Max (metal) on 2026-09-09 and the knob is why that
	// was a config change rather than a code change. Same model and quant
	// (qwen2.5:7b-instruct, digest 845dbda0ea48), worst wall time per batch:
	//
	//	size    Mac M1 Max      Ryzen 7 3800X
	//	4        6.5 s           47.3 s
	//	8       12.1 s           --
	//	12      18.6 s           --
	//	20      30.3 s          201 s
	//
	// Cost is linear in batch size on BOTH (~1.5 s/book on the Mac, ~10 s/book
	// on the CPU); the hardware moves the slope, not the shape. Prod is 8.
	//
	// ⚠️ Size is NOT bounded only by the deadline. At size 20 the Mac returned
	// 16 results for 20 filenames in one of two windows -- no error, no refusal,
	// just four filenames silently absent from the JSON array. The phase pads
	// and trims to the input length, so those four are indistinguishable from
	// books the model declined to parse. That is a QUALITY ceiling that sits
	// below the timing ceiling, and it is the reason prod runs 8 rather than the
	// 12 that would still fit inside the deadline. Do not raise this knob purely
	// because the clock allows it -- check that len(results) == len(inputs).
	//
	// Upper bound is the stuck-op watchdog: registry's defaultProgressTimeout is
	// 5m and this phase reports progress BEFORE each call (see runAIBatchPhase),
	// so a timeout at or above 5m converts a batch failure into a killed
	// operation -- a strictly worse and much harder-to-diagnose outcome.
	// ResolveAIParseBatch clamps to AIParseBatchTimeoutCeiling for that reason.
	ParseBatchSize           int `json:"parse_batch_size"            mapstructure:"parse_batch_size"`
	ParseBatchTimeoutSeconds int `json:"parse_batch_timeout_seconds" mapstructure:"parse_batch_timeout_seconds"`

	// ParseBatchWorkers is how many parse batches may be in flight at once.
	//
	// It belongs with the pair above and not with the other concurrency knobs,
	// because against a SERIAL backend it is not a throughput setting at all --
	// it is a way to lose batches. Ollama defaults to OLLAMA_NUM_PARALLEL=1 and
	// serves one request at a time, so N concurrent batches put N-1 of them in
	// the server's queue where they spend their own ParseBatchTimeoutSeconds
	// waiting rather than working. Total throughput is unchanged (the backend is
	// the bottleneck either way); the only difference is how many batches fail.
	//
	// Measured 2026-09-09 on prod: 20 books at size 4 = 5 batches, with the
	// then-fixed 4 workers against a CPU-only Ollama. Batches 1, 3 and 5 parsed
	// 12 books; batches 2 and 4 timed out having done nothing but queue. Setting
	// this to 1 for that backend makes the deadline mean "the model is too slow"
	// again instead of "something else was ahead of me in line".
	//
	// ⚠️ The paragraph above explains WHY 1 was right for that backend, and the
	// explanation does not transfer -- but the value does. On the Mac M1 Max
	// prod moved to on 2026-09-09, Ollama DOES serve requests concurrently, so
	// the queueing story is simply false there. 1 is still correct, for a
	// different reason: the GPU is already saturated by a single request.
	// Measured, 40 filenames at size 4 --
	//
	//	1 worker, serial:      4 books / 6.3 s  = 0.63 books/s
	//	4 workers, concurrent: 16 books / 25.4 s = 0.63 books/s
	//
	// Identical throughput. Concurrency does not add work, it only redistributes
	// latency: each individual batch slowed from 6.3 s to ~15.5 s. A naive
	// "sum-of-parts / wall" reading of that run reports a 2.45x speedup, which
	// is an artifact of dividing by the inflated per-batch times -- it measures
	// contention, not gain. Whenever this knob is revisited, compare
	// books-per-second end to end; do not trust a speedup ratio.
	//
	// So: raise this only for a backend that is NOT saturated by one request
	// (a hosted API with real server-side concurrency, or several distinct
	// endpoints), and prove it with a books/s comparison first.
	//
	// 0 means DefaultAIParseBatchWorkers, which is what this was as a constant.
	ParseBatchWorkers int `json:"parse_batch_workers" mapstructure:"parse_batch_workers"`
}

// AI filename-parsing batch defaults and bounds. Exported because the scanner
// phase, the parser's own cap and the config-validation path must all agree on
// them; three copies of "20" in three packages is exactly how the parser's
// independent maxBatchSize came to silently truncate anything larger.
const (
	// DefaultAIParseBatchSize and DefaultAIParseBatchTimeout are the historical
	// hardcoded values, preserved as defaults so an install pointed at a hosted
	// API sees no behavior change from this becoming configurable.
	//
	// They are the WRONG defaults for a local CPU backend, and not by a small
	// margin. Measured 2026-09-09 against qwen2.5:7b-instruct on a Ryzen 7
	// 3800X, worst observed wall time per batch:
	//
	//	size 1   17.9 s
	//	size 2   20.1 s
	//	size 3   29.4 s   <- inside 30 s by under 2%
	//	size 4   47.3 s
	//	size 20  201 s    <- the default: 6.7x over
	//
	// So on that hardware the 30 s default holds only to size 2. Size 3 is
	// nominally inside it and should still not be used: a margin that thin is
	// decided by which other model Ollama happens to have resident. This is how
	// an install sits at 0 books parsed across 81 consecutive runs while every
	// reachability probe stays green -- /api/tags answers instantly on a host
	// that cannot finish a single batch. If ai_backend.llm_mode is local, set
	// both fields explicitly and size the timeout off a measured batch.
	DefaultAIParseBatchSize    = 20
	DefaultAIParseBatchTimeout = 30 * time.Second

	// AIParseBatchTimeoutCeiling keeps a configured timeout below the watchdog's
	// 5m default ProgressTimeout with margin to spare. Above this the operation
	// is killed for inactivity instead of the batch simply failing.
	AIParseBatchTimeoutCeiling = 4 * time.Minute

	// AIParseBatchSizeCeiling bounds a single call's output. The parse prompt
	// emits ~68 completion tokens per filename, so 100 is ~6,800 tokens -- past
	// the point where a small local model's context starts truncating the JSON
	// array and the whole batch is lost rather than one entry.
	AIParseBatchSizeCeiling = 100

	// DefaultAIParseBatchWorkers is the historical `const aiBatchWorkers = 4`.
	DefaultAIParseBatchWorkers = 4

	// AIParseBatchWorkersCeiling caps in-flight batches. Higher than this stops
	// being concurrency and starts being a self-inflicted queue on any backend
	// that does not fan out, and the per-worker pacing delay stops bounding the
	// aggregate request rate in any useful way.
	AIParseBatchWorkersCeiling = 16
)

// AIParseBatchSettings is how the filename-parsing phase is allowed to talk to
// the LLM backend: how much work per call, how long a call may take, and how
// many calls at once.
//
// One struct rather than three getters because all three are coupled through
// the backend's own concurrency. Size sets how long a call takes; workers sets
// how long a call WAITS before it starts on a serial backend; and the timeout
// has to cover whichever of those the deployment actually produces. A caller
// reading one without the others has almost certainly made a mistake -- that
// exact mistake cost 8 of 20 books on prod on 2026-09-09.
type AIParseBatchSettings struct {
	Size    int
	Timeout time.Duration
	Workers int
}

// ResolveAIParseBatch returns the effective batch settings for the AI
// filename-parsing phase, applying defaults and bounds.
//
// The clamping is what stops a caller configuring a combination that cannot
// succeed: a huge batch against a short deadline, a deadline long enough to
// trip the 5m watchdog, or (added 2026-09-09) more in-flight batches than the
// backend can actually serve at once.
func (c *Config) ResolveAIParseBatch() AIParseBatchSettings {
	size := c.AIBackend.ParseBatchSize
	if size <= 0 {
		size = DefaultAIParseBatchSize
	}
	if size > AIParseBatchSizeCeiling {
		size = AIParseBatchSizeCeiling
	}

	timeout := time.Duration(c.AIBackend.ParseBatchTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultAIParseBatchTimeout
	}
	if timeout > AIParseBatchTimeoutCeiling {
		timeout = AIParseBatchTimeoutCeiling
	}

	workers := c.AIBackend.ParseBatchWorkers
	if workers <= 0 {
		workers = DefaultAIParseBatchWorkers
	}
	if workers > AIParseBatchWorkersCeiling {
		workers = AIParseBatchWorkersCeiling
	}

	return AIParseBatchSettings{Size: size, Timeout: timeout, Workers: workers}
}

// Embedding request-timeout default and bound. Exported so the client's own
// fallback (ai.EmbeddingClient.WithRequestTimeout clamps <= 0 to the default)
// and the config-side resolution cannot drift apart -- the same one-constant
// rule the AI parse batch pair follows.
const (
	// DefaultEmbeddingRequestTimeout is the historical hardcoded per-attempt
	// budget, preserved so an install that never sets the key sees no change.
	DefaultEmbeddingRequestTimeout = 30 * time.Second

	// EmbeddingRequestTimeoutCeiling bounds a configured per-attempt timeout.
	//
	// This is 90 s and not the parse path's 4m because the two loops have a
	// different shape under the stuck-op watchdog (registry defaultProgressTimeout
	// 5m). ai.EmbeddingClient.embedBatchRaw makes up to THREE attempts per batch,
	// sleeping 1 s then 4 s between them, and nothing reports progress inside
	// that loop -- so the watchdog sees the whole loop, not one attempt. The
	// worst case for a dead backend is 3 x timeout + 5 s; at 90 s that is 275 s,
	// under the 5m watchdog with margin. At the parse path's 4m it would be 12m,
	// and a backend that merely stalled would get the OPERATION killed for
	// inactivity instead of the batch failing and the scan moving on -- a
	// strictly worse outcome that is much harder to diagnose (see the same
	// argument on AIParseBatchTimeoutCeiling). 90 s is also 3.6x the measured
	// 25.28 s cold load, so the case this knob exists for fits comfortably.
	// TestEmbeddingRequestTimeoutCeilingStaysUnderWatchdog asserts the arithmetic.
	EmbeddingRequestTimeoutCeiling = 90 * time.Second
)

// ResolveEmbeddingRequestTimeout returns the effective per-attempt timeout for
// embeddings requests, applying the default and the ceiling. 0 or negative
// means "unset" and yields DefaultEmbeddingRequestTimeout; anything above
// EmbeddingRequestTimeoutCeiling is clamped to it rather than rejected, for the
// same reason ResolveAIParseBatch clamps: an out-of-range value in a persisted
// config blob must not stop the server from starting, and the clamped value is
// the largest one that is actually safe under the watchdog.
func (c *Config) ResolveEmbeddingRequestTimeout() time.Duration {
	timeout := time.Duration(c.Embedding.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultEmbeddingRequestTimeout
	}
	if timeout > EmbeddingRequestTimeoutCeiling {
		timeout = EmbeddingRequestTimeoutCeiling
	}
	return timeout
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
		return AIBackendModeLocal
	}
	// DERIVATION NEVER YIELDS A PAID BACKEND.
	//
	// This used to return AIBackendModeOpenAI whenever a key was present and
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
	return AIBackendModeDisabled
}

// CatalogConfig drives the author catalog (design
// .claude/notes/catalog-wanted-requests-design-2026-10-01.md). Enabled is the
// feature flag: off (the default) makes catalog.harvest-authors refuse to run
// and the /catalog routes answer 404. Nothing in the catalog writes to books,
// book files or the iTunes library.
type CatalogConfig struct {
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// Language keeps only products in this provider language ("english").
	// Audible's author listing returns every language under one name search.
	Language string `json:"language" mapstructure:"language"`
	// Marketplace is the Audible marketplace the entries are filed under. Only
	// "us" (api.audible.com) is fetched today.
	Marketplace string `json:"marketplace" mapstructure:"marketplace"`
	// HarvestRateFraction is the share of Audible's effective request budget
	// the harvest may use (0 < f <= 1). The harvest runs its own limiter at
	// fraction x audible RPS ON TOP of the shared per-provider token bucket,
	// so it can never take the whole budget from metadata fetches.
	HarvestRateFraction float64 `json:"harvest_rate_fraction" mapstructure:"harvest_rate_fraction"`
	// MaxProductsPerAuthor caps one author's listing (raw products received).
	// Hitting it is logged and recorded on the author state, never silent.
	MaxProductsPerAuthor int `json:"max_products_per_author" mapstructure:"max_products_per_author"`
}

// ScheduledTaskConfig holds per-task scheduler settings.
type ScheduledTaskConfig struct {
	Enabled   bool `json:"enabled"    mapstructure:"enabled"`
	Interval  int  `json:"interval"   mapstructure:"interval"`
	OnStartup bool `json:"on_startup" mapstructure:"on_startup"`
}

// LibraryScanFullConfig drives the periodic FULL library sweep -- a
// library.scan with force_update AND include_root_dir set, which re-reads and
// re-hashes every file rather than skipping unchanged ones.
//
// It deliberately does NOT reuse ScheduledTaskConfig, because a full sweep
// cannot be driven by a plain ticker. scheduler.Start builds an in-memory
// time.NewTicker(interval) with no persisted last-run time, so an interval
// longer than the process's uptime never fires at all -- and it fires nothing
// while logging a healthy-looking "Scheduled task interval" line, which is the
// exact silent-drop shape the scheduler's own comments were written about.
// Production restarted 2026-08-24 07:24 EDT, so a 168h ticker would have been
// reset before it ever elapsed.
//
// Instead the two durations are separated:
//   - Interval    is a short DUE-CHECK cadence and keeps ordinary ticker
//     semantics, so it is always far below any realistic uptime.
//   - PeriodHours is how long between actual sweeps, evaluated against a
//     timestamp persisted in the settings store (library_scan_full_last_run).
//     That comparison is restart-safe by construction.
type LibraryScanFullConfig struct {
	Enabled bool `json:"enabled" mapstructure:"enabled"`

	// Interval is MINUTES BETWEEN DUE CHECKS, not between sweeps. Each check is
	// a single settings read and is almost always a no-op.
	Interval int `json:"interval" mapstructure:"interval"`

	// PeriodHours is HOURS BETWEEN SWEEPS. 168 = weekly.
	PeriodHours int `json:"period_hours" mapstructure:"period_hours"`
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
	// LibraryScan drives the periodic incremental library scan. Unlike the rest
	// of this family it ships ENABLED with a non-zero interval: without it
	// nothing in the process ever discovers a newly added book on its own
	// (the fsnotify watcher is opt-in and best-effort).
	LibraryScan ScheduledTaskConfig `json:"library_scan"                mapstructure:"library_scan"`

	// LibraryScanFull is the weekly FULL sweep. See LibraryScanFullConfig for
	// why it has its own type rather than joining the family above.
	LibraryScanFull LibraryScanFullConfig `json:"library_scan_full" mapstructure:"library_scan_full"`
	DedupRefresh    ScheduledTaskConfig   `json:"dedup_refresh"               mapstructure:"dedup_refresh"`
	// AcoustIDBackfill schedules the acoustid.backfill op. Ships DISABLED:
	// fingerprinting reads every un-fingerprinted file, and turning it on is
	// an owner decision. Before 2026-09-12 the op declared a cron Schedule
	// that nothing read, so it never ran unattended at all.
	AcoustIDBackfill         ScheduledTaskConfig `json:"acoustid_backfill" mapstructure:"acoustid_backfill"`
	LabelRefinement          ScheduledTaskConfig `json:"label_refinement"            mapstructure:"label_refinement"`
	AuthorSplit              ScheduledTaskConfig `json:"author_split"                mapstructure:"author_split"`
	DbOptimize               ScheduledTaskConfig `json:"db_optimize"                 mapstructure:"db_optimize"`
	MetadataRefresh          ScheduledTaskConfig `json:"metadata_refresh"            mapstructure:"metadata_refresh"`
	ResolveProductionAuthors ScheduledTaskConfig `json:"resolve_production_authors"  mapstructure:"resolve_production_authors"`
	SeriesPrune              ScheduledTaskConfig `json:"series_prune"                mapstructure:"series_prune"`
	AIDedupBatch             ScheduledTaskConfig `json:"ai_dedup_batch"              mapstructure:"ai_dedup_batch"`
	Reconcile                ScheduledTaskConfig `json:"reconcile"                   mapstructure:"reconcile"`
	// MetadataUpgrade schedules the metadata_upgrade task
	// (scheduler.metadata-upgrade) on its own interval, independent of the
	// maintenance window. Only Interval and OnStartup are read: the task is
	// scheduled whenever Interval > 0 (and a metadata fetch service exists),
	// so PUT /api/v1/tasks/metadata_upgrade {"interval_minutes":1440} alone
	// turns it on and 0 turns it off. Enabled is NOT read, on purpose: a blob
	// saved before this field existed decodes it as false, and a second
	// switch would leave the owner's interval write silently inert. Ships at
	// 0 (off); scheduling it is an owner decision.
	MetadataUpgrade ScheduledTaskConfig `json:"metadata_upgrade" mapstructure:"metadata_upgrade"`
	// ASINBackfill schedules the asin_backfill task (metafetch.asin-backfill,
	// live, Audible only) on its own interval, in minutes. Gated like
	// MetadataUpgrade: only Interval is read, the task is scheduled whenever
	// Interval > 0 (and the op is registered), and 0 turns it off. Ships at
	// 360 (6h). It never runs on startup (memdb warmup) or in the
	// maintenance window, so OnStartup and Enabled are not read.
	ASINBackfill ScheduledTaskConfig `json:"asin_backfill" mapstructure:"asin_backfill"`
	// CandidateFetch schedules the candidate_fetch task
	// (metadata.candidate-fetch with unfetched=true: fetch only, never
	// apply) on its own interval, in minutes. Gated like ASINBackfill: only
	// Interval is read, 0 turns it off. Ships at 360 (6h).
	CandidateFetch ScheduledTaskConfig `json:"candidate_fetch" mapstructure:"candidate_fetch"`
	// CatalogHarvest schedules the catalog_harvest task
	// (catalog.harvest-authors, live) on its own interval, in minutes. Gated
	// like CandidateFetch: only Interval is read, 0 turns it off; the task
	// also needs catalog.enabled. Ships at 360 (6h), like its siblings. The
	// op re-lists an author only after its 30-day re-harvest interval
	// (partial and failed authors retry every run), so after the first pass a
	// tick costs a library read plus the new and due authors' requests.
	CatalogHarvest ScheduledTaskConfig `json:"catalog_harvest" mapstructure:"catalog_harvest"`
}

// Config holds application configuration
type Config struct {
	// Core paths
	RootDir      string      `json:"root_dir"`
	PathAliases  []PathAlias `json:"path_aliases"   mapstructure:"path_aliases"`
	DatabasePath string      `json:"database_path"`
	DatabaseType string      `json:"database_type"` // "pebble" (default) or "sqlite"

	// ActivityBackend names the activity-log store backend. Pebble is the only
	// one: the SQLite activity backend and its Pebble-to-SQLite migration were
	// deleted in 01-P75 (owner decision D11; the last build that had them is
	// tagged activity-sqlite-last). The field stays so that an old conf or
	// environment still saying "sqlite" is caught: Validate (startup and every
	// PUT) and the activitystore build both reject any value other than empty
	// or "pebble" through ValidateActivityBackend, instead of quietly running on
	// Pebble while the operator believes SQLite is in use. The old
	// activity.sqlite file is left on disk; nothing opens or deletes it.
	ActivityBackend string `json:"activity_backend" mapstructure:"activity_backend"`
	PlaylistDir     string `json:"playlist_dir"`
	SetupComplete   bool   `json:"setup_complete"`

	// Library organization
	OrganizationStrategy    string `json:"organization_strategy"` // 'auto', 'copy', 'hardlink', 'reflink', 'symlink'
	ScanOnStartup           bool   `json:"scan_on_startup"`
	AutoOrganize            bool   `json:"auto_organize"`
	AutoScanEnabled         bool   `json:"auto_scan_enabled"`
	AutoScanDebounceSeconds int    `json:"auto_scan_debounce_seconds"`
	FolderNamingPattern     string `json:"folder_naming_pattern"`
	FileNamingPattern       string `json:"file_naming_pattern"`
	CreateBackups           bool   `json:"create_backups"`
	// BackupDir is where database backups are written. Absolute paths are used
	// as-is; empty means "a backups/ directory beside the database", which is
	// the historical behaviour. Pointing this off the database's own filesystem
	// is strongly recommended -- see the 2026-08-29 outage, where a 15 GB
	// archive written beside the database filled the disk and killed it.
	BackupDir string `json:"backup_dir" mapstructure:"backup_dir"`

	// BackupMaxTotalBytes caps the TOTAL bytes of retained backup archives.
	// 0 means unset (the built-in default applies); a negative value means
	// unlimited. See backup.ResolveMaxTotalBytes for why that translation is
	// explicit rather than a cast. This became worth configuring once BackupDir
	// could point at a volume far larger than the database's own filesystem: the
	// built-in 40 GiB cap would otherwise let an 11 TB library volume retain a
	// single archive.
	BackupMaxTotalBytes int64 `json:"backup_max_total_bytes" mapstructure:"backup_max_total_bytes"`

	// BackupCompression selects the archive format: "gzip", "zstd" or "none".
	//
	// Empty means gzip, which is both the historical format and the value this
	// field deserializes to in every config blob written before it existed.
	// Config is persisted as a full-struct marshal with no `omitempty`, so a
	// zero value that meant "off" would arm itself silently on upgrade -- the
	// failure mode that disabled chapter consolidation for 12,525 books. It is
	// validated at read time so a typo is a startup error rather than a backup
	// that fails hours later at 3am.
	BackupCompression string `json:"backup_compression" mapstructure:"backup_compression"`

	// BackupCompressionLevel is interpreted by the selected codec; 0 means that
	// codec's default. It does NOT mean gzip.NoCompression -- see
	// backup.LevelDefault.
	BackupCompressionLevel int `json:"backup_compression_level" mapstructure:"backup_compression_level"`

	// Storage quotas
	EnableDiskQuota  bool `json:"enable_disk_quota"`
	DiskQuotaPercent int  `json:"disk_quota_percent"`
	EnableUserQuotas bool `json:"enable_user_quotas"`

	// Metadata
	WriteBackMetadata bool `json:"write_back_metadata"`
	// EmbedCoverArt gates metafetch.embedCoverInBookFiles. Default true: the
	// embed ran unconditionally before the setting was wired, so true keeps
	// behaviour unchanged on upgrade.
	EmbedCoverArt   bool             `json:"embed_cover_art"`
	MetadataSources []MetadataSource `json:"metadata_sources"`

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
	// in API responses). Also settable by an operator layer
	// (ABO_BOOKS_ACOUSTID_API_KEY, a secret, or the operator file;
	// see the rename table) so ops-only deployments don't need to touch the
	// settings DB.
	AcoustIDAPIKey string `json:"acoustid_api_key"`

	// FPParallelWorkers is the number of parallel fpcalc workers used by the
	// acoustid.fingerprint-rescan op. Bounds-checked to [1,32] at read time;
	// falls back to 4 outside that range. See internal/plugins/acoustid.
	FPParallelWorkers int `json:"fp_parallel_workers" mapstructure:"fp_parallel_workers"`

	// FingerprintRemoteWorkersEnabled turns on the remote fingerprint worker
	// API (/api/v1/fingerprint/worker/*), through which fp-worker processes on
	// other hosts lease windowed-fingerprint jobs from a running
	// acoustid.window-backfill. OFF by default: while off, every worker route
	// answers 404. Read per request, so flipping it needs no restart.
	FingerprintRemoteWorkersEnabled bool `json:"fingerprint_remote_workers_enabled" mapstructure:"fingerprint_remote_workers_enabled"`

	// FingerprintWorkerToolVersions allowlists the tool builds a remote worker
	// may compute windows with, beyond the server's own (always allowed). Each
	// entry is "<fpcalc version>/<ffmpeg version>", e.g. "1.6.1/8.0.1".
	//
	// !!! READ BEFORE ADDING A PAIR !!! An entry is a declaration that this
	// build's windows are BYTE-IDENTICAL to the server's. Every listed pair and
	// the server's own pair become ONE equivalence class
	// (fingerprint.ToolsEquivalent): windows stamped with any of them count as
	// current, so the server lane NEVER recomputes them, and WindowSetSimilarity
	// compares them with each other as if one build cut them all. Add a pair only
	// after the worker's parity gate proved byte-identical output on the
	// calibration files. A wrong entry silently poisons every comparison that
	// touches that worker's windows, and removing it later re-queues all of them.
	FingerprintWorkerToolVersions []string `json:"fingerprint_worker_tool_versions" mapstructure:"fingerprint_worker_tool_versions"`

	// FingerprintWindowReferenceTools is the reference tool pair of a
	// remote-only acoustid.window-backfill run ({"remote_only": true}), as
	// "<fpcalc version>/<ffmpeg version>", e.g. "1.6.1/9.0.2". Empty (the
	// default) means remote-only live runs refuse to start.
	//
	// In a remote-only run the server decodes nothing, so it has no tool pair
	// of its own: this pair takes the server's place. It (plus the
	// fingerprint_worker_tool_versions allowlist) is the equivalence class,
	// the pair calibration windows must carry, and, while no window cut with
	// it exists yet under a root, the ONE pair a worker may bootstrap with:
	// a worker running exactly this pair passes its startup gate on the
	// file-identity checks alone and becomes the reference every later worker
	// must reproduce byte for byte. Windows the server cut with its own pair
	// are NOT equivalent to it unless that pair is allowlisted, so a
	// remote-only run re-plans them.
	FingerprintWindowReferenceTools string `json:"fingerprint_window_reference_tools" mapstructure:"fingerprint_window_reference_tools"`

	// FingerprintLengthSec is how many seconds of audio fpcalc analyses per
	// file (its -length flag). 0 or unset means 120, fpcalc's own default and
	// what every stored fingerprint was made with; N > 0 analyses the first N
	// seconds, capped at 600 (larger values are clamped to 600 with a warning).
	// Whole-file mode (-length 0, ~80x the decode work measured on 2026-09-12)
	// is deliberately NOT reachable from here: negative values fall back to
	// 120. See internal/fingerprint and internal/plugins/acoustid.
	FingerprintLengthSec int `json:"fingerprint_length_sec" mapstructure:"fingerprint_length_sec"`

	// WhisperClipCacheDir overrides where the intro-transcribe job caches
	// extracted 90s WAV clips. Empty resolves to {RootDir}/.wav-cache.
	WhisperClipCacheDir string `json:"whisper_clip_cache_dir" mapstructure:"whisper_clip_cache_dir"`
	// WhisperBatchSleepMS is the pause (ms) between remote-Whisper sub-batches
	// so the GPU can shed heat. 0 disables the pause.
	WhisperBatchSleepMS int `json:"whisper_batch_sleep_ms" mapstructure:"whisper_batch_sleep_ms"`

	// WhisperBatchSize is how many WAV files go in one /transcribe-batch
	// request. 0 means use the built-in default (16). Small values spread work
	// across the pool instead of queueing it at one server, which is what you
	// want when the workers serialise inference internally; large values cut
	// HTTP overhead on servers that batch for real.
	WhisperBatchSize int `json:"whisper_batch_size" mapstructure:"whisper_batch_size"`

	// WhisperMaxInFlight caps TOTAL simultaneous transcription requests across
	// every endpoint. 0 means unlimited. This is deliberately NOT the sum of
	// the per-endpoint caps: an operator may have plenty of server capacity and
	// still want fewer requests outstanding at once.
	WhisperMaxInFlight int `json:"whisper_max_in_flight" mapstructure:"whisper_max_in_flight"`

	// OpenAIBaseURL overrides the OpenAI API base URL for every OpenAI cloud
	// API caller app-wide: the OpenAI parser (internal/ai/openai_parser.go),
	// the embedding client (internal/ai/embedding_client.go), the dedup-bench
	// CLI tools, and internal/server/bench.go. Distinct from
	// AIBackend.LocalBaseURL, which is a local LLM/embedding endpoint, not an
	// OpenAI cloud API override.
	OpenAIBaseURL string `json:"openai_base_url" mapstructure:"openai_base_url"`

	// ABSAuthProbeEnabled turns on a verbose per-request diagnostic logging
	// which credentials each Audiobookshelf client sends. Off by default —
	// ABS routes are polled every 15-20s, so an always-on line is noise
	// outside a diagnostic window.
	ABSAuthProbeEnabled bool `json:"abs_auth_probe_enabled" mapstructure:"abs_auth_probe_enabled"`
	// AuthorityEvidenceEnabled feeds the authority lists (internal/authority,
	// built by maintenance.authority-build) into authorcredit as person
	// evidence for credit parts named like a book or series: an author entry
	// links such a part, and an author role seen at tier O (owner library) or
	// tier A (an author credit with a contributor ASIN) may make it the
	// primary. Off by default: the store then answers
	// authority.Empty() and credit resolution is unchanged. Read on every
	// resolve, so flipping it needs no restart.
	AuthorityEvidenceEnabled bool `json:"authority_evidence_enabled" mapstructure:"authority_evidence_enabled"`
	// RetireVersionsApplyEnabled is the feature flag of the retired-versions
	// fixer's Apply (the Repairs-lane fixer "retired-versions"; plan
	// docs 2026-10-22, PR 7). Off (the default) keeps the fixer preview-only:
	// the Repairs routes refuse an apply of it and its Apply writes nothing.
	// On, an apply moves each selected trashed book's file records onto the
	// live book it lost to, as a hidden retired version (never deleting a
	// row). Env override RETIRE_VERSIONS_APPLY_ENABLED; read on every apply, so
	// flipping it needs no restart.
	RetireVersionsApplyEnabled bool `json:"retire_versions_apply_enabled" mapstructure:"retire_versions_apply_enabled"`
	// KeptSweepApplyEnabled is the switch of the nightly kept-file sweep
	// (versions.SweepKeptFiles, a step of maintenance.trash-cleanup): files a
	// version purge left on disk because another row or book named them, revisited
	// to remove the ones nothing names any more. It removes a file ONLY inside the
	// version's own .versions/{id}/ folder; a noted file anywhere else is never
	// removed in either mode, and is listed in the result every run. Off (the
	// default) keeps the sweep report-only: it makes every decision and lists the
	// files it WOULD remove (and the notes it would drop) in the operation result
	// and summary, but unlinks no file and drops no note. On, it removes those
	// files and drops their notes. Report-only is not read-only: in either mode a note the sweep
	// cannot settle (an owner lookup that errors, a path the disk cannot settle, a
	// failed unlink) is counted per calendar day on the version, and after
	// versions.PurgeUndecidedEscalateAfter days in a row the version's status moves
	// to purge_failed and the unsettled notes into its PurgeFailed list (record
	// writes, never file changes). Env override KEPT_SWEEP_APPLY_ENABLED; read
	// once per cleanup run, so flipping it needs no restart and a run never mixes
	// the two modes.
	KeptSweepApplyEnabled bool `json:"kept_sweep_apply_enabled" mapstructure:"kept_sweep_apply_enabled"`
	// ABSItunesPositionBackfillUserID pins the one-time iTunes-position
	// backfill job to a specific user ID instead of the default
	// single-user/earliest-created-user resolution. An ID matching no user
	// is a hard error, not a silent fallback.
	ABSItunesPositionBackfillUserID string `json:"abs_itunes_position_backfill_user_id" mapstructure:"abs_itunes_position_backfill_user_id"`

	// OTelExporterOTLPEndpoint is the OpenTelemetry OTLP exporter endpoint.
	// Empty (the default) disables OpenTelemetry entirely.
	OTelExporterOTLPEndpoint string `json:"otel_exporter_otlp_endpoint" mapstructure:"otel_exporter_otlp_endpoint"`

	// OTelMetricsOTLPEndpoint is the OTLP/gRPC collector the optional metric
	// push reader sends to. Empty (the default) means no push; /metrics is
	// unaffected either way. It never falls back to OTelExporterOTLPEndpoint.
	OTelMetricsOTLPEndpoint string `json:"otel_metrics_otlp_endpoint" mapstructure:"otel_metrics_otlp_endpoint"`
	// OTelMetricsOTLPInterval is the push period. Env OTEL_METRIC_EXPORT_INTERVAL
	// is the OTel standard: a bare integer in MILLISECONDS ("60000"); a Go
	// duration ("60s") is accepted too (telemetry.ParseMetricsInterval).
	OTelMetricsOTLPInterval string `json:"otel_metrics_otlp_interval" mapstructure:"otel_metrics_otlp_interval"`
	// OTelMetricsOTLPInsecure allows plaintext gRPC to a bare host:port or a
	// dns:/// target (an http:// URL is plaintext by itself, https:// is TLS).
	OTelMetricsOTLPInsecure bool `json:"otel_metrics_otlp_insecure" mapstructure:"otel_metrics_otlp_insecure"`
	// TelemetryEnvironment is the deployment.environment resource attribute.
	TelemetryEnvironment string `json:"telemetry_environment" mapstructure:"telemetry_environment"`

	// ListWarmerHeapDeltaMB caps how many MB of live heap (the bytes the last
	// GC cycle kept, runtime/metrics /gc/heap/live:bytes) above the baseline
	// taken once memdb is published the library-list cache warmer's trickle
	// phase may add before backing off. The ceiling is clamped to 80% of the
	// process memory limit (GOMEMLIMIT) when one is set. Also read from the legacy
	// LIST_WARMER_MAX_HEAP_MB env var name for back-compat (same setting,
	// old name — see internal/server/library_list_warmer.go).
	ListWarmerHeapDeltaMB int `json:"list_warmer_heap_delta_mb" mapstructure:"list_warmer_heap_delta_mb"`
	// ListWarmerTrickleIntervalMS is the gap (ms) between library-list cache
	// warmer trickle ticks.
	ListWarmerTrickleIntervalMS int `json:"list_warmer_trickle_interval_ms" mapstructure:"list_warmer_trickle_interval_ms"`

	// LibraryCountsCacheMinIntervalSeconds bounds how often the primary-book
	// count cache (CountPrimaryBooks) is allowed to re-scan the library.
	LibraryCountsCacheMinIntervalSeconds int `json:"library_counts_cache_min_interval_seconds" mapstructure:"library_counts_cache_min_interval_seconds"`

	// BleveDescriptionMaxChars caps how many runes of a book's description
	// are indexed into the Bleve full-text search index.
	BleveDescriptionMaxChars int `json:"bleve_description_max_chars" mapstructure:"bleve_description_max_chars"`

	// Per-feature OpenAI model selection. Default to gpt-5-mini for all three
	// to preserve historical behavior. See spec docs/superpowers/specs/2026-04-27-per-feature-llm-model-knob-design.md.
	// Note: DedupReviewModel has moved to Dedup.ReviewModel.
	MetadataReviewModel string `json:"metadata_review_model" mapstructure:"metadata_review_model"`
	FilenameParseModel  string `json:"filename_parse_model"  mapstructure:"filename_parse_model"`
	CoverArtModel       string `json:"cover_art_model"       mapstructure:"cover_art_model"`

	// WhisperRemoteURL points batch transcription at a remote faster-whisper
	// server (e.g. "http://192.168.1.x:8000"); empty uses the local uv path.
	// Bound to WHISPER_REMOTE_URL.
	WhisperRemoteURL string `json:"whisper_remote_url" mapstructure:"whisper_remote_url"`

	// WhisperEndpoints declares a pool of remote faster-whisper servers. When
	// non-empty it takes precedence over WhisperRemoteURL. Plain structs only —
	// this package must never import internal/transcribe; the conversion to
	// transcribe.Endpoint happens at the TranscribeBatch seam.
	// Environment-authoritative (WHISPER_ENDPOINTS, a JSON array string, e.g.
	// `[{"url":"http://whisper-1.local:8000","concurrency":2,"priority":1,"kind":"gpu","label":"gpu-box"}]`).
	WhisperEndpoints []WhisperEndpoint `json:"whisper_endpoints" mapstructure:"whisper_endpoints"`

	// WhisperRequires is the capability label set every transcription
	// endpoint must satisfy to receive work — the "tier" in tier routing. An
	// endpoint qualifies when its capability set contains EVERY label here;
	// the surviving endpoints are then ordered by Priority as before, so this
	// filters candidates rather than replacing the spill model.
	//
	// EMPTY MEANS ANY ENDPOINT. That is the historical behaviour and must
	// stay explicit: the pool must not begin refusing everything the moment
	// nobody has configured a label.
	//
	// Environment-authoritative (WHISPER_REQUIRES, comma-separated, e.g.
	// "gpu" or "gpu,local"). Labels are matched case-insensitively.
	WhisperRequires []string `json:"whisper_requires" mapstructure:"whisper_requires"`

	// AIEndpoints are the capability-routing server rows (PLAN section 3; see
	// AIEndpoint). Derived once from the legacy AI fields by
	// migrateAIEndpointsBlob. No omitempty: an operator's empty list must
	// round-trip as [] so the migration does not re-run over it.
	//
	// Read for dispatch ONLY when AIEndpointsRouting is true, and then only by
	// the call sites listed on that field; every other site still resolves its
	// backend from ai_backend / openai_api_key / whisper_endpoints.
	AIEndpoints []AIEndpoint `json:"ai_endpoints" mapstructure:"ai_endpoints"`

	// AIEndpointsRouting switches the routed call sites from the legacy
	// scalar backend fields onto the ai_endpoints pool (design rollout step
	// 2). Default false: with it off every AI call resolves its backend
	// exactly as before this field existed. It is read at CALL time, so a
	// PUT /api/v1/config takes effect on the next request without a restart.
	//
	// Routed when true (and only in llm_mode / embedding_mode local or
	// openai-fallback-local; explicit openai mode is never routed):
	//   - llm.filename_parse — the scan / library.ai-parse batch parser
	//   - embed.text         — the embedclient service (EmbedBatch)
	AIEndpointsRouting bool `json:"ai_endpoints_routing" mapstructure:"ai_endpoints_routing"`

	// Performance
	ConcurrentScans int `json:"concurrent_scans"`

	// ScanProgressEvery is how many directories/files a library scan processes
	// between progress checkpoints. It exists because the stuck-op watchdog
	// kills an operation after ProgressTimeout without one, and how long a
	// batch of N items takes depends entirely on the storage underneath: a
	// library on slow or high-latency storage may need a smaller interval than
	// the default of 20. 0 means use the default.
	ScanProgressEvery int `json:"scan_progress_every" mapstructure:"scan_progress_every"`
	// ScanStandDownGraceSeconds delays re-queuing a library scan that a
	// metadata apply stood down, measured from the last apply's release. An
	// apply arriving inside the window finds no scan running and starts at
	// once. Without it, applying metadata one book at a time restarted the scan
	// between every click, and each click waited ~57s for the restarted scan to
	// park (2026-09-13). 0 re-queues immediately. Default 45.
	ScanStandDownGraceSeconds int `json:"scan_standdown_grace_seconds" mapstructure:"scan_standdown_grace_seconds"`
	// ChapterConsolidationThresholdMin is the per-file duration threshold (minutes)
	// used during scanning to detect chapter-named files. If a group of ≥ 3 files
	// sharing the same base title (e.g. "01 - My Book", "02 - My Book") each
	// averages below this duration, they are consolidated into one book record.
	// Default 10 (DefaultChapterConsolidationThresholdMin). 0 or less means the
	// default, never "disabled": until 2026-10-03 a 0 switched consolidation
	// off, and because 0 is also what any partially-populated Config carries, a
	// stray zero silently stopped multi-file grouping for eleven days (12,525
	// books imported without book_file rows). Read it through
	// ResolveChapterConsolidationThresholdMin, which maps a zero to the
	// default with a warning.
	ChapterConsolidationThresholdMin int `json:"chapter_consolidation_threshold_min" mapstructure:"chapter_consolidation_threshold_min"`
	// RepairChapterMaxMin is the longest a single file may run (minutes) and
	// still count as a chapter when a REPAIR JOB (the fragment consolidation
	// fixer) folds separately imported chapter files into one book. It is
	// deliberately separate from ChapterConsolidationThresholdMin, which the
	// import scanner applies to files as they arrive: owner 2026-10-03, "one
	// for import, one for jobs". A repair row is a dry run a person approves,
	// so it can afford a longer limit than the unattended scanner. Default
	// 120. 0 or less means the default, never "disabled".
	RepairChapterMaxMin int `json:"repair_chapter_max_min" mapstructure:"repair_chapter_max_min"`
	// FragmentWindowContainmentMin is where the chapter-fragment fixer's
	// window-containment evidence line (the share of a fragment's window-print
	// band keys its candidate parent also has, 02-PR8) reads as "supporting"
	// rather than "weak". Display only: the line never makes a row applicable
	// and never changes which match proves a repoint. Default 0.5; a value
	// outside (0, 1] means the default (ResolveFragmentWindowContainmentMin).
	FragmentWindowContainmentMin float64 `json:"fragment_window_containment_min" mapstructure:"fragment_window_containment_min"`
	// MetadataCandidateFetchWorkers is the batch candidate fetch's worker
	// count. 0 (the default) sizes it from the enabled sources' summed rate
	// budget (server.candidateFetchWorkers); any other value is used as is,
	// clamped to 1-64.
	MetadataCandidateFetchWorkers int `json:"metadata_candidate_fetch_workers"`
	// IdentificationAdvanceEnabled turns on the identification driver
	// (identification.advance, spec 02 PR 14). On, a 5-minute scheduler tick
	// enqueues the driver while the identification dirty set
	// (ident:dirty:) holds a book, and the 6-hour candidate_fetch tick only
	// MARKS the books its selection finds (logging safety_net_marked=N)
	// instead of fetching them itself. Off (the default), candidate_fetch
	// fetches exactly as before and nothing enqueues the driver; the marks
	// writers leave are inert keys.
	IdentificationAdvanceEnabled bool `json:"identification_advance_enabled"`
	// GoogleBooksDailyLimit is the ONE Google Books daily lookup counter's
	// total (quota day: midnight Pacific; internal/metadata/dailyquota),
	// shared by every Google Books caller: the candidate fetch's fallback, the
	// interactive search dialog, the bulk fetch and any other. An interactive
	// lookup (a person waiting) may use the count up to this. Owner decision
	// 2026-10-06: the key's 1,000 queries/day. 0 (unset) means 1,000; a
	// negative value refuses every Google Books lookup.
	GoogleBooksDailyLimit int `json:"google_books_daily_limit"`
	// GoogleBooksBackgroundDailyLimit is where BACKGROUND Google Books lookups
	// (every one no person is waiting on) stop on the same counter, so the
	// rest of GoogleBooksDailyLimit stays reserved for interactive ones.
	// Owner decision 2026-10-06: 800. 0 (unset) means 800; a negative value
	// refuses every background lookup. Never above GoogleBooksDailyLimit.
	GoogleBooksBackgroundDailyLimit int `json:"google_books_background_daily_limit"`
	// GoogleBooksFallbackDailyLimit turns the batch candidate fetch's Google
	// Books fallback step off when negative. Its count is the shared counter
	// above (it capped a fallback-only count until 2026-10-06); a positive
	// value set from that time can only LOWER the background cap
	// (GoogleBooksBackgroundDailyLimit unset: min of the two), never raise
	// it past 800.
	GoogleBooksFallbackDailyLimit int `json:"google_books_fallback_daily_limit"`
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
	// DBCensusExactReadMBPerSec caps how many MB/s of keys and values the
	// maintenance.db-census-exact op reads (Pebble reads values with their
	// blocks even on a keys-only pass). Keeps the pass from evicting the
	// page/ARC cache the library is served from. 0 means the default (50).
	DBCensusExactReadMBPerSec int `json:"db_census_exact_read_mb_per_sec"`
	// DBCensusExactCooldownHours is the minimum time between two exact census
	// runs; a run inside it is refused unless forced. 0 means the default (6).
	DBCensusExactCooldownHours int `json:"db_census_exact_cooldown_hours"`
	// MinBookSizeBytes: single-file books below this size are flagged as suspicious and
	// skipped for heavy processing. Set to -1 to disable. Defaults to 5242880 (5 MB).
	MinBookSizeBytes int64 `json:"min_book_size_bytes"`
	// MinRescanAgeHours: a file the library ALREADY has a scan-cache entry for is
	// only re-read once its mtime is at least this old. A file that is still
	// being written changes mtime and size between every scan, so without this
	// the scanner re-reads and re-hashes it on every pass and stores whatever
	// half-written metadata it happened to see. Waiting for the file to go quiet
	// costs at most one period of staleness and removes that whole class of churn.
	//
	// It does NOT delay discovery, and it is not a general scan cooldown: a path
	// with no cache entry is new and is read immediately, a book flagged
	// NeedsRescan bypasses it, and a force_update scan disables the scan cache
	// outright so the gate is never consulted. Set to -1 to disable.
	// Defaults to 144 (6 days).
	MinRescanAgeHours int `json:"min_rescan_age_hours"`
	// LogRetentionDays is the purge_old_logs window: system log rows (syslog:)
	// and legacy operation log rows (operationlog:) older than this many days
	// are deleted. It never shortens undo history: the undo journal (opchange:)
	// is not pruned by this or any log setting (see todo.d UNDO-WINDOW-PRUNE).
	// Default 90 (DefaultLogRetentionDays). -1 (any negative value)
	// keeps logs forever. 0 means the default, NOT "keep forever": until
	// 2026-10-11 InitConfig never read this key, so every install ran with 0
	// and the purge never ran; see ResolveLogRetentionDays. Consumers must call
	// ResolveLogRetentionDays rather than read the field.
	LogRetentionDays int `json:"log_retention_days"`
	// OperationLogRetentionDays is the retention-and-hygiene job's operation
	// sweep window. 0 or less means 90; there is no "keep forever" value (the
	// job has always mapped 0 to 90, whatever this comment used to say).
	OperationLogRetentionDays int `json:"operation_log_retention_days"`
	// Activity log retention (separate from operation log retention)
	ActivityLogRetentionChangeDays int `json:"activity_log_retention_change_days"` // default 90
	ActivityLogRetentionDebugDays  int `json:"activity_log_retention_debug_days"`  // default 30
	// ActivityLogCompactionDays is the compaction window of the midnight
	// cleanup-activity-log op, and it is consulted ONLY while nightly
	// compaction (below) is disabled. 0 still means 14 there, as it always has:
	// production stores 0, and giving 0 a new meaning would have silently
	// changed that deployment from 14 days of detail to one.
	ActivityLogCompactionDays int `json:"activity_log_compaction_days"` // default 14

	// ActivityLogNightlyCompactionEnabled runs maintenance.nightly-compact-
	// activity-log once a day just after local midnight, collapsing every
	// activity entry from before the kept days into daily digests on every
	// activity backend. While it is on, cleanup-activity-log skips its own
	// compaction pass. Default true (owner decision 2026-09-13). Also
	// toggleable as the nightly_activity_compaction task on the Maintenance
	// tab.
	ActivityLogNightlyCompactionEnabled bool `json:"activity_log_nightly_compaction_enabled"`
	// ActivityLogFullDetailDays is how many whole days BEFORE today the
	// nightly compaction keeps in full detail. 0 (the default) keeps today
	// only: the cutoff is the start of the current local day. N moves the
	// cutoff back N local calendar days. Bounded by
	// MaxActivityLogFullDetailDays.
	ActivityLogFullDetailDays int `json:"activity_log_full_detail_days"`
	// AIJournalRetentionDays is how long a whisper transcript stays in the AI
	// result journal (internal/ai/resultjournal) before the nightly
	// ai_journal_prune task (maintenance.prune-ai-journal) deletes it. The
	// journal only has to carry a result across a restart or re-run; the
	// transcript itself is stored on the book row. Default 30; 0 keeps entries
	// forever (the task does not run). Bounded by MaxAIJournalRetentionDays.
	AIJournalRetentionDays int `json:"ai_journal_retention_days"`

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

	// Catalog holds the author-catalog feature flag and harvest settings.
	Catalog CatalogConfig `json:"catalog" mapstructure:"catalog"`

	// AIBackend holds the backend-mode toggle for the embedding + LLM clients
	// (independent EmbeddingMode / LLMMode enums, local endpoint coordinates).
	// See AIBackendConfig and EffectiveEmbeddingMode / EffectiveLLMMode.
	AIBackend AIBackendConfig `json:"ai_backend" mapstructure:"ai_backend"`

	// API limits. MasterListRetention is how many stored master lists
	// (read-status import inputs) are kept per uploader: an upload prunes the
	// uploader's oldest lists beyond it; 0 means the default (10), negative is
	// invalid.
	APIRateLimitPerMinute int  `json:"api_rate_limit_per_minute"`
	JSONBodyLimitMB       int  `json:"json_body_limit_mb"`
	UploadBodyLimitMB     int  `json:"upload_body_limit_mb"`
	MasterListRetention   int  `json:"master_list_retention"`
	EnableAuth            bool `json:"enable_auth"`
	EnableRateLimit       bool `json:"enable_rate_limit"`

	// ReviewApplyEnabled is the GLOBAL "big switch" for the review-queue apply path.
	// When false (the default), approving a review hold records the decision but NEVER
	// executes the real-world action (e.g. a regroup CombineBooks merge) — every hold
	// stays visible in the review pane for human eyes. Flip to true only when you want
	// approvals to actually apply. Producer-agnostic: gates ALL registered apply
	// handlers, not just regroup.
	ReviewApplyEnabled bool `json:"review_apply_enabled"`

	// BulkApplyMaxItems is the fail-safe ceiling on how many books ONE bulk apply
	// (metadata review apply, review-queue bulk approve/replay, auto-match,
	// batch update, reconcile apply, diagnostics suggestions) may touch. Exceeding
	// it is a refusal with zero writes, never a truncation. Zero or negative means
	// "unset" and falls back to applycap.Default (5,000) — it can never mean
	// unlimited. See internal/applycap.
	BulkApplyMaxItems int `json:"bulk_apply_max_items"`

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
	// OwnerEmail is the Cloudflare Access identity that owner-only actions
	// (Repairs owner apply) require: a request is the owner only when it
	// carries a VERIFIED Cf-Access-Jwt-Assertion whose email equals this,
	// case-insensitively. A password, OAuth, temp-login or invite session is
	// never enough, because the server can create those identities itself
	// (a stolen password, a newly created admin) and cannot create this one.
	// Empty refuses every owner-only action. A sign-in setting: only an
	// interactive session may change it (protected_fields.go).
	OwnerEmail string `json:"owner_email"`

	// ── Audiobookshelf-compatible sync API (design spec Phase 1) ─────────────
	//
	// Environment-authoritative, like the OAuth/Cloudflare keys above: they are set
	// from a systemd Environment= drop-in. The ABS secret must never live in the blob.
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
	ABSJWTSecret string `json:"-" mapstructure:"abs_jwt_secret"`
	// ABSAccessTokenTTL defaults to 720h = 30 DAYS, not one hour (§1.6 item 1): many
	// ABS clients implement no refresh at all, and a short access token logs them out
	// on that cadence. 30d is empirically proven by a known-working ABS shim. It must
	// be shorter than ABSRefreshTokenTTL; absauth.Load clamps it (with a boot
	// warning) when it is not.
	ABSAccessTokenTTL string `json:"abs_access_token_ttl"`
	// ABSRefreshTokenTTL is the SESSION IDLE TTL, default 1440h (60d). Every refresh,
	// and authenticated use at most about once a day, pushes the session's expiry to
	// now plus this, so a device used at least once per window stays signed in. It
	// must outlive the access token so the client's 401 arrives while the session
	// can still be refreshed; with both at 30d (until 2026-10-19) every session died
	// with its token and AudioBooth was logged out mid-use.
	ABSRefreshTokenTTL string `json:"abs_refresh_token_ttl"`
	// ABSSessionMaxTTL optionally caps a session's total lifetime from login,
	// however actively it is used. "0" (the default) means no cap.
	ABSSessionMaxTTL string `json:"abs_session_max_ttl"`
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

	// ── Invitation email and guest access (env_only_settings.go) ────────────
	//
	// ENVIRONMENT-ONLY and `json:"-"`: never in GET /config, never in the blob,
	// refused by PUT /config. Read from the environment by applyInviteEnv.
	//
	// SMTP transport for invitation email. Unconfigured (no host or no From)
	// means no mail is sent and the admin copies the link by hand. A username
	// is optional: plain SMTP to a local relay with no AUTH is supported.
	SMTPHost     string `json:"-" mapstructure:"smtp_host"`
	SMTPPort     int    `json:"-" mapstructure:"smtp_port"`
	SMTPUsername string `json:"-" mapstructure:"smtp_username"`
	SMTPPassword string `json:"-" mapstructure:"smtp_password"`
	SMTPFrom     string `json:"-" mapstructure:"smtp_from"`
	// PublicBaseURL is the origin invitation links point at; falls back to
	// OAuthRedirectBaseURL, then the server's external URL (InviteBaseURL).
	PublicBaseURL string `json:"-" mapstructure:"public_base_url"`
	// Optional Cloudflare Access guest-policy sync: on accept, the invitee's
	// email is appended to the include list of this ONE reusable Access policy.
	// Inert unless all three are set (GuestPolicySyncConfigured).
	CFAccountID      string `json:"-" mapstructure:"cf_account_id"`
	CFGuestsPolicyID string `json:"-" mapstructure:"cf_guests_policy_id"`
	CFPolicyAPIToken string `json:"-" mapstructure:"cf_policy_api_token"`
	// Optional per-guest phone-app credential (CF_GUEST_SERVICE_TOKENS, default
	// off): accepting an emailed invitation mints that guest's OWN Cloudflare
	// service token and adds that one token id to the non-identity "books"
	// policy CFServiceAuthPolicyID. Needs CF_POLICY_API_TOKEN to also hold the
	// "Access: Service Tokens Edit" permission; without it the API answers 403
	// and the feature fails closed. Active only when GuestServiceTokensEnabled.
	CFGuestServiceTokens  bool   `json:"-" mapstructure:"cf_guest_service_tokens"`
	CFServiceAuthPolicyID string `json:"-" mapstructure:"cf_service_auth_policy_id"`
	// InviteEmailServiceToken (INVITE_EMAIL_SERVICE_TOKEN, default off) also
	// emails the minted credential to the guest's verified address right after
	// the accept. Off means it is shown on the accept page only.
	InviteEmailServiceToken bool `json:"-" mapstructure:"invite_email_service_token"`
	// TunnelConnectorAddrs is a comma-separated list of the IP addresses the
	// Cloudflare tunnel connectors (cloudflared, which in production run on
	// other LAN hosts) connect to this service FROM; list every connector.
	// Only a request whose peer is one of them has its CF-Connecting-IP header
	// believed on routes that carry no Access assertion (the public invite
	// routes and bootstrap). An unlisted connector is keyed on its own
	// address: one rate-limit bucket shared by every visitor it relays.
	TunnelConnectorAddrs string `json:"-" mapstructure:"tunnel_connector_addrs"`
	// AllowedPeerCIDRs is a comma-separated list of the networks (CIDR, e.g.
	// 192.0.2.0/24) or single addresses allowed to open a connection to the
	// public listeners. A peer that is none of loopback, a TunnelConnectorAddrs
	// entry or listed here is answered 403 before any routing, so the origin is
	// reachable only through the tunnel and the LAN you name even if a port is
	// exposed by mistake. Empty (the default) allows every peer and logs a
	// startup warning. An entry that cannot be parsed stops the service starting
	// (a typo must neither narrow nor widen the list silently). The list must
	// cover the LAN, including this host's own LAN address, and any Prometheus
	// scraper or LAN client that talks to it directly.
	AllowedPeerCIDRs string `json:"-" mapstructure:"allowed_peer_cidrs"`
	// LoginSaturatedRate and LoginSaturatedBurst (LOGIN_SATURATED_RATE logins
	// per second, LOGIN_SATURATED_BURST) are the service-wide token bucket on
	// password logins admitted while the aggregate login ceilings are spent.
	// Empty means coreauth.DefaultSaturatedRate / DefaultSaturatedBurst; use
	// LoginSaturatedLimits, which validates them (the server refuses to start
	// on an unusable value). Kept as the raw text so a typo is an error, not a
	// silent zero.
	LoginSaturatedRate  string `json:"-" mapstructure:"login_saturated_rate"`
	LoginSaturatedBurst string `json:"-" mapstructure:"login_saturated_burst"`

	// Memory management
	// CacheInvalidateOnBookUpdate controls whether updating a book's metadata
	// invalidates the list/facets caches. Defaults to false so caches stay
	// warm across metadata fetches and write-back operations.
	CacheInvalidateOnBookUpdate bool `json:"cache_invalidate_on_book_update"`
	// MetadataFetchCacheTTLDays is how long (in days) the DB-backed metadata
	// fetch cache (Audible/Audnexus/etc. API results) is considered fresh: the
	// per-book rows and the shared provider-answer cache's non-empty answers.
	// 0 means never expire. Default DefaultMetadataFetchCacheTTLDays (180).
	MetadataFetchCacheTTLDays int `json:"metadata_fetch_cache_ttl_days"`
	// MetadataFetch holds the metadata_fetch.* settings of the shared
	// provider-answer cache (pcache:, see MetadataFetchConfig).
	MetadataFetch MetadataFetchConfig `json:"metadata_fetch" mapstructure:"metadata_fetch"`
	// BootstrapKeyTTL is how long a bootstrap-issued, full-scope admin API key
	// stays valid: a Go duration ("8h", "90m"). Empty means the default,
	// coreauth.DefaultBootstrapKeyTTL (8h); anything over
	// coreauth.MaxBootstrapKeyTTL (24h) is capped, and an invalid or
	// non-positive value falls back to the default.
	// It never means "never expire". Resolve with ResolveBootstrapKeyTTL.
	//
	// Set from the environment / config file only (BOOTSTRAP_KEY_TTL), never
	// from the settings API or the stored config blob (`json:"-"`): the blob
	// used to carry a materialized default for the old days setting, and a
	// sign-in lifetime is not something an API key should be able to raise.
	BootstrapKeyTTL string `json:"-" mapstructure:"bootstrap_key_ttl"`
	// BootstrapKeyTTLDays is the DEPRECATED predecessor of BootstrapKeyTTL
	// (whole days, default 30 until 2026-10-07). A config file or environment
	// that still sets it is honoured, capped at coreauth.MaxBootstrapKeyTTL, with a
	// startup warning, and only when BootstrapKeyTTL is unset. `json:"-"` so
	// the 30 that older config blobs stored is ignored. (SEC-1/PROC-6)
	BootstrapKeyTTLDays int `json:"-" mapstructure:"bootstrap_key_ttl_days"`

	// Lifecycle / retention
	PurgeSoftDeletedAfterDays   int  `json:"purge_soft_deleted_after_days"`
	PurgeSoftDeletedDeleteFiles bool `json:"purge_soft_deleted_delete_files"`
	// BackupRetentionDays is how old a .bak-* file must be before the backup
	// cleanup ops delete it. 0 means "not set": see EffectiveBackupRetentionDays.
	BackupRetentionDays int `json:"backup_retention_days"`

	// Logging
	LogLevel string `json:"log_level"` // 'debug', 'info', 'warn', 'error'

	// ITunes holds all iTunes import settings.
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
	// It is a list: a JSON array in the environment
	// (ABO_CORE_LIBRARY_PROTECTED_PATHS='["/a","/b"]') and a list in the operator
	// file.
	ProtectedPaths []string `json:"protected_paths"` // default: empty

	// Auto-update
	AutoUpdate AutoUpdateConfig `json:"auto_update" mapstructure:"auto_update"`

	// Maintenance window (unified — replaces separate auto-update window)
	Maintenance MaintenanceConfig `json:"maintenance" mapstructure:"maintenance"`

	// Download client integration
	DownloadClient DownloadClientConfig `json:"download_client"`

	// Apply pipeline.
	//
	// path_format and segment_title_format used to live here. They drove a
	// SECOND target-path builder that disagreed with folder_naming_pattern +
	// file_naming_pattern by two directory levels, and because both were live
	// every organize undid the previous apply. There is now one builder and one
	// pair of pattern keys; these two are deleted rather than migrated.
	AutoRenameOnApply    bool `json:"auto_rename_on_apply"`
	AutoWriteTagsOnApply bool `json:"auto_write_tags_on_apply"`
	VerifyAfterWrite     bool `json:"verify_after_write"`

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

	// Search holds library search settings (search.* keys).
	Search SearchConfig `json:"search" mapstructure:"search"`

	// EnabledSortIndexes names the library sort fields that get a memdb
	// sorted secondary index, turning "sort by X" from a
	// materialise-the-whole-filtered-set-and-sort into a streaming walk.
	//
	// Recognised: narrator, year, created_at, updated_at, duration, file_size,
	// bitrate. ("title" is always indexed and is not configurable.) Unknown
	// names are ignored with a warning.
	//
	// author and series are NOT recognised and cannot be. Their indexes are
	// built by an indexer that receives only *Book, and the *Book held in
	// memdb has Author and Series nil'd by stripBookForMemdb, so such an index
	// sorts the whole library under one empty key. Those two sorts are served
	// by resolving the name from the authors/series tables while materialising
	// the match set — correct, and it costs no index. See
	// hydrateSortNames in internal/books/store/memdb_summaries.go.
	//
	// ⚠️ THE DEFAULT IS NARROW, AND THAT IS DELIBERATE. Each index costs real
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
	// Defaults to {"year", "created_at"}. created_at was added 2026-10-11 (owner
	// decision): ABS "Recently Added" and the Library tab's sort-by-date-added
	// otherwise materialise and sort the whole ~16.5k visible set per request
	// (~30-39 ms, ~78 MB allocated); measured ~7 µs with the index, for ~19 MB of
	// memdb and slightly slower inserts. Before any default existed this was empty, and the
	// ABS handler's sort keys resolved to indexes that were never registered,
	// so every non-title sort returned unordered rows behind a 200 OK. Kept
	// deliberately narrow because each index taxes scan insert throughput; see
	// the SetDefault call for the reasoning.
	//
	// BOOT-ONLY. books.ApplyStoreTunables hands the operator layers' value
	// (modules.books.search.enabled_sort_indexes, a JSON list in the
	// environment) to SetEnabledSortIndexes BEFORE any store opens, then
	// writes back the set that was actually built (unknown names dropped).
	// That is the value for the life of the process. So GET /api/v1/config always
	// reports the built set. To change it, edit the config file or the
	// environment and restart. An explicit empty list in the config file
	// means "no optional indexes".
	//
	// This setting is a speed/memory tradeoff, not a correctness one — in
	// BOTH directions, and the second direction had to be fixed to make that
	// sentence true. An absent index is fine: the store materialises the match
	// set and sorts it before paginating. A PRESENT index was NOT fine until
	// 2026-08-25: enabling one moved the request onto a branch that re-sorted
	// the store's already-ordered page against fields BookSummary does not
	// carry, and the tie-breaker rewrote it into insertion order. That hit
	// exactly year, bitrate and bitrate_kbps — and "year" is this very default,
	// so a stock install had precisely one sort index enabled and it was the
	// one being corrupted. Both routes are asserted per key by
	// internal/audiobooks/sort_indexed_path_test.go (index on) and
	// sort_every_field_test.go (index off).
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

// SupportedExtensionSet returns the effective library audio-extension set:
// the user's `supported_extensions`, or — when that is nil or empty — the
// compiled-in audioext.Default list.
//
// Every predicate that asks "is this file part of the library?" should call
// this rather than hardcoding a list or reading AppConfig.SupportedExtensions
// directly. Reading the field directly is wrong twice over: it races with
// Mutate (this function takes the read lock), and it returns nil in any binary
// that has not run InitConfig, which makes the predicate answer false for
// every file. See internal/audioext for why fail-open is the required
// behaviour rather than a convenience.
//
// The returned Set is freshly built and owned by the caller, so a caller may
// keep it and even write to it without affecting anyone else.
//
// Cost, measured on an M1 Max: 482 ns, 1032 B, 5 allocs per call — dominated
// by building the 15-entry map, not by the lock. That is cheap next to an
// os.Stat, an os.ReadDir or a Pebble read, so the per-item call sites that sit
// beside one of those are left alone deliberately. Hoist it out of a loop that
// does NOT already pay for I/O per item, the way rrFindInITunes and
// deluge.AudioExtensions callers do.
func SupportedExtensionSet() audioext.Set {
	// Resolve runs INSIDE the read lock: taking a copy of the slice header and
	// releasing first would read the backing array unsynchronised.
	mu.RLock()
	defer mu.RUnlock()
	return audioext.Resolve(AppConfig.SupportedExtensions)
}

// AuthorityEvidenceEnabled reports authority_evidence_enabled under the read
// lock. authorcredit's evidence source reads it on every credit resolve while
// PUT /config may flip it, so the read must be synchronised; one field under
// RLock, not a whole-struct Snapshot copy per resolve.
func AuthorityEvidenceEnabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return AppConfig.AuthorityEvidenceEnabled
}

// RetireVersionsApplyEnabled reports retire_versions_apply_enabled under the
// read lock. The retired-versions fixer asks on every Repairs request and
// every apply while PUT /config may flip it, so the read is one field under
// RLock, not a whole-struct Snapshot copy.
func RetireVersionsApplyEnabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return AppConfig.RetireVersionsApplyEnabled
}

// KeptSweepApplyEnabled reports kept_sweep_apply_enabled under the read lock.
// The trash cleanup reads it once per run (server wiring) while PUT /config may
// flip it, so the read is one field under RLock, not a whole-struct Snapshot
// copy.
func KeptSweepApplyEnabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return AppConfig.KeptSweepApplyEnabled
}

// Mutate applies fn to AppConfig under a write lock.
// ALL write sites (startup init, update service, test setups) must use this.
//
// With a Facade installed the change is a persisted write as the process's own
// actor and it can be refused; Mutate has no error to return, so the refusal is
// logged once. A caller that must tell its own caller whether the change took
// uses MutateChecked or MutatePersist.
func Mutate(fn func(*Config)) {
	if err := MutateChecked(fn); err != nil {
		mutateRefused(err)
	}
}

// MutateChecked is Mutate that returns the Facade's refusal. With no Facade
// installed it changes memory only and never fails.
func MutateChecked(fn func(*Config)) error {
	if f := Current(); f != nil {
		return f.Mutate(fn)
	}
	mutateLocal(fn)
	return nil
}

// mutateLocal is Mutate on the process-wide Config: what runs with no Facade
// installed, and what Project uses to publish the Facade's projection.
func mutateLocal(fn func(*Config)) {
	mu.Lock()
	defer mu.Unlock()
	// Deferred after the Unlock defer so it runs first, still under the lock:
	// a reader that sees the new generation and then calls Snapshot is
	// guaranteed to see the write. It also fires when fn panics, because fn may
	// have changed the config before it did.
	defer generation.Add(1)
	fn(&AppConfig)
}

// generation counts completed Mutate calls; see Generation.
var generation atomic.Uint64

// Generation returns a counter that changes whenever Mutate has changed the
// config. A consumer that derives state from the config (internal/ai derives
// its metric model-label set) compares it to the value it last synced at, one
// atomic load per use, and re-reads Snapshot only when it moved. That covers
// every write path (startup load, PUT /config, the update service's rollback)
// without each of them having to know about the consumer, and without
// internal/config importing it. It counts calls, not changed values, so a
// no-op Mutate also moves it; consumers must treat a move as "re-derive".
// Writes that bypass Mutate (direct AppConfig assignments, which the Mutate
// convention forbids) are not seen.
func Generation() uint64 { return generation.Load() }

// WhisperEndpoint declares one remote Whisper server for the dispatch pool.
// Mirror of transcribe.Endpoint kept as a plain config struct so this package
// never imports internal/transcribe.
type WhisperEndpoint struct {
	URL string `json:"url"         mapstructure:"url"`
	// Concurrency is the max SIMULTANEOUS requests to this endpoint. Omitted
	// or 0 means 1 -- note this is NOT the whisper_max_in_flight convention,
	// where 0 means unlimited; a per-endpoint cap that defaulted to unlimited
	// would silently reinstate the unbounded fan-out this exists to prevent.
	Concurrency int    `json:"concurrency" mapstructure:"concurrency"`
	Label       string `json:"label"       mapstructure:"label"`
	// Priority: lower = preferred (GPU box 1, CPU box 100).
	Priority int `json:"priority" mapstructure:"priority"`
	// RequireGPU refuses to send work to this endpoint unless its /health
	// reports a GPU device. It exists because a Whisper server on the wrong
	// silicon does not fail -- it serves healthy 200s from a CPU backend at
	// roughly a tenth of the speed, which looks like a slow library rather
	// than a misconfiguration.
	//
	// Fail-closed: an endpoint whose /health cannot be reached, or which is
	// too old to report a device at all, is refused rather than assumed
	// healthy. A pre-2.9.0 whisper_server.py omits the field and so is
	// refused -- upgrade the worker or leave require_gpu off for it.
	RequireGPU bool `json:"require_gpu" mapstructure:"require_gpu"`
	// Capabilities are labels an operator DECLARES about this endpoint, for
	// properties no probe can see ("local", "unmetered", "fast"). Work is
	// routed to endpoints whose capability set contains every label in
	// WhisperRequires.
	//
	// Labels that are MEASURED from /health -- gpu, cpu, batch, cuda, metal,
	// mps, rocm, hip -- are ignored if declared here and derived from the
	// probe instead. Kind failed as a control precisely because an operator
	// could assert "gpu" on a CPU box and be believed.
	Capabilities []string `json:"capabilities" mapstructure:"capabilities"`
}

// DefaultMetadataSources is the metadata source list a settings blob without
// metadata_sources runs with: HEAD's configFromViper fallback (the list it
// used when the key was not set), carried by books' projection base
// (appConfigBase) since F3-07, so an unset metadata_sources still means these
// six and a stored list, even an empty one, replaces them.
func DefaultMetadataSources() []MetadataSource {
	return []MetadataSource{
		{ID: "audible", Name: "Audible", Enabled: true, Priority: 1, Credentials: map[string]string{}},
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 2, Credentials: map[string]string{}},
		{ID: "audnexus", Name: "Audnexus", Enabled: true, Priority: 3, Credentials: map[string]string{}},
		{ID: "google-books", Name: "Google Books", Enabled: false, Priority: 4, RequiresAuth: true, Credentials: map[string]string{"apiKey": ""}},
		{ID: "hardcover", Name: "Hardcover", Enabled: false, Priority: 5, RequiresAuth: true, Credentials: map[string]string{}},
		// Disabled by default: Wikipedia's API returns 403.
		{ID: "wikipedia", Name: "Wikipedia", Enabled: false, Priority: 6, Credentials: map[string]string{}},
	}
}

// ValidateActivityBackend reports whether v is an accepted ActivityBackend
// value: empty (a config that predates the setting) or "pebble", compared
// case-insensitively after trimming. Anything else is an error. "sqlite" gets
// its own message because it was a real setting until 01-P75 removed the
// SQLite activity backend, and an operator who still has it in a conf must be
// told to remove it rather than see a server that silently ignores it.
func ValidateActivityBackend(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "pebble":
		return nil
	case "sqlite":
		return fmt.Errorf("activity_backend: sqlite is no longer supported (the SQLite activity backend was removed); " +
			"remove activity_backend or set it to pebble. The old activity.sqlite file is not read or deleted")
	default:
		return fmt.Errorf("activity_backend: %q is not a supported value; the only activity backend is pebble "+
			"(remove activity_backend or set it to pebble)", v)
	}
}

// validPatternPlaceholder accepts a naming-pattern placeholder, optionally
// carrying a format spec: "{title}", "{track}", "{track:02d}".
//
// The spec half is not optional in practice — the default file pattern is
// "{title} - {track:02d}", and without it this validator rejects the shipped
// default and every book fails to save its config.
var validPatternPlaceholder = regexp.MustCompile(`\{[A-Za-z0-9_]+(?::[A-Za-z0-9]+)?\}`)

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
		// Report what stat ACTUALLY said. This used to claim "does not exist"
		// for every stat failure, so a permission problem (EACCES), a
		// non-directory component (ENOTDIR) and a symlink loop (ELOOP) all
		// produced the same wrong sentence, sending whoever read it to look for
		// a missing directory that was sitting right there. Hit on 2026-09-09
		// during a database relocation: the first guess was permissions, and the
		// message gave no way to tell.
		if os.IsNotExist(err) {
			return fmt.Errorf("%s parent directory %q does not exist", field, parent)
		}
		return fmt.Errorf("%s parent directory %q is not usable: %w", field, parent, err)
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
	if errs := c.Problems(); len(errs) > 0 {
		return fmt.Errorf("invalid configuration: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Normalize makes the in-place normalisations Validate and Problems make,
// without the checks: each zero that means "the default" becomes the default,
// and the iTunes legacy path shims are derived. It is idempotent.
//
// Project calls it on every projection it publishes. HEAD normalised the
// process config at boot (Validate) and on every PUT (Problems on the
// candidate), so readers never saw these zeros; with the flip, a rebuild
// publishes the raw rows, and without this a save would put them back.
func (c *Config) Normalize() {
	// iTunes 4-state model: derive the legacy path shims (inert unless
	// itunes.libraries is populated). See itunes_libraries.go / spec §2.
	c.ITunes.Resolve()

	// The empty-string normalization is load-bearing, not cosmetic: an upgraded
	// install whose stored config_blob predates the field gets "" from
	// migrateEmbeddingBlob (persistence.go), and no default reaches that
	// path. Without this line those installs would stay on chromem
	// forever — or, once the enum check exists, fail to start outright.
	if c.Embedding.VectorBackend == "" {
		c.Embedding.VectorBackend = "hnsw"
	}

	if c.MinBookSizeBytes == 0 {
		c.MinBookSizeBytes = 5 * 1024 * 1024
	}
	// 0 means "unset" and takes the default; -1 (or any negative) disables the
	// rescan-age gate outright. Same idiom as MinBookSizeBytes above.
	if c.MinRescanAgeHours == 0 {
		c.MinRescanAgeHours = 144
	}
}

// Problems is every structural problem Validate would report, one message
// each, and the same normalisations Validate makes (a zero that means "the
// default" is replaced). The settings write path asks for the list so it can
// name each setting; Validate is the boot and one-error form of the same checks.
func (c *Config) Problems() []string {
	var errs []string

	c.Normalize()

	// iTunes 4-state model: Normalize derived the legacy path shims; run the
	// fail-closed library assertions (inert unless itunes.libraries is
	// populated). See itunes_libraries.go / spec §2.
	errs = append(errs, c.ITunes.ValidateLibraries(c.ProtectedPaths)...)

	switch c.DatabaseType {
	case "pebble", "sqlite":
	default:
		errs = append(errs, "database_type must be 'pebble' or 'sqlite'")
	}

	// activity_backend: Pebble is the only activity backend (01-P75). A leftover
	// "sqlite" must stop startup with a message, never run on Pebble while the
	// conf says otherwise.
	if err := ValidateActivityBackend(c.ActivityBackend); err != nil {
		errs = append(errs, err.Error())
	}

	// embedding.vector_backend is enum-like and was previously never validated:
	// internal/server/registry_wire.go selects HNSW only on an exact "hnsw"
	// match and silently falls through to the brute-force chromem scan for
	// every other value, so a typo cost two orders of magnitude with no error
	// and no log line. Normalize-then-reject, the same shape as DatabaseType.
	//
	// Normalize has already turned "" into "hnsw" (see there).
	switch c.Embedding.VectorBackend {
	case "hnsw", "chromem":
	default:
		errs = append(errs, "embedding.vector_backend must be 'hnsw' or 'chromem'")
	}

	// chapter_consolidation_threshold_min: normalize, never reject. A 0 in a
	// stored blob or config file predates the rule that 0 means the default,
	// and refusing to start over it would turn a recovered setting into an
	// outage. Validate runs after InitConfig, after the database overlay and
	// on every PUT candidate, so this is where a zero is caught on every path
	// that loads or changes config. See ResolveChapterConsolidationThresholdMin.
	normalizeChapterConsolidationThreshold(c, "the loaded configuration")
	// log_retention_days: same rule, same reason. 0 means the default, never
	// "keep forever" (that is -1); see ResolveLogRetentionDays.
	normalizeLogRetentionDays(c, "the loaded configuration")

	// Backup compression is validated HERE, at startup and on PUT /config,
	// rather than at backup time.
	//
	// This is not tidiness. CreateBackup prunes old archives before it writes,
	// so a name it cannot resolve means the prune has already happened and no
	// replacement archive is written -- one bad character in this field
	// destroyed backup history on every attempt and reported it as a warning.
	// Rejecting the value at the point it is set keeps that failure impossible
	// instead of merely unlikely.
	//
	// ResolveCodec is the authority rather than a list literal here, so this
	// can never drift from what the backup package actually accepts (it also
	// takes the aliases "gz", "zst", "store" and "uncompressed", which a
	// hand-written list would wrongly reject).
	if codec, cerr := backup.ResolveCodec(c.BackupCompression); cerr != nil {
		errs = append(errs, cerr.Error())
	} else if lerr := backup.ValidateLevel(codec, c.BackupCompressionLevel); lerr != nil {
		errs = append(errs, lerr.Error())
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
	if c.ScanStandDownGraceSeconds < 0 || c.ScanStandDownGraceSeconds > 600 {
		errs = append(errs, "scan_standdown_grace_seconds must be between 0 and 600")
	}
	if c.AutoScanDebounceSeconds < 0 {
		errs = append(errs, "auto_scan_debounce_seconds must be >= 0")
	}
	if c.OperationTimeoutMinutes < 0 {
		errs = append(errs, "operation_timeout_minutes must be >= 0")
	}
	if c.DBCensusExactReadMBPerSec < 0 {
		errs = append(errs, "db_census_exact_read_mb_per_sec must be >= 0")
	}
	if c.DBCensusExactCooldownHours < 0 {
		errs = append(errs, "db_census_exact_cooldown_hours must be >= 0")
	}
	if c.APIRateLimitPerMinute < 0 {
		errs = append(errs, "api_rate_limit_per_minute must be >= 0")
	}
	if c.JSONBodyLimitMB < 0 {
		errs = append(errs, "json_body_limit_mb must be >= 0")
	}
	if c.UploadBodyLimitMB < 0 {
		errs = append(errs, "upload_body_limit_mb must be >= 0")
	}
	if c.MasterListRetention < 0 {
		errs = append(errs, "master_list_retention must be >= 0")
	}
	if c.EnableDiskQuota && (c.DiskQuotaPercent < 1 || c.DiskQuotaPercent > 100) {
		errs = append(errs, "disk_quota_percent must be between 1 and 100")
	}
	// The field is an int, so a fractional day ("1.5") is already refused by
	// the JSON decode into Config; only the range is checked here. Same bound
	// as the manual compaction's older_than_days (maintenance.MaxCompactDays):
	// beyond it time.AddDate wraps and the cutoff lands near NOW, which would
	// compact today.
	if c.ActivityLogFullDetailDays < 0 || c.ActivityLogFullDetailDays > MaxActivityLogFullDetailDays {
		errs = append(errs, fmt.Sprintf("activity_log_full_detail_days must be a whole number of days between 0 and %d",
			MaxActivityLogFullDetailDays))
	}
	if c.AIJournalRetentionDays < 0 || c.AIJournalRetentionDays > MaxAIJournalRetentionDays {
		errs = append(errs, fmt.Sprintf("ai_journal_retention_days must be a whole number of days between 0 and %d (0 keeps entries forever)",
			MaxAIJournalRetentionDays))
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
	// Syntax is not the only way a naming pattern destroys data; see
	// naming_patterns.go. Empty means "unset, the settings default applies",
	// which is the convention the two blocks above already follow.
	errs = append(errs, validateNamingPatterns(c.FolderNamingPattern, c.FileNamingPattern)...)

	for _, ext := range c.SupportedExtensions {
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			errs = append(errs, fmt.Sprintf("supported extension %q must start with '.'", ext))
			break
		}
	}

	// Dedup score ladder. This is the same conversion registry_wire.go runs
	// to build the engine, so a ladder that fails here is exactly one that
	// would refuse to start. The message has to name where the value LIVES,
	// because it is not necessarily config.yaml: the effective dedup.signals
	// is config.yaml overlaid by the settings blob persisted in the database
	// (written by PUT /api/v1/config — the Settings → Dedup page — and by the
	// dedup.calibrate-composite apply op), and the blob wins. An operator
	// told to "fix config.yaml" for a value the UI persisted cannot repair it
	// by editing config.yaml; they need to know to re-PUT a valid ladder.
	if _, err := c.Dedup.Signals.ScoreConfig(); err != nil {
		errs = append(errs, fmt.Sprintf("%v (effective value = config.yaml dedup.signals overlaid by the settings blob persisted in the database via PUT /api/v1/config / Settings → Dedup / calibrate-composite apply — the persisted blob wins, so re-save a valid ladder there, or delete the blob's dedup.signals keys, if config.yaml already looks right)", err))
	}

	return errs
}

// SearchConfig groups the search.* settings.
type SearchConfig struct {
	ResultCache SearchResultCacheConfig `json:"result_cache" mapstructure:"result_cache"`
}

// SearchResultCacheConfig sizes the shared search result cache
// (internal/searchcache): the full ranked ID list per distinct search, served
// as page slices and kept current by the store-wide change generation.
//
// search.result_cache.enabled (default true) is the rollback switch: false
// restores the per-request search. max_bytes (default 128 MiB) caps the IDs
// held, least recently used first. wait_seconds (default 20) is how long a web
// request waits for a new search before it is answered 202 with a search ID to
// poll. The change ring that keeps entries current is not configurable: it
// holds the last 65,536 changed books (searchcache.DefaultRingSize); an entry
// read after more changes than that is rebuilt instead of patched.
type SearchResultCacheConfig struct {
	Enabled     bool  `json:"enabled" mapstructure:"enabled"`
	MaxBytes    int64 `json:"max_bytes" mapstructure:"max_bytes"`
	WaitSeconds int   `json:"wait_seconds" mapstructure:"wait_seconds"`
}

// EffectiveBackupRetentionDays is the retention, in days, that the .bak-*
// cleanup ops use: BackupRetentionDays when set (> 0), else
// PurgeSoftDeletedAfterDays when set, else 30. The fallback keeps behaviour
// unchanged until an operator sets backup_retention_days.
func EffectiveBackupRetentionDays() int {
	if AppConfig.BackupRetentionDays > 0 {
		return AppConfig.BackupRetentionDays
	}
	if AppConfig.PurgeSoftDeletedAfterDays > 0 {
		return AppConfig.PurgeSoftDeletedAfterDays
	}
	return 30
}

// DefaultMetadataFetchCacheTTLDays is metadata_fetch_cache_ttl_days's
// default: how long a non-empty cached provider answer stays fresh.
const DefaultMetadataFetchCacheTTLDays = 180

// Default negative TTLs of the shared provider-answer cache, in days (owner
// decision D30).
const (
	DefaultNegativeTTLDaysAudible     = 14
	DefaultNegativeTTLDaysAudnexus    = 14
	DefaultNegativeTTLDaysOpenLibrary = 30
	DefaultNegativeTTLDaysGoogle      = 7
)

// MetadataFetchConfig groups the metadata_fetch.* settings: the shared
// provider-answer cache (pcache:q: questions, pcache:asin: products) that
// replaces the per-book metadata_fetch_cache:<book>:<provider>#q... rows.
//
// shared_cache (default true) is the rollback switch: false sends every read
// and write back to the per-book rows, exactly as before the shared cache, and
// leaves the pcache: rows inert.
//
// negative_ttl_days is how long an EMPTY provider answer is replayed instead
// of re-asked, per provider (defaults: Audible and Audnexus 14, Open Library
// 30, Google Books 7). 0 turns negative caching off for that provider (an
// empty answer is neither written nor served); it never means "forever". A
// provider with no entry here (Hardcover, Wikipedia) gets no negative caching.
// A non-empty answer uses metadata_fetch_cache_ttl_days. "Search again" on a
// book deletes the shared answers its questions map to; nothing else busts a
// negative entry before its TTL.
//
// catalog_stage (default false) turns on the local author-catalog blocking
// stage (internal/catalog.MatchBook, spec 02 section 3.3 B1/B2): before the
// provider fan-out a book is looked up in the harvested catalog with no
// provider call, and what it finds joins the candidate pool as REVIEW-ONLY
// candidates (owner decision D31). It adds candidates; it does not replace or
// skip any provider call. Off, a search is exactly what it was; it stays off
// until the owner has read the catalog coverage report
// (catalog.coverage-report) and turns it on.
//
// folder_parse_person_title (default true) is the folder-parse variant of the
// search question (spec 02 section 3.3 Q): a book whose stored title is a
// person's name, or is its own live author, is searched by the name of the
// folder holding its files when that folder names a real title. It is a new
// question for those books (about 1,270 on the 10-06 census), so turning it on
// makes the next candidate fetch ask the providers again for them; turning it
// off restores the stored-title question.
type MetadataFetchConfig struct {
	SharedCache            bool                         `json:"shared_cache" mapstructure:"shared_cache"`
	CatalogStage           bool                         `json:"catalog_stage" mapstructure:"catalog_stage"`
	FolderParsePersonTitle bool                         `json:"folder_parse_person_title" mapstructure:"folder_parse_person_title"`
	NegativeTTLDays        MetadataFetchNegativeTTLDays `json:"negative_ttl_days" mapstructure:"negative_ttl_days"`
}

// MetadataFetchNegativeTTLDays is metadata_fetch.negative_ttl_days.*.
type MetadataFetchNegativeTTLDays struct {
	Audible     int `json:"audible" mapstructure:"audible"`
	Audnexus    int `json:"audnexus" mapstructure:"audnexus"`
	OpenLibrary int `json:"openlibrary" mapstructure:"openlibrary"`
	Google      int `json:"google" mapstructure:"google"`
}

// DefaultMetadataFetchConfig is the shipped metadata_fetch.* block.
func DefaultMetadataFetchConfig() MetadataFetchConfig {
	return MetadataFetchConfig{
		SharedCache: true,
		// Off until the owner has read the catalog coverage report.
		CatalogStage: false,
		// On: the variant the brief ships. false is the rollback.
		FolderParsePersonTitle: true,
		NegativeTTLDays: MetadataFetchNegativeTTLDays{
			Audible:     DefaultNegativeTTLDaysAudible,
			Audnexus:    DefaultNegativeTTLDaysAudnexus,
			OpenLibrary: DefaultNegativeTTLDaysOpenLibrary,
			Google:      DefaultNegativeTTLDaysGoogle,
		},
	}
}

// DefaultFragmentWindowContainmentMin is the shipped
// fragment_window_containment_min (02-PR8).
const DefaultFragmentWindowContainmentMin = 0.5

// ResolveFragmentWindowContainmentMin returns the effective threshold at which
// the fragment fixer calls a window-containment line "supporting": the
// configured value, or DefaultFragmentWindowContainmentMin when it is outside
// (0, 1] (unset, a partially-populated Config, or a typo). The value only
// labels an informational evidence line, so falling back is safe.
func (c *Config) ResolveFragmentWindowContainmentMin() float64 {
	if v := c.FragmentWindowContainmentMin; v > 0 && v <= 1 {
		return v
	}
	return DefaultFragmentWindowContainmentMin
}
