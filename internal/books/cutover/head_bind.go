// file: internal/books/cutover/head_bind.go
// version: 1.0.0
// guid: eb852def-4850-4a03-bc91-f4d78e6e85f8
// last-edited: 2026-11-11

// FROZEN COPY of HEAD (748fd787a) env binding list; read-only; deleted in R3 with the other HEAD-format readers (05-config §1.12.2, 03 §3.8).

package cutover

// headStateDirEnv and headAllowKeyRegenEnv are the two process-environment
// names HEAD reads outside viper: the secure state directory
// (config.SecureStateDirEnv, internal/config/state_dir.go) and the guard
// that permits generating a new settings encryption key
// (internal/books/boot.go allowKeyRegenEnv).
const (
	headStateDirEnv      = "ABK_STATE_DIR"
	headAllowKeyRegenEnv = "ABK_ALLOW_ENCRYPTION_KEY_REGEN"
)

// headBinding is one viper.BindEnv call HEAD makes: Field is the viper key
// (a json or mapstructure tag name, dotted for nested keys) and Env the
// environment variable names passed after it.
//
// cmd/root.go initConfig also calls viper.AutomaticEnv with no key replacer,
// so for every viper key the upper-cased key name is an environment name as
// well; this list is the explicit BindEnv set only.
type headBinding struct {
	Field string
	Env   []string
}

