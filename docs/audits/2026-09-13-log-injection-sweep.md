<!-- file: docs/audits/2026-09-13-log-injection-sweep.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e2c91d4-3b6a-4f08-9a5e-d14b8c7f2e60 -->
<!-- last-edited: 2026-09-13 -->

# go/log-injection sweep (2026-09-13)

Owner decision, 2026-09-13: sweep every open `go/log-injection` CodeQL alert on
`main` and add a CI guard so the class stops growing.

## Baseline

Measured on `main` at `5601696a2`: **307** open `go/log-injection` alerts (the
request said 306; one more landed before the sweep started).

```bash
gh api --paginate "repos/falkcorp/audiobook-organizer/code-scanning/alerts?ref=refs/heads/main&state=open&per_page=100" \
  --jq '[.[] | select(.rule.id=="go/log-injection")] | length'
```

Alert columns are **byte** offsets, not character offsets. On a line with a
multi-byte character (an em dash in the message) a character-indexed script
lands on the wrong argument.

## Sink kinds

| Kind of sink | Alerts |
|---|---:|
| `slog.Info/Warn/Error/Debug` package function, tainted attribute value | 275 |
| `slog` package function, tainted message (message string built from input) | 5 |
| `log/slog` imported under an alias (`stdlog.Warn`), tainted attribute value | 2 |
| Method on a `*slog.Logger` value (`r.logger.Warn(...)`), tainted attribute value | 25 |
| `fmt`/`log.Printf` | 0 |
| **Total** | **307** |

Every alert is on a direct `log/slog` call. None is on `logger.Logger`,
`OperationLogger` or the std-logger bridge, which run each line through
`sanitizeLogLine`. The 25 method-call alerts are all in
`internal/operations/registry`, whose `Registry` holds a plain `*slog.Logger`.

## Package plan

PR A is this PR. PR B and PR C are the rest of the free packages. PR D covers
packages that other open work is editing right now; it waits until that work
merges.

| Package | Alerts | PR |
|---|---:|---|
| `internal/metafetch` | 69 | A |
| `internal/operations/registry` | 25 | A |
| `internal/server/handlers/abs` | 16 | A |
| `internal/fileops` | 12 | A |
| `internal/server` (none are in `server_maintenance_deps.go`) | 25 | B |
| `internal/server/handlers` | 21 | B |
| `internal/server/handlers/audiobooks` | 17 | B |
| `internal/server/handlers/metadata` | 5 | B |
| `internal/server/handlers/dedup` | 4 | B |
| `internal/server/absauth` | 3 | B |
| `internal/server/middleware` | 2 | B |
| `internal/server/handlers/operations` | 2 | B |
| `internal/server/handlers/duplicates` | 2 | B |
| `internal/database` | 21 | C |
| `internal/metadata` | 19 | C |
| `internal/audiobooks` | 16 | C |
| `internal/httputil` | 5 | C |
| `internal/plugins/itunes` | 4 | C |
| `internal/tagger` | 4 | C |
| `internal/logging` | 3 | C |
| `internal/organizer` | 3 | C |
| `internal/itunes/service` | 3 | C |
| `internal/activity` | 3 | C |
| `internal/scheduler` | 2 | C |
| `internal/importer`, `internal/tools`, `internal/realtime`, `internal/deluge` | 1 each (4) | C |
| `internal/merge` | 14 | D, later (combine soft-delete PR) |
| `internal/dedup` | 3 | D, later (combine soft-delete PR) |

Totals: A 122, B 81, C 87, D 17, which is 307.

Other packages on the hold list have **no** open alerts today:
`internal/scanner` and `internal/ai` (AI split PR #3361);
`internal/plugins/maintenance/cleanup.go`, `compact_activity_log.go` and
`internal/server/server_maintenance_deps.go` (nightly compaction PR); and
`internal/config`, `internal/aidispatch` and
`internal/server/handlers/aibackends` (capability PR #3362). PR D rechecks
them once that work merges, because those PRs can add new sinks.

## How PR A fixed them

Each tainted value is wrapped with `logger.SanitizeLogValue` at the sink.
Message wording and keys are unchanged.

- Errors become `logger.SanitizeLogValue(err.Error())`. Every such site is
  inside an `err != nil` branch. The two exceptions are in
  `internal/metafetch/service_search.go`: the `err != nil || result == nil`
  branch and its `else`, where `err` can be nil. Those use `fmt.Sprint(err)`,
  which prints `<nil>` the way slog already did.
- `internal/metafetch/service_apply.go` logs two `int` durations that CodeQL
  counts as tainted. They are logged as `strconv.Itoa` strings now. The
  TextHandler output is byte-identical; a JSON handler would quote them.
- `internal/server/handlers/abs/stream.go`'s `fileNotFound` builds an `args`
  slice. The request-derived values are wrapped where the slice is built, and
  the paths and errors that callers pass are wrapped at the call sites.

CodeQL credits `SanitizeLogValue` as a barrier only because its
`strings.ReplaceAll` of `\r` and `\n` runs on every path. See the doc comment
in `internal/logger/sanitize.go` before you touch it.

## The CI guard

`internal/logger/slog_guard_test.go` (`TestGuard_NoDirectSlogCalls`) parses
every non-test `.go` file under `internal/` and `cmd/`. It fails on a direct
call to a `log/slog` package function (`Debug`, `Info`, `Warn`, `Error`, their
`*Context` forms, `Log`, `LogAttrs`) or to `slog.Default()`. It resolves import
aliases, so `stdlog.Warn` counts. `internal/logger` is exempt.

There are two gates, and they split the work on purpose:

- **The guard is a class gate.** `slog_guard_ratchet_test.go` records each file
  that called slog directly on 2026-09-13 and its call count: **328 files,
  1,974 calls**. A file that is not on the list fails. So does a listed file
  that goes above its count. Both ceilings may only go down. A file leaves the
  list only when it has no direct calls left.
- **CodeQL is the per-alert gate.** Wrapping a value closes its alert but
  leaves the `slog` call in place. So the files fixed in PR A stay on the
  ratchet at their old counts, and the ratchet does not shrink in this PR.
  Moving a package's calls onto `logger.Logger` is what shrinks it.

### Known gap: `*slog.Logger` methods

The guard does not match method calls on a `*slog.Logger` value, such as
`r.logger.Warn(...)` in the registry. It runs without `go/types`, so a
selector like `x.logger.Info` looks the same as a call on the sanitizing
`logger.Logger`. A name-based rule would flag every sanitized call site in the
repo. Tainted values do reach these sinks: all 25 registry alerts are of this
kind. PR A wraps them, and CodeQL remains the only gate for this shape. To
close the gap, the guard would need type information
(`golang.org/x/tools/go/packages`), which costs a full type-check of the module
on every run. Another option is to replace the registry's `*slog.Logger` with
a sanitizing handler type.

## Verification

- The guard fails on a throwaway `internal/zzprobe/probe.go` that calls
  `slog.Info` directly, and passes once that file is deleted.
  `TestGuard_SlogRuleMatches` checks the rule against synthetic source: a plain
  call, an aliased import, `slog.Default()`, a `*Context` form, and a clean
  file.
- The crediting proof is the PR's CodeQL run. Compare the open count on
  `refs/pull/<N>/merge` with `main`, across all rules. The numbers are in the
  PR description.
