<!-- file: docs/plans/platform-core/reviews/spec-round-11-verification.md -->
<!-- version: 1.0.0 -->
<!-- guid: 003698b4-0609-45cc-9eaa-45406b90c9ae -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 11: verification of the round-10 fixes

Lens: **round-10 fix verification** (ID prefix `VER11`). Inputs: `docs/specs/platform-core/` at the round-10
revision (03 v1.14.1; 05-http-api, 05-storage, 05-tasks, 06 v1.12.1; 05-metrics, 13, 04 v1.11.1; 09 v1.11.0).
Binding: R10-1..R10-11, R9-1..R9-11, A-1..A-17, R3-1..R8-6. Code anchors are `path:line @748fd787a`; third-party
anchors are at the versions `go.mod` pins (otelgin v0.72.0, `otel/exporters/prometheus` v0.69.0, client_golang
v1.25.0), read from the module cache. Experiments ran in a scratch module that copies the repo's `go.mod`, with the
pinned go1.27.2 toolchain; nothing in the repo was modified.

**Result: 1 BLOCKER, 2 MAJOR.** Nine of the eleven resolutions landed consistently and hold against HEAD. The two that
do not:

- **R10-3's "disjoint by `http_route`" argument is false at otelgin v0.72.0.** otelgin falls back to the request's
  `ServeMux` pattern when gin matched no route, and the spec makes both recorders emit no-route 404s. As written the
  design either fails its own HEAD-parity test or serves a duplicate series, which fails every scrape (VER11-1).
- **R10-7's check (d) refuses every Dependabot and workflow-bot PR.** That contradicts 03 §3.2 item 6, which routes
  dependency and security updates through ordinary PRs (VER11-2).

R10-1 also left one sentence in 05-tasks that re-creates COR10-1's defect for the other 33 `/api/v1/operations` routes
(VER11-3). `hello` compiles against the owner files (section B).

## A. Regression check (R10-1 … R10-11)