// headBindings is the recorded argument list of every BindEnv call HEAD makes
// while declaring defaults, grouped by source file, each group in call order.
// It was recorded by running HEAD's registrars against a recording
// defaultRegistrar (not grepped): 130 calls in config.go
// (registerConfigDefaults), 9 in abs_config.go (registerABSDefaults) and
// 16 in invite_config.go (registerInviteDefaults, a loop over the sixteen
// inviteEnvOnlySettings rows; the file is env_only_settings.go after F4-02).
var headBindings = []headBinding{
	// internal/config/config.go registerConfigDefaults (130 calls).
	{Field: "acoustid_api_key", Env: []string{"ACOUSTID_API_KEY"}},
	{Field: "fp_parallel_workers", Env: []string{"FP_PARALLEL_WORKERS"}},
	{Field: "fingerprint_length_sec", Env: []string{"FP_LENGTH_SEC"}},
	{Field: "whisper_clip_cache_dir", Env: []string{"WHISPER_CLIP_CACHE_DIR"}},
	{Field: "whisper_batch_sleep_ms", Env: []string{"WHISPER_BATCH_SLEEP_MS"}},
	{Field: "whisper_batch_size", Env: []string{"WHISPER_BATCH_SIZE"}},
	{Field: "whisper_max_in_flight", Env: []string{"WHISPER_MAX_IN_FLIGHT"}},
	{Field: "openai_base_url", Env: []string{"OPENAI_BASE_URL"}},
	{Field: "abs_auth_probe_enabled", Env: []string{"ABS_AUTH_PROBE"}},
	{Field: "retire_versions_apply_enabled", Env: []string{"RETIRE_VERSIONS_APPLY_ENABLED"}},
	{Field: "kept_sweep_apply_enabled", Env: []string{"KEPT_SWEEP_APPLY_ENABLED"}},
	{Field: "abs_itunes_position_backfill_user_id", Env: []string{"ABS_ITUNES_POSITION_BACKFILL_USER_ID"}},
	{Field: "otel_exporter_otlp_endpoint", Env: []string{"OTEL_EXPORTER_OTLP_ENDPOINT"}},
	{Field: "otel_metrics_otlp_endpoint", Env: []string{"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"}},
	{Field: "otel_metrics_otlp_interval", Env: []string{"OTEL_METRIC_EXPORT_INTERVAL"}},
	{Field: "otel_metrics_otlp_insecure", Env: []string{"OTEL_EXPORTER_OTLP_METRICS_INSECURE"}},
	{Field: "deluge_web_url", Env: []string{"DELUGE_WEB_URL"}},
	{Field: "deluge_web_password", Env: []string{"DELUGE_WEB_PASSWORD"}},
	{Field: "deluge_discovery_label", Env: []string{"DELUGE_DISCOVERY_LABEL"}},
	{Field: "deluge_discovery_enabled", Env: []string{"DELUGE_DISCOVERY_ENABLED"}},
	{Field: "deluge_move_enabled", Env: []string{"DELUGE_MOVE_ENABLED"}},
	{Field: "list_warmer_heap_delta_mb", Env: []string{"LIST_WARMER_HEAP_DELTA_MB", "LIST_WARMER_MAX_HEAP_MB"}},
	{Field: "list_warmer_trickle_interval_ms", Env: []string{"LIST_WARMER_TRICKLE_INTERVAL_MS"}},
	{Field: "library_counts_cache_min_interval_seconds", Env: []string{"LIBRARY_COUNTS_CACHE_MIN_INTERVAL_SECONDS"}},
	{Field: "bleve_description_max_chars", Env: []string{"BLEVE_DESCRIPTION_MAX_CHARS"}},
	{Field: "audible_base_url", Env: []string{"AUDIBLE_BASE_URL"}},
	{Field: "openlibrary_base_url", Env: []string{"OPENLIBRARY_BASE_URL"}},
	{Field: "audnexus_base_url", Env: []string{"AUDNEXUS_BASE_URL"}},
	{Field: "google_books_base_url", Env: []string{"GOOGLE_BOOKS_BASE_URL"}},
	{Field: "oauth_enabled", Env: []string{"OAUTH_ENABLED"}},
	{Field: "oauth_github_client_id", Env: []string{"OAUTH_GITHUB_CLIENT_ID"}},
	{Field: "oauth_github_client_secret", Env: []string{"OAUTH_GITHUB_CLIENT_SECRET"}},
	{Field: "oauth_google_client_id", Env: []string{"OAUTH_GOOGLE_CLIENT_ID"}},
	{Field: "oauth_google_client_secret", Env: []string{"OAUTH_GOOGLE_CLIENT_SECRET"}},
	{Field: "oauth_redirect_base_url", Env: []string{"OAUTH_REDIRECT_BASE_URL"}},
	{Field: "oauth_allowed_emails", Env: []string{"OAUTH_ALLOWED_EMAILS"}},
	{Field: "oauth_default_role", Env: []string{"OAUTH_DEFAULT_ROLE"}},
	{Field: "cf_access_team_domain", Env: []string{"CF_ACCESS_TEAM_DOMAIN"}},
	{Field: "cf_access_aud", Env: []string{"CF_ACCESS_AUD"}},
	{Field: "owner_email", Env: []string{"OWNER_EMAIL"}},
	{Field: "whisper_remote_url", Env: []string{"WHISPER_REMOTE_URL"}},
	{Field: "whisper_endpoints", Env: []string{"WHISPER_ENDPOINTS"}},
	{Field: "whisper_requires", Env: []string{"WHISPER_REQUIRES"}},
	{Field: "bootstrap_key_ttl", Env: []string{"BOOTSTRAP_KEY_TTL"}},
	{Field: "bootstrap_key_ttl_days", Env: []string{"BOOTSTRAP_KEY_TTL_DAYS"}},
	{Field: "scheduled.library_scan.enabled", Env: []string{"SCHEDULED_LIBRARY_SCAN_ENABLED"}},
	{Field: "scheduled.library_scan.interval", Env: []string{"SCHEDULED_LIBRARY_SCAN_INTERVAL"}},
	{Field: "scheduled.library_scan.on_startup", Env: []string{"SCHEDULED_LIBRARY_SCAN_ON_STARTUP"}},
	{Field: "scheduled.library_scan_full.enabled", Env: []string{"SCHEDULED_LIBRARY_SCAN_FULL_ENABLED"}},
	{Field: "scheduled.library_scan_full.interval", Env: []string{"SCHEDULED_LIBRARY_SCAN_FULL_INTERVAL"}},
	{Field: "scheduled.library_scan_full.period_hours", Env: []string{"SCHEDULED_LIBRARY_SCAN_FULL_PERIOD_HOURS"}},
	{Field: "scheduled.dedup_refresh.enabled", Env: []string{"SCHEDULED_DEDUP_REFRESH_ENABLED"}},
	{Field: "scheduled.dedup_refresh.interval", Env: []string{"SCHEDULED_DEDUP_REFRESH_INTERVAL"}},
	{Field: "scheduled.dedup_refresh.on_startup", Env: []string{"SCHEDULED_DEDUP_REFRESH_ON_STARTUP"}},
	{Field: "scheduled.acoustid_backfill.enabled", Env: []string{"SCHEDULED_ACOUSTID_BACKFILL_ENABLED"}},
	{Field: "scheduled.acoustid_backfill.interval", Env: []string{"SCHEDULED_ACOUSTID_BACKFILL_INTERVAL"}},
	{Field: "scheduled.acoustid_backfill.on_startup", Env: []string{"SCHEDULED_ACOUSTID_BACKFILL_ON_STARTUP"}},
	{Field: "scheduled.metadata_upgrade.interval", Env: []string{"SCHEDULED_METADATA_UPGRADE_INTERVAL"}},
	{Field: "scheduled.metadata_upgrade.on_startup", Env: []string{"SCHEDULED_METADATA_UPGRADE_ON_STARTUP"}},
	{Field: "scheduled.asin_backfill.interval", Env: []string{"SCHEDULED_ASIN_BACKFILL_INTERVAL"}},
	{Field: "scheduled.catalog_harvest.interval", Env: []string{"SCHEDULED_CATALOG_HARVEST_INTERVAL"}},
	{Field: "scheduled.label_refinement.enabled", Env: []string{"SCHEDULED_LABEL_REFINEMENT_ENABLED"}},
	{Field: "scheduled.label_refinement.interval", Env: []string{"SCHEDULED_LABEL_REFINEMENT_INTERVAL"}},
	{Field: "scheduled.label_refinement.on_startup", Env: []string{"SCHEDULED_LABEL_REFINEMENT_ON_STARTUP"}},
	{Field: "scheduled.author_split.enabled", Env: []string{"SCHEDULED_AUTHOR_SPLIT_ENABLED"}},
	{Field: "scheduled.author_split.interval", Env: []string{"SCHEDULED_AUTHOR_SPLIT_INTERVAL"}},
	{Field: "scheduled.author_split.on_startup", Env: []string{"SCHEDULED_AUTHOR_SPLIT_ON_STARTUP"}},
	{Field: "scheduled.db_optimize.enabled", Env: []string{"SCHEDULED_DB_OPTIMIZE_ENABLED"}},
	{Field: "scheduled.db_optimize.interval", Env: []string{"SCHEDULED_DB_OPTIMIZE_INTERVAL"}},
	{Field: "scheduled.db_optimize.on_startup", Env: []string{"SCHEDULED_DB_OPTIMIZE_ON_STARTUP"}},
	{Field: "scheduled.metadata_refresh.enabled", Env: []string{"SCHEDULED_METADATA_REFRESH_ENABLED"}},
	{Field: "scheduled.metadata_refresh.interval", Env: []string{"SCHEDULED_METADATA_REFRESH_INTERVAL"}},
	{Field: "scheduled.metadata_refresh.on_startup", Env: []string{"SCHEDULED_METADATA_REFRESH_ON_STARTUP"}},
	{Field: "scheduled.resolve_production_authors.enabled", Env: []string{"SCHEDULED_RESOLVE_PRODUCTION_AUTHORS_ENABLED"}},
	{Field: "scheduled.resolve_production_authors.interval", Env: []string{"SCHEDULED_RESOLVE_PRODUCTION_AUTHORS_INTERVAL"}},
	{Field: "scheduled.series_prune.enabled", Env: []string{"SCHEDULED_SERIES_PRUNE_ENABLED"}},
	{Field: "scheduled.series_prune.interval", Env: []string{"SCHEDULED_SERIES_PRUNE_INTERVAL"}},
	{Field: "scheduled.series_prune.on_startup", Env: []string{"SCHEDULED_SERIES_PRUNE_ON_STARTUP"}},
	{Field: "scheduled.ai_dedup_batch.enabled", Env: []string{"SCHEDULED_AI_DEDUP_BATCH_ENABLED"}},
	{Field: "scheduled.ai_dedup_batch.interval", Env: []string{"SCHEDULED_AI_DEDUP_BATCH_INTERVAL"}},
	{Field: "scheduled.ai_dedup_batch.on_startup", Env: []string{"SCHEDULED_AI_DEDUP_BATCH_ON_STARTUP"}},
	{Field: "scheduled.reconcile.enabled", Env: []string{"SCHEDULED_RECONCILE_ENABLED"}},
	{Field: "scheduled.reconcile.interval", Env: []string{"SCHEDULED_RECONCILE_INTERVAL"}},
	{Field: "scheduled.reconcile.on_startup", Env: []string{"SCHEDULED_RECONCILE_ON_STARTUP"}},
	{Field: "auto_update.enabled", Env: []string{"AUTO_UPDATE_ENABLED"}},
	{Field: "auto_update.channel", Env: []string{"AUTO_UPDATE_CHANNEL"}},
	{Field: "auto_update.check_minutes", Env: []string{"AUTO_UPDATE_CHECK_MINUTES"}},
	{Field: "auto_update.window_start", Env: []string{"AUTO_UPDATE_WINDOW_START"}},
	{Field: "auto_update.window_end", Env: []string{"AUTO_UPDATE_WINDOW_END"}},
	{Field: "maintenance.enabled", Env: []string{"MAINTENANCE_ENABLED"}},
	{Field: "maintenance.window_start", Env: []string{"MAINTENANCE_WINDOW_START"}},
	{Field: "maintenance.window_end", Env: []string{"MAINTENANCE_WINDOW_END"}},
	{Field: "maintenance.acoustid_online_lookup", Env: []string{"MAINTENANCE_ACOUSTID_ONLINE_LOOKUP"}},
	{Field: "maintenance.acoustid_nightly_limit", Env: []string{"MAINTENANCE_ACOUSTID_NIGHTLY_LIMIT"}},
	{Field: "maintenance.acoustid_backfill", Env: []string{"MAINTENANCE_ACOUSTID_BACKFILL"}},
	{Field: "embedding.enabled", Env: []string{"EMBEDDING_ENABLED"}},
	{Field: "embedding.model", Env: []string{"EMBEDDING_MODEL"}},
	{Field: "embedding.dimensions", Env: []string{"EMBEDDING_DIMENSIONS"}},
	{Field: "embedding.base_url", Env: []string{"EMBEDDING_BASE_URL"}},
	{Field: "embedding.vector_backend", Env: []string{"VECTOR_INDEX_BACKEND"}},
	{Field: "embedding.request_timeout_seconds", Env: []string{"EMBEDDING_REQUEST_TIMEOUT_SECONDS"}},
	{Field: "dedup.chromem_lazy", Env: []string{"DEDUP_CHROMEM_LAZY"}},
	{Field: "activity_backend", Env: []string{"ACTIVITY_BACKEND"}},
	{Field: "ai_backend.embedding_mode", Env: []string{"AI_BACKEND_EMBEDDING_MODE"}},
	{Field: "ai_backend.llm_mode", Env: []string{"AI_BACKEND_LLM_MODE"}},
	{Field: "ai_backend.local_base_url", Env: []string{"AI_BACKEND_LOCAL_BASE_URL"}},
	{Field: "ai_backend.local_embedding_model", Env: []string{"AI_BACKEND_LOCAL_EMBEDDING_MODEL"}},
	{Field: "ai_backend.local_llm_model", Env: []string{"AI_BACKEND_LOCAL_LLM_MODEL"}},
	{Field: "ai_backend.parse_batch_size", Env: []string{"AI_BACKEND_PARSE_BATCH_SIZE"}},
	{Field: "ai_backend.parse_batch_timeout_seconds", Env: []string{"AI_BACKEND_PARSE_BATCH_TIMEOUT_SECONDS"}},
	{Field: "ai_backend.parse_batch_workers", Env: []string{"AI_BACKEND_PARSE_BATCH_WORKERS"}},
	{Field: "metadata_scoring.embedding_enabled", Env: []string{"METADATA_SCORING_EMBEDDING_ENABLED"}},
	{Field: "metadata_scoring.embedding_min_score", Env: []string{"METADATA_SCORING_EMBEDDING_MIN_SCORE"}},
	{Field: "metadata_scoring.embedding_best_match", Env: []string{"METADATA_SCORING_EMBEDDING_BEST_MATCH"}},
	{Field: "metadata_scoring.llm_enabled", Env: []string{"METADATA_SCORING_LLM_ENABLED"}},
	{Field: "metadata_scoring.llm_rerank_epsilon", Env: []string{"METADATA_SCORING_LLM_RERANK_EPSILON"}},
	{Field: "metadata_scoring.llm_rerank_top_k", Env: []string{"METADATA_SCORING_LLM_RERANK_TOP_K"}},
	{Field: "metadata_scoring.transcription_title_exact_boost", Env: []string{"METADATA_SCORING_TRANSCRIPTION_TITLE_EXACT_BOOST"}},
	{Field: "metadata_scoring.transcription_title_substr_boost", Env: []string{"METADATA_SCORING_TRANSCRIPTION_TITLE_SUBSTR_BOOST"}},
	{Field: "metadata_scoring.transcription_author_boost", Env: []string{"METADATA_SCORING_TRANSCRIPTION_AUTHOR_BOOST"}},
	{Field: "metadata_scoring.transcription_narrator_boost", Env: []string{"METADATA_SCORING_TRANSCRIPTION_NARRATOR_BOOST"}},
	{Field: "metadata_scoring.compilation_penalty", Env: []string{"METADATA_SCORING_COMPILATION_PENALTY"}},
	{Field: "metadata_scoring.rich_metadata_field_bonus", Env: []string{"METADATA_SCORING_RICH_METADATA_FIELD_BONUS"}},
	{Field: "metadata_scoring.rich_metadata_bonus_cap", Env: []string{"METADATA_SCORING_RICH_METADATA_BONUS_CAP"}},
	{Field: "metadata_scoring.f1_min_score", Env: []string{"METADATA_SCORING_F1_MIN_SCORE"}},
	{Field: "metadata_scoring.series_name_match_boost", Env: []string{"METADATA_SCORING_SERIES_NAME_MATCH_BOOST"}},
	{Field: "metadata_scoring.series_number_exact_boost", Env: []string{"METADATA_SCORING_SERIES_NUMBER_EXACT_BOOST"}},
	{Field: "metadata_scoring.series_number_wrong_penalty", Env: []string{"METADATA_SCORING_SERIES_NUMBER_WRONG_PENALTY"}},
	{Field: "metadata_scoring.bulk_fetch_workers", Env: []string{"METADATA_SCORING_BULK_FETCH_WORKERS"}},
	{Field: "metadata_scoring.write_back_workers", Env: []string{"METADATA_SCORING_WRITE_BACK_WORKERS"}},

	// internal/config/abs_config.go registerABSDefaults (9 calls).
	{Field: "abs_api_enabled", Env: []string{"ABS_API_ENABLED"}},
	{Field: "abs_auth_modes", Env: []string{"ABS_AUTH_MODES"}},
	{Field: "abs_jwt_secret", Env: []string{"ABS_JWT_SECRET"}},
	{Field: "abs_access_token_ttl", Env: []string{"ABS_ACCESS_TOKEN_TTL"}},
	{Field: "abs_refresh_token_ttl", Env: []string{"ABS_REFRESH_TOKEN_TTL"}},
	{Field: "abs_refresh_grace", Env: []string{"ABS_REFRESH_GRACE"}},
	{Field: "abs_session_max_ttl", Env: []string{"ABS_SESSION_MAX_TTL"}},
	{Field: "abs_server_version", Env: []string{"ABS_SERVER_VERSION"}},
	{Field: "abs_default_library_id", Env: []string{"ABS_DEFAULT_LIBRARY_ID"}},

	// internal/config/invite_config.go registerInviteDefaults (16 calls).
	{Field: "smtp_host", Env: []string{"SMTP_HOST"}},
	{Field: "smtp_port", Env: []string{"SMTP_PORT"}},
	{Field: "smtp_username", Env: []string{"SMTP_USERNAME"}},
	{Field: "smtp_password", Env: []string{"SMTP_PASSWORD"}},
	{Field: "smtp_from", Env: []string{"SMTP_FROM"}},
	{Field: "public_base_url", Env: []string{"PUBLIC_BASE_URL"}},
	{Field: "cf_account_id", Env: []string{"CF_ACCOUNT_ID"}},
	{Field: "cf_guests_policy_id", Env: []string{"CF_GUESTS_POLICY_ID"}},
	{Field: "cf_policy_api_token", Env: []string{"CF_POLICY_API_TOKEN"}},
	{Field: "cf_guest_service_tokens", Env: []string{"CF_GUEST_SERVICE_TOKENS"}},
	{Field: "cf_service_auth_policy_id", Env: []string{"CF_SERVICE_AUTH_POLICY_ID"}},
	{Field: "invite_email_service_token", Env: []string{"INVITE_EMAIL_SERVICE_TOKEN"}},
	{Field: "tunnel_connector_addrs", Env: []string{"TUNNEL_CONNECTOR_ADDRS"}},
	{Field: "allowed_peer_cidrs", Env: []string{"ALLOWED_PEER_CIDRS"}},
	{Field: "login_saturated_rate", Env: []string{"LOGIN_SATURATED_RATE"}},
	{Field: "login_saturated_burst", Env: []string{"LOGIN_SATURATED_BURST"}},
}
