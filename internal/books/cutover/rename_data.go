// file: internal/books/cutover/rename_data.go
// version: 1.0.0
// guid: b874765a-43e8-4e15-a309-3879bf7e2e64
// last-edited: 2026-11-11

package cutover

// The generator's data (05-config §1.12.3): the section table, the explicit
// rows and sub-key rows, and the few per-name facts the generator cannot read
// from HEAD's types. Everything here is copied from the spec's tables; the
// generator (rename_table.go) applies it to the frozen Config and fails on
// anything it cannot assign. No HEAD name is invented: a name that appears
// here and not in the frozen Config fails the generator too.

// section is one row of the spec's section table.
type section struct {
	name     string   // modules.books.<name>
	whole    []string // =name: a struct-typed field the section takes as a whole
	keys     []string // exact keys
	prefixes []string // prefix matches (the spec's name_*)
	strip    string   // the prefix stripped from a leaf
}

// sectionTable is the spec's section table, in its order. A name is assigned
// by exact name (whole) first, then exact key, then the longest matching
// prefix; two matches of one rank fail the generator.
var sectionTable = []section{
	{name: "abs", prefixes: []string{"abs_"}, strip: "abs_"},
	{name: "activity", prefixes: []string{"activity_"}, strip: "activity_"},
	{name: "ai", keys: []string{"enable_ai_parsing", "filename_parse_model", "cover_art_model"}},
	{name: "cache", keys: []string{"list_warmer_heap_delta_mb", "list_warmer_trickle_interval_ms", "library_counts_cache_min_interval_seconds"}, prefixes: []string{"cache_"}, strip: "cache_"},
	{name: "catalog", whole: []string{"catalog"}},
	{name: "cfguests", prefixes: []string{"cf_"}, strip: "cf_"},
	{name: "chapters", keys: []string{"chapter_consolidation_threshold_min", "repair_chapter_max_min", "fragment_window_containment_min"}},
	{name: "db_census", prefixes: []string{"db_census_"}, strip: "db_census_"},
	{name: "dedup", whole: []string{"dedup"}, prefixes: []string{"dedup_"}, strip: "dedup_"},
	{name: "download", prefixes: []string{"deluge_"}, strip: "deluge_"},
	{name: "embedding", whole: []string{"embedding"}},
	{name: "fingerprint", keys: []string{"fp_parallel_workers"}, prefixes: []string{"fingerprint_"}, strip: "fingerprint_"},
	{name: "itunes", whole: []string{"itunes"}},
	{name: "master_list", prefixes: []string{"master_list_"}, strip: "master_list_"},
	{name: "metadata", keys: []string{"authority_evidence_enabled", "identification_advance_enabled"}, prefixes: []string{"metadata_", "google_books_"}, strip: "metadata_"},
	{name: "openlibrary", prefixes: []string{"openlibrary_"}, strip: "openlibrary_"},
	{name: "organize", keys: []string{"organization_strategy", "auto_organize", "folder_naming_pattern", "file_naming_pattern", "auto_rename_on_apply"}},
	{name: "playlist", prefixes: []string{"playlist_"}, strip: "playlist_"},
	{name: "purge", prefixes: []string{"purge_"}, strip: "purge_"},
	{name: "quota", keys: []string{"enable_disk_quota", "disk_quota_percent", "enable_user_quotas"}},
	{name: "review", keys: []string{"review_apply_enabled", "bulk_apply_max_items"}},
	{name: "scan", keys: []string{"auto_scan_enabled", "auto_scan_debounce_seconds", "concurrent_scans", "min_book_size_bytes", "min_rescan_age_hours", "supported_extensions", "exclude_patterns", "coalesce_shattered_siblings"}, prefixes: []string{"scan_"}, strip: "scan_"},
	{name: "search", whole: []string{"search"}, keys: []string{"bleve_description_max_chars", "disable_per_user_search_filters", "enabled_sort_indexes"}},
	{name: "setup", prefixes: []string{"setup_"}, strip: "setup_"},
	{name: "tags", keys: []string{"write_back_metadata", "embed_cover_art", "auto_write_tags_on_apply", "verify_after_write", "create_backups"}},
	{name: "tools", whole: []string{"tools"}},
	{name: "versions", keys: []string{"retire_versions_apply_enabled", "kept_sweep_apply_enabled"}},
	{name: "whisper", prefixes: []string{"whisper_"}, strip: "whisper_"},
}