| Resolution | Status | Where verified (spec; code @748fd787a) |
|---|---|---|
| R10-1 operations routes stay books' | **Landed for the five routes.** 03 R6, 04 row, 05-http-api §4.8 rule 1, rule 5 and "The operations component", 05-tasks 5T.3.10, 07, 09 S5 and §9.8, and `TestS5_OperationsComponentStaysBooks` all agree. Core's run route is spelled `/api/core/v1/tasks/runs/{id}` everywhere. No `ReportLegacyRoute`, `…MovesWhole` or core dispatcher remains. HEAD anchors hold: `wire_operations_routes.go:27,103,110`, `wire_library_routes.go:48` and `server_lifecycle.go:1952`, all `library.view`. **Residue:** 05-tasks' compat sentence (VER11-3). | VER11-3 |
| R10-2 ambient scopes never reach a confined key | **Fixed.** 05-rbac §3.4, §4.4 and §6.3 (one statement) decide confinement on the effective set. 05-authn §11.2 H1, 06 `LegacyAmbientScopes`, 09 §9.2.6 and §9.13, and 12 §12.4.4 cite it. `Confined()` requires a non-empty set, which matches HEAD's `workerOnly` (`internal/server/middleware/auth.go:268-297`, intersection `:403-421`). Both tests are named. | — |
| R10-3 otelgin / stage-2 split, `WithoutScopeInfo` | **Half-fixed.** The LEAD correction landed in 03, 05-http-api, 05-metrics, 06, 09, 13 and 14: no scope label is served, pre-mount refusals carry no route, and `MetricViews`, `ServiceName`/`ServerName` and `ServedMeterProvider` are in place. HEAD anchors hold: `telemetry.go:378,446-447`, `views.go` (views by instrument name only), `series_contract_test.go:727-728`. **But the disjointness premise fails** (VER11-1). `ops_abandoned_calls` moves once, at T6, consistently in 05-metrics §13, 05-tasks 5T.11 and 09. | VER11-1 |
| R10-4 hold on the batch, one rank table | **Fixed.** `LocksHeld(b, l)` is used in every owner file (no `ctx` form is left). The 18-row table is in 05-storage §5S.3.2 and is cited by 05-authn, 05-config, 05-tasks, 05-metadata, 05-notify and 05-ai. Its four HEAD nested orders match the code (`pebble_store_auth.go:209-212,590-593,744-760`, `user_merge.go:507-529`). The 05-metadata one-batch contradiction is gone. Two inner leaf mutexes have no row (noted below). | — |
| R10-5 `OnCommitted` (gap-fill inside the lock) | **Fixed.** `storage.OnCommitted(b, CommitStep{Key, Run})` is spelled the same in 03, 05-storage, 05-config and 06. `ConfigCommitObserver` survives only in history lines. The step renders from rows read inside the hold. Every committer is named: `Committer`, `Registry`, `notify`, and `secrets` for a kept name. This matches HEAD's `SaveConfigToDatabase` hold through `SaveConfigToFile` (`persistence.go:1940-2013`). | — |
| R10-6 swap failure edges, hook confinement | **Fixed.** Machine 1 has `rechecked --> swapped_back` and `staged --> rejected` (05-storage §5S.3.16). Step c is copy-if-exists (05-backup step c, x). `hook_paths`/`ConfigResetPlan.Paths`/`Intent.HookPaths` agree. HEAD's wipe (`handlers/system/handler.go:507-534,571-577`) is the OL dump directory plus RootDir's entries, which `hook_paths` covers. | — |
| R10-7 soak trailer | **Landed, with one new defect.** The grammar `<stage> <n\|flip>`, S6–S8 flip-sets-`release:`, landing-commit rows, deploy-log `<since>`, the allow-list file, the revert tree check and check (d) are consistent in 09, README, 14 K20 and 03. Check (d) refuses bot PRs (VER11-2). | VER11-2 |
| R10-8 moved updater | **Fixed.** 05-updater §5U.4 is HEAD's raw-asset pipeline (`internal/updater/updater.go:214-280,318-326`) plus the hotfix checks. Archive, checksums, backup, container and exec are in the deferred-build table only. 09 S8 and 14 agree. | — |
| R10-9 CI tolerance | **Fixed.** `go list -e` with the listed-embed-output tolerance is in 03 R1(b), §3.5.1 item 4 and 09. `vet-darwin` runs without `embed_frontend` in 09, 12 and 13. The 12 §12.8.1 pass-0 callback, real-rewrite assertion and commit-map comparison are present. | — |
| R10-10 `Registry.Test`, auth-off parity | **Fixed.** 05-metadata M1.3 `Test` applies Put's rule, including `auth_ref` resolution and no `Local` inheritance. `HEADAuthOffWritable` extends to integration-bound fields in 05-config §1.3.1 and §1.3.7. The `ABO_CORE_*` promise is withdrawn in 09 §9.2.6. This matches `update_service.go:770-786` and `protected_fields.go:137-165`. | — |
| R10-11 staging `binary_id`, H1/H2 scope | **Fixed.** `StagingMarker.BinaryID` is in both halves (05-storage §5S.3.7). `WriteStagingMarker` (06), 09 §9.3 steps 1–2 and the refusals and tests agree. H1/H2 delete every checkpoint, and set-asides are deferred or swept (`ErrCutoverResidue`). | — |

## B. `hello` compiles against the owner files

**Yes.** I checked every identifier in 06 §6.12's Go blocks against its owner:

- 05-rbac: `Permission`, `PermissionSpec{Name, Title, Class, DefaultGrants}`, `ClassView`, `RoleName`, `RoleViewer`,
  `Need`, `CoreAdmin`.
- 05-http-api: `Group`, `Handle[Req,Resp]`, `Route{Method, Path, Name, Perm, Summary}`, `In[Req].Req`,
  `Invalid(...FieldError) *Error`, `Field(pointer, code, message)`.
- 05-tasks: `Definition`, `Task`, `TaskSpec{Common; Run}`, `Common{Title, Help, Effects, Perms, Resume}`,
  `ReadOnly(Res(...))`, `Permissions{Start}`, `ResumeFromZero`, `RC.Context`, `CPU()`, `RunItems` returning
  `(ItemsResult, error)` with `Counters`, `ItemRC.Count`, `ItemsOptions[T]{Label}`.
- 05-storage: `Spec{Keyspace, Migrations}`, `Migration{Version, Name, Compat, Seed, Up}`, `Additive`,
  `MigrationTx.Keyspace/Set`, `Keyspace.Sub/Get/Scan`, `ScanOptions`, `KV.Clone`, `ErrNotFound`.
