// file: internal/books/config.go
// version: 1.0.0
// guid: 1f5450cc-b423-4507-8227-57791d3c26ec
// last-edited: 2026-11-19

package books

import (
	"runtime"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/tasks"
)

// Books' config sections (F3-07 slice b; 05-config sections 1.3.2 and 1.12.3).
//
// One section per modules.books.<name> of the rename table's section table,
// plus the three the explicit rows add (identity, scheduler, maintenance),
// holding every key a class-k or class-s row of the table targets. The Go
// types are HEAD's own (config.Config's field types, so a HEAD value stages
// into its key unchanged, with no converter but the schedule ones); the
// defaults are HEAD's registered defaults (internal/books/cutover/testdata/
// head_defaults.json) with the two computed ones, concurrent_scans and
// playlist_dir, computed here. TestBooksSettings_DefaultsMatchHEAD reads the
// frozen HEAD table independently and fails on any drift.
//
// Protection: a key the rename table marks admin (HEAD's protected-field
// classes, 05-config section 1.3.7) is ProtectAdmin, except the owner trust
// root (modules.books.identity.* and modules.books.cfaccess.allowed_emails,
// ProtectOwner) and the two operator-only secrets (abs.jwt_secret and
// cfguests.policy_api_token, ProtectOperator). Reload: live unless HEAD
// itself reads the value once at start (search.enabled_sort_indexes,
// dedup.chromem_lazy), which is restart; the per-key audit of the rest
// belongs to the consumers' move onto the facade (seams.d/F3-07.md).
//
// modules.books.plugins.<id>.settings is a secret record with no key
// (05-config 1.12.3), so no section declares it.
//
// Nothing reads these handles until the facade lands: ABO's boot leaves
// Config.CoreConfigBoot off, so the sections are registered only by tests
// and by a boot that sets the gate.

// ptr returns a pointer to v, for the *float64 keys whose HEAD default is set.
func ptr[T any](v T) *T { return &v }

// dur converts a time.Duration into the declaration type.
func dur(d time.Duration) config.Duration { return config.Duration(d) }

// booksProtectionWaivers are the keys whose name has one of 05-config 1.3.7's
// shapes (a URL, a credential token) and that carry no protection class, each
// with the reason. HEAD classed none of them and the rename table's admin
// column is false for each, so raising one would change its retired names'
// verdict from warn to refuse (and regenerate the golden): an owner decision
// that seams.d/F3-07.md surfaces. TestBooksSettings_ShapeRuleClassified pins
// the set.
var booksProtectionWaivers = map[config.Path]string{
	"modules.books.cfguests.guest_service_tokens":       "a boolean switch (HEAD cf_guest_service_tokens), not a credential",
	"modules.books.cfguests.invite_email_service_token": "a boolean switch (HEAD invite_email_service_token), not a credential",
	"modules.books.embedding.base_url":                  "HEAD classed this key none and the rename table's admin column is false; raising it is a retired-name verdict change for the owner to decide",
	"modules.books.whisper.endpoints":                   "HEAD classed this key none and the rename table's admin column is false; raising it is a retired-name verdict change for the owner to decide",
	"modules.books.whisper.remote_url":                  "HEAD classed this key none and the rename table's admin column is false; raising it is a retired-name verdict change for the owner to decide",
}

// booksSection declares one books section with the waivers of its own keys.
func booksSection[T any](path config.Path, defaults T) module.SettingsSection {
	own := map[config.Path]string{}
	for p, reason := range booksProtectionWaivers {
		if strings.HasPrefix(string(p), string(path)+".") {
			own[p] = reason
		}
	}
	if len(own) == 0 {
		return module.Settings(config.Define(path, defaults))
	}
	return module.Settings(config.Define(path, defaults, config.ProtectionWaivers(own)))
}

