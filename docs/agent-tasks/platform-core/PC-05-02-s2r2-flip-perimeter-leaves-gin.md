<!-- file: docs/agent-tasks/platform-core/PC-05-02-s2r2-flip-perimeter-leaves-gin.md -->
<!-- version: 1.0.0 -->
<!-- guid: d01e8447-06b2-4240-b7f0-dbb38fbf01aa -->
<!-- last-edited: 2026-11-04 -->

# PC-05-02 — S2 r2 flip: the perimeter leaves gin; the books mount supplies its CSP, rate classes and compression flags; pre-mount refusals recorded with no http_route

| Field | Value |
|---|---|
| ID | PC-05-02 |
| Tier | **T3 Opus/high** — an observable change on every request (headers, compression, limits, cross-origin refusals, where refused requests are counted); the metric path needs an Opus review because a second recorder or a skewed instrument fails every scrape |
| Wave | W05 (S2, release `S2r2`), serialized step 2 — the flip, last PR of W05 |
| Depends on | PC-05-01 |
| Trailer | `Stage-Release: S2 flip` |
| Required checks | 09 §9.2.1's table plus `compat-rehearsal` (cell c) and `unit-lifecycle` |
| Spec | 09 §9.4 S2 rows HTTP and Done when; 09 §9.2.1 row 12 (cost and release note); 05-http-api §4.2 stage 2 (a)–(c), §8.1 S2 row, §8.2, §8.4, §12; 05-metrics §6 (seeding through the real chain at S2 r2; golden-diff table); 05-config §1.12.2 ("Edits made outside core config") |
| Digest | `docs/plans/platform-core/digests/S2.md` §6 "r2", §7, §8 |

