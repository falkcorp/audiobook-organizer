<!-- file: docs/specs/platform-core/05-logging.md -->
<!-- version: 1.0.0 -->
<!-- guid: b62e0a52-4f32-4aeb-8bb9-4429ec976537 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Logging (`core/log`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §3, §8, §9;
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §6–7;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md) (`go-logr/logr` v1.4.4).
Normative: [03-architecture](03-architecture.md) R4 (`log` is in the lowest layer), R5 (no `slog.SetDefault`
in core), §3.6 naming. Config mechanics: [05-config](05-config.md).

## Decisions at a glance

| # | Decision |
|---|---|
| L1 | `log/slog` is the only logging API. Core hands out `*slog.Logger` values; there is no core `Logger` interface. ABO's printf-style `logger.Logger` (with progress, change recording and cancellation folded in) is not carried into core. |
| L2 | One handler pipeline per `App`; per-module child loggers carry `module=<id>` (and optionally `subsystem=<name>`); levels are settable per module (and per module.subsystem) at runtime. |
| L3 | Core never calls `slog.SetDefault`, `log.SetOutput`, `otel.SetLogger` or `otel.SetErrorHandler`. `app.InstallProcessDefaults` is the single opt-in helper that does all four for an app that wants it (ABO does). |
| L4 | Redaction is in the pipeline, once, before fan-out: type-based (`secrets.Value`, `LogValuer`), key-based (deny-list), URL-based (userinfo and credential query params). |
| L5 | The **system log journal** (persisted operational records ≥ info, queryable, retained N days) is core. The **domain activity feed** stays in the app module. Security events go to core `audit`; task run logs to core `tasks`. |
| L6 | The `go-logr/logr` adapter stays, but only as the value-dropping OTel SDK sink inside `app.InstallProcessDefaults`; `logr` is never imported by other core packages. |

## 1. Purpose & scope

Construct and own each `App`'s `slog` pipeline: output format and destination, levels (global, per module,
per module.subsystem, changeable live), context-carried attributes, trace correlation, redaction, and
asynchronous taps that feed the system journal and the realtime log tail. Out of scope: audit records
([05-rbac](05-rbac.md)), task run logs and progress ([05-tasks](05-tasks.md)), metrics ([05-metrics](05-metrics.md)),
the domain activity feed (app module).

## 2. Current state at HEAD

| Aspect | Today | Anchor |
|---|---|---|
| Packages | `internal/logger` (877 LOC) and `internal/logging` (529 LOC) | `raw/12` §1, §3 |
| `logger.Logger` | composite of `LevelLogger` (printf-style Trace…Error), `ProgressReporter`, `ChangeRecorder`, `CancellationReporter`, `SubLoggerFactory`; `Change` carries `BookID` (domain leak) | `internal/logger/logger.go:44-98` |
| `StandardLogger` | formats with `fmt.Sprintf`, sanitizes, prefixes `"<subsystem>: "` into the **message** (no attribute), writes via `slog.Default()`; Trace → Debug; progress/changes/cancel are no-ops; optional `ActivityLogWriter` copies info+ lines into the system activity log | `logger/logger.go:105-127`, `logger/standard.go:13-66` |
| Context helpers | `logging.Info/Warn/Error/Debug(ctx, …)` add `OpContext` attrs and call package-level `slog.*` | `internal/logging/structured.go:47-72`, `opcontext.go:15-28` |
| Two sanitizers | `logger.sanitizeLogLine` escapes every C0 control and DEL; `logging.Sanitize` escapes only CR and LF | `logger/sanitize.go:40-77`, `logging/sanitize.go:28-43` |
| App-specific redaction | invite-token path redaction | `logging/redact_path.go:107,165` |
| Usage | `logger.New(` 322 calls in 238 non-test files, 210 of them package-level vars (`raw/12` counts 207); `slog.Info/Warn/Error/Debug(` 1,460 calls in 261 files; `slog.Default()` 33 in 15 files; `logging.Info/…(` 231 in 37 files | this writer's grep; `raw/12` §3 |
| Ratchet | `TestGuard_NoDirectSlogCalls` ceiling per file, may only go down | `internal/logger/slog_guard_ratchet_test.go` |
| Process globals | `slog.SetDefault` at `cmd/root.go:718` (text handler, **Debug**, stdout + file) and again at `internal/server/server.go:1154` (text handler, **Info**, into the activity writer); stdlib `log.SetOutput(aw)` | `cmd/root.go:698-723`, `internal/server/server.go:1134-1155` |
| File logging | `logs/audiobook-organizer-<date>.log` relative to the **working directory**, date fixed at process start, never rotated | `cmd/root.go:700-712` |
| `log_level` config | not wired to any handler level; only gates gin debug mode and one iTunes branch; the `--log-level` flag writes `AppConfig.LogLevel` directly | `internal/server/server.go:97-99`, `internal/itunes/import.go:269`, `cmd/root.go:649-651` |
| Activity coupling | the activity `Writer` is an `io.Writer` that tees to stdout and **parses every slog text line** back into `ActivityEntry{Type:"system"}` rows; tier derived from level | `internal/activity/writer.go:92-105,149-215` |
| System log rows | `syslog:<RFC3339Nano>:<source>` JSON rows; pruned by `log_retention_days` (default 90) together with `operationlog:` rows | `internal/database/pebble_store_activity.go:20-37,434`, `internal/logger/retention.go:28-31,49`, `internal/config/log_retention.go:18` |
| OTel SDK logger | process-wide logr sink that **drops values** (the SDK logs raw env input such as malformed OTLP headers) and rate-limits; installed by `InitOTEL` | `internal/telemetry/sdklog.go:1-110`, `telemetry.go:46-49` |
| logr | direct dependency only for that sink | `go.mod:14`, `internal/telemetry/sdklog.go:17` |

