// file: internal/books/cutover/head_config.go
// version: 1.0.0
// guid: b63ba9c0-3c3d-4916-9e05-9bede14d5e3a
// last-edited: 2026-11-11

// FROZEN COPY of HEAD (748fd787a) config.Config and every type it references; read-only; deleted in R3 with the other HEAD-format readers (05-config §1.12.2, 03 §3.8).

package cutover

// Types only: field names, json and mapstructure tags and field order are
// byte-identical to HEAD's internal/config/config.go, plus ai_endpoints.go
// (headAIEndpoint), itunes_libraries.go (headLibrarySet, headLibraryRef) and
// internal/tools/config_types.go (headToolMode, headToolConfig,
// headToolsConfig). Field doc comments are dropped. No method is carried:
// none of these types has a MarshalJSON, UnmarshalJSON or text-marshaller
// method at HEAD, so encoding/json treats them as plain data.

// headITunesPathMap is HEAD's config.ITunesPathMap.
type headITunesPathMap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// headPathAlias is HEAD's config.PathAlias.
type headPathAlias struct {
	Root    string `json:"root"`
	Windows string `json:"windows"`
	UNC     string `json:"unc"`
	SMBURL  string `json:"smb_url"`
}

// headMetadataSource is HEAD's config.MetadataSource.
type headMetadataSource struct {
	ID           string                      `json:"id"`
	Name         string                      `json:"name"`
	Enabled      bool                        `json:"enabled"`
	Priority     int                         `json:"priority"`
	RequiresAuth bool                        `json:"requires_auth"`
	Credentials  map[string]string           `json:"credentials"`
	BaseURL      string                      `json:"base_url,omitempty" mapstructure:"base_url"`
	RateLimit    headMetadataSourceRateLimit `json:"rate_limit,omitzero" mapstructure:"rate_limit"`
}

// headRateLimitTier is HEAD's config.RateLimitTier.
type headRateLimitTier string

// headMetadataSourceRateLimit is HEAD's config.MetadataSourceRateLimit.
type headMetadataSourceRateLimit struct {
	Tier           headRateLimitTier `json:"tier,omitempty" mapstructure:"tier"`
	RPS            float64           `json:"rps,omitempty" mapstructure:"rps"`
	Burst          int               `json:"burst,omitempty" mapstructure:"burst"`
	MaxRetries     int               `json:"max_retries,omitempty" mapstructure:"max_retries"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" mapstructure:"timeout_seconds"`
	MaxConcurrent  int               `json:"max_concurrent,omitempty" mapstructure:"max_concurrent"`
}

// headDownloadClientConfig is HEAD's config.DownloadClientConfig.
type headDownloadClientConfig struct {
	Torrent headTorrentClientConfig `json:"torrent"`
}

// headTorrentClientConfig is HEAD's config.TorrentClientConfig.
type headTorrentClientConfig struct {
	Type   string           `json:"type"`
	Deluge headDelugeConfig `json:"deluge"`
}

// headDelugeConfig is HEAD's config.DelugeConfig.
type headDelugeConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// headPluginConfig is HEAD's config.PluginConfig.
type headPluginConfig struct {
	Enabled  bool              `json:"enabled"`
	Settings map[string]string `json:"settings"`
}

// headEmbeddingConfig is HEAD's config.EmbeddingConfig.
type headEmbeddingConfig struct {
	Enabled               bool   `json:"enabled"        mapstructure:"enabled"`
	Model                 string `json:"model"          mapstructure:"model"`
	Dimensions            int    `json:"dimensions"     mapstructure:"dimensions"`
	BaseURL               string `json:"base_url"       mapstructure:"base_url"`
	VectorBackend         string `json:"vector_backend" mapstructure:"vector_backend"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds" mapstructure:"request_timeout_seconds"`
}

// headDedupSignalConfig is HEAD's config.DedupSignalConfig.
type headDedupSignalConfig struct {
	BandCertainMin float64                            `json:"band_certain_min" mapstructure:"band_certain_min"`
	BandHighMin    float64                            `json:"band_high_min" mapstructure:"band_high_min"`
	BandMediumMin  float64                            `json:"band_medium_min" mapstructure:"band_medium_min"`
	BandReviewMin  float64                            `json:"band_review_min" mapstructure:"band_review_min"`
	Confidence     map[string]headDedupKindConfidence `json:"confidence,omitempty" mapstructure:"confidence"`
}

// headDedupKindConfidence is HEAD's config.DedupKindConfidence.
type headDedupKindConfidence struct {
	MinConfidence float64 `json:"min_confidence,omitempty" mapstructure:"min_confidence"`
	MaxConfidence float64 `json:"max_confidence,omitempty" mapstructure:"max_confidence"`
}

