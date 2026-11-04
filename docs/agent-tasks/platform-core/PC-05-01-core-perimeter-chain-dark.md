<!-- file: docs/agent-tasks/platform-core/PC-05-01-core-perimeter-chain-dark.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f09b6d1-1b94-4481-b72b-1c3537b841df -->
<!-- last-edited: 2026-11-04 -->

# PC-05-01 — Core perimeter chain (dark): headers, CORS, gzip exclusions, tracing, stage-2 metrics into otelgin's instruments, rate classes, request size and cross-origin protection

| Field | Value |
|---|---|
| ID | PC-05-01 |
| Tier | **T3 Opus/high** — every stage must reproduce HEAD's observable behaviour once ABO switches to it, metrics included: a description or scope-version skew in stage 2 makes a second instrument and fails every scrape |
| Wave | W05 (S2, release `S2r2`), serialized step 1 |
| Depends on | OWNER-04 (the `S2r1` soak row) |
| Trailer | `Stage-Release: S2 2` |
| Spec | 05-http-api §4.2 (stages 0–11; stage 8 excluded), §3.5 (`Options`, `InstrumentScope`, `ClientAddrFrom`), §8.1 S2 row, §8.2 (wrapper marks stage 2's carrier), §9 (concurrency), §10 (keys), §11 (metrics), §12 (test names); 05-metrics §3, §6; 09 §9.2.1 row 12; 09 §9.2.7 |
| Digest | `docs/plans/platform-core/digests/S2.md` §4.4, §6 "r2", §7 |

**Gate.** Open and make green any time; merge only after OWNER-04's `S2r1` row is in `ci/soak.txt`.

**Dark.** The chain is built and tested on `examples/hello` only. ABO's gin perimeter stays in place until the r2 flip
(PC-05-02), which removes it from gin and turns the core chain on for the legacy mount in the same PR.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-05-01 -b feat/pc-05-01-core-perimeter origin/main
cd ../audiobook-organizer-pc-05-01 && git rebase origin/main && git worktree list
make web-deps
```

Never run `go work init` (CLAUDE.md).

## Exact files

| Action | Path | Stage(s) |
|---|---|---|
| create | `core/internal/httpmw/chain.go` | assembly in the fixed order, stage 1 (request ID), stage 3 (recovery, `mediacore_http_panics_total`), stage 4 (access log with `security_event`) |
| create | `core/internal/httpmw/headers.go` | stage 5 security headers, CSP per owner, `Reporting-Endpoints` |
| create | `core/internal/httpmw/buildheader.go` | stage 5 `X-App-Build` |
| create | `core/internal/httpmw/cors.go` | stage 6 |
| create | `core/internal/httpmw/crossorigin.go` | stage 7 (`net/http.CrossOriginProtection`) |
| create | `core/internal/httpmw/gzip.go` | stage 9 |
| create | `core/internal/httpmw/ratelimit.go` | stage 10 |
| create | `core/internal/httpmw/metrics.go` | stage 2 tracing span and HTTP metrics, the per-request route carrier |
| create | `core/internal/httpmw/chain_test.go` | the tests below |
| modify | `core/app/options.go` | wire `HTTPMetricsScope`, `ServiceName` (→ `httpapi.Options.ServerName`), `ServedMeterProvider`, `MetricViews` into the router (06 §6.4.5) |
| also touched (see Planner notes) | `core/internal/httpmw/peer.go` (stage 0 client address), `core/httpapi/router.go` (`Build` applies the chain; `ClientAddrFrom`), `core/httpapi/legacy.go` (wrapper marks the carrier), body limit (stage 11) |
| create | `changelog.d/<date>_pc_05_01_core_perimeter.md` (headerless) |

## Re-verify (rerun at your base; HEAD anchors at `f07f516be`)

```bash
grep -n '^func corsMiddleware\|^func securityHeadersMiddleware' internal/server/server_middleware.go    # 2 (~56, ~102)
grep -n 'Reporting-Endpoints' internal/server/server_middleware.go | wc -l                              # >= 1 (~109)
grep -n '^func contentSecurityPolicy' internal/server/csp.go                                             # 1 (~129)
grep -n 'gzip.WithExcludedPaths\|gzip.WithExcludedPathsRegexs' internal/server/server.go               # 2 (~1604-1605)
grep -n '^func NewIPRateLimiter\|^func (r \*IPRateLimiter) Start' internal/server/middleware/ratelimit.go   # 2 (~96, ~196)
grep -n 'burst := max(rpm/5, 10)' internal/server/server_lifecycle.go                                   # 1 (~1832)
grep -n '^func MaxRequestBodySize\|^func selectBodyLimit' internal/server/middleware/request_size.go   # 2
grep -n 'otelgin.Middleware("audiobook-organizer"' internal/server/server.go                            # 1 (~1673)
grep -n 'SkipPaths: \[\]string{"/api/v1/operations/events", "/api/v3/ops/events"}' internal/server/server.go   # 1 (~696)
grep -n 'credentialQueryKeys' internal/server/middleware/log_redact.go | head -1                        # >= 1 (~18)
grep -n 'otelgin v0.72.0\|otelgin v' go.mod                                                             # v0.72.0
grep -n 'go.opentelemetry.io/otel v' go.mod                                                             # v1.47.0 (semconv v1.43.0 inside otelgin)
ls core/internal/httpmw/peer.go core/httpapi/router.go core/httpapi/legacy.go core/examples/hello     # exist (W04)
```

**STOP and report** if otelgin is no longer v0.72.0 at your base (stage 2's constructors and boundaries are tied to
its semconv version, 05-http-api §4.2 stage 2 (b): "the semconv version moves with otelgin's in one PR").

## Steps (each stage reproduces the HEAD behaviour named in its "Replaces" column of §4.2)

1. **Stage 0** (`peer.go`): resolve the client address once (the client-IP header believed only from
   `TrustedProxies`, `ClientIPHeader`), publish it for `httpapi.ClientAddrFrom(ctx)`; the allow-list gate already
   exists (PC-04-03).
2. **Stage 1**: ULID request ID; keep a well-formed inbound `X-Request-ID` (`^[A-Za-z0-9._-]{1,64}$`) as
   `client_request_id`; echo `X-Request-ID`.
3. **Stage 2** (`metrics.go`), exactly 05-http-api §4.2 stage 2 (a)–(c):
   - Span: `otelhttp` with a **no-op meter provider** (spans only); span name = the route from the carrier; span URL
     attributes written after `log.Redactor` (`log.DefaultRedactQuery()`, `log.DefaultPathRules()`).
   - Metrics: create `http.server.request.duration`, `.request.body.size`, `.response.body.size` **once, at
     `Build`**, with `go.opentelemetry.io/otel/semconv/v1.43.0/httpconv` constructors, in the meter that
     `Options.HTTPMetricsScope{Name, Version, SchemaURL}` names on the served meter provider (zero value: core's
     `httpapi` scope); pass otelgin's duration boundaries explicitly (`0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25,
     0.5, 0.75, 1, 2.5, 5, 7.5, 10`). Record typed attributes as otelgin does (`http.response.status_code` and
     `server.port` as integers; method normalised), `server.address` = `Options.ServerName` (never `Host`),
     `server.port` = the port in `ServerName` if present, else the request `Host`'s port.
   - Carrier: one per-request route carrier put in the context **before** the mux; each registered route's wrapper
     writes its pattern with method and host stripped; the legacy mount's wrapper marks it immediately before calling
     gin and writes no route (edit PC-04-01's wrapper at its `// stage 2 marks the carrier here` comment); stage 2
     records nothing for a marked request; a request a core stage answers before the mount, and a router 404 with no
     route, records **no** `http_route`.
4. **Stage 3**: recovery → problem 500 `internal`, redacted stack, span marked error, `mediacore_http_panics_total`
   (under the app's namespace).
5. **Stage 4**: access log through the 05-logging redaction; skip HEAD's `SkipPaths` (`/api/v1/operations/events`,
   `/api/v3/ops/events`); stage 7 refusals logged with attribute `security_event` (no audit before S4).
6. **Stage 5**: nosniff, frame, referrer, permissions policy; HSTS only when `r.TLS != nil` or a trusted proxy says
   https; when the handler set no `Content-Security-Policy`, write the owner's policy (legacy mount:
   `LegacyMount.DefaultCSP(r)`, evaluated per response; otherwise `default-src 'none'; frame-ancestors 'self'`);
   `Reporting-Endpoints: csp="<owner's report URL>"` with every CSP; `X-App-Build` on **every** response (404, 405,
   410 included) from `LegacyMount.BuildID`, else `dev`. Port HEAD's `securityHeadersMiddleware` values exactly.