// explicitRow is one row of the spec's table that no section rule produces
// (a platform family, an integration instance field or secret, a library
// root or mapping, a task window or schedule, or a books key with a fixed
// name), or whose class the section rule cannot know.
type explicitRow struct {
	head    string
	class   Class // "" = the derived class (k, or o for an env/file field)
	stored  Stored
	targets []Target
	// admin marks a core target declared at ProtectAdmin or higher in
	// core/kernel/core_sections.go at BASE; a books target takes HEAD's
	// protected-field class instead (headProtected, §1.3.7), whatever this
	// says. It decides the refuse-or-warn verdict of the row's retired names
	// (05-config §1.3.6), which config itself computes from the registry:
	// F3-07's TestRenameTable_EveryKeyRegistered checks this column against
	// the real declarations.
	admin      bool
	failClosed bool
	url        bool // a URL-valued key: M2 splits userinfo out of it
	note       string
}

func tk(path string, conv string) Target { return Target{Kind: TargetKey, Path: path, Convert: conv} }
func tkc(path string, conv string) Target {
	return Target{Kind: TargetKey, Path: path, Convert: conv, Coined: true}
}
func ts(path string) Target { return Target{Kind: TargetSecret, Path: path} }
func tsc(path string) Target {
	return Target{Kind: TargetSecret, Path: path, Coined: true}
}
func tpass(path string, dest bool) Target {
	return Target{Kind: TargetPassthrough, Path: path, Dest: dest}
}
func tinst(desc string) Target { return Target{Kind: TargetInstance, Path: desc} }
func tentry(list, desc string) Target {
	return Target{Kind: TargetEntry, Path: desc, List: list}
}
func tfold(desc string) Target {
	return Target{Kind: TargetFold, Path: desc, List: "core.auth.oidc.providers", Convert: ConvertOIDCFold}
}