// headDedupConfig is HEAD's config.DedupConfig.
type headDedupConfig struct {
	BookHighThreshold          float64                                 `json:"book_high_threshold" mapstructure:"book_high_threshold"`
	BookLowThreshold           float64                                 `json:"book_low_threshold" mapstructure:"book_low_threshold"`
	AuthorHighThreshold        float64                                 `json:"author_high_threshold" mapstructure:"author_high_threshold"`
	AuthorLowThreshold         float64                                 `json:"author_low_threshold" mapstructure:"author_low_threshold"`
	AutoMergeEnabled           bool                                    `json:"auto_merge_enabled" mapstructure:"auto_merge_enabled"`
	EmbeddingsEnabled          bool                                    `json:"embeddings_enabled" mapstructure:"embeddings_enabled"`
	LLMAutoMergeHighConfidence bool                                    `json:"llm_auto_merge_high_confidence" mapstructure:"llm_auto_merge_high_confidence"`
	AutoResolveEnabled         bool                                    `json:"auto_resolve_enabled" mapstructure:"auto_resolve_enabled"`
	OnImportViaScheduler       bool                                    `json:"on_import_via_scheduler" mapstructure:"on_import_via_scheduler"`
	ReviewModel                string                                  `json:"review_model" mapstructure:"review_model"`
	Signals                    headDedupSignalConfig                   `json:"signals" mapstructure:"signals"`
	EmbeddingThresholdsByModel map[string]headEmbeddingModelThresholds `json:"embedding_thresholds_by_model,omitempty" mapstructure:"embedding_thresholds_by_model"`
	ChromemLazy                bool                                    `json:"chromem_lazy" mapstructure:"chromem_lazy"`
}

// headEmbeddingModelThresholds is HEAD's config.EmbeddingModelThresholds.
type headEmbeddingModelThresholds struct {
	High float64 `json:"high" mapstructure:"high"`
	Low  float64 `json:"low" mapstructure:"low"`
}

// headDedupBoilerplateConfig is HEAD's config.DedupBoilerplateConfig.
type headDedupBoilerplateConfig struct {
	ExtraTitlePatterns  []string `json:"extra_title_patterns" mapstructure:"extra_title_patterns"`
	ExtraPrefixPatterns []string `json:"extra_prefix_patterns" mapstructure:"extra_prefix_patterns"`
}

// headITunesConfig is HEAD's config.ITunesConfig.
type headITunesConfig struct {
	LibraryITLPath  string              `json:"library_write_path" mapstructure:"library_write_path"`
	LibraryReadPath string              `json:"library_read_path"  mapstructure:"library_read_path"`
	PathTrimEnabled bool                `json:"path_trim_enabled"  mapstructure:"path_trim_enabled"`
	WindowsRootPath string              `json:"windows_root_path"  mapstructure:"windows_root_path"`
	MediaRoot       string              `json:"media_root"         mapstructure:"media_root"`
	PathMappings    []headITunesPathMap `json:"path_mappings"      mapstructure:"path_mappings"`
	Libraries       headLibrarySet      `json:"libraries" mapstructure:"libraries"`
}

// headMaintenanceConfig is HEAD's config.MaintenanceConfig.
type headMaintenanceConfig struct {
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
	AcoustIDBackfill     bool `json:"acoustid_backfill" mapstructure:"acoustid_backfill"`
}

