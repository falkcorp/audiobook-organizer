<!-- file: docs/plans/platform-core/reviews/spec-round-12-final-verification.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c3e9427-cb8c-47a7-8be8-9c1568ced894 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 12: final verification of R11-1 to R11-5

Lens: **final verification of the round-11 resolutions** (ID prefix `FIN12`). Inputs: `docs/specs/platform-core/`
after the round-11 revision: 03 v1.14.2; 05-http-api, 05-storage, 05-tasks, 06 and 14 v1.12.2; 05-metrics, 13 and README
v1.11.2; 09 v1.11.1. Binding: R11-1..R11-5, R10-1..R10-11, R9-1..R9-11, A-1..A-17 and R3-1..R8-6. Code anchors
are `path:line @748fd787a`. Third-party anchors are at the versions `go.mod` pins, read from the module cache.

**Result: 0 BLOCKER, 3 MAJOR, 7 MINOR.**

- **R11-1 works.** The rebuilt experiment (section A) confirms all three required properties:
  - `NoRoute` and SPA loads record no `http_route`, and every gin-handled label set equals HEAD's;
  - a gather has no duplicate family or series, and `/metrics` answers 200;
  - a core route records its own `http_route`.

  Its text landed everywhere except one stale sentence in 09 S2 (FIN12-6). The "identical by construction" claim
  also covers less than it says (FIN12-7).
- **The three MAJORs are half-fixes of round 11:**
  - R11-2's admitted paths miss two of HEAD's five Dependabot ecosystems (FIN12-1);
  - R11-4's `levels:` rule depends on an H flip PR that the marker rules cannot express, and its revert is refused
    (FIN12-2);
  - R11-3 left three books operations handlers with no tasks-client method to call (FIN12-3).
- **Reviser residuals:** FIN12-3 (MAJOR), FIN12-4 (MINOR) and FIN12-5 (MINOR).

## A. The R11-1 experiment

**Setup.** I built a scratch Go module outside the repo, copied the repo's `go.mod` and `go.sum` into it (gin v1.12.0,
otelgin v0.72.0, otel and sdk/metric v1.47.0, exporters/prometheus v0.69.0, client_golang v1.25.0), and built it
with the pinned go1.27.2 offline from the module cache, using a private build cache. Afterwards I deleted the module
and its cache and ran `go clean -cache`. Nothing in either checkout was modified.

**What it models.**