7. **Stage 6**: CORS with an explicit allowlist (`core.http.cors.allowed_origins`), same-origin always; port
   `corsMiddleware`'s semantics exactly.
8. **Stage 7**: `http.CrossOriginProtection` on unsafe methods; trusted origins = `core.http.cors.allowed_origins`;
   `X-Forwarded-Host` replaces `r.Host` for the comparison only when the TCP peer is a `TrustedProxies` member;
   refusal 403 `cross_origin_rejected`; the first refusal raises a health warning naming the key; **no credential
   type is exempt**; rebuilt (never mutated) on config change.
9. **Stage 9**: gzip skipping `HandleRaw` streams, `/metrics`, `text/event-stream` responses and every legacy route
   with `NoCompress`; the writer implements `Unwrap` and `Flush`.
10. **Stage 10**: per-client token bucket keyed on stage 0's address; class from `Route.RateClass` or
    `LegacyRoute.RateClass`; `core.http.rate_limit.enabled`, `.per_minute` (100), `.burst` (`max(per_minute/5,
    10)`, HEAD's rule), `.max_clients` (10 000); sharded maps (`runtime.GOMAXPROCS(0)` shards) with an LRU cap and one
    sweeper goroutine per `Router` stopped by the app context (05-http-api §9).
11. **Stage 11**: `MaxBody` for native routes; for legacy routes an outer cap of `core.http.body.upload_max_bytes`
    (gin keeps its per-group limits).
12. Stage 8 (Basic gate) and stages 12–13 are **not** built here (S3, S4). Hot-reloaded perimeter values are compiled
    into immutable values swapped through `atomic.Pointer` on config change (05-http-api §9).
13. `core/app/options.go`: pass the values of step 3 into `httpapi.Options` (06 §6.4.5).

## Code contract

The stage order is fixed by 05-http-api §4.2 (0 peer/client address; 1 request ID; 2 tracing and metrics; 3 recovery;
4 access log; 5 headers/CSP/`X-App-Build`/`Reporting-Endpoints`; 6 CORS; 7 cross-origin; 8 Basic gate (absent); 9
compression; 10 rate limit; 11 body limit; 12–13 absent; 14 handler). `httpapi.InstrumentScope{Name, Version,
SchemaURL string}` and `Options.ServerName`, `Options.HTTPMetricsScope` are §3.5's. No new exported core API is
needed beyond those.

## TDD tests to write first (names fixed by 05-http-api §12; on `examples/hello` through `coretest`)

- `TestChain_OrderGolden` — the recorded stage order equals §4.2's table (with stage 8 and 12–13 absent at S2).
- `TestCSRF_CookieBodilessPOSTFromSiblingOriginRefused`, `TestCrossOrigin_TrustedProxyForwardedHost`.
- `TestPerimeter_XAppBuildOnEveryResponse` (native, infra, 404, 405 on hello; the legacy half runs in PC-05-02).
- `TestCSP_DefaultPerOwner` — native default and a handler-set CSP kept (the legacy SPA half runs in PC-05-02).
- `TestRequestID_GeneratedAndPropagated` (header and slog record; the audit half is S4).
- `TestRateLimit_ClientBuckets` (`testing/synctest`; headers; the legacy `RateClass` golden half runs in PC-05-02).
- `TestTrace_NoApikeyInSpanAttributes`, `TestAccessLog_NoInviteToken`.
- `TestStage2_RouteSurvivesRequestClone`, `TestStage2_ServerAddressIgnoresHost`.
- `TestClientIP_ZonedConnectorTrusted`, `TestUnmatched_APIPathIsProblemNotHTML`, `TestUnclean_PathNotRedirectedByMux`.
- A unit test that stage 2's three instruments match otelgin's name, unit, description, kind, number type, scope
  name/version/schema URL and bucket boundaries when built with otelgin's constants (name coined here:
  `TestStage2_InstrumentsIdenticalToOtelgin`; core may import otelgin **only in this test** — if the core module's
  import rules forbid even that, compare against constants copied from otelgin v0.72.0's `internal/semconv/server.go`
  with a comment naming the file).