// headMetadataScoringConfig is HEAD's config.MetadataScoringConfig.
type headMetadataScoringConfig struct {
	EmbeddingEnabled              bool      `json:"embedding_enabled"    mapstructure:"embedding_enabled"`
	EmbeddingMinScore             float64   `json:"embedding_min_score"  mapstructure:"embedding_min_score"`
	EmbeddingBestMatch            float64   `json:"embedding_best_match" mapstructure:"embedding_best_match"`
	LLMEnabled                    bool      `json:"llm_enabled"          mapstructure:"llm_enabled"`
	LLMRerankEpsilon              float64   `json:"llm_rerank_epsilon"   mapstructure:"llm_rerank_epsilon"`
	LLMRerankTopK                 int       `json:"llm_rerank_top_k"     mapstructure:"llm_rerank_top_k"`
	TranscriptionTitleExactBoost  float64   `json:"transcription_title_exact_boost"  mapstructure:"transcription_title_exact_boost"`
	TranscriptionTitleSubstrBoost float64   `json:"transcription_title_substr_boost" mapstructure:"transcription_title_substr_boost"`
	TranscriptionAuthorBoost      float64   `json:"transcription_author_boost"       mapstructure:"transcription_author_boost"`
	TranscriptionNarratorBoost    float64   `json:"transcription_narrator_boost"     mapstructure:"transcription_narrator_boost"`
	CompilationPenalty            *float64  `json:"compilation_penalty"       mapstructure:"compilation_penalty"`
	RichMetadataFieldBonus        float64   `json:"rich_metadata_field_bonus" mapstructure:"rich_metadata_field_bonus"`
	RichMetadataBonusCap          *float64  `json:"rich_metadata_bonus_cap"   mapstructure:"rich_metadata_bonus_cap"`
	F1MinScore                    *float64  `json:"f1_min_score"              mapstructure:"f1_min_score"`
	SeriesNameMatchBoost          float64   `json:"series_name_match_boost"     mapstructure:"series_name_match_boost"`
	SeriesNumberExactBoost        float64   `json:"series_number_exact_boost"   mapstructure:"series_number_exact_boost"`
	SeriesNumberWrongPenalty      float64   `json:"series_number_wrong_penalty" mapstructure:"series_number_wrong_penalty"`
	DurationTierMultipliers       []float64 `json:"duration_tier_multipliers" mapstructure:"duration_tier_multipliers"`
	DurationTierScores            []float64 `json:"duration_tier_scores"      mapstructure:"duration_tier_scores"`
	BulkFetchWorkers              int       `json:"bulk_fetch_workers" mapstructure:"bulk_fetch_workers"`
	WriteBackWorkers              int       `json:"write_back_workers" mapstructure:"write_back_workers"`
	SourceFanoutWorkers           int       `json:"source_fanout_workers" mapstructure:"source_fanout_workers"`
}

// headAIBackendConfig is HEAD's config.AIBackendConfig.
type headAIBackendConfig struct {
	EmbeddingMode            string `json:"embedding_mode"        mapstructure:"embedding_mode"`
	LLMMode                  string `json:"llm_mode"              mapstructure:"llm_mode"`
	LocalBaseURL             string `json:"local_base_url"        mapstructure:"local_base_url"`
	LocalEmbeddingModel      string `json:"local_embedding_model" mapstructure:"local_embedding_model"`
	LocalLLMModel            string `json:"local_llm_model"       mapstructure:"local_llm_model"`
	ParseBatchSize           int    `json:"parse_batch_size"            mapstructure:"parse_batch_size"`
	ParseBatchTimeoutSeconds int    `json:"parse_batch_timeout_seconds" mapstructure:"parse_batch_timeout_seconds"`
	ParseBatchWorkers        int    `json:"parse_batch_workers" mapstructure:"parse_batch_workers"`
}

// headCatalogConfig is HEAD's config.CatalogConfig.
type headCatalogConfig struct {
	Enabled              bool    `json:"enabled" mapstructure:"enabled"`
	Language             string  `json:"language" mapstructure:"language"`
	Marketplace          string  `json:"marketplace" mapstructure:"marketplace"`
	HarvestRateFraction  float64 `json:"harvest_rate_fraction" mapstructure:"harvest_rate_fraction"`
	MaxProductsPerAuthor int     `json:"max_products_per_author" mapstructure:"max_products_per_author"`
}

// headScheduledTaskConfig is HEAD's config.ScheduledTaskConfig.
type headScheduledTaskConfig struct {
	Enabled   bool `json:"enabled"    mapstructure:"enabled"`
	Interval  int  `json:"interval"   mapstructure:"interval"`
	OnStartup bool `json:"on_startup" mapstructure:"on_startup"`
}

// headLibraryScanFullConfig is HEAD's config.LibraryScanFullConfig.
type headLibraryScanFullConfig struct {
	Enabled     bool `json:"enabled" mapstructure:"enabled"`
	Interval    int  `json:"interval" mapstructure:"interval"`
	PeriodHours int  `json:"period_hours" mapstructure:"period_hours"`
}

// headAutoUpdateConfig is HEAD's config.AutoUpdateConfig.
type headAutoUpdateConfig struct {
	Enabled      bool   `json:"enabled"       mapstructure:"enabled"`
	Channel      string `json:"channel"       mapstructure:"channel"`
	CheckMinutes int    `json:"check_minutes" mapstructure:"check_minutes"`
	WindowStart  int    `json:"window_start"  mapstructure:"window_start"`
	WindowEnd    int    `json:"window_end"    mapstructure:"window_end"`
}