- **Metrics pipeline (HEAD's).** One SDK MeterProvider with HEAD's three `http.server.*` bucket views
  (`internal/telemetry/views.go:62-64`) is installed as the OTel global. It exports through the Prometheus exporter
  with `WithoutScopeInfo` (`telemetry.go:378`) into a registry.
- **Gin engine (HEAD's configuration).** `otelgin.Middleware("audiobook-organizer")` with a no-op tracer reads the
  global. The routes are `/api/v1/books/:id`, the operations component (`:id/activity`, `:id/results`, `v2/:id`)
  and a `NoRoute` that applies `spaWillNotServe` (`spa_fallback.go:115-125`) before it serves the shell.
- **HEAD variant.** Gin is the root handler, as at `server_lifecycle.go:913-915,1079`.
- **Spec variants.** A `ServeMux` sits in front of gin:
  - every manifest route is a pattern, including the generalised `GET /api/v1/operations/{p4}/{p5}` and the kept
    `…/v2/{id}`;
  - the legacy `/` fallback checks `NonSPA` and then calls gin;
  - the legacy `/api/` handler answers router 404s;
  - one native route, `GET /api/core/v1/tasks/runs/{id}`.
- **The wrapper.** It marks stage 2's carrier, puts the pattern in the context (`MatchedPattern`) and clears
  `r.Pattern` on the copy it hands to gin. The SEC2-22 post-match check runs in gin after otelgin.
- **Stage 2.** It creates its three instruments with `semconv/v1.43.0/httpconv` in the scope named by
  `HTTPMetricsScope` = otelgin's `ScopeName`/`Version`, on the served SDK provider. It records unmarked requests
  with otelgin's attribute rules (typed status and port, fixed `server.address`, route from the carrier only), and
  a stage-10 stand-in answers 429 before the mux.

**Requests.** All carry a Host with port 8484, as HEAD's golden seed does:

- a gin route, a generalised-pattern member, a generalised-pattern miss and the kept `v2` route;
- two SPA loads;
- `DELETE /library` and `GET /apix` (gin `NoRoute` 404s);
- the colliding router 404s `DELETE /api/v1/x` and `GET /api/v1/x`, and a `NonSPA` 404 (`/livez/`);
- the native route;
- a pre-mount 429.

| Variant | Gather | Scrape | Rows with `http_route="/"` | Rows with the mux pattern | Result |
|---|---|---|---|---|---|
| HEAD | ok | 200 | 0 | 0 | baseline: 6 duration series |
| R10 as written (pattern passed to gin, stage 2 in core's `httpapi` scope) | ok | 200 | 4 (SPA ×2, both `NoRoute` 404s) | 1 (the miss) | fails HEAD parity (VER11-1 point 2 reproduced) |
| pattern cleared, stage 2 in core's own scope | **6 errors** ("collected before with the same name and label values") | **500** | 0 | 0 | duplicate series (VER11-1 point 4 reproduced) |
| **R11-1** (pattern cleared, otelgin's scope and version, typed attributes) | **ok** | **200** | **0** | **0** | 8 series; the 10 requests HEAD also serves give byte-identical label sets (only `/livez/` differs, the intended FE3-13 404); `DELETE` 404 and `GET` 404 aggregate across both recorders (counts 2 and 4); core route `http_route="/api/core/v1/tasks/runs/{id}"`; 429 with no route; no `otel_scope_` label |
| R11-1, otelgin built before the global is set (OTel delegate) | ok | 200 | 0 | 0 | shared too, so the order of engine build and install does not matter |
| R11-1, stage-2 duration created first with no bucket advice | ok | 200 | 0 | 0 | shared; HEAD's buckets come from the view (see FIN12-7 (b)) |
| R11-1, status and port recorded as strings | 6 errors | 500 | 0 | 0 | the typed values are load-bearing |
| R11-1, scope `Version` empty | 6 errors | 500 | 0 | 0 | the exact `Version` is load-bearing |
| R11-1, stage-2 duration with another description (a semconv skew) | 2 errors | 500 | 0 | 0 | the SDK makes a second instrument; see FIN12-7 (a) |

The mechanism is the SDK's instrument cache:

1. `MeterProvider.Meter` caches meters by scope (name, version, schema URL, attributes; `sdk/metric@v1.47.0
   provider.go:79-98`).
2. A meter caches histograms by name, description, unit and kind (`meter.go:703-717`, `instrument.go:164-176`).
3. So two identical creations in otelgin's scope share one aggregator.

otelgin's meter has no schema URL (`otelgin@v0.72.0 gin.go:56-61`), and its `MetricAttributes` falls back to
`req.Pattern` only when the route is empty (`internal/semconv/server.go:392-394`).

**Conclusion.** The design works as specified against HEAD. It depends on three exact matches, each confirmed by a
failing variant: otelgin's scope version, typed attribute values and an identical description.
`TestNoRouteSeriesSharedNoDuplicate` checks all three on every otelgin bump.

## B. Did R11-1 to R11-5 land in every file?

| Resolution | Status | Where checked |
|---|---|---|
| R11-1 | **Landed, except 09 S2's HTTP row** (FIN12-6). | **Consistent:** 03 §3.6 "Metric name"; 05-http-api §1.1, §3.5 (`HTTPMetricsScope`, `InstrumentScope`, `MatchedPattern`), §4.2 stage 2 (a)–(c), §4.8 escaped-segment row, §8.1 S2 and S5 rows, §8.2 and §12; 05-metrics §3 rules 1–3 and "One instrument set", §6 seeding, §12; 06 `MountLegacy`, `PromRegisterer`, `ServedMeterProvider` and `HTTPMetricsScope`; 09 §9.2.1 row 12 and §9.2.7; 13 §13.5.1; 14 hazards row. **Gaps:** the "disjoint by `http_route`" wording survives only in history lines, but the identity tuple and the by-construction claim are incomplete (FIN12-7) and the shared-series test starts one release late (FIN12-8). |
| R11-2 | **Landed consistently, but the rule is incomplete** (FIN12-1, FIN12-9). | 03 §3.2 item 6; 09 §9.2.1 "Bot commits", §9.8 row 37 and the `test_check_soak.py` row; README; 14 K20. The `ratchet-lower.yml` handling (a trailer in its own message) is right. |
| R11-3 | **Landed.** | **The count holds:** HEAD registers exactly 38 `/operations` routes: `server_lifecycle.go` 13, `wire_library_routes.go` 2, `wire_operations_routes.go` 23. **Consistent:** 05-tasks 5T.3.10 and the Client comment, 09 S5 Endpoints, 05-http-api §4.8, §8.1 S5 row and `TestS5_OperationsComponentStaysBooks`, 07 §7.9, 04's row. **Residuals:** FIN12-3 and FIN12-4. |
| R11-4 | **Landed for UI-0. Half-fixed for H1–H3** (FIN12-2). | The grammars, the `levels:` pass criterion, §9.8 row 36, §9.13, README, 14 K20 and 04's UI-0 row agree. The level sidecar exists from S0, because every guard-aware boot rewrites it (05-storage §5S.3.7), so S0–S10 rows can pass. **Residual:** FIN12-5. |
| R11-5 | **Landed.** | **Revert clause:** 09 check (c) and §9.8 row 38; its scope is too narrow for H flips (FIN12-2 point 4). **`set_aside_started`:** 06 P0a. **`seed`:** 09 S2b agrees with 05-storage §5S.4. **Fresh verified archive:** 09 §9.13 and the H1/H2 rows, 05-storage §5S.3.13 item 4, its deferred-build row and `TestH1H2_DeferredWithoutFreshVerifiedArchive`; one interaction remains (FIN12-10). **Rank rows:** 960 and 970. **Other notes:** the R10-9 justification (09, 07), the gzip response-size note (05-http-api, 09 row 12) and the updater self-test fixture (05-updater 5U.12) are applied. The `Registry.Test` LAN note named no concrete text and was not applied, which R11-5 allows. |

## C. Findings

| ID | Sev | Spec file and section | Finding |
|---|---|---|---|
| FIN12-1 | MAJOR | 09 §9.2.1 "Bot commits" (`09:283-297`), §9.8 row 37; 03 §3.2 item 6 (`03:246-250`); README; 14 K20 | R11-2 half-fixed: HEAD's grouped Dependabot PR is still refused |
| FIN12-2 | MAJOR | 09 §9.2.1 flip-PR rule (`09:170-175`) and checks (b)/(c) (`09:276-282`); §9.13 (`09:1726-1729`); §9.8 rows 36 and 38 | R11-4 half-fixed for H releases: no PR can write `levels:`, and the revert of one that did is refused |
| FIN12-3 | MAJOR | 05-tasks 5T.3.10 (`05-tasks:847-865`, `:1084-1098`); 09 S5 Endpoints (`09:1315`) | R11-3 residual: timeline, log download and record discard have no `tasks.Client` method |
| FIN12-4 | MINOR | 05-tasks 5T.3.10 table (`05-tasks:1072`) and compat sentence (`:1076`); 09 S5 Endpoints | Residual: `/maintenance-window/*` is "replaced" but neither aliased nor kept |
| FIN12-5 | MINOR | 09 §9.2.1 "Soaks" (`09:199-205`), UI-0 Flip PR row (`09:1264`) | Residual: UI0 adds a 14-day slot to the serial soak chain |
| FIN12-6 | MINOR | 09 S2 HTTP row (`09:1237`) | R11-1 not landed: "the legacy handler wrapper passes `r.Pattern` to gin" |
| FIN12-7 | MINOR | 05-metrics §3 (`05-metrics:189,195,200,549`); 05-http-api §4.2 stage 2 (b) (`05-http-api:663`) | R11-1's "otelgin's by construction" covers less than it states |
| FIN12-8 | MINOR | 05-http-api §12 (`05-http-api:1458`) | `TestNoRouteSeriesSharedNoDuplicate` starts at S2 r2, but the collision exists from S2 r1 |
| FIN12-9 | MINOR | 09 §9.2.1 (`09:289-290`), §9.8 row 37 | The merge-commit exemption can never hold for a GitHub "Update branch" merge |
| FIN12-10 | MINOR | 09 §9.2.1 `<since>` (`09:236-241`), §9.13 (`09:1729-1735`); 05-storage §5S.3.13 item 4 | A deferred H1/H2 boot starts the soak clock before the cutover runs |

### FIN12-1 — MAJOR — R11-2 half-fixed: the bot path list misses two of HEAD's five Dependabot ecosystems

**Evidence.**

- **HEAD's Dependabot configuration.** `.github/dependabot.yml` @748fd787a puts five ecosystems into one
  `multi-ecosystem-group: dependencies` with one weekly schedule: `docker` (`directory: /`), `github-actions`,
  `gitsubmodule`, `gomod` and `npm` (`/web`). Dependabot therefore opens one PR a week that spans all of them.
- **HEAD's latest Dependabot commit is refused.** That commit, `1c2244869` ("deps(deps): Bump node", the
  dependencies group), touches six files: `Dockerfile`, `Dockerfile.build-cgo`, `go.mod`, `go.sum`,
  `web/package.json` and `web/package-lock.json`. Under 09:286-289 the admitted set is:
  - `go.mod`, `core/go.mod`, `package.json` files;
  - `go.sum`, `core/go.sum`, `package-lock.json` files;
  - `.github/`.

  The two Dockerfiles are outside that set. A bot commit "in a PR that touches any other path is refused like any
  untrailered commit", so check (d) refuses this commit.
- **The submodule ecosystem is refused too.** Its `.standards` gitlink (`.gitmodules`) is also outside the set.
- **The claim does not hold.** 09:290 says "Dependabot's version and security PRs … merge as at HEAD". From S0 that is
  false for every week whose group carries a base-image or submodule bump.

**Fix (09 §9.2.1, §9.8 row 37, the `test_check_soak.py` row; 03 §3.2 item 6, README and 14 K20 restate it).**

1. Define the admitted set per configured ecosystem, as a fixed table in `check_soak.py`:

   | Ecosystem | Admitted paths |
   |---|---|
   | gomod | `go.mod` and `go.sum` in each gomod directory |
   | npm | `package.json` and `package-lock.json` in each npm directory |
   | docker | `Dockerfile*` in each docker directory (HEAD: `Dockerfile`, `Dockerfile.build-cgo`), optionally only `FROM` lines |
   | github-actions | `.github/` |
   | gitsubmodule | the gitlink paths `.gitmodules` lists (HEAD: `.standards`) |

2. Add three test cases:
   - `1c2244869`'s six-file set is admitted;
   - a `.standards`-only Dependabot PR is admitted;
   - a Dependabot commit that also touches `internal/` is still refused.
3. The root-tidy step of 03 §3.2 item 6 is repo-controlled, like `ratchet-lower.yml`. Give its commit
   `Stage-Release: none` instead of relying on the bot-author rule, and push it with the PAT. HEAD's
   `auto-revert.yml` pushes with the PAT for the reason it states: a `GITHUB_TOKEN` push triggers no workflow, so the
   PR's required checks would not re-run on the tidy commit.

### FIN12-2 — MAJOR — R11-4 half-fixed for H releases: no PR can write `levels:`, and its revert is refused

**Evidence.**

1. **No H marker value exists.** A flip PR is "the PR whose diff changes the `shipped_stage:` line", and that line's
   ordered values are `pre-S0 < S2r1 < … < S10` (09:170-174).
2. **§9.13's trailer is not a flip trailer.** Check (b) refuses "a `flip` trailer on a PR that changes no
   `shipped_stage:` or a `shipped_stage:` change without one" (09:276-277). §9.13 gives the H trailer as
   `Stage-Release: H1 1` "and so on" (09:1727-1728).
3. **The `levels:` line cannot be written.** Check (c) refuses "a `levels:` change in any PR but an H release's flip
   PR" (09:279-280). The PR that sets `release: H1` therefore has two options, and both are refused:
   - It changes no marker. Then it is no flip PR, and its `levels:` line is refused.
   - It writes an `H1` marker value. That value is outside the ordered list, and it carries a non-flip trailer.

   **Consequence:** H1's soak row compares "absent = every owner at 0" against a build and a store that are
   raised, so `soak_record.py` refuses the row. Check (a) then refuses H2 and H3. This is BLK11-1 point 1 again.
4. **The revert of an H flip is refused.** The admitted-revert exemption covers only check (c)'s backwards-value
   clause (09:277-278, :302; §9.8 row 38). The exact inverse of an H flip also changes `levels:`, which check (c)'s
   `levels:` clause refuses.
   - So a bad H flip cannot be backed out of `main` before auto-promotion, and row 38 gives exactly that as the
     reason the revert rule exists.
   - H flips are the one-way, floored steps that `make rollback` cannot undo.
   - No fix-forward PR can lower `levels:` either.

**Fix (09 §9.2.1 "One flip PR", checks (b) and (c), §9.13, §9.8 rows 36 and 38, `test_check_soak.py`).**

1. Extend the marker's ordered values with `H1`, `H2`, `H3` and `H2H3` after `S10`, in landing order, as the release
   IDs already are.
2. Each H stage table names its flip PR. That PR carries `Stage-Release: <H> flip` and sets `shipped_stage:`,
   `release:` and `levels:` together. Content PRs carry `<H> 1`. Correct §9.13's trailer sentence to match.
3. Add "except in an admitted revert" to check (c)'s `levels:` clause, as R11-5 did for the backwards-value clause.
4. Add three test cases:
   - an `H1 flip` PR that changes all three lines is admitted after `S10`'s row;
   - an `H1 1` PR that changes `levels:` is refused;
   - the exact revert of an H1 flip is admitted.

### FIN12-3 — MAJOR — R11-3 residual: three books operations handlers have no `tasks.Client` method

**Evidence.** HEAD registers three routes that the round-11 text never assigns a client method:

| Route | Handler |
|---|---|
| `GET /operations/timeline` (`wire_operations_routes.go:24`) | `GetOperationTimeline`, `handlers/operations_v2.go:237` |
| `GET /operations/v2/:id/logs/download` (`:26`) | `DownloadOperationLogs`, `operations_v2.go:494` |
| `DELETE /operations/v2/:id/record` (`:37`) | `DiscardOperationV2`, `operations_v2.go:680` |

- **R11-3 took away their path to core.** Before R11-3 the first two were aliases onto native `GET /timeline` and
  `GET /runs/{id}/log`. R11-3 makes every `/api/v1/operations` route a books route "whose handler calls core's tasks
  client from T0+T1".
- **The client has nothing to call for them.** `tasks.Client` has only `Start`, `Get`, `Runs`, `Cancel`, `Decide`,
  `Pause`, `Unpause` and `PauseState` (05-tasks:847-865). Neither 05-tasks:1089-1094 nor 09 S5 names the three
  routes.
- **Timeline.** `Runs` bounds its window on `Created >= Since` (05-tasks `RunFilter`). HEAD bounds the window on
  CompletedAt and keeps in-flight runs (the store comment quoted at `operations_v2.go:298`), so `Runs` cannot
  reproduce HEAD's rows.
- **Log download.** No client method reads a run's log page. `RunStore.Pages` is internal to the executor.
- **Discard.** HEAD's discard deletes the row, indexes, checkpoint, logs and errors. It answers 409 while the run is
  queued, running, waiting or held by a live worker (`operations_v2.go:665-679`).
  - From T0+T1 only the executor knows whether a live worker holds the run.
  - `RunStore` has no single-run delete, only `Prune` of terminal runs by time (05-tasks:1348).
  - A books handler that deletes the rows itself bypasses the executor's state and the `opsMu` hold.
  - At T8, when opsbridge (HEAD's registry path) goes, the handler has nothing to call.

**Fix (05-tasks 5T.3.10 Client and the R11-3 paragraph, 09 S5 Endpoints).** Add three methods to `tasks.Client`,
and have the native routes use the same code:

- **`Timeline(ctx, TimelineQuery) ([]RunView, error)`.** This is native `GET /timeline`'s query: a finished-at window
  that includes unfinished runs, `Kinds` with former-ID resolution, `States`, and HEAD's `Limit` clamp.
- **`Log(ctx, id RunID, q PageQuery) iter.Seq2[LogLine, error]`.** It is gated by the def's `View` and passed through
  `RedactRunView`'s log rule. Books keeps `mayReadLogsOf`, `readableLogs` and the gzip rendering.
- **`Discard(ctx, id RunID) error`.**
  - Permission: Cancel's rule, as HEAD's "same permission as cancel".
  - Errors: `ErrRunNotFound` (404) and a new `ErrRunActive` (409).
  - Mechanism: the executor stages it in `WriteRuns` through a new `RunStore.Delete(ctx, b, id)`. For books' store
    that is HEAD's registry discard key set, under `opsMu`.

Also:

- List the three routes and methods beside the other client calls in 05-tasks and 09 S5.
- Add `TestS5_OperationsTimelineLogDiscardParity`. It checks HEAD's timeline window and log body. Discard of a
  running run answers 409. Discard of a terminal run removes every key, and the resume sweep does not resurrect it.
- **Acceptable alternative for the two reads only:** books reads its own taskstore, which it owns through S10.
  Discard must still go through the executor.

### FIN12-4 — MINOR — Residual: `/maintenance-window/*` is "replaced" but neither aliased nor kept

**Evidence.**

- **HEAD's routes.** HEAD has `POST /maintenance-window/run`, `GET /maintenance-window/status` and
  `PUT /maintenance-window/config`, all `settings.manage` (`wire_operations_routes.go:121-123`).
- **The spec lists them as replaced but never aliases them.** 05-tasks' table lists `/maintenance-window/*` in the
  schedules row's "Replaces" column (05-tasks:1072). The compat sentence aliases only `/api/v3/ops/*`,
  `/api/v1/op-defs` and `/api/v1/tasks` (:1076). 09 S5 counts `/api/v3/ops/*` (12), `/api/v1/op-defs` (2) and
  `/api/v1/tasks` (3), which matches HEAD's 3 `/tasks` routes (`:118-120`), and never mentions the window.
- **The legacy SPA still needs them.** Its MaintenanceTab calls these routes until S9 (07's split row).
- **Why MINOR.** 05-http-api §8.1's "legacy paths aliased" rule gives the default reading.

**Fix.** Choose one of two options:

1. **Alias them.** Add "`/api/v1/maintenance-window/*` (3)" to the compat sentence and to 09 S5's alias list, with
   these targets, and add the ledger rows:
   - `run` → `POST /definitions/{kind}/runs` for the maintenance-window kind;
   - `config` → `PATCH /schedules/{kind}`;
   - `status` → `GET /schedules`, with a `LegacyPath.Shape` that renders HEAD's status body.
2. **Keep them in books.** If `Shape` cannot render HEAD's status body, drop them from "Replaces" and keep the three
   as books gin routes on `Deps.Tasks`, like the operations routes.

### FIN12-5 — MINOR — Residual: UI0 takes its own 14-day slot in the serial soak chain

**Evidence.**

- **UI0 joins the serial order.** UI0 "takes its place in the order where it lands" between `S2r3` and `S4r1`
  (09:199-205).
- **Every later step waits for it.** Check (a) makes the next flip need UI0's row. Check (b) makes UI-0's own content
  wait on the predecessor row of the release in flight.
- **Cost.** The chain from `S2r3` to `S4r1` grows from three soaks (`S2br1`, `S2br2`, `S3`) to four, which adds at
  least 14 days.
- **What UI-0 actually ships.** Only `@mediacore/api-client` and the Docker frontend stage (04:914). Its work was
  meant to overlap S2b and S3 (09:1022).
- **Why MINOR.** This is a schedule cost, not a correctness defect.

**Fix, if the owner does not accept the cost: make UI-0 a rider.**

- Check (b) judges its trailer `UI-0 1` against the predecessor of the release `release:` names when the PR merges
  (`S2r3` or later, and before `S4r1`), as it judges that release's own content.
- UI-0 never changes `release:`. Drop `UI0` from the release-ID grammar, from the pass-criteria parenthesis and from
  04's row.
- The hosting release's soak and rollback cover UI-0's content, and the trailer still declares that content, so
  BLK11-1's undeclared-content hole stays closed.

Otherwise, keep UI0 and state the extra 14 days in 09 §9.3's schedule.

### FIN12-6 — MINOR — R11-1 not landed in 09 S2's HTTP row

**Evidence.** 09:1237 still says two things that R11-1 changed:

- "the legacy handler wrapper passes `r.Pattern` to gin and returns 404 when gin's matched route … is not a member";
- "stage 2 records only core and module routes and the pre-mount refusals …", with no shared instrument set.

09:14 gives the owning 05 file precedence on mechanism, which is why this is MINOR. But this row is the stage table
that implementers follow, and following it reproduces VER11-1.

**Fix.** Rewrite the two clauses:

> "the legacy handler wrapper clears `r.Pattern` before gin and carries the matched pattern as
> `httpapi.MatchedPattern(ctx)`, and returns 404 when … (SEC2-22, A-1, R11-1)"

and

> "… with no `http_route`, all into otelgin's instruments (`app.Options.HTTPMetricsScope`; R10-3, R11-1)"

### FIN12-7 — MINOR — R11-1's "otelgin's by construction" covers less than it states

One root cause: the spec asserts the equivalence with otelgin instead of listing what it requires.

**(a) The owner identity tuple omits description and number type.**

- 05-metrics:189 and :195 record the identity as `(scope name, scope version, instrument name, kind, unit)`.
- But :200 requires the same "number type, unit and description", and `TestFamilyTaken_SemconvHTTPOneInstrument`
  (:549) expects a description mismatch to be refused.
- Experiment: the same scope with a different description gives a second SDK instrument (the instrument ID includes
  `Description`), duplicate series and a 500 scrape. The wrapper as worded admits it.
- When this happens: an otelgin bump to a newer semconv while core still imports `semconv/v1.43.0` produces exactly
  this case.

**(b) Bucket advice is not part of `httpconv`'s constructor.**

- otelgin passes `WithExplicitBucketBoundaries(0.005 … 10)` itself (`internal/semconv/server.go:56-60`).
- The SDK keeps the first creation's advice, because the instrument ID carries none.
- ABO is safe through `MetricViews` (the no-advice variant passed). A native app with no `MetricViews` would get the
  SDK's default boundaries (0 … 10000) on a histogram measured in seconds.

**(c) The source of `server.port` is unstated.**

- otelgin takes the port from the request's Host when the server name has none (`server.go:364-377`).
- HEAD's golden pins `server_port` (`series.golden:85-87`, seeded with a Host that has a port).
- A stage 2 that uses the listener port, or omits the port, splits each colliding pair into two series. That causes
  no gather error, but it is not the "one series" that `TestNoRouteSeriesSharedNoDuplicate` asserts.

**Fix.**

- **05-metrics.** The identity becomes (scope name, version, schema URL, instrument name, kind, number type, unit,
  description). `InstrumentScope` gains `SchemaURL`, which is empty for otelgin v0.72.0 (`gin.go:56-59`).
- **05-http-api stage 2 (b).**
  - Stage 2 passes otelgin's duration boundaries explicitly.
  - It takes `server.port` from the Host port when `ServerName` has none, by otelgin's `requiredHTTPPort` rule.
  - The exact scope version and the typed status and port are load-bearing; the string and empty-version variants
    fail without them.

### FIN12-8 — MINOR — The shared-series test starts one release late

**Evidence.**

- `TestNoRouteSeriesSharedNoDuplicate` runs "from S2 r2" (05-http-api:1458).
- The colliding pair exists from S2 r1. The mux and its router 404s land at S2 r1: §8.2 says "under a legacy prefix it
  gets 404".
- Stage 2 records "at every release", and at S2 r1–r2 it records into `ServedMeterProvider` (05-metrics §3 rule 2).

**Fix.** Run the test from S2 r1, for example with `DELETE /library` against `DELETE /api/v1/x`.

### FIN12-9 — MINOR — The merge-commit exemption can never hold

**Evidence.**

- 09:289-290 says "A GitHub-generated merge commit whose parents are both in the PR's range counts the same way."
- An "Update branch" merge has the base tip as its second parent, and the base tip is never in `base..head`, so the
  condition never holds.
- "Counts the same way" also limits the exemption to bot-path PRs. So every human PR that was updated by a merge is
  refused by check (d).
- Why MINOR: the repo merges by rebase, and updating by rebase avoids the problem.

**Fix.** Replace the sentence:

> "a merge commit whose first parent is in the PR's range, whose other parents are ancestors of the PR's base, and whose
> remerge diff is empty (`git show --remerge-diff` prints nothing) counts as `none` in any PR"

Add one test case.

### FIN12-10 — MINOR — A deferred H1/H2 boot starts the soak clock before the cutover runs

**Evidence.**

- R11-5 defers H1 and H2 until a fresh verified archive exists. The deferred boot serves the old store, and the
  owner then takes the archive and restarts (09 §9.13; 05-storage §5S.3.13 item 4).
- `<since>` is a deploy time (09:236-241). A restart is not a deploy, and the level check runs only at record time.
- Example: an H1 that is deployed on day 0 and cuts over on day 10 records its row on day 14, after only 4 days at
  the raised level.

**Fix.**

- For an H release, `<since>` is the later of the deploy and the first boot that ran at the declared levels.
- Add `levels=<vector>` to the boot line that `TestBootLogsBinaryID` already pins, and have `soak_record.py` read it
  from the journal it already scans.

## D. Residuals the reviser flagged

| Residual | Finding | Severity | Fix in one line |
|---|---|---|---|
| Three operations handlers without a `tasks.Client` method (timeline, logs download, v2 record discard) | FIN12-3 | MAJOR | Add `Client.Timeline`, `Client.Log` and `Client.Discard` (with `RunStore.Delete` under the executor's hold), name them in 05-tasks and 09 S5, and add a parity test |
| 05-tasks lists `/maintenance-window/*` as replaced; 09 S5 does not count it | FIN12-4 | MINOR | Alias the three routes, listed and counted with ledger rows, or keep them in books and drop them from "Replaces" |
| UI0 takes its own 14-day soak slot | FIN12-5 | MINOR | Make UI-0 a rider on the release in flight, or keep UI0 and state the +14 days |

## E. Noted, not filed

- **The Basic gate does not see router 404s.** From S2 r1 until S3 the gate is still in gin, so it does not see the
  router's 404s for unmatched legacy-prefix and `NonSPA` paths. An unauthenticated client therefore gets 404 where
  HEAD answered 401. That is a route-existence oracle over an open-source route set, and R11 did not cause it.
- **The semconv coupling outlives the repo split.** "The semconv version moves with otelgin's in one PR" holds only
  while core is nested. After the 12 split, an otelgin bump that changes semconv needs a core release first, and
  `TestNoRouteSeriesSharedNoDuplicate` fails the Dependabot PR until then. That is safe; 12 or 13 could name it.
- **The post-match check's chain position is unstated.** It must follow otelgin for §4.2 (a)'s "the §8.2 post-match
  refusal … included" to hold. The experiment placed it after otelgin.
- **06 P0a's wording is looser than 05-storage's.** 06 says the in-flight file is written "for a reset at
  `set_aside_started`"; 05-storage:1303 says the file precedes it. The intent is the same.