// booksSettings returns books' Manifest.Settings.
func booksSettings() []module.SettingsSection {
	return []module.SettingsSection{
		booksSection("modules.books.abs", absDefaults()),
		booksSection("modules.books.acoustid", acoustidDefaults()),
		booksSection("modules.books.activity", activityDefaults()),
		booksSection("modules.books.ai", aiDefaults()),
		booksSection("modules.books.cache", cacheDefaults()),
		booksSection("modules.books.catalog", catalogDefaults()),
		booksSection("modules.books.cfaccess", cfaccessDefaults()),
		booksSection("modules.books.cfguests", cfguestsDefaults()),
		booksSection("modules.books.chapters", chaptersDefaults()),
		booksSection("modules.books.db_census", dbCensusDefaults()),
		booksSection("modules.books.dedup", dedupDefaults()),
		booksSection("modules.books.download", downloadDefaults()),
		booksSection("modules.books.embedding", embeddingDefaults()),
		booksSection("modules.books.fingerprint", fingerprintDefaults()),
		booksSection("modules.books.identity", identityDefaults()),
		booksSection("modules.books.itunes", itunesDefaults()),
		booksSection("modules.books.maintenance", maintenanceDefaults()),
		booksSection("modules.books.master_list", masterListDefaults()),
		booksSection("modules.books.metadata", metadataDefaults()),
		booksSection("modules.books.openlibrary", openlibraryDefaults()),
		booksSection("modules.books.operations", operationsDefaults()),
		booksSection("modules.books.organize", organizeDefaults()),
		booksSection("modules.books.playlist", playlistDefaults()),
		booksSection("modules.books.purge", purgeDefaults()),
		booksSection("modules.books.quota", quotaDefaults()),
		booksSection("modules.books.review", reviewDefaults()),
		booksSection("modules.books.scan", scanDefaults()),
		booksSection("modules.books.scheduler", schedulerDefaults()),
		booksSection("modules.books.search", searchDefaults()),
		booksSection("modules.books.setup", setupDefaults()),
		booksSection("modules.books.tags", tagsDefaults()),
		booksSection("modules.books.tools", toolsDefaults()),
		booksSection("modules.books.versions", versionsDefaults()),
		booksSection("modules.books.whisper", whisperDefaults()),
	}
}

type booksDedupKindConfidence struct {
	MinConfidence float64 `json:"min_confidence,omitempty"`
	MaxConfidence float64 `json:"max_confidence,omitempty"`
}

type booksEmbeddingModelThresholds struct {
	High float64 `json:"high"`
	Low  float64 `json:"low"`
}

type booksWhisperEndpoint struct {
	URL          string   `json:"url"`
	Concurrency  int      `json:"concurrency"`
	Label        string   `json:"label"`
	Priority     int      `json:"priority"`
	RequireGPU   bool     `json:"require_gpu"`
	Capabilities []string `json:"capabilities"`
}

type absSettings struct {
	AccessTokenTTL               string      `json:"access_token_ttl" protect:"admin"`
	APIEnabled                   bool        `json:"api_enabled" protect:"admin"`
	AuthModes                    string      `json:"auth_modes" protect:"admin"`
	AuthProbeEnabled             bool        `json:"auth_probe_enabled"`
	DefaultLibraryID             string      `json:"default_library_id"`
	ITunesPositionBackfillUserID string      `json:"itunes_position_backfill_user_id"`
	JWTSecret                    secrets.Ref `json:"jwt_secret" protect:"operator"`
	RefreshGrace                 string      `json:"refresh_grace" protect:"admin"`
	RefreshTokenTTL              string      `json:"refresh_token_ttl" protect:"admin"`
	ServerVersion                string      `json:"server_version"`
	SessionMaxTTL                string      `json:"session_max_ttl" protect:"admin"`
}

type acoustidSettings struct {
	APIKey secrets.Ref `json:"api_key"`
}

type activitySettings struct {
	Backend                     string `json:"backend" protect:"admin"`
	LogCompactionDays           int    `json:"log_compaction_days"`
	LogFullDetailDays           int    `json:"log_full_detail_days"`
	LogNightlyCompactionEnabled bool   `json:"log_nightly_compaction_enabled"`
	LogRetentionChangeDays      int    `json:"log_retention_change_days"`
	LogRetentionDebugDays       int    `json:"log_retention_debug_days"`
}

type aiSettings struct {
	CoverArtModel      string `json:"cover_art_model"`
	EnableAIParsing    bool   `json:"enable_ai_parsing"`
	FilenameParseModel string `json:"filename_parse_model"`
}

type cacheSettings struct {
	InvalidateOnBookUpdate               bool `json:"invalidate_on_book_update"`
	LibraryCountsCacheMinIntervalSeconds int  `json:"library_counts_cache_min_interval_seconds"`
	ListWarmerHeapDeltaMB                int  `json:"list_warmer_heap_delta_mb"`
	ListWarmerTrickleIntervalMS          int  `json:"list_warmer_trickle_interval_ms"`
}

type catalogSettings struct {
	Enabled              bool    `json:"enabled"`
	HarvestRateFraction  float64 `json:"harvest_rate_fraction"`
	Language             string  `json:"language"`
	Marketplace          string  `json:"marketplace"`
	MaxProductsPerAuthor int     `json:"max_products_per_author"`
}

type cfaccessSettings struct {
	AllowedEmails string `json:"allowed_emails" protect:"owner"`
}

type cfguestsSettings struct {
	AccountID               string      `json:"account_id"`
	GuestServiceTokens      bool        `json:"guest_service_tokens"`
	GuestsPolicyID          string      `json:"guests_policy_id"`
	InviteEmailServiceToken bool        `json:"invite_email_service_token"`
	PolicyAPIToken          secrets.Ref `json:"policy_api_token" protect:"operator"`
	ServiceAuthPolicyID     string      `json:"service_auth_policy_id"`
}