// headScheduledTasksConfig is HEAD's config.ScheduledTasksConfig.
type headScheduledTasksConfig struct {
	LibraryScan              headScheduledTaskConfig   `json:"library_scan"                mapstructure:"library_scan"`
	LibraryScanFull          headLibraryScanFullConfig `json:"library_scan_full" mapstructure:"library_scan_full"`
	DedupRefresh             headScheduledTaskConfig   `json:"dedup_refresh"               mapstructure:"dedup_refresh"`
	AcoustIDBackfill         headScheduledTaskConfig   `json:"acoustid_backfill" mapstructure:"acoustid_backfill"`
	LabelRefinement          headScheduledTaskConfig   `json:"label_refinement"            mapstructure:"label_refinement"`
	AuthorSplit              headScheduledTaskConfig   `json:"author_split"                mapstructure:"author_split"`
	DbOptimize               headScheduledTaskConfig   `json:"db_optimize"                 mapstructure:"db_optimize"`
	MetadataRefresh          headScheduledTaskConfig   `json:"metadata_refresh"            mapstructure:"metadata_refresh"`
	ResolveProductionAuthors headScheduledTaskConfig   `json:"resolve_production_authors"  mapstructure:"resolve_production_authors"`
	SeriesPrune              headScheduledTaskConfig   `json:"series_prune"                mapstructure:"series_prune"`
	AIDedupBatch             headScheduledTaskConfig   `json:"ai_dedup_batch"              mapstructure:"ai_dedup_batch"`
	Reconcile                headScheduledTaskConfig   `json:"reconcile"                   mapstructure:"reconcile"`
	MetadataUpgrade          headScheduledTaskConfig   `json:"metadata_upgrade" mapstructure:"metadata_upgrade"`
	ASINBackfill             headScheduledTaskConfig   `json:"asin_backfill" mapstructure:"asin_backfill"`
	CandidateFetch           headScheduledTaskConfig   `json:"candidate_fetch" mapstructure:"candidate_fetch"`
	CatalogHarvest           headScheduledTaskConfig   `json:"catalog_harvest" mapstructure:"catalog_harvest"`
}