- 05-config: `Path`, `Section`, `Define`, `Docs`, `Handle[T].Get() *T`.
- 06: `module.Module`, `Manifest` fields, `UIDescriptor`, `Settings`/`Setting`, the `Registrar` methods,
  `Deps.Storage.Keyspace`, `Deps.Log`, `app.New`/`Run`, the `app.Options` fields hello sets, and `coretest`'s
  `New`/`WithModules`/`WithUser`/`AssertPermission`/`As`/`GetJSON`/`RunTask`/`RunResult`.
- 07: `ui.Bundle` and `ui.MustFromFS`.

Round 10 added no required field. `ServiceName`, `MetricViews` and `ServedMeterProvider` default safely, and nothing
hello uses changed signature.

## C. Findings

### VER11-1: BLOCKER. R10-3 half-fixed: the two HTTP recorders are not disjoint, so the design either fails its HEAD-parity test or fails every scrape

**Where:**
- 05-http-api §4.2 stage 2 row (`05-http-api.md:642`):
  - (a) otelgin records "gin's `NoRoute` included, with HEAD's `http_route` (`c.FullPath()`, none for `NoRoute`, as
    at HEAD)";
  - (c) "A request the router answers with no route (… an unmatched path) records no `http_route` either";
  - "their series stay disjoint by `http_route`".
- 05-http-api §8.2 (`:1211-1217`): the wrapper "passes the matched `r.Pattern` to gin". A request matching no manifest
  route "under a legacy prefix … gets 404 in the legacy error shape" from the router.
- 05-metrics §3:
  - rule 1 (`05-metrics.md:231`): "no route for gin's `NoRoute`, as at HEAD";
  - rule 3 (`:245-253`): "no route is a value set gin never produces for these statuses (gin's `NoRoute` answers 404
    or the SPA's 200)";
  - the `ErrFamilyTaken` two-scope exception (`:196-201`).
- `TestLegacySeriesLabelValuesMatchHEAD` (`05-http-api.md:1426`): a request to gin's `NoRoute` must equal HEAD's golden.
- The same claim is restated in 03 §3.6 "Metric name", 09 §9.2.1 row 12, 09 §9.2.7, 13 §13.5.1 and 14.

**Evidence:**
1. **otelgin falls back to `r.Pattern`.** otelgin v0.72.0 records `Route: c.FullPath()` (`gin.go:176`), and
   `MetricAttributes` does `if route == "" && req.Pattern != "" { route = httpRoute(req.Pattern) }`
   (`internal/semconv/server.go:392`). At HEAD gin is the server's root handler, behind only the peer allow-list
   (`server_lifecycle.go:913-915,1079`). No `ServeMux` sets `Pattern` there, so `NoRoute` has no route. In the spec the
   mux sets `r.Pattern`, and the wrapper passes it to gin on purpose for the SEC2-22 post-match check.
2. **What gin records as specified.** Experiment: a gin engine with HEAD's otelgin config sits behind a `ServeMux` with
   a `/` fallback and `GET /api/v1/operations/{p4}/{p5}`. It records:
   - SPA page loads and every `NoRoute` 404 with `http_route="/"`;
   - a generalised-pattern miss with `http_route="/api/v1/operations/{p4}/{p5}"`.

   HEAD records neither value (`static_embed.go:121`, `spa_fallback.go:115-125`). So the `NoRoute` cell of
   `TestLegacySeriesLabelValuesMatchHEAD` cannot pass from S2. Every SPA load and every 404 changes its label value.
