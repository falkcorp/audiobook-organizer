// file: core/kernel/core_sections.go
// version: 1.0.3
// guid: 4514c014-7c87-4abb-836c-8bd6c5fa7b0b
// last-edited: 2026-11-10

package kernel

import (
	"encoding/json"
	"runtime"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/log"
)

// This file is the kernel's core-section list (05-config §1.12.1, "Every core
// section exists from F2", IMPL-1): one Go struct per core.* section, each key
// carrying its default, reload class and protection from its owner's key
// table. Nothing reads these keys before its owner adopts them, so declaring
// them changes no behaviour; F3's M2 stages every renamed key into a path that
// is registered here.
//
// Struct tags follow 05-config §1.3.1: json is the key segment, reload is
// "restart" or "boot" (absent means live), protect is "interactive", "admin",
// "owner" or "operator" (absent means none), schema carries JSON Schema
// keywords. A map-typed field is one key whose value is the whole map
// (05-config §1.3.6); a list is one key as well.
//
// Left out of this file, each added by the row named, never declared here as a
// stand-in:
//   - every key typed as the tasks package's schedule configuration, and the
//     core.tasks windows map: F2-04;
//   - the two secret-typed keys, core.auth.oidc.<id>.client_secret and
//     core.http.basic_gate.password: F3-03;
//   - the fail-closed declarations on core.auth.mode and
//     core.http.basic_gate.enabled: F3-02.
//
// Defaults the owner states as a template (<AppID>_session, logs/<app-id>.log,
// the compiled state directory, the user agent) are the empty string here: the
// app supplies the value at registration, and a literal template must never
// become a cookie name or a path. Defaults that depend on the host
// (min(4, NumCPU), NumCPU) are computed when coreSections is called.

const (
	kib = 1 << 10
	mib = 1 << 20
	gib = 1 << 30
)

// dur converts a time.Duration into the declaration type.
func dur(d time.Duration) config.Duration { return config.Duration(d) }

// hashWorkersDefault is the owner tables' min(4, NumCPU).
func hashWorkersDefault() int { return min(4, runtime.NumCPU()) }