// headConfig is HEAD's config.Config.
type headConfig struct {
	RootDir                              string                      `json:"root_dir"`
	PathAliases                          []headPathAlias             `json:"path_aliases"   mapstructure:"path_aliases"`
	DatabasePath                         string                      `json:"database_path"`
	DatabaseType                         string                      `json:"database_type"`
	ActivityBackend                      string                      `json:"activity_backend" mapstructure:"activity_backend"`
	PlaylistDir                          string                      `json:"playlist_dir"`
	SetupComplete                        bool                        `json:"setup_complete"`
	OrganizationStrategy                 string                      `json:"organization_strategy"`
	ScanOnStartup                        bool                        `json:"scan_on_startup"`
	AutoOrganize                         bool                        `json:"auto_organize"`
	AutoScanEnabled                      bool                        `json:"auto_scan_enabled"`
	AutoScanDebounceSeconds              int                         `json:"auto_scan_debounce_seconds"`
	FolderNamingPattern                  string                      `json:"folder_naming_pattern"`
	FileNamingPattern                    string                      `json:"file_naming_pattern"`
	CreateBackups                        bool                        `json:"create_backups"`
	BackupDir                            string                      `json:"backup_dir" mapstructure:"backup_dir"`
	BackupMaxTotalBytes                  int64                       `json:"backup_max_total_bytes" mapstructure:"backup_max_total_bytes"`
	BackupCompression                    string                      `json:"backup_compression" mapstructure:"backup_compression"`
	BackupCompressionLevel               int                         `json:"backup_compression_level" mapstructure:"backup_compression_level"`
	EnableDiskQuota                      bool                        `json:"enable_disk_quota"`
	DiskQuotaPercent                     int                         `json:"disk_quota_percent"`
	EnableUserQuotas                     bool                        `json:"enable_user_quotas"`
	WriteBackMetadata                    bool                        `json:"write_back_metadata"`
	EmbedCoverArt                        bool                        `json:"embed_cover_art"`
	MetadataSources                      []headMetadataSource        `json:"metadata_sources"`
	OpenLibraryDumpEnabled               bool                        `json:"openlibrary_dump_enabled"`
	OpenLibraryDumpDir                   string                      `json:"openlibrary_dump_dir"`
	HardcoverAPIToken                    string                      `json:"hardcover_api_token"`
	GoogleBooksAPIKey                    string                      `json:"google_books_api_key"`
	EnableAIParsing                      bool                        `json:"enable_ai_parsing"`
	OpenAIAPIKey                         string                      `json:"openai_api_key"`
	AcoustIDAPIKey                       string                      `json:"acoustid_api_key"`
	FPParallelWorkers                    int                         `json:"fp_parallel_workers" mapstructure:"fp_parallel_workers"`
	FingerprintRemoteWorkersEnabled      bool                        `json:"fingerprint_remote_workers_enabled" mapstructure:"fingerprint_remote_workers_enabled"`
	FingerprintWorkerToolVersions        []string                    `json:"fingerprint_worker_tool_versions" mapstructure:"fingerprint_worker_tool_versions"`
	FingerprintWindowReferenceTools      string                      `json:"fingerprint_window_reference_tools" mapstructure:"fingerprint_window_reference_tools"`
	FingerprintLengthSec                 int                         `json:"fingerprint_length_sec" mapstructure:"fingerprint_length_sec"`
	WhisperClipCacheDir                  string                      `json:"whisper_clip_cache_dir" mapstructure:"whisper_clip_cache_dir"`
	WhisperBatchSleepMS                  int                         `json:"whisper_batch_sleep_ms" mapstructure:"whisper_batch_sleep_ms"`
	WhisperBatchSize                     int                         `json:"whisper_batch_size" mapstructure:"whisper_batch_size"`
	WhisperMaxInFlight                   int                         `json:"whisper_max_in_flight" mapstructure:"whisper_max_in_flight"`
	OpenAIBaseURL                        string                      `json:"openai_base_url" mapstructure:"openai_base_url"`
	ABSAuthProbeEnabled                  bool                        `json:"abs_auth_probe_enabled" mapstructure:"abs_auth_probe_enabled"`
	AuthorityEvidenceEnabled             bool                        `json:"authority_evidence_enabled" mapstructure:"authority_evidence_enabled"`
	RetireVersionsApplyEnabled           bool                        `json:"retire_versions_apply_enabled" mapstructure:"retire_versions_apply_enabled"`
	KeptSweepApplyEnabled                bool                        `json:"kept_sweep_apply_enabled" mapstructure:"kept_sweep_apply_enabled"`
	ABSItunesPositionBackfillUserID      string                      `json:"abs_itunes_position_backfill_user_id" mapstructure:"abs_itunes_position_backfill_user_id"`
	OTelExporterOTLPEndpoint             string                      `json:"otel_exporter_otlp_endpoint" mapstructure:"otel_exporter_otlp_endpoint"`
	OTelMetricsOTLPEndpoint              string                      `json:"otel_metrics_otlp_endpoint" mapstructure:"otel_metrics_otlp_endpoint"`
	OTelMetricsOTLPInterval              string                      `json:"otel_metrics_otlp_interval" mapstructure:"otel_metrics_otlp_interval"`
	OTelMetricsOTLPInsecure              bool                        `json:"otel_metrics_otlp_insecure" mapstructure:"otel_metrics_otlp_insecure"`
	TelemetryEnvironment                 string                      `json:"telemetry_environment" mapstructure:"telemetry_environment"`
	ListWarmerHeapDeltaMB                int                         `json:"list_warmer_heap_delta_mb" mapstructure:"list_warmer_heap_delta_mb"`
	ListWarmerTrickleIntervalMS          int                         `json:"list_warmer_trickle_interval_ms" mapstructure:"list_warmer_trickle_interval_ms"`
	LibraryCountsCacheMinIntervalSeconds int                         `json:"library_counts_cache_min_interval_seconds" mapstructure:"library_counts_cache_min_interval_seconds"`
	BleveDescriptionMaxChars             int                         `json:"bleve_description_max_chars" mapstructure:"bleve_description_max_chars"`
	MetadataReviewModel                  string                      `json:"metadata_review_model" mapstructure:"metadata_review_model"`
	FilenameParseModel                   string                      `json:"filename_parse_model"  mapstructure:"filename_parse_model"`
	CoverArtModel                        string                      `json:"cover_art_model"       mapstructure:"cover_art_model"`
	WhisperRemoteURL                     string                      `json:"whisper_remote_url" mapstructure:"whisper_remote_url"`
	WhisperEndpoints                     []headWhisperEndpoint       `json:"whisper_endpoints" mapstructure:"whisper_endpoints"`
	WhisperRequires                      []string                    `json:"whisper_requires" mapstructure:"whisper_requires"`
	AIEndpoints                          []headAIEndpoint            `json:"ai_endpoints" mapstructure:"ai_endpoints"`
	AIEndpointsRouting                   bool                        `json:"ai_endpoints_routing" mapstructure:"ai_endpoints_routing"`
	ConcurrentScans                      int                         `json:"concurrent_scans"`
	ScanProgressEvery                    int                         `json:"scan_progress_every" mapstructure:"scan_progress_every"`
	ScanStandDownGraceSeconds            int                         `json:"scan_standdown_grace_seconds" mapstructure:"scan_standdown_grace_seconds"`
	ChapterConsolidationThresholdMin     int                         `json:"chapter_consolidation_threshold_min" mapstructure:"chapter_consolidation_threshold_min"`
	RepairChapterMaxMin                  int                         `json:"repair_chapter_max_min" mapstructure:"repair_chapter_max_min"`
	FragmentWindowContainmentMin         float64                     `json:"fragment_window_containment_min" mapstructure:"fragment_window_containment_min"`
	MetadataCandidateFetchWorkers        int                         `json:"metadata_candidate_fetch_workers"`
	IdentificationAdvanceEnabled         bool                        `json:"identification_advance_enabled"`
	GoogleBooksDailyLimit                int                         `json:"google_books_daily_limit"`
	GoogleBooksBackgroundDailyLimit      int                         `json:"google_books_background_daily_limit"`
	GoogleBooksFallbackDailyLimit        int                         `json:"google_books_fallback_daily_limit"`
	CoalesceShatteredSiblings            bool                        `json:"coalesce_shattered_siblings"`
	OperationTimeoutMinutes              int                         `json:"operation_timeout_minutes"`
	DBCensusExactReadMBPerSec            int                         `json:"db_census_exact_read_mb_per_sec"`
	DBCensusExactCooldownHours           int                         `json:"db_census_exact_cooldown_hours"`
	MinBookSizeBytes                     int64                       `json:"min_book_size_bytes"`
	MinRescanAgeHours                    int                         `json:"min_rescan_age_hours"`
	LogRetentionDays                     int                         `json:"log_retention_days"`
	OperationLogRetentionDays            int                         `json:"operation_log_retention_days"`
	ActivityLogRetentionChangeDays       int                         `json:"activity_log_retention_change_days"`
	ActivityLogRetentionDebugDays        int                         `json:"activity_log_retention_debug_days"`
	ActivityLogCompactionDays            int                         `json:"activity_log_compaction_days"`
	ActivityLogNightlyCompactionEnabled  bool                        `json:"activity_log_nightly_compaction_enabled"`
	ActivityLogFullDetailDays            int                         `json:"activity_log_full_detail_days"`
	AIJournalRetentionDays               int                         `json:"ai_journal_retention_days"`
	Embedding                            headEmbeddingConfig         `json:"embedding" mapstructure:"embedding"`
	Dedup                                headDedupConfig             `json:"dedup" mapstructure:"dedup"`
	DedupBoilerplate                     headDedupBoilerplateConfig  `json:"dedup_boilerplate" mapstructure:"dedup_boilerplate"`
	MetadataScoring                      headMetadataScoringConfig   `json:"metadata_scoring" mapstructure:"metadata_scoring"`
	Catalog                              headCatalogConfig           `json:"catalog" mapstructure:"catalog"`
	AIBackend                            headAIBackendConfig         `json:"ai_backend" mapstructure:"ai_backend"`
	APIRateLimitPerMinute                int                         `json:"api_rate_limit_per_minute"`
	JSONBodyLimitMB                      int                         `json:"json_body_limit_mb"`
	UploadBodyLimitMB                    int                         `json:"upload_body_limit_mb"`
	MasterListRetention                  int                         `json:"master_list_retention"`
	EnableAuth                           bool                        `json:"enable_auth"`
	EnableRateLimit                      bool                        `json:"enable_rate_limit"`
	ReviewApplyEnabled                   bool                        `json:"review_apply_enabled"`
	BulkApplyMaxItems                    int                         `json:"bulk_apply_max_items"`
	BasicAuthEnabled                     bool                        `json:"basic_auth_enabled"`
	BasicAuthUsername                    string                      `json:"basic_auth_username"`
	BasicAuthPassword                    string                      `json:"basic_auth_password"`
	OAuthEnabled                         bool                        `json:"oauth_enabled"`
	OAuthGithubClientID                  string                      `json:"oauth_github_client_id"`
	OAuthGithubClientSecret              string                      `json:"oauth_github_client_secret"`
	OAuthGoogleClientID                  string                      `json:"oauth_google_client_id"`
	OAuthGoogleClientSecret              string                      `json:"oauth_google_client_secret"`
	OAuthRedirectBaseURL                 string                      `json:"oauth_redirect_base_url"`
	OAuthAllowedEmails                   string                      `json:"oauth_allowed_emails"`
	OAuthDefaultRole                     string                      `json:"oauth_default_role"`
	CFAccessTeamDomain                   string                      `json:"cf_access_team_domain"`
	CFAccessAUD                          string                      `json:"cf_access_aud"`
	OwnerEmail                           string                      `json:"owner_email"`
	ABSAPIEnabled                        bool                        `json:"abs_api_enabled"`
	ABSAuthModes                         string                      `json:"abs_auth_modes"`
	ABSJWTSecret                         string                      `json:"-" mapstructure:"abs_jwt_secret"`
	ABSAccessTokenTTL                    string                      `json:"abs_access_token_ttl"`
	ABSRefreshTokenTTL                   string                      `json:"abs_refresh_token_ttl"`
	ABSSessionMaxTTL                     string                      `json:"abs_session_max_ttl"`
	ABSRefreshGrace                      string                      `json:"abs_refresh_grace"`
	ABSServerVersion                     string                      `json:"abs_server_version"`
	ABSDefaultLibraryID                  string                      `json:"abs_default_library_id"`
	SMTPHost                             string                      `json:"-" mapstructure:"smtp_host"`
	SMTPPort                             int                         `json:"-" mapstructure:"smtp_port"`
	SMTPUsername                         string                      `json:"-" mapstructure:"smtp_username"`
	SMTPPassword                         string                      `json:"-" mapstructure:"smtp_password"`
	SMTPFrom                             string                      `json:"-" mapstructure:"smtp_from"`
	PublicBaseURL                        string                      `json:"-" mapstructure:"public_base_url"`
	CFAccountID                          string                      `json:"-" mapstructure:"cf_account_id"`
	CFGuestsPolicyID                     string                      `json:"-" mapstructure:"cf_guests_policy_id"`
	CFPolicyAPIToken                     string                      `json:"-" mapstructure:"cf_policy_api_token"`
	CFGuestServiceTokens                 bool                        `json:"-" mapstructure:"cf_guest_service_tokens"`
	CFServiceAuthPolicyID                string                      `json:"-" mapstructure:"cf_service_auth_policy_id"`
	InviteEmailServiceToken              bool                        `json:"-" mapstructure:"invite_email_service_token"`
	TunnelConnectorAddrs                 string                      `json:"-" mapstructure:"tunnel_connector_addrs"`
	AllowedPeerCIDRs                     string                      `json:"-" mapstructure:"allowed_peer_cidrs"`
	LoginSaturatedRate                   string                      `json:"-" mapstructure:"login_saturated_rate"`
	LoginSaturatedBurst                  string                      `json:"-" mapstructure:"login_saturated_burst"`
	CacheInvalidateOnBookUpdate          bool                        `json:"cache_invalidate_on_book_update"`
	MetadataFetchCacheTTLDays            int                         `json:"metadata_fetch_cache_ttl_days"`
	MetadataFetch                        headMetadataFetchConfig     `json:"metadata_fetch" mapstructure:"metadata_fetch"`
	BootstrapKeyTTL                      string                      `json:"-" mapstructure:"bootstrap_key_ttl"`
	BootstrapKeyTTLDays                  int                         `json:"-" mapstructure:"bootstrap_key_ttl_days"`
	PurgeSoftDeletedAfterDays            int                         `json:"purge_soft_deleted_after_days"`
	PurgeSoftDeletedDeleteFiles          bool                        `json:"purge_soft_deleted_delete_files"`
	BackupRetentionDays                  int                         `json:"backup_retention_days"`
	LogLevel                             string                      `json:"log_level"`
	ITunes                               headITunesConfig            `json:"itunes" mapstructure:"itunes"`
	DelugeWebURL                         string                      `json:"deluge_web_url"`
	DelugeWebPassword                    string                      `json:"deluge_web_password"`
	DelugeDiscoveryLabel                 string                      `json:"deluge_discovery_label"`
	DelugeDiscoveryEnabled               bool                        `json:"deluge_discovery_enabled"`
	DelugeMoveEnabled                    bool                        `json:"deluge_move_enabled"`
	ProtectedPaths                       []string                    `json:"protected_paths"`
	AutoUpdate                           headAutoUpdateConfig        `json:"auto_update" mapstructure:"auto_update"`
	Maintenance                          headMaintenanceConfig       `json:"maintenance" mapstructure:"maintenance"`
	DownloadClient                       headDownloadClientConfig    `json:"download_client"`
	AutoRenameOnApply                    bool                        `json:"auto_rename_on_apply"`
	AutoWriteTagsOnApply                 bool                        `json:"auto_write_tags_on_apply"`
	VerifyAfterWrite                     bool                        `json:"verify_after_write"`
	Scheduled                            headScheduledTasksConfig    `json:"scheduled" mapstructure:"scheduled"`
	Plugins                              map[string]headPluginConfig `json:"plugins"`
	SupportedExtensions                  []string                    `json:"supported_extensions"`
	ExcludePatterns                      []string                    `json:"exclude_patterns"`
	Tools                                headToolsConfig             `json:"tools"`
	WriteStartupReadOnlyKey              bool                        `json:"write_startup_readonly_key" mapstructure:"write_startup_readonly_key"`
	DisablePerUserSearchFilters          bool                        `json:"disable_per_user_search_filters"`
	Search                               headSearchConfig            `json:"search" mapstructure:"search"`
	EnabledSortIndexes                   []string                    `json:"enabled_sort_indexes" mapstructure:"enabled_sort_indexes"`
}

