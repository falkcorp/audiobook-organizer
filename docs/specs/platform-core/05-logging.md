<!-- file: docs/specs/platform-core/05-logging.md -->
<!-- version: 1.8.2 -->
<!-- guid: b62e0a52-4f32-4aeb-8bb9-4429ec976537 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Logging (`core/log`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §3, §8, §9;
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §6–7;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md) (`go-logr/logr` v1.4.4).
Normative: [03-architecture](03-architecture.md) v1.11.1 R4 (`log` is in **L0** with `entity`, `lifecycle`,
`version`, `metrics`; the journal service `core/internal/logjournal` imports `storage` and `tasks`, so by
R4's bridge rule it sits at **L4** and only the kernel imports it), R5 (no `slog.SetDefault` in core except the
opt-in `app.InstallProcessDefaults`; R5 binds core only, and ABO's logger shim is an allowed books-module value),
§3.6 naming. Config
mechanics: [05-config](05-config.md).

## Decisions at a glance

| # | Decision |
|---|---|
| L1 | `log/slog` is the only logging API in core. Core hands out `*slog.Logger` values; there is no core `Logger` interface. ABO's printf-style `logger.Logger` (with progress, change recording and cancellation folded in) is not carried into core; it survives in ABO as a books-module shim over core handles (§11). |
| L2 | One handler pipeline per `App`; per-module child loggers carry `module=<id>` (and optionally `subsystem=<name>`); levels are settable per module (and per module.subsystem) at runtime. |
| L3 | Core never calls `slog.SetDefault`, `log.SetOutput`, `otel.SetLogger`, `otel.SetErrorHandler`, `otel.SetMeterProvider`, `otel.SetTracerProvider` or `otel.SetTextMapPropagator`. `app.InstallProcessDefaults` is the single opt-in helper that does all of them, with this App's pipeline and telemetry providers, for an app that wants it (ABO does from R1, the release that deletes its façade's installs; MOD4-3, OPS5-14). |
| L4 | Redaction is in the pipeline, once, before fan-out: type-based (`secrets.Value`, `LogValuer`), key-based (deny-list), URL-based (userinfo and credential query params) and **path-based** (one-time tokens in URL paths, including core's own invite route). The same `*log.Redactor` redacts trace span attributes ([05-metrics](05-metrics.md) §3, SEC-22). |
| L5 | The **system log journal** (persisted operational records ≥ info, queryable, retained N days) is core and is readable only with `core:system.manage`. It persists through a store interface, `log.JournalStore` (R4-1), whose one implementation is core's native store over `c/log/journal/`; an app with pre-core system-log rows imports them once, in its own cutover step, through `log.Importer` (ABO: books' M5 in R2, §5). The **domain activity feed** stays in the app module. Security events go to core `audit`; task run logs to core `tasks`. |
| L6 | The `go-logr/logr` adapter stays, but only as the value-dropping OTel SDK sink inside `app.InstallProcessDefaults`; `logr` is never imported by other core packages. |

## 1. Purpose & scope

Construct and own each `App`'s `slog` pipeline: output format and destination, levels (global, per module,
per module.subsystem, changeable live), context-carried attributes, trace correlation, redaction, and
asynchronous taps that feed the system journal and the realtime log tail. Out of scope: audit records
([05-rbac](05-rbac.md)), task run logs and progress ([05-tasks](05-tasks.md)), metrics ([05-metrics](05-metrics.md)),
the domain activity feed (app module), the HTTP access-log middleware ([05-http-api](05-http-api.md), which logs
through this pipeline and so inherits its redaction).

## 2. Current state at HEAD

| Aspect | Today | Anchor |
|---|---|---|
| Packages | `internal/logger` (877 LOC) and `internal/logging` (529 LOC) | `raw/12` §1, §3 |
| `logger.Logger` | composite of `LevelLogger` (printf-style Trace…Error), `ProgressReporter`, `ChangeRecorder`, `CancellationReporter`, `SubLoggerFactory`; `Change` carries `BookID` (domain leak) | `internal/logger/logger.go:44-98` |
| `StandardLogger` | formats with `fmt.Sprintf`, sanitizes, prefixes `"<subsystem>: "` into the **message** (no attribute), writes via `slog.Default()`; Trace → Debug; progress/changes/cancel are no-ops; optional `ActivityLogWriter` copies info+ lines into the system activity log | `logger/logger.go:105-127`, `logger/standard.go:13-66` |
| Context helpers | `logging.Info/Warn/Error/Debug(ctx, …)` add `OpContext` attrs and call package-level `slog.*` | `internal/logging/structured.go:47-72`, `opcontext.go:15-28` |
| Two sanitizers | `logger.sanitizeLogLine` escapes every C0 control and DEL; `logging.Sanitize` escapes only CR and LF | `logger/sanitize.go:40-77`, `logging/sanitize.go:28-43` |
| Path redaction | `RedactInvitePath`: any `invite`/`invites` segment followed by another segment, at any position, after bounded percent-decoding rounds and dot-walk checks; fails closed to `/[REDACTED]` | `logging/redact_path.go:99-120,165` |
| Query redaction | gin access-log formatter and recovery mask `?token=` / `?api_key=` (ABS clients put bearer credentials in the URL) | `internal/server/server.go:695-704` |
| Usage | `logger.New(` 322 calls in 238 non-test files, 210 of them package-level vars (`raw/12` counts 207); `slog.Info/Warn/Error/Debug(` 1,460 calls in 261 files; `slog.Default()` 33 in 15 files; `logging.Info/…(` 231 in 37 files | this writer's grep; `raw/12` §3 |
| Ratchet | `TestGuard_NoDirectSlogCalls` ceiling per file, may only go down | `internal/logger/slog_guard_ratchet_test.go` |
| Process globals | `slog.SetDefault` at `cmd/root.go:718` (text handler, **Debug**, stdout + file) and again at `internal/server/server.go:1154` (text handler, **Info**, into the activity writer); stdlib `log.SetOutput(aw)` | `cmd/root.go:698-723`, `internal/server/server.go:1134-1155` |
| File logging | `logs/audiobook-organizer-<date>.log` relative to the **working directory**, date fixed at process start, never rotated | `cmd/root.go:700-712` |
| `log_level` config | not wired to any handler level; only gates gin debug mode and one iTunes branch; the `--log-level` flag writes `AppConfig.LogLevel` directly | `internal/server/server.go:97-99`, `internal/itunes/import.go:269`, `cmd/root.go:649-651` |
| Activity coupling | the activity `Writer` is an `io.Writer` that tees to stdout and **parses every slog text line** back into `ActivityEntry{Type:"system"}` rows; tier derived from level | `internal/activity/writer.go:92-105,149-215` |
| System log rows | `syslog:<RFC3339Nano>:<source>` JSON rows; pruned by `log_retention_days` (default 90) together with `operationlog:` rows | `internal/database/pebble_store_activity.go:20-37,434`, `internal/logger/retention.go:28-31,49`, `internal/config/log_retention.go:18` |
| System log access | `GET /api/v1/system/logs` requires `settings.manage` (admin only by default) | `internal/server/wire_system_routes.go:28,31` |
| OTel SDK logger | process-wide logr sink that **drops values** (the SDK logs raw env input such as malformed OTLP headers) and rate-limits; installed by `InitOTEL` | `internal/telemetry/sdklog.go:1-110`, `telemetry.go:46-49` |
| logr | direct dependency only for that sink | `go.mod:14`, `internal/telemetry/sdklog.go:17` |