// coreSections returns every core section of this row's key tables as a
// config.Define value, in the order of the owners' specs. F2-16 hands the list
// to the config store at boot; until then nothing calls it outside tests.
func coreSections() []any {
	return []any{
		// 05-ai §7
		config.Define("core.ai", aiSettings{
			CooldownBase:          dur(30 * time.Second),
			CooldownMax:           dur(5 * time.Minute),
			QuotaCooldown:         dur(30 * time.Minute),
			DeadlineFailovers:     1,
			KindLimits:            aiKindLimits{},
			Structured:            aiStructured{RepairAttempts: 1},
			CapsTTL:               dur(time.Hour),
			BatchParallelism:      2,
			BatchMaxApplyAttempts: 5,
			LogNoCapableEvery:     dur(time.Minute),
		}),

		// 05-authn §8
		config.Define("core.auth", authSettings{
			Mode:                "required",
			DisabledAllowRemote: false,
			Session: authSession{
				TTL:              dur(24 * time.Hour),
				RememberTTL:      dur(168 * time.Hour),
				IdleTimeout:      0,
				CookieName:       "",
				CookieHostPrefix: true,
			},
			Password: authPassword{MinLength: 8},
			APIKey: authAPIKey{
				DefaultTTL: dur(720 * time.Hour),
				MaxTTL:     dur(8760 * time.Hour),
			},
			Bootstrap: authBootstrap{
				Enabled:  true,
				TokenTTL: dur(10 * time.Minute),
				KeyTTL:   dur(8 * time.Hour),
				ReadonlyKey: authReadonlyKey{
					Enabled: false,
					Scopes:  []string{"core:system.view"},
				},
			},
			SetupRequiresToken: true,
			Login: authLogin{
				MaxFailuresPerSource: 15,
				Window:               dur(15 * time.Minute),
				SoftThreshold:        5,
				SoftStep:             dur(200 * time.Millisecond),
				SoftMax:              dur(2 * time.Second),
				SaturatedRate:        2,
				SaturatedBurst:       8,
			},
			Invite:    authInvite{TTL: dur(72 * time.Hour)},
			TempLogin: authTempLogin{TTL: dur(15 * time.Minute)},
			OIDC: authOIDC{
				RedirectBaseURL: "",
				Providers:       []authOIDCProvider{},
			},
			Owner: authOwner{Required: false},
		}),

		// 05-backup §5B.7
		config.Define("core.backup", backupSettings{
			Enabled:            true,
			Dir:                "backups",
			Compression:        "zstd",
			CompressionLevel:   0,
			IncludeDerived:     false,
			IncludeRecreatable: false,
			VerifyAfterCreate:  true,
			VerifyKeep:         3,
			Retention: backupRetention{
				MaxCount:      10,
				MaxTotalBytes: 40 * gib,
				MaxAge:        0,
			},
			BeforeBreakingMigration: true,
			HashWorkers:             hashWorkersDefault(),
			Restore:                 backupRestore{RehashOnBoot: false},
		}),

		// 05-config §1.7 and §2.7
		config.Define("core.paths", pathsSettings{StateDir: ""}),
		config.Define("core.config", configSettings{File: "", SubscriberDrainTimeout: dur(5 * time.Second)}),
		config.Define("core.secrets", secretsSettings{KeyFile: "secrets.keyring", AllowKeyRegen: false}),

		// 05-download §7 (the phase 2 rows are not declared; core.library.mappings
		// is 05-library's and is declared once, below)
		config.Define("core.download", downloadSettings{CapsTTL: dur(time.Hour)}),

		// 05-events §5E.9
		config.Define("core.events", eventsSettings{QueueSize: 256, CloseDrain: dur(2 * time.Second)}),
		config.Define("core.outbox", outboxSettings{
			MaxAttempts:          20,
			BackoffMin:           dur(time.Second),
			BackoffMax:           dur(5 * time.Minute),
			RelayWorkers:         4,
			RelayRotate:          dur(time.Hour),
			RelayEnabled:         true,
			HandlerTimeout:       dur(30 * time.Second),
			IntakePoll:           dur(time.Second),
			UnorderedParallelism: 4,
			OrphanRetention:      dur(168 * time.Hour),
		}),

		// 05-health §5H.7 (core.shutdown.budget is also 06 §6.8's row: declared once)
		config.Define("core.health", healthSettings{
			CheckInterval: dur(30 * time.Second),
			CheckTimeout:  dur(5 * time.Second),
			LiveStall:     dur(60 * time.Second),
		}),
		config.Define("core.shutdown", shutdownSettings{
			Budget:    dur(25 * time.Second),
			HTTPDrain: dur(10 * time.Second),
		}),
		config.Define("core.diagnostics", diagnosticsSettings{
			Dir:          "diagnostics",
			Retention:    dur(24 * time.Hour),
			LogLines:     5000,
			TaskRuns:     200,
			MaxFileBytes: 64 * mib,
		}),

		// 05-http-api §10
		config.Define("core.http", httpSettings{
			Listen:         ":8484",
			PeerAllowlist:  []string{},
			TrustedProxies: []string{},
			ClientIPHeader: "",
			CORS:           httpCORS{AllowedOrigins: []string{}},
			Body:           httpBody{JSONMaxBytes: 1 * mib, UploadMaxBytes: 10 * mib},
			RateLimit: httpRateLimit{
				Enabled:    true,
				PerMinute:  100,
				Burst:      20, // max(per_minute/5, 10) at the default per_minute
				MaxClients: 10000,
			},
			BasicGate:         httpBasicGate{Enabled: false, Username: ""},
			OpenAPI:           httpOpenAPI{Public: false},
			ReadHeaderTimeout: dur(15 * time.Second),
			IdleTimeout:       dur(120 * time.Second),
		}),

		// 05-library §5L.7 (also the one declaration of core.library.mappings, 05-download §7)
		config.Define("core.library", librarySettings{
			Roots:          []json.RawMessage{},
			Mappings:       []json.RawMessage{},
			ProtectedPaths: []string{},
			Transfer:       libraryTransfer{Workers: 4},
			Hash: libraryHash{
				MaxBytesPerSec:   0,
				WorkersPerDevice: hashWorkersDefault(),
			},
			Watch: libraryWatch{
				Debounce:     dur(5 * time.Second),
				PollInterval: dur(15 * time.Minute),
			},
			FreeSpace:  libraryFreeSpace{WarnPercent: 10, DefaultMinBytes: 5 * gib},
			Quarantine: libraryQuarantine{Retention: 0},
		}),

		// 05-logging §8: core.log is log.Settings; the journal's two sections are
		// kernel structs that a later row (F5-15) swaps to the logjournal types
		// in this same list.
		config.Define("core.log", log.DefaultSettings()),
		config.Define("core.log.journal", logJournalSettings{
			Enabled:       true,
			MinLevel:      "info",
			RetentionDays: 90,
			QueueSize:     4096,
		}),
		config.Define("core.log.schedules", logSchedules{}),

		// 05-metadata M1.7 and M2.7
		config.Define("core.integrations", integrationsSettings{
			Egress:              integrationsEgress{LANAllow: []string{}},
			HealthParallelism:   4,
			ProbeTimeout:        dur(10 * time.Second),
			DrainTimeout:        dur(30 * time.Second),
			MaxInstancesPerKind: 32,
			Disabled:            false,
		}),
		config.Define("core.providerhttp", providerHTTPSettings{
			Fallback: providerHTTPFallback{
				RPS:        2.0,
				Burst:      2,
				MaxRetries: 3,
				Timeout:    dur(30 * time.Second),
			},
			RetryAfterMax: dur(5 * time.Minute),
			Hold:          providerHTTPHold{Max: dur(24 * time.Hour)},
			UserAgent:     "",
		}),

		// 05-metrics §10. The four OTel ecosystem names are the keys' second
		// canonical env names (05-config §1.3.6); the option only records them.
		config.Define("core.metrics", metricsSettings{
			Prometheus:       metricsPrometheus{Enabled: true},
			Path:             "/metrics",
			Listen:           "",
			ListenPublic:     false,
			OTLP:             metricsOTLP{Endpoint: "", Interval: dur(60 * time.Second), Insecure: false},
			Environment:      "prod",
			CardinalityLimit: 2000,
		},
			config.EnvBinding("core.metrics.otlp.endpoint", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"),
			config.EnvBinding("core.metrics.otlp.interval", "OTEL_METRIC_EXPORT_INTERVAL"),
			config.EnvBinding("core.metrics.otlp.insecure", "OTEL_EXPORTER_OTLP_METRICS_INSECURE"),
		),
		config.Define("core.tracing", tracingSettings{
			OTLP:        tracingOTLP{Endpoint: "", Insecure: false},
			SampleRatio: 1.0,
		},
			config.EnvBinding("core.tracing.otlp.endpoint", "OTEL_EXPORTER_OTLP_ENDPOINT"),
		),

		// 05-notify §7
		config.Define("core.notify", notifySettings{
			SendTimeout: dur(15 * time.Second),
			MaxAttempts: 10,
			MaxAge:      dur(24 * time.Hour),
		}),

		// 05-rbac §10
		config.Define("core.rbac", rbacSettings{CacheSize: 4096}),
		config.Define("core.audit", auditSettings{
			Retention:       auditRetention{MaxAge: dur(8760 * time.Hour), MaxRecords: 1000000},
			AsyncBuffer:     4096,
			CoalesceWindow:  dur(time.Minute),
			DetailsMaxBytes: 4096,
		}),

		// 05-realtime §8
		config.Define("core.realtime", realtimeSettings{
			RingSize:        1024,
			ClientBuffer:    128,
			MaxClients:      256,
			MaxPerBucket:    16,
			Heartbeat:       dur(15 * time.Second),
			WriteTimeout:    dur(10 * time.Second),
			RetryAfter:      dur(5 * time.Second),
			MaxCoalesceKeys: 10000,
		}),

		// 05-storage §5S.7
		config.Define("core.storage", storageSettings{
			Path:           "",
			CacheBytes:     512 * mib,
			MemtableBytes:  64 * mib,
			MaxBatchBytes:  256 * mib,
			CloseTimeout:   dur(30 * time.Second),
			SnapshotMaxAge: dur(30 * time.Second),
			Sequence:       storageSequence{Lease: 64},
			Claims:         storageClaims{Mode: "strict"},
			CheckpointDir:  "",
			Checkpoint:     storageCheckpoint{FreeSpaceMargin: 2 * gib},
			Migration:      storageMigration{MaxBatchBytes: 64 * mib, MaxBatchOps: 250000},
			DataMigration:  storageDataMigration{ChunkBytes: 4 * mib, ChunkItems: 2000},
			Swap: storageSwap{
				IntentTTL:        dur(24 * time.Hour),
				KeepPrevious:     dur(168 * time.Hour),
				ResetHookTimeout: dur(30 * time.Minute),
			},
		}),
		config.Define("core.index", indexSettings{
			Reconcile: indexReconcile{Workers: runtime.NumCPU(), Interval: dur(5 * time.Second)},
		}),

		// 05-tasks §5T.10
		config.Define("core.tasks", tasksSettings{
			Workers:               8,
			SystemWorkers:         2,
			CPUSlots:              runtime.NumCPU(),
			Kinds:                 map[string]tasksKindDial{},
			ShutdownDrain:         dur(10 * time.Second),
			ShutdownExtendMax:     dur(60 * time.Second),
			MinCheckpointInterval: dur(30 * time.Second),
			MaxYields:             1000,
			ZombieCap:             4,
			StanddownLease:        dur(2 * time.Minute),
			SchedulePoll:          dur(time.Minute),
			Timezone:              "",
			IdempotencyTTL:        dur(168 * time.Hour),
			HistoryRetention:      dur(2160 * time.Hour),
		}),

		// 05-updater §5U.8
		config.Define("core.updater", updaterSettings{
			Mode:        "off",
			Channel:     "stable",
			MaxDownload: 512 * mib,
		}),

		// 06 §6.8. core.modules.<id>.enabled: the section is the map keyed by
		// module ID (data, 05-config §1.3.6); each value holds the one boot-time
		// key. The default is each manifest's DefaultEnabled, which the kernel
		// resolves, so the declared default is the empty map.
		config.Define("core.modules", map[string]moduleSettings{}),
		config.Define("core.lifecycle", lifecycleSettings{
			StartTimeout:    dur(60 * time.Second),
			RestartExitCode: 75,
		}),

		// 07 §7.15
		config.Define("core.ui", uiSettings{
			Enabled:     true,
			CSP:         uiCSP{ConnectSrc: []string{}, ReportOnly: false},
			Diagnostics: uiDiagnostics{Accept: true},
		}),
	}
}

// ---- core.ai (05-ai §7) ----

type aiSettings struct {
	CooldownBase          config.Duration `json:"cooldown_base"`
	CooldownMax           config.Duration `json:"cooldown_max"`
	QuotaCooldown         config.Duration `json:"quota_cooldown"`
	DeadlineFailovers     int             `json:"deadline_failovers"`
	KindLimits            aiKindLimits    `json:"kind_limits"`
	Structured            aiStructured    `json:"structured"`
	CapsTTL               config.Duration `json:"caps_ttl"`
	BatchParallelism      int             `json:"batch_parallelism" schema:"minimum=1"`
	BatchMaxApplyAttempts int             `json:"batch_max_apply_attempts"`
	LogNoCapableEvery     config.Duration `json:"log_no_capable_every"`
	// schedule keys (tasks.ScheduleConfig): F2-04
}

// aiKindLimits is per kind of call; 0 means no limit.
type aiKindLimits struct {
	Text       int `json:"text"`
	Embed      int `json:"embed"`
	Transcribe int `json:"transcribe"`
}

type aiStructured struct {
	RepairAttempts int `json:"repair_attempts"`
}

// ---- core.auth (05-authn §8) ----

// secret-typed keys: F3-03 (core.auth.oidc.<id>.client_secret, one per provider)

type authSettings struct {
	Mode                string        `json:"mode" reload:"restart" protect:"owner" schema:"enum=required|disabled"`
	DisabledAllowRemote bool          `json:"disabled_allow_remote" reload:"restart" protect:"admin"`
	Session             authSession   `json:"session"`
	Password            authPassword  `json:"password"`
	APIKey              authAPIKey    `json:"apikey"`
	Bootstrap           authBootstrap `json:"bootstrap"`
	SetupRequiresToken  bool          `json:"setup_requires_token" protect:"admin"`
	Login               authLogin     `json:"login"`
	Invite              authInvite    `json:"invite"`
	TempLogin           authTempLogin `json:"temp_login"`
	OIDC                authOIDC      `json:"oidc"`
	Owner               authOwner     `json:"owner"`
	// schedule keys (tasks.ScheduleConfig): F2-04
}

type authSession struct {
	TTL              config.Duration `json:"ttl"`
	RememberTTL      config.Duration `json:"remember_ttl"`
	IdleTimeout      config.Duration `json:"idle_timeout"` // 0 = off
	CookieName       string          `json:"cookie_name" reload:"restart" protect:"admin"`
	CookieHostPrefix bool            `json:"cookie_host_prefix" reload:"restart" protect:"admin"`
}

type authPassword struct {
	MinLength int `json:"min_length" protect:"admin"`
}

type authAPIKey struct {
	DefaultTTL config.Duration `json:"default_ttl"`
	MaxTTL     config.Duration `json:"max_ttl" protect:"admin"`
}

type authBootstrap struct {
	Enabled     bool            `json:"enabled" reload:"restart" protect:"admin"`
	TokenTTL    config.Duration `json:"token_ttl" reload:"restart" protect:"admin"`
	KeyTTL      config.Duration `json:"key_ttl" protect:"admin"` // the owner table caps it at 24h
	ReadonlyKey authReadonlyKey `json:"readonly_key"`
}

type authReadonlyKey struct {
	Enabled bool     `json:"enabled" reload:"restart" protect:"admin"`
	Scopes  []string `json:"scopes" reload:"restart" protect:"admin"`
}

type authLogin struct {
	MaxFailuresPerSource int             `json:"max_failures_per_source" protect:"admin"`
	Window               config.Duration `json:"window" protect:"admin"`
	SoftThreshold        int             `json:"soft_threshold" protect:"admin"`
	SoftStep             config.Duration `json:"soft_step" protect:"admin"`
	SoftMax              config.Duration `json:"soft_max" protect:"admin"`
	// A value outside its range refuses boot, as HEAD refuses start.
	SaturatedRate  float64 `json:"saturated_rate" reload:"restart" protect:"admin" schema:"minimum=0.01,maximum=50"`
	SaturatedBurst int     `json:"saturated_burst" reload:"restart" protect:"admin" schema:"minimum=1,maximum=200"`
}

type authInvite struct {
	TTL config.Duration `json:"ttl"`
}

type authTempLogin struct {
	TTL config.Duration `json:"ttl" protect:"admin"`
}

type authOIDC struct {
	RedirectBaseURL string             `json:"redirect_base_url" protect:"admin" schema:"format=uri"`
	Providers       []authOIDCProvider `json:"providers" protect:"owner"`
}

// authOIDCProvider is one entry of core.auth.oidc.providers. ID is
// [a-z][a-z0-9_]{0,47} and is neither "providers" nor "redirect_base_url".
// The list is one key (05-config §1.3.1) and config protects whole keys, so
// the whole list is ProtectOwner, every entry field included (05-config
// §1.3.7's owner-trust-root row, 05-authn §8, SEC3-6): an admin who could
// repoint an entry's issuer or client_id while its allowed_emails name the
// owner could mint the owner's identity. The owner tags on AllowedEmails and
// AllowedDomains restate the key's; Register refuses an element tag stricter
// than its key.
type authOIDCProvider struct {
	ID                  string   `json:"id"`
	Kind                string   `json:"kind" schema:"enum=oidc|github"`
	Issuer              string   `json:"issuer"`
	ClientID            string   `json:"client_id"`
	Scopes              []string `json:"scopes"`
	AllowedEmails       []string `json:"allowed_emails" protect:"owner"`
	AllowedDomains      []string `json:"allowed_domains" protect:"owner"`
	DefaultRole         string   `json:"default_role"`
	LinkExistingByEmail bool     `json:"link_existing_by_email"`
}

type authOwner struct {
	// Required is derived at runtime: true iff an owner verifier is configured
	// and an owner identity is set (05-authn §4.8). The declared default is false.
	Required bool `json:"required" reload:"restart" protect:"owner"`
}

// ---- core.backup (05-backup §5B.7) ----

type backupSettings struct {
	Enabled                 bool            `json:"enabled" protect:"admin"`
	Dir                     string          `json:"dir" reload:"restart" protect:"admin" schema:"format=path"`
	Compression             string          `json:"compression" schema:"enum=zstd|gzip|none"`
	CompressionLevel        int             `json:"compression_level"` // 0 = the codec's default
	IncludeDerived          bool            `json:"include_derived" protect:"admin"`
	IncludeRecreatable      bool            `json:"include_recreatable" protect:"admin"`
	VerifyAfterCreate       bool            `json:"verify_after_create" protect:"interactive"`
	VerifyKeep              int             `json:"verify_keep"`
	Retention               backupRetention `json:"retention"`
	BeforeBreakingMigration bool            `json:"before_breaking_migration" reload:"restart" protect:"admin"`
	HashWorkers             int             `json:"hash_workers"`
	Restore                 backupRestore   `json:"restore"`
	// schedule keys (tasks.ScheduleConfig): F2-04
}

type backupRetention struct {
	MaxCount      int             `json:"max_count" protect:"interactive" schema:"minimum=1"`
	MaxTotalBytes int64           `json:"max_total_bytes" protect:"interactive"`
	MaxAge        config.Duration `json:"max_age" protect:"interactive"`
}

type backupRestore struct {
	RehashOnBoot bool `json:"rehash_on_boot"`
}

// ---- core.paths, core.config, core.secrets (05-config §1.7, §2.7) ----

type pathsSettings struct {
	// StateDir is pre-boot: read from env and flags only, a config file that
	// sets it fails boot. The default is the app's compiled one.
	StateDir string `json:"state_dir" reload:"boot" protect:"operator" schema:"format=path"`
}

type configSettings struct {
	File                   string          `json:"file" reload:"restart" protect:"operator" schema:"format=path"`
	SubscriberDrainTimeout config.Duration `json:"subscriber_drain_timeout" reload:"restart"`
}

type secretsSettings struct {
	KeyFile       string `json:"key_file" reload:"restart" protect:"operator" schema:"format=path"` // relative to the state dir
	AllowKeyRegen bool   `json:"allow_key_regen" reload:"restart" protect:"operator"`
}

// ---- core.download (05-download §7) ----

type downloadSettings struct {
	CapsTTL config.Duration `json:"caps_ttl"`
}

// ---- core.events, core.outbox (05-events §5E.9) ----

type eventsSettings struct {
	QueueSize  int             `json:"queue_size" reload:"restart"`
	CloseDrain config.Duration `json:"close_drain"`
}

type outboxSettings struct {
	MaxAttempts          int             `json:"max_attempts"`
	BackoffMin           config.Duration `json:"backoff_min"`
	BackoffMax           config.Duration `json:"backoff_max"`
	RelayWorkers         int             `json:"relay_workers"`
	RelayRotate          config.Duration `json:"relay_rotate" reload:"restart"`
	RelayEnabled         bool            `json:"relay_enabled"`
	HandlerTimeout       config.Duration `json:"handler_timeout"`
	IntakePoll           config.Duration `json:"intake_poll"`
	UnorderedParallelism int             `json:"unordered_parallelism"`
	OrphanRetention      config.Duration `json:"orphan_retention"`
}

// ---- core.health, core.shutdown, core.diagnostics (05-health §5H.7, 06 §6.8) ----

type healthSettings struct {
	CheckInterval config.Duration `json:"check_interval"`
	CheckTimeout  config.Duration `json:"check_timeout"`
	LiveStall     config.Duration `json:"live_stall"`
}

type shutdownSettings struct {
	// Budget is 25s; the app sets 8s in a container when unset. It must stay
	// below the supervisor's stop timeout (05-health §5H.4).
	Budget    config.Duration `json:"budget" reload:"restart"`
	HTTPDrain config.Duration `json:"http_drain"`
}

type diagnosticsSettings struct {
	Dir          string          `json:"dir" reload:"restart" protect:"admin" schema:"format=path"` // resolves against the DB's parent
	Retention    config.Duration `json:"retention"`
	LogLines     int             `json:"log_lines"`
	TaskRuns     int             `json:"task_runs"`
	MaxFileBytes int             `json:"max_file_bytes"`
	// schedule keys (tasks.ScheduleConfig): F2-04
}

// ---- core.http (05-http-api §10) ----

// secret-typed keys: F3-03 (core.http.basic_gate.password)

type httpSettings struct {
	Listen            string          `json:"listen" reload:"restart" protect:"admin"`
	PeerAllowlist     []string        `json:"peer_allowlist" protect:"admin"`  // CIDRs
	TrustedProxies    []string        `json:"trusted_proxies" protect:"admin"` // CIDRs or addresses with an optional IPv6 zone
	ClientIPHeader    string          `json:"client_ip_header" protect:"admin"`
	CORS              httpCORS        `json:"cors"`
	Body              httpBody        `json:"body"`
	RateLimit         httpRateLimit   `json:"rate_limit"`
	BasicGate         httpBasicGate   `json:"basic_gate"`
	OpenAPI           httpOpenAPI     `json:"openapi"`
	ReadHeaderTimeout config.Duration `json:"read_header_timeout" reload:"restart"`
	IdleTimeout       config.Duration `json:"idle_timeout" reload:"restart"`
}

type httpCORS struct {
	AllowedOrigins []string `json:"allowed_origins" protect:"admin"`
}

type httpBody struct {
	JSONMaxBytes   int `json:"json_max_bytes"`
	UploadMaxBytes int `json:"upload_max_bytes"`
}

type httpRateLimit struct {
	Enabled    bool `json:"enabled" protect:"admin"`
	PerMinute  int  `json:"per_minute"`
	Burst      int  `json:"burst"`
	MaxClients int  `json:"max_clients" reload:"restart"`
}

// httpBasicGate holds the gate's switch and username. The enabled switch is
// fail-closed from F3-02; the username and password are not.
type httpBasicGate struct {
	Enabled  bool   `json:"enabled" protect:"admin"`
	Username string `json:"username" protect:"admin"`
}

type httpOpenAPI struct {
	Public bool `json:"public" protect:"admin"`
}

// ---- core.library (05-library §5L.7) ----

// librarySettings carries the DB-backed lists as raw JSON because their
// element types (root specs and path mappings, 05-library §5L.3) are not
// declared in core/library yet.
type librarySettings struct {
	Roots          []json.RawMessage `json:"roots" protect:"admin"`
	Mappings       []json.RawMessage `json:"mappings" protect:"interactive"`
	ProtectedPaths []string          `json:"protected_paths" protect:"admin"` // absolute paths
	Transfer       libraryTransfer   `json:"transfer"`
	Hash           libraryHash       `json:"hash"`
	Watch          libraryWatch      `json:"watch"`
	FreeSpace      libraryFreeSpace  `json:"free_space"`
	Quarantine     libraryQuarantine `json:"quarantine"`
	// schedule keys (tasks.ScheduleConfig): F2-04
}

type libraryTransfer struct {
	Workers int `json:"workers"`
}

type libraryHash struct {
	MaxBytesPerSec   int64 `json:"max_bytes_per_sec"` // 0 = unlimited
	WorkersPerDevice int   `json:"workers_per_device"`
}

type libraryWatch struct {
	Debounce     config.Duration `json:"debounce"`
	PollInterval config.Duration `json:"poll_interval"`
}

type libraryFreeSpace struct {
	WarnPercent     int   `json:"warn_percent"`
	DefaultMinBytes int64 `json:"default_min_bytes" protect:"interactive"`
}

type libraryQuarantine struct {
	Retention config.Duration `json:"retention" protect:"interactive"` // 0 = keep until purged
}

// ---- core.log, core.log.journal, core.log.schedules (05-logging §8) ----
//
// core.log is log.Settings, the type 05-logging §3 names for it, with
// log.DefaultSettings as its defaults; log (L0) cannot import config, so the
// kernel registers it. log.Settings declares neither "journal" nor
// "schedules": the sections below are the journal's, whose types do not exist
// until F5-15 and are kernel structs until then.

type logJournalSettings struct {
	Enabled       bool   `json:"enabled" reload:"restart"`
	MinLevel      string `json:"min_level" schema:"enum=debug|info|warn|error"`
	RetentionDays int    `json:"retention_days" schema:"minimum=0"` // 0 = keep
	QueueSize     int    `json:"queue_size" reload:"restart" schema:"minimum=64"`
}

type logSchedules struct {
	// schedule keys (tasks.ScheduleConfig): F2-04
}

// ---- core.integrations, core.providerhttp (05-metadata M1.7, M2.7) ----

type integrationsSettings struct {
	Egress              integrationsEgress `json:"egress"`
	HealthParallelism   int                `json:"health_parallelism" schema:"minimum=1"`
	ProbeTimeout        config.Duration    `json:"probe_timeout"`
	DrainTimeout        config.Duration    `json:"drain_timeout"`
	MaxInstancesPerKind int                `json:"max_instances_per_kind"`
	Disabled            bool               `json:"disabled" protect:"admin"`
	// schedule keys (tasks.ScheduleConfig): F2-04
}

type integrationsEgress struct {
	// LANAllow lists hosts, host:port or CIDRs that every instance without
	// Local may reach (SEC-15).
	LANAllow []string `json:"lan_allow" protect:"admin"`
}

type providerHTTPSettings struct {
	Fallback      providerHTTPFallback `json:"fallback"`
	RetryAfterMax config.Duration      `json:"retry_after_max"`
	Hold          providerHTTPHold     `json:"hold"`
	UserAgent     string               `json:"user_agent" reload:"restart"` // the app supplies <app>/<version>
}

type providerHTTPFallback struct {
	RPS        float64         `json:"rps"`
	Burst      int             `json:"burst"`
	MaxRetries int             `json:"max_retries"`
	Timeout    config.Duration `json:"timeout"`
}

type providerHTTPHold struct {
	Max config.Duration `json:"max"`
}

// ---- core.metrics, core.tracing (05-metrics §10) ----

type metricsSettings struct {
	Prometheus       metricsPrometheus `json:"prometheus"`
	Path             string            `json:"path" reload:"restart" protect:"admin"`
	Listen           string            `json:"listen" reload:"restart" protect:"admin"` // host:port; empty is the main listener
	ListenPublic     bool              `json:"listen_public" reload:"restart" protect:"operator"`
	OTLP             metricsOTLP       `json:"otlp"`
	Environment      string            `json:"environment" reload:"restart"`
	CardinalityLimit int               `json:"cardinality_limit" reload:"restart"`
}

type metricsPrometheus struct {
	Enabled bool `json:"enabled" reload:"restart"`
}

type metricsOTLP struct {
	Endpoint string          `json:"endpoint" reload:"restart" protect:"admin"`
	Interval config.Duration `json:"interval" reload:"restart"`
	Insecure bool            `json:"insecure" reload:"restart" protect:"admin"`
}

type tracingSettings struct {
	OTLP        tracingOTLP `json:"otlp"`
	SampleRatio float64     `json:"sample_ratio" reload:"restart" schema:"minimum=0,maximum=1"`
}

type tracingOTLP struct {
	Endpoint string `json:"endpoint" reload:"restart" protect:"admin"`
	Insecure bool   `json:"insecure" reload:"restart" protect:"admin"`
}

// ---- core.notify (05-notify §7) ----

type notifySettings struct {
	SendTimeout config.Duration `json:"send_timeout"`
	MaxAttempts int             `json:"max_attempts"`
	MaxAge      config.Duration `json:"max_age"`
}

// ---- core.rbac, core.audit (05-rbac §10) ----

type rbacSettings struct {
	CacheSize int `json:"cache_size" reload:"restart"`
}

type auditSettings struct {
	Retention       auditRetention  `json:"retention"`
	AsyncBuffer     int             `json:"async_buffer" reload:"restart"`
	CoalesceWindow  config.Duration `json:"coalesce_window"`
	DetailsMaxBytes int             `json:"details_max_bytes"`
	// schedule keys (tasks.ScheduleConfig): F2-04
}

type auditRetention struct {
	MaxAge     config.Duration `json:"max_age" protect:"admin"`
	MaxRecords int             `json:"max_records" protect:"admin"`
}

// ---- core.realtime (05-realtime §8) ----

type realtimeSettings struct {
	RingSize        int             `json:"ring_size" reload:"restart"`
	ClientBuffer    int             `json:"client_buffer" reload:"restart"`
	MaxClients      int             `json:"max_clients"`
	MaxPerBucket    int             `json:"max_per_bucket"`
	Heartbeat       config.Duration `json:"heartbeat" reload:"restart"`
	WriteTimeout    config.Duration `json:"write_timeout"`
	RetryAfter      config.Duration `json:"retry_after"`
	MaxCoalesceKeys int             `json:"max_coalesce_keys" reload:"restart"`
}

// ---- core.storage, core.index (05-storage §5S.7) ----
//
// None of these keys is DB-persisted (they are read before the DB opens), so
// every one is ProtectOperator: file, env or flag only.

type storageSettings struct {
	Path           string               `json:"path" reload:"restart" protect:"operator" schema:"format=path"` // empty: <state_dir>/storage/main.pebble
	CacheBytes     int64                `json:"cache_bytes" reload:"restart" protect:"operator"`
	MemtableBytes  int64                `json:"memtable_bytes" reload:"restart" protect:"operator"`
	MaxBatchBytes  int64                `json:"max_batch_bytes" protect:"operator"`
	CloseTimeout   config.Duration      `json:"close_timeout" protect:"operator"`
	SnapshotMaxAge config.Duration      `json:"snapshot_max_age" protect:"operator"`
	Sequence       storageSequence      `json:"sequence"`
	Claims         storageClaims        `json:"claims"`
	CheckpointDir  string               `json:"checkpoint_dir" reload:"restart" protect:"operator" schema:"format=path"` // empty: the DB's parent
	Checkpoint     storageCheckpoint    `json:"checkpoint"`
	Migration      storageMigration     `json:"migration"`
	DataMigration  storageDataMigration `json:"data_migration"`
	Swap           storageSwap          `json:"swap"`
	// schedule keys (tasks.ScheduleConfig): F2-04
}

type storageSequence struct {
	Lease int `json:"lease" reload:"restart" protect:"operator"`
}

type storageClaims struct {
	// Mode is strict from R1; shadow is an emergency dial that logs and counts
	// a violation and allows it, persisting nothing.
	Mode string `json:"mode" reload:"restart" protect:"operator" schema:"enum=strict|shadow"`
}

type storageCheckpoint struct {
	FreeSpaceMargin int64 `json:"free_space_margin" reload:"restart" protect:"operator"`
}

type storageMigration struct {
	MaxBatchBytes int64 `json:"max_batch_bytes" reload:"restart" protect:"operator"`
	MaxBatchOps   int   `json:"max_batch_ops" reload:"restart" protect:"operator"`
}

type storageDataMigration struct {
	ChunkBytes int64 `json:"chunk_bytes" protect:"operator"`
	ChunkItems int   `json:"chunk_items" protect:"operator"`
}

type storageSwap struct {
	IntentTTL        config.Duration `json:"intent_ttl" protect:"operator"`
	KeepPrevious     config.Duration `json:"keep_previous" protect:"operator"`
	ResetHookTimeout config.Duration `json:"reset_hook_timeout" reload:"restart" protect:"operator"`
}

type indexSettings struct {
	Reconcile indexReconcile `json:"reconcile"`
}

type indexReconcile struct {
	Workers  int             `json:"workers" protect:"operator"`
	Interval config.Duration `json:"interval" protect:"operator"`
}

// ---- core.tasks (05-tasks §5T.10) ----

// tasksSettings is the core.tasks section: every key of the owner's table
// except the per-owner schedule keys and the windows map, both F2-04's.
type tasksSettings struct {
	Workers               int                      `json:"workers" reload:"restart"`
	SystemWorkers         int                      `json:"system_workers" reload:"restart"` // reserved for PrioritySystem
	CPUSlots              int                      `json:"cpu_slots"`
	Kinds                 map[string]tasksKindDial `json:"kinds"` // one JSON value; empty means no caps
	ShutdownDrain         config.Duration          `json:"shutdown_drain"`
	ShutdownExtendMax     config.Duration          `json:"shutdown_extend_max"` // systemd only
	MinCheckpointInterval config.Duration          `json:"min_checkpoint_interval"`
	MaxYields             int                      `json:"max_yields"`
	ZombieCap             int                      `json:"zombie_cap"` // per module
	StanddownLease        config.Duration          `json:"standdown_lease"`
	SchedulePoll          config.Duration          `json:"schedule_poll" reload:"restart"`
	Timezone              string                   `json:"timezone" reload:"restart"` // an IANA zone; empty is the server's local zone
	IdempotencyTTL        config.Duration          `json:"idempotency_ttl"`           // the HTTP start route only
	HistoryRetention      config.Duration          `json:"history_retention"`
	// schedule keys (tasks.ScheduleConfig): F2-04
	// windows (map): F2-04
}

// tasksKindDial is the per-kind dial of core.tasks.kinds.
type tasksKindDial struct {
	ParallelismCap int `json:"parallelism_cap"`
}

// ---- core.updater (05-updater §5U.8) ----

type updaterSettings struct {
	Mode        string `json:"mode" protect:"admin" schema:"enum=off|check"`
	Channel     string `json:"channel" protect:"admin" schema:"enum=stable|prerelease"`
	MaxDownload int64  `json:"max_download"`
	// schedule keys (tasks.ScheduleConfig): F2-04
}

// ---- core.modules, core.lifecycle (06 §6.8) ----

// moduleSettings is the value of one entry of core.modules, keyed by module ID.
type moduleSettings struct {
	Enabled bool `json:"enabled" reload:"boot"`
}

type lifecycleSettings struct {
	StartTimeout    config.Duration `json:"start_timeout" reload:"restart"`
	RestartExitCode int             `json:"restart_exit_code" protect:"admin"`
}

// ---- core.ui (07 §7.15) ----

type uiSettings struct {
	Enabled     bool          `json:"enabled" reload:"restart"`
	CSP         uiCSP         `json:"csp"`
	Diagnostics uiDiagnostics `json:"diagnostics"`
}

type uiCSP struct {
	ConnectSrc []string `json:"connect_src"` // bare DNS names
	ReportOnly bool     `json:"report_only"`
}

type uiDiagnostics struct {
	Accept bool `json:"accept"`
}