// headWhisperEndpoint is HEAD's config.WhisperEndpoint.
type headWhisperEndpoint struct {
	URL          string   `json:"url"         mapstructure:"url"`
	Concurrency  int      `json:"concurrency" mapstructure:"concurrency"`
	Label        string   `json:"label"       mapstructure:"label"`
	Priority     int      `json:"priority" mapstructure:"priority"`
	RequireGPU   bool     `json:"require_gpu" mapstructure:"require_gpu"`
	Capabilities []string `json:"capabilities" mapstructure:"capabilities"`
}

// headSearchConfig is HEAD's config.SearchConfig.
type headSearchConfig struct {
	ResultCache headSearchResultCacheConfig `json:"result_cache" mapstructure:"result_cache"`
}

// headSearchResultCacheConfig is HEAD's config.SearchResultCacheConfig.
type headSearchResultCacheConfig struct {
	Enabled     bool  `json:"enabled" mapstructure:"enabled"`
	MaxBytes    int64 `json:"max_bytes" mapstructure:"max_bytes"`
	WaitSeconds int   `json:"wait_seconds" mapstructure:"wait_seconds"`
}

// headMetadataFetchConfig is HEAD's config.MetadataFetchConfig.
type headMetadataFetchConfig struct {
	SharedCache            bool                             `json:"shared_cache" mapstructure:"shared_cache"`
	CatalogStage           bool                             `json:"catalog_stage" mapstructure:"catalog_stage"`
	FolderParsePersonTitle bool                             `json:"folder_parse_person_title" mapstructure:"folder_parse_person_title"`
	NegativeTTLDays        headMetadataFetchNegativeTTLDays `json:"negative_ttl_days" mapstructure:"negative_ttl_days"`
}