**Gate.** Open and make green any time; merge only after OWNER-04's `S2r1` row is current and PC-05-01 has merged.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-05-02 -b feat/pc-05-02-s2r2-flip origin/main
cd ../audiobook-organizer-pc-05-02 && git rebase origin/main && git worktree list
make web-deps
```

Never run `go work init` (CLAUDE.md).

## Exact files

| Action | Path |
|---|---|
| modify | `internal/server/server.go` (gin global middleware: remove the perimeter) |
| modify | `internal/server/server_middleware.go` (delete `corsMiddleware`, `securityHeadersMiddleware` once nothing calls them) |
| modify | `internal/server/middleware/ratelimit.go` (the `/api` use moves to stage 10; the type stays for the bootstrap and invite limiters) |
| modify | `internal/server/middleware/request_size.go` (only the outer cap moves; gin keeps its per-group limits, 05-http-api §4.2 stage 11) |
| modify | `internal/server/legacy_manifest.go` (`DefaultCSP`, `CSPReport`, `BuildID`, `BasicExemptAssets`; manifest `RateClass`/`NoCompress` already there) |
| modify | `internal/server/client_trust.go` (`trustedClientIP` starts from `httpapi.ClientAddrFrom`) |
| modify | the series contract test's seeding (under `internal/telemetrypolicy/`, see Planner notes); `series.golden` must show an empty diff |
| modify | `ci/stage.txt` |
| also touched (see Planner notes) | `internal/server/server_lifecycle.go` (the `/api/v1` group's `apiRateLimiter`/`bodyLimitMiddleware` use), `internal/books/module.go` (mount fields) |
| create | `changelog.d/<date>_pc_05_02_s2r2_flip.md` (headerless; release note below) |

## Re-verify (counts at `f07f516be`; rerun at your base)

```bash
grep -n 'router.Use(' internal/server/server.go | wc -l                                  # 9 at f07f516be: 8 calls (~695-731) + 1 comment (~1670)
grep -n 'router.Use(gin.LoggerWithConfig\|router.Use(servermiddleware.RedactingRecovery())\|router.Use(securityHeadersMiddleware())\|router.Use(corsMiddleware())\|router.Use(servermiddleware.BasicAuth())\|router.Use(compressionMiddleware())\|router.Use(tracingMiddleware()...)' internal/server/server.go   # 8: the 7 perimeter calls (~695, 704-707, 725, 731) + the comment at ~1670
grep -n 'api.Use(apiRateLimiter, bodyLimitMiddleware' internal/server/server_lifecycle.go | wc -l   # 2 (~1869, ~1871)
grep -n '^func trustedClientIP' internal/server/client_trust.go                         # 1 (~799)
grep -n 'trustedClientIP(c, clientTrust' internal/server/*.go | grep -v _test | wc -l   # 3 (bootstrap ~207, invite ~405, client_trust ~600)
grep -n '^func contentSecurityPolicy' internal/server/csp.go                             # 1
grep -n 'func seedOtelgin\|production gin engine' $(git ls-files | grep -E 'series_contract_test\.go$')   # PC-04-06's production-engine seed
cat ci/stage.txt | grep -E '^(shipped_stage|release):'                                  # S2r1 / S2r1
ls core/internal/httpmw/chain.go core/internal/httpmw/metrics.go                         # exist (PC-05-01)
```

**STOP and report** if `ci/stage.txt` does not name `S2r1` for both lines or PC-05-01's files are absent.

## Steps

1. **Turn the core chain on for the mount.** `books.Module`'s `MountLegacy` call (or the kernel's `Build`, per
   PC-05-01) now runs stages 0–7 and 9–11 in front of gin for every request; stages 12–13 stay off (`LegacyAuthGin`).
2. **Remove the perimeter from gin in the same PR** (05-http-api §8.1 S2 row): delete the gin logger, recovery,
   security headers, CORS, compression (`compressionMiddleware` and `captureRawResponseController` if nothing else
   needs it) and the tracer half of `tracingMiddleware`; remove `apiRateLimiter` and the outer body cap from the
   `/api/v1` group. **Keep in gin:** the Basic gate (`servermiddleware.BasicAuth()`, until S3) and `otelgin`
   configured as at HEAD (`otelgin.Middleware("audiobook-organizer")`, the OTel global meter provider and views,
   HEAD's scope) with **only its tracer turned off** (`otelgin.WithTracerProvider` with a no-op provider); otelgin keeps
   HEAD's position after the Basic gate (05-http-api §4.2 stage 2 "Chain position"). The bootstrap, invite and ABS
   limiters stay in gin unchanged.
3. **Books mount values** (`legacy_manifest.go`): `DefaultCSP` = today's `contentSecurityPolicy()` evaluated per
   response (inline-script hash, Access team host; `csp.go` stays in ABO); `CSPReport: "/api/v1/csp-report"`;
   `BuildID` = the `<meta name="mediacore-build">` of the embedded `web/dist/index.html` (`dev` when nothing is
   embedded); `BasicExemptAssets` = HEAD's `basicauth.go:40-46` static-asset rule (recorded now, consumed at S3);
   each route's `RateClass` and `NoCompress` as PC-04-01 recorded them.
4. **Client address** (`client_trust.go`): `trustedClientIP` starts from `httpapi.ClientAddrFrom(c.Request.Context())`
   and keeps only its "local peer + verified Access assertion" branch on top (05-http-api §8.1 S2 row). The three
   limiter call sites keep their behaviour; their tests stay unchanged.
5. **Series test through the real chain** (05-metrics §6): move the `http_server_*` seeding onto a `coretest` boot with
   books' `MountLegacy` engine and HEAD's otelgin (never a test-local otelgin), with one request to a sampled `:id`
   legacy route and one legacy-path request that stage 10 refuses with 429. The golden diff must be **empty**; the
   source column stays `otelgin`.
6. **`ci/stage.txt`**: `shipped_stage: S2r2`, `release: S2r2`.
7. **Release note** (the changelog fragment carries it verbatim; 09 §9.2.1 row 12): "Requests refused before they
   reach a route (rate limited, too large, cross-origin preflights and refusals, the Basic gate's sign-in challenge
   and, from the identity release, requests that are not signed in) are now counted by the core server's HTTP metrics
   without an `http_route` label, instead of under the route they were sent to; preflights and Basic-gate challenges
   are counted for the first time. `http_server_response_body_size` now measures responses before compression."
   Plus: new cross-origin protection for cookie-authenticated unsafe requests (non-browser clients unaffected, no
   credential type exempt).

## Code contract

No new exported API. The mount fields are those of `httpapi.LegacyMount` (05-http-api §8.2): `DefaultCSP
func(*http.Request) string`, `CSPReport string`, `BuildID string`, `BasicExemptAssets func(path string) bool`; the
client address is read with `httpapi.ClientAddrFrom(ctx) (addr netip.Addr, attributed bool)` (§3.5). otelgin stays
`otelgin.Middleware("audiobook-organizer", otelgin.WithTracerProvider(<no-op>))`.

## TDD tests to write first (names fixed by 05-http-api §12 and 09 S2)

ABO side, real engine, through the `coretest` boot with books' mount:

- `TestCSP_DefaultPerOwner` — the legacy SPA response carries the inline-script hash, the configured Access team
  host and `Reporting-Endpoints`.
- `TestPerimeter_XAppBuildOnEveryResponse` — legacy, 404 and 410 responses carry `X-App-Build`.
- `TestRateLimit_ClientBuckets` — its legacy `RateClass` golden half (the rate-limit class golden).
- `TestCompressionExclusionsEqualHEAD` — the four legacy exclusions carry `NoCompress`; `text/event-stream` never
  gzipped; the log download gzipped once.
- `TestCSRF_CookieBodilessPOSTFromSiblingOriginRefused` on a legacy route (the cross-origin test).
- `TestPerimeterHonoursBlobOnlyValues` — extend PC-04-05's static half: rate limit disabled only in the blob → stage
  10 off; a runtime edit through `PUT /api/v1/config` applies without restart.
- `TestLegacySeriesLabelValuesMatchHEAD`, `TestHTTPSeries_OneRecorderPerRequest`, `TestNoRouteSeriesSharedNoDuplicate`,
  `TestPreMountRejection_RecordedByStage2` (the 429 and 413 cases at S2 r2; a 429 on a generalised-pattern member
  records no `http_route`; `Gather` returns no error).
- Keep green: PC-04-01's real-engine tests (`TestLegacyMount_MuxAndGinAgreeOnEncodedSlash` included), PC-04-06's wire
  goldens, ABS cell c, the bootstrap/invite/ABS limiter tests, `TestHistogramBucketsPinned`, `TestHEADSeriesStillPresent`.

## Acceptance

```bash
grep -n 'router.Use(' internal/server/server.go
#   expect only the Basic gate and otelgin's middleware (plus any non-perimeter Use your base had; list them in the PR)
go test ./internal/server/ -run 'TestCSP_|TestPerimeter_|TestRateLimit_|TestCompressionExclusionsEqualHEAD|TestCSRF_|TestPerimeterHonoursBlobOnlyValues|TestLegacySeriesLabelValuesMatchHEAD|TestHTTPSeries_|TestNoRouteSeries|TestPreMountRejection_|TestLegacyManifest_|TestLegacyMount_|Bootstrap|Invite' -count=1   # ok
go test ./internal/server/handlers/abs/ -count=1                                         # ok (ABS limiter and conformance unchanged)
go test ./internal/wirecontract/ -count=1                                                # ok
POL=$(dirname $(git ls-files | grep -E 'series_contract_test\.go$')); go test ./$POL/ -count=1    # ok
git diff --exit-code origin/main -- $(git ls-files | grep -E '(^|/)series\.golden$') && echo GOLDEN-UNCHANGED   # GOLDEN-UNCHANGED
grep -E '^(shipped_stage|release):' ci/stage.txt                                         # shipped_stage: S2r2 / release: S2r2
make ci                                                                                  # exit 0
make -C core ci                                                                          # exit 0
#   on the PR: compat-rehearsal (R-test row 12 case: a pre-mount 429 recorded with no http_route on cur and by otelgin on prev) and unit-lifecycle green
```

## Invariants and STOP conditions

- Every request that reaches gin is recorded by otelgin alone, with HEAD's `http_route`, `server_address`, labels and
  buckets; a request a core stage answers first is recorded by stage 2 alone, with no `http_route`. Any duplicate
  series, `Gather` error or `/metrics` 500 → STOP.
- Authentication and authorization do not change (gin's `RequireAuth`, `s.perm`, guards unchanged; stages 12–13 off).
- The Basic gate stays gin's; its exempt set is unchanged until S3.
- No stored format changes; rollback is a binary swap.
- CLAUDE.md concurrency: no library-scale loop.

## Out of scope

The Basic gate's move (S3), authentication (S4), logging and telemetry installs (W06), `PromRegisterer` (PC-06-02).

## Rollback

Binary swap to the S2r1 build. After a rollback below r2 otelgin records the refused requests under their route again
(row 12); nothing stored changes.

## Repo rules

Worktree per task off fresh `origin/main`; 4-line headers on every changed file; headerless changelog fragment;
conventional commit `feat(s2): S2 r2 flip — perimeter leaves gin for the core chain` ending with
`Stage-Release: S2 flip` and `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase` once every
required check (`compat-rehearsal`, `unit-lifecycle` included) is green on an up-to-date branch; never squash; never
`--admin`.

## Planner notes

- Path drift: the row lists `internal/telemetry/contract/testdata/series.golden`; after PC-03-04 the suite lives in
  `internal/telemetrypolicy/`, and the golden must not change — the file that changes is the contract test's seeding.
- Files missing from the row: `internal/server/server_lifecycle.go` (the `/api/v1` group's `api.Use(apiRateLimiter,
  bodyLimitMiddleware, ...)`), `internal/books/module.go` (if the mount fields are set there rather than in
  `legacy_manifest.go`), and the series contract test file.