// explicitRows are the top-level explicit rows (05-config §1.12.3's table),
// keyed by HEAD name. The class column is the spec's counts, not its printed
// column: the spec counts abs_jwt_secret and cf_policy_api_token among the 11
// secrets and the table prints them as `o`; the generator follows the counts
// (classOverride below) and the PR records the discrepancy.
var explicitRows = []explicitRow{
	{head: "root_dir", class: ClassInput, targets: []Target{tentry("core.library.roots", "core.library.roots entry books-library (Path)")}, admin: true},
	{head: "path_aliases", class: ClassInput, targets: []Target{tentry("core.library.roots", "core.library.roots entry books-library (Aliases)")}, admin: true},
	{head: "database_path", targets: []Target{tk("core.storage.path", ConvertNeverStored)}, admin: true, note: "ProtectOperator: never stored"},
	{head: "database_type", class: ClassDropped, note: "Pebble only"},
	{head: "backup_dir", targets: []Target{tk("core.backup.dir", "")}, admin: true},
	{head: "backup_max_total_bytes", targets: []Target{tk("core.backup.retention.max_total_bytes", "")}},
	{head: "backup_compression", targets: []Target{tk("core.backup.compression", "")}},
	{head: "backup_compression_level", targets: []Target{tk("core.backup.compression_level", "")}},
	{head: "metadata_sources", class: ClassInput, targets: []Target{tinst("metadata/<source id> instances")}, note: "credentials: `s` rows below"},
	{head: "hardcover_api_token", class: ClassSecret, stored: StoredRow, targets: []Target{tpassSecret("integrations.metadata.hardcover.api_token")}, note: "encrypted row"},
	{head: "google_books_api_key", class: ClassSecret, stored: StoredRow, targets: []Target{tpassSecret("integrations.metadata.google-books.api_key")}, note: "encrypted row"},
	{head: "openai_api_key", class: ClassSecret, stored: StoredRow, targets: []Target{tpassSecret("integrations.ai.openai.api_key")}, note: "encrypted row"},
	{head: "acoustid_api_key", class: ClassSecret, targets: []Target{ts("modules.books.acoustid.api_key")}, note: "plaintext in the blob"},
	{head: "openai_base_url", class: ClassInput, targets: []Target{tpass("integrations.ai.openai.base_url", true)}},
	{head: "otel_exporter_otlp_endpoint", targets: []Target{tk("core.tracing.otlp.endpoint", "")}, admin: true, url: true},
	{head: "otel_metrics_otlp_endpoint", targets: []Target{tk("core.metrics.otlp.endpoint", "")}, admin: true, url: true},
	{head: "otel_metrics_otlp_interval", targets: []Target{tk("core.metrics.otlp.interval", "")}, note: "bare integer = ms"},
	{head: "otel_metrics_otlp_insecure", targets: []Target{tk("core.metrics.otlp.insecure", "")}, admin: true},
	{head: "telemetry_environment", targets: []Target{tk("core.metrics.environment", "")}},
	{head: "ai_endpoints", class: ClassInput, targets: []Target{tinst("ai/<row id> instances")}, note: "05-ai §10"},
	{head: "ai_endpoints_routing", class: ClassDropped, note: "not a router input (05-ai §10)"},
	{head: "operation_timeout_minutes", targets: []Target{tk("modules.books.operations.stale_threshold_minutes", "")}, note: "read at HEAD only as the staleness threshold of books' GET /api/v1/operations/stale"},
	{head: "log_retention_days", targets: []Target{tk("core.log.journal.retention_days", ConvertLogRetention)}, note: "0 -> 90, negative -> 0 (keep)"},
	{head: "operation_log_retention_days", targets: []Target{tk("core.tasks.history_retention", ConvertDaysToDuration)}, note: "days -> duration"},
	{head: "ai_journal_retention_days", targets: []Target{tkc("core.ai.journal_retention", ConvertDaysToDuration)}, note: "days -> duration"},
	{head: "ai_backend", class: ClassDropped, note: "its scalars are already folded into ai_endpoints by HEAD's codec (05-ai §10)"},
	{head: "api_rate_limit_per_minute", targets: []Target{tk("core.http.rate_limit.per_minute", "")}},
	{head: "json_body_limit_mb", targets: []Target{tk("core.http.body.json_max_bytes", ConvertMiBToBytes)}, note: "MiB -> bytes"},
	{head: "upload_body_limit_mb", targets: []Target{tk("core.http.body.upload_max_bytes", ConvertMiBToBytes)}, note: "MiB -> bytes"},
	{head: "enable_auth", targets: []Target{tk("core.auth.mode", ConvertAuthMode)}, admin: true, failClosed: true, note: "true -> required, false -> disabled; fail-closed key"},
	{head: "enable_rate_limit", targets: []Target{tk("core.http.rate_limit.enabled", "")}, admin: true},
	{head: "basic_auth_enabled", targets: []Target{tk("core.http.basic_gate.enabled", "")}, admin: true, failClosed: true, note: "fail-closed key"},
	{head: "basic_auth_username", targets: []Target{tk("core.http.basic_gate.username", "")}, admin: true},
	{head: "basic_auth_password", class: ClassSecret, stored: StoredRow, targets: []Target{ts("core.http.basic_gate.password")}, admin: true, note: "encrypted row; empty = unset"},
	{head: "oauth_enabled", targets: []Target{tfold("core.auth.oidc.providers")}, admin: true, note: "fold (05-authn)"},
	{head: "oauth_github_client_id", targets: []Target{tfold("core.auth.oidc.providers entry github")}, admin: true, note: "fold (05-authn)"},
	{head: "oauth_github_client_secret", class: ClassSecret, targets: []Target{tsc("core.auth.oidc.github.client_secret")}, admin: true, note: "the secret the github entry's client_secret ref names; plaintext in the blob"},
	{head: "oauth_google_client_id", targets: []Target{tfold("core.auth.oidc.providers entry google")}, admin: true, note: "fold (05-authn)"},
	{head: "oauth_google_client_secret", class: ClassSecret, targets: []Target{tsc("core.auth.oidc.google.client_secret")}, admin: true, note: "the secret the google entry's client_secret ref names; plaintext in the blob"},
	{head: "oauth_redirect_base_url", targets: []Target{tk("core.auth.oidc.redirect_base_url", "")}, admin: true, url: true},
	{head: "oauth_allowed_emails", targets: []Target{tfold("core.auth.oidc.providers[].allowed_emails"), tk("modules.books.cfaccess.allowed_emails", "")}, admin: true, note: "fold (05-authn); M2 also writes the value to the books key, because HEAD's internal/cfaccess reads it as the Cloudflare Access allow-list"},
	{head: "oauth_default_role", targets: []Target{tfold("core.auth.oidc.providers[].default_role")}, admin: true, note: "fold (05-authn)"},
	{head: "cf_access_team_domain", targets: []Target{tk("modules.books.identity.cf_access_team_domain", "")}, admin: true},
	{head: "cf_access_aud", targets: []Target{tk("modules.books.identity.cf_access_aud", "")}, admin: true},
	{head: "owner_email", targets: []Target{tk("modules.books.identity.owner_email", "")}, admin: true},
	{head: "smtp_host", targets: []Target{tpass("integrations.notify.email.host", true)}},
	{head: "smtp_port", targets: []Target{tpass("integrations.notify.email.port", true)}},
	{head: "smtp_username", targets: []Target{tpass("integrations.notify.email.username", false)}},
	{head: "smtp_password", class: ClassSecret, targets: []Target{tpassSecret("integrations.notify.email.password")}},
	{head: "smtp_from", targets: []Target{tpass("integrations.notify.email.from", false)}},
	{head: "public_base_url", targets: []Target{tkc("core.http.public_base_url", "")}, url: true},
	{head: "invite_email_service_token", targets: []Target{tk("modules.books.cfguests.invite_email_service_token", "")}},
	{head: "tunnel_connector_addrs", targets: []Target{tk("core.http.trusted_proxies", ConvertCSVList)}, admin: true, note: "comma list -> list; a storable admin key from F3 (§1.3.7)"},
	{head: "allowed_peer_cidrs", targets: []Target{tk("core.http.peer_allowlist", ConvertCSVList)}, admin: true, note: "comma list -> list; a storable admin key from F3 (§1.3.7)"},
	{head: "login_saturated_rate", targets: []Target{tkc("core.auth.login.saturated_rate", "")}, admin: true},
	{head: "login_saturated_burst", targets: []Target{tkc("core.auth.login.saturated_burst", "")}, admin: true},
	{head: "bootstrap_key_ttl", targets: []Target{sharedKey("core.auth.bootstrap.key_ttl", "")}, admin: true},
	{head: "bootstrap_key_ttl_days", targets: []Target{sharedKey("core.auth.bootstrap.key_ttl", ConvertDaysToDuration)}, admin: true, note: "days -> duration"},
	{head: "backup_retention_days", targets: []Target{tk("core.backup.retention.max_age", ConvertDaysToDuration)}, note: "days -> duration"},
	{head: "log_level", targets: []Target{tk("core.log.level", "")}},
	{head: "protected_paths", class: ClassInput, targets: []Target{tk("core.library.protected_paths", "")}, admin: true, note: "05-library §5L.10"},
	{head: "auto_update", targets: []Target{tk("core.updater.*", "")}, note: "sub-key rows below"},
	{head: "maintenance", targets: []Target{tk("core.tasks.windows.nightly.*", ""), tk("modules.books.maintenance.*", "")}, note: "sub-key rows below"},
	{head: "download_client", class: ClassInput, targets: []Target{tinst("download/deluge instance")}, note: "sub-key rows below"},
	{head: "scheduled", targets: []Target{tk("modules.books.scheduler.schedules.*", "")}, note: "sub-key rows below"},
	{head: "plugins", class: ClassInput, targets: []Target{tinst("notify/webhook-<n> instances")}, note: "sub-key rows below"},
	{head: "write_startup_readonly_key", targets: []Target{tk("core.auth.bootstrap.readonly_key.enabled", "")}, admin: true},
	{head: "deluge_web_url", class: ClassInput, targets: []Target{tpass("integrations.download.deluge.url", true)}},
	{head: "deluge_web_password", class: ClassSecret, targets: []Target{tpassSecret("integrations.download.deluge.password")}, note: "plaintext in the blob"},
}

