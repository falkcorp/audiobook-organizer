<!-- file: docs/agent-tasks/platform-core/R1-32-log-redactor-and-span-redaction.md -->
<!-- version: 1.0.5 -->
<!-- guid: 7dce556b-315e-476c-899c-10d184f05c10 -->
<!-- last-edited: 2026-11-05 -->

# R1-32 — log.Redactor in core/log, the one redaction type of 05-logging §3, and metrics.Options.Redactor with TestTrace_NoApikeyInSpanAttributes

| Field | Value |
|---|---|
| ID | `R1-32` |
| Tier | **T3** (Opus / high): the one redactor every log line, span and perimeter stage goes through; a gap leaks a credential |
| Release / wave | R1 / W09 |
| Depends on | R1-07, R1-10 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-logging §3 (Redactor, NewRedactor and its defaults), §6 (fail-open on a panicking value), §10 (redaction tests); 05-metrics §3 (Options.Redactor, span redaction), §8 failure table ("Redactor nil while tracing is on"), §11, §12; 05-http-api §4.2 stages 2 and 4 |
| Branch | `agent/platform-core-r1-32-log-redactor-and-span-redaction` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the
named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1
(09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (touch nothing outside this list without recording it under the PR's
"Scope notes"):

- `core/log/redactor.go` (create)
- `core/log/redactor_test.go` (create)
- `core/metrics/options.go` (modify: R1-10 declared `Options` there)
- `core/metrics/trace_redact.go` (create)
- `core/metrics/trace_redact_test.go` (create)
- `internal/telemetry/telemetry.go` (modify)

Two of those files are not obvious from the title: the redacting span exporter's own source file
`core/metrics/trace_redact.go`, and ABO's façade `internal/telemetry/telemetry.go`, which must pass a
`Redactor` once a nil one with tracing on fails at boot.

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-32" -b agent/platform-core-r1-32-log-redactor-and-span-redaction origin/main
cd "$REPO/.worktrees/platform-core-r1-32"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...`
and `go -C core build ./...`. Line numbers below were read at the planning base `ac4368bdb` (no code changed between
it and HEAD); the spec's anchors are at `748fd787a`. Re-run every grep at your base before editing.

## Goal

Create the one redaction type of core, `*log.Redactor` (05-logging §3, decision L4: "redaction is in the pipeline,
once, before fan-out … the same `*log.Redactor` redacts trace span attributes"), and give `metrics.Options` its
`Redactor` field with the redacting span exporter of 05-metrics §3 ("Span redaction (normative, SEC-22)").

Later rows use this type and add no second one: R1-17's perimeter stages 2 (tracing) and 4 (access log) redact
through it (05-http-api §4.2); R1-18's log work and every later redaction keep it; the kernel builds it from the
defaults plus `core.log.redact_keys` and module path rules once core's `Logging` exists (05-logging §4). This row is
**core only** apart from the one façade line below; ABO's own middlewares (`log_redact.go`, `trace_redact.go`) stay
until R1-17 moves them.

## Code contract (copy exactly; 05-logging §3, 05-metrics §3)

```go
package log // core/log/redactor.go

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

func DefaultPathRules() []PathRule  // [invite]
func DefaultRedactKeys() []string
func DefaultRedactQuery() []string
func URLKeys() []string
```

```go
package metrics // core/metrics/options.go: add one field to R1-10's Options
	Redactor *log.Redactor // from Logging.Redactor(); applied to span names and URL attributes before export
```

The four default lists are **functions returning a fresh copy on every call**, never exported package-level slices
(05-logging §3; R5; `TestNoGlobals` forbids exported mutable package state). Their contents, verbatim from 05-logging
§3:

| List | Contents |
|---|---|
| `DefaultRedactKeys()` (matched case-insensitively after stripping `-`, `_`, `.`) | `password`, `passwd`, `secret`, `token`, `apikey`, `authorization`, `cookie`, `setcookie`, `privatekey`, `clientsecret`, `credential`, `credentials`, `accesstoken`, `refreshtoken`, `sessionid` |
| `DefaultRedactQuery()` (matched case-insensitively after one round of percent-decoding of the name) | `token`, `access_token`, `refresh_token`, `api_key`, `apikey`, `key`, `password`, `secret`, `code`, `code_verifier`, `sig`, `signature` |
| `URLKeys()` | `path`, `url`, `uri`, `request_uri`, `referer`, `url.full`, `url.path`, `url.query`, `http.target`, `http.url` |
| `DefaultPathRules()` | one rule, `invite`: ABO's `RedactInvitePath` algorithm (any `invite`/`invites` segment followed by another segment, at any position, after percent-decoding and dot-walk checks) |

`DefaultRedactQuery()` is a superset of HEAD's `credentialQueryKeys` (`internal/server/middleware/log_redact.go:26-37`).

## Invariants and the tests that prove them (write first, red)

In `core/log/redactor_test.go`, the 05-logging §10 rows this type owns (spec names copied exactly):

- `TestDefaultLists_FreshCopies`: editing or appending to the slice one call of `DefaultPathRules()`,
  `DefaultRedactKeys()`, `DefaultRedactQuery()` or `URLKeys()` returns leaves the next call's result and every
  `Redactor` unchanged.
- `TestRedact_DefaultKeys`, `TestRedact_NestedGroups`, `TestRedact_URLUserinfoAndQuery`, `TestRedact_SecretsValue`:
  values replaced. `core/secrets` does not exist in R1, so `TestRedact_SecretsValue` asserts the `slog.LogValuer`
  path with a test type whose `LogValue` returns `[REDACTED]` (the `secrets.Value` cell is added when F3 lands
  `secrets`; say so in a comment).
- `TestRedact_CoreInvitePath`: `/api/core/v1/auth/invites/<token>` and HEAD's path-form `/invite/<token>` under
  `path`, `url.path`, inside a full URL and percent-encoded → token absent (the text and JSON halves here; the journal
  and realtime-tail halves are added by the rows that land them).
- `TestRedactQuery_SupersetOfHEAD`: every key of HEAD's `credentialQueryKeys` (a fixture copy in the test) is in
  `DefaultRedactQuery()`; `?code=…&code_verifier=…&refresh_token=…&SECRET=…` → all values redacted.
- `TestRedact_TempLoginTokenQuery`: `/api/core/v1/auth/temp-login?token=<t>` → `token=[REDACTED]`.
- `TestRedact_PathRuleFailsClosed`: over-long, over-escaped and dot-walked invite paths → `/[REDACTED]` (port ABO's
  fixtures from `core/log/redact_path_test.go`, which R1-07 moved there).
- `TestRedactor_SpanAttributes`: `Redactor.String` on `url.full`, `url.query`, `http.target` with `apikey=` /
  `token=` → redacted.
- A value whose `LogValue` panics is replaced with `"[REDACTED:panic]"` and never crashes (05-logging §6 failure
  table; name coined here: `TestRedact_PanickingValuerFailsOpen`).

In `core/metrics/trace_redact_test.go` (05-metrics §12):

- `TestTrace_NoApikeyInSpanAttributes` (this row owns it): an in-memory span exporter behind the redacting wrapper;
  spans for a request `?apikey=…&token=…` and for `/api/core/v1/auth/invites/<t>` (name, `url.full`, `url.path`,
  `url.query`, `http.target`) → no secret in the span name or any attribute.
- `New` with `Tracing != nil` and `Redactor == nil` fails (05-metrics §8: "Redactor nil while tracing is on |
  fail-closed (boot)"); with `Tracing == nil` and `Redactor == nil` it succeeds (no exporter, nothing to redact).
  Name coined here: `TestNew_TracingWithoutRedactorFails`.

The redactor is immutable after `NewRedactor`; concurrent use from many goroutines is race-free (run the tests under
`-race`). No exported package-level variable of slice, map, func or pointer type in `core/log`.

## Dependency check

R1-07 and R1-10 merged:
`test -f core/log/redact_path.go && grep -q 'type Options struct' core/metrics/options.go && echo ok` prints `ok`.
If `type Options struct` is in another file of `core/metrics`, add the field there, record the file under Scope notes,
and continue. If `core/log` or `core/metrics` is missing, STOP.

## Re-verify (expected at your base, after R1-07 and R1-10)

```bash
grep -n '^func RedactInvitePath' core/log/redact_path.go          # expect 1 (R1-07 moved it; the invite rule reuses it)
git grep -n 'type Redactor struct' -- core                         # expect 0 (this row creates it)
grep -n 'Redactor' core/metrics/options.go                         # expect only R1-10's doc comment, no field
git grep -n 'metrics.New(' -- internal core | grep -v _test.go     # expect internal/telemetry/telemetry.go (the façade) and nothing else
grep -n 'credentialQueryKeys = map' internal/server/middleware/log_redact.go   # expect 1 (the fixture source for the superset test)
```

## Steps

1. Write the tests above (red).
2. `core/log/redactor.go`: `Redactor`, `NewRedactor`, `PathRule`, the four list functions, and the `Attr`, `URL`,
   `Path`, `String` methods. The invite rule's `Fn` calls the moved `RedactInvitePath`; do not copy the algorithm.
   `Attr` resolves `slog.LogValuer` (recovering a panic to `"[REDACTED:panic]"`), replaces deny-listed keys with
   `"[REDACTED]"`, recurses into groups, and gives values under `URLKeys()`, `*url.URL` values and URL-shaped strings
   the `URL` treatment (05-logging §3 "Pipeline").
3. `core/metrics/options.go`: add `Redactor *log.Redactor` with the spec's comment; remove R1-10's "added later"
   comment. `core/metrics` may import `core/log` (same layer L0, 05-metrics §3; `log` never imports `metrics`).
4. `core/metrics/trace_redact.go`: the redacting span exporter of 05-metrics §3. Before export, each
   span's name and each attribute under `log.URLKeys()` go through `Options.Redactor.String`; any attribute whose key
   is on the redactor's key deny-list becomes `[REDACTED]`. `New` wraps **every** span exporter it builds, inside the
   Provider, so no caller can register an unwrapped one. `New` refuses `Tracing != nil && Redactor == nil`. The
   counter `<namespace>_metrics_span_redactions_total{kind}` (`kind` ∈ key, url, path; 05-metrics §11) is recorded
   by the wrapper on core's metrics scope.
5. ABO façade: in `internal/telemetry/telemetry.go`, where it builds `metrics.Options`, pass
   `Redactor: logging.NewRedactor(nil)` (the `logging` import alias of `mediacore/log`, R1-07's rule), so tracing
   keeps working. No other ABO change; HEAD's span and log middlewares stay until R1-17.
6. If an existing `core/metrics` test builds `Options` with `Tracing` set and no `Redactor`, it now fails at `New`:
   give it `log.NewRedactor(nil)` and record the file under Scope notes. Change no assertion.
7. Header bumps; fragment `changelog.d/<today>_core_log_redactor.md` (`### Added`: "Internal: one credential redactor
   for logs and traces in the core module."). Commit: `feat(core): log.Redactor and span redaction in metrics`.

## STOP conditions

- A default list would have to differ from 05-logging §3's text (a missing or extra key): stop and report; never
  shrink a list.
- Any path lets a span leave the Provider without the wrapper.
- `core/log` would need to import `core/metrics` (R4 forbids it).

## Acceptance

```bash
go -C core test ./log/ ./metrics/ -race -count=1                                              # expect ok
go -C core test ./log/ -run 'TestDefaultLists_FreshCopies|TestRedact_|TestRedactQuery_SupersetOfHEAD|TestRedactor_SpanAttributes' -count=1 -v | grep -c '^--- FAIL'   # expect 0
go -C core test ./metrics/ -run 'TestTrace_NoApikeyInSpanAttributes|TestNew_TracingWithoutRedactorFails' -count=1 -v | grep -c '^--- PASS'   # expect 2
git grep -n 'type Redactor struct' -- core | wc -l                                            # expect 1
git grep -nE '^var [A-Z][A-Za-z]* *(=|\[\]|map|func|\*)' -- core/log ':!*_test.go' | wc -l      # expect 0 (no exported mutable package state)
go test ./internal/telemetry/... ./internal/telemetrypolicy/ -count=1                         # expect ok (series unchanged)
make ci && make -C core ci                                                                    # expect exit 0
```

## Out of scope

Core's `Logging` pipeline, taps, levels and file output (05-logging §3 beyond the redactor; R1-36 builds them, on top of this row's `Redactor`; R1-18 only consumes them);
`core.log.redact_keys` (core config arrives at F3); moving ABO's `log_redact.go`/`trace_redact.go` into the perimeter
(R1-17); the journal (F5).

## Rollback

Revert the PR; nothing imports the redactor yet except the façade line and the metrics wrapper. No stored data
changes.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines. The redactor holds no lock and no goroutine.
- **No `go.work`.** Never run `go work init` in a checkout or worktree (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
