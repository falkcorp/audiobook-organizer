<!-- file: docs/agent-tasks/platform-core/R1-36-core-log-logging-pipeline.md -->
<!-- version: 1.0.2 -->
<!-- guid: 151713b2-8479-41b2-9b1c-2a47c6953d88 -->
<!-- last-edited: 2026-11-05 -->

# R1-36 — core/log's `Logging` pipeline (New, Root/Module/Scope, SetLevels, Level, Tap, Counters, Close), WithAttrs/AttrsFrom, LevelTrace, RotatingFile and the pipeline order; Sanitize, SanitizeErr, OpContext and the package-level helpers deleted from core/log

| Field | Value |
|---|---|
| ID | `R1-36` |
| Tier | **T3** (Opus / high): a new public logging API with live levels and file rotation, frozen by the API-stability gate |
| Release / wave | R1 / W11 |
| Depends on | R1-07, R1-32, R1-09 (each merged on `main` before you start) |
| Spec | 05-logging §3 (Go API, "Pipeline (normative order)", "Levels"), §4 (lifecycle), §6 (error semantics), §7 (concurrency contract), §10 (core must-have tests), §11 item 2 (stage 2: `Sanitize`/`SanitizeErr` deleted, context helpers become `log.WithAttrs`, `OpContext` becomes task-run attrs); 03 R4 (`log` is L0), R5 (no globals) |
| Branch | `agent/platform-core-r1-36-core-log-logging-pipeline` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the
named required checks of 09 §9.2.1 that exist at your base.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this
list without recording it under the PR's "Scope notes"):

- `core/log/` (a whole directory: step 6 creates `logging.go`, `attrs.go`, `rotate.go`, `handler.go` and
  `logging_test.go`, and deletes R1-07's moved files `sanitize.go`, `sanitize_test.go`, `structured.go`,
  `structured_test.go`, `opcontext.go` and `integration_test.go`)
