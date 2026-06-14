// file: internal/config/config.go
// version: 1.49.0
// guid: 7b8c9d0e-1f2a-3b4c-5d6e-7f8a9b0c1d2e
// last-edited: 2026-06-14

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

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

	// Per-feature OpenAI model selection. Default to gpt-5-mini for all four
	// to preserve historical behavior. See spec docs/superpowers/specs/2026-04-27-per-feature-llm-model-knob-design.md.
	DedupReviewModel    string `json:"dedup_review_model"    mapstructure:"dedup_review_model"`
	MetadataReviewModel string `json:"metadata_review_model" mapstructure:"metadata_review_model"`
	FilenameParseModel  string `json:"filename_parse_model"  mapstructure:"filename_parse_model"`
	CoverArtModel       string `json:"cover_art_model"       mapstructure:"cover_art_model"`

	// Performance
	ConcurrentScans int `json:"concurrent_scans"`
	// ChapterConsolidationThresholdMin is the per-file duration threshold (minutes)
	// used during scanning to detect chapter-named files. If a group of ≥ 3 files
	// sharing the same base title (e.g. "01 - My Book", "02 - My Book") each
	// averages below this duration, they are consolidated into one book record.
	// Default 10. Set to 0 to disable consolidation.
	ChapterConsolidationThresholdMin int `json:"chapter_consolidation_threshold_min"`
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

	// Embedding-based dedup
	EmbeddingEnabled         bool    `json:"embedding_enabled"`           // default true
	EmbeddingModel           string  `json:"embedding_model"`             // default "text-embedding-3-large"
	DedupBookHighThreshold   float64 `json:"dedup_book_high_threshold"`   // default 0.95
	DedupBookLowThreshold    float64 `json:"dedup_book_low_threshold"`    // default 0.85
	DedupAuthorHighThreshold float64 `json:"dedup_author_high_threshold"` // default 0.92
	DedupAuthorLowThreshold  float64 `json:"dedup_author_low_threshold"`  // default 0.80
	DedupAutoMergeEnabled    bool    `json:"dedup_auto_merge_enabled"`    // default true
	// DedupEmbeddingsEnabled controls whether the embedding layer (Layer 2) is
	// active for dedup.full-scan and per-import dedup checks. Default true
	// (preserves current behaviour). Set to false on air-gapped / no-internet
	// boxes to skip all OpenAI embedding calls; Layer 1 (hash/ISBN/title/
	// duration) still runs and produces candidates. A circuit-breaker in
	// FullScan also disables the path automatically after 3 consecutive API
	// failures, so toggling this manually is only needed when you want to
	// suppress the API calls from the very start.
	DedupEmbeddingsEnabled bool `json:"dedup_embeddings_enabled"` // default true
	// DedupOnImportViaScheduler, when true, routes the post-import dedup check
	// through the UOS dependency scheduler (dedup.check-book op, M4) instead
	// of firing an eager goroutine. Defaults false so production import behavior
	// is unchanged until explicitly opted in. Set to true to enable the
	// scheduled path once book_sig_v1 population is confirmed in the library.
	DedupOnImportViaScheduler bool `json:"dedup_on_import_via_scheduler"` // default false — opt-in
	// DedupLLMAutoMergeHighConfidence, when true, automatically
	// applies a merge when the LLM review (Layer 3) returns a
	// "duplicate" verdict with confidence "high". Opt-in because
	// a false-positive high-confidence verdict silently merges
	// the wrong pair of books. When enabled, every auto-merge
	// tags the surviving book with
	// `dedup:merge-survivor:llm-auto` as a system tag so users
	// can filter the dedup tab for "things the LLM decided
	// for me" and review them post-hoc.
	DedupLLMAutoMergeHighConfidence bool `json:"dedup_llm_auto_merge_high_confidence"` // default false — opt-in

	// Metadata candidate scoring (PR1)
	MetadataEmbeddingScoringEnabled bool    `json:"metadata_embedding_scoring_enabled"` // default true
	MetadataEmbeddingMinScore       float64 `json:"metadata_embedding_min_score"`       // default 0.50
	MetadataEmbeddingBestMatchMin   float64 `json:"metadata_embedding_best_match_min"`  // default 0.70

	// Metadata LLM rerank tier (PR2)
	MetadataLLMScoringEnabled bool    `json:"metadata_llm_scoring_enabled"` // default false — opt-in, costs money
	MetadataLLMRerankEpsilon  float64 `json:"metadata_llm_rerank_epsilon"`  // default 0.01
	MetadataLLMRerankTopK     int     `json:"metadata_llm_rerank_top_k"`    // default 5

	// Tag-write backup policy.
	//
	// When enabled, metadata_fetch_service creates a .bak-<timestamp>
	// hardlink next to every audio file it's about to write tags to.
	// Historically this was always on and accumulated tens of thousands
	// of stale backup files across the library, filling terabytes of
	// disk. It's also of questionable value — TagLib writes tags
	// in-place, which means a hardlink snapshot DOES NOT preserve
	// pre-write content if the writer modifies the inode's data. The
	// hardlinks only survive as real backups when the writer happens
	// to use a rename-dance, which is not guaranteed across formats.
	//
	// Default is false: don't create the backups at all. Users who
	// want belt-and-suspenders recovery can turn it on, but they
	// should pair it with the cleanup-backups maintenance endpoint
	// to keep the library from growing unbounded.
	WriteBackupBeforeTagWrite bool `json:"write_backup_before_tag_write"` // default false

	// API limits
	APIRateLimitPerMinute  int  `json:"api_rate_limit_per_minute"`
	AuthRateLimitPerMinute int  `json:"auth_rate_limit_per_minute"`
	JSONBodyLimitMB        int  `json:"json_body_limit_mb"`
	UploadBodyLimitMB      int  `json:"upload_body_limit_mb"`
	EnableAuth             bool `json:"enable_auth"`
	EnableRateLimit        bool `json:"enable_rate_limit"`

	// Basic HTTP auth (lightweight single-user alternative)
	BasicAuthEnabled  bool   `json:"basic_auth_enabled"`
	BasicAuthUsername string `json:"basic_auth_username"`
	BasicAuthPassword string `json:"basic_auth_password"`

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
	MemoryLimitPercent        int `json:"memory_limit_percent"` // % of system memory
	MemoryLimitMB             int `json:"memory_limit_mb"`      // absolute MB

	// Lifecycle / retention
	PurgeSoftDeletedAfterDays   int  `json:"purge_soft_deleted_after_days"`
	PurgeSoftDeletedDeleteFiles bool `json:"purge_soft_deleted_delete_files"`

	// Logging
	LogLevel          string `json:"log_level"`  // 'debug', 'info', 'warn', 'error'
	LogFormat         string `json:"log_format"` // 'text' or 'json'
	EnableJsonLogging bool   `json:"enable_json_logging"`

	// iTunes sync
	ITunesSyncEnabled      bool            `json:"itunes_sync_enabled"`
	ITunesSyncInterval     int             `json:"itunes_sync_interval"` // minutes
	ITLWriteBackEnabled    bool            `json:"itl_write_back_enabled"`
	ITunesLibraryWritePath string          `json:"itunes_library_write_path"` // ITL path used for write-back (always ITL)
	ITunesLibraryReadPath  string          `json:"itunes_library_read_path"`  // path used for sync (XML or ITL)
	ITunesPathMappings     []ITunesPathMap `json:"itunes_path_mappings"`      // Stored path mappings for write-back
	ITunesAutoWriteBack    bool            `json:"itunes_auto_write_back"`    // Auto write-back on every edit (batched)
	ITunesPathTrimEnabled  bool            `json:"itunes_path_trim_enabled"`  // Trim filenames to fit Windows MAX_PATH for iTunes compatibility
	ITunesWindowsRootPath  string          `json:"itunes_windows_root_path"`  // Windows equivalent of RootDir, e.g. "W:\audiobook-organizer" (no trailing slash)
	ITunesMediaRoot        string          `json:"itunes_media_root"`         // Local path to iTunes Media/Audiobooks, e.g. "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks"

	// Deluge integration
	DelugeWebURL           string `json:"deluge_web_url"`           // e.g. "http://172.16.2.30:8112"
	DelugeWebPassword      string `json:"deluge_web_password"`      // Web UI password (default: "deluge")
	DelugeDiscoveryLabel   string `json:"deluge_discovery_label"`   // label to filter for discovery (e.g. "audiobooks")
	DelugeDiscoveryEnabled bool   `json:"deluge_discovery_enabled"` // enable /discover endpoint (identify unimported torrents)
	DelugeMoveEnabled      bool   `json:"deluge_move_enabled"`      // enable MoveStorage calls when books are reorganized
	// ProtectedPaths is an explicit list of filesystem path prefixes that must
	// never be edited in-place (tag writes, renames, deletes). These are merged
	// with the Deluge save_path set at runtime. iTunes media paths belong here.
	ProtectedPaths []string `json:"protected_paths"` // default: empty

	// Auto-update
	AutoUpdateEnabled      bool   `json:"auto_update_enabled"`
	AutoUpdateChannel      string `json:"auto_update_channel"`       // "stable" or "develop"
	AutoUpdateCheckMinutes int    `json:"auto_update_check_minutes"` // e.g. 60
	AutoUpdateWindowStart  int    `json:"auto_update_window_start"`  // hour 0-23, e.g. 1
	AutoUpdateWindowEnd    int    `json:"auto_update_window_end"`    // hour 0-23, e.g. 4

	// Maintenance window (unified — replaces separate auto-update window)
	MaintenanceWindowEnabled bool `json:"maintenance_window_enabled"`
	MaintenanceWindowStart   int  `json:"maintenance_window_start"` // hour 0-23, default 1
	MaintenanceWindowEnd     int  `json:"maintenance_window_end"`   // hour 0-23, default 4

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

	// Scheduled maintenance tasks
	ScheduledDedupRefreshEnabled   bool `json:"scheduled_dedup_refresh_enabled"`
	ScheduledDedupRefreshInterval  int  `json:"scheduled_dedup_refresh_interval"` // minutes, default 360
	ScheduledDedupRefreshOnStartup bool `json:"scheduled_dedup_refresh_on_startup"`

	ScheduledAuthorSplitEnabled   bool `json:"scheduled_author_split_enabled"`
	ScheduledAuthorSplitInterval  int  `json:"scheduled_author_split_interval"` // minutes, default 0 (manual)
	ScheduledAuthorSplitOnStartup bool `json:"scheduled_author_split_on_startup"`

	ScheduledDbOptimizeEnabled   bool `json:"scheduled_db_optimize_enabled"`
	ScheduledDbOptimizeInterval  int  `json:"scheduled_db_optimize_interval"` // minutes, default 1440
	ScheduledDbOptimizeOnStartup bool `json:"scheduled_db_optimize_on_startup"`

	ScheduledMetadataRefreshEnabled   bool `json:"scheduled_metadata_refresh_enabled"`
	ScheduledMetadataRefreshInterval  int  `json:"scheduled_metadata_refresh_interval"` // minutes
	ScheduledMetadataRefreshOnStartup bool `json:"scheduled_metadata_refresh_on_startup"`

	ScheduledResolveProductionAuthorsEnabled  bool `json:"scheduled_resolve_production_authors_enabled"`
	ScheduledResolveProductionAuthorsInterval int  `json:"scheduled_resolve_production_authors_interval"` // minutes, 0 = manual only

	ScheduledSeriesPruneEnabled   bool `json:"scheduled_series_prune_enabled"`
	ScheduledSeriesPruneInterval  int  `json:"scheduled_series_prune_interval"` // minutes, default 0 (manual)
	ScheduledSeriesPruneOnStartup bool `json:"scheduled_series_prune_on_startup"`

	// AI Batch API
	ScheduledAIDedupBatchEnabled   bool `json:"scheduled_ai_dedup_batch_enabled"`
	ScheduledAIDedupBatchInterval  int  `json:"scheduled_ai_dedup_batch_interval"` // minutes, default 1440 (24h)
	ScheduledAIDedupBatchOnStartup bool `json:"scheduled_ai_dedup_batch_on_startup"`

	ScheduledReconcileEnabled   bool `json:"scheduled_reconcile_enabled"`
	ScheduledReconcileInterval  int  `json:"scheduled_reconcile_interval"` // minutes, default 0 (manual)
	ScheduledReconcileOnStartup bool `json:"scheduled_reconcile_on_startup"`

	// Per-task maintenance window toggles
	MaintenanceWindowDedupRefresh       bool `json:"maintenance_window_dedup_refresh"`
	MaintenanceWindowSeriesPrune        bool `json:"maintenance_window_series_prune"`
	MaintenanceWindowAuthorSplit        bool `json:"maintenance_window_author_split"`
	MaintenanceWindowTombstoneCleanup   bool `json:"maintenance_window_tombstone_cleanup"`
	MaintenanceWindowReconcile          bool `json:"maintenance_window_reconcile"`
	MaintenanceWindowPurgeDeleted       bool `json:"maintenance_window_purge_deleted"`
	MaintenanceWindowPurgeOldLogs       bool `json:"maintenance_window_purge_old_logs"`
	MaintenanceWindowDbOptimize         bool `json:"maintenance_window_db_optimize"`
	MaintenanceWindowLibraryScan        bool `json:"maintenance_window_library_scan"`
	MaintenanceWindowLibraryOrganize    bool `json:"maintenance_window_library_organize"`
	MaintenanceWindowMetadataRefresh    bool `json:"maintenance_window_metadata_refresh"`
	MaintenanceWindowLibrarySizeRefresh bool `json:"maintenance_window_library_size_refresh"`
	// MaintenanceWindowAcoustIDOnlineLookup gates the nightly
	// acoustid.lookup-online task. Off by default — the task hits a
	// third-party API and uses quota, so requires explicit opt-in.
	MaintenanceWindowAcoustIDOnlineLookup bool `json:"maintenance_window_acoustid_online_lookup"`
	// AcoustIDOnlineLookupNightlyLimit caps the number of files processed
	// per nightly run, so the maintenance window doesn't get monopolized
	// by a slow third-party API. 0 = no cap (the op processes everything
	// eligible). Default 5000 ≈ ~33 min at 400ms throttle.
	AcoustIDOnlineLookupNightlyLimit int `json:"acoustid_online_lookup_nightly_limit"`

	// Plugin system
	Plugins map[string]PluginConfig `json:"plugins"`

	SupportedExtensions []string `json:"supported_extensions"`
	ExcludePatterns     []string `json:"exclude_patterns"`
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

	// Per-feature model defaults — gpt-5-mini preserves historical behavior
	viper.SetDefault("dedup_review_model", "gpt-5-mini")
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

	// Set memory management defaults
	viper.SetDefault("memory_limit_type", "items")
	viper.SetDefault("cache_size", 1000)
	viper.SetDefault("metadata_fetch_cache_ttl_days", 180)
	viper.SetDefault("memory_limit_percent", 25)
	viper.SetDefault("memory_limit_mb", 512)

	// Lifecycle / retention defaults
	viper.SetDefault("purge_soft_deleted_after_days", 30)
	viper.SetDefault("purge_soft_deleted_delete_files", false)

	// Set logging defaults
	viper.SetDefault("log_level", "info")
	viper.SetDefault("log_format", "text")
	viper.SetDefault("enable_json_logging", false)

	// Scheduled maintenance task defaults
	viper.SetDefault("scheduled_dedup_refresh_enabled", false)
	viper.SetDefault("scheduled_dedup_refresh_interval", 360)
	viper.SetDefault("scheduled_dedup_refresh_on_startup", false)
	viper.SetDefault("scheduled_author_split_enabled", false)
	viper.SetDefault("scheduled_author_split_interval", 0)
	viper.SetDefault("scheduled_author_split_on_startup", false)
	viper.SetDefault("scheduled_db_optimize_enabled", false)
	viper.SetDefault("scheduled_db_optimize_interval", 1440)
	viper.SetDefault("scheduled_db_optimize_on_startup", false)
	viper.SetDefault("scheduled_metadata_refresh_enabled", false)
	viper.SetDefault("scheduled_metadata_refresh_interval", 0)
	viper.SetDefault("scheduled_metadata_refresh_on_startup", false)

	viper.SetDefault("scheduled_ai_dedup_batch_enabled", false)
	viper.SetDefault("scheduled_ai_dedup_batch_interval", 1440)
	viper.SetDefault("scheduled_ai_dedup_batch_on_startup", false)

	// iTunes sync defaults
	viper.SetDefault("itunes_sync_enabled", true)
	viper.SetDefault("itunes_sync_interval", 30)
	viper.SetDefault("itl_write_back_enabled", false)
	viper.SetDefault("itunes_library_write_path", "")
	viper.SetDefault("itunes_library_read_path", "")
	viper.SetDefault("itunes_auto_write_back", false)
	viper.SetDefault("itunes_path_trim_enabled", false)
	viper.SetDefault("itunes_windows_root_path", "")
	viper.SetDefault("itunes_media_root", "")

	// Auto-update defaults
	viper.SetDefault("auto_update_enabled", false)
	viper.SetDefault("auto_update_channel", "stable")
	viper.SetDefault("auto_update_check_minutes", 60)
	viper.SetDefault("auto_update_window_start", 1)
	viper.SetDefault("auto_update_window_end", 4)

	// Maintenance window defaults
	viper.SetDefault("maintenance_window_enabled", true)
	viper.SetDefault("maintenance_window_start", 1)
	viper.SetDefault("maintenance_window_end", 4)
	// Per-task defaults — maintenance tasks default true
	viper.SetDefault("maintenance_window_dedup_refresh", true)
	viper.SetDefault("maintenance_window_series_prune", true)
	viper.SetDefault("maintenance_window_author_split", true)
	viper.SetDefault("maintenance_window_tombstone_cleanup", true)
	viper.SetDefault("maintenance_window_reconcile", true)
	viper.SetDefault("maintenance_window_purge_deleted", true)
	viper.SetDefault("maintenance_window_purge_old_logs", true)
	viper.SetDefault("maintenance_window_db_optimize", true)
	// Non-maintenance tasks default false
	viper.SetDefault("maintenance_window_library_scan", false)
	viper.SetDefault("maintenance_window_library_organize", false)
	viper.SetDefault("maintenance_window_metadata_refresh", false)
	// FS-walk-based on-disk size refresh — true by default (cheap, runs nightly,
	// keeps the FS-side cache fresh for any caller that queries physical sizes).
	viper.SetDefault("maintenance_window_library_size_refresh", true)

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
	})
	viper.SetDefault("exclude_patterns", []string{})

	supportedExtensions := []string{
		".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma",
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
			DedupReviewModel:    viper.GetString("dedup_review_model"),
			MetadataReviewModel: viper.GetString("metadata_review_model"),
			FilenameParseModel:  viper.GetString("filename_parse_model"),
			CoverArtModel:       viper.GetString("cover_art_model"),

			// Performance
			ConcurrentScans:                  viper.GetInt("concurrent_scans"),
			ChapterConsolidationThresholdMin: viper.GetInt("chapter_consolidation_threshold_min"),
			OperationTimeoutMinutes:          viper.GetInt("operation_timeout_minutes"),
			MinBookSizeBytes:                 viper.GetInt64("min_book_size_bytes"),
			APIRateLimitPerMinute:            viper.GetInt("api_rate_limit_per_minute"),
			AuthRateLimitPerMinute:           viper.GetInt("auth_rate_limit_per_minute"),
			JSONBodyLimitMB:                  viper.GetInt("json_body_limit_mb"),
			UploadBodyLimitMB:                viper.GetInt("upload_body_limit_mb"),
			EnableAuth:                       viper.GetBool("enable_auth"),
			EnableRateLimit:                  viper.GetBool("enable_rate_limit"),
			BasicAuthEnabled:                 viper.GetBool("basic_auth_enabled"),
			BasicAuthUsername:                viper.GetString("basic_auth_username"),
			BasicAuthPassword:                viper.GetString("basic_auth_password"),

			// Memory management
			MemoryLimitType:           viper.GetString("memory_limit_type"),
			CacheSize:                 viper.GetInt("cache_size"),
			MetadataFetchCacheTTLDays: viper.GetInt("metadata_fetch_cache_ttl_days"),
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
			AutoUpdateEnabled:      viper.GetBool("auto_update_enabled"),
			AutoUpdateChannel:      viper.GetString("auto_update_channel"),
			AutoUpdateCheckMinutes: viper.GetInt("auto_update_check_minutes"),
			AutoUpdateWindowStart:  viper.GetInt("auto_update_window_start"),
			AutoUpdateWindowEnd:    viper.GetInt("auto_update_window_end"),

			// Maintenance window
			MaintenanceWindowEnabled:              viper.GetBool("maintenance_window_enabled"),
			MaintenanceWindowStart:                viper.GetInt("maintenance_window_start"),
			MaintenanceWindowEnd:                  viper.GetInt("maintenance_window_end"),
			MaintenanceWindowDedupRefresh:         viper.GetBool("maintenance_window_dedup_refresh"),
			MaintenanceWindowSeriesPrune:          viper.GetBool("maintenance_window_series_prune"),
			MaintenanceWindowAuthorSplit:          viper.GetBool("maintenance_window_author_split"),
			MaintenanceWindowTombstoneCleanup:     viper.GetBool("maintenance_window_tombstone_cleanup"),
			MaintenanceWindowReconcile:            viper.GetBool("maintenance_window_reconcile"),
			MaintenanceWindowPurgeDeleted:         viper.GetBool("maintenance_window_purge_deleted"),
			MaintenanceWindowPurgeOldLogs:         viper.GetBool("maintenance_window_purge_old_logs"),
			MaintenanceWindowDbOptimize:           viper.GetBool("maintenance_window_db_optimize"),
			MaintenanceWindowLibraryScan:          viper.GetBool("maintenance_window_library_scan"),
			MaintenanceWindowLibraryOrganize:      viper.GetBool("maintenance_window_library_organize"),
			MaintenanceWindowMetadataRefresh:      viper.GetBool("maintenance_window_metadata_refresh"),
			MaintenanceWindowLibrarySizeRefresh:   viper.GetBool("maintenance_window_library_size_refresh"),
			MaintenanceWindowAcoustIDOnlineLookup: viper.GetBool("maintenance_window_acoustid_online_lookup"),
			AcoustIDOnlineLookupNightlyLimit:      viper.GetInt("acoustid_online_lookup_nightly_limit"),

			// iTunes sync
			ITunesSyncEnabled:      viper.GetBool("itunes_sync_enabled"),
			ITunesSyncInterval:     viper.GetInt("itunes_sync_interval"),
			ITLWriteBackEnabled:    viper.GetBool("itl_write_back_enabled"),
			ITunesLibraryWritePath: viper.GetString("itunes_library_write_path"),
			ITunesLibraryReadPath:  viper.GetString("itunes_library_read_path"),
			ITunesAutoWriteBack:    viper.GetBool("itunes_auto_write_back"),
			ITunesPathTrimEnabled:  viper.GetBool("itunes_path_trim_enabled"),
			ITunesWindowsRootPath:  viper.GetString("itunes_windows_root_path"),
			ITunesMediaRoot:        viper.GetString("itunes_media_root"),

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
			PathFormat:           viper.GetString("path_format"),
			SegmentTitleFormat:   viper.GetString("segment_title_format"),
			AutoRenameOnApply:    viper.GetBool("auto_rename_on_apply"),
			AutoWriteTagsOnApply: viper.GetBool("auto_write_tags_on_apply"),
			VerifyAfterWrite:     viper.GetBool("verify_after_write"),

			SupportedExtensions: supportedExtensions,
			ExcludePatterns:     excludePatterns,
		}

		// Embedding-based dedup (defaults used unless DB settings override)
		c.EmbeddingEnabled = true
		c.EmbeddingModel = "text-embedding-3-large"
		c.DedupBookHighThreshold = 0.95
		c.DedupBookLowThreshold = 0.85
		c.DedupAuthorHighThreshold = 0.92
		c.DedupAuthorLowThreshold = 0.80
		c.DedupAutoMergeEnabled = true
		c.DedupEmbeddingsEnabled = true           // opt-out: set false on no-internet boxes
		c.DedupLLMAutoMergeHighConfidence = false // opt-in
		c.DedupOnImportViaScheduler = false       // opt-in — keep eager path until M4 is confirmed

		// Metadata candidate scoring (defaults used unless DB settings override)
		c.MetadataEmbeddingScoringEnabled = true
		c.MetadataEmbeddingMinScore = 0.50
		c.MetadataEmbeddingBestMatchMin = 0.70
		c.MetadataLLMScoringEnabled = false
		c.MetadataLLMRerankEpsilon = 0.01
		c.MetadataLLMRerankTopK = 5
		c.WriteBackupBeforeTagWrite = false

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

		// Backward compatibility: map old config key names to new ones
		if c.ITunesLibraryWritePath == "" {
			c.ITunesLibraryWritePath = viper.GetString("itunes_library_itl_path")
		}
		if c.ITunesLibraryReadPath == "" {
			c.ITunesLibraryReadPath = viper.GetString("itunes_library_xml_path")
		}

		// Auto-enable ITL write-back when a write path is configured
		if c.ITunesLibraryWritePath != "" && !c.ITLWriteBackEnabled {
			c.ITLWriteBackEnabled = true
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
			DedupReviewModel:    "gpt-5-mini",
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

			// Embedding-based dedup
			EmbeddingEnabled:                true,
			EmbeddingModel:                  "text-embedding-3-large",
			DedupBookHighThreshold:          0.95,
			DedupBookLowThreshold:           0.85,
			DedupAuthorHighThreshold:        0.92,
			DedupAuthorLowThreshold:         0.80,
			DedupAutoMergeEnabled:           true,
			DedupEmbeddingsEnabled:          true, // opt-out: set false on no-internet boxes
			DedupLLMAutoMergeHighConfidence: false,
			DedupOnImportViaScheduler:       false, // opt-in

			// Metadata candidate scoring (PR1)
			MetadataEmbeddingScoringEnabled: true,
			MetadataEmbeddingMinScore:       0.50,
			MetadataEmbeddingBestMatchMin:   0.70,

			// Metadata LLM rerank tier (PR2)
			MetadataLLMScoringEnabled: false,
			MetadataLLMRerankEpsilon:  0.01,
			MetadataLLMRerankTopK:     5,

			// Tag-write backup default OFF — old default was always-on and
			// accumulated tens of thousands of stale backup files across
			// the library (multi-TB apparent size). Opt-in if you want it.
			WriteBackupBeforeTagWrite: false,

			// Logging
			LogLevel:          "info",
			LogFormat:         "text",
			EnableJsonLogging: false,

			// Auto-update
			AutoUpdateEnabled:      false,
			AutoUpdateChannel:      "stable",
			AutoUpdateCheckMinutes: 60,
			AutoUpdateWindowStart:  1,
			AutoUpdateWindowEnd:    4,

			// Maintenance window
			MaintenanceWindowEnabled:          true,
			MaintenanceWindowStart:            1,
			MaintenanceWindowEnd:              4,
			MaintenanceWindowDedupRefresh:     true,
			MaintenanceWindowSeriesPrune:      true,
			MaintenanceWindowAuthorSplit:      true,
			MaintenanceWindowTombstoneCleanup: true,
			MaintenanceWindowReconcile:        true,
			MaintenanceWindowPurgeDeleted:     true,
			MaintenanceWindowPurgeOldLogs:     true,
			MaintenanceWindowDbOptimize:       true,
			MaintenanceWindowLibraryScan:      false,
			MaintenanceWindowLibraryOrganize:  false,
			MaintenanceWindowMetadataRefresh:  false,
			// AcoustID online lookup is OFF by default — uses third-party
			// quota and only helps users who set ACOUSTID_API_KEY. Opt-in
			// via setting + env key.
			MaintenanceWindowAcoustIDOnlineLookup: false,
			AcoustIDOnlineLookupNightlyLimit:      5000,

			// iTunes sync
			ITunesSyncEnabled:      true,
			ITunesSyncInterval:     30,
			ITLWriteBackEnabled:    false,
			ITunesLibraryWritePath: "",

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
			PathFormat:           "{author}/{series_prefix}{title}/{track_title}.{ext}",
			SegmentTitleFormat:   "{title} - {track}/{total_tracks}",
			AutoRenameOnApply:    true,
			AutoWriteTagsOnApply: true,
			VerifyAfterWrite:     true,

			SupportedExtensions: []string{
				".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma",
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
		}
	}) // end Mutate
}