// headMetadataFetchNegativeTTLDays is HEAD's config.MetadataFetchNegativeTTLDays.
type headMetadataFetchNegativeTTLDays struct {
	Audible     int `json:"audible" mapstructure:"audible"`
	Audnexus    int `json:"audnexus" mapstructure:"audnexus"`
	OpenLibrary int `json:"openlibrary" mapstructure:"openlibrary"`
	Google      int `json:"google" mapstructure:"google"`
}

// headAIEndpoint is HEAD's config.AIEndpoint.
type headAIEndpoint struct {
	ID               string            `json:"id"       mapstructure:"id"`
	Label            string            `json:"label"    mapstructure:"label"`
	Protocol         string            `json:"protocol" mapstructure:"protocol"`
	URL              string            `json:"url"      mapstructure:"url"`
	ChatModel        string            `json:"chat_model"  mapstructure:"chat_model"`
	EmbedModel       string            `json:"embed_model" mapstructure:"embed_model"`
	CapabilityModels map[string]string `json:"capability_models,omitempty" mapstructure:"capability_models"`
	Priority         int               `json:"priority" mapstructure:"priority"`
	Concurrency      int               `json:"concurrency" mapstructure:"concurrency"`
	Enabled          bool              `json:"enabled"     mapstructure:"enabled"`
	Capabilities     []string          `json:"capabilities" mapstructure:"capabilities"`
	Labels           []string          `json:"labels,omitempty"   mapstructure:"labels"`
	Features         []string          `json:"features,omitempty" mapstructure:"features"`
	RequireGPU       bool              `json:"require_gpu" mapstructure:"require_gpu"`
	AuthRef          string            `json:"auth_ref,omitempty" mapstructure:"auth_ref"`
	HostRoots        map[string]string `json:"host_roots,omitempty" mapstructure:"host_roots"`
}