- `core/health/flight.go` (modify: re-point R1-09's `Sanitize` call)
- `core/internal/archtest_layers_test.go` (modify: the in-module control for the deleted health -> log edge)
- The 43 ABO files that step 6 rewrites (measured at `f7ac5b11e`):
  `internal/dedup/{auto_resolve,book_runtime,drain_stale,engine}.go`, `internal/httputil/respond.go`,
  `internal/importer/service.go`, `internal/metabatch/upgrade.go`,
  `internal/metafetch/{book_runtime,file_pipeline,service}.go`,
  `internal/plugins/dedup/{build_candidate_index,check_book,emb_reencode,embed_async,lsh_index_build,register}.go`,
  `internal/plugins/maintenance/{activity_filter_index_backfill,cleanup,compact_activity_log,intro_transcribe,optimize,recompact_activity_digests}.go`,
  `internal/scanner/{chapter_consolidation,oversized_dir,scanner,shattered_coalesce}.go`,
  `internal/server/{ai_scan_apply_op,batch_apply_one,batch_apply_refusal,duplicates_helpers,duplicates_ops,entities_ops,library_ai_parse_op,library_core_ops,metadata_bulk_fetch_log,metadata_ops,series_rename_ops,server_maintenance_deps,server_search}.go`,
  `internal/server/handlers/ai.go`, `internal/server/handlers/dedup/manual_candidates.go`,
  `internal/server/handlers/operations/{handler,pause}.go`
- `internal/server/opcontext.go` and `internal/server/opcontext_test.go` (create)
- `internal/logger/ctx.go` and `internal/logger/ctx_test.go` (create: the books-side `InfoCtx`/`WarnCtx`/`ErrorCtx`/
  `DebugCtx` helpers step 6 moves the callers onto; `internal/logger/` is otherwise R1-18's, and these two files are
  this row's)

Record every ABO path above under the PR's "Scope notes".

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-36" -b agent/platform-core-r1-36-core-log-logging-pipeline origin/main
cd "$REPO/.worktrees/platform-core-r1-36"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and
`go -C core build ./...`. The spec anchors at `748fd787a`; caller counts below were measured at the planning base
`f7ac5b11e` with `internal/logging` (R1-07 moves it to `core/log` and keeps the import alias `logging`).

## Goal

Build one App's logging pipeline in `github.com/falkcorp/mediacore/log` exactly as 05-logging §3 specifies, so that
R1-13's kernel can construct it in `app.New` (05-logging §4 step 1), R1-18 can consume `Logging.Tap` and
`RotatingFile`, and F5-15 finds `Record` and `Tap`. Then finish stage 2's core half (05-logging §11 item 2): core `log`
stops exporting the names the spec deletes.

1. `Logging` with `New`, `Root`, `Module`, `Scope`, `Handler`, `Redactor`, `SetLevels`, `Level`, `Tap`, `Counters`,
   `Close`; the types it needs (`Options`, `Format`, `Record`, `Counters`); the level constants with `LevelTrace`;
   `ParseLevel`; the normative attribute keys; the `core.log` section type `Settings`/`FileSettings`/`DefaultSettings`.
2. `WithAttrs`/`AttrsFrom` (replace `OpContext`'s role).
3. The handler in the normative order: level gate (per scope, atomic) → ctx attrs and trace/span IDs from
   `trace.SpanContextFromContext(ctx)` → redact through R1-32's `*Redactor` → fan-out to the primary
   `slog.TextHandler`/`slog.JSONHandler` and to taps. No message sanitizer: the stdlib handlers quote control characters
   (05-logging §3).
4. `RotatingFile` (size-based, `os.Root`-confined, no third-party dependency).
5. Delete `Sanitize`, `SanitizeErr`, `OpContext` (with `WithOp`, `OpFromContext`, `AddEntity`, `SetStatus`) and the
   package-level `Info`/`Warn`/`Error`/`Debug` from core `log`; rewrite their callers; re-point
   `core/health/flight.go` to the handler's escaping.

**This row builds no kernel wiring.** `app.New` constructing `Logging` and `Deps.Log = Logging.Module(id)` are R1-13's;
the books tap and file selection are R1-18's; live re-application of `core.log.level`, `levels` and `redact_keys`
(`SetLevels` on a config change) is F3-07's; the journal (`JournalStore`, `Importer`, `JournalQuery`, `JournalPage`,
`ImportedRecord`, `OtherUserPredicate`) is F5-15's; the `Redactor`, `PathRule` and the four default lists are R1-32's.

## Code contract (copy exactly from 05-logging §3)

```go
package log

const (
	LevelTrace = slog.Level(-8)
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

func ParseLevel(s string) (slog.Level, error) // "trace|debug|info|warn|error", case-insensitive

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

type Logging struct{ /* unexported */ }

func New(o Options) (*Logging, error)

func (l *Logging) Root() *slog.Logger                          // module="core"
func (l *Logging) Module(id string) *slog.Logger               // module=id; panics never: invalid id -> error at kernel validation
func (l *Logging) Scope(module, subsystem string) *slog.Logger // module + subsystem, own level gate
func (l *Logging) Handler() slog.Handler                       // for adapters (gin bridge, logr sink)
func (l *Logging) Redactor() *Redactor                         // the pipeline's redactor, for span redaction (05-metrics)
func (l *Logging) SetLevels(root slog.Level, per map[string]slog.Level) (unknown []string)
func (l *Logging) Level(scope string) slog.Level

func (l *Logging) Tap(name string, min slog.Level, queue int, fn func(ctx context.Context, r Record)) (stop func())
func (l *Logging) Counters() Counters
func (l *Logging) Close(ctx context.Context) error // drains taps until ctx expires

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

func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context
func AttrsFrom(ctx context.Context) []slog.Attr

type RotatingFile struct{ /* unexported */ }

func OpenRotatingFile(root *os.Root, name string, maxBytes int64, maxFiles int) (*RotatingFile, error)
func (f *RotatingFile) Write(p []byte) (int, error)
func (f *RotatingFile) Close() error
```

`Counters`' fields are not spelled out in the spec. 05-logging §3 says it reports "per-tap drop counts, records per
level/module and redactions per kind", and §9's metrics read it (`mediacore_log_records_total{level,module}`,
`mediacore_log_dropped_total{sink}`, `mediacore_log_redactions_total{kind}` with `kind` in key, url, path, valuer).
Define `type Counters struct` (field names coined here) holding exactly those three maps as value copies, so a caller
never shares the pipeline's state. `DefaultSettings()` returns 05-logging §8's defaults: `level` `info`, `levels` `{}`,
`format` `text`, `output` `stderr`, `file.path` `logs/<app-id>.log` (leave `Path` empty here: the kernel fills the
app ID), `max_size_mb` 100, `max_files` 7, `tee_stderr` true, `add_source` false, `redact_keys` `[]`.

## Invariants (each has a test below)

- I1. **Pipeline order** (05-logging §3): level gate → ctx attrs + trace/span IDs → redact → fan-out. Taps receive only
  redacted records, so nothing a tap stores holds a token the redactor recognises.
- I2. **Levels** (05-logging §3 "Levels"): a record from scope `m.s` resolves `Levels["m.s"]` → `Levels["m"]` → root.
  Each scope's gate is a `*slog.LevelVar`; `SetLevels` updates them atomically, so `Enabled` stays one atomic load and
  existing loggers see the change without being rebuilt. `SetLevels` returns the keys of `per` that name no known
  scope (`unknown`); they are ignored, never an error (05-logging §6 "Unknown module in `levels`": fail-open). A
  scope created later still resolves through the current table.
- I3. `LevelTrace` renders as `TRACE` through `ReplaceAttr` and is gated below debug.
- I4. **Taps never block the caller** (05-logging §6, §7): each tap owns one goroutine and one bounded channel of
  `queue` records; a full queue drops the record and counts it per tap; a summary record "N dropped" goes to the tap
  when space returns; `stop()` is idempotent and joins the goroutine; `Close(ctx)` stops accepting, drains every tap
  until `ctx` expires and returns `ctx.Err()` if it expired first.
- I5. **No globals** (03 R5; 05-logging L3): no `slog.SetDefault`, `log.SetOutput` or `otel.Set*` call in `core/log`;
  no exported package-level variable of slice, map, func or pointer type. Two `Logging` values in one process share
  nothing.
- I6. **Error semantics** (05-logging §6): a primary-output write error loses the record, counts it under sink
  `primary` and writes at most one stderr line per minute; a `LogValue` panic replaces the attr with
  `"[REDACTED:panic]"` (R1-32's `Redactor.Attr` may already do this; do not add a second recovery if it does);
  rotation failure keeps writing the current file and warns on stderr; `OpenRotatingFile` failure is returned (the
  kernel turns it into a boot error).
- I7. **`RotatingFile`** serialises `Write` and rotation with one mutex; rotates when the next write would pass
  `maxBytes`; keeps at most `maxFiles` files (the live one included); every open, rename and remove goes through the
  `*os.Root`, so a `name` with `..` or an absolute path fails.
- I8. **Imports** (05-logging §3; 03 R4): stdlib and `go.opentelemetry.io/otel/trace` only. No import of `config`,
  `secrets`, `storage` or `metrics`.
- I9. After step 6, `core/log` exports none of `Sanitize`, `SanitizeErr`, `OpContext`, `WithOp`, `OpFromContext`,
  `Info`, `Warn`, `Error`, `Debug`, and no file in either module calls them.

## Dependency check

```bash
test -f core/log/redactor.go && git grep -q 'func NewRedactor(' -- core/log && echo R1-32_OK        # expect R1-32_OK
test -f core/health/flight.go && git grep -n 'corelog.Sanitize' -- core/health/flight.go | wc -l      # expect 1 (R1-09's interim call)
test ! -e internal/logging && echo R1-07_OK                                                           # expect R1-07_OK
```

If any check fails, STOP and report which predecessor's output is missing. Do not build it here.

## Re-verify (expected at your base)

```bash
ls core/log/                                       # expect R1-07's files (integration_test.go opcontext.go redact_path.go redact_path_test.go retention.go retention_test.go sanitize.go sanitize_test.go structured.go structured_test.go) plus R1-32's redactor.go redactor_test.go
git grep -n 'type Logging struct\|func WithAttrs(\|type RotatingFile struct' -- core/log | wc -l   # expect 0
git grep -n 'LevelTrace' -- core/log | wc -l       # expect 0
M=github.com/falkcorp/audiobook-organizer
git grep -l 'logging\.\(Sanitize\|SanitizeErr\|WithOp\|OpContext\|OpFromContext\|Info\|Warn\|Error\|Debug\)\b' -- '*.go' ':!core/log' | wc -l   # expect 43 (the ABO list above; fewer if a later row already rewrote some)
git grep -n 'logging\.\(Info\|Warn\|Error\|Debug\)(' -- '*.go' ':!core/log' | wc -l     # expect 231 at f7ac5b11e
git grep -n 'logging\.Sanitize(\|logging\.SanitizeErr(' -- '*.go' ':!core/log' | wc -l  # expect 15 (8 Sanitize, 7 SanitizeErr) at f7ac5b11e
git grep -l 'logging\.OpContext\|logging\.WithOp(' -- '*.go' ':!core/log'               # expect internal/server/{duplicates_ops,library_core_ops,metadata_ops}.go
(cd core && go list -m go.opentelemetry.io/otel/trace)                                 # expect one module line with a version (R1-10's otel requirements)
grep -c 'go.opentelemetry.io/otel/trace' core/archtest/thirdparty_allow.txt             # expect 1
```

If `go.opentelemetry.io/otel/trace` is not in core's module graph or not in the allowlist, add it (`go -C core get`,
pinned to the root's version; one line in `core/archtest/thirdparty_allow.txt`) and record `core/go.mod`,
`core/go.sum` and the allowlist under "Scope notes". If the caller counts are higher than shown, rewrite the extra
callers the same way and list them.

## Tests first (write each before its code; 05-logging §10, names copied exactly)

In `core/log/logging_test.go`:

- `TestModuleLevels_Live` — `SetLevels` changes `Enabled` for `books` and `books.scanner` without new loggers.
- `TestHandler_EscapesControlChars` — CR, LF, ESC in message and attrs cannot forge a line (text and JSON).
- `TestContextAttrs_AndTraceIDs` — `WithAttrs` and active span IDs appear on records.
- `TestTwoAppsIsolated` — two `Logging` instances, distinct outputs and levels.
- `TestLevelTraceRendering` — `TRACE` label; trace gated below debug.
- `TestRotatingFile_RotatesAndCaps` — size rotation, `max_files` respected, `os.Root` confinement.
- `TestTap_NeverBlocks` (05-logging §10) — `testing/synctest`: a stalled tap, 10k logs complete; drops are counted
  in `Counters()` under the tap's name.
- `TestPipelineOrder_TapsSeeRedacted` (name coined here) — a record carrying `password=…` and a URL with `?token=…`
  reaches a tap and the primary output redacted, and carries the ctx attrs added before redaction (I1).
- `TestSetLevels_UnknownScopesReported` (name coined here) — `SetLevels` returns the unknown keys and changes nothing
  for them (I2).
- `TestClose_DrainsWithinDeadline` (name coined here, `testing/synctest`) — `Close` drains a slow tap until the
  context expires and returns `context.DeadlineExceeded` (I4).

Not this row's: `TestInstallProcessDefaults_RestoreAndExclusive` is R1-15's (it needs `app.InstallProcessDefaults`);
the `TestRedact_*`
rows and `TestDefaultLists_FreshCopies` are R1-32's; `TestNoGlobals` is R1-13's archtest, and its `log` half must pass
over this row's files.

## Steps

1. Write the tests above (red).
2. `core/log/logging.go`: the constants, `ParseLevel`, `Format`, `Settings`, `FileSettings`, `DefaultSettings`,
   `Options`, `Logging`, `Record`, `Counters` and the `Logging` methods. `New` validates `Options` (nil `Output`,
   unknown `Format`, nil `Redactor` → error; `Redactor` nil may instead mean `NewRedactor(nil)` only if R1-32's
   contract says so, otherwise error), builds the primary stdlib handler with `ReplaceAttr` for `TRACE`, and the
   level table of `*slog.LevelVar`.
3. `core/log/handler.go`: the one `slog.Handler` behind `Root`/`Module`/`Scope`/`Handler`, implementing I1. `Enabled`
   reads the scope's `LevelVar`; `Handle` appends `AttrsFrom(ctx)` and the span IDs (`KeyTraceID`, `KeySpanID`, only
   when the span context is valid), redacts every attr through `l.Redactor()`, writes the primary handler, then offers
   a `Record` copy to each tap at or above its `min`. `WithAttrs`/`WithGroup` return handlers that keep the scope.
   Count records per level and module, drops per sink, redactions per kind.
4. `core/log/attrs.go`: `WithAttrs`/`AttrsFrom` on an unexported context key; `WithAttrs` appends to the attrs already
   in `ctx` (copy, never share the backing array).
5. `core/log/rotate.go`: `RotatingFile` per I7. Rotated names are `<name>.1` … `<name>.<maxFiles-1>`, oldest removed.
6. **Stage 2 deletions (05-logging §11 item 2).** Delete `core/log/sanitize.go`, `sanitize_test.go`, `structured.go`,
   `structured_test.go`, `opcontext.go` and `integration_test.go` (it tests only `WithOp`/`Info`). Then rewrite every
   caller found by the re-verify greps, mechanically:
   - First create `internal/logger/ctx.go` with `InfoCtx`, `WarnCtx`, `ErrorCtx` and `DebugCtx`, each
     `func(ctx context.Context, msg string, args ...any)` (names fixed by the plan). Each logs through the existing books
     logger path (the `slog.Default()` route `logToStdout` uses, with the package's `sanitizeLogLine` barrier on the
     message and string values, as HEAD's `logging.Info` sanitised them) and appends
     `corelog.AttrsFrom(ctx)` (import `corelog "github.com/falkcorp/mediacore/log"`). The file sits inside
     `TestGuard_NoDirectSlogCalls`'s permanent exempt prefix `internal/logger/` (HEAD
     `internal/logger/slog_guard_test.go`), so its own `slog` calls are allowed.
   - `logging.Info(ctx, ` → `logger.InfoCtx(ctx, `, and the same for `Warn`→`WarnCtx`, `Error`→`ErrorCtx`,
     `Debug`→`DebugCtx`. Add the `internal/logger` import where the file lacks it. Do **not** rewrite to package-level
     `slog.InfoContext` and the like: HEAD's `TestGuard_NoDirectSlogCalls` fails any non-test file under `internal/` or
     `cmd/` that calls a package-level `slog.Info/Warn/Error/Debug`, a `*Context` variant or `slog.Default` beyond its
     entry in `internal/logger/slog_guard_ratchet_test.go`. Several of the 43 files (for example
     `internal/server/metadata_ops.go` and `internal/scanner/scanner.go`) have no entry at all, and the ratchet's
     ceilings (`slogRatchetFileCeiling = 268`, `slogRatchetCallCeiling = 1485` at HEAD) may only go down (05-logging §2
     "Ratchet", §11 item 3). Never add an entry or raise a ceiling.
   - Add `internal/logger/ctx_test.go` with `TestCtxHelpers_AppendAttrsFrom` (name coined here): each of the four
     helpers, called with a `ctx` carrying `corelog.WithAttrs(ctx, slog.String("k", "v"))` and a CR/LF in the message,
     writes one record holding `k=v` with the control characters escaped.
   - `logging.Sanitize(x)` → `x`; `logging.SanitizeErr(err)` → `err`. The stdlib text and JSON handlers quote control
     characters, and the `internal/logger` printf methods (`pauseLog.Warn(...)` in
     `internal/server/handlers/operations/pause.go`) still sanitise their own line until R1-18's shim.
   - `logging.OpContext` and `logging.WithOp`: in package `internal/server` only (3 files). Create
     `internal/server/opcontext.go` (name coined here) with an unexported `opContext` type carrying the same four fields
     and the `AddEntity`/`SetStatus` methods, implementing `slog.LogValuer` so that its `LogValue()` returns a group
     with the keys `opID`, `opType`, `opStatus` and, when non-empty, `entities` (JSON-encoded, as HEAD's `opAttrs`
     does). Replace `ctx = logging.WithOp(ctx, op)` with
     `ctx = logging.WithAttrs(ctx, slog.Any("", op))`: the value is resolved at log time, so a later `SetStatus` shows on
     later records, and an empty-key group is inlined, so the keys stay flat as today. Add
     `internal/server/opcontext_test.go` with `TestOpContextAttrsResolvedAtLogTime` (name coined here): through a
     `Logging` built in the test, a record logged after `SetStatus("failed")` carries `opStatus=failed` and the flat keys.
     This type is the books-side stand-in until 05-tasks sets run attrs (05-logging §11 item 2); do not add it to core.
   - Keep the import alias `logging "github.com/falkcorp/mediacore/log"` wherever the file still uses another
     `core/log` name; remove the import where nothing is left.
   Note for reviewers: between this merge and R1-15's `app.InstallProcessDefaults`, `slog.Default()` is still HEAD's
   text handler, which does not read `AttrsFrom`. The step 6 helpers append `corelog.AttrsFrom(ctx)` themselves, so
   the three `internal/server` files' op attrs still reach those logs; a record that bypasses the helpers lacks them
   until R1-15 merges. Do not rely on the R1 freeze to cover that window: if OWNER-01 overrode the R1 freeze (09
   §9.2.1; R1-35 then sets `''`), a production minor in this window logs without those op attrs. Record that in the PR.
7. **`core/health/flight.go`:** replace `corelog.Sanitize(fmt.Sprintf(format, args...))` with
   `fmt.Sprintf(format, args...)` in `logf`, and remove the `corelog` import if nothing else uses it. The handler's
   escaping now covers it (05-logging §3, `TestHandler_EscapesControlChars`). Leave the rest of the file unchanged.
8. `go -C core fix ./log/...` (core's `fix-check`); headers on every new and modified file; one fragment
   `changelog.d/<today>_core_logging_pipeline.md` (`### Changed`: "Internal: core's logging pipeline (levels per module,
   context attributes, taps, rotating file) and removal of the old log-sanitising helpers."). Commits may be split
   (pipeline; deletions with the caller rewrite in one commit, so every commit builds).

## Acceptance

```bash
go build ./... && go -C core build ./...                                                     # expect exit 0
go -C core vet ./log/... ./health/...                                                        # expect exit 0
go -C core test -race -count=1 ./log/... ./health/...                                        # expect ok x2
go -C core test -count=1 -run 'TestModuleLevels_Live|TestHandler_EscapesControlChars|TestContextAttrs_AndTraceIDs|TestTwoAppsIsolated|TestLevelTraceRendering|TestRotatingFile_RotatesAndCaps' -v ./log/ | grep -c '^--- PASS'   # expect 6
go test -count=1 -run TestOpContextAttrsResolvedAtLogTime ./internal/server/                 # expect ok
go test -count=1 -run 'TestGuard_NoDirectSlogCalls|TestCtxHelpers_AppendAttrsFrom' ./internal/logger/   # expect ok
git diff --exit-code origin/main -- internal/logger/slog_guard_ratchet_test.go               # expect exit 0 (ratchet unchanged)
git grep -n 'func Sanitize\|func SanitizeErr\|type OpContext\|func WithOp\|func OpFromContext' -- core/log | wc -l   # expect 0
git grep -n '^func \(Info\|Warn\|Error\|Debug\)(' -- core/log | wc -l                        # expect 0
git grep -n 'logging\.\(Sanitize\|SanitizeErr\|WithOp\|OpContext\|OpFromContext\|Info\|Warn\|Error\|Debug\)\b' -- '*.go' | wc -l   # expect 0
git grep -n 'Sanitize' -- core/health/flight.go | wc -l                                      # expect 0
go -C core list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./log | sort -u        # expect github.com/falkcorp/mediacore/log and go.opentelemetry.io/otel/trace (plus what otel/trace itself imports)
git grep -n 'slog.SetDefault\|log.SetOutput\|otel.Set' -- core/log | wc -l                   # expect 0
make -C core ci                                                                              # expect exit 0
make ci                                                                                      # expect exit 0 and "All CI checks passed"
```

## STOP conditions

- A predecessor's output is missing (Dependency check).
- The pipeline needs an import outside I8 (for example `metrics` for the counters: R1-18's `mediacore.log` scope
  bridge reads `Counters()` instead, 05-logging §9).
- `TestGuard_NoDirectSlogCalls` fails after the rewrite. Move the offending call onto the step 6 helpers; never add a
  ratchet entry or raise a ceiling. If a call cannot move, report it.
- R1-32's `Redactor` lacks a method the redact stage needs (`Attr` is the one the pipeline calls). Do not add a second
  redaction type; report it.
- A required CodeQL or log-injection check fails on the `Sanitize` removal. The spec deletes it on purpose
  (05-logging §3: the handlers quote control characters); report the alert IDs to the lead instead of re-adding it.
- A caller of a deleted name cannot be rewritten by the step 6 rules without changing what it logs. Report it.

## Out of scope

Kernel construction of `Logging`, `Deps.Log`, `app.InstallProcessDefaults` (R1-13, R1-15). The books tap, the file
output selection and the `internal/logger` shim (R1-18). Live config re-application (F3-07). The journal, its store,
importer (F5-15). The metrics bridge over `Counters()` (R1-18, which adds the
`mediacore.log` scope and its series golden, 05-logging §9). `RedactInvitePath`, `Redactor` and the default lists (R1-32). `internal/logger`'s
`SanitizeLogValue` (a different package; R1-18's shim).

## Rollback

Revert the PR: the deleted helpers and `OpContext` come back with their callers; nothing stored changes.

## Planner notes

- **`Counters`' fields** are not given by 05-logging §3; this brief coins them from §3's sentence and §9's labels.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines. Each tap is exactly one goroutine (05-logging §7).
- **No `go.work`.** Never run `go work init` in a checkout or worktree (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