// tpassSecret is a passthrough secret: an integration instance's credential
// field, which a retired name must refuse (05-metadata M1.3).
func tpassSecret(path string) Target {
	return Target{Kind: TargetSecret, Path: path, Passthrough: true}
}

// sharedKey is a key two rows may both name (bootstrap_key_ttl and
// bootstrap_key_ttl_days, which HEAD binds to one setting).
func sharedKey(path, conv string) Target {
	return Target{Kind: TargetKey, Path: path, Convert: conv, Shared: true}
}

// classOverride gives the class of a section-derived row the section rule
// cannot: the two ProtectOperator secrets the spec counts among the 11 `s`
// (§1.3.7: "the ABS JWT signing secret ... and the Cloudflare policy token
// ... are ProtectOperator secrets, class `o` in §1.12.3"; the table prints
// `o`, the counts line says 11 `s` and 16 `o`, which only these two rows
// reconcile; the discrepancy is recorded in the PR and seams.d/F3-06.md).
var classOverride = map[string]Class{
	"abs_jwt_secret":      ClassSecret,
	"cf_policy_api_token": ClassSecret,
}

// subRow is one explicit sub-key row (the spec's "Sub-key rows" table), keyed
// by HEAD's dotted name. A struct-typed field's sub-keys that follow their
// parent need none.
type subRow struct {
	head    string
	class   Class
	targets []Target
	note    string
	admin   bool
}