Consequence worth stating: in `serve`, the second `slog.SetDefault` (`server.go:1154`) replaces the first
(`cmd/root.go:718`), so after `NewServer` the log **file stops receiving records** and the level drops from
Debug to Info (the activity writer tees to stdout only, `internal/activity/writer.go:94`). Listed under
out-of-scope defects; the design below makes it impossible (one pipeline, sinks are added, never replaced).

## 3. Go API

Package `github.com/falkcorp/mediacore/log` (L0). Imports: stdlib and `go.opentelemetry.io/otel/trace` (span
context only). Does **not** import `config`, `secrets`, `storage` or `metrics` (`secrets.Value` redacts itself
through `slog.LogValuer`, so `log` needs no import of it).

```go
package log

// Levels. slog has no Trace; ABO's LevelTrace maps here instead of to Debug.
const (
	LevelTrace = slog.Level(-8)
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

func ParseLevel(s string) (slog.Level, error) // "trace|debug|info|warn|error", case-insensitive

// Normative attribute keys. Other specs use these constants, not string literals.
const (
	KeyModule    = "module"    // module ID or "core"
	KeySubsystem = "subsystem" // e.g. "tasks", "scanner"
	KeyTaskKind  = "task_kind" // "books.scan"
	KeyRunID     = "run_id"
	KeyRequestID = "request_id"
	KeyRoute     = "route"     // route template, never the raw path
	KeyPath      = "path"      // raw request path; always passes the path rules
	KeyUserID    = "user_id"
	KeyTraceID   = "trace_id"
	KeySpanID    = "span_id"
	KeyError     = "err"
)

type Format uint8

const (
	FormatText Format = iota + 1
	FormatJSON
)

// Settings is the core.log section type. Declared here (plain struct, tags are strings) and
// registered by the kernel, because log cannot import config (R4). It has no "journal" or "schedules"
// field: the journal's keys are the separate sections core.log.journal and core.log.schedules, owned by
// core/internal/logjournal (§5), because the schedule key is a tasks.ScheduleConfig and log (L0) cannot
// import tasks (L4).
type Settings struct {
	Level      string            `json:"level" schema:"enum=trace|debug|info|warn|error"`
	Levels     map[string]string `json:"levels"` // "books" or "books.scanner" -> level
	Format     string            `json:"format" reload:"restart" schema:"enum=text|json"`
	Output     string            `json:"output" reload:"restart" schema:"enum=stderr|stdout|file"`
	File       FileSettings      `json:"file" reload:"restart"`
	AddSource  bool              `json:"add_source"`
	RedactKeys []string          `json:"redact_keys"`
}

type FileSettings struct {
	Path      string `json:"path" schema:"format=path" protect:"admin"`
	MaxSizeMB int    `json:"max_size_mb" schema:"minimum=1"`
	MaxFiles  int    `json:"max_files" schema:"minimum=1"`
	TeeStderr bool   `json:"tee_stderr"`
}

func DefaultSettings() Settings

type Options struct {
	Output    io.Writer // already opened by the kernel (stderr, stdout, or a *RotatingFile)
	Format    Format
	Level     slog.Level
	Levels    map[string]slog.Level
	AddSource bool
	Redactor  *Redactor
	Now       func() time.Time
}

// Logging is one App's pipeline. Safe for concurrent use.
type Logging struct{ /* unexported */ }

func New(o Options) (*Logging, error)

func (l *Logging) Root() *slog.Logger                          // module="core"
func (l *Logging) Module(id string) *slog.Logger               // module=id; panics never: invalid id -> error at kernel validation
func (l *Logging) Scope(module, subsystem string) *slog.Logger // module + subsystem, own level gate
func (l *Logging) Handler() slog.Handler                       // for adapters (gin bridge, logr sink)
func (l *Logging) Redactor() *Redactor                         // the pipeline's redactor, for span redaction (05-metrics)
func (l *Logging) SetLevels(root slog.Level, per map[string]slog.Level) (unknown []string)
func (l *Logging) Level(scope string) slog.Level

// Tap registers an asynchronous consumer of redacted records at or above min.
// The caller never blocks: when the bounded queue is full the record is dropped and counted.
func (l *Logging) Tap(name string, min slog.Level, queue int, fn func(ctx context.Context, r Record)) (stop func())

// Counters reports per-tap drop counts, records per level/module and redactions per kind
// (read by the kernel's metrics bridge; log cannot import metrics).
func (l *Logging) Counters() Counters

func (l *Logging) Close(ctx context.Context) error // drains taps until ctx expires

// Record is an immutable, already-redacted copy handed to taps.
type Record struct {
	Time      time.Time
	Level     slog.Level
	Module    string
	Subsystem string
	Message   string
	Attrs     []slog.Attr // resolved (LogValuer applied), groups preserved
	TraceID   string
	SpanID    string
}

// OtherUserPredicate reports whether a journal record (from the native store
// or from an app's own JournalStore) names another user's data; viewer is the
// principal's user ID. A module registers at most one
// through Registrar.LogOtherUserData (06); the journal query and the live tail
// return a flagged record only to a principal that also holds core:users.view
// and blank its user_id otherwise (§5). It must be pure and fast (called per
// record) and fail closed: true when unsure.
type OtherUserPredicate func(rec Record, viewer string) bool

// JournalStore persists the system log journal (R4-1). It is SDK, frozen by the API-stability gate, and
// names only log types, so log (L0) imports nothing for it. It is the stated exception to "every write method
// takes the caller's *storage.Batch" (05-storage "Store interfaces"; COR5-12, MOD5-10): it is append-only,
// each call commits its own batch, and it never joins a multi-owner commit. Its one implementation is core's
// native store (core/internal/logjournal), which keeps records in c/log/journal/<ULID>; no app supplies a
// store (D10). logtest.JournalStoreSuite is its contract (§10). Records reaching a store are already redacted.
type JournalStore interface {
	Append(ctx context.Context, recs []Record) error              // one durable batch per call
	Query(ctx context.Context, q JournalQuery) (JournalPage, error) // newest first
	// Prune deletes up to limit records older than before, oldest first, in one bounded batch; more reports
	// whether older records remain (the prune task calls it until more is false or its budget ends).
	Prune(ctx context.Context, before time.Time, limit int) (deleted int, more bool, err error)
}

// Importer is the native journal's one-time import path for an app's pre-core system-log rows (§5; 03 §3.3
// "Store interfaces"). The kernel builds it over the native store and hands it to the owning app's cutover
// step in module.Importers (06 §6.4.2). Unlike the other importers it does not stage into the caller's batch
// (the one exception 03 §3.3's importer rule allows): log is L0 and cannot name a storage type, the same
// reason JournalStore commits its own batches. Instead
// Import is idempotent: each record's key is derived from its Time and SourceID, so a step that resumes after
// a crash between an import and its source deletes rewrites the same keys and imports nothing twice.
type Importer interface {
	Import(ctx context.Context, recs []ImportedRecord) error // one durable batch per call
}

type ImportedRecord struct {
	Record
	SourceID string // the app's stable ID of the source row (ABO: its syslog: key)
}

type JournalQuery struct {
	MinLevel     slog.Level
	Module       string    // "" = all
	Since, Until time.Time // zero = unbounded
	Text         string    // case-insensitive substring of the message
	Cursor       string    // opaque, from JournalPage.Next
	Limit        int       // capped by the query route
}

type JournalPage struct {
	Records []Record
	Next    string // "" when exhausted
}

// Context-carried attributes (replace internal/logging.OpContext). The handler appends them to every
// record logged with that ctx; tasks, httpapi and realtime set run_id / request_id / route / user_id.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context
func AttrsFrom(ctx context.Context) []slog.Attr

// Redaction.
type Redactor struct{ /* immutable */ }

func NewRedactor(extraKeys []string, extraPaths ...PathRule) *Redactor // always includes the defaults below (fresh copies)
func (r *Redactor) Attr(a slog.Attr) slog.Attr
func (r *Redactor) URL(raw string) string           // userinfo removed, credential query params replaced, path rules applied
func (r *Redactor) Path(p string) string            // path rules only
func (r *Redactor) String(key, value string) string // key deny-list, then URL/path handling for URL-keyed or URL-shaped values

// PathRule redacts a token-bearing path segment. Rules are pure functions of the path (no route table).
type PathRule struct {
	Name string                   // "invite", or a module-registered name
	Fn   func(path string) string // must fail closed (return a redacted path) when unsure
}

// The four default lists are functions that return a fresh copy on every call, never exported package-level
// slices: R5 forbids exported mutable package state in core (TestNoGlobals), and a shared slice any importer
// could append to would weaken redaction for every App in the process (MOD5-5, SIMP5-14). Same pattern as
// jsonschemax.Base() (03 R4).

// DefaultPathRules covers core's own token-bearing routes: any "invite"/"invites" segment followed by
// another segment, at any position, after percent-decoding and dot-walk checks — ABO's RedactInvitePath
// algorithm (internal/logging/redact_path.go:99-120) moved into core. It covers the core route
// /api/core/v1/auth/invites/{token} (05-authn), and HEAD's books invite routes and /invite/{token}
// page for as long as they exist (the R2 identity flip deletes them; FE4-2, 05-authn, 07).
// Fragment-form links (/invite#token=, /login/temp#token=) never reach the server.
func DefaultPathRules() []PathRule // [invite]

// DefaultRedactKeys: matched case-insensitively after stripping '-', '_', '.':
// "password", "passwd", "secret", "token", "apikey", "authorization", "cookie", "setcookie",
// "privatekey", "clientsecret", "credential", "credentials", "accesstoken", "refreshtoken", "sessionid".
func DefaultRedactKeys() []string

// DefaultRedactQuery: query parameters whose values are always replaced, matched case-insensitively after one
// round of percent-decoding of the name (also /auth/temp-login?token=, 05-authn). A superset of HEAD's
// credentialQueryKeys (internal/server/middleware/log_redact.go:26-37), so OAuth authorization codes,
// PKCE verifiers and refresh tokens in a callback URL never reach the log or a span (SEC2-18). This is
// the one list: 05-http-api's stages reference it instead of restating it:
// "token", "access_token", "refresh_token", "api_key", "apikey", "key", "password", "secret",
// "code", "code_verifier", "sig", "signature".
func DefaultRedactQuery() []string

// URLKeys: URL-keyed attributes whose string values always get URL and path handling, whatever they look
// like (a bare path "/api/core/v1/auth/invites/…" is not URL-shaped):
// "path", "url", "uri", "request_uri", "referer", "url.full", "url.path", "url.query", "http.target", "http.url".
func URLKeys() []string

// RotatingFile is the built-in size-based rotator for Output="file" (no third-party dependency).
type RotatingFile struct{ /* unexported */ }

func OpenRotatingFile(root *os.Root, name string, maxBytes int64, maxFiles int) (*RotatingFile, error)
func (f *RotatingFile) Write(p []byte) (int, error)
func (f *RotatingFile) Close() error
```