// headLibraryRef is HEAD's config.LibraryRef.
type headLibraryRef struct {
	ITLPath string `json:"itl_path" mapstructure:"itl_path"`
	XMLPath string `json:"xml_path" mapstructure:"xml_path"`
	Frozen  bool   `json:"frozen"   mapstructure:"frozen"`
}

// headLibrarySet is HEAD's config.LibrarySet.
type headLibrarySet struct {
	Original     headLibraryRef `json:"original" mapstructure:"original"`
	AO           headLibraryRef `json:"ao" mapstructure:"ao"`
	PointedAt    string         `json:"pointed_at" mapstructure:"pointed_at"`
	ImportSource string         `json:"import_source" mapstructure:"import_source"`
}

// headToolMode is HEAD's tools.ToolMode.
type headToolMode string

// headToolConfig is HEAD's tools.ToolConfig.
type headToolConfig struct {
	Mode       headToolMode `json:"mode"`
	CustomPath string       `json:"custom_path"`
}

// headToolsConfig is HEAD's tools.ToolsConfig.
type headToolsConfig struct {
	ManagedDir          string         `json:"managed_dir"`
	Ollama              headToolConfig `json:"ollama"`
	Fpcalc              headToolConfig `json:"fpcalc"`
	AllowPeriodicOllama bool           `json:"allow_periodic_ollama"`
	OllamaDebounceMin   int            `json:"ollama_debounce_min"`
}
