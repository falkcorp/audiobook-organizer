<!-- file: docs/agent-tasks/platform-core/R1-17-perimeter-into-core-stages.md -->
<!-- version: 1.1.0 -->
<!-- guid: 8b504d86-5e95-4fb7-a1c8-ff2de68cf3a7 -->
<!-- last-edited: 2026-11-05 -->

# R1-17 — The perimeter moves into core stages 0-11 and leaves gin in the same PR (except the Basic gate), otelgin removed, X-App-Build at stage 5, the HTTP metric changes with their dashboards and rules

| Field | Value |
|---|---|
| ID | `R1-17` |
| Tier | **T4** (Opus / xhigh): every request's path (CSP, rate classes, cross-origin, compression) changes owner in one PR |
| Release / wave | R1 / W16 |
| Depends on | R1-15, R1-16, R1-12, R1-32 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-http-api §4.2 (stages 0-11), §8.1 R1 row, §12; 05-metrics §13 item 3 (http_server_* row); 09 §9.2.7; 09 §9.4 R1 Content: runtime, release note (HTTP-metrics lines) |
| Branch | `agent/platform-core-r1-17-perimeter-into-core-stages` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/httpapi/perimeter/`
- `core/internal/httpmw/`
- `internal/server/middleware/`: `ratelimit.go`, `request_size.go`, `log_redact.go`, `recovery_redact.go`, `trace_redact.go`
- `internal/server/`: `server.go`, `server_middleware.go`, `csp.go`, `client_trust.go`, `server_lifecycle.go`
- `internal/httputil/`
- `deploy/grafana/dashboards/`
- `deploy/prometheus/`
- `go.mod`
- `go.sum`
- `core/`: `go.mod`, `go.sum`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-17" -b agent/platform-core-r1-17-perimeter-into-core-stages origin/main
cd "$REPO/.worktrees/platform-core-r1-17"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Move ABO's HTTP perimeter from gin into core's fixed middleware chain, stages 0–11, and remove it from gin **in the
same PR**, except the Basic gate (stage 8), which stays in gin until F3 (05-http-api §4.2, §8.1 R1 row; 09 §9.4 R1
"Content: runtime"; 04 §8.1 R1). Every request's path changes owner in one PR: peer allowlist and client address
(0), request ID (1), tracing and HTTP metrics (2: stage 2 records every request; `otelgin` leaves the engine and
`go.mod`, 13 §13.5.1), recovery (3), access log (4), security headers with `DefaultCSP` = today's per-response
`contentSecurityPolicy()` and `Reporting-Endpoints`, plus `X-App-Build` on every response (5), CORS (6),
cross-origin protection (7, new; refusals go to the access log as `security_event` until `audit` exists),
compression with HEAD's per-path exclusions (9), the `/api` rate limiter with per-route `RateClass` (10), body
limits (11). Stages 12–13 (authenticate/authorize) do not run for the legacy mount until F4.

Perimeter values reach the kernel through `app.Options.R1Values` (05-config §1.12.1; R1-15 wired the call): listen
address and TLS files, `core.http.rate_limit.*` from `enable_rate_limit`/`api_rate_limit_per_minute`, body limits,
peer allow-list, trusted proxies. A value saved only in the UI reaches stages 0, 10 and 11; an edit through
`PUT /api/v1/config` takes effect at the next restart until F3 (an R1 release-note line).

**HTTP metrics change in this PR, with their dashboards and rules** (05-metrics §13 item 3, the `http_server_*`
row; 09 §9.2.7; XFILE-1): core's stage 2 emits the semconv `http_server_*` families with mux patterns as
`http_route` (gin templates → mux patterns, a value change named in R1's release note). Every dashboard panel and
rule under `deploy/grafana/dashboards/` and `deploy/prometheus/` that references a changed family is edited here,
and `TestDeployReferencedSeriesServed` (R1-10) proves it.

Kept unchanged (row notes): the ABS conformance suite, the bootstrap, invite and ABS limiter tests (they use
`client_trust.go`'s `trustedClientIP`, which stays in ABO until F4, 05-http-api §8.1 R1 "Client address"). The 36
permission-less routes carry `AnyAuthenticated` in the manifest (R1-14 recorded them).

## Tests this task owns (spec names; 05-http-api §12; 09 §9.4 R1 Verification "the perimeter's own tests")

- `TestStage2_RecordsEveryRequest`
- `TestPerimeterHonoursBlobOnlyValues` (a perimeter value saved only in the config blob takes effect at boot)
- `TestCSP_DefaultPerOwner` (per-owner CSP header goldens)
- `TestCompressionExclusionsEqualHEAD` (HEAD's exclusions: `/api/events`, `/metrics`, `/api/v1/auth/accept-invite`
  and `^/api/v1/operations/v2/[^/]+/logs/download$`, `internal/server/server.go:1603-1605` at `f7ac5b11e`, plus every
  `text/event-stream` response and `HandleRaw` streams)
- `TestPerimeter_XAppBuildOnEveryResponse`
- also, for the stages moved: `TestChain_OrderGolden`, `TestRequestID_GeneratedAndPropagated`,
  `TestRateLimit_ClientBuckets`, `TestCSRF_CookieBodilessPOSTFromSiblingOriginRefused`,
  `TestCrossOrigin_TrustedProxyForwardedHost`, `TestClientIP_ZonedConnectorTrusted`, `TestAccessLog_NoInviteToken`,
  `TestHandler_NoDoubleGzip`, `TestStage2_ServerAddressIgnoresHost` (both 05-metrics §12 cells; this row owns them:
  one gzip layer through the httpapi stack under `Accept-Encoding: gzip`, and two requests to one route with
  different `Host` headers give one `server_address` value, `Options.ServiceName`).
- Not this row's: `TestTrace_NoApikeyInSpanAttributes` is R1-32's (it lands with `log.Redactor`); keep it green
  once stage 2 records the spans.
- Unchanged and green: the ABS conformance suite; `TestDeployReferencedSeriesServed` and `TestPromRules_Promtool`.

## Dependency check

R1-15, R1-16, R1-12, R1-32 merged: `test -d internal/books && test -d core/internal/sdnotify && test -d core/rbac && git grep -q 'type Redactor struct' -- core/log && echo ok`.
Stages 2 and 4 redact through R1-32's `log.Redactor` (05-logging §3: `NewRedactor`, `URL`, `String`; the query list
`log.DefaultRedactQuery()` and path rules `log.DefaultPathRules()`); the stages hold the one `*log.Redactor` the
kernel builds and restate no list (05-http-api §4.2 stage 2). If `type Redactor struct` is not in `core/log`, STOP:
R1-32 has not merged.

## Re-verify (expected at `f7ac5b11e`)

```bash
grep -n 'otelgin' go.mod internal/server/server.go | head                         # expect go.mod:31 and server.go:59,727,1668-1673
grep -n 'router.Use(' internal/server/server.go | head -20                        # the gin perimeter installs to remove (recovery :704, headers :705, cors :706, ...)
sed -n 1603,1605p internal/server/server.go                                       # expect the gzip exclusions above
ls internal/server/middleware/ratelimit.go internal/server/middleware/request_size.go internal/server/middleware/log_redact.go internal/server/middleware/recovery_redact.go internal/server/middleware/trace_redact.go internal/server/csp.go internal/server/client_trust.go   # expect all exist
grep -rln 'http_server_\|http_route' deploy/grafana/dashboards deploy/prometheus | wc -l   # record: every hit is edited in this PR if its family changes
```

## Steps (outline; the stage implementation is yours within 05-http-api §4.2)

1. Tests first (red), including per-owner CSP goldens and the compression-exclusion set read from HEAD.
2. `core/httpapi/perimeter/` (the stage chain) and `core/internal/httpmw/` (the stage implementations), from HEAD's
   middleware files as listed in 05-http-api §4.2's "today" column; `internal/httputil` → `mediacore/httpapi`
   helpers where 04 §8.2 says so (its envelope types; `internal/httputil` keeps `respond.go` and becomes layer 5,
   03 §3.5.1 item 1 "Consequence in R1").
3. Remove each moved middleware from the gin engine in the same commit; delete `otelgin` from the engine and from
   `go.mod`/`go.sum` (tidy both modules).
4. Edit dashboards and rules for the changed HTTP families; update `alert-rules.test.yml` so `promtool test rules`
   passes.
5. Header bumps; fragment `changelog.d/<today>_perimeter_core.md` with the R1 release-note lines this PR owns:
   "Every response carries an `X-App-Build` header naming the build." and the HTTP-metrics lines of 09 §9.2.7 (the
   `http_route` value change among them), and "A setting saved through the API for the listener, rate limit, body
   limits, peer allow-list or trusted proxies takes effect at the next restart." Commit(s) per stage group; every
   commit builds.

## STOP conditions

- An ABS conformance test, or the bootstrap, invite or ABS limiter tests, would need an expectation change.
- A gin middleware cannot be removed without the Basic gate moving (it stays in gin until F3).
- A dashboard or rule references a family whose new name you cannot derive from 05-metrics §13 item 3.

## Code contract

The stage chain and `httpapi` perimeter options exactly as 05-http-api §4.2 and §3 name them; no new exported name outside them.

## Acceptance

```bash
go -C core test ./httpapi/... ./internal/httpmw/... -race -count=1          # expect ok
go test ./internal/server/... -short -count=1                                # expect ok (ABS conformance included)
git grep -n 'otelgin' -- go.mod internal | wc -l                              # expect 0
go test ./internal/telemetrypolicy/ -run TestDeployReferencedSeriesServed -count=1 && go test ./internal/opsmetrics/ -run TestPromRules_Promtool -count=1   # expect ok ok
make test-e2e                                                                 # expect green
make ci && make -C core ci                                                    # expect exit 0
```

## Out of scope

The Basic gate (F3); stages 12–13 for the legacy mount (F4); `client_trust.go`'s move (F4); `/metrics` serving
(F8).

## Rollback

Revert the PR: the perimeter returns to gin with HEAD's metric names, and the dashboard edits revert with it.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Move window (09 §9.2.10).** This task moves or re-homes packages. Before you start, the lead
  announces a move window in the tracking issue at least one working day ahead, naming the 04 rows it
  freezes and its end (at most two working days). If no announcement exists, STOP and ask the lead. The PR
  description lists every `old ==> new` package path, one per line. Every commit of the PR builds:
  `go build ./... && go -C core build ./...` at each commit (`scripts/ci/check_commits_build.py` checks it
  in `extraction-gates`); keep the rename and the import rewrite in one commit, or make each commit build.
  Apply in the same PR the `go fix` modernizers core's `fix-check` requires in the moved files
  (`go -C core fix ./...`, 04 §2.2).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.