**Pipeline (normative order):** level gate (per scope, atomic) → append ctx attrs and trace/span ids from
`trace.SpanContextFromContext(ctx)` → redact (resolve `LogValuer`; deny-listed keys → `"[REDACTED]"`; for
`*url.URL` values, values under `URLKeys()` and other URL-shaped string values: userinfo removed, query params in
`DefaultRedactQuery()` replaced, path rules applied) → fan-out to the primary `slog.TextHandler`/`slog.JSONHandler`
and to taps. Because taps receive redacted records, nothing the journal or the realtime tail stores holds a
token the redactor recognises. The text and JSON handlers already quote control characters in messages and
values, so core adds **no** message sanitizer (locked by `TestHandler_EscapesControlChars`); ABO's two
sanitizers retire.

**Levels.** Resolution for a record from scope `m.s`: `Levels["m.s"]` → `Levels["m"]` → root. Each scope's
gate is a `*slog.LevelVar`; `SetLevels` updates them atomically, so `Enabled` stays one atomic load.
`LevelTrace` renders as `TRACE` via `ReplaceAttr`.

**Module path rules.** A module whose routes carry tokens in other path shapes registers a `PathRule` through
`module.Registrar` ([06-app-sdk](06-app-sdk.md)); the kernel builds the `Redactor` with them before any listener
opens. The invite shape needs no module rule: it is a default (SEC-21).

**Process defaults (in package `app`, not `log`):**

```go
// InstallProcessDefaults points the process-wide defaults at this App's pipeline and telemetry:
//   slog.SetDefault(a.Logging().Root())                       // also redirects the stdlib "log" package
//   otel.SetLogger(valueDroppingSink)                          // logr sink that keeps message + key names, drops values
//   otel.SetErrorHandler(rateLimited)                          // class-keyed limiter, logs through the App's pipeline
//   otel.SetMeterProvider(a.metrics.MeterProvider())           // 05-metrics §3; code that still creates
//   otel.SetTracerProvider(a.metrics.TracerProvider())         //   instruments or spans through the globals
//   otel.SetTextMapPropagator(a.metrics.Propagator())          //   binds to this App's providers (MOD4-3)
// restore() puts the previous values back (tests). Calling it for a second App in the same process
// returns ErrProcessDefaultsTaken: only one App may own the process defaults. It never touches the Prometheus
// default registry (05-metrics §6).
func InstallProcessDefaults(a *App) (restore func(), err error)
```