3. **What happens once parity is forced.** To pass that test, the implementer must hand gin a request with no
   `Pattern`. gin's `NoRoute` 404 then has no route. Core's router answers its own no-route 404s with the same label set:
   - an unmatched legacy-prefix path, per §8.2;
   - the `/` fallback's `NonSPA` 404s.

   Examples of collisions:
   - `DELETE /library` (gin `NoRoute`, `spaWillNotServe`) against `DELETE /api/v1/x` (core);
   - `GET /apix` (gin's `/api` prefix rule) against `GET /api/v1/x` (core).

   Both recorders share method, status, `server_address`, port, scheme and protocol, and both carry no route.
4. **Why that fails `/metrics`.** With `WithoutScopeInfo` the exporter adds no scope label and does not merge across
   scopes (`exporter.go:255-330`). client_golang refuses a repeated name and label set (`registry.go:989-992`), and
   `promhttp` answers 500.
   - Experiment: one gin `NoRoute` 404 and one core-scope 404 with the same method gather as "collected metric … was
     collected before with the same name and label values".
   - The histogram is cumulative, so every later scrape fails until restart. This is round 9's duplicate-family
     blocker class, triggered by ordinary scanner traffic.
5. **The spec's own disjointness argument does not cover this.** Rule 3's argument covers only the pre-mount statuses
   (401/403/413/429/preflight). §4.2 (c) adds core no-route 404s, which gin's `NoRoute` also produces, as rule 3 itself
   says. `TestHTTPSeries_OneRecorderPerRequest` (`:1427`) never sends a 404.

**Fix (05-http-api §4.2 stage 2 and §8.2, 05-metrics §3, with the cross-file restatements in 03 §3.6, 09 §9.2.7 and
13):**
1. In §8.2, the wrapper hands gin a request with `Pattern` cleared. The post-match check reads the matched pattern from
   the wrapper's own context value, so otelgin's `NoRoute` records no route, as at HEAD. State otelgin's
   `req.Pattern` fallback as the reason.
2. On an app with a legacy mount, stage 2 records into the **same SDK instruments** as the mount's recorder:
   - Add `httpapi.Options.HTTPMetricsScope{Name, Version}`. ABO passes otelgin's `ScopeName` and `Version`, as data,
     so core imports nothing.
   - Use otelgin's instrument names, units and descriptions.
   - Use typed attribute values: `http.response.status_code` and `server.port` as integers.

   Any label set that both paths produce then becomes one aggregated series, and no duplicate is possible whatever the
   route values. Experiment: with the same scope and typed attributes, the colliding pair above gathers with no error.
   This also restores HEAD's single OTLP scope for the family. Delete the `ErrFamilyTaken` two-scope exception and the
   "disjoint by `http_route`" sentences.
3. Alternative to 2: a written no-route ownership rule that names which recorder owns each no-route (method, status)
   pair. For example, while a `ServesSPA` mount exists, every router 404 goes to the mount's `NoRoute`.
4. State otelgin's chain position once. 05-http-api (a) says "the engine's first middleware once the perimeter leaves
   gin", but 05-metrics rule 1 and 13 say "after … the Basic gate", which is still in gin from S2 r2 to S3.
5. Add `TestHTTPSeries_NoRouteBothRecorders` (from S2 r2, real chain): a gin `NoRoute` 404 and a router 404 with the
   same method gather without error, and the `NoRoute` series has no `http_route`. Add a 404 case to
   `TestHTTPSeries_OneRecorderPerRequest`.

### VER11-2: MAJOR. R10-7's check (d) refuses every Dependabot and workflow-bot PR, contradicting 03 §3.2 item 6

**Where:**
- 09 §9.2.1 check (d) (`09-abo-on-core.md:263-266`): a non-doc PR "with a commit that carries no `Stage-Release:` line"
  is refused. Only owner bookkeeping and admitted reverts are exempt (`:1506`).
- The trailer sources are a `commit-msg` hook, `.gitmessage`, the PR template and the `/parallel-sweep` stamp
  (`:206-216`), all of which serve human or agent authors only.
- 03 §3.2 item 6 (`03-architecture.md:229-240`):
  - Dependabot gomod gains `/core`;
  - "a workflow step runs the root `go mod tidy` on Dependabot `/core` PRs and pushes the result";
  - security updates "are opened per directory".

**Evidence:**
- `.github/dependabot.yml` opens weekly gomod, npm, docker, github-actions and gitsubmodule PRs, and
  security-update PRs, all non-doc.
- `ratchet-lower.yml:119,139` opens a PR whose commit is "chore(ci): lower ratchet baselines".
- None of these commits can carry a trailer. Dependabot writes its own messages, and an edited Dependabot PR is no
  longer rebased by Dependabot. GitHub's "Update branch" merge commit carries none either.
- `extraction-gates`, which runs `check_soak.py`, is required on every non-doc PR from S0. So from S0 every dependency
  and security update, and the 03 §3.2 item 6 root-tidy push, is refused until someone rewrites the commits by hand.
- At HEAD these PRs merge. This regression is introduced by the R10-7 fix and contradicts 03.

**Fix (09 §9.2.1 check (d), its error row, and `test_check_soak.py`):**
- Treat a commit as `Stage-Release: none` when its author is on a committed list `ci/trailer_exempt_authors.txt`
  (`dependabot[bot]`, the repo's `github-actions[bot]` for the root-tidy step and `ratchet-lower`) **and** the PR
  touches only dependency manifests, lockfiles, Dockerfile `FROM` lines, workflow action pins or ratchet baseline files.
  Any other path still needs a trailer.
- Exempt GitHub-generated merge commits whose parents are both in the PR's range.
- Owner-edit the list as bookkeeping. Cite the rule from 03 §3.2 item 6.
- Add test cases: a Dependabot gomod PR (admitted), a Dependabot PR that also touches `internal/` (refused), and a
  root-tidy bot commit (admitted).

### VER11-3: MAJOR. R10-1 half-fixed: 05-tasks still makes every other `/api/v1/operations` route a core alias, which is COR10-1's defect for 33 more routes

**Where:**
- 05-tasks 5T.3.10 (`05-tasks.md:1057-1059`): "Compat: `/api/v3/ops/*` and `/api/v1/operations*`/`/op-defs`/`/tasks`
  (except the five books routes below) are aliases onto the same handlers".
- 09 S5 Endpoints (`09-abo-on-core.md:1278`) disagrees: "under `/api/v1/operations` only the task routes 05-tasks
  5T.3.10's table names as replaced (`POST …/v2`, `DELETE …/v2/:id`, retry, finish, `pause`/`resume`); the prefix's
  other gin routes (38 in all …) … stay books' gin routes".
- 05-http-api §8.1 S6 row disagrees too: the three legacy event streams, `/api/v1/operations/events` among them, stay
  gin manifest routes.

**Evidence:**
- HEAD registers 38 routes under `/api/v1/operations` (`grep 'protected.*("/operations' internal/server/*.go`).
- Most are books-domain handlers with no core target, for example:
  - `stale`, `clear-stale`, `optimize-database`, `sweep-tombstones`, `set-internal-flag`, `audit-files`,
    `:id/class-lists/:class`, `:id/undo/preflight` and `:id/revert` (`wire_operations_routes.go:101-112`);
  - `reconcile/*`, `cleanup-version-groups` and `merge-novg-duplicates` (`server_lifecycle.go:1953-1959`).
- 05-tasks' exception list now names only the five. Read literally, everything else under the prefix becomes an
  alias, and "an alias targets a native core route of its group" (05-tasks' own sentence, `:1079-1081`). That is
  COR10-1's defect: `Build` fails, or the routes vanish.
- By 09's precedence, "on *how* a mechanism works, the owning 05-* file wins", so the broken reading is the binding
  one.

**Fix:** In 05-tasks 5T.3.10, replace the sentence with: "Compat: `/api/v3/ops/*`, `/api/v1/op-defs`, `/api/v1/tasks`
and, under `/api/v1/operations`, exactly the legacy paths the table's 'Replaces' column names are aliases onto the
table's native routes; every other `/api/v1/operations` route (33 at the evidence commit), the five operations routes
and the `events` projection among them, stays a books gin route (09 S5)." Have `TestS5_OperationsComponentStaysBooks`
assert the count of books routes under the prefix.

## D. Noted, not filed

These are MINOR, or not regressions of round 10.

- **The rank table (R10-4) misses two inner leaf mutexes.** `RevocationJournal.Append`'s per-process mutex is taken
  inside auth's identity `stage` (05-authn §4.12, 05-backup §5B.6). The books file mutex of the gap-fill step is taken
  inside the hold (05-config §1.12.2). So `TestLockRanksTotalOrder`'s "every core-side mutex on a commit path has a
  row" fails as written. I found no deadlock.
- **R10-9's justification is inaccurate.** It says the `embed_frontend`-only files "import only `embed`", which is false
  for `internal/server/static_embed.go` and its recovery test. The conclusion holds: no GOOS constraint, and the image
  job compiles them.
- **Response sizes shift.** At HEAD gin-contrib/gzip ran before otelgin, so `http_server_response_body_size` counted
  compressed bytes (`server.go:720-731`). With gzip in stage 9 it counts uncompressed bytes. This changes observation
  values, not series identity.
- **The S8 updater adds a self-test.** Its `--version-json` self-test is new, so S0's fake-asset tests "pass unchanged"
  only if the fake answers it.
- **`Registry.Test` may not cover HEAD's LAN test.** Its "unsaved body never inherits `Local=true`" leaves the legacy
  `POST /ai/test-connection` alias unspecified for a saved LAN endpoint, which HEAD tests.