// subRows are the explicit rows of struct-typed fields' sub-keys.
var subRows = []subRow{
	{head: "itunes.path_mappings", class: ClassInput, targets: []Target{tentry("core.library.mappings", "core.library.mappings entries, Source: \"itunes\"")}, note: "05-library §5L.10; core.library.mappings is ProtectInteractive, so its retired name warns although HEAD classed the field FieldServerPath"},
	{head: "maintenance.enabled", class: ClassKey, targets: []Target{tk("core.tasks.windows.nightly.enabled", "")}},
	{head: "maintenance.window_start", class: ClassKey, targets: []Target{tk("core.tasks.windows.nightly.start", "")}},
	{head: "maintenance.window_end", class: ClassKey, targets: []Target{tk("core.tasks.windows.nightly.end", "")}},
	{head: "auto_update.enabled", class: ClassKey, targets: []Target{tk("core.updater.mode", ConvertUpdaterMode)}, admin: true, note: "true -> check (unattended apply stays check-only, 05-updater), false -> off"},
	{head: "auto_update.channel", class: ClassKey, targets: []Target{tk("core.updater.channel", ConvertUpdaterChannel)}, admin: true, note: "develop -> prerelease"},
	{head: "auto_update.check_minutes", class: ClassKey, targets: []Target{tk("core.updater.schedules.check", ConvertUpdaterCheck)}, note: "{enabled: true, every: <minutes>}"},
	{head: "auto_update.window_start", class: ClassDropped, note: "read and ignored since HEAD's updater hotfix (05-updater)"},
	{head: "auto_update.window_end", class: ClassDropped, note: "read and ignored since HEAD's updater hotfix (05-updater)"},
	{head: "download_client.torrent.type", class: ClassInput, targets: []Target{tinst("download/deluge instance fields")}, note: "05-download §10"},
	{head: "download_client.torrent.deluge.host", class: ClassInput, targets: []Target{tinst("download/deluge instance fields")}, note: "05-download §10"},
	{head: "download_client.torrent.deluge.port", class: ClassInput, targets: []Target{tinst("download/deluge instance fields")}, note: "05-download §10"},
	{head: "download_client.torrent.deluge.username", class: ClassInput, targets: []Target{tinst("download/deluge instance fields")}, note: "05-download §10"},
	{head: "download_client.torrent.deluge.password", class: ClassSecret, targets: []Target{tpassSecretCoined("integrations.download.deluge.daemon_password")}, note: "plaintext in the blob"},
}

func tpassSecretCoined(path string) Target {
	return Target{Kind: TargetSecret, Path: path, Passthrough: true, Coined: true}
}