The value-dropping sink is ABO's `sdkLogSink` (`internal/telemetry/sdklog.go:50-110`) moved, re-targeted at
`Logging.Handler()`. `logr.FromSlogHandler` (`logr` v1.4.4 `slogr.go:30`) is **not** used, because it would
forward the SDK's raw values — the reason the sink exists. `logr` remains in the module graph regardless
(OTel depends on it), so keeping the adapter costs no dependency.

## 4. Lifecycle

1. `app.New`: after `config.Load` (05-config §1.4 step 3), the kernel builds `log.Options` from `core.log`,
   builds the `Redactor` (defaults + `core.log.redact_keys` + module path rules), opens the output (file via
   `os.Root` on `core.paths.state_dir` when `output=file`; open failure is a boot error), constructs `Logging`.
   Every later component receives `Root()`, `Scope("core", …)` or `Module(id)` through its constructor or
   `module.Deps.Log` ([06-app-sdk](06-app-sdk.md)). The metrics provider receives `Logging.Redactor()`
   ([05-metrics](05-metrics.md) §7).
2. Before storage opens, logs go only to the primary output. After storage and tasks are up, the kernel starts
   the journal tap (`core/internal/logjournal`, §5) over the native `JournalStore` — records logged before that
   are not journaled. An app's cutover step that imports pre-core rows (§5) runs earlier, in P4.
3. The kernel subscribes to the `core.log` handle (`OnChange`): `level`, `levels`, `add_source` and
   `redact_keys` apply live; format/output/file need a restart. `logjournal` subscribes to its own
   `core.log.journal` handle: `min_level` and `retention_days` apply live, and `tasks` re-reads the schedule key
   `core.log.schedules.journal_prune` every poll (05-tasks §5T.3.8).
4. `App.Run` shutdown: `Logging.Close(ctx)` runs **last**, after every other component, draining taps
   within the shutdown budget.

## 5. Where the activity log lives (decision)

ABO's "activity log" is four things sharing one store and one page; the platform splits them by consumer
(ABO-migration table: the key families named here are books', 05-storage §5S.9.1):

| Today | Evidence | Goes to | Why |
|---|---|---|---|
| "system" rows derived by parsing slog text lines; `syslog:` rows from `StandardLogger` | `internal/activity/writer.go:149-215`; `pebble_store_activity.go:20-37` | **core `log` journal** (`core/internal/logjournal` over the native `c/log/journal/`; HEAD's `syslog:` rows imported once by books' M5) | Operational, app-neutral, and needed by every app (Sonarr/Radarr ship the same thing as System → Events). Structured `Record`s replace text parsing. |
| "audit"-typed rows (guest tokens, email displaced, user merge, admin edits) | `raw/10` §6, `internal/activity/user_data.go:38-52` | **core `audit`** (new rows only; existing `act:` rows stay books, 05-storage §5S.9.1) | Security trail; 05-rbac. |
| Operation logs (`operationlog:`) | `logger/retention.go:28-31` | **core `tasks`** run log (imported as run log pages by books' tasks cutover, 05-tasks §5T.8.3) | Belongs to the run; 05-tasks. |
| Book-scoped change history, tiers, daily digests, compaction, undo links (`act:` families, 2.5k LOC non-test) | `raw/12` §8, `raw/05-storage.md` rows 55–63 | **books module** (ABO) | Domain feed. The TV/Movies analogue is entity history inside `acquire` (`raw/16` "Implications" row 12) — a different shape — so the §3.4 promotion rule (two real consumers of one shape) is not met. |

The shell's Activity page ([07-ui-shell](07-ui-shell.md)) composes core panes (tasks; the system log for holders
of `core:system.manage`; audit for holders of `core:audit.view`) with module-contributed panes (the books feed).

**Journal contract.** `logjournal` is a `Tap` at `core.log.journal.min_level` (default info) handing records to
`JournalStore.Append` in batches (≤256 records or 1 s, whichever first); the query route and the prune task call
the same store. Its settings are two sections
declared by `logjournal` (L4) and registered by the kernel next to `core.log` (05-config §1.3.1 allows the
nested section paths because `log.Settings` declares neither field): `core.log.journal` and the schedule section
`core.log.schedules` (R2-7 key form `<owner>.<subsystem>.schedules.<name>`, 05-config §1.6):

```go
package logjournal // core/internal/logjournal

type Settings struct { // section core.log.journal
	Enabled       bool   `json:"enabled"`
	MinLevel      string `json:"min_level" schema:"enum=debug|info|warn|error"`
	RetentionDays int    `json:"retention_days" schema:"minimum=0"`
	QueueSize     int    `json:"queue_size" reload:"restart" schema:"minimum=64"`
}

type Schedules struct { // section core.log.schedules
	JournalPrune tasks.ScheduleConfig `json:"journal_prune"` // tasks.ScheduleFromConfig("core.log.schedules.journal_prune")
}
```

- Query: `GET /api/core/v1/system/logs?level=&module=&since=&until=&q=&cursor=`, permission
  **`core:system.manage`** — the journal holds file paths, user names and provider error text, and today's
  equivalent route needs `settings.manage`, which only admin holds (`wire_system_routes.go:28,31`). The seed
  roles `editor` and `viewer` do not reach it ([05-rbac](05-rbac.md), SEC-8).
- Live tail: realtime topic `core.log.recorded.v1`, permission `core:system.manage`, delivered by
  [05-realtime](05-realtime.md)'s permission filter.
- Retention: task `core.log.journal.prune`, `tasks.Parallelism` sequential with reason "single range-delete per
  day bucket", scheduled by `tasks.ScheduleFromConfig("core.log.schedules.journal_prune")`. The key is
  owner-declared with type `tasks.ScheduleConfig` and default `{"enabled":true,"every":"24h"}`
  (R2-7; 05-config §1.6; [05-tasks](05-tasks.md)); there is no bespoke timer and no duration-typed interval key.
- Auth-off reach: `core:system.manage` is `AuthRequired` ([05-rbac](05-rbac.md)), so with auth off the query
  route and the live tail answer 401. HEAD's `GET /api/v1/system/logs` is open with auth off, because `s.perm`
  passes everything in that mode (`internal/server/wire_admin_role.go:18-22`, `wire_system_routes.go:31`).
  This is a **deliberate tightening**: the journal holds file paths, user names and provider error text, and
  with auth off any peer that reaches the port would read them. It is one of the auth-off tightenings the R2
  release note lists (05-rbac §4.11). HEAD's route is deleted in R2 together with the SPA's move to the native
  route (no alias, D10).
- **Native store (SIMP4-3).** `core/internal/logjournal` keeps one record per `c/log/journal/<ULID>`
  (versioned JSON of the record: attributes flattened to dotted keys with string, number or bool values),
  ULIDs from the record time, so `Query` and `Prune` are prefix scans and range deletes. Every app, `hello` and
  `coretest` use it; it lands with `logjournal` in R2's flip F5 (`logjournal` imports `tasks`). Core holds no ABO row
  shape and no storage grant for the journal (R4-1).
- **ABO's rows: imported once (M5; D10, D12 (b)).** HEAD's system-log rows (`syslog:<RFC3339Nano>:<source>`,
  `internal/database/pebble_store_activity.go:20-37`; user ID, source, level, message, time,
  `internal/database/store.go:675-682`) are history that R2 keeps. Books' BootBlocking step M5 of the R2
  migration list (03 §3.8; [09](09-abo-on-core.md) §9.4), carried by the tasks flip F5, decodes them in chunks
  (books code in `internal/books/cutover`) and calls `log.Importer.Import`
  with one `ImportedRecord` per row: `Time`, `Level` and `Message` as stored, `Module` `books`, `Subsystem`
  the row's `source` (HEAD's writers name their subsystem there), `Attrs` the row's `user_id` when it has one,
  `SourceID` the row's key. It then deletes the chunk's source rows; a crash in between re-imports the same
  keys (idempotent, §3). A row that does not decode is quarantined and reported, never dropped (05-storage
  §5S.3.13). M5 runs behind the pre-migration checkpoint; rollback is the offline restore of that checkpoint
  plus the pre-freeze release's binary (the last production release before F3 merged, `.prev` after R2
  deploys; 09). In the same release ABO's own writers and pruner of those rows are deleted (§11), so the
  journal has one writer and one pruner, and the activity `Writer`'s line parsing goes too: from R2 no
  `system` entries are added to the books activity feed; the journal serves them.