Consequence worth stating: in `serve`, the second `slog.SetDefault` (`server.go:1154`) replaces the first
(`cmd/root.go:718`), so after `NewServer` the log **file stops receiving records** and the level drops from
Debug to Info (the activity writer tees to stdout only, `internal/activity/writer.go:94`). Listed under
out-of-scope defects; the design below makes it impossible (one pipeline, sinks are added, never replaced).

## 3. Go API

Package `github.com/falkcorp/mediacore/log`. Imports: stdlib and `go.opentelemetry.io/otel/trace` (span
context only). Does **not** import `config`, `secrets`, `storage` or `metrics` (R4: `log` is the lowest layer;
`secrets.Value` redacts itself through `slog.LogValuer`, so `log` needs no import of it).

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
	KeyRoute     = "route"
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
// registered by the kernel, because log cannot import config (R4).
type Settings struct {
	Level      string            `json:"level" schema:"enum=trace|debug|info|warn|error"`
	Levels     map[string]string `json:"levels"` // "books" or "books.scanner" -> level
	Format     string            `json:"format" reload:"restart" schema:"enum=text|json"`
	Output     string            `json:"output" reload:"restart" schema:"enum=stderr|stdout|file"`
	File       FileSettings      `json:"file" reload:"restart"`
	AddSource  bool              `json:"add_source"`
	RedactKeys []string          `json:"redact_keys"`
	Journal    JournalSettings   `json:"journal"`
}

type FileSettings struct {
	Path      string `json:"path" schema:"format=path"`
	MaxSizeMB int    `json:"max_size_mb" schema:"minimum=1"`
	MaxFiles  int    `json:"max_files" schema:"minimum=1"`
	TeeStderr bool   `json:"tee_stderr"`
}