// virtualRows are sub-key rows of a map or a slice's elements, which
// reflection over the frozen Config cannot walk: they name members M2
// reads by path.
var virtualRows = []subRow{
	{head: "metadata_sources[].credentials.<k>", class: ClassSecret, targets: []Target{{Kind: TargetSecret, Path: "integrations.metadata.<source id>.credentials.<k>", Passthrough: true}}, note: "plaintext in the blob"},
	{head: "plugins.webhook", class: ClassInput, targets: []Target{tinst("notify/webhook-<n> instances")}, note: "enabled, settings.urls, settings.events"},
	{head: "plugins.webhook.settings.secret", class: ClassSecret, targets: []Target{{Kind: TargetSecret, Path: "integrations.notify.webhook-<n>.secret", Passthrough: true}}, note: "plaintext; one record per instance n, the same value"},
	{head: "plugins.<id>", class: ClassSecret, targets: []Target{{Kind: TargetSecret, Path: "modules.books.plugins.<id>.settings"}}, note: "any other plugins.<id>: internal/plugin is retired (02 N4); the free-form map may hold credentials, so M2 keeps it as one encrypted record and reports it"},
	{head: "ai_endpoints[]", class: ClassInput, targets: []Target{tinst("ai/<row id> instances")}, note: "no secret of their own (05-metadata M1.10)"},
}

// scheduledIntervalOnly are the scheduled entries HEAD reads only for their
// interval: the task runs iff the interval is above 0 (config.go:1016-1045).
var scheduledIntervalOnly = map[string]bool{
	"metadata_upgrade": true,
	"asin_backfill":    true,
	"candidate_fetch":  true,
	"catalog_harvest":  true,
}

// scheduledExtraTarget are scheduled entries with a second target.
var scheduledExtraTarget = map[string]Target{
	"db_optimize": tk("core.storage.schedules.compact", ConvertScheduleStandard),
}

// bindAlias maps a bound viper key that is not a field of Config to the row
// whose value it backfills: the four per-source base URLs backfill
// metadata_sources[].base_url (configFromViper).
var bindAlias = map[string]string{
	"audible_base_url":      "metadata_sources",
	"openlibrary_base_url":  "metadata_sources",
	"audnexus_base_url":     "metadata_sources",
	"google_books_base_url": "metadata_sources",
}

// urlRows are the section-derived rows whose value is a URL.
var urlRows = map[string]bool{
	"whisper_remote_url": true,
	"embedding.base_url": true,
}

// headProtected are the HEAD names HEAD's configFieldRules classes
// FieldSignIn, FieldExecutable, FieldDatabase and FieldServerPath (a key
// covers every field below it): the books keys that keep their class
// (05-config §1.3.7 "so a books key HEAD classed ... keeps that class").
var headProtected = []string{
	"enable_auth", "basic_auth_enabled", "basic_auth_username", "basic_auth_password",
	"oauth_enabled", "oauth_github_client_id", "oauth_github_client_secret", "oauth_google_client_id",
	"oauth_google_client_secret", "oauth_redirect_base_url", "oauth_allowed_emails", "oauth_default_role",
	"cf_access_team_domain", "cf_access_aud", "owner_email", "abs_api_enabled", "abs_auth_modes",
	"abs_access_token_ttl", "abs_refresh_token_ttl", "abs_session_max_ttl", "abs_refresh_grace",
	"write_startup_readonly_key", "enable_rate_limit",
	"tools.managed_dir", "tools.ollama.mode", "tools.ollama.custom_path", "tools.fpcalc.mode", "tools.fpcalc.custom_path",
	"database_path", "database_type", "activity_backend",
	"root_dir", "path_aliases", "playlist_dir", "backup_dir", "openlibrary_dump_dir", "whisper_clip_cache_dir",
	"folder_naming_pattern", "file_naming_pattern", "protected_paths",
	"itunes.library_write_path", "itunes.library_read_path", "itunes.windows_root_path", "itunes.media_root",
	"itunes.path_mappings", "itunes.libraries.original", "itunes.libraries.ao", "itunes.libraries.pointed_at",
	"itunes.libraries.import_source",
}

// processNames are the two ABK_* names HEAD reads outside viper
// (05-config §1.12.3 step 1): they retire into canonical keys of core.
var processNames = []struct {
	name, canonical string
}{
	{headStateDirEnv, "core.paths.state_dir"},
	{headAllowKeyRegenEnv, "core.secrets.allow_key_regen"},
}