- User data in system log lines: HEAD serves the system activity log to `settings.manage` but
  drops lines that name another user's data, and blanks their user ID, unless the caller also holds
  `users.manage` (`activity.NamesOthersUserLog`, `internal/server/handlers/system/handler.go:444-458`). The query
  route and the live tail keep that rule for imported and new records alike: a record the books-registered
  predicate (`log.OtherUserPredicate`, §3) classifies as naming another user's data (registered next to its `PathRule`s, [06](06-app-sdk.md); for an imported record it classifies by subsystem and message, as HEAD's `activity.NamesOthersUserLog(source, message)` does, `internal/activity/user_data.go:258-260`)
  is returned only to principals that also hold `core:users.view`, the read-side successor of that gate
  ([05-rbac](05-rbac.md) §6.1); otherwise it is omitted and `user_id` is blanked.

## 6. Error semantics

| Path | Mode | Why |
|---|---|---|
| Primary output write error | fail-open: record lost, counted, one stderr line per minute | logging must never fail a request |
| `output=file` open fails at boot | fail-closed (boot error) | operator asked for a file |
| Rotation fails at runtime | fail-open: keep writing current file, warn on stderr | availability over tidiness |
| Tap queue full | fail-open: drop, count per tap; a summary record "N dropped" when space returns | caller never blocks (ABO precedent: `internal/activity/writer.go:242-246`) |
| Journal batch write fails | fail-open: drop batch, count, health token `log_journal_degraded` | journal is diagnostic |
| Unknown module in `levels` | fail-open: warn, ignored | modules may be disabled (restart-bound enablement, 03 §3.7) |
| Invalid level string | fail-closed via config validation (`ErrInvalid`) | typo must not silently mean "info" |
| Redactor panics on a value (`LogValue` panic) | fail-open: attr replaced with `"[REDACTED:panic]"` | never leak, never crash |
| Path rule cannot decide (over-long or over-escaped path) | fail-closed: path logged as `/[REDACTED]` | ABO's rule (`redact_path.go:99-106`) |
| `InstallProcessDefaults` twice | `ErrProcessDefaultsTaken` | one owner of process globals |

## 7. Concurrency contract

`Logging` and every `*slog.Logger` it returns are safe for concurrent use. Level gates are `slog.LevelVar`
(atomic). Each tap owns one goroutine and one bounded channel (`queue` records; default 4096 for the journal);
`stop()` is idempotent and joins the goroutine. The primary handler writes under the stdlib handler's own
mutex; `RotatingFile` serializes `Write` and rotation with one mutex. `Redactor` is immutable (rebuilt and
swapped atomically when `redact_keys` changes). `Close` cancels and drains with the caller's deadline. Two
`App`s in one process have fully separate pipelines (`TestTwoAppsIsolated`).

## 8. Config keys

| Key | Type | Default | Hot reload? | Protection |
|---|---|---|---|---|
| `core.log.level` | enum | `info` | yes | none |
| `core.log.levels` | map module[.subsystem] → enum | `{}` | yes | none |
| `core.log.format` | enum `text\|json` | `text` | no | none |
| `core.log.output` | enum `stderr\|stdout\|file` | `stderr` | no | none |
| `core.log.file.path` | path (relative to state dir) | `logs/<app-id>.log` | no | admin |
| `core.log.file.max_size_mb` | int | `100` | no | none |
| `core.log.file.max_files` | int | `7` | no | none |
| `core.log.file.tee_stderr` | bool | `true` | no | none |
| `core.log.add_source` | bool | `false` | yes | none |
| `core.log.redact_keys` | []string (added to defaults; defaults cannot be removed) | `[]` | yes | none |
| `core.log.journal.enabled` | bool | `true` | no | none |
| `core.log.journal.min_level` | enum | `info` | yes | none |
| `core.log.journal.retention_days` | int (0 = keep) | `90` (M2 converts ABO's `log_retention_days` once: HEAD's `0` means the default 90 and a negative value means keep, `internal/config/config.go:1479-1488`, so they become `90` and `0`) | yes | none |
| `core.log.schedules.journal_prune` | `tasks.ScheduleConfig` | `{"enabled":true,"every":"24h"}` | yes (re-read every poll) | none |
| `core.log.journal.queue_size` | int | `4096` | no | none |

The `core.log.*` rows except `core.log.journal.*` and `core.log.schedules.*` belong to the `core.log` section
(`log.Settings`); the `core.log.journal.*` rows to `logjournal.Settings` and `core.log.schedules.journal_prune`
to `logjournal.Schedules` (§5). All are operational
dials (09 §9.2.8): none changes persisted state shape. `logjournal.Settings` and `logjournal.Schedules` are
registered dark from F2, the foundation PRs ([09](09-abo-on-core.md) §9.4), as every core section is: F2 lands
those two settings types in `core/internal/logjournal` and the kernel registers them, so the config cutover M2
(F3) converts `log_retention_days` into a registered `core.log.journal.retention_days` (IMPL-1); the journal, its
store and `core.log.journal.prune` serve from F5.

## 9. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_log_records_total` | counter | `level`, `module` (closed set: enabled module IDs + `core`) |
| `mediacore_log_dropped_total` | counter | `sink` ∈ primary, journal, realtime, (registered tap names) |
| `mediacore_log_journal_write_errors_total` | counter | — |
| `mediacore_log_journal_lag_seconds` | gauge | — |
| `mediacore_log_redactions_total` | counter | `kind` ∈ key, url, path, valuer |