type JournalSettings struct {
	Enabled       bool   `json:"enabled"`
	MinLevel      string `json:"min_level" schema:"enum=debug|info|warn|error"`
	RetentionDays int    `json:"retention_days" schema:"minimum=0"`
	QueueSize     int    `json:"queue_size" reload:"restart" schema:"minimum=64"`
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
func (l *Logging) SetLevels(root slog.Level, per map[string]slog.Level) (unknown []string)
func (l *Logging) Level(scope string) slog.Level

// Tap registers an asynchronous consumer of redacted records at or above min.
// The caller never blocks: when the bounded queue is full the record is dropped and counted.
func (l *Logging) Tap(name string, min slog.Level, queue int, fn func(ctx context.Context, r Record)) (stop func())

// Dropped reports per-tap drop counts (read by the metrics bridge in the kernel).
func (l *Logging) Dropped() iter.Seq2[string, uint64]

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

func NewRedactor(extraKeys ...string) *Redactor // always includes DefaultRedactKeys
func (r *Redactor) Attr(a slog.Attr) slog.Attr

var DefaultRedactKeys = []string{ // matched case-insensitively after stripping '-', '_', '.'
	"password", "passwd", "secret", "token", "apikey", "authorization", "cookie", "setcookie",
	"privatekey", "clientsecret", "credential", "credentials", "accesstoken", "refreshtoken", "sessionid",
}

// RotatingFile is the built-in size-based rotator for Output="file" (no third-party dependency).
type RotatingFile struct{ /* unexported */ }

func OpenRotatingFile(root *os.Root, name string, maxBytes int64, maxFiles int) (*RotatingFile, error)
func (f *RotatingFile) Write(p []byte) (int, error)
func (f *RotatingFile) Close() error
```

**Pipeline (normative order):** level gate (per scope, atomic) → append ctx attrs and trace/span ids from
`trace.SpanContextFromContext(ctx)` → redact (resolve `LogValuer`, deny-listed keys → `"[REDACTED]"`,
`*url.URL` and URL-shaped string values: userinfo removed, query params named
`token|access_token|api_key|apikey|key|sig|signature|password` replaced) → fan-out to the primary
`slog.TextHandler`/`slog.JSONHandler` and to taps. The text and JSON handlers already quote control
characters in messages and values, so core adds **no** message sanitizer (locked by
`TestHandler_EscapesControlChars`); ABO's two sanitizers retire.

**Levels.** Resolution for a record from scope `m.s`: `Levels["m.s"]` → `Levels["m"]` → root. Each scope's
gate is a `*slog.LevelVar`; `SetLevels` updates them atomically, so `Enabled` stays one atomic load.
`LevelTrace` renders as `TRACE` via `ReplaceAttr`.

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

1. `app.New`: after `config.Load` (operator layers only), the kernel builds `log.Options` from `core.log`,
   opens the output (file via `os.Root` on `core.paths.state_dir` when `output=file`; open failure is a boot
   error), constructs `Logging`. Every later component receives `Root()`, `Scope("core", …)` or
   `Module(id)` through its constructor or `module.Deps.Log` ([06-app-sdk](06-app-sdk.md)).
2. Before storage opens, logs go only to the primary output. After storage opens, the kernel starts the
   journal tap (`core/internal/logjournal`, §5) — records logged before that are not journaled.
3. The kernel subscribes to the `core.log` handle (`OnChange`): `level`, `levels`, `add_source`,
   `redact_keys`, `journal.min_level`, `journal.retention_days` apply live; format/output/file need a restart.
4. `App.Run` shutdown: `Logging.Close(ctx)` runs **last**, after every other component, draining taps
   within the shutdown budget.

## 5. Where the activity log lives (decision)

ABO's "activity log" is four things sharing one store and one page; the platform splits them by consumer:

| Today | Evidence | Goes to | Why |
|---|---|---|---|
| "system" rows derived by parsing slog text lines; `syslog:` rows from `StandardLogger` | `internal/activity/writer.go:149-215`; `pebble_store_activity.go:20-37` | **core `log` journal** (`core/internal/logjournal`, keyspace `c/log/journal/`) | Operational, app-neutral, and needed by every app (Sonarr/Radarr ship the same thing as System → Events). Structured `Record`s replace text parsing. |
| "audit"-typed rows (guest tokens, email displaced, user merge, admin edits) | `raw/10` §6, `internal/activity/user_data.go:38-52` | **core `audit`** | Security trail; 05-rbac. |
| Operation logs (`operationlog:`) | `logger/retention.go:28-31` | **core `tasks`** run log | Belongs to the run; 05-tasks. |
| Book-scoped change history, tiers, daily digests, compaction, undo links (`act:` families, 2.5k LOC non-test) | `raw/12` §8, `raw/05-storage.md` rows 55–63 | **books module** (ABO) | Domain feed. The TV/Movies analogue is entity history inside `acquire` (`raw/16` "Implications" row 12) — a different shape — so the §3.4 promotion rule (two real consumers of one shape) is not met. |

The shell's Activity page ([07-ui-shell](07-ui-shell.md)) composes core panes (tasks, system log, audit for
holders of `core:audit.view`) with module-contributed panes (the books feed).

**Journal contract.** `logjournal` is a `Tap` at `journal.min_level` (default info) writing
`c/log/journal/<ULID>` records in batches (≤256 records or 1 s, whichever first). Query:
`GET /api/core/v1/system/logs?level=&module=&since=&until=&q=&cursor=` (permission `core:system.view`, as for the other system pages in [05-health](05-health.md));
live tail: realtime topic `core.log.recorded.v1`, permission-filtered ([05-realtime](05-realtime.md)).
Retention: task `core.log.journal.prune` (daily, `tasks.Parallelism` sequential with reason "single
range-delete per day bucket"). ABO's existing `syslog:` family is **read in place** by the query adapter as
a legacy claim and ages out under the same retention (90 days by ABO default); no copy migration.

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
| `InstallProcessDefaults` twice | `ErrProcessDefaultsTaken` | one owner of process globals |

## 7. Concurrency contract

`Logging` and every `*slog.Logger` it returns are safe for concurrent use. Level gates are `slog.LevelVar`
(atomic). Each tap owns one goroutine and one bounded channel (`queue` records; default 4096 for the journal);
`stop()` is idempotent and joins the goroutine. The primary handler writes under the stdlib handler's own
mutex; `RotatingFile` serializes `Write` and rotation with one mutex. `Close` cancels and drains with the
caller's deadline. Two `App`s in one process have fully separate pipelines (`TestTwoAppsIsolated`).

## 8. Config keys

| Key | Type | Default | Hot reload? |
|---|---|---|---|
| `core.log.level` | enum | `info` | yes |
| `core.log.levels` | map module[.subsystem] → enum | `{}` | yes |
| `core.log.format` | enum `text\|json` | `text` | no |
| `core.log.output` | enum `stderr\|stdout\|file` | `stderr` | no |
| `core.log.file.path` | path (relative to state dir) | `logs/<app-id>.log` | no |
| `core.log.file.max_size_mb` | int | `100` | no |
| `core.log.file.max_files` | int | `7` | no |
| `core.log.file.tee_stderr` | bool | `true` | no |
| `core.log.add_source` | bool | `false` | yes |
| `core.log.redact_keys` | []string (added to defaults; defaults cannot be removed) | `[]` | yes |
| `core.log.journal.enabled` | bool | `true` | no |
| `core.log.journal.min_level` | enum | `info` | yes |
| `core.log.journal.retention_days` | int (0 = keep) | `90` | yes |
| `core.log.journal.queue_size` | int | `4096` | no |

## 9. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_log_records_total` | counter | `level`, `module` (closed set: enabled module IDs + `core`) |
| `mediacore_log_dropped_total` | counter | `sink` ∈ primary, journal, realtime, (registered tap names) |
| `mediacore_log_journal_write_errors_total` | counter | — |
| `mediacore_log_journal_lag_seconds` | gauge | — |
| `mediacore_log_redactions_total` | counter | `kind` ∈ key, url, valuer |

These are recorded by a kernel-side bridge (`log` cannot import `metrics`, R4): the bridge registers
observable instruments reading `Logging` counters.

## 10. Tests (core must have)

| Test | Asserts |
|---|---|
| `TestNoGlobals` (archtest, R5) | no `slog.SetDefault`, `log.SetOutput`, `otel.SetLogger`, `otel.SetErrorHandler` in core outside `app.InstallProcessDefaults` |
| `TestModuleLevels_Live` | `SetLevels` changes `Enabled` for `books` and `books.scanner` without new loggers |
| `TestRedact_DefaultKeys` / `_NestedGroups` / `_URLUserinfoAndQuery` / `_SecretsValue` | values replaced; `secrets.Value` prints `[REDACTED]` in text and JSON |
| `TestHandler_EscapesControlChars` | CR, LF, ESC in message and attrs cannot forge a line (text and JSON) |
| `TestTap_NeverBlocks` | `testing/synctest`: stalled tap, 10k logs complete; drops counted |
| `TestContextAttrs_AndTraceIDs` | `WithAttrs` and active span ids appear on records |
| `TestTwoAppsIsolated` | two `Logging` instances, distinct outputs and levels |
| `TestLevelTraceRendering` | `TRACE` label; trace gated below debug |
| `TestInstallProcessDefaults_RestoreAndExclusive` | restore works; second install errors; OTel sink drops values |
| `TestJournal_BatchAndRetention` / `TestJournal_ReadsLegacySyslog` | batching bounds; prune; legacy rows returned by query |
| `TestRotatingFile_RotatesAndCaps` | size rotation, `max_files` respected, `os.Root` confinement |

## 11. Migration from ABO

1. **Stage 1 (no call-site churn):** ABO's `main` calls `app.InstallProcessDefaults`. All 1,460 direct
   `slog.*` calls, 33 `slog.Default()` uses, and the 210 package-level `logger.New` vars (which log through
   `slog.Default()`, `logger.go:107`) now reach the App pipeline. Delete `setupFileLogging`
   (`cmd/root.go:698-723`), the second `slog.SetDefault` and `log.SetOutput(aw)` (`server.go:1134-1155`), and the
   activity `Writer`'s line parsing (`internal/activity/writer.go:149-215`) — replaced by the journal. ABO's
   compat file output: `core.log.output=file`, `core.log.file.path` set by the books module's defaults to
   keep a file under the state dir (path change release-noted; the old cwd-relative path is not reproduced).
2. **Stage 2:** `internal/logger` shrinks to a books-side adapter `logger.FromSlog(*slog.Logger) Logger`
   (printf methods format then log with `subsystem` as an **attribute**; progress/changes/cancel delegate to
   the `tasks` run handle where one exists). `internal/logging` context helpers become
   `log.WithAttrs`; `OpContext` becomes task-run attrs set by `tasks`; `Sanitize`/`SanitizeErr` are deleted
   (handler escaping); `RedactInvitePath` stays app-side, registered as a books redaction hook through
   `module.Registrar` (route-shape knowledge is the app's).
3. **Stage 3:** packages receive `*slog.Logger` by injection (`module.Deps.Log`, constructor params). The
   existing ratchet is re-pointed: it counts `slog.Default()`, package-level `slog.Info/Warn/Error/Debug`
   and `logger.New(` per file, ceilings only down; at zero, `InstallProcessDefaults` stays only for
   third-party libraries that log via the default.
4. `internal/telemetry/sdklog.go` and `errorhandler.go` move into `app` (process-defaults helper);
   `InitOTEL`'s automatic install (`telemetry.go:49`) is deleted.
5. `log_level` / `--log-level` map to `core.log.level`, which now actually sets the handler level (today it
   does not, §2).

## 12. Open items

1. OTLP log export (OTel logs bridge) as an additional tap: deferred until a collector exists in the
   reference deployment (ABO's tracing is off in production, `internal/telemetry/telemetry.go:36-38`).
2. Whether the journal should also index `run_id` for "logs of this task" or leave that to the tasks run log
   (recommendation: tasks run log; avoid two sources).

## Objections to 03

1. §3.3 describes `log` as "`log/slog` construction, per-module child loggers, redaction". This spec adds the
   **journal** (persisted system log) as a core concern; its implementation lives in `core/internal/logjournal`
   (it needs `storage`, which `log` cannot import), and only the `Record`/`Tap` types are in `log`. Not a
   contradiction, but the §3.3 row should mention it so the package mapping (04) assigns ABO's `syslog:`
   family and the activity writer's system rows to core rather than to the books module.

## Out-of-scope defects found

1. After `NewServer`, `slog.SetDefault` at `internal/server/server.go:1154` replaces the file-logging default
   installed at `cmd/root.go:718`, so the `serve` log file stops receiving records and the level silently
   drops from Debug to Info.
2. `log_level` configuration is never applied to a handler level (`server.go:97-99`, `itunes/import.go:269`
   are its only readers); `--log-level` writes `config.AppConfig.LogLevel` without `Mutate` (`cmd/root.go:650`).
3. The update service logs the caller's Access e-mail on refusal (`internal/config/update_service.go:821-823`),
   which the redaction policy here would classify as `sensitive`.
