<!-- file: docs/specs/platform-core/05-logging.md -->
<!-- version: 1.3.0 -->
<!-- guid: b62e0a52-4f32-4aeb-8bb9-4429ec976537 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Logging (`core/log`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §3, §8, §9;
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §6–7;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md) (`go-logr/logr` v1.4.4).
Normative: [03-architecture](03-architecture.md) v1.5.1 R4 (`log` is in **L0** with `entity`, `lifecycle`,
`version`, `metrics`; the journal implementation `core/internal/logjournal` imports `storage` and `tasks`, so by
R4's bridge rule it sits at **L4** and only the kernel imports it), R5 (no `slog.SetDefault` in core; R5 binds
core only, and ABO's logger shim is an allowed books-module value), §3.6 naming. Config
mechanics: [05-config](05-config.md).

## Decisions at a glance

| # | Decision |
|---|---|
| L1 | `log/slog` is the only logging API in core. Core hands out `*slog.Logger` values; there is no core `Logger` interface. ABO's printf-style `logger.Logger` (with progress, change recording and cancellation folded in) is not carried into core; it survives in ABO as a books-module shim over core handles (§11). |
| L2 | One handler pipeline per `App`; per-module child loggers carry `module=<id>` (and optionally `subsystem=<name>`); levels are settable per module (and per module.subsystem) at runtime. |
| L3 | Core never calls `slog.SetDefault`, `log.SetOutput`, `otel.SetLogger` or `otel.SetErrorHandler`. `app.InstallProcessDefaults` is the single opt-in helper that does all four for an app that wants it (ABO does). |
| L4 | Redaction is in the pipeline, once, before fan-out: type-based (`secrets.Value`, `LogValuer`), key-based (deny-list), URL-based (userinfo and credential query params) and **path-based** (one-time tokens in URL paths, including core's own invite route). The same `*log.Redactor` redacts trace span attributes ([05-metrics](05-metrics.md) §3, SEC-22). |
| L5 | The **system log journal** (persisted operational records ≥ info, queryable, retained N days) is core and is readable only with `core:system.manage`. The **domain activity feed** stays in the app module. Security events go to core `audit`; task run logs to core `tasks`. |
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

// Context-carried attributes (replace internal/logging.OpContext). The handler appends them to every
// record logged with that ctx; tasks, httpapi and realtime set run_id / request_id / route / user_id.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context
func AttrsFrom(ctx context.Context) []slog.Attr

// Redaction.
type Redactor struct{ /* immutable */ }

func NewRedactor(extraKeys []string, extraPaths ...PathRule) *Redactor // always includes the defaults below
func (r *Redactor) Attr(a slog.Attr) slog.Attr
func (r *Redactor) URL(raw string) string           // userinfo removed, credential query params replaced, path rules applied
func (r *Redactor) Path(p string) string            // path rules only
func (r *Redactor) String(key, value string) string // key deny-list, then URL/path handling for URL-keyed or URL-shaped values

// PathRule redacts a token-bearing path segment. Rules are pure functions of the path (no route table).
type PathRule struct {
	Name string                   // "invite", or a module-registered name
	Fn   func(path string) string // must fail closed (return a redacted path) when unsure
}

// DefaultPathRules covers core's own token-bearing routes: any "invite"/"invites" segment followed by
// another segment, at any position, after percent-decoding and dot-walk checks — ABO's RedactInvitePath
// algorithm (internal/logging/redact_path.go:99-120) moved into core. It covers the core route
// /api/core/v1/auth/invites/{token} (05-authn), the books legacy invite routes, and the legacy shell link
// /invite/{token} that is answered by a redirect to /invite#token= during the window (SEC2-6, 07).
// Fragment-form links (/invite#token=, /login/temp#token=) never reach the server.
var DefaultPathRules = []PathRule{ /* invite */ }

var DefaultRedactKeys = []string{ // matched case-insensitively after stripping '-', '_', '.'
	"password", "passwd", "secret", "token", "apikey", "authorization", "cookie", "setcookie",
	"privatekey", "clientsecret", "credential", "credentials", "accesstoken", "refreshtoken", "sessionid",
}

// Query parameters whose values are always replaced, matched case-insensitively after one round of
// percent-decoding of the name (also /auth/temp-login?token=, 05-authn). A superset of HEAD's
// credentialQueryKeys (internal/server/middleware/log_redact.go:26-37), so OAuth authorization codes,
// PKCE verifiers and refresh tokens in a callback URL never reach the log or a span (SEC2-18). This is
// the one list: 05-http-api's stages reference it instead of restating it.
var DefaultRedactQuery = []string{
	"token", "access_token", "refresh_token", "api_key", "apikey", "key", "password", "secret",
	"code", "code_verifier", "sig", "signature",
}

// URL-keyed attributes: string values under these keys always get URL and path handling,
// whatever they look like (a bare path "/api/core/v1/auth/invites/…" is not URL-shaped).
var URLKeys = []string{
	"path", "url", "uri", "request_uri", "referer", "url.full", "url.path", "url.query", "http.target", "http.url",
}

// RotatingFile is the built-in size-based rotator for Output="file" (no third-party dependency).
type RotatingFile struct{ /* unexported */ }

func OpenRotatingFile(root *os.Root, name string, maxBytes int64, maxFiles int) (*RotatingFile, error)
func (f *RotatingFile) Write(p []byte) (int, error)
func (f *RotatingFile) Close() error
```

**Pipeline (normative order):** level gate (per scope, atomic) → append ctx attrs and trace/span ids from
`trace.SpanContextFromContext(ctx)` → redact (resolve `LogValuer`; deny-listed keys → `"[REDACTED]"`; for
`*url.URL` values, values under `URLKeys` and other URL-shaped string values: userinfo removed, query params in
`DefaultRedactQuery` replaced, path rules applied) → fan-out to the primary `slog.TextHandler`/`slog.JSONHandler`
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
// InstallProcessDefaults points the process-wide defaults at this App's pipeline:
//   slog.SetDefault(a.Logging().Root())   // also redirects the stdlib "log" package
//   otel.SetLogger(valueDroppingSink)      // logr sink that keeps message + key names, drops values
//   otel.SetErrorHandler(rateLimited)      // class-keyed limiter, logs through the App's pipeline
// restore() puts the previous values back (tests). Calling it for a second App in the same process
// returns ErrProcessDefaultsTaken: only one App may own the process defaults.
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
   the journal tap (`core/internal/logjournal`, §5) — records logged before that are not journaled.
3. The kernel subscribes to the `core.log` handle (`OnChange`): `level`, `levels`, `add_source` and
   `redact_keys` apply live; format/output/file need a restart. `logjournal` subscribes to its own
   `core.log.journal` handle: `min_level` and `retention_days` apply live, and `tasks` re-reads the schedule key
   `core.log.schedules.journal_prune` every poll (05-tasks §5T.3.8).
4. `App.Run` shutdown: `Logging.Close(ctx)` runs **last**, after every other component, draining taps
   within the shutdown budget.

## 5. Where the activity log lives (decision)

ABO's "activity log" is four things sharing one store and one page; the platform splits them by consumer:

| Today | Evidence | Goes to | Why |
|---|---|---|---|
| "system" rows derived by parsing slog text lines; `syslog:` rows from `StandardLogger` | `internal/activity/writer.go:149-215`; `pebble_store_activity.go:20-37` | **core `log` journal** (`core/internal/logjournal`, keyspace `c/log/journal/`) | Operational, app-neutral, and needed by every app (Sonarr/Radarr ship the same thing as System → Events). Structured `Record`s replace text parsing. |
| "audit"-typed rows (guest tokens, email displaced, user merge, admin edits) | `raw/10` §6, `internal/activity/user_data.go:38-52` | **core `audit`** (new rows only; existing `act:` rows stay books, 05-storage §5S.9.1) | Security trail; 05-rbac. |
| Operation logs (`operationlog:`) | `logger/retention.go:28-31` | **core `tasks`** run log | Belongs to the run; 05-tasks. |
| Book-scoped change history, tiers, daily digests, compaction, undo links (`act:` families, 2.5k LOC non-test) | `raw/12` §8, `raw/05-storage.md` rows 55–63 | **books module** (ABO) | Domain feed. The TV/Movies analogue is entity history inside `acquire` (`raw/16` "Implications" row 12) — a different shape — so the §3.4 promotion rule (two real consumers of one shape) is not met. |

The shell's Activity page ([07-ui-shell](07-ui-shell.md)) composes core panes (tasks; the system log for holders
of `core:system.manage`; audit for holders of `core:audit.view`) with module-contributed panes (the books feed).

**Journal contract.** `logjournal` is a `Tap` at `core.log.journal.min_level` (default info) writing
`c/log/journal/<ULID>` records in batches (≤256 records or 1 s, whichever first). Its settings are two sections
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
  route, the live tail and the legacy `/api/v1/system/logs` alias answer 401. Today they are open with auth off,
  because `s.perm` passes everything in that mode (`internal/server/wire_admin_role.go:18-22`,
  `wire_system_routes.go:31`). This is a **deliberate tightening**: the journal holds file paths, user names and
  provider error text, and with auth off any peer that reaches the port would read them. It is recorded in
  09 §9.2.6 as an exception to the auth-off reach invariant, and the alias is excluded from the auth-off half of
  `TestLegacyRoleReachUnchanged` (SEC2-21).
- Legacy rows (R3-1): ABO's `syslog:` family (`syslog:<RFC3339Nano>:<source>`,
  `internal/database/pebble_store_activity.go:20-37`) stays a **books** family and is **read in place**; there is
  no copy, no adopt step (`core/log#1` is deleted), no claim hand-over and no level step, so no `Floor` and no
  rollback floor come from logging (DATA3-14). `logjournal` reaches it through one explicit, logged storage
  grant ([05-storage](05-storage.md) §5S.9.1 row 14): read, plus `Delete` for retention, never `Set`. A
  books-supplied `ReadAdapter` decodes the rows into `Record`s for the query route, so core holds no ABO row
  shape (R1). New records go only to `c/log/journal/` (a core-only family). Retention: the prune task also
  deletes aged `syslog:` rows through the grant, by `core.log.journal.retention_days`, in bounded chunks as HEAD
  does (`pebble_store_activity.go:428-434`); HEAD's own `syslog:` pruner stops in the release where the journal
  becomes the only writer (§11), so each row has one pruner. **Rollback:** an older build sees its `syslog:` rows
  but not journal records written after the upgrade; rows an older build writes during a rollback are read by
  the query adapter after roll-forward.
- User data in system log lines: HEAD serves the system activity log (`syslog:` rows) to `settings.manage` but
  drops lines that name another user's data, and blanks their user ID, unless the caller also holds
  `users.manage` (`activity.NamesOthersUserLog`, `internal/server/handlers/system/handler.go:444-458`). The query
  route and the live tail keep that rule for legacy and native records alike: a record the books-registered
  predicate classifies as naming another user's data (registered next to its `PathRule`s, [06](06-app-sdk.md))
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
| `core.log.journal.retention_days` | int (0 = keep) | `90` (ABO `log_retention_days`, window alias with a `Convert`: legacy `0` means the default 90 and a negative value means keep, `internal/config/config.go:1479-1488`, so they map to `90` and `0`) | yes | none |
| `core.log.schedules.journal_prune` | `tasks.ScheduleConfig` | `{"enabled":true,"every":"24h"}` | yes (re-read every poll) | none |
| `core.log.journal.queue_size` | int | `4096` | no | none |

The `core.log.*` rows except `core.log.journal.*` and `core.log.schedules.*` belong to the `core.log` section
(`log.Settings`); the `core.log.journal.*` rows to `logjournal.Settings` and `core.log.schedules.journal_prune`
to `logjournal.Schedules` (§5). All are operational
dials (09 §9.2.8): none changes persisted state shape.

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
| `TestNoGlobals` (archtest, R5) | no `slog.SetDefault`, `log.SetOutput`, `otel.SetLogger`, `otel.SetErrorHandler` in core outside `app.InstallProcessDefaults` |
| `TestModuleLevels_Live` | `SetLevels` changes `Enabled` for `books` and `books.scanner` without new loggers |
| `TestRedact_DefaultKeys` / `_NestedGroups` / `_URLUserinfoAndQuery` / `_SecretsValue` | values replaced; `secrets.Value` prints `[REDACTED]` in text and JSON |
| `TestRedact_CoreInvitePath` | `/api/core/v1/auth/invites/<token>` and the legacy shell link `/invite/<token>` under `path`, `url.path`, inside a full URL and percent-encoded → token absent in text, JSON, journal record and realtime tail record |
| `TestRedactQuery_SupersetOfHEAD` | every key of HEAD's `credentialQueryKeys` (fixture copy) is in `DefaultRedactQuery`; `?code=…&code_verifier=…&refresh_token=…&SECRET=…` → all values redacted |
| `TestRedact_TempLoginTokenQuery` | `/api/core/v1/auth/temp-login?token=<t>` → `token=[REDACTED]` |
| `TestRedact_PathRuleFailsClosed` | over-long / over-escaped / dot-walked invite paths → `/[REDACTED]` (ABO fixtures ported) |
| `TestRedactor_SpanAttributes` | `Redactor.String` on `url.full`, `url.query`, `http.target` with `apikey=` / `token=` → redacted (consumed by 05-metrics' span exporter test) |
| `TestHandler_EscapesControlChars` | CR, LF, ESC in message and attrs cannot forge a line (text and JSON) |
| `TestTap_NeverBlocks` | `testing/synctest`: stalled tap, 10k logs complete; drops counted |
| `TestContextAttrs_AndTraceIDs` | `WithAttrs` and active span ids appear on records |
| `TestTwoAppsIsolated` | two `Logging` instances, distinct outputs and levels |
| `TestLevelTraceRendering` | `TRACE` label; trace gated below debug |
| `TestInstallProcessDefaults_RestoreAndExclusive` | restore works; second install errors; OTel sink drops values |
| `TestJournal_BatchAndRetention` / `TestJournal_ReadsLegacySyslog` | batching bounds; prune of journal records and of aged `syslog:` rows through the grant (no `Set` possible); legacy rows returned by query; no `core/log` adopt step or level stamp exists |
| `TestJournal_OtherUsersLinesNeedUsersView` | a `core:system.manage` principal without `core:users.view` gets neither legacy nor native records the books predicate flags, and sees `user_id` blank; with `core:users.view` it gets both (HEAD fixture) |
| `TestJournal_PruneScheduleKeyDeclared` | `core.log.schedules.journal_prune` is declared with type `tasks.ScheduleConfig` in `logjournal.Schedules` and accepted by `ScheduleFromConfig` at registration |
| `TestJournal_RequiresSystemManage` | `core:system.view`-only principal gets 403 on the query route and no `core.log.recorded.v1` events |
| `TestRotatingFile_RotatesAndCaps` | size rotation, `max_files` respected, `os.Root` confinement |

## 11. Migration from ABO

R5 binds core only (SIMP-3). ABO's logger keeps working as a books-module shim over core handles; moving ABO's
call sites to injected loggers is an optional ratchet, not an extraction stage.

1. **Stage 1 (required; no call-site churn):** ABO's `main` calls `app.InstallProcessDefaults`. All 1,460 direct
   `slog.*` calls, 33 `slog.Default()` uses, and the 210 package-level `logger.New` vars (which log through
   `slog.Default()`, `logger.go:107`) now reach the App pipeline, including its redaction. Delete
   `setupFileLogging` (`cmd/root.go:698-723`), the second `slog.SetDefault` and `log.SetOutput(aw)`
   (`server.go:1134-1155`), and the activity `Writer`'s line parsing (`internal/activity/writer.go:149-215`) —
   replaced by the journal. In the same release ABO's `StandardLogger` stops writing `syslog:` rows and HEAD's
   `syslog:` pruner (`internal/logger/retention.go:41`) stops pruning them; the journal prune task takes over
   (§5); `operationlog:` pruning stays with [05-tasks](05-tasks.md). ABO's compat file output: `core.log.output=file`, `core.log.file.path` set by the books
   module's defaults to keep a file under the state dir (path change release-noted; the old cwd-relative path is
   not reproduced).
2. **Stage 2 (required; small) — shim.** `internal/logger` becomes the books-side shim
   `logger.FromSlog(*slog.Logger) Logger` (printf methods format then log with `subsystem` as an **attribute**, so
   per-subsystem levels work; progress/changes/cancel delegate to the `tasks` run handle where one exists, tied to
   09 S5). `internal/logging` context helpers become `log.WithAttrs`; `OpContext` becomes task-run attrs set by
   `tasks`; `Sanitize`/`SanitizeErr` are deleted (handler escaping); `RedactInvitePath` moves into core as the
   default invite `PathRule` (§3), so no books hook is needed for it.
3. **Optional ratchet — injection.** The existing ratchet is re-pointed and kept from stage 1: it counts
   `slog.Default()`, package-level `slog.Info/Warn/Error/Debug` and `logger.New(` per file, ceilings only down.
   Packages switch to injected `*slog.Logger` (`module.Deps.Log`, constructor params) when touched; at zero,
   `InstallProcessDefaults` stays only for third-party libraries that log via the default. Listed in the 11 §11.5
   checklist with the config façade removal (prerequisite only for multi-module binaries).
4. `internal/telemetry/sdklog.go` and `errorhandler.go` stay in ABO's thin `internal/telemetry` façade, which
   installs them, until `app.InstallProcessDefaults` exists at S2; they then move into `app` and
   `InitOTEL`'s automatic install (`telemetry.go:49`) is deleted with the façade (03 R5, MOD3-3;
   [05-metrics](05-metrics.md) §13).
5. `internal/logger/logtest` moves into `coretest` as a capture handler the test injects (it hands the test a
   `*slog.Logger`); it never calls `slog.SetDefault` (`logtest.go:83` today), so core tests stay parallel-safe.
   A helper that sets the process default for ABO code stays in ABO (03 R5, MOD3-3).
6. `log_level` / `--log-level` map to `core.log.level`, which now actually sets the handler level (today it
   does not, §2). `log_retention_days` maps to `core.log.journal.retention_days` (alias, 05-config §1.12.3) with
   the `Convert` of §8, because ABO's `0` means "default" and the core key's `0` means "keep".

## 12. Open items

1. OTLP log export (OTel logs bridge) as an additional tap: deferred until a collector exists in the
   reference deployment (ABO's tracing is off in production, `internal/telemetry/telemetry.go:36-38`).
2. Whether the journal should also index `run_id` for "logs of this task" or leave that to the tasks run log
   (recommendation: tasks run log; avoid two sources).

## Objections to 03

None open. Both earlier items are resolved by 03 v1.3.0: §3.3's `log` row names the journal and
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

- R3-1 / DATA3-14 — §5 "Legacy rows": `syslog:` stays a books family read in place through a logged grant (read and retention delete); `core/log#1` adopt step deleted, so logging contributes no level step or rollback floor; legacy prune moves to the journal prune task; rollback visibility stated; §11 step 1; test.
- R3-1 (activity read in place) — §5: HEAD's other-user-data filter on system log lines kept for legacy and native records (`core:users.view`); test.
- MOD3-3 — §11 steps 4–5: SDK logger and error handler stay in ABO's telemetry façade until S2; `logtest` becomes an injected capture handler in `coretest`, never `slog.SetDefault`.