These are recorded by a kernel-side bridge (`log` cannot import `metrics`, R4): the bridge holds the core
`*metrics.Scope` `mediacore.log` and registers observable instruments that read `Logging.Counters()`.

## 10. Tests (core must have)

| Test | Asserts |
|---|---|
| `TestNoGlobals` (archtest, R5) | no `slog.SetDefault`, `log.SetOutput`, `otel.SetLogger`, `otel.SetErrorHandler`, `otel.SetMeterProvider`, `otel.SetTracerProvider`, `otel.SetTextMapPropagator` in core outside `app.InstallProcessDefaults` (its exception list names exactly these seven); `log` exports no package-level variable of slice, map, func or pointer type (the default lists are functions, MOD5-5) |
| `TestDefaultLists_FreshCopies` | appending to or editing the slice one call of `DefaultPathRules()`, `DefaultRedactKeys()`, `DefaultRedactQuery()` or `URLKeys()` returns leaves the next call's result and every `Redactor` unchanged |
| `TestModuleLevels_Live` | `SetLevels` changes `Enabled` for `books` and `books.scanner` without new loggers |
| `TestRedact_DefaultKeys` / `_NestedGroups` / `_URLUserinfoAndQuery` / `_SecretsValue` | values replaced; `secrets.Value` prints `[REDACTED]` in text and JSON |
| `TestRedact_CoreInvitePath` | `/api/core/v1/auth/invites/<token>` and HEAD's path-form link `/invite/<token>` under `path`, `url.path`, inside a full URL and percent-encoded → token absent in text, JSON, journal record and realtime tail record |
| `TestRedactQuery_SupersetOfHEAD` | every key of HEAD's `credentialQueryKeys` (fixture copy) is in `DefaultRedactQuery()`; `?code=…&code_verifier=…&refresh_token=…&SECRET=…` → all values redacted |
| `TestRedact_TempLoginTokenQuery` | `/api/core/v1/auth/temp-login?token=<t>` → `token=[REDACTED]` |
| `TestRedact_PathRuleFailsClosed` | over-long / over-escaped / dot-walked invite paths → `/[REDACTED]` (ABO fixtures ported) |
| `TestRedactor_SpanAttributes` | `Redactor.String` on `url.full`, `url.query`, `http.target` with `apikey=` / `token=` → redacted (consumed by 05-metrics' span exporter test) |
| `TestHandler_EscapesControlChars` | CR, LF, ESC in message and attrs cannot forge a line (text and JSON) |
| `TestTap_NeverBlocks` | `testing/synctest`: stalled tap, 10k logs complete; drops counted |
| `TestContextAttrs_AndTraceIDs` | `WithAttrs` and active span ids appear on records |
| `TestTwoAppsIsolated` | two `Logging` instances, distinct outputs and levels |
| `TestLevelTraceRendering` | `TRACE` label; trace gated below debug |
| `TestInstallProcessDefaults_RestoreAndExclusive` | after install, `otel.GetMeterProvider()`, `otel.GetTracerProvider()` and `otel.GetTextMapPropagator()` return the App's, an instrument created through the global appears in the App's scrape, and `slog.Default()` logs through the pipeline; restore puts all seven back; second install errors; OTel sink drops values |
| `TestJournal_BatchAndRetention` | batching bounds; the prune task calls `JournalStore.Prune` until `more` is false, by `retention_days`; no `core/log` migration step or level stamp exists |
| `logtest.JournalStoreSuite` (exported contract) | run over the native store: `Append` then `Query` round-trips level, module, subsystem, message, time and attributes; filters and cursors; `Prune` bounded and oldest first; its importer cases: importing the same `ImportedRecord`s twice leaves one record each, and imported records sort by their own `Time` among native ones (one suite per interface and its importer, 03 §3.3) |
| `TestCutover_SyslogImported` (ABO) | over a store written by the R0 binary (the corrupt row from 09 §9.3's labelled hand-built overlay, which R0's API cannot write), M5 leaves no `syslog:` row, imports one journal record per row with time, level, source as `Subsystem`, message and `user_id`, quarantines the corrupt row, and resumes after a crash at any chunk point without duplicates |
| `TestNativeJournalStore_RoundTrip` | the native store over `c/log/journal/` passes the suite (SIMP4-3) |
| `TestJournal_OtherUsersLinesNeedUsersView` | a `core:system.manage` principal without `core:users.view` gets neither imported nor new records the books predicate flags, and sees `user_id` blank; with `core:users.view` it gets both (imported records from the R0 fixture) |
| `TestJournal_PruneScheduleKeyDeclared` | `core.log.schedules.journal_prune` is declared with type `tasks.ScheduleConfig` in `logjournal.Schedules` and accepted by `ScheduleFromConfig` at registration |
| `TestJournal_RequiresSystemManage` | `core:system.view`-only principal gets 403 on the query route and no `core.log.recorded.v1` events |
| `TestRotatingFile_RotatesAndCaps` | size rotation, `max_files` respected, `os.Root` confinement |

## 11. Migration from ABO

R5 binds core only (SIMP-3). ABO's logger keeps working as a books-module shim over core handles; moving ABO's
call sites to injected loggers is an optional ratchet, not an extraction stage.

1. **Stage 1, split by when the journal exists (required; no call-site churn; MOD4-5).**
   - **In R1** (09 §9.4; the composition-root work) ABO's `main` calls `app.InstallProcessDefaults`. All 1,460 direct `slog.*` calls, 33
     `slog.Default()` uses, and the 210 package-level `logger.New` vars (which log through `slog.Default()`,
     `logger.go:107`) now reach the App pipeline, including its redaction. Delete `setupFileLogging`
     (`cmd/root.go:698-723`), the second `slog.SetDefault` and `log.SetOutput(aw)` (`server.go:1134-1155`) —
     nothing else. The activity `Writer`, which received slog text through that second default, is fed instead by
     a books `Logging.Tap` at info that renders each record with a `slog.TextHandler` into it, so its line parsing
     and its `system` entries are unchanged; ABO's `StandardLogger` keeps writing system-log rows and HEAD's
     pruner (`internal/logger/retention.go:41`, which stays in ABO's façade, MOD4-6) keeps pruning them, because
     the journal that replaces them does not exist before R2. ABO's file output: `core.log.output=file`,
     `core.log.file.path` set by the books module's defaults to keep a file under the state dir (path change
     release-noted; the old cwd-relative path is not reproduced).
   - **In R2, which starts `logjournal`** over the native store and imports HEAD's rows (M5, §5), ABO deletes,
     in the same release: the activity `Writer`'s line parsing (`internal/activity/writer.go:149-215`) and its
     tap, `StandardLogger`'s system-log writes, and `internal/logger/retention.go` (HEAD's pruner of `syslog:`
     and `operationlog:` rows). The journal becomes the only writer and `core.log.journal.prune` the only pruner
     of the system log; the `operationlog:` rows become run log pages and are pruned by `core.tasks.prune`
     ([05-tasks](05-tasks.md) §5T.8.2). The check "the journal query returns ABO's imported and new records"
     belongs to R2 (09 §9.4).