type chaptersSettings struct {
	ChapterConsolidationThresholdMin int     `json:"chapter_consolidation_threshold_min"`
	FragmentWindowContainmentMin     float64 `json:"fragment_window_containment_min"`
	RepairChapterMaxMin              int     `json:"repair_chapter_max_min"`
}

type dbCensusSettings struct {
	ExactCooldownHours int `json:"exact_cooldown_hours"`
	ExactReadMBPerSec  int `json:"exact_read_mb_per_sec"`
}

type dedupBoilerplate struct {
	ExtraPrefixPatterns []string `json:"extra_prefix_patterns"`
	ExtraTitlePatterns  []string `json:"extra_title_patterns"`
}

type dedupSignals struct {
	BandCertainMin float64                             `json:"band_certain_min"`
	BandHighMin    float64                             `json:"band_high_min"`
	BandMediumMin  float64                             `json:"band_medium_min"`
	BandReviewMin  float64                             `json:"band_review_min"`
	Confidence     map[string]booksDedupKindConfidence `json:"confidence"`
}

type dedupSettings struct {
	AuthorHighThreshold        float64                                  `json:"author_high_threshold"`
	AuthorLowThreshold         float64                                  `json:"author_low_threshold"`
	AutoMergeEnabled           bool                                     `json:"auto_merge_enabled"`
	AutoResolveEnabled         bool                                     `json:"auto_resolve_enabled"`
	Boilerplate                dedupBoilerplate                         `json:"boilerplate"`
	BookHighThreshold          float64                                  `json:"book_high_threshold"`
	BookLowThreshold           float64                                  `json:"book_low_threshold"`
	ChromemLazy                bool                                     `json:"chromem_lazy" reload:"restart"`
	EmbeddingThresholdsByModel map[string]booksEmbeddingModelThresholds `json:"embedding_thresholds_by_model"`
	EmbeddingsEnabled          bool                                     `json:"embeddings_enabled"`
	LLMAutoMergeHighConfidence bool                                     `json:"llm_auto_merge_high_confidence"`
	OnImportViaScheduler       bool                                     `json:"on_import_via_scheduler"`
	ReviewModel                string                                   `json:"review_model"`
	Signals                    dedupSignals                             `json:"signals"`
}

type downloadSettings struct {
	DiscoveryEnabled bool   `json:"discovery_enabled"`
	DiscoveryLabel   string `json:"discovery_label"`
	MoveEnabled      bool   `json:"move_enabled"`
}