## Acceptance

```bash
(cd core && go build ./... && go vet ./internal/httpmw/ ./httpapi/ ./app/)                # exit 0
(cd core && go test ./internal/httpmw/ ./httpapi/ ./app/ ./examples/hello/... -count=1)   # ok each
(cd core && go test ./internal/httpmw/ -run 'TestChain_OrderGolden|TestCSRF_|TestCrossOrigin_|TestPerimeter_XAppBuild|TestCSP_|TestRequestID_|TestRateLimit_|TestTrace_|TestAccessLog_|TestStage2_|TestClientIP_|TestUnmatched_|TestUnclean_' -count=1 -v 2>&1 | grep -c '^--- PASS')
#   expect: >= 15
(cd core && go test -race ./internal/httpmw/ -count=1)                                    # ok
git diff --stat origin/main -- internal/ cmd/ main.go                                     # no output (ABO untouched)
make -C core ci                                                                           # exit 0
make ci                                                                                   # exit 0
```

## Invariants and STOP conditions

- No per-request goroutine in the chain (05-http-api §9). The rate limiter's sweeper is the only background goroutine.
- `server.address` never comes from `Host`. A label value a client can mint → STOP.
- Stage 2 records into the provider whose registry `/metrics` serves at that release (r2: `ServedMeterProvider`, the
  S1 façade's global), never into an unserved one.
- Core imports nothing of gin or otelgin outside the one comparison test.
- CLAUDE.md concurrency: no library-scale loop; the rate-limiter shards are the concurrency design §9 fixes.

## Out of scope

Removing the perimeter from gin, the books mount's `DefaultCSP`/`BuildID`/`RateClass`/`NoCompress` values, the
legacy halves of the perimeter tests, `TestCompressionExclusionsEqualHEAD`, `TestPerimeterHonoursBlobOnlyValues`,
the series golden (all PC-05-02); the Basic gate (S3); authentication (S4).

## Rollback

Revert the PR; nothing in ABO uses the chain yet.

## Repo rules

Worktree per task off fresh `origin/main`; 4-line headers on every new or changed file; headerless changelog
fragment; conventional commit `feat(httpapi): core perimeter chain stages 0-11 (dark)` ending with
`Stage-Release: S2 2` and `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never
squash; never `--admin` past a required check.

## Planner notes

- The row's files omit the places the chain must also touch: `core/internal/httpmw/peer.go` (stage 0's client
  address), `core/httpapi/router.go` (`Build` applying the chain, `ClientAddrFrom`), `core/httpapi/legacy.go` (the
  wrapper marks stage 2's carrier), and a file for stage 11 (body limit); stages 1, 3 and 4 have no file of their own
  in the row, so this brief puts them in `chain.go`.