2. **Stage 2 (required; small) — shim.** `internal/logger` becomes the books-side shim
   `logger.FromSlog(*slog.Logger) Logger` (printf methods format then log with `subsystem` as an **attribute**, so
   per-subsystem levels work; progress/changes/cancel delegate to the `tasks` run handle where one exists, tied to
   the R2 tasks flip). `internal/logging` context helpers become `log.WithAttrs`; `OpContext` becomes task-run attrs set by
   `tasks`; `Sanitize`/`SanitizeErr` are deleted (handler escaping); `RedactInvitePath` moves into core as the
   default invite `PathRule` (§3), so no books hook is needed for it.
3. **Optional ratchet — injection.** The existing ratchet is re-pointed and kept from stage 1: it counts
   `slog.Default()`, package-level `slog.Info/Warn/Error/Debug` and `logger.New(` per file, ceilings only down.
   Packages switch to injected `*slog.Logger` (`module.Deps.Log`, constructor params) when touched; at zero,
   `InstallProcessDefaults` stays only for third-party libraries that log via the default. Listed in the 11 §11.5
   checklist with the config façade removal (prerequisite only for multi-module binaries).
4. `internal/telemetry/sdklog.go` and `errorhandler.go` stay in ABO's thin `internal/telemetry` façade, which
   installs them, until ABO calls `app.InstallProcessDefaults` in R1; they then move into `app`. In R1 that
   helper
   takes over **every** install the façade did — the logger, the error handler, the meter and tracer providers
   and the propagator (§3) — and the façade's install code, including `InitOTEL`'s automatic install
   (`telemetry.go:49`), is deleted; the rest of `internal/telemetry` stays in ABO as books telemetry (03 R5,
   MOD3-3, MOD4-3, MOD4-4; [05-metrics](05-metrics.md) §13).
5. `internal/logger/logtest` stays in ABO for ABO's tests. `coretest` provides its own capture handler, injected
   (it hands the test a `*slog.Logger`) and never `slog.SetDefault`, so core tests stay parallel-safe (03 R5,
   MOD3-3, MOD4-12).