type embeddingSettings struct {
	BaseURL               string `json:"base_url"`
	Dimensions            int    `json:"dimensions"`
	Enabled               bool   `json:"enabled"`
	Model                 string `json:"model"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	VectorBackend         string `json:"vector_backend"`
}

type fingerprintSettings struct {
	FPParallelWorkers    int      `json:"fp_parallel_workers"`
	LengthSec            int      `json:"length_sec"`
	RemoteWorkersEnabled bool     `json:"remote_workers_enabled"`
	WindowReferenceTools string   `json:"window_reference_tools"`
	WorkerToolVersions   []string `json:"worker_tool_versions"`
}

type identitySettings struct {
	CFAccessAud        string `json:"cf_access_aud" protect:"owner"`
	CFAccessTeamDomain string `json:"cf_access_team_domain" protect:"owner"`
	OwnerEmail         string `json:"owner_email" protect:"owner"`
}

type itunesLibrariesAo struct {
	Frozen  bool   `json:"frozen" protect:"admin"`
	ITLPath string `json:"itl_path" protect:"admin"`
	XMLPath string `json:"xml_path" protect:"admin"`
}

type itunesLibrariesOriginal struct {
	Frozen  bool   `json:"frozen" protect:"admin"`
	ITLPath string `json:"itl_path" protect:"admin"`
	XMLPath string `json:"xml_path" protect:"admin"`
}

type itunesLibraries struct {
	AO           itunesLibrariesAo       `json:"ao"`
	ImportSource string                  `json:"import_source" protect:"admin"`
	Original     itunesLibrariesOriginal `json:"original"`
	PointedAt    string                  `json:"pointed_at" protect:"admin"`
}

type itunesSettings struct {
	Libraries        itunesLibraries `json:"libraries"`
	LibraryReadPath  string          `json:"library_read_path" protect:"admin"`
	LibraryWritePath string          `json:"library_write_path" protect:"admin"`
	MediaRoot        string          `json:"media_root" protect:"admin"`
	PathTrimEnabled  bool            `json:"path_trim_enabled"`
	WindowsRootPath  string          `json:"windows_root_path" protect:"admin"`
}

type maintenanceSettings struct {
	AcoustIDBackfill     bool `json:"acoustid_backfill"`
	AcoustIDNightlyLimit int  `json:"acoustid_nightly_limit"`
	AcoustIDOnlineLookup bool `json:"acoustid_online_lookup"`
	AuthorSplit          bool `json:"author_split"`
	DBOptimize           bool `json:"db_optimize"`
	DedupRefresh         bool `json:"dedup_refresh"`
	LibraryOrganize      bool `json:"library_organize"`
	LibraryScan          bool `json:"library_scan"`
	LibrarySizeRefresh   bool `json:"library_size_refresh"`
	MetadataRefresh      bool `json:"metadata_refresh"`
	PurgeDeleted         bool `json:"purge_deleted"`
	PurgeOldLogs         bool `json:"purge_old_logs"`
	Reconcile            bool `json:"reconcile"`
	SeriesPrune          bool `json:"series_prune"`
	TombstoneCleanup     bool `json:"tombstone_cleanup"`
}

type masterListSettings struct {
	Retention int `json:"retention"`
}

type metadataFetchNegativeTTLDays struct {
	Audible     int `json:"audible"`
	Audnexus    int `json:"audnexus"`
	Google      int `json:"google"`
	OpenLibrary int `json:"openlibrary"`
}

type metadataFetch struct {
	CatalogStage           bool                         `json:"catalog_stage"`
	FolderParsePersonTitle bool                         `json:"folder_parse_person_title"`
	NegativeTTLDays        metadataFetchNegativeTTLDays `json:"negative_ttl_days"`
	SharedCache            bool                         `json:"shared_cache"`
}

type metadataScoring struct {
	BulkFetchWorkers              int       `json:"bulk_fetch_workers"`
	CompilationPenalty            *float64  `json:"compilation_penalty"`
	DurationTierMultipliers       []float64 `json:"duration_tier_multipliers"`
	DurationTierScores            []float64 `json:"duration_tier_scores"`
	EmbeddingBestMatch            float64   `json:"embedding_best_match"`
	EmbeddingEnabled              bool      `json:"embedding_enabled"`
	EmbeddingMinScore             float64   `json:"embedding_min_score"`
	F1MinScore                    *float64  `json:"f1_min_score"`
	LLMEnabled                    bool      `json:"llm_enabled"`
	LLMRerankEpsilon              float64   `json:"llm_rerank_epsilon"`
	LLMRerankTopK                 int       `json:"llm_rerank_top_k"`
	RichMetadataBonusCap          *float64  `json:"rich_metadata_bonus_cap"`
	RichMetadataFieldBonus        float64   `json:"rich_metadata_field_bonus"`
	SeriesNameMatchBoost          float64   `json:"series_name_match_boost"`
	SeriesNumberExactBoost        float64   `json:"series_number_exact_boost"`
	SeriesNumberWrongPenalty      float64   `json:"series_number_wrong_penalty"`
	SourceFanoutWorkers           int       `json:"source_fanout_workers"`
	TranscriptionAuthorBoost      float64   `json:"transcription_author_boost"`
	TranscriptionNarratorBoost    float64   `json:"transcription_narrator_boost"`
	TranscriptionTitleExactBoost  float64   `json:"transcription_title_exact_boost"`
	TranscriptionTitleSubstrBoost float64   `json:"transcription_title_substr_boost"`
	WriteBackWorkers              int       `json:"write_back_workers"`
}

type metadataSettings struct {
	AuthorityEvidenceEnabled        bool            `json:"authority_evidence_enabled"`
	CandidateFetchWorkers           int             `json:"candidate_fetch_workers"`
	Fetch                           metadataFetch   `json:"fetch"`
	FetchCacheTTLDays               int             `json:"fetch_cache_ttl_days"`
	GoogleBooksBackgroundDailyLimit int             `json:"google_books_background_daily_limit"`
	GoogleBooksDailyLimit           int             `json:"google_books_daily_limit"`
	GoogleBooksFallbackDailyLimit   int             `json:"google_books_fallback_daily_limit"`
	IdentificationAdvanceEnabled    bool            `json:"identification_advance_enabled"`
	ReviewModel                     string          `json:"review_model"`
	Scoring                         metadataScoring `json:"scoring"`
}

type openlibrarySettings struct {
	DumpDir     string `json:"dump_dir" protect:"admin"`
	DumpEnabled bool   `json:"dump_enabled"`
}

type operationsSettings struct {
	StaleThresholdMinutes int `json:"stale_threshold_minutes"`
}

type organizeSettings struct {
	AutoOrganize         bool   `json:"auto_organize"`
	AutoRenameOnApply    bool   `json:"auto_rename_on_apply"`
	FileNamingPattern    string `json:"file_naming_pattern" protect:"admin"`
	FolderNamingPattern  string `json:"folder_naming_pattern" protect:"admin"`
	OrganizationStrategy string `json:"organization_strategy"`
}

type playlistSettings struct {
	Dir string `json:"dir" protect:"admin"`
}

type purgeSettings struct {
	SoftDeletedAfterDays   int  `json:"soft_deleted_after_days"`
	SoftDeletedDeleteFiles bool `json:"soft_deleted_delete_files"`
}

type quotaSettings struct {
	DiskQuotaPercent int  `json:"disk_quota_percent"`
	EnableDiskQuota  bool `json:"enable_disk_quota"`
	EnableUserQuotas bool `json:"enable_user_quotas"`
}

type reviewSettings struct {
	BulkApplyMaxItems  int  `json:"bulk_apply_max_items"`
	ReviewApplyEnabled bool `json:"review_apply_enabled"`
}

type scanSettings struct {
	AutoScanDebounceSeconds   int      `json:"auto_scan_debounce_seconds"`
	AutoScanEnabled           bool     `json:"auto_scan_enabled"`
	CoalesceShatteredSiblings bool     `json:"coalesce_shattered_siblings"`
	ConcurrentScans           int      `json:"concurrent_scans"`
	ExcludePatterns           []string `json:"exclude_patterns"`
	MinBookSizeBytes          int64    `json:"min_book_size_bytes"`
	MinRescanAgeHours         int      `json:"min_rescan_age_hours"`
	OnStartup                 bool     `json:"on_startup"`
	ProgressEvery             int      `json:"progress_every"`
	StanddownGraceSeconds     int      `json:"standdown_grace_seconds"`
	SupportedExtensions       []string `json:"supported_extensions"`
}

type schedulerSchedules struct {
	AcoustIDBackfill         tasks.ScheduleConfig `json:"acoustid_backfill"`
	AIDedupBatch             tasks.ScheduleConfig `json:"ai_dedup_batch"`
	ASINBackfill             tasks.ScheduleConfig `json:"asin_backfill"`
	AuthorSplit              tasks.ScheduleConfig `json:"author_split"`
	CandidateFetch           tasks.ScheduleConfig `json:"candidate_fetch"`
	CatalogHarvest           tasks.ScheduleConfig `json:"catalog_harvest"`
	DBOptimize               tasks.ScheduleConfig `json:"db_optimize"`
	DedupRefresh             tasks.ScheduleConfig `json:"dedup_refresh"`
	LabelRefinement          tasks.ScheduleConfig `json:"label_refinement"`
	LibraryScan              tasks.ScheduleConfig `json:"library_scan"`
	LibraryScanFull          tasks.ScheduleConfig `json:"library_scan_full"`
	MetadataRefresh          tasks.ScheduleConfig `json:"metadata_refresh"`
	MetadataUpgrade          tasks.ScheduleConfig `json:"metadata_upgrade"`
	Reconcile                tasks.ScheduleConfig `json:"reconcile"`
	ResolveProductionAuthors tasks.ScheduleConfig `json:"resolve_production_authors"`
	SeriesPrune              tasks.ScheduleConfig `json:"series_prune"`
}

type schedulerSettings struct {
	Schedules schedulerSchedules `json:"schedules"`
}

type searchResultCache struct {
	Enabled     bool  `json:"enabled"`
	MaxBytes    int64 `json:"max_bytes"`
	WaitSeconds int   `json:"wait_seconds"`
}

type searchSettings struct {
	BleveDescriptionMaxChars    int               `json:"bleve_description_max_chars"`
	DisablePerUserSearchFilters bool              `json:"disable_per_user_search_filters"`
	EnabledSortIndexes          []string          `json:"enabled_sort_indexes" reload:"restart"`
	ResultCache                 searchResultCache `json:"result_cache"`
}

type setupSettings struct {
	Complete bool `json:"complete"`
}

type tagsSettings struct {
	AutoWriteTagsOnApply bool `json:"auto_write_tags_on_apply"`
	CreateBackups        bool `json:"create_backups"`
	EmbedCoverArt        bool `json:"embed_cover_art"`
	VerifyAfterWrite     bool `json:"verify_after_write"`
	WriteBackMetadata    bool `json:"write_back_metadata"`
}

type toolsFpcalc struct {
	CustomPath string `json:"custom_path" protect:"admin"`
	Mode       string `json:"mode" protect:"admin"`
}

type toolsOllama struct {
	CustomPath string `json:"custom_path" protect:"admin"`
	Mode       string `json:"mode" protect:"admin"`
}

type toolsSettings struct {
	AllowPeriodicOllama bool        `json:"allow_periodic_ollama"`
	Fpcalc              toolsFpcalc `json:"fpcalc"`
	ManagedDir          string      `json:"managed_dir" protect:"admin"`
	Ollama              toolsOllama `json:"ollama"`
	OllamaDebounceMin   int         `json:"ollama_debounce_min"`
}

type versionsSettings struct {
	KeptSweepApplyEnabled      bool `json:"kept_sweep_apply_enabled"`
	RetireVersionsApplyEnabled bool `json:"retire_versions_apply_enabled"`
}

type whisperSettings struct {
	BatchSize    int                    `json:"batch_size"`
	BatchSleepMS int                    `json:"batch_sleep_ms"`
	ClipCacheDir string                 `json:"clip_cache_dir" protect:"admin"`
	Endpoints    []booksWhisperEndpoint `json:"endpoints"`
	MaxInFlight  int                    `json:"max_in_flight"`
	RemoteURL    string                 `json:"remote_url"`
	Requires     []string               `json:"requires"`
}

// abs
func absDefaults() absSettings {
	return absSettings{
		AccessTokenTTL:               "720h",
		APIEnabled:                   false,
		AuthModes:                    "cf,jwt",
		AuthProbeEnabled:             false,
		DefaultLibraryID:             "b5e3a5b2-a76e-471f-b18b-915e4716d053",
		ITunesPositionBackfillUserID: "",
		JWTSecret:                    secrets.Ref{},
		RefreshGrace:                 "10m",
		RefreshTokenTTL:              "1440h",
		ServerVersion:                "2.36.0",
		SessionMaxTTL:                "0",
	}
}

// acoustid
func acoustidDefaults() acoustidSettings {
	return acoustidSettings{
		APIKey: secrets.Ref{},
	}
}

// activity
func activityDefaults() activitySettings {
	return activitySettings{
		Backend:                     "pebble",
		LogCompactionDays:           0,
		LogFullDetailDays:           0,
		LogNightlyCompactionEnabled: true,
		LogRetentionChangeDays:      0,
		LogRetentionDebugDays:       0,
	}
}

// ai
func aiDefaults() aiSettings {
	return aiSettings{
		CoverArtModel:      "gpt-5-mini",
		EnableAIParsing:    true,
		FilenameParseModel: "gpt-5-mini",
	}
}

// cache
func cacheDefaults() cacheSettings {
	return cacheSettings{
		InvalidateOnBookUpdate:               false,
		LibraryCountsCacheMinIntervalSeconds: 600,
		ListWarmerHeapDeltaMB:                4096,
		ListWarmerTrickleIntervalMS:          10000,
	}
}

// catalog
func catalogDefaults() catalogSettings {
	return catalogSettings{
		Enabled:              false,
		HarvestRateFraction:  0.5,
		Language:             "english",
		Marketplace:          "us",
		MaxProductsPerAuthor: 2000,
	}
}

// cfaccess
func cfaccessDefaults() cfaccessSettings {
	return cfaccessSettings{
		AllowedEmails: "",
	}
}

// cfguests
func cfguestsDefaults() cfguestsSettings {
	return cfguestsSettings{
		AccountID:               "",
		GuestServiceTokens:      false,
		GuestsPolicyID:          "",
		InviteEmailServiceToken: false,
		PolicyAPIToken:          secrets.Ref{},
		ServiceAuthPolicyID:     "",
	}
}

// chapters
func chaptersDefaults() chaptersSettings {
	return chaptersSettings{
		ChapterConsolidationThresholdMin: 10,
		FragmentWindowContainmentMin:     0.5,
		RepairChapterMaxMin:              120,
	}
}

// db_census
func dbCensusDefaults() dbCensusSettings {
	return dbCensusSettings{
		ExactCooldownHours: 6,
		ExactReadMBPerSec:  50,
	}
}

// dedup
func dedupDefaults() dedupSettings {
	return dedupSettings{
		AuthorHighThreshold: 0.92,
		AuthorLowThreshold:  0.8,
		AutoMergeEnabled:    true,
		AutoResolveEnabled:  false,
		Boilerplate: dedupBoilerplate{
			ExtraPrefixPatterns: []string{},
			ExtraTitlePatterns:  []string{},
		},
		BookHighThreshold:          0.95,
		BookLowThreshold:           0.85,
		ChromemLazy:                false,
		EmbeddingThresholdsByModel: map[string]booksEmbeddingModelThresholds{},
		EmbeddingsEnabled:          true,
		LLMAutoMergeHighConfidence: false,
		OnImportViaScheduler:       false,
		ReviewModel:                "gpt-5-mini",
		Signals: dedupSignals{
			BandCertainMin: 97,
			BandHighMin:    90,
			BandMediumMin:  75,
			BandReviewMin:  60,
			Confidence:     map[string]booksDedupKindConfidence{},
		},
	}
}

// download
func downloadDefaults() downloadSettings {
	return downloadSettings{
		DiscoveryEnabled: false,
		DiscoveryLabel:   "",
		MoveEnabled:      false,
	}
}

// embedding
func embeddingDefaults() embeddingSettings {
	return embeddingSettings{
		BaseURL:               "",
		Dimensions:            3072,
		Enabled:               true,
		Model:                 "text-embedding-3-large",
		RequestTimeoutSeconds: 0,
		VectorBackend:         "hnsw",
	}
}

// fingerprint
func fingerprintDefaults() fingerprintSettings {
	return fingerprintSettings{
		FPParallelWorkers:    4,
		LengthSec:            120,
		RemoteWorkersEnabled: false,
		WindowReferenceTools: "",
		WorkerToolVersions:   []string{},
	}
}

// identity
func identityDefaults() identitySettings {
	return identitySettings{
		CFAccessAud:        "",
		CFAccessTeamDomain: "",
		OwnerEmail:         "",
	}
}

// itunes
func itunesDefaults() itunesSettings {
	return itunesSettings{
		Libraries: itunesLibraries{
			AO: itunesLibrariesAo{
				Frozen:  false,
				ITLPath: "",
				XMLPath: "",
			},
			ImportSource: "",
			Original: itunesLibrariesOriginal{
				Frozen:  false,
				ITLPath: "",
				XMLPath: "",
			},
			PointedAt: "",
		},
		LibraryReadPath:  "",
		LibraryWritePath: "",
		MediaRoot:        "",
		PathTrimEnabled:  false,
		WindowsRootPath:  "",
	}
}

// maintenance
func maintenanceDefaults() maintenanceSettings {
	return maintenanceSettings{
		AcoustIDBackfill:     false,
		AcoustIDNightlyLimit: 5000,
		AcoustIDOnlineLookup: false,
		AuthorSplit:          true,
		DBOptimize:           true,
		DedupRefresh:         true,
		LibraryOrganize:      false,
		LibraryScan:          false,
		LibrarySizeRefresh:   true,
		MetadataRefresh:      false,
		PurgeDeleted:         true,
		PurgeOldLogs:         true,
		Reconcile:            true,
		SeriesPrune:          true,
		TombstoneCleanup:     true,
	}
}

// master_list
func masterListDefaults() masterListSettings {
	return masterListSettings{
		Retention: 10,
	}
}

// metadata
func metadataDefaults() metadataSettings {
	return metadataSettings{
		AuthorityEvidenceEnabled: false,
		CandidateFetchWorkers:    0,
		Fetch: metadataFetch{
			CatalogStage:           false,
			FolderParsePersonTitle: true,
			NegativeTTLDays: metadataFetchNegativeTTLDays{
				Audible:     14,
				Audnexus:    14,
				Google:      7,
				OpenLibrary: 30,
			},
			SharedCache: true,
		},
		FetchCacheTTLDays:               180,
		GoogleBooksBackgroundDailyLimit: 0,
		GoogleBooksDailyLimit:           0,
		GoogleBooksFallbackDailyLimit:   0,
		IdentificationAdvanceEnabled:    false,
		ReviewModel:                     "gpt-5-mini",
		Scoring: metadataScoring{
			BulkFetchWorkers:              4,
			CompilationPenalty:            ptr(0.15),
			DurationTierMultipliers:       []float64{1.3, 1.2, 1.1, 1, 0.75, 0.5},
			DurationTierScores:            []float64{20, 15, 10, 0, -10, -20},
			EmbeddingBestMatch:            0.88,
			EmbeddingEnabled:              false,
			EmbeddingMinScore:             0.82,
			F1MinScore:                    ptr(0.35),
			LLMEnabled:                    false,
			LLMRerankEpsilon:              0.05,
			LLMRerankTopK:                 5,
			RichMetadataBonusCap:          ptr(0.15),
			RichMetadataFieldBonus:        0.05,
			SeriesNameMatchBoost:          1.4,
			SeriesNumberExactBoost:        2,
			SeriesNumberWrongPenalty:      0.5,
			SourceFanoutWorkers:           0,
			TranscriptionAuthorBoost:      1.6,
			TranscriptionNarratorBoost:    1.4,
			TranscriptionTitleExactBoost:  2,
			TranscriptionTitleSubstrBoost: 1.4,
			WriteBackWorkers:              4,
		},
	}
}

// openlibrary
func openlibraryDefaults() openlibrarySettings {
	return openlibrarySettings{
		DumpDir:     "",
		DumpEnabled: false,
	}
}

// operations
func operationsDefaults() operationsSettings {
	return operationsSettings{
		StaleThresholdMinutes: 30,
	}
}

// organize
func organizeDefaults() organizeSettings {
	return organizeSettings{
		AutoOrganize:         true,
		AutoRenameOnApply:    true,
		FileNamingPattern:    "{title} - {track:02d}",
		FolderNamingPattern:  "{author}/{series}/{title} ({print_year})",
		OrganizationStrategy: "auto",
	}
}

// playlist
func playlistDefaults() playlistSettings {
	return playlistSettings{
		Dir: "playlists",
	}
}

// purge
func purgeDefaults() purgeSettings {
	return purgeSettings{
		SoftDeletedAfterDays:   30,
		SoftDeletedDeleteFiles: false,
	}
}

// quota
func quotaDefaults() quotaSettings {
	return quotaSettings{
		DiskQuotaPercent: 80,
		EnableDiskQuota:  false,
		EnableUserQuotas: false,
	}
}

// review
func reviewDefaults() reviewSettings {
	return reviewSettings{
		BulkApplyMaxItems:  5000,
		ReviewApplyEnabled: false,
	}
}

// scan
func scanDefaults() scanSettings {
	return scanSettings{
		AutoScanDebounceSeconds:   30,
		AutoScanEnabled:           false,
		CoalesceShatteredSiblings: false,
		ConcurrentScans:           max(runtime.NumCPU(), 4),
		ExcludePatterns:           []string{},
		MinBookSizeBytes:          0,
		MinRescanAgeHours:         0,
		OnStartup:                 false,
		ProgressEvery:             0,
		StanddownGraceSeconds:     45,
		SupportedExtensions:       []string{".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma", ".opus", ".oga", ".wav", ".aiff", ".aif", ".mka", ".aax", ".aaxc"},
	}
}

// scheduler
func schedulerDefaults() schedulerSettings {
	return schedulerSettings{
		Schedules: schedulerSchedules{
			AcoustIDBackfill:         tasks.ScheduleConfig{Enabled: false, Every: dur(24 * time.Hour)},
			AIDedupBatch:             tasks.ScheduleConfig{Enabled: false, Every: dur(24 * time.Hour)},
			ASINBackfill:             tasks.ScheduleConfig{Enabled: true, Every: dur(6 * time.Hour)},
			AuthorSplit:              tasks.ScheduleConfig{Enabled: false},
			CandidateFetch:           tasks.ScheduleConfig{Enabled: true, Every: dur(6 * time.Hour)},
			CatalogHarvest:           tasks.ScheduleConfig{Enabled: true, Every: dur(6 * time.Hour)},
			DBOptimize:               tasks.ScheduleConfig{Enabled: false, Every: dur(24 * time.Hour)},
			DedupRefresh:             tasks.ScheduleConfig{Enabled: false, Every: dur(6 * time.Hour)},
			LabelRefinement:          tasks.ScheduleConfig{Enabled: false, Every: dur(168 * time.Hour)},
			LibraryScan:              tasks.ScheduleConfig{Enabled: true, Every: dur(6 * time.Hour)},
			LibraryScanFull:          tasks.ScheduleConfig{Enabled: true, Every: dur(168 * time.Hour)},
			MetadataRefresh:          tasks.ScheduleConfig{Enabled: false},
			MetadataUpgrade:          tasks.ScheduleConfig{Enabled: false},
			Reconcile:                tasks.ScheduleConfig{Enabled: false},
			ResolveProductionAuthors: tasks.ScheduleConfig{Enabled: false},
			SeriesPrune:              tasks.ScheduleConfig{Enabled: false},
		},
	}
}

// search
func searchDefaults() searchSettings {
	return searchSettings{
		BleveDescriptionMaxChars:    500,
		DisablePerUserSearchFilters: false,
		EnabledSortIndexes:          []string{"year", "created_at"},
		ResultCache: searchResultCache{
			Enabled:     true,
			MaxBytes:    134217728,
			WaitSeconds: 20,
		},
	}
}

// setup
func setupDefaults() setupSettings {
	return setupSettings{
		Complete: false,
	}
}

// tags
func tagsDefaults() tagsSettings {
	return tagsSettings{
		AutoWriteTagsOnApply: true,
		CreateBackups:        true,
		EmbedCoverArt:        true,
		VerifyAfterWrite:     true,
		WriteBackMetadata:    false,
	}
}

// tools
func toolsDefaults() toolsSettings {
	return toolsSettings{
		AllowPeriodicOllama: false,
		Fpcalc: toolsFpcalc{
			CustomPath: "",
			Mode:       "system",
		},
		ManagedDir: "/var/lib/audiobook-organizer/tools",
		Ollama: toolsOllama{
			CustomPath: "",
			Mode:       "system",
		},
		OllamaDebounceMin: 10,
	}
}

// versions
func versionsDefaults() versionsSettings {
	return versionsSettings{
		KeptSweepApplyEnabled:      false,
		RetireVersionsApplyEnabled: false,
	}
}

// whisper
func whisperDefaults() whisperSettings {
	return whisperSettings{
		BatchSize:    16,
		BatchSleepMS: 8000,
		ClipCacheDir: "",
		Endpoints:    []booksWhisperEndpoint{},
		MaxInFlight:  0,
		RemoteURL:    "",
		Requires:     []string{},
	}
}