6. `log_level` / `--log-level` map to `core.log.level`, which now actually sets the handler level (today it
   does not, §2). `log_retention_days` is converted once into `core.log.journal.retention_days` by the config
   cutover (M2, 05-config's rename table) with the mapping of §8, because ABO's `0` means "default" and the core
   key's `0` means "keep"; there is no alias (D10).

## 12. Open items

1. OTLP log export (OTel logs bridge) as an additional tap: deferred until a collector exists in the
   reference deployment (ABO's tracing is off in production, `internal/telemetry/telemetry.go:36-38`).
2. Whether the journal should also index `run_id` for "logs of this task" or leave that to the tasks run log
   (recommendation: tasks run log; avoid two sources).

## Objections to 03

None open. `log.Importer` is idempotent instead of staging into the caller's batch, because `log` is L0 and
imports no storage type (R4); 03 §3.3's importer rule states this exception, and keys derived from each
record's time and source ID give books' M5 the same exactly-once result (§3).

Earlier items: both are resolved by 03 v1.3.0: §3.3's `log` row names the journal and
`internal/logjournal`, and R4's bridge rule places `logjournal` at L4 (COR-19).

## Out-of-scope defects found

1. After `NewServer`, `slog.SetDefault` at `internal/server/server.go:1154` replaces the file-logging default
   installed at `cmd/root.go:718`, so the `serve` log file stops receiving records and the level silently
   drops from Debug to Info.
2. `log_level` configuration is never applied to a handler level (`server.go:97-99`, `itunes/import.go:269`
   are its only readers); `--log-level` writes `config.AppConfig.LogLevel` without `Mutate` (`cmd/root.go:650`).
3. The update service logs the caller's Access e-mail on refusal (`internal/config/update_service.go:821-823`),
   which the redaction policy here would classify as `sensitive`.

## Review history

- SIMP-3 — §11: stage 3 became an optional ratchet; ABO logger survives as a books-module shim; L1 updated.
- SIMP-6 — §5, §8: journal prune scheduled by `tasks.ScheduleFromConfig` over `core.log.journal.prune_every`; no bespoke timer (key renamed in round 2, below).
- SEC-21 — L4, §3 `DefaultPathRules` (invite rule moved into core, covers `/api/core/v1/auth/invites/*`), `URLKeys`, temp-login `token` query; journal and tail need `core:system.manage`; tests.
- SEC-22 — L4, §3 `Logging.Redactor()`, `Redactor.URL/String` exposed for span-attribute redaction in 05-metrics.
- SEC-8 (journal side, with SEC-21) — §5: query route and realtime topic moved from `core:system.view` to `core:system.manage`.
- SEC-6 — §8 Protection column; `core.log.file.path` is `ProtectAdmin`.
- COR-19 — header and Objections: `log` at L0, `logjournal` at L4 under 03 v1.3.0's R4 bridge rule; objection closed.
- COR-20 — §9: metrics bridge holds a core `*metrics.Scope`; `Counters()` replaces `Dropped()`.
- DATA-2 — §5: `syslog:` ownership and claim mode referenced from 05-storage §5S.9.1, not restated; `act:` stays books.
- OPS-17 — §8: keys classified as operational dials.

Round 2:

- R2-7 / SIMP2-1 / COR2-6 — §3, §4, §5, §8: journal keys moved to sections `core.log.journal` and `core.log.schedules` owned by `logjournal` (log cannot import tasks); `prune_every` (duration) replaced by `core.log.schedules.journal_prune` of type `tasks.ScheduleConfig` (binding form `<owner>.<subsystem>.schedules.<name>`), default every 24h; test.
- SEC2-18 — §3 `DefaultRedactQuery` ⊇ HEAD's `credentialQueryKeys` (adds `refresh_token`, `secret`, `code`, `code_verifier`); the one list 05-http-api references; `TestRedactQuery_SupersetOfHEAD`.
- SEC2-21 — §5: auth-off unreachability of the journal recorded as a deliberate tightening (09 §9.2.6 exception).
- SEC2-6 — §3 `DefaultPathRules` comment and `TestRedact_CoreInvitePath` cover the legacy shell link `/invite/{token}`.
- Header — 03 v1.4.0. Also corrected the `log_retention_days` alias: ABO's `0` means default 90 (`config.go:1479-1488`), so it needs a `Convert` (§8, §11).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): Round-1 history line no longer writes a non-conforming `ScheduleFromConfig("…")` literal (05-tasks doc-lint).

Round 3:

- R3-1 / DATA3-14 — §5 "Legacy rows": `syslog:` stays a books family read in place through a logged grant (read and retention delete); `core/log#1` adopt step deleted, so logging contributes no level step or rollback floor; legacy prune moves to the journal prune task; rollback visibility stated; §11 step 1; test. (Superseded in round 4 by R4-1: no grant; ABO supplies its own `JournalStore`.)
- R3-1 (activity read in place) — §5: HEAD's other-user-data filter on system log lines kept for legacy and native records (`core:users.view`); test.
- MOD3-3 — §11 steps 4–5: SDK logger and error handler stay in ABO's telemetry façade until S2; `logtest` becomes an injected capture handler in `coretest`, never `slog.SetDefault`. (Amended in round 4: `logtest` stays in ABO, MOD4-12; the helper also installs the OTel providers, MOD4-3.)

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): §5: the `syslog:` grant comes into force with `logjournal` at S5; `core/log#1`/`Floor` remnants removed from §5 and the test table; §3 defines `log.OtherUserPredicate`, the type 06's `Registrar.LogOtherUserData` takes. (Superseded in round 4 by R4-1: no grant; ABO supplies its own `JournalStore`.)

Round 4:

- R4-1 / SIMP4-2 — L5, §3 `log.JournalStore` (with `JournalQuery`, `JournalPage`) replaces the `syslog:` grant and the books `ReadAdapter`; §5 ABO's store contract (HEAD-shape rows, attributes in a books-owned extension record with the row hash, never extra members in the legacy row), rollback now costless; `logtest.JournalStoreSuite`. (Superseded by A-2.)
- SIMP4-3 — §5 native store over `c/log/journal/` for apps without legacy rows, landing at S5; `TestNativeJournalStore_RoundTrip`.
- SIMP4-8 — §5: the "grant without an adapter fails boot" sentence is gone with the grant (no absent-grantee rule remains in this file).
- MOD4-3 — L3, §3 `InstallProcessDefaults` also sets the meter and tracer providers and the propagator, never the Prometheus registry (05-metrics §6 owns `/metrics`); `TestNoGlobals` exception list and `TestInstallProcessDefaults_RestoreAndExclusive` updated; §11 item 4.
- MOD4-4 — §11 item 4: only the façade's install code is deleted at S2; the rest of `internal/telemetry` stays in ABO.
- MOD4-5 — §11 stage 1 split: S2 r3 does the process defaults and file output only (the activity `Writer` fed by a books tap); line parsing, `StandardLogger`'s system-log writes and HEAD's pruner are removed in the S5 release that starts the journal; the "journal shows ABO records" done-check moves to S5.
- MOD4-6 — §11 stage 1: `internal/logger/retention.go` stays in ABO's façade until the S5 release, then is deleted.
- MOD4-12 — §11 item 5: `internal/logger/logtest` stays in ABO; `coretest` has its own injected capture handler.

Integration pass (round 4):

- integration pass (round 4): §5: ABO key names (`syslog:`) removed from the store contract and user-data bullet; the HEAD→core table labelled as an ABO-migration table.

Round 5:

- MOD5-5 / SIMP5-14 — §3: `DefaultPathRules`, `DefaultRedactKeys`, `DefaultRedactQuery` and `URLKeys` are functions returning fresh copies (R5, `TestNoGlobals`); the invite comment says the legacy `/invite/{token}` page is served in path form S4–S9 (FE4-2); pipeline and test references updated; `TestDefaultLists_FreshCopies`.
- COR5-12 / MOD5-10 — §3 `JournalStore`: stated as the exception to "every write method takes the caller's `*storage.Batch`" (append-only, own batch, never a multi-owner commit).
- DATA5-4 — §5 ABO's store: the journal extension declares no Widening field, so it is never `Suspended`; fingerprint named `legacy_ref.fp` (DATA5-16). (Superseded by A-2.)
- OPS5-12 — §5 rollback: the S5 removal of the activity `Writer`'s line parsing leaves the older build's activity feed without `system` entries for the S5+ period (09 §9.2.1); the journal itself adds no cost.
- OPS5-14 — L3, §11 item 4: ABO calls `app.InstallProcessDefaults` at S2 r3, the release that deletes the façade's installs and sets `PromRegisterer`.

Round 6:

- integration pass (round 6): §5 names `legacy.Extension` (was `storage.Extension`; R6-3, 05-storage's one list). (Superseded by A-2.)

Round 7:

- DATA7-8 / R7-3 — §5 ABO's store: `Prune` deletes extension records through `legacy.Extension.Delete` with the record it read (`ctx`, `seen`), a conditional delete, never a blind one in books' `Serialized` `ext` sub-prefix. (Superseded by A-2.)
- COR7-7 — §5: the extension segment `logs` is cited as 05-storage §5S.3.11's fixed segment for `legacy.Stores.Logs`. (Superseded by A-2.)
- Header — 03 reference v1.9.1.

Advisor:

- A-2 (ADV-S1) — L5, §5 ABO's store, §10 `logtest.JournalStoreSuite`: the books extension record for journal attributes (and its fingerprint, stale handling and conditional extension deletes) is deleted; ABO's store writes and reads HEAD's row shape only (`user_id`, source, level, message, time), drops what that shape cannot hold through S10, and classifies other-user records by source and message as HEAD does; native stores keep every field.
- A-3 (ADV-S2) — §5 `Prune`: no storage-enforced write check on the legacy rows; HEAD's one-writer, one-pruner discipline (R7-3's conditional delete deleted with the extension).
- Header — 03 reference v1.11.1.
- integration pass (advisor): 09 §9.2.1 row 6 cited; journal rows on ABO confirmed HEAD-shaped with no extension family.

D10–D12 revision (2026-11-04):

- Owner decisions D10 (no backward compatibility), D11 (no soaks) and D12 (b) (log history kept, converted)
  applied. L5, §3 `JournalStore`, §4 step 2 and §5: the journal has one implementation, the native store;
  ABO's own `JournalStore` over HEAD's `syslog:` rows (`Manifest.LegacyStores.Logs`, the HEAD-shaped
  `Append`/`Query`/`Prune`, its rollback paragraph) is deleted. New `log.Importer` and
  `ImportedRecord`: books' R2 step M5 imports HEAD's rows once, idempotently, and deletes them; undecodable
  rows are quarantined.
- §5 auth-off: the alias of `/api/v1/system/logs` and its exclusion from the reach gate are deleted; HEAD's
  route goes in R2 and the tightening is a release-note line.
- §8: `retention_days` is converted once by M2, not aliased. §10: the suite runs over the native store only;
  importer cases and `TestCutover_SyslogImported` added. §11: stages re-keyed to R1/R2; the
  `log_retention_days` alias replaced by the one-time conversion. L3 and §3: `PromRegisterer` mention removed;
  the invite path rule's S4–S9 window removed. Consistency pass: the `log.Importer` objection settled (03 §3.3
  states the idempotent-importer exception) and D12's answers cited as D12 (a)–(d).

Round 13 (2026-11-04): IMPL-1, IMPL-8, CONFORM-1. §8: `logjournal`'s two sections are registered dark from F2 so
M2 at F3 writes a registered key; §10: the corrupt `syslog:` row comes from the fixture's hand-built overlay; §5:
rollback names the pre-freeze release's binary.
