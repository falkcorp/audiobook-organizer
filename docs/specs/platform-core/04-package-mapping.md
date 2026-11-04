<!-- file: docs/specs/platform-core/04-package-mapping.md -->
<!-- version: 1.2.0 -->
<!-- guid: a52b0af6-8e36-41c8-a78d-94859f6cac27 -->
<!-- last-edited: 2026-11-04 -->

# 04 — Package mapping

Every one of the **213** packages of the root module, mapped to exactly one target bucket, with
the reason and the import-graph evidence that forces the decision. This file applies the
promotion rule of [03-architecture](03-architecture.md) §3.4 and uses its names, package tree
and rules R1–R8. Decisions are fixed by [02-decision-record](02-decision-record.md) (D1–D9,
N1–N12).

Evidence commit: `748fd787a` (the spec branch changes no code: `git diff 748fd787a HEAD` touches only
`docs/` and one `todo.d/` fragment). Raw inputs: [raw/01](../../plans/platform-core/raw/01-package-inventory.md)
(authoritative package list), [raw/02](../../plans/platform-core/raw/02-import-graph-hotspots.md),
[raw/03](../../plans/platform-core/raw/03-layering-guard.md),
[raw/04](../../plans/platform-core/raw/04-database-store.md),
[raw/05](../../plans/platform-core/raw/05-storage.md),
[raw/06](../../plans/platform-core/raw/06-tasks-ops.md),
[raw/07](../../plans/platform-core/raw/07-plugin-registry-sdk.md),
[raw/09](../../plans/platform-core/raw/09-http-api.md),
[raw/10](../../plans/platform-core/raw/10-auth-rbac.md),
[raw/11](../../plans/platform-core/raw/11-integrations.md),
[raw/12](../../plans/platform-core/raw/12-cross-cutting.md).

## 1. Summary

| bucket | packages | non-test LOC | test LOC |
|---|---|---|---|
| Core public (SDK) path | 14 | 15793 | 15105 |
| Core internal path | 4 | 22464 | 32993 |
| Core `shared/` path | 0 | 0 | 0 |
| Merge into another package | 26 | 10842 | 12937 |
| ABO app path (stays in the root module) | 158 | 620871 | 728579 |
| Delete | 11 | 34861 | 3115 |
| **total** | **213** | **704831** | **792729** |

Of the 26 merge rows, 25 merge into a core package and 1 into an ABO package. Estimated non-test LOC that ends up in core (whole core-bucket and core-merge packages minus the files their split lists keep in ABO or delete, plus the files ABO packages' split lists send to core): **72850** of 704831 (10.3%). File-level detail for every split is in the row's *Split* cell.

| bucket | generic | mixed | audiobook |
|---|---|---|---|
| Core public (SDK) path | 11 | 3 | 0 |
| Core internal path | 1 | 3 | 0 |
| Core `shared/` path | 0 | 0 | 0 |
| Merge into another package | 24 | 2 | 0 |
| ABO app path (stays in the root module) | 20 | 36 | 102 |
| Delete | 3 | 6 | 2 |

Headline decisions:

1. **`internal/database` is split, not moved.** Of `database.Store`'s 445 methods, **108** sit
   behind core service contracts (tasks 39, auth 33, storage 11, AI jobs 10, rbac 6, library 6,
   log 3) and **337** stay with the books module (314 domain + 23 legacy ops-v1). The 39 task-run
   methods keep their implementation in the residue as ABO's legacy run-row writers until
   05-tasks T8 deletes them (core reads those families in place and writes only `c/tasks/`); the
   4 `SettingsStore` methods stay with books because `setting:` is books' runtime-marker store
   (cross-cutting resolution 7). The residue is renamed `internal/books/store` (§4).
2. **No package qualifies for `mediacore/shared/` yet.** The promotion rule's second door (two real
   consumers) is closed until a second app exists; §9 lists the candidates and their triggers.
3. **`pkg/` stops exposing `database.Book`.** `pkg/ops` becomes the public `mediacore/tasks` with
   its Book-typed `Writer` moved to the books module; `pkg/plugin/sdk` is deleted (§6).
4. **`internal/models` holds nothing for core**: its live dedup-score types merge into
   `internal/dedup/unified` (§5).
5. **Four registries are deleted** (`internal/serviceregistry`, `internal/plugin`,
   `internal/maintenance`'s job registry, `internal/opscatalog`) in favour of explicit
   `module.Registrar` composition (N4, R5).
6. **Gin-based packages are re-implemented, not moved** (`internal/httputil`, `internal/realtime`,
   `internal/server/middleware`, `internal/server/handlers/opsv3`): core is `net/http` (N7). ABO's
   gin engine is mounted **permanently** through `httpapi.MountLegacy` (cross-cutting resolution 2),
   so the gin helpers the legacy mount needs (`internal/httputil`'s `parse.go`/`respond.go`,
   `internal/server`'s `gone.go`) stay in ABO for good; gin copies of concerns core takes over are
   deleted at the stage that takes them over (perimeter S2, auth S4, task API S5, SPA S9).
8. **No ABO import is ever rewired onto `mediacore/internal/...` or `mediacore/kernel`** (03 §3.4
   visibility rule, R2; MOD-2). The internal/public column is derived from the import graph: a
   package that ABO code still uses goes to a public path (`compactprogress` → `storage`, `oauth` →
   `auth/oidc`, `lifecycle` → `lifecycle`) or stays in ABO until a named stage rewires its last ABO user to
   a public surface (§8.3 lists every such gate with every ABO user that `go list -deps -test -json`
   finds, checked at every stage; the generator fails on an ungated or undeclared user, MOD2-1).
9. **Moves are keyed by [09](09-abo-on-core.md)'s stages**, not by a separate numbering (§8;
   MOD-6). 09 references these rows for its per-stage move lists instead of restating them.
7. **Three packages are dead code** with zero importers in test and non-test code
   (`internal/bookfiles`, `internal/writeback`, and `internal/operations/freshness`); the first two
   are deleted, the third merges into `tasks` because a consumer is already documented.

## 2. Method and rules applied

### 2.1 Package set and cross-check

- `go list ./...` with default tags at the evidence commit returns **213** packages; the set equals
  raw/01's inventory and the 213 keys of `layerOf` (`internal/arch/layering_test.go:59-286`). No
  package exists only under a build tag (raw/01 "Build-tag-only packages").
- LOC, layer and class come from raw/01 and were recomputed from `go list -json` at the same
  commit: every value matches. **Importer counts are generated, once** (MOD2-1): the `in/out`
  column, the "in N, out M" prefix of every evidence cell, every "N importers" phrase, §8.2 and
  §8.3 all come from one parse of `go list -deps -test -json ./...` at the evidence commit. `in` is
  the number of root-module packages that import the package from any file, test files included
  (so it can exceed raw/01's non-test in-degree); `out` is its non-test in-module imports.
- A helper script kept outside the repository builds the tables from one mapping file plus that
  `go list` output and a per-file parse of the same packages (imports, top-level declarations,
  struct fields, methods, unresolved identifiers), and **fails** unless:
  (a) the mapping covers exactly the 213 packages `go list ./...` returns; (b) every file named in a
  *Split* cell is a non-test file of that package, named once;
  (c) **MOD-2 at every stage** (R2-5): for each stage point of 09 in order — both legal positions of
  UI-0 (after S2, or after S3) and 05-tasks T8 at its earliest (right after T7) — no file still in
  ABO at that point imports a path that is non-importable by then (`mediacore/internal/...`,
  `mediacore/kernel`), and every ABO user found is declared in that gate with how it is rewired
  (§8.3);
  (d) **R1 at every stage**: no file that has landed in core by a stage point imports ABO code at
  that point unless the row states how the import is cut (§8.5, or a re-implemented part, §8.6);
  (e) **splits type-check** (MOD2-3): a part that moves verbatim shares no unexported name, no
  struct field or method access and no method on a foreign type with another part of its package;
  parts that are re-implemented are listed with the generated count of staying-side references the
  stage PR rewrites (§8.6);
  (f) every core path named in §8 exists in 03 §3.3 or in a §3 row (MOD2-2).
  The tables below are its output. Tooling can re-check completeness against the alphabetical
  index in §11.

### 2.2 Buckets

The bucket records **what happens to the package's current import path**:

| bucket | meaning |
|---|---|
| core public | the path leaves the root module; the bulk becomes one public (SDK) core package |
| core internal | the path leaves; the bulk becomes one `mediacore/internal/...` package |
| core `shared/` | the path leaves; it becomes a `mediacore/shared/...` package |
| merge | the path leaves; its content is absorbed into a package whose main identity is something else (core or ABO) |
| ABO app path | the path stays in the root module, unchanged or renamed as stated; parts may still be extracted to core, listed in *Split* |
| delete | the path leaves and nothing replaces it as a package (retired mechanism or dead code) |

A *Split* cell lists every file that leaves a package, by destination, with its LOC. Files not
named stay where the bucket says. A split part is either a **move** (the files change package
verbatim, apart from import paths, and must type-check on both sides, §2.1 (e)) or a
**re-implementation** (the content is rewritten against core contracts in the stage PR, the ABO
files are deleted then, and the staying side's references are rewritten in the same PR; §8.6 lists
every such part). *Split* cells are not the input of core's history path file: 12 §12.8.1 derives it
from real history (pure renames only), so a re-implemented part keeps only its post-arrival history
(MOD2-12). Where a single file must itself be divided (for example
`internal/database/pebble_store.go`, `internal/config/config.go`), the cell says so and §4 / §7
give the cut line. A *mixed* package (raw/01 class) with no *Split* cell stays wholly in ABO: its
mixed-ness is a dependency on books types (`database.Book*` etc.), and those types stay in ABO too.

### 2.3 Promotion rule, as applied

03 §3.4's rule is non-circular: a package is **built** in core only if ABO consumes it during 09's
stages or a built core package needs it. A package (or part) of the root module therefore goes to
core only if one of these holds:

| test | source | used for |
|---|---|---|
| P1 — it implements a built core package of 03 §3.3 (Build column "ABO" or "core") | 03 §3.4 (a) | storage, tasks, events, log, metrics, config, secrets, httpapi, realtime, auth, rbac, audit, library, integrations (ai, metadata, download/deluge, notify mail+webhook, providerhttp), backup, updater, health, ui, kernel, archtest, coretest |
| P2 — it has two real consumers across apps | 03 §3.4 | none today (one app) |
| P3 — a built core package needs it | 03 §3.4 (b) | e.g. email normalization (`internal/util/email.go`) is needed by core auth; Range serving is needed by core httpapi |

No row maps to a **deferred-build** package (`acquire`, `arrfacade`, `integrations/indexer`, the
download grab/monitor pipeline, non-Deluge download adapters): ABO has no code for them, so they
appear in no stage here.

Packages that are domain-free but fail all three stay in ABO: `internal/cache`, `internal/flight`,
`internal/keyedmutex`, `internal/errhandling`, `internal/tools`, `internal/querygrammar`,
`internal/mtls`, `internal/cfaccesspolicy`, `internal/cfservicetoken`, `internal/wirecontract`,
`internal/applycap`. (`internal/keyedmutex` was moved into core only by build experiment E2 in 03
§3.2.1, which tests the module mechanism; 03 §3.2.1 records that it is not a placement decision.)
`internal/appdirs` and `internal/telemetry/contract` stay for a different reason: they import or walk
ABO code (MOD-21, MOD-6).

Where raw/01's class was wrong for this decision, the row says so: `internal/server/absauth` is
classed audiobook-specific but holds the web login's password hashing, throttle and auth audit
type (used by `internal/server/handlers/auth.go`, `user.go`, `internal/server/bootstrap.go`); `internal/serverdecode`
is classed generic but is the in-process *audio* decoding switch.

### 2.4 New root-module packages created by splits

| new path | holds |
|---|---|
| `internal/books` | the module (`books.Module()`), its task `Resource` declarations and subject resolvers (from `internal/operations/registry/deps.go`, `types.go`), schedule declarations (from `internal/scheduler/tasks.go`), selection helpers (from `internal/operations/selection.go`), `PageBooks` (from `pkg/plugin/sdk/iterate.go`), task-kind aliases (from `internal/opscatalog/tombstones.go`), the books typed `Writer` contract (from `pkg/ops/writer.go`) |
| `internal/books/store` (+`storetest`, `aggtest`) | rename of the `internal/database` residue (§4) |
| `internal/books/writer` | rename of the `internal/operations/opswriter` residue |
| `internal/telemetrypolicy` | rename of the test-only `internal/telemetry/contract` suite, plus `internal/telemetry/ratchet_test.go` (both walk or import ABO code; MOD-6) |
| `internal/cfaccess` | the Cloudflare Access identity adapter and owner proof, consolidated from `internal/oauth/cfaccess.go`, `internal/server/middleware/{cfaccess,cfbounce,owner_guard}.go`, `internal/auth/owner.go`, `internal/server/{cf_recovery,guest_service_tokens,invite_guest_access,owner_routes}.go` (G11: deployment-specific, behind core auth's authenticator extension point) |

## 3. Mapping tables

Columns: current package (anchor target of §11), `layerOf` layer, non-test LOC, raw/01 class
(generic / mixed / audiobook), non-test in-degree / out-degree, target, reason, evidence. Evidence
for rows without hand-written text is computed from the import graph (audiobook-class imports
first, then importers).

### 3.1 Core public (SDK) path (14 packages, 15793 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|
| <a id="p-internal-aidispatch"></a>`internal/aidispatch` | 3 | 1802 | mixed | 7/2 | mediacore/integrations/ai (routing, endpoint health, inflight, attribution) | Generic capability->endpoint router with cooldowns; capability IDs become per-module registrations. | in 7, out 2. imports `logger`, `telemetry`; `Call[T]` `dispatch.go:347`; process-global `defaultHealth` `health.go:46` (R5); 10 audiobook capability IDs registered at import `capabilities.go:67-95,176,185`.<br>**Split:** **mediacore/integrations/ai** ← `attribution.go`, `classify.go`, `dispatch.go`, `errors.go`, `health.go`, `inflight.go`, `locality.go`, `metrics.go` (1498 LOC); **internal/ai (ABO capability IDs; registration mechanism -> core)** ← `capabilities.go` (304 LOC) |
| <a id="p-internal-auth"></a>`internal/auth` | 3 | 610 | generic | 20/1 | mediacore/auth (identity context, credential methods); permissions + seed roles -> mediacore/rbac | Small, dependency-light identity core (raw/10 'positive building blocks'). The closed permission list and seed roles become `rbac` registrations (G1-G3). Owner proof is Cloudflare-Access-specific (G11) and stays in ABO. | in 20, out 1. imports `internal/database` only for `User`/`Role` and two seed-store interfaces (`seed.go:20-30`); closed list `permissions.go:91-129`; 13 atoms `permissions.go:24-85`; seed overwrite `seed.go:86-129`; owner proof `owner.go`; 20 importers.<br>**Split:** **mediacore/auth** ← `context.go`, `user_active.go` (202 LOC); **mediacore/rbac** ← `permissions.go`, `seed.go` (300 LOC); **internal/cfaccess (ABO, owner proof)** ← `owner.go` (108 LOC) |
| <a id="p-internal-backup"></a>`internal/backup` | 3 | 1582 | generic | 6/3 | mediacore/backup | Consistent checkpoint-based archive of the DB directory with codecs, retention and verification; no book logic. Must grow to every core store and module-declared artifacts (03 §3.3). | in 6, out 3. imports `diskstats`, `logger`, `safepath`; `Checkpointable` `backup.go:28-33`; dead SQLite branches `backup.go:959,1028,1103` (delete); restore does not reopen the live store `internal/server/handlers/system/handler.go:823-905` (raw/05 §5). |
| <a id="p-internal-lifecycle"></a>`internal/lifecycle` | 0 | 35 | generic | 3/0 | mediacore/lifecycle (`ErrShutdown` cancellation cause) | Shutdown-vs-cancel sentinel consumed by the executor and scheduler. 03 §3.3 defines the public L0 package `lifecycle` (shutdown types used by kernel and tasks), and `internal/aiscan` stays in ABO, so the target is public (MOD-6, MOD-2). | in 3, out 0. `ErrShutdown`/`IsShutdown` `shutdown.go:33`; importers `aiscan`, `registry`, `scheduler`. |
| <a id="p-internal-logging"></a>`internal/logging` | 0 | 529 | generic | 13/0 | mediacore/log | slog-native context helpers, op context, sanitizers and path redaction: the shape 03 §3.3 prescribes for `log`. | in 13, out 0. no in-module imports (raw/02 a3: zero closure); `structured.go:47-68`, `opcontext.go:15`, `sanitize.go:28,38`, `redact_path.go:107` (invite-token redaction; invites are core auth). |
| <a id="p-internal-mail"></a>`internal/mail` | 0 | 364 | generic | 3/0 | mediacore/integrations/notify/mail | SMTP `Sender`; fully generic (raw/11 §6). | in 3, out 0. no in-module imports; `Sender` `mail.go:60-62`; `SMTPSender` `mail.go:85`. |
| <a id="p-internal-metadata-providerhttp"></a>`internal/metadata/providerhttp` | 3 | 417 | generic | 4/1 | mediacore/integrations/providerhttp | Per-provider throttled HTTP clients; must become instance-scoped (03 §3.3). | in 4, out 1. imports `logger`; package-global limits/clients `providerhttp.go:40,60-81` (raw/12 §9). |
| <a id="p-internal-oauth"></a>`internal/oauth` | 3 | 827 | generic | 4/3 | mediacore/auth/oidc (OIDC/OAuth providers, public API; 05-authn §11) | GitHub OAuth2, Google OIDC, signed PKCE state and user linking are generic SSO (03 §3.3 `auth`: OIDC/OAuth). ABO's ABS OpenID flow (`internal/server/handlers/abs/openid.go`, `internal/server/absauth`) stays in ABO and uses `Config`, `New`, `IdentityClaims` and `ParseAllowedEmails`, so the providers go to the public `auth` package, not `internal/` (MOD-2). The Cloudflare Access JWT verifier is deployment-specific (G11). | in 4, out 3. `Provider` `providers.go:23-27` (2 impls `:37,:119`); `resolve.go:32,65`; `state.go:55-132`; CF verifier `cfaccess.go:68-97`; imports `database`, `logger`, `util` (email normalization).<br>**Split:** **internal/cfaccess (ABO)** ← `cfaccess.go` (132 LOC) |
| <a id="p-internal-plugins-webhook"></a>`internal/plugins/webhook` | 4 | 162 | generic | 1/1 | mediacore/integrations/notify/webhook | Generic HMAC webhook notifier; becomes a notify adapter subscribed to core events. | in 1, out 1. imports only `internal/plugin` (`plugin.go:33`); brand header names `X-Audiobook-Event`/`-Signature-256` `plugin.go:124,126` -> app-supplied prefix. |
| <a id="p-internal-realtime"></a>`internal/realtime` | 5 | 426 | generic | 2/0 | mediacore/realtime (+ mediacore/internal/sse) | SSE hub with client subscriptions; core makes it resumable and permission-filtered (03 §3.3), off gin. The books-changed coalescer generalizes to an entity-change coalescer keyed by `entity.Ref` (03 §3.3). | in 2, out 0. no in-module imports; gin `HandleSSE` (raw/12 §5); `EventBooksChanged` `book_changes.go:18`; importers `itunes/service`, `server`.<br>**Split:** **mediacore/realtime** ← `events.go`, `book_changes.go` (426 LOC) |
| <a id="p-internal-telemetry"></a>`internal/telemetry` | 0 | 1363 | generic | 10/0 | mediacore/metrics | OTel tracer/meter/log providers, views, attribute allow-list, endpoint validation: platform metrics (03 §3.3). Must become per-`App` instances (R5). Its repo-walking test `ratchet_test.go` (`TestRatchet_ScansRealTree`, `ratchet_test.go:310`) stays in ABO, in `internal/telemetrypolicy` (MOD-6). | in 10, out 0. no in-module imports; `otel.SetTracerProvider`/`SetMeterProvider` `telemetry.go:287,461` and `instanceID` `resource.go:31` are process globals (raw/12 §9); scope prefix brand `meter.go:17`. |
| <a id="p-internal-updater"></a>`internal/updater` | 3 | 547 | generic | 1/2 | mediacore/updater | Self-update mechanism; the release source must be a parameter. | in 1, out 2. repo slug hard-coded `updater.go:36,68-76`; imports `config`, `serviceregistry` (`register.go:60,76` -> delete).<br>**Split:** **mediacore/updater** ← `updater.go`, `scheduler.go` (453 LOC); **delete (serviceregistry)** ← `register.go` (94 LOC) |
| <a id="p-pkg-ops"></a>`pkg/ops` | 3 | 3073 | mixed | 8/2 | mediacore/tasks | The v3 authoring model (Task/Batch/Fixer/Pipeline, Schedule, Effects, CancelContract, RC) is domain-neutral and becomes the public `tasks` API (N9). Its Book-typed `Writer` is the only audiobook edge and leaves. | in 8, out 2. imports `internal/auth` (`approval.go:11`, `definition.go:20,93`) and `internal/database` (`writer.go:11`; `Writer` methods `ModifyBook`/`ModifyBookFile`/`RepointBookFile`/`ModifyBookAuthors` `writer.go:46-90`); `SubjectType` book/book_file `writer.go:112-121`; `LaneMac` `lanes.go:15`; omitted `Concurrency` defaults to `CPU()` `batch.go:123` (R7 tightens this to no zero value).<br>**Split:** **internal/books (typed books Writer contract; `tasks.Writer` becomes generic over `Resource`)** ← `writer.go` (148 LOC) |
| <a id="p-pkg-ops-opstest"></a>`pkg/ops/opstest` | 4 | 4056 | mixed | 0/6 | mediacore/tasks/taskstest | Conformance suite, fault injection and fake world for task definitions; every app needs it. | in 0, out 6. imports `opswriter`, `registry`, `state`, `pkg/ops`, `database`, `logger`; Book fakes `fakes.go:74,83,93` (raw/02); no importer in `go list -deps -test` (`tools/cmd/oplint/analyzer_test.go:35` names it only as an `analysistest` fixture path string).<br>**Split:** **internal/books/store/storetest (Book-typed fakes)** ← `fakes.go` (613 LOC) |

### 3.2 Core internal path (4 packages, 22464 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|
| <a id="p-internal-operations-registry"></a>`internal/operations/registry` | 4 | 15137 | mixed | 39/14 | mediacore/internal/executor (engine; public surface mediacore/tasks) | The production v2 executor (dispatcher gates, workers, watchdog, resume, cancel, pause gate, RunItems) becomes core's executor. 31 root packages call its API (`RunItems`, `Reporter`, `OperationDef`, `EnqueueOption`, `Registry`), so the **engine** moves at S5 (05-tasks T0) while this path stays in ABO as the v2 adapter re-implemented over public `tasks` (05-tasks `opsbridge`), and is deleted when 05-tasks T8 removes its last ABO importer (MOD-2 gate). Audiobook residue: book-field `Requires` predicates, the `Resource` constant list, `GetBookByID` dep adapter. | in 39, out 14. imports `auth`, `database`, `logger`, `metrics`, `opsmetrics`, `sdnotify`, `serviceregistry`, `pkg/ops`; `database.Book` predicates `deps.go:53,78,84-125`; `Resource` consts `books/book_files/authors/series/catalog/...` `types.go:384-398`; v1 subject `book` `types.go:417-420`; `New(store, slog.Default(), 8, hub)` `register.go:98`; 39 importers; RunItems `Concurrency` clamp `run_items.go:155-157`.<br>**Split:** **internal/books (subject resolvers, Resource declarations, dep adapter)** ← `deps.go` (311 LOC); **delete (serviceregistry wrapper)** ← `register.go` (131 LOC) |
| <a id="p-internal-scheduler"></a>`internal/scheduler` | 4 | 4249 | mixed | 5/16 | mediacore/internal/scheduler (public `tasks.Schedule`; moves when 05-tasks T7 rewires its last ABO importer) | One schedule mechanism for core (durable interval clock, daily-at, cron, maintenance window, v3 schedule loop). The 38 ABO `TaskDefinition`s and the metadata-upgrade extra op are books declarations; `OperationDef.Schedule` cron strings become live `tasks.Schedule` values (raw/06 §7.5). | in 5, out 16. imports `dedup` (`tasks.go:21`), `scanner` (`tasks.go:24`), `metabatch`/`metafetch` (`extra_ops.go:25-26`) -> 80 transitive deps, 39 audiobook (raw/02 finding 6); durable clock `interval_clock.go:60,146`; v3 loop `v3_schedule.go:193,622`.<br>**Split:** **mediacore/internal/scheduler** ← `cron.go`, `daily_at.go`, `interval_clock.go`, `maintenance.go`, `scheduler.go`, `v3_schedule.go` (2268 LOC); **internal/books (schedule declarations)** ← `tasks.go`, `extra_ops.go`, `full_sweep.go` (1981 LOC) |
| <a id="p-internal-sdnotify"></a>`internal/sdnotify` | 0 | 149 | generic | 5/0 | mediacore/internal/sdnotify (behind `lifecycle` readiness and start-timeout hooks) | systemd notify protocol used by the kernel lifecycle and long migrations. It moves at S2 and **all five** ABO importers are rewired in that stage (MOD2-1, §8.3): `.` (`--print-capabilities` fast exit, `main.go:53`) → `app`; `internal/server` (READY/STATUS/STOPPING, `server_lifecycle.go:340-343,552`) → the kernel owns readiness; `internal/database` (`startup_notify.go:24`) and `internal/search` (`register.go:55`) → the kernel's `ExtendStart` hook (`func(step string)`, 05-health §5H.3.4) handed to the books module; `internal/operations/registry` (`subprocess.go:70`, child-env scrub) → the public constant `lifecycle.NotifySocketEnv`. | in 5, out 0. stdlib only (`sdnotify.go`); importers `.`, `database`, `registry`, `search`, `server`. |
| <a id="p-internal-server-handlers-opsv3"></a>`internal/server/handlers/opsv3` | 5 | 2929 | mixed | 1/9 | mediacore/internal/taskhttp (native `/api/core/v1/tasks`; `/api/v3/ops` alias in ABO) | The cleanest existing API (own package, injected authz, resumable SSE): becomes core's task API (raw/09 §8.4). The repairs-specific access rule becomes a books authz hook. | in 1, out 9. imports `repairs` (`access.go:15`), `database.WorkerProgress` (`view.go:32,69,102`), gin; SSE resume `events.go:490-640`; 12 routes (raw/09 §2.3). |

### 3.3 Core `shared/` path (0 packages, 0 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|

### 3.4 Merge into another package (26 packages, 10842 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|
| <a id="p-internal-ai-aijobs"></a>`internal/ai/aijobs` | 3 | 575 | generic | 3/2 | mediacore/integrations/ai (durable provider batch jobs) | Provider batch-API job lifecycle is a generic AI capability; its callback registry becomes module registration (R5). | in 3, out 2. imports `database` (AIJobsStore), `logger`; `Register` `aijobs.go:89`; `BatchClient` `aijobs.go:31-36`. |
| <a id="p-internal-ai-resultjournal"></a>`internal/ai/resultjournal` | 3 | 263 | generic | 5/2 | mediacore/integrations/ai (content-keyed result journal) | Durable cache of finished AI results; generic; stores through a keyspace. | in 5, out 2. imports `database` (RawKV), `logger`; `KVStore` `journal.go:81`; family `aijournal:` (raw/05 §3). |
| <a id="p-internal-boundedcall"></a>`internal/boundedcall` | 0 | 120 | generic | 2/0 | mediacore/library (time-bounded uninterruptible calls on stuck mounts) | Guarding blocking syscalls on stuck mounts is part of safe filesystem access. | in 2, out 0. stdlib; `boundedcall.go:120`; importers `plugins/maintenance`, `repairs`. |
| <a id="p-internal-compactprogress"></a>`internal/compactprogress` | 3 | 359 | generic | 3/2 | mediacore/storage (compaction progress reporting; public `storage.Admin` statistics) | Reports Pebble compaction progress. `internal/metafetch`, `internal/plugins/maintenance` and `internal/server` stay in ABO and use `Stats`, `Step` and `RunStep`, so it goes to the public `storage` package, not `internal/pebble` (MOD-2). | in 3, out 2. imports `database` (`stats.go:22`, CompactionStats) + `logger`; importers `metafetch`, `plugins/maintenance`, `server`. |
| <a id="p-internal-diag-flight"></a>`internal/diag/flight` | 3 | 659 | generic | 2/1 | mediacore/health (flight recorder in the diagnostics bundle) | Runtime trace flight recorder written on incidents: health diagnostics. It imports ABO's `internal/logger` (`flight.go:108`, `var flightLog = logger.New("flight-recorder")`), which stays in ABO, so the move **rewrites** it to take a `*slog.Logger` (a `log` child logger) through its constructor, with no package-level logger (R1, R5; MOD2-11). | in 2, out 1. imports `logger` only (`flight.go:108`); `flight.go:659`; importers `server`, `handlers`. |
| <a id="p-internal-diskstats"></a>`internal/diskstats` | 0 | 24 | generic | 2/0 | mediacore/library (free-space checks) | Free-space check; used by core backup and library. | in 2, out 0. stdlib; `diskstats_unix.go`; importers `backup`, `server`. |
| <a id="p-internal-libroot"></a>`internal/libroot` | 3 | 215 | generic | 2/1 | mediacore/library | Library-root trust checks (mounted, owned) every library-wide op makes: `library` Root semantics. | in 2, out 1. `libroot.go:189`, `owner_unix.go:26`; imports `util`; importers `maintenance/jobs`, `plugins/maintenance`. |
| <a id="p-internal-logger-logtest"></a>`internal/logger/logtest` | 0 | 135 | generic | 11/0 | mediacore/coretest (slog capture) | Captures slog output; a core test-kit concern. | in 11, out 0. 24 test importer files; no in-module imports. |
| <a id="p-internal-mail-mailtest"></a>`internal/mail/mailtest` | 3 | 48 | generic | 2/1 | mediacore/coretest (recording notifier) | Recording fake sender for tests. | in 2, out 1. imports `mail`; 2 test importer files. |
| <a id="p-internal-metadata-dailyquota"></a>`internal/metadata/dailyquota` | 0 | 348 | generic | 4/0 | mediacore/integrations/providerhttp (daily quota) | Per-provider daily budget; part of the outbound resilience stack. | in 4, out 0. no in-module imports; `For(provider)` global `dailyquota.go:333`; `Install` from `init()` in `internal/metadata/google_quota.go:61`. |
| <a id="p-internal-models"></a>`internal/models` | 0 | 337 | mixed | 5/0 | internal/dedup/unified (dedup score types); `Audiobook` -> internal/itunes | Only the dedup-score type family is live; it is book-pair dedup evidence. Book DTOs are dead except `Audiobook` (one user). Nothing belongs in core (raw/02 §b). | in 5, out 0. 5 importers: `internal/database` (`embedding_store.go:22`), `internal/dedup/unified` (`score.go:23,106,127,135,144` aliases), `internal/itunes` (`import.go:361,394`), `internal/plugins/dedup` (`calibrate_composite.go:180,778`); generic DTOs `audiobook.go:109-144` have 0 users.<br>**Split:** **internal/dedup/unified** ← `dedup_score.go` (188 LOC); **internal/itunes (`Audiobook` only; other types deleted)** ← `audiobook.go` (149 LOC) |
| <a id="p-internal-operations"></a>`internal/operations` | 3 | 379 | generic | 15/1 | mediacore/tasks (trigger source, v1-era progress types); selection -> internal/books | Shared v1-era op types. Trigger source and `ProgressReporter` are task concerns; `SelectionSpec`/`ResolveBookIDs` select books. | in 15, out 1. `ProgressReporter` `progress.go:24`; `ResolveBookIDs` `selection.go:16`; trigger source `trigger_source.go:25,31`; v1 checkpoint helpers `state.go:60,78`; 15 importers.<br>**Split:** **mediacore/tasks** ← `progress.go`, `state.go`, `trigger_source.go` (306 LOC); **internal/books** ← `selection.go`, `types.go` (73 LOC) |
| <a id="p-internal-operations-childop"></a>`internal/operations/childop` | 3 | 158 | generic | 4/2 | mediacore/tasks (parent follows child run) | Parent/child run liveness is executor behaviour. | in 4, out 2. imports `database` (v2 run reader) + `state`; `Follow` `follow.go:46,101`. |
| <a id="p-internal-operations-freshness"></a>`internal/operations/freshness` | 0 | 50 | generic | 0/0 | mediacore/tasks (per-item freshness stamps) | Domain-free per-item stamp primitive. Zero importers today, but a consumer is documented (`internal/plugins/maintenance/chapters_backfill.go:40-52`: wire freshness before scheduling that op); belongs with tasks rather than being deleted and re-invented. | in 0, out 0. no importer, no in-module import; `freshness.go:31`. |
| <a id="p-internal-operations-opmode"></a>`internal/operations/opmode` | 0 | 97 | generic | 10/0 | mediacore/tasks (preview/live mode resolution) | Every writer defaults to preview: a tasks contract. | in 10, out 0. no in-module imports; `dryrun.go:8-36,66`; 10 importers. |
| <a id="p-internal-operations-state"></a>`internal/operations/state` | 0 | 578 | generic | 11/0 | mediacore/tasks (public `State`, v3 ten-state machine) | Run-status vocabulary and transition table; public because handlers, UI generators and apps classify runs. | in 11, out 0. no in-module imports; `state.go:29,150-183`, `v3.go:20-31,187,289,344`; 11 importers incl. `tools/cmd/opsgen`. |
| <a id="p-internal-opsmetrics"></a>`internal/opsmetrics` | 3 | 525 | generic | 5/2 | mediacore/tasks (task-run instruments; ABO's existing instrument names are kept through its metric prefix, SIMP-13) | OTel instruments for task runs with a closed def-id label set: a tasks concern. Core names new apps' instruments `mediacore_tasks_*`; ABO's `audiobook_organizer.ops.*` names do not change (03 §3.6 metric row). | in 5, out 2. imports `operations/state`, `telemetry`; instrument names `audiobook_organizer.ops.*` `opsmetrics.go:12-15,114`; `RegisterDefID` `defids.go:24`. |
| <a id="p-internal-security-pathvalidation"></a>`internal/security/pathvalidation` | 0 | 255 | generic | 7/0 | mediacore/library (path confinement; superseded by os.Root where possible) | Path traversal validation is library confinement. | in 7, out 0. stdlib; `pathvalidation.go:255`; 7 importers. |
| <a id="p-internal-security-safehttp"></a>`internal/security/safehttp` | 0 | 267 | generic | 2/0 | mediacore/integrations/providerhttp (SSRF-safe client for provider-supplied URLs) | Outbound-HTTP safety for third-party URLs belongs to the adapter HTTP layer. | in 2, out 0. stdlib; `safehttp.go:267`; importers `covers`, `metadata`. |
| <a id="p-internal-security-safepath"></a>`internal/security/safepath` | 0 | 51 | generic | 6/0 | mediacore/library | Path-confinement helper used by backup and file ops. | in 6, out 0. stdlib; `safepath.go:51`; 6 importers incl. `backup`. |
| <a id="p-internal-server-middleware"></a>`internal/server/middleware` | 5 | 3247 | mixed | 10/11 | mediacore/auth + mediacore/httpapi (generic middleware); ABS files -> internal/server/absauth; Cloudflare files -> internal/cfaccess | `RequireAuth`, permission checks, credential/owner guards, rate limiting, body limits and log/trace redaction are the core request chain (raw/09 §8.2), re-implemented on net/http. The gin copies of the perimeter concerns are deleted at S2, when core's perimeter middleware wraps the legacy mount; the gin auth chain is deleted at S4, when the `legacyauth` bridge (new ABO code in `internal/server`) reads the core principal inside gin (cross-cutting resolution 2). ABS and Cloudflare Access binders are app/deployment specific. | in 10, out 11. imports `server/absauth` (`absauth.go:19`), `fingerprint/workerapi` (`request_size.go:13`); `RequireAuth` `auth.go:152-262`; role-name admin check `auth.go:522-537` (G4); worker confinement `auth.go:271-306`; 10 importers.<br>**Split:** **mediacore/auth (authn/authz middleware)** ← `auth.go`, `credential_guard.go`, `actor.go`, `basicauth.go` (803 LOC); **mediacore/httpapi** ← `ratelimit.go`, `request_size.go`, `log_redact.go`, `recovery_redact.go`, `trace_redact.go` (873 LOC); **internal/server/absauth (ABO)** ← `absauth.go`, `absauthprobe.go` (948 LOC); **internal/cfaccess (ABO)** ← `cfaccess.go`, `cfbounce.go`, `owner_guard.go` (623 LOC) |
| <a id="p-internal-startup"></a>`internal/startup` | 0 | 211 | generic | 3/0 | mediacore/kernel (startup step classes) + mediacore/health (degraded tokens) | The reviewed Fatal/Degraded step table is the kernel supervisor's contract; modules declare their own steps through `module.Registrar`. `internal/server` builds the table today, so the move happens at S2, when the server lifecycle moves into the kernel (MOD-2 gate). | in 3, out 0. stdlib-only leaf; `steps.go:35-118`; importers `cmd` (test only, `commands_test.go`), `internal/server`, `internal/server/handlers/system` (test only). |
| <a id="p-internal-walkresume"></a>`internal/walkresume` | 0 | 242 | generic | 4/0 | mediacore/tasks (yield/resume helpers) | Exists only because layer-3 packages could not import the registry (`walkresume.go:5-17`); with tasks below every ABO layer the seam is unnecessary and its `ErrYield`/checkpoint cadence become tasks helpers. | in 4, out 0. no in-module imports; importers `itunes`, `plugins/maintenance`, `remux`, `server`; `CheckpointEvery = 10` (raw/06 §1.7). |
| <a id="p-internal-watcher"></a>`internal/watcher` | 3 | 457 | generic | 1/3 | mediacore/library (root watching) | fsnotify supervision of media roots is a library concern; the extension filter becomes a module-supplied predicate. | in 1, out 3. imports `appdirs`, `config`, `pathutil`; `config.SupportedExtensionSet()` `watcher.go:243` (raw/02 a2); `fsnotify` dependency. |
| <a id="p-tools-cmd-oplint"></a>`tools/cmd/oplint` | 6 | 998 | generic | 0/0 | mediacore/archtest/tasklint | Lints task bundles (unbundled-def, writer-bypass, store-in-deps, missing-help, sequential-empty-reason, deletes-owner-review); these rules govern every app's `tasks` definitions, so they are a `tasks` platform concern delivered through `archtest` (apps run it from their own tests). | in 0, out 0. analyzer `tools/cmd/oplint/analyzer.go`; hard-codes `modulePath` (`analyzer.go:45`) and ~12 fixtures under `tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/...` (raw/02 §e) -> becomes a parameter; test imports `pkg/ops/opstest` (`analyzer_test.go:35`). |
| <a id="p-tools-cmd-opsgen"></a>`tools/cmd/opsgen` | 6 | 244 | generic | 0/1 | mediacore/internal/openapi | Generates TS status enums from `operations/state`; in core the task states are OpenAPI enums and `@mediacore/api-client` is generated from the core OpenAPI document (03 §3.3), so the generator is subsumed. | in 0, out 1. writes `web/src/generated/ops.ts` from `internal/operations/state/{state,v3}.go` (`tools/cmd/opsgen/main.go:6,28`, raw/09 §5). |

### 3.5 ABO app path (stays in the root module) (158 packages, 620871 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|
| <a id="p-root"></a>`.` | 6 | 86 | mixed | 0/6 | . (unchanged) | The ABO `main`; becomes the composition root that calls `app.New(ctx, opts, books.Module())` (03 §3.7). | in 0, out 6. imports `internal/database` (`main.go:13`), `internal/server` (`main.go:16`), registry, sdnotify, telemetry. Its edge `. -> cmd` (`main.go:12`) is the root-module in-module positive control (03 §3.5.1). |
| <a id="p-cmd"></a>`cmd` | 6 | 1876 | mixed | 1/14 | cmd (unchanged; no file moves) | Cobra CLI of ABO; the CLI and cobra stay in ABO (03 §3.5.1, 06, 13 §13.5.1). No file of `cmd` moves (MOD2-4): `serve`'s store/OTel/slog bootstrap in `root.go` is **replaced** by `app.New`/`app.InstallProcessDefaults` calls at S2 and its flag wiring moves onto `app.Options`; `rootCmd`, `SetVersion`/`Execute` (`root.go:92,462-467`, called by `main.go:23,31`) and `guardAgainstKeyRegeneration` (`root.go:760`) stay. `cmd/` is not in core's history path file (12 §12.8.1). | in 1, out 14. imports `internal/fingerprint` (`cmd/fp_worker.go:24`), uses `database.BookCore/Author/Series` (`cmd/diagnostics.go:116`); `cmd/root.go:280` opens the store, `:718` calls `slog.SetDefault` (R5: replaced by `app.InstallProcessDefaults`). Build-tagged files that stay with it: `dedup_bench.go`, `dedup_bench_batch.go`, `dedup_bench_crossval.go`, `dedup_bench_pass2.go`, `dedup_bench_prompts.go`, `dedup_bench_runner.go`, `dedup_bench_status.go`, `dedup_bench_summary.go`, `dedup_bench_types.go` (`//go:build bench`, `rootCmd.AddCommand` at `dedup_bench.go:63`), `umask_windows.go`. |
| <a id="p-cmd-itl-audit-encoding"></a>`cmd/itl-audit-encoding` | 6 | 90 | audiobook | 0/1 | cmd/itl-audit-encoding (unchanged) | iTunes maintenance CLI; audiobook-only. | in 0, out 1. imports `internal/itunes` only (raw/01). |
| <a id="p-cmd-itl-check"></a>`cmd/itl-check` | 6 | 89 | audiobook | 0/1 | cmd/itl-check (unchanged) | iTunes maintenance CLI; audiobook-only. | in 0, out 1. imports `internal/itunes` only (raw/01). |
| <a id="p-cmd-itl-diff"></a>`cmd/itl-diff` | 6 | 319 | audiobook | 0/1 | cmd/itl-diff (unchanged) | iTunes maintenance CLI; audiobook-only. | in 0, out 1. imports `internal/itunes` only (raw/01). |
| <a id="p-cmd-mtls-bridge"></a>`cmd/mtls-bridge` | 6 | 369 | generic | 0/1 | cmd/mtls-bridge (unchanged) | Dev tool bridging an MCP stdio protocol between hosts; not a platform concern (03 §3.3 list) and one consumer. | in 0, out 1. only importer of `internal/mtls` (`cmd/mtls-bridge/main.go:12`; raw/09 §7: not used by the HTTP server). |
| <a id="p-cmd-pebble-inject-skip"></a>`cmd/pebble-inject-skip` | 6 | 65 | generic | 0/1 | cmd/pebble-inject-skip (unchanged) | Operator repair tool for ABO's Pebble store, deliberately bypassing the format guard. | in 0, out 1. opens Pebble directly (`cmd/pebble-inject-skip/main.go:35`, raw/05 §1 row 8); writes ABO key families. |
| <a id="p-cmd-pid-census"></a>`cmd/pid-census` | 6 | 226 | mixed | 0/2 | cmd/pid-census (unchanged) | iTunes maintenance CLI; audiobook-only. | in 0, out 2. imports `internal/itunes` only (raw/01). |
| <a id="p-cmd-verify-suite"></a>`cmd/verify-suite` | 6 | 57 | generic | 0/1 | cmd/verify-suite (unchanged) | ABO dev tool; one consumer; not a platform concern. | in 0, out 1. no importer; one in-module import (raw/01). |
| <a id="p-internal-acoustid"></a>`internal/acoustid` | 0 | 224 | audiobook | 2/0 | internal/acoustid (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is a thin client for the acoustid.org /v2/lookup API. We only need the smallest slice of the response —". | in 2, out 0; importers: `internal/plugins/acoustid`, `internal/reconcile` |
| <a id="p-internal-activity"></a>`internal/activity` | 3 | 2526 | mixed | 14/5 | internal/activity (unchanged path; book changelog + per-user audit entries) | The books activity feed (tiers, batching, digests, changelog) stays in the books module (03 §3.3 `log` row; 05-logging §5): core `log` gets a new system-log journal and core `audit` gets new rows only. The slog line parsing in `writer.go:149-215` is deleted by 05-logging stage 1. | in 14, out 5. imports `undo` (`changelog.go:17`), uses `database.MetadataChangeRecord/BookPathChange` (`changelog.go:27-28`); serviceregistry registrations `register.go:37-120`; 14 importers.<br>**Split:** **delete (serviceregistry)** ← `register.go` (129 LOC); **internal/activity (ABO)** ← `api.go`, `batcher.go`, `service.go`, `writer.go`, `changelog.go`, `user_data.go` (2397 LOC) |
| <a id="p-internal-ai"></a>`internal/ai` | 3 | 6108 | mixed | 10/9 | internal/ai (unchanged path; audiobook AI features) | The OpenAI-compatible chat/batch/embedding clients, retry, pool routing and AI metrics are a provider adapter (`integrations/ai/openai`); prompts and features (dedup review, author dedup, cover text, metadata scoring) are books. | in 10, out 9. imports `covertext` (`cover_text.go:20`), uses `database.DedupCandidate` (`dedup_review.go:80`); concrete `*OpenAIParser` `openai_parser.go:261,358`; `aijobs.Register` from `init()` `author_dedup_job.go:132`, `dedup_review.go:242`; 10 importers.<br>**Split:** **mediacore/integrations/ai/openai (adapter)** ← `embedding_batch.go`, `embedding_client.go`, `openai_batch.go`, `openai_batch_find.go`, `retry.go`, `pool_routing.go`, `ai_metrics.go`, `ai_metrics_models.go`, `telemetry.go`, `aijobs_adapter.go` (3002 LOC); **delete (serviceregistry)** ← `register.go` (174 LOC); **internal/ai (ABO; openai_parser.go splits: client -> adapter, Parse* methods -> ABO)** ← `openai_parser.go`, `author_dedup_job.go`, `cover_text.go`, `dedup_review.go`, `embedding_scorer.go`, `llm_scorer.go`, `metadata_llm_review.go`, `metadata_scorer.go` (2932 LOC) |
| <a id="p-internal-ai-mocks"></a>`internal/ai/mocks` | 3 | 166 | generic | 2/1 | internal/ai/mocks (unchanged) | Mock of `MetadataCandidateScorer`, which stays in ABO. | in 2, out 1. `mock_metadata_candidate_scorer.go`; 3 test importer files. |
| <a id="p-internal-aiscan"></a>`internal/aiscan` | 3 | 2915 | mixed | 2/5 | internal/aiscan (unchanged) | AI author-scan pipeline over books and dedup; book metadata cross-validation. | in 2, out 5; imports audiobook pkgs: `internal/dedup`; importers: `internal/server`, `internal/server/handlers` |
| <a id="p-internal-appdirs"></a>`internal/appdirs` | 3 | 88 | generic | 8/3 | internal/appdirs (unchanged path; adapter from ABO config to `library.AppDirs`) | Core `library` takes an `AppDirs` value (the type moves with `internal/pathutil/hidden.go`); `FromConfig` takes `*config.Config` and `Current` reads the global `config.AppConfig` (`appdirs.go:48-73`), which R1 and R5 keep out of core, so the package stays in ABO as the adapter (MOD-21). | in 8, out 3. imports `backup`, `config`, `pathutil` (`appdirs.go:55-66`); 8 importers. |
| <a id="p-internal-applycap"></a>`internal/applycap` | 0 | 401 | generic | 9/0 | internal/applycap (unchanged) | Ceiling on how many BOOKS one bulk apply may touch; the concept is ABO's bulk metadata apply. | in 9, out 0. stdlib; `applycap.go:401`; 9 ABO importers. |
| <a id="p-internal-applygate"></a>`internal/applygate` | 3 | 2970 | audiobook | 10/8 | internal/applygate (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the certainty gate every BULK metadata apply consults before it writes a candidate onto a book: the cached". | in 10, out 8; imports audiobook pkgs: `internal/authorjunk`, `internal/authorname`, `internal/franchise`, `internal/metadata` (+2); importers: `internal/catalog`, `internal/itunes/service`, `internal/maintenance/jobs` (+7) |
| <a id="p-internal-arch"></a>`internal/arch` | 0 | 0 | generic | 0/0 | internal/arch (unchanged path; ABO layering guard) | 03 §3.5.1: the classification engine moves to `archtest` as a library; the ABO guard keeps its rule with the ratio floor, the `. -> internal/server` control edge and layer -1 for core imports. | in 0, out 0. 0 non-test LOC; `const minModulePackages = 150` `layering_test.go:38`; control edge `:40`; `layerOf` 213 keys `:59-286`; sibling guards hard-code `internal/...` (raw/03 §4.7).<br>**Split:** **mediacore/archtest (engine, from layering_test.go)** |
| <a id="p-internal-audio"></a>`internal/audio` | 3 | 107 | audiobook | 1/1 | internal/audio (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 1; imports audiobook pkgs: `internal/transcode`; importers: `internal/server` |
| <a id="p-internal-audiobooks"></a>`internal/audiobooks` | 3 | 20631 | audiobook | 15/29 | internal/audiobooks (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "provides the core business logic for managing audiobooks, including CRUD operations, metadata management". | in 15, out 29; imports audiobook pkgs: `internal/authorcredit`, `internal/dedup`, `internal/itunesguard`, `internal/mediainfo` (+11); importers: `internal/batch`, `internal/decisions`, `internal/maintenance/jobs` (+12) |
| <a id="p-internal-audioext"></a>`internal/audioext` | 0 | 160 | audiobook | 5/0 | internal/audioext (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "owns the ONE list of file extensions that decide whether a file on disk is part of this library". | in 5, out 0; importers: `internal/bookfiles`, `internal/config`, `internal/deluge` (+2) |
| <a id="p-internal-audioutil"></a>`internal/audioutil` | 0 | 384 | audiobook | 8/0 | internal/audioutil (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "holds small, dependency-free helpers shared by the audio processing packages (internal/mediainfo". | in 8, out 0; importers: `internal/fingerprint`, `internal/mediainfo`, `internal/plugins/acoustid` (+5) |
| <a id="p-internal-authorcredit"></a>`internal/authorcredit` | 3 | 1317 | audiobook | 10/4 | internal/authorcredit (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "turns one author credit string ("J.N. Chaney, Jonathan P. Brazee") into the author rows a book should be". | in 10, out 4; imports audiobook pkgs: `internal/authority`, `internal/personname`; importers: `internal/audiobooks`, `internal/authority/authoritybuild`, `internal/importer` (+7) |
| <a id="p-internal-authority"></a>`internal/authority` | 3 | 810 | audiobook | 8/2 | internal/authority (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "keeps the authority lists: a rebuildable reference index of known people (authors, narrators, cast members)". | in 8, out 2; imports audiobook pkgs: `internal/personname`; importers: `internal/authorcredit`, `internal/authority/authoritybuild`, `internal/catalog` (+5) |
| <a id="p-internal-authority-authoritybuild"></a>`internal/authority/authoritybuild` | 4 | 1233 | audiobook | 1/5 | internal/authority/authoritybuild (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 5; imports audiobook pkgs: `internal/authorcredit`, `internal/authority`, `internal/catalog`, `internal/metadata`; importers: `internal/plugins/maintenance` |
| <a id="p-internal-authorjunk"></a>`internal/authorjunk` | 3 | 1351 | audiobook | 7/3 | internal/authorjunk (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "answers "is this AUTHOR ROW a person, or something the importer filed in the author field that is not a". | in 7, out 3; imports audiobook pkgs: `internal/authorname`, `internal/personname`; importers: `internal/applygate`, `internal/catalog`, `internal/metabatch` (+4) |
| <a id="p-internal-authormerge"></a>`internal/authormerge` | 3 | 342 | audiobook | 2/2 | internal/authormerge (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the journaled write path every author merge shares: moving one book's credits off the merged author, and". | in 2, out 2; imports audiobook pkgs: `internal/undo`; importers: `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-authorname"></a>`internal/authorname` | 0 | 757 | audiobook | 12/1 | internal/authorname (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "holds the one author name the system writes to mean "we could not resolve an author". It exists as its own". | in 12, out 1; imports audiobook pkgs: `internal/personname`; importers: `internal/applygate`, `internal/authorjunk`, `internal/dedup` (+9) |
| <a id="p-internal-batch"></a>`internal/batch` | 3 | 1169 | audiobook | 5/6 | internal/batch (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 5, out 6; imports audiobook pkgs: `internal/merge`, `internal/versionprimary`; importers: `internal/audiobooks`, `internal/metafetch`, `internal/server` (+2) |
| <a id="p-internal-boilerplate"></a>`internal/boilerplate` | 3 | 95 | audiobook | 3/1 | internal/boilerplate (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "holds the compiled-in list of publisher intro/outro "titles" that are not real books, plus a pure matcher". | in 3, out 1; imports: `internal/util`; importers: `internal/dedup`, `internal/dedup/dataset`, `internal/plugins/maintenance` |
| <a id="p-internal-bookfileaudio"></a>`internal/bookfileaudio` | 3 | 209 | audiobook | 7/3 | internal/bookfileaudio (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "fills a book_file row's audio facts (duration, codec) at the moment the row is created, so no writer stores a". | in 7, out 3; imports audiobook pkgs: `internal/mediainfo`; importers: `internal/importer`, `internal/maintenance/jobs`, `internal/merge` (+4) |
| <a id="p-internal-cache"></a>`internal/cache` | 0 | 294 | generic | 10/1 | internal/cache (unchanged) | Generic in-memory cache with generation counter, but every consumer is ABO and no core contract needs it (03 §3.4 rule). | in 10, out 1. imports `metrics`; importers `ai`, `audiobooks`, `database`, `server`, handlers (9). |
| <a id="p-internal-catalog"></a>`internal/catalog` | 4 | 3071 | audiobook | 3/9 | internal/catalog (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the author catalog (design .claude/notes/catalog-wanted-requests-design-2026-10-01.md, phase P1): the". | in 3, out 9; imports audiobook pkgs: `internal/applygate`, `internal/authority`, `internal/authorjunk`, `internal/metadata` (+2); importers: `internal/authority/authoritybuild`, `internal/server`, `internal/server/handlers/metadata` |
| <a id="p-internal-cfaccesspolicy"></a>`internal/cfaccesspolicy` | 0 | 622 | generic | 1/0 | internal/cfaccesspolicy (unchanged) | Edits a Cloudflare Access policy to admit invited guests: deployment-specific edge auth (G11), one consumer. | in 1, out 0. `client.go:83-325`; 1 importer. |
| <a id="p-internal-cfservicetoken"></a>`internal/cfservicetoken` | 0 | 437 | generic | 1/0 | internal/cfservicetoken (unchanged) | Mints per-guest Cloudflare service tokens; deployment-specific (G11). | in 1, out 0. `client.go:53,170-191`; name prefix `aorg-guest-`; 1 importer. |
| <a id="p-internal-chaptershape"></a>`internal/chaptershape` | 0 | 97 | audiobook | 4/0 | internal/chaptershape (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "recognises the "one chapter per folder" layout: <Book>/<Book> - 1/file <Book>/<Book> - 2/file ... The scanner". | in 4, out 0; importers: `internal/database`, `internal/organizer`, `internal/plugins/maintenance` (+1) |
| <a id="p-internal-config"></a>`internal/config` | 1 | 10506 | mixed | 52/10 | internal/config (unchanged path; books typed config section `modules.books.*`, legacy-key compat, and the `config.AppConfig` façade over core handles, kept: SIMP-3) | Generic loader machinery (layering, `Mutate`/`Snapshot`/`Generation`, viper decode, unknown/removed-key detection, protected fields, secure state dir, secret-row persistence) goes to core `config`/`secrets`; the 864-line struct's audiobook keys, blob migrations and ABO sub-configs stay. Its five wrong-way edges disappear because core `config` imports nothing above storage (R4). | in 52, out 10. imports `audioext` (`config.go:23`), `dedup/unified` (`dedup_score_config.go:11`, `update_service.go:22`), `database`, `auth`, `backup`, `aidispatch` (allowed wrong-way edges 1-5, `internal/arch/layering_test.go:291-295`); global `var AppConfig` (`config.go:1939`), viper (`config.go:2590`) violate R5; 52 importers.<br>**Split:** **mediacore/config (loader, layering, generation, decode, key audits)** ← `config_deepcopy.go`, `viper_decode.go`, `unknown_keys.go`, `removed_keys.go`, `protected_fields.go`, `blob_default_audit.go`, `state_dir.go`, `persistence_owner_unix.go` (1562 LOC); **mediacore/integrations/ai (endpoint config section)** ← `ai_endpoints.go` (554 LOC); **mediacore/auth (bootstrap-key TTL, invite + SMTP sections)** ← `bootstrap_key_ttl.go`, `invite_config.go` (324 LOC); **mediacore/log (retention section)** ← `log_retention.go` (104 LOC); **delete (serviceregistry accessor)** ← `register.go` (51 LOC); **internal/config (ABO; config.go/persistence.go/update_service.go are split by function: loader+secret rows -> core, struct+blob migrations -> ABO)** ← `abs_config.go`, `chapter_consolidation.go`, `dedup_score_config.go`, `itunes_libraries.go`, `naming_patterns.go`, `config.go`, `persistence.go`, `update_service.go` (7911 LOC) |
| <a id="p-internal-covers"></a>`internal/covers` | 3 | 166 | audiobook | 3/2 | internal/covers (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 2; imports: `internal/security/safehttp`, `internal/security/safepath`; importers: `internal/plugins/maintenance`, `internal/server`, `internal/server/handlers/abs` |
| <a id="p-internal-covertext"></a>`internal/covertext` | 0 | 226 | audiobook | 3/0 | internal/covertext (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "stores the text a vision model read off a cover image". | in 3, out 0; importers: `internal/ai`, `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-database"></a>`internal/database` | 2 | 103432 | mixed | 110/16 | internal/books/store (renamed; residue = books module store) | Keystone split. The 445-method `Store` dissolves: 108 methods sit behind core service contracts (69 move into core; the 39 `OpsV2Store` methods stay in the residue as ABO's legacy run-row writers until 05-tasks T8 deletes them), 337 stay as the books module's store (314 domain + 23 legacy ops-v1). The residue is renamed because, once core owns `storage`, a root-module package named `database` would read as the platform DB. See §4. | in 110, out 16. 110 importers (61 audiobook, 37 mixed, 6 generic importers, raw/02 finding 1); `type Book struct` `internal/database/store.go:202`; `Store` composes six composites `store.go:48-55`; imports 7 audiobook packages (`pebble_store.go:31,33,35`, `pebble_store_authors.go:21`, `metadata_field_locks.go:15`, `book_own_folder.go:15`, `pebble_store_bookmarks.go:39`) and `models` (`embedding_store.go:22`).<br>**Split:** **mediacore/storage + mediacore/internal/pebble** ← `census.go`, `census_exact.go`, `census_families.go`, `compaction_stats.go`, `database.go`, `interface.go`, `migration_bookkeeping.go`, `partial_read.go`, `pebble_gate.go`, `pebble_metrics_export.go`, `raw_batch.go`, `raw_snapshot.go`, `startup_notify.go`, `storage_format.go`, `storage_format_repair.go`, `store_from_store.go` (4108 LOC); **mediacore/storage/index (dirty set, change observer)** ← `change_observer.go`, `dirty_set.go`, `pebble_store_search_dirty.go` (401 LOC); **delete at 05-tasks T8 (ABO's v2/v3 run-row store; until then in the books residue, its writers unused after T0: core claims the ops families, reads them in place through `internal/executor/legacyhist` and writes only `c/tasks/`, books keeps `opstate:`; 05-storage §5S.9.1, 05-tasks §5T.6)** ← `iface_op_rows.go`, `iface_ops_v2.go`, `iface_ops_v3.go`, `op_class_lists.go`, `ops_v2_recent.go`, `pebble_store_op_rows.go`, `pebble_store_ops_v2.go`, `pebble_store_ops_v2_status_lookup.go`, `pebble_store_ops_v2_timeline.go`, `pebble_store_ops_v2_wait.go`, `pebble_store_ops_v3.go`, `pebble_store_ops_v3_lastrun.go`, `pebble_store_ops_v3_migrate.go`, `pebble_store_ops_v3_reconcile.go`, `pebble_store_ops_v3_rowmarks.go` (7592 LOC); **mediacore/auth (token helpers and store capability interfaces ABO packages name)** ← `apikey_token.go`, `iface_auth.go`, `iface_oauth.go` (137 LOC); **mediacore/internal/authstore (users, sessions, keys, invites, OAuth ids; exported types, capability interfaces and errors in these files are cut out to mediacore/auth)** ← `invite_id.go`, `oauth_identity.go`, `pebble_store_auth.go`, `pebble_store_invite_delivery.go`, `pebble_store_verified_email_backfill.go` (3053 LOC); **mediacore/integrations/ai (batch-job store)** ← `ai_jobs_types.go`, `pebble_store_aijobs.go` (292 LOC); **mediacore/integrations/providerhttp (persisted throttle holds)** ← `provider_throttle.go` (74 LOC); **mediacore/library (root folders store)** ← `pebble_store_importpaths.go` (261 LOC); **mediacore/secrets (encryption and key file; file divided: the `setting:` row methods stay in the residue, cross-cutting resolution 7)** ← `settings.go` (466 LOC); **delete (decorator/SQLite leftovers)** ← `audiobooks.go`, `diagnostics_types.go`, `store_capability.go`, `storecap.go`, `web.go` (249 LOC) |
| <a id="p-internal-database-aggtest"></a>`internal/database/aggtest` | 0 | 146 | generic | 4/0 | internal/books/store/aggtest (renamed) | Counts `RecomputeBookAggregates` runs for tests; books-only. | in 4, out 0. test-only helper; 9 test importer files; `RecomputeBookAggregates` is books (`BookAggregateWriter`, raw/04 §8). |
| <a id="p-internal-database-dbtest"></a>`internal/database/dbtest` | 3 | 449 | mixed | 11/1 | internal/books/store/storetest (renamed) | Books store test fixtures and invariants. The in-memory engine it relies on becomes core `coretest.NewStorage(t)` (vfs.NewMem), which this package then wraps. | in 11, out 1. uses `database.Book` (`internal/database/dbtest/invariants.go:84`); in-memory store `NewPebbleStoreInMemory` (`internal/database/pebble_store.go:578`); 30 test importer files.<br>**Split:** **mediacore/coretest (in-memory storage engine)** ← `newstore.go` (99 LOC) |
| <a id="p-internal-decisions"></a>`internal/decisions` | 3 | 2751 | mixed | 3/3 | internal/decisions (unchanged) | Retired-version decisions over `Book`/`BookVersion`/`BookFile`. | in 3, out 3; imports: `internal/database`, `internal/keyedmutex`, `internal/logger`; importers: `internal/audiobooks`, `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-dedup"></a>`internal/dedup` | 3 | 15806 | audiobook | 19/21 | internal/dedup (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "exact-rule provenance and the placeholder-title guard". | in 19, out 21; imports audiobook pkgs: `internal/authorname`, `internal/boilerplate`, `internal/dedup/dataset`, `internal/dedup/unified` (+6); importers: `internal/aiscan`, `internal/audiobooks`, `internal/decisions` (+16) |
| <a id="p-internal-dedup-dataset"></a>`internal/dedup/dataset` | 3 | 821 | audiobook | 3/4 | internal/dedup/dataset (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "builds labeled dedup examples and runs deterministic catchers over them". | in 3, out 4; imports audiobook pkgs: `internal/boilerplate`, `internal/fingerprint`; importers: `internal/dedup`, `internal/plugins/dedup`, `internal/server/handlers/dedup` |
| <a id="p-internal-dedup-unified"></a>`internal/dedup/unified` | 3 | 686 | audiobook | 10/1 | internal/dedup/unified (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "provides the scoring core for the unified deduplication pipeline (SPEC 1, fable5)". | in 10, out 1; imports: `internal/models`; importers: `internal/config`, `internal/database`, `internal/decisions` (+7) |
| <a id="p-internal-deluge"></a>`internal/deluge` | 3 | 2264 | mixed | 4/10 | internal/deluge (unchanged path; library import, discovery, protected paths) | The Deluge Web JSON-RPC client is the first `download` adapter; discovery-by-label, import, mark-in-place and protected paths are audiobook integration logic. | in 4, out 10. concrete client `client.go:39,136,259-483` (raw/11 §4); `discovery.go:36` imports `database`; imports `fingerprint`, `tagger`, `audioext`.<br>**Split:** **mediacore/integrations/download/deluge** ← `client.go` (506 LOC) |
| <a id="p-internal-diagnosis"></a>`internal/diagnosis` | 0 | 295 | audiobook | 1/0 | internal/diagnosis (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "probes audio files with available system tools (ffprobe, mediainfo, file) to produce structured diagnostic". | in 1, out 0; importers: `internal/remux` |
| <a id="p-internal-diagnostics"></a>`internal/diagnostics` | 3 | 799 | mixed | 3/2 | internal/diagnostics (unchanged) | Diagnostic export of books and iTunes albums (SlimBook). | in 3, out 2; imports audiobook pkgs: `internal/itunes`; importers: `internal/server`, `internal/server/handlers`, `internal/server/handlers/opaccess` |
| <a id="p-internal-errhandling"></a>`internal/errhandling` | 0 | 120 | generic | 1/0 | internal/errhandling (unchanged) | Silent-failure sweep primitive used only by the ABO server. | in 1, out 0. stdlib; 1 importer (`internal/server`). |
| <a id="p-internal-filehash"></a>`internal/filehash` | 0 | 186 | audiobook | 14/0 | internal/filehash (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "owns the ONE hash algorithm that identifies an audio file in this database". | in 14, out 0; importers: `internal/fileops`, `internal/importer`, `internal/itunes/service` (+11) |
| <a id="p-internal-fileops"></a>`internal/fileops` | 3 | 1660 | mixed | 16/6 | internal/fileops (unchanged path; tag writing + book-file hash recording only) | Byte copy, reflink, safe move/copy, browse allow-list and the canonical whole-file hash are generic library operations; tag writing and book_file hash recording are books. | in 16, out 6. imports `filehash` (`book_file_hash_recorder.go:12`, `write_tags_safe.go:21`), `database.BookFile*` (`book_file_hash_recorder.go:20-40`); 16 importers.<br>**Split:** **mediacore/library** ← `copy.go`, `hash.go`, `reflink.go`, `reflink_linux.go`, `safe_operations.go`, `service.go` (1070 LOC); **delete (serviceregistry)** ← `register.go` (22 LOC); **internal/fileops (ABO)** ← `book_file_hash_recorder.go`, `write_tags_safe.go` (568 LOC) |
| <a id="p-internal-fingerprint"></a>`internal/fingerprint` | 0 | 2866 | audiobook | 13/1 | internal/fingerprint (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "generates AcoustID-compatible acoustic fingerprints for audio files". | in 13, out 1; imports audiobook pkgs: `internal/audioutil`; importers: `cmd`, `internal/database`, `internal/dedup` (+10) |
| <a id="p-internal-fingerprint-workerapi"></a>`internal/fingerprint/workerapi` | 0 | 314 | audiobook | 4/0 | internal/fingerprint/workerapi (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the wire format of the remote fingerprint worker API (/api/v1/fingerprint/worker/*), shared by the server". | in 4, out 0; importers: `internal/fingerprint/workerclient`, `internal/plugins/acoustid`, `internal/server/handlers/fpworker` (+1) |
| <a id="p-internal-fingerprint-workerclient"></a>`internal/fingerprint/workerclient` | 3 | 1968 | audiobook | 1/4 | internal/fingerprint/workerclient (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the fp-worker client: a process on another host (an Apple Silicon Mac in practice) that leases". | in 1, out 4; imports audiobook pkgs: `internal/fingerprint`, `internal/fingerprint/workerapi`; importers: `cmd` |
| <a id="p-internal-flight"></a>`internal/flight` | 0 | 128 | generic | 3/0 | internal/flight (unchanged) | Collapse-concurrent-requests helper; consumers are ABO only. | in 3, out 0. stdlib; importers `audiobooks`, `server`, `handlers/audiobooks`. |
| <a id="p-internal-foldercover"></a>`internal/foldercover` | 3 | 345 | audiobook | 2/3 | internal/foldercover (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "decides whether a book should take its cover from an image in its own folder, and applies that decision". | in 2, out 3; imports audiobook pkgs: `internal/audioext`, `internal/metadata`; importers: `internal/plugins/maintenance`, `internal/scanner` |
| <a id="p-internal-foldernames"></a>`internal/foldernames` | 3 | 344 | audiobook | 3/4 | internal/foldernames (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "builds the person and series evidence the folder parse (metadata.ExtractMetadataFromFolderWith) decides an". | in 3, out 4; imports audiobook pkgs: `internal/authority`, `internal/metadata`, `internal/personname`; importers: `internal/metafetch`, `internal/plugins/maintenance`, `internal/scanner` |
| <a id="p-internal-franchise"></a>`internal/franchise` | 0 | 812 | audiobook | 5/0 | internal/franchise (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the one matcher for the libraries the owner curates by hand: Doctor Who, Big Finish and Torchwood". | in 5, out 0; importers: `internal/applygate`, `internal/maintenance/jobs`, `internal/metadata` (+2) |
| <a id="p-internal-httputil"></a>`internal/httputil` | 0 | 604 | mixed | 19/2 | internal/httputil (unchanged path; gin helpers of the permanent legacy mount); envelope types and Range serving -> mediacore/httpapi | Response envelope and request parsing are the core HTTP framework, but the gin helpers cannot move (core is `net/http`, N7) and ABO's gin engine is mounted permanently (`httpapi.MountLegacy`, cross-cutting resolution 2), so `parse.go` and `respond.go` stay; core re-implements them on net/http. `RespondWithApplyCapExceeded` uses ABO's `internal/applycap` (`respond.go:16,78`) and stays with them (MOD-21). The gin-free envelope types and Range serving move. | in 19, out 2. imports `gin` (third-party), `applycap`, `logging`; `.m4b/.m4a` content-type switch `rangeserve.go:34`; `RespondWithError` envelope (raw/09 §5); 19 importers, 18 in server.<br>**Split:** **mediacore/httpapi (envelope types)** ← `types.go` (106 LOC); **mediacore/httpapi (Range serving; media types come from module registration)** ← `rangeserve.go` (222 LOC); **internal/httputil (ABO, gin helpers)** ← `parse.go`, `respond.go` (276 LOC) |
| <a id="p-internal-identification"></a>`internal/identification` | 3 | 347 | audiobook | 1/1 | internal/identification (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the body of the identification.advance driver (spec 02 section 3.2 "The driver", section 4 PR 14): it". | in 1, out 1; imports: `internal/database`; importers: `internal/server` |
| <a id="p-internal-importer"></a>`internal/importer` | 4 | 789 | audiobook | 3/15 | internal/importer (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 15; imports audiobook pkgs: `internal/authorcredit`, `internal/bookfileaudio`, `internal/dedup`, `internal/filehash` (+4); importers: `internal/server`, `internal/server/handlers`, `internal/server/handlers/mocks` |
| <a id="p-internal-itunes"></a>`internal/itunes` | 3 | 8822 | audiobook | 12/6 | internal/itunes (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "provides functionality for importing audiobooks from iTunes Library.xml files". | in 12, out 6; imports: `internal/config`, `internal/database`, `internal/logger`, `internal/models` (+2); importers: `cmd/itl-audit-encoding`, `cmd/itl-check`, `cmd/itl-diff` (+9) |
| <a id="p-internal-itunes-service"></a>`internal/itunes/service` | 5 | 5587 | audiobook | 7/21 | internal/itunes/service (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "contains the iTunes integration: import pipeline, playlist import, and ITL download". | in 7, out 21; imports audiobook pkgs: `internal/applygate`, `internal/authorcredit`, `internal/dedup`, `internal/filehash` (+8); importers: `internal/plugins/itunes`, `internal/plugins/maintenance`, `internal/server` (+4) |
| <a id="p-internal-itunesguard"></a>`internal/itunesguard` | 3 | 136 | audiobook | 3/3 | internal/itunesguard (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the one iTunes-ownership predicate for writes to a book's version-group primary flag". | in 3, out 3; imports audiobook pkgs: `internal/repairs`, `internal/versionprimary`; importers: `internal/audiobooks`, `internal/plugins/maintenance`, `internal/server/handlers/dedup` |
| <a id="p-internal-keyedmutex"></a>`internal/keyedmutex` | 0 | 55 | generic | 3/0 | internal/keyedmutex (unchanged) | Per-key mutex; consumers are ABO only (moved into core only in build experiment E2, which is not a placement decision). | in 3, out 0. stdlib; importers `decisions`, `deluge` (`import.go:24,67`), `versions`. |
| <a id="p-internal-linkintegrity"></a>`internal/linkintegrity` | 0 | 489 | audiobook | 1/0 | internal/linkintegrity (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "holds the shared vocabulary for the "First Aid" library validation + repair pass: the shapes a broken". | in 1, out 0; importers: `internal/plugins/maintenance` |
| <a id="p-internal-logger"></a>`internal/logger` | 0 | 442 | mixed | 73/0 | internal/logger (unchanged path; books façade over mediacore/log, kept; deleting it is an optional ratchet, SIMP-3) | The printf `Logger` interface bundles progress, change recording and cancellation (task concerns) and leaks `BookID`; core exposes `log/slog` only, so the interface is not promoted. R5 binds core only (SIMP-3): the façade survives as books code over core handles, and the 342-file migration to slog is an optional ratchet, not an extraction stage. | in 73, out 0. `BookID` field `internal/logger/logger.go:45`; interface `logger.go:54-98`; writes through `slog.Default()` (`standard.go`); 73 importers, 342 files import it (raw/12 §3).<br>**Split:** **mediacore/log (sanitize, attrs, retention policy)** ← `sanitize.go`, `attrs.go`, `retention.go` (223 LOC); **internal/logger (ABO façade over mediacore/log)** ← `logger.go`, `standard.go` (219 LOC) |
| <a id="p-internal-maintenance-jobs"></a>`internal/maintenance/jobs` | 4 | 12366 | audiobook | 3/25 | internal/maintenance/jobs (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "registers all built-in maintenance jobs with the maintenance registry". | in 3, out 25; imports audiobook pkgs: `internal/applygate`, `internal/authorcredit`, `internal/authorname`, `internal/bookfileaudio` (+10); importers: `internal/audiobooks`, `internal/server`, `internal/server/handlers/abs` |
| <a id="p-internal-masterlist"></a>`internal/masterlist` | 0 | 1309 | audiobook | 2/0 | internal/masterlist (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the input side of the master-list import: the canonical row an uploaded list is stored as, the validation". | in 2, out 0; importers: `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-matcher"></a>`internal/matcher` | 0 | 545 | audiobook | 4/1 | internal/matcher (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 4, out 1; imports audiobook pkgs: `internal/personname`; importers: `internal/database`, `internal/dedup`, `internal/plugins/maintenance` (+1) |
| <a id="p-internal-mediainfo"></a>`internal/mediainfo` | 3 | 313 | audiobook | 6/1 | internal/mediainfo (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 6, out 1; imports audiobook pkgs: `internal/audioutil`; importers: `cmd`, `internal/audiobooks`, `internal/bookfileaudio` (+3) |
| <a id="p-internal-merge"></a>`internal/merge` | 3 | 13113 | audiobook | 28/10 | internal/merge (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 28, out 10; imports audiobook pkgs: `internal/bookfileaudio`, `internal/metastate`, `internal/personname`, `internal/syncapi/progress` (+1); importers: `internal/audiobooks`, `internal/batch`, `internal/database` (+25) |
| <a id="p-internal-metabatch"></a>`internal/metabatch` | 4 | 3528 | audiobook | 5/13 | internal/metabatch (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 5, out 13; imports audiobook pkgs: `internal/applygate`, `internal/authority`, `internal/authorjunk`, `internal/authorname` (+2); importers: `internal/plugins/maintenance`, `internal/scheduler`, `internal/server` (+2) |
| <a id="p-internal-metadata"></a>`internal/metadata` | 3 | 12071 | audiobook | 24/18 | internal/metadata (unchanged path; book metadata sources and parsing) | Six audiobook providers and the `BookMetadata` result type stay. The throttle registry and circuit breaker are independent of `BookMetadata` (raw/11 §3) and move to core; core `integrations/metadata` defines a generic `Provider[Q, R]` that ABO's `MetadataSource` implements. | in 24, out 18. `MetadataSource` `source.go:52-56`; `defaultThrottles` global `throttle_registry.go:69`; `CircuitBreaker` `circuitbreaker.go:32,138`; imports `operations/registry` (allowed wrong-way edge 7, `layering_test.go:297`); 24 importers.<br>**Split:** **mediacore/integrations/providerhttp (throttle registry, circuit breaker)** ← `throttle.go`, `throttle_registry.go`, `circuitbreaker.go` (997 LOC) |
| <a id="p-internal-metadata-mocks"></a>`internal/metadata/mocks` | 3 | 456 | audiobook | 1/1 | internal/metadata/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 1; imports audiobook pkgs: `internal/metadata`; importers: `internal/server/handlers/duplicates` |
| <a id="p-internal-metafetch"></a>`internal/metafetch` | 3 | 21306 | audiobook | 17/31 | internal/metafetch (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 17, out 31; imports audiobook pkgs: `internal/authorcredit`, `internal/authority`, `internal/authorjunk`, `internal/authorname` (+12); importers: `internal/applygate`, `internal/audiobooks`, `internal/catalog` (+14) |
| <a id="p-internal-metastate"></a>`internal/metastate` | 0 | 73 | audiobook | 8/0 | internal/metastate (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "owns the on-disk representation of per-book metadata state: the user-preference KEY a book's state is stored". | in 8, out 0; importers: `internal/audiobooks`, `internal/database`, `internal/merge` (+5) |
| <a id="p-internal-metrics"></a>`internal/metrics` | 0 | 840 | mixed | 14/1 | internal/metrics (unchanged path; books metrics on the App registry; names unchanged, `audiobook_organizer_*`, SIMP-13) | Generic collectors (operations, Go runtime, Pebble) move to core; book/search/catalog/ABS series stay. Package-level collectors on the default registry violate R5. | in 14, out 1. namespace `audiobook_organizer` `metrics.go:19`; `books_total` `metrics.go:51-53`; `prometheus.MustRegister` default registry `metrics.go:300` (raw/12 §4); 14 importers.<br>**Split:** **mediacore/internal/pebble (Pebble collector; ABO's Open Library store reaches it through `storage.OpenAuxiliary`)** ← `pebble_collector.go` (251 LOC); **internal/metrics (ABO; metrics.go split: operations_* series -> mediacore/tasks)** ← `metrics.go`, `pipeline_metrics.go` (589 LOC) |
| <a id="p-internal-mtls"></a>`internal/mtls` | 0 | 693 | generic | 1/0 | internal/mtls (unchanged) | CA/PSK + client-cert TLS for the MCP bridge tool; not part of HTTP auth. | in 1, out 0. only importer `cmd/mtls-bridge` (`internal/mtls/transport.go:14`, raw/10 §2.1). |
| <a id="p-internal-openlibrary"></a>`internal/openlibrary` | 2 | 843 | audiobook | 4/2 | internal/openlibrary (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 4, out 2; imports: `internal/database`, `internal/metrics`; importers: `internal/metadata`, `internal/metafetch`, `internal/server` (+1) |
| <a id="p-internal-operations-opswriter"></a>`internal/operations/opswriter` | 3 | 1930 | mixed | 6/3 | internal/books/writer (renamed; implements the books typed Writer) | Fencing is an executor mechanism and goes to core as public `tasks` fencing (`ErrFenced`, `WithFence`, `FenceFromContext`; `fence.go` is self-contained). Write-ahead intents and the evidence journal stay with the Writer: `writer.go` uses their unexported `intents` (`writer.go:123,149`) and `journalMemo` (`:133`) and `intent.go:15`/`evidence_journal.go:14` are typed through `database` (MOD2-3), so they are books code. | in 6, out 3. uses `database.MetadataChangeRecord/BookAuthor/BookFile/Book` (36 lines, `evidence.go:58,337,356`, raw/02 a1); imports `pkg/ops`; 6 importers (`registry`, `plugins/dedup`, `repairs`, `opstest`).<br>**Split:** **mediacore/tasks (run fencing: `ErrFenced`, `WithFence`, `FenceFromContext`)** ← `fence.go` (120 LOC); **internal/books/writer** ← `writer.go`, `evidence.go`, `history.go`, `intent.go`, `evidence_journal.go` (1810 LOC) |
| <a id="p-internal-organizer"></a>`internal/organizer` | 3 | 10070 | audiobook | 10/22 | internal/organizer (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 10, out 22; imports audiobook pkgs: `internal/authorname`, `internal/bookfileaudio`, `internal/chaptershape`, `internal/filehash` (+7); importers: `internal/audiobooks`, `internal/itunes/service`, `internal/metafetch` (+7) |
| <a id="p-internal-pathutil"></a>`internal/pathutil` | 0 | 1040 | mixed | 25/0 | internal/pathutil (unchanged path; iTunes/versions-dir/protected-folder rules only) | Named roots, root splitting (remote-worker wire format), separator handling and hidden-dir sweep rules are generic path mapping; iTunes and book-versions conventions are ABO. | in 25, out 0. `OpenLibraryDumpDir` `hidden.go:80`; iTunes special case `protected.go:403`; 25 importers.<br>**Split:** **mediacore/library (path mapping)** ← `pathvars.go`, `root.go`, `prefix.go`, `rel.go`, `hidden.go` (514 LOC); **internal/pathutil (ABO)** ← `itunes.go`, `protected.go`, `versions_dir.go` (526 LOC) |
| <a id="p-internal-personname"></a>`internal/personname` | 0 | 1844 | audiobook | 17/0 | internal/personname (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "answers "does this string look like a human author's name, rather than a title or a structural marker?" --". | in 17, out 0; importers: `internal/authorcredit`, `internal/authority`, `internal/authorjunk` (+14) |
| <a id="p-internal-playlist"></a>`internal/playlist` | 3 | 791 | audiobook | 2/2 | internal/playlist (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 2, out 2; imports: `internal/database`, `internal/search`; importers: `cmd`, `internal/server/handlers` |
| <a id="p-internal-plugins-acoustid"></a>`internal/plugins/acoustid` | 4 | 5718 | audiobook | 2/13 | internal/plugins/acoustid (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the UOS plugin for AcoustID fingerprinting operations". | in 2, out 13; imports audiobook pkgs: `internal/acoustid`, `internal/audioutil`, `internal/dedup`, `internal/fingerprint` (+1); importers: `internal/opscatalog`, `internal/server` |
| <a id="p-internal-plugins-dedup"></a>`internal/plugins/dedup` | 4 | 9279 | audiobook | 2/21 | internal/plugins/dedup (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "op dedup.breakdown-backfill". | in 2, out 21; imports audiobook pkgs: `internal/dedup`, `internal/dedup/dataset`, `internal/dedup/unified`, `internal/fingerprint` (+2); importers: `internal/opscatalog`, `internal/server` |
| <a id="p-internal-plugins-deluge"></a>`internal/plugins/deluge` | 4 | 2433 | mixed | 2/11 | internal/plugins/deluge (unchanged) | Audiobook import centralization over Deluge; becomes books tasks on the core download contract. | in 2, out 11; imports audiobook pkgs: `internal/filehash`, `internal/tagger`; importers: `internal/opscatalog`, `internal/server` |
| <a id="p-internal-plugins-itunes"></a>`internal/plugins/itunes` | 4 | 355 | audiobook | 1/6 | internal/plugins/itunes (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the UOS plugin for iTunes/Music library operations". | in 1, out 6; imports audiobook pkgs: `internal/itunes/service`; importers: `internal/opscatalog` |
| <a id="p-internal-plugins-maintenance"></a>`internal/plugins/maintenance` | 4 | 101319 | audiobook | 2/71 | internal/plugins/maintenance (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "op maintenance.build-folder-book-files". | in 2, out 71; imports audiobook pkgs: `internal/applygate`, `internal/audiobooks`, `internal/audioutil`, `internal/authorcredit` (+41); importers: `internal/server`, `internal/server/handlers` |
| <a id="p-internal-plugins-metafetch"></a>`internal/plugins/metafetch` | 4 | 3942 | audiobook | 1/11 | internal/plugins/metafetch (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "op metafetch.calibrate-scoring (INIT-3-T1)". | in 1, out 11; imports audiobook pkgs: `internal/applygate`, `internal/metadata`, `internal/metafetch`, `internal/repairs`; importers: `internal/opscatalog` |
| <a id="p-internal-policy"></a>`internal/policy` | 0 | 80 | audiobook | 4/0 | internal/policy (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "evaluates per-book processing policy tags". | in 4, out 0; importers: `internal/metafetch`, `internal/organizer`, `internal/server` (+1) |
| <a id="p-internal-quarantine"></a>`internal/quarantine` | 3 | 1003 | audiobook | 2/7 | internal/quarantine (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 2, out 7; imports audiobook pkgs: `internal/authorname`, `internal/filehash`; importers: `internal/scanner`, `internal/server` |
| <a id="p-internal-querygrammar"></a>`internal/querygrammar` | 0 | 896 | mixed | 5/0 | internal/querygrammar (unchanged; `shared/` candidate) | The Library search-bar value syntax; one app consumes it. Promote to `mediacore/shared/querygrammar` when a second app adopts the same search bar (03 §3.4). | in 5, out 0. no in-module imports; `BookID`/`Title` in the budget error `budget.go:70`; importers `audiobooks`, `search`, `server`, `handlers`, `handlers/audiobooks`. |
| <a id="p-internal-readstatus"></a>`internal/readstatus` | 3 | 322 | audiobook | 3/1 | internal/readstatus (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 1; imports: `internal/database`; importers: `internal/merge`, `internal/server/handlers`, `internal/server/handlers/abs` |
| <a id="p-internal-reconcile"></a>`internal/reconcile` | 3 | 3893 | audiobook | 2/17 | internal/reconcile (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 2, out 17; imports audiobook pkgs: `internal/acoustid`, `internal/fingerprint`, `internal/merge`, `internal/scanner` (+2); importers: `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-remux"></a>`internal/remux` | 3 | 671 | audiobook | 3/8 | internal/remux (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 8; imports audiobook pkgs: `internal/diagnosis`; importers: `cmd/pebble-inject-skip`, `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-repairs"></a>`internal/repairs` | 3 | 5148 | audiobook | 13/14 | internal/repairs (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the shared framework behind the Repairs lane of /review: one contract every library fixer implements, and". | in 13, out 14; imports audiobook pkgs: `internal/applygate`, `internal/franchise`, `internal/merge`, `internal/undo`; importers: `internal/audiobooks`, `internal/itunesguard`, `internal/plugins/maintenance` (+10) |
| <a id="p-internal-scanlock"></a>`internal/scanlock` | 0 | 359 | audiobook | 5/0 | internal/scanlock (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the per-BOOK lock that lets a library scan and a metadata apply run at the same time without either one". | in 5, out 0; importers: `internal/organizer`, `internal/scanner`, `internal/server` (+2) |
| <a id="p-internal-scanner"></a>`internal/scanner` | 3 | 14775 | audiobook | 11/27 | internal/scanner (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "provides file scanning and processing utilities for the audiobook organizer". | in 11, out 27; imports audiobook pkgs: `internal/audioutil`, `internal/authorcredit`, `internal/authorname`, `internal/bookfileaudio` (+12); importers: `cmd`, `internal/audiobooks`, `internal/importer` (+8) |
| <a id="p-internal-search"></a>`internal/search` | 2 | 4425 | mixed | 6/6 | internal/search (unchanged path; books index documents, field routing, per-user terms) | `storage/index` is a platform contract (03 §3.3), so the bleve engine lifecycle (open, mapping-version rebuild, markers, degraded open) and a query AST + translator go to core; the book document mapping, field routing and per-user terms stay. | in 6, out 6. uses `database.UserBookState/Author/Series/Book` (`index_builder.go:28-29,84`, 29 lines); `bleve_index.go` has 92 book/author/series mentions; degraded open `register.go:46-60`; mapping rebuild `bleve_index.go:186-215`; `database.SetCollectionPerViewerClassifier` init hook `field_routing.go:159`.<br>**Split:** **mediacore/internal/bleveindex (engine lifecycle cut out of bleve_index.go)**; **mediacore/storage/index (query AST, regex terms)** ← `query_ast.go`, `regex_terms.go` (316 LOC); **delete (serviceregistry)** ← `register.go` (115 LOC); **internal/search (ABO; `bleve_translator.go` translates the books grammar and declares `PerUserFilter`/`TranslateAt`, used by `internal/audiobooks` and `internal/playlist`, so it stays)** ← `bleve_translator.go`, `bleve_index.go`, `document.go`, `field_routing.go`, `field_term.go`, `index_builder.go`, `per_user_match.go`, `per_user_mixed.go`, `per_user_shape.go`, `per_user_values.go`, `query_parser.go`, `relative_date.go`, `tag_terms.go` (3994 LOC) |
| <a id="p-internal-searchcache"></a>`internal/searchcache` | 3 | 1157 | audiobook | 4/2 | internal/searchcache (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "holds the shared search result cache: the full ranked list of matching book IDs per (query, filter set)". | in 4, out 2; imports: `internal/logger`, `internal/metrics`; importers: `internal/audiobooks`, `internal/server`, `internal/server/handlers/abs` (+1) |
| <a id="p-internal-seqnum"></a>`internal/seqnum` | 0 | 270 | audiobook | 3/0 | internal/seqnum (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "extracts a volume / sequence number ("Big Cats 3", "Vol". | in 3, out 0; importers: `internal/applygate`, `internal/catalog`, `internal/metafetch` |
| <a id="p-internal-server"></a>`internal/server` | 5 | 46789 | mixed | 2/122 | internal/server (unchanged path; books HTTP adapter, mounted permanently through `httpapi.MountLegacy`, N7) | The gin composition layer of ABO, mounted permanently behind core's perimeter (cross-cutting resolution 2). The core concerns inside it are re-implemented in core on net/http and leave at the stage that makes them native: perimeter at S2, auth at S4, task admin at S5, SPA serving at S9; books route groups stay unless rewritten for their own reasons. | in 2, out 122. out 122, imports 56 audiobook packages (raw/02 d); 10 `serviceregistry.Register` from `init()` `registry_wire.go:97-343`; ~48 `addOpRegistrar` inits; `NewServer` ~612 lines `server.go:653`; plugin mount `plugins_init.go:42`.<br>**Split:** **mediacore/auth (bootstrap, temp login, invite accept, reset, login/invite/bootstrap rate limits, credential/admin route helpers, API-key expiry)** ← `bootstrap.go`, `bootstrap_rate_limit.go`, `auth_temp_login.go`, `auth_accept_invite.go`, `auth_reset_password.go`, `login_ceiling.go`, `invite_rate_limit.go`, `credential_routes.go`, `wire_admin_role.go`, `toplevel_auth_chain.go`, `apikey_expiry_stamp.go`, `apikey_expiry_sweep.go`, `wire_auth_routes.go`, `wire_oauth.go` (3316 LOC); **mediacore/httpapi (perimeter: peer allow-list, CSP, security headers/CORS)** ← `peer_allowlist.go`, `csp.go`, `csp_report.go`, `server_middleware.go` (1072 LOC); **mediacore/ui (SPA fallback, static; at S9, FE-1)** ← `spa_fallback.go`, `static_nonembed.go` (244 LOC); **mediacore/health (flight recorder wiring)** ← `flight_recorder.go` (55 LOC); **mediacore/updater (HTTP)** ← `update_handlers.go` (64 LOC); **mediacore/integrations/providerhttp (throttle store wiring)** ← `provider_throttle_wire.go` (65 LOC); **mediacore/storage/index (dirty-set reconciler, change observer)** ← `search_reconciler.go`, `search_observer.go`, `index_queue.go` (997 LOC); **delete (init registrars, plugin mount)** ← `op_registrars.go`, `plugins_init.go` (77 LOC); **internal/cfaccess (ABO)** ← `cf_recovery.go`, `guest_service_tokens.go`, `invite_guest_access.go`, `owner_routes.go` (1332 LOC); **internal/server (ABO; gin 410 stubs of the permanent legacy mount, core `httpapi.Gone` is a port)** ← `gone.go` (36 LOC) |
| <a id="p-internal-server-absauth"></a>`internal/server/absauth` | 5 | 1536 | audiobook | 4/1 | internal/server/absauth (unchanged path; ABS JWT/refresh only) | Not purely ABS: web login uses its argon2id hashing, failed-login throttle and saturation limiter, and its audit event type. Those move to core; the ABS token surface stays. | in 4, out 1. `absauth.HashPassword` used by `internal/server/handlers/user.go`, `auth.go`, `internal/server/bootstrap.go`, `auth_accept_invite.go`, `login_ceiling.go` (grep at 748fd787a); `absauth.Audit` 46 uses; argon2id `password.go:131-190` (raw/10 §7).<br>**Split:** **mediacore/auth (password hashing, login throttle, saturation, LRU)** ← `password.go`, `throttle.go`, `saturation.go`, `lru.go` (853 LOC); **mediacore/audit (auth audit events)** ← `audit.go` (137 LOC); **internal/server/absauth (ABO)** ← `config.go`, `token.go`, `refresh.go` (546 LOC) |
| <a id="p-internal-server-handlers"></a>`internal/server/handlers` | 5 | 20579 | mixed | 4/47 | internal/server/handlers (unchanged path; books handlers) | Books HTTP handlers; the auth/user/API-key/invite handlers become core `auth` routes at S4 and scheduler admin a core `tasks` route at S5 (their only ABO caller, `internal/server`'s wiring, is deleted in the same PR; MOD-2 gate); the activity routes serve the books feed and stay; plugin handlers die with `internal/plugin`. | in 4, out 47. uses `database.Book/Collection/BookFile` (`ai.go:44`; 249 lines, raw/02 a1); imports `plugins/maintenance` (`activity_compact.go:22`), `server/absauth`; 47 module imports.<br>**Split:** **mediacore/internal/authhttp** ← `auth.go`, `apikeys.go`, `oauth_login.go`, `user.go`, `user_credentials.go`, `web_session_revoke.go`, `invite_delivery.go` (2535 LOC); **mediacore/internal/taskhttp (scheduler admin)** ← `scheduler_admin.go` (543 LOC); **mediacore/health (trace download)** ← `diagnostics_traces.go` (89 LOC); **delete (legacy plugin handlers)** ← `plugins.go` (188 LOC) |
| <a id="p-internal-server-handlers-abs"></a>`internal/server/handlers/abs` | 5 | 16070 | audiobook | 2/15 | internal/server/handlers/abs (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "implements the Audiobookshelf-compatible auth surface (design spec Phase 1): GET /ping, GET /status, POST". | in 2, out 15; imports audiobook pkgs: `internal/audioutil`, `internal/covers`, `internal/metadata`, `internal/searchcache` (+2); importers: `internal/server`, `internal/server/handlers` |
| <a id="p-internal-server-handlers-admindebug"></a>`internal/server/handlers/admindebug` | 5 | 1030 | mixed | 1/5 | internal/server/handlers/admindebug (unchanged) | Debug-edit of Book/BookFile rows with undo images (`admin_debug_edit:`). | in 1, out 5; imports audiobook pkgs: `internal/merge`; importers: `internal/server` |
| <a id="p-internal-server-handlers-aibackends"></a>`internal/server/handlers/aibackends` | 5 | 420 | generic | 1/4 | internal/server/handlers/aibackends (unchanged) | Toggles ABO's local-Ollama vs remote backend mode (managed tool lifecycle); one consumer. | in 1, out 4. imports `aidispatch`, `config`, `httputil`, `tools`; `aibackends.go:420`. |
| <a id="p-internal-server-handlers-audiobooks"></a>`internal/server/handlers/audiobooks` | 5 | 4056 | audiobook | 1/27 | internal/server/handlers/audiobooks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 27; imports audiobook pkgs: `internal/audiobooks`, `internal/batch`, `internal/merge`, `internal/metadata` (+6); importers: `internal/server` |
| <a id="p-internal-server-handlers-audiobooks-mocks"></a>`internal/server/handlers/audiobooks/mocks` | 5 | 3473 | audiobook | 1/5 | internal/server/handlers/audiobooks/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 5; imports audiobook pkgs: `internal/audiobooks`, `internal/batch`, `internal/metafetch`; importers: `internal/server/handlers/audiobooks` |
| <a id="p-internal-server-handlers-dedup"></a>`internal/server/handlers/dedup` | 5 | 4407 | audiobook | 1/13 | internal/server/handlers/dedup (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 13; imports audiobook pkgs: `internal/dedup`, `internal/dedup/dataset`, `internal/itunesguard`, `internal/merge` (+1); importers: `internal/server` |
| <a id="p-internal-server-handlers-dedup-mocks"></a>`internal/server/handlers/dedup/mocks` | 5 | 1600 | audiobook | 1/4 | internal/server/handlers/dedup/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 4; imports audiobook pkgs: `internal/dedup`, `internal/merge`; importers: `internal/server/handlers/dedup` |
| <a id="p-internal-server-handlers-duplicates"></a>`internal/server/handlers/duplicates` | 5 | 771 | audiobook | 1/9 | internal/server/handlers/duplicates (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 9; imports audiobook pkgs: `internal/dedup`, `internal/merge`, `internal/metadata`, `internal/undo`; importers: `internal/server` |
| <a id="p-internal-server-handlers-duplicates-mocks"></a>`internal/server/handlers/duplicates/mocks` | 5 | 681 | audiobook | 1/4 | internal/server/handlers/duplicates/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 4; imports audiobook pkgs: `internal/merge`, `internal/metadata`; importers: `internal/server/handlers/duplicates` |
| <a id="p-internal-server-handlers-entities"></a>`internal/server/handlers/entities` | 5 | 2187 | audiobook | 1/9 | internal/server/handlers/entities (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 9; imports audiobook pkgs: `internal/audiobooks`, `internal/dedup`, `internal/undo`, `internal/work`; importers: `internal/server` |
| <a id="p-internal-server-handlers-entities-mocks"></a>`internal/server/handlers/entities/mocks` | 5 | 2602 | audiobook | 1/4 | internal/server/handlers/entities/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 4; imports audiobook pkgs: `internal/audiobooks`, `internal/work`; importers: `internal/server/handlers/entities` |
| <a id="p-internal-server-handlers-fpworker"></a>`internal/server/handlers/fpworker` | 5 | 228 | audiobook | 2/2 | internal/server/handlers/fpworker (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "serves the remote fingerprint worker API (/api/v1/fingerprint/worker/*): fp-worker processes on other hosts". | in 2, out 2; imports audiobook pkgs: `internal/fingerprint/workerapi`; importers: `internal/fingerprint/workerclient`, `internal/server` |
| <a id="p-internal-server-handlers-metadata"></a>`internal/server/handlers/metadata` | 5 | 2466 | audiobook | 1/17 | internal/server/handlers/metadata (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 17; imports audiobook pkgs: `internal/applygate`, `internal/catalog`, `internal/merge`, `internal/metabatch` (+4); importers: `internal/server` |
| <a id="p-internal-server-handlers-metadata-mocks"></a>`internal/server/handlers/metadata/mocks` | 5 | 2901 | audiobook | 1/3 | internal/server/handlers/metadata/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 3; imports audiobook pkgs: `internal/metafetch`; importers: `internal/server/handlers/metadata` |
| <a id="p-internal-server-handlers-mocks"></a>`internal/server/handlers/mocks` | 5 | 9993 | mixed | 2/12 | internal/server/handlers/mocks (unchanged) | Mocks of books handler dependencies (importer, iTunes, AI pipeline). | in 2, out 12; imports audiobook pkgs: `internal/dedup`, `internal/importer`, `internal/itunes/service`, `internal/merge` (+2); importers: `internal/server`, `internal/server/handlers` |
| <a id="p-internal-server-handlers-opaccess"></a>`internal/server/handlers/opaccess` | 5 | 885 | mixed | 7/10 | internal/server/handlers/opaccess (unchanged) | Access rules for repair plans and per-user result views; tied to repairs, undo, merge, usermerge. | in 7, out 10. imports `repairs` (`repairs.go:24`), `undo` (`:26`), `merge`, `usermerge`, `diagnostics`; 7 importers. |
| <a id="p-internal-server-handlers-operations"></a>`internal/server/handlers/operations` | 5 | 1052 | mixed | 1/14 | internal/server/handlers/operations (unchanged path; `/api/v1/operations` compat surface) | Legacy v2 operations API kept as a compatibility window (09); generic pause/resume and run listing are served natively by core `taskhttp`. | in 1, out 14. imports `audiobooks` (`handler.go:44`), `undo` (`:53`); uses `database.BookAuthor/BookNarrator/Author` (`handler.go:276-313`); `pause.go`.<br>**Split:** **mediacore/internal/taskhttp (pause/resume)** ← `pause.go` (170 LOC) |
| <a id="p-internal-server-handlers-operations-mocks"></a>`internal/server/handlers/operations/mocks` | 5 | 2022 | mixed | 1/3 | internal/server/handlers/operations/mocks (unchanged) | Mocks for the books operations handler (Author/Narrator/Book types). | in 1, out 3; imports: `internal/database`, `internal/operations/registry`, `internal/scheduler`; importers: `internal/server/handlers/operations` |
| <a id="p-internal-server-handlers-repairs"></a>`internal/server/handlers/repairs` | 5 | 710 | audiobook | 1/9 | internal/server/handlers/repairs (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "serves the Repairs lane of /review (/api/v1/repairs/*): list the fixers, start a plan, page a stored plan's". | in 1, out 9; imports audiobook pkgs: `internal/repairs`; importers: `internal/server` |
| <a id="p-internal-server-handlers-review"></a>`internal/server/handlers/review` | 5 | 1390 | audiobook | 2/6 | internal/server/handlers/review (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "hosts the universal review-queue HTTP handlers (PR-A1)". | in 2, out 6; imports audiobook pkgs: `internal/itunes/service`, `internal/merge`; importers: `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-server-handlers-system"></a>`internal/server/handlers/system` | 5 | 1402 | mixed | 1/14 | internal/server/handlers/system (unchanged path) | System handler mixes backup/restore (core `backup` routes), system status/logs (core `health`/`log`) and ABO dedup/metafetch/organizer actions. | in 1, out 14. imports `dedup` (`handler.go:44`), `metafetch` (`:47`), `backup`; uses `database.BookCore/Author` (`interfaces.go:39`); backup routes `handler.go:769-776,862`. |
| <a id="p-internal-server-handlers-system-mocks"></a>`internal/server/handlers/system/mocks` | 5 | 1866 | mixed | 1/3 | internal/server/handlers/system/mocks (unchanged) | Mocks for the system handler's books store (BookCore/Author). | in 1, out 3; imports: `internal/config`, `internal/database`, `internal/sysinfo`; importers: `internal/server/handlers/system` |
| <a id="p-internal-server-handlers-tools"></a>`internal/server/handlers/tools` | 5 | 92 | generic | 1/1 | internal/server/handlers/tools (unchanged) | HTTP for ABO-managed external tools (Ollama, fpcalc). | in 1, out 1. imports `internal/tools` only. |
| <a id="p-internal-serverdecode"></a>`internal/serverdecode` | 0 | 45 | generic | 2/0 | internal/serverdecode (unchanged) | Switch for in-process audio decoding; audio-specific despite the generic class in raw/01. | in 2, out 0. stdlib; `internal/serverdecode` 45 LOC; importers ABO. |
| <a id="p-internal-sweep"></a>`internal/sweep` | 3 | 373 | mixed | 4/4 | internal/sweep (unchanged) | Tombstone/temp sweeps over `Book`/`BookCore` rows. | in 4, out 4; imports: `internal/activity`, `internal/database`, `internal/logger`, `internal/pathutil`; importers: `internal/audiobooks`, `internal/plugins/maintenance`, `internal/server` (+1) |
| <a id="p-internal-syncapi-conformance"></a>`internal/syncapi/conformance` | 0 | 333 | audiobook | 1/0 | internal/syncapi/conformance (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "diffs this server's Audiobookshelf-compatible API responses against golden fixtures captured from a real". | in 1, out 0; importers: `internal/server/handlers/abs` |
| <a id="p-internal-syncapi-progress"></a>`internal/syncapi/progress` | 0 | 405 | audiobook | 5/0 | internal/syncapi/progress (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "implements the ABS-sync progress conflict-resolution policy from docs/specs/2026-07-29-abs-sync-api-design.md". | in 5, out 0; importers: `internal/audiobooks`, `internal/database`, `internal/maintenance/jobs` (+2) |
| <a id="p-internal-sysinfo"></a>`internal/sysinfo` | 3 | 694 | mixed | 3/3 | internal/sysinfo (unchanged path; library dashboard) | Host memory/uptime and service status are health diagnostics; dashboard counts (books, playlists) are books. | in 3, out 3. uses `database.Playlist` (`dashboard_service.go:23`), `Books` count `:38,68`; imports `config`, `database`, `serviceregistry`.<br>**Split:** **mediacore/health (host stats, service status)** ← `memory.go`, `memory_linux.go`, `uptime.go`, `uptime_linux.go`, `service.go` (540 LOC); **delete (serviceregistry)** ← `register.go` (21 LOC); **internal/sysinfo (ABO)** ← `dashboard_service.go` (133 LOC) |
| <a id="p-internal-tagger"></a>`internal/tagger` | 3 | 744 | audiobook | 12/4 | internal/tagger (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 12, out 4; imports: `internal/config`, `internal/database`, `internal/fileops`, `internal/logger`; importers: `cmd`, `internal/audiobooks`, `internal/deluge` (+9) |
| <a id="p-internal-telemetry-contract"></a>`internal/telemetry/contract` | 0 | 0 | generic | 0/0 | internal/telemetrypolicy (renamed; stays in the root module) | Test-only suite that pins ABO's series names and walks the ABO tree; moving it into core reproduces the E2 hazard (core `go mod tidy` pulls the app module, 03 §3.2.1), so it stays in ABO (MOD-6). | in 0, out 0. 0 non-test LOC, 1531 test LOC (raw/01); `series_contract_test.go:51-56` imports `internal/ai`, `aidispatch`, `database`, `metrics`, `opsmetrics`; `declared_families_test.go` walks `internal/`, `pkg/` and `cmd/`. |
| <a id="p-internal-testutil"></a>`internal/testutil` | 4 | 415 | mixed | 2/6 | internal/testutil (unchanged) | Integration fixtures importing metadata, scanner, itunes. | in 2, out 6; imports audiobook pkgs: `internal/itunes`, `internal/metadata`, `internal/scanner`; importers: `internal/itunes/service`, `internal/server` |
| <a id="p-internal-testutil-rapidgen"></a>`internal/testutil/rapidgen` | 3 | 243 | mixed | 4/1 | internal/testutil/rapidgen (unchanged) | Property-test generators for Book/Author/BookVersion. | in 4, out 1; imports: `internal/database`; importers: `internal/audiobooks`, `internal/database`, `internal/playlist` (+1) |
| <a id="p-internal-titleutil"></a>`internal/titleutil` | 0 | 172 | audiobook | 4/0 | internal/titleutil (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "provides shared helpers for normalising book titles". | in 4, out 0; importers: `internal/database`, `internal/itunes/service`, `internal/metadata` (+1) |
| <a id="p-internal-tools"></a>`internal/tools` | 0 | 704 | mixed | 7/0 | internal/tools (unchanged) | Managed external binaries (Ollama daemon, fpcalc) and a book-ID embed queue; ABO pipeline tooling, one consumer. | in 7, out 0. embed queue keyed by `book_id` `embed_queue.go:66`; `config_types.go` names the audiobook pipeline; 7 importers. |
| <a id="p-internal-trackseq"></a>`internal/trackseq` | 0 | 164 | audiobook | 1/0 | internal/trackseq (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "answers one question: what sequence number does this audiobook filename carry? It exists because that". | in 1, out 0; importers: `internal/scanner` |
| <a id="p-internal-transcode"></a>`internal/transcode` | 3 | 489 | audiobook | 3/4 | internal/transcode (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 4; imports audiobook pkgs: `internal/audioutil`; importers: `internal/audio`, `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-transcribe"></a>`internal/transcribe` | 3 | 8306 | audiobook | 2/4 | internal/transcribe (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "extracts and transcribes the opening seconds of an audio file". | in 2, out 4; imports: `internal/ai/resultjournal`, `internal/aidispatch`, `internal/config`, `internal/logger`; importers: `internal/plugins/maintenance`, `internal/reconcile` |
| <a id="p-internal-undo"></a>`internal/undo` | 3 | 7214 | audiobook | 18/2 | internal/undo (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 18, out 2; imports audiobook pkgs: `internal/merge`; importers: `internal/activity`, `internal/audiobooks`, `internal/authormerge` (+15) |
| <a id="p-internal-usermerge"></a>`internal/usermerge` | 3 | 2561 | audiobook | 4/5 | internal/usermerge (unchanged) | Merges one account's per-user books data into another; the per-user data is books-owned. | in 4, out 5. audiobook-specific per raw/01 (`apply.go:17`); `UserMerger` capability `internal/database/user_merge.go:384`. |
| <a id="p-internal-util"></a>`internal/util` | 0 | 1478 | mixed | 23/0 | internal/util (unchanged path; audiobook string helpers) | Mostly audiobook string rules (credits, narrator, Bible title sort, transcript match). Email normalization is needed by core auth; SafeJoin and file modes by core library. | in 23, out 0. `NarratorCreditVerdict` `narrator_credit.go:15`; `title_sort.go` numbered Bible books; `NormalizeEmail` used by `oauth`; 23 importers.<br>**Split:** **mediacore/auth (email normalization)** ← `email.go` (55 LOC); **mediacore/library (SafeJoin, permission modes)** ← `path.go`, `perms.go` (57 LOC) |
| <a id="p-internal-versionprimary"></a>`internal/versionprimary` | 3 | 1619 | audiobook | 16/4 | internal/versionprimary (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the ONE rule for which member of a version group is its primary". | in 16, out 4; imports audiobook pkgs: `internal/audioutil`; importers: `internal/audiobooks`, `internal/batch`, `internal/dedup` (+13) |
| <a id="p-internal-versionprimary-vptest"></a>`internal/versionprimary/vptest` | 3 | 202 | audiobook | 15/1 | internal/versionprimary/vptest (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "seeds version groups on a real PebbleStore for the tests of every path that hands a group's primary flag on". | in 15, out 1; imports: `internal/database`; importers: `internal/audiobooks`, `internal/batch`, `internal/dedup` (+12) |
| <a id="p-internal-versions"></a>`internal/versions` | 3 | 6194 | audiobook | 4/8 | internal/versions (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 4, out 8; imports audiobook pkgs: `internal/filehash`, `internal/merge`; importers: `internal/audiobooks`, `internal/importer`, `internal/plugins/maintenance` (+1) |
| <a id="p-internal-wirecontract"></a>`internal/wirecontract` | 0 | 0 | generic | 0/0 | internal/wirecontract (unchanged) | Golden JSON contract tests for ABO's tygo types; core gets its own contract tests from its OpenAPI document. | in 0, out 0. 0 non-test LOC, 262 test LOC; goldens under `web/src/types/contract/testdata/` (raw/09 §5). |
| <a id="p-internal-work"></a>`internal/work` | 3 | 102 | audiobook | 3/2 | internal/work (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 2; imports: `internal/database`, `internal/serviceregistry`; importers: `internal/server`, `internal/server/handlers/entities`, `internal/server/handlers/entities/mocks` |
| <a id="p-tools-cmd-dedup-dataset-audit"></a>`tools/cmd/dedup-dataset-audit` | 6 | 270 | audiobook | 0/1 | tools/cmd/dedup-dataset-audit (unchanged) | Audiobook maintenance CLI. | in 0, out 1. audiobook-specific per raw/01; no importer. |
| <a id="p-tools-cmd-itunes-group-preview"></a>`tools/cmd/itunes-group-preview` | 6 | 83 | audiobook | 0/2 | tools/cmd/itunes-group-preview (unchanged) | Audiobook maintenance CLI. | in 0, out 2. audiobook-specific per raw/01; no importer. |
| <a id="p-tools-cmd-merge-split-books"></a>`tools/cmd/merge-split-books` | 6 | 264 | audiobook | 0/0 | tools/cmd/merge-split-books (unchanged) | Audiobook maintenance CLI. | in 0, out 0. audiobook-specific per raw/01; no importer. |
| <a id="p-tools-cmd-orphan-nonprimary-census"></a>`tools/cmd/orphan-nonprimary-census` | 6 | 474 | audiobook | 0/0 | tools/cmd/orphan-nonprimary-census (unchanged) | Audiobook maintenance CLI. | in 0, out 0. audiobook-specific per raw/01; no importer. |
| <a id="p-tools-cmd-reconcile-book-counts"></a>`tools/cmd/reconcile-book-counts` | 6 | 260 | audiobook | 0/2 | tools/cmd/reconcile-book-counts (unchanged) | Audiobook maintenance CLI. | in 0, out 2. audiobook-specific per raw/01; no importer. |
| <a id="p-tools-cmd-reconcile-paths"></a>`tools/cmd/reconcile-paths` | 6 | 657 | audiobook | 0/1 | tools/cmd/reconcile-paths (unchanged) | Audiobook maintenance CLI. | in 0, out 1. audiobook-specific per raw/01; no importer. |

### 3.6 Delete (11 packages, 34861 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|
| <a id="p-internal-bookfiles"></a>`internal/bookfiles` | 3 | 105 | audiobook | 0/2 | - | Dead: no importer in non-test or test code. `FirstAudioFile` helpers were split out only to keep the SDK dependency ratchet small (`first_audio_file.go:6-9`), a reason that disappears with `pkg/plugin/sdk`. | in 0, out 2. no importer (raw/01) and 0 test importer files (grep at 748fd787a); only reference is its `layerOf` key `internal/arch/layering_test.go:153`. |
| <a id="p-internal-database-mocks"></a>`internal/database/mocks` | 3 | 31462 | mixed | 23/1 | - (successor: storetest in-memory store + per-consumer narrow mocks) | Mockery mock of the full 445-method `Store`. After the split there is no full Store to mock; recreating a 333-method `books.Store` only to keep this mock would re-widen the coupling CLAUDE.md's worked example removed. Cost surfaced: 114 test files use it. | in 23, out 1. `internal/database/mocks/mock_store.go:4564`, 31,408 LOC (raw/04 §5); 114 test importer files (counted at 748fd787a). |
| <a id="p-internal-logger-mocks"></a>`internal/logger/mocks` | 0 | 300 | generic | 1/0 | - | Mocks `ActivityLogWriter`/`RetentionStore` of the retired printf logger; core regenerates mocks from its own interfaces. | in 1, out 0. `mock_activity_log_writer.go`, `mock_retention_store.go`; 2 test importer files. |
| <a id="p-internal-maintenance"></a>`internal/maintenance` | 4 | 630 | mixed | 4/3 | - (successor: mediacore/tasks definitions; temporary adapter in internal/books) | `MaintenanceJob` is the v2.5 job shape N9 retires; its global `Register` panics on duplicates and is populated from 34 `init()`s (R5). The 60-method `JobStore` straddles core and books (§4.3). | in 4, out 3. `MaintenanceJob` `job.go:474-499`; `JobStore` `job.go:442`; `registry.go:19-24` panics on duplicate; uses `database.Book/BookFile/BookAuthor` (`job.go:254-256`); 4 importers. |
| <a id="p-internal-opscatalog"></a>`internal/opscatalog` | 4 | 385 | mixed | 1/7 | - (successor: module.Registrar + kernel catalog validation) | The v3 bundle catalog is empty and hard-lists ABO plugin bundles; module registration replaces it. Op-ID tombstones become the books module's task-kind alias list. | in 1, out 7. `var All = []func(Deps) ops.Bundle{}` `catalog.go:56`; imports `plugins/acoustid/dedup/itunes/metafetch` `catalog.go:28-32`; 1 importer (`internal/server`).<br>**Split:** **internal/books (task-kind aliases)** ← `tombstones.go` (98 LOC) |
| <a id="p-internal-plugin"></a>`internal/plugin` | 3 | 477 | mixed | 10/2 | - (successor: mediacore/events, mediacore/integrations/notify, module.Registrar) | Legacy runtime plugin registry with one implementor (webhook), a goroutine-per-handler bus using the publisher ctx, an unauthenticated route mount, and three vestigial capability interfaces. | in 10, out 2. `globalRegistry` `registry.go:15,45`; bus `events.go:85-103` (not as designed, raw/07 §2); `EventBook*` + `Event.BookID` `events.go:19-37`; `DownloadClient` zero implementors `plugin.go:52-63`; mount bypasses auth `internal/server/plugins_init.go:42` (raw/10 G8). |
| <a id="p-internal-plugins"></a>`internal/plugins` | 4 | 0 | generic | 0/0 | - (successor: R7 `tasks.Parallelism` has no zero value) | Test-only lint that every `RunItems` call states non-zero `Concurrency`; R7 makes the property a type error. Kept until the last v2 `RunItems` call site is migrated, then deleted. | in 0, out 0. 0 non-test LOC; `internal/plugins/runitems_concurrency_lint_test.go:106-128`; ~170 call sites (raw/06 §2). |
| <a id="p-internal-serviceregistry"></a>`internal/serviceregistry` | 0 | 532 | mixed | 24/0 | - (successor: mediacore/kernel container, explicit construction) | Process-global factory map filled by 43 `init()` registrations; N4/R5 forbid it. Typed keys include audiobook names. | in 24, out 0. `var registered = map[string]ServiceDef{}` (no mutex) `registry.go:38`; `KeyAudiobook` `keys.go:14`; 24 importers; `IncludeGroup` init-by-import coupling (raw/07 §6.4). |
| <a id="p-internal-writeback"></a>`internal/writeback` | 3 | 143 | audiobook | 0/1 | - (concept superseded by mediacore/outbox) | Dead: a durable ITL write-back outbox that nothing imports (`outbox.go` header: backlog 4.3). The same need is met by core's same-batch `outbox` (03 §3.3) when the iTunes write-back is ported. | in 0, out 1. no importer and 0 test importer files; only reference `internal/arch/layering_test.go:210,530`. |
| <a id="p-pkg-plugin-sdk"></a>`pkg/plugin/sdk` | 4 | 519 | mixed | 10/2 | - (successor: mediacore/tasks + module.Registrar) | Type-alias facade over the v2 registry that hands `database.Book` out; N4 retires it. `PageBooks` keepalive paging is a books helper; `Progress` becomes a tasks helper. | in 10, out 2. aliases onto `internal/operations/registry` (`operation.go:11-18`, `reporter.go:12`, `events.go:11-15`); `iterate.go:12,26,46` uses `database.Book`; 10 importers (6 plugins + importer, reconcile, scheduler, server).<br>**Split:** **internal/books (`PageBooks`)** ← `iterate.go` (100 LOC); **mediacore/tasks (progress helper)** ← `progress.go` (120 LOC) |
| <a id="p-tools-cmd-sdkguard"></a>`tools/cmd/sdkguard` | 6 | 308 | generic | 0/0 | - | Guards `pkg/plugin/sdk`'s internal-dependency allow-list; the SDK is retired (N4) and R1/R2 `archtest` guards replace the check for every core package. | in 0, out 0. allow-list `tools/cmd/sdkguard/main.go:77-80` (registry, auth, database, serviceregistry); hard-codes `internal/` prefix `:66` (raw/03 §3.10). |


## 4. The keystone: `internal/database`

### 4.1 What is being split

`internal/database` is 103,432 non-test LOC in 272 files (raw/01), imported by 104 of the 213
packages (raw/02 finding 1). `database.Store` (`internal/database/store.go:48-55`) is pure
composition of six unexported composites — `catalogStore` 132, `mediaStore` 75, `accountStore` 44,
`enrichmentStore` 60, `operationsStore` 92, `platformStore` 42 = 445 methods
(`store.go:58,70,99,111,123,134`; raw/04 §1). A further 115 exported capability interfaces live off
`Store` and are reached through `database.AsCapability[T]` (`store_capability.go:89`) or concrete
`*PebbleStore` assertions (raw/04 §6). The only production implementation is `*PebbleStore`
(`pebble_store.go:107`; 772 exported methods).

The package also imports seven audiobook packages (`fingerprint`, `matcher`, `titleutil`,
`personname`, `metastate`, `chaptershape`, `syncapi/progress`) and `models` (raw/02 finding 2).
None of those edges survives into core: every file that needs them stays in the books residue.

### 4.2 Method-level cut of `database.Store`

| composite (methods) | to core | to books | leaf → target |
|---|---|---|---|
| `catalogStore` (132) | 0 | 132 | `BookStore` 63, `AuthorStore` 25, `SeriesStore` 11, `NarratorStore` 8, `WorkStore` 9, `BookVersionStore` 10, `CollectionStore` 6 → books |
| `mediaStore` (75) | 0 | 75 | `BookFileStore` 44, `BookSegmentStore` 6, `ChapterReader` 1, `PlaylistStore` 5, `UserPlaylistStore` 9, `PathHistoryStore` 2, `PlaybackStore` 8 → books |
| `accountStore` (44) | 39 | 5 | `UserReader`+`UserWriter` 7, `SessionStore` 6, `OAuthIdentityStore` 3, `APIKeyStore` 9, `InviteStore` 5, per-user preferences 3 (`SetUserPreferenceForUser`, `GetUserPreferenceForUser`, `GetAllPreferencesForUser`) → `auth`; `RoleStore` 6 → `rbac`; `DeleteExpiredABSSessions` 1 and the 4 global preference methods (`Get/Set/Delete/GetAllUserPreferences`, which also hold per-book metadata state, `internal/metastate`) → books |
| `enrichmentStore` (60) | 0 | 60 | `MetadataStore` 7, `MetadataCacheStore` 4, `RejectedMetadataStore` 3, `ExternalIDStore` 11, `TagStore` 26, `UserTagStore` 4, `HashBlocklistStore` 5 → books |
| `operationsStore` (92) | 54 | 38 | `OpsV2Store` 39 → behind the `tasks` contract: ABO callers re-point to `tasks`, the implementation stays in the residue until 05-tasks T8 deletes it but is not called for writes after T0: core claims the ops families (books keeps `opstate:`, 05-storage §5S.9.1), reads them in place (`internal/executor/legacyhist`) and writes only `c/tasks/` (05-tasks §5T.6); `SystemActivityStore` 3 → `log` (the `syslog:` rows, read in place by the journal adapter, 05-logging §5); `AIJobsStore` 10 → `integrations/ai`; `Optimize`+`CompactionStats` 2 → `storage`; `OperationStore` v1 23 → books *legacy* (deleted when its last caller moves; core starts with v2/v3 run state only, N9); `OperationChangeStore` 4 (undo journal keyed by `BookID`, `store.go:634-656`) → books; `MaintenanceStore` scan-cache/dirty-folder 6, `ReviewStore` 5 → books |
| `platformStore` (42) | 15 | 27 | `LifecycleStore` 2, `RawKVStore` 7 → `storage` (keyspace-scoped); `ImportPathStore` minus `CountBooksByPathPrefix` 6 → `library`; `SettingsStore` 4 → books (`setting:` is books' runtime-marker store with 56 non-test `SetSetting` call sites; core `config`/`secrets` read `config_blob` and the 4 secret rows through a books-provided legacy source, cross-cutting resolution 7, 09 S3); `CountBooksByPathPrefix` 1, `ITunesStateStore` 6, `StatsStore` 8, `UserPositionStore` 8 → books |
| **total 445** | **108** | **337** | |

Core gets **no** wide composite in return: each core service exposes its own small Go API
(05-storage, 05-tasks, 05-authn, 05-rbac, 05-library, 05-config, 05-ai, 05-logging), and the books
residue does not re-create a 337-method `Store` either (§4.6).

### 4.3 How the narrowed consumer interfaces guide the cut

raw/04 §4 counted 591 interfaces outside `internal/database` whose method set is a subset of
`Store`. Re-running the scan at the evidence commit with each method tagged by the target in §4.2
(go/ast walk, embedded `database.*` interfaces resolved; helper outside the repository) finds 587
(the 4-interface difference comes from how unresolvable embeds are skipped) with median width 3.
Re-run for v1.1.0 with `SettingsStore` tagged books. Classified by target:

| class | interfaces | what it means for the cut |
|---|---|---|
| books-only | 465 | already the right shape; they re-point from `database` to `internal/books/store` by import rewrite only |
| core-only | 46 | consumers of identity, roles, run state; they re-point to the core service API (e.g. `AuthStore` `internal/server/handlers/auth.go:82`, `UserStore` `internal/server/handlers/user.go:42`, `UserStore` `internal/oauth/resolve.go:32`, `APIKeyHandlerStore` `internal/server/handlers/apikeys.go:114`, `authSessionStore` `internal/server/middleware/auth.go:38`) |
| raw-KV-only | 29 | books code using `SetRaw/GetRaw/ScanPrefix` over its own key families (e.g. 9 in `internal/merge`); they become methods of the books module's `storage.Keyspace` handle (R8), not a dependency on a core service |
| straddling | 47 | need **two** dependencies after the split (a books store interface plus a core service handle in `module.Deps`); these are the real rewiring work |

The straddling set is where the cut is visible. The widest:

| interface | declared at | methods | targets |
|---|---|---|---|
| `ServerOpsStore` | `internal/server/server_ops_store.go:42` | 102 | books 71, books-legacy 11, core:auth 10, core:tasks 5, core:library 3, core:storage 2 |
| `OpsStore` | `internal/plugins/maintenance/deps.go:403` | 91 | books 79, core:storage 9, core:auth 2, core:library 1 |
| `JobStore` | `internal/maintenance/job.go:442` | 70 | books 52, books-legacy 9, core:storage 7, core:auth 1, core:tasks 1 |
| `opRegistryStore` | `internal/operations/registry/register.go:51` | 40 | core:tasks 39, books 1 |
| `opsHousekeeping` | `internal/plugins/maintenance/deps.go:304` | 22 | books 11, core:storage 9, core:auth 2 |
| `OperationsStore` | `internal/server/handlers/operations/interfaces.go:74` | 21 | books 16, books-legacy 3, core:tasks 2 |
| `SystemStore` | `internal/server/handlers/system/interfaces.go:108` | 21 | books 18, core:log 1, core:storage 1, core:tasks 1 |
| `serverOperationStore` | `internal/server/server_ops_store.go:77` | 17 | books-legacy 11, core:tasks 5, books 1 |
| `IngestStore` | `internal/versions/store.go:96` | 15 | books 9, core:library 6 |
| `Store` | `internal/diagnostics/store.go:41` | 12 | books 10, core:log 1, core:tasks 1 |
| `jobOperationStore` | `internal/maintenance/job.go:417` | 12 | books-legacy 9, books 2, core:tasks 1 |
| `UserStateRepairStore` | `internal/merge/repair_user_state.go:32` | 12 | books 8, core:storage 3, core:auth 1 |

Three conclusions drive the plan:

1. **The cut line is real, not invented.** 465 + 46 + 29 = 540 of 587 consumer interfaces fall
   wholly on one side. The cut follows the composites' existing seams except inside
   `accountStore` (ABS sessions, global preferences), `operationsStore` (v1, undo journal,
   scan cache, review) and `platformStore` (stats, iTunes, positions).
2. **`opRegistryStore` proves the executor's only books dependency is `GetBookByID`** (39 of its 40
   methods are `OpsV2Store`). That one method backs the book-field `Requires` predicates in
   `internal/operations/registry/deps.go:53,78,84-125`; it becomes a subject-resolver callback the
   books module registers, which is how `internal/executor` loses its import of books types.
3. **The three widest interfaces (`ServerOpsStore`, `OpsStore`, `JobStore`) are composition-root
   bags**, not consumers: each is split into its books part and explicit core handles. `JobStore`
   disappears with `internal/maintenance` (N9).

### 4.4 Capability interfaces off `Store` (raw/04 §6)

`OpsV3Store` and its parts (`iface_ops_v3.go:397-488`), `OpRowStore`, `OperationsV2SinceLister`
stay in the residue with ABO's legacy run-row writers until 05-tasks T8 deletes them (their row
types are named by about 30 root packages today; core's own run store writes `c/tasks/`, 05-tasks
§5T.6). `ActivityStorer`/`ActivityReader`/`ActivityWriter`/`ActivityCounter`/`ActivityLifecycle`/
`ActivityRetention`/`ActivityPartialQuerier`/`ActivityFilterIndexBackfiller` stay with books: the
activity feed is the books module's (03 §3.3 `log` row, 05-logging §5). To core: `RawSnapshot`, `RawSnapshotter`,
`RawBatchApplier`, `PrefixPageScanner`, `PebbleMetricsSampler`, `DBCensusProvider`,
`DBCensusExactRunner` → `storage`; `SearchIndexDirtyStore`, `ChangeObserver`, `ChangeObservable` →
`storage/index`; `InviteDeliveryStore`, `InviteEmailGrantStore`, `InviteAcceptChecker`,
`InviteIDLookup`, `UserCredentialWriter`, `UserPasswordRehasher`, `StrictUserLister`,
`AdminEmailSetter`, `UnverifiedEmailDisplacer`, `VerifiedEmailMarker`, `VerifiedEmailIdentityLinker`
→ declared in public `auth`, implemented in `internal/authstore` (ABO's `internal/usermerge`,
`internal/merge`, `internal/server/handlers` and `internal/cfaccess` keep using them; MOD-2). `StoreUnwrapper` and `AsCapability` are deleted: with no decorators and no
wide interface, capability-by-assertion has nothing left to discover (`store_capability.go:60-100`;
raw/04 §5 "Decorators: none at HEAD"). `GuestServiceTokenStore` stays in the books residue (it is ABO deployment state) and is consumed by `internal/cfaccess`; every
other capability interface (Book*, Author*, Series*, Retired*, Sync*, Bookmark*, Review*, Narrator*,
Collection*, FileProvenance*, VectorANN*, …) stays with books.

### 4.5 File-level split

| destination | files | non-test LOC |
|---|---|---|
| `mediacore/storage` + `mediacore/internal/pebble` | 16 | 4,108 |
| `mediacore/storage/index` | 3 | 401 |
| `mediacore/auth` (token helpers, capability interfaces) | 3 | 137 |
| `mediacore/internal/authstore` | 5 | 3,053 |
| `mediacore/integrations/ai` | 2 | 292 |
| `mediacore/integrations/providerhttp` | 1 | 74 |
| `mediacore/library` | 1 | 261 |
| `mediacore/secrets` (file divided) | 1 | 466 |
| delete (decorator and SQLite leftovers) | 5 | 249 |
| delete at 05-tasks T8 (legacy run-row writers; residue until then) | 15 | 7,592 |
| `internal/books/store` (residue, incl. the books activity store and `keyfamilies.go`) | 220 | 86,799 |

The exact file lists are in the `internal/database` row's *Split* cell. Four files must be
divided internally, and `keyfamilies.go` is kept whole:

| file | cut |
|---|---|
| `pebble_store.go` (6,767) | engine open/format check/close/checkpoint/ID counters/ULIDs (`:548-1060`: `NewPebbleStore`, `NewPebbleStoreInMemory`, `openPebbleChecked`, `initPebbleStore`, `Close`, `Checkpoint`, `DB`, `nextID`, `newULID`) → `internal/pebble`; the `PebbleStore` struct keeps only books fields and embeds a `storage.Keyspace`; `migrateImportPathKeys` (`:1060`) becomes a books legacy migration step |
| `store.go` (1,897) | `User`, `Session`, `Role`, `APIKey`, `Invite` types → `auth`/`rbac`; `Setting` stays (books' `setting:` store, cross-cutting resolution 7); v2 op row types stay with ABO's legacy run-row writers until 05-tasks T8; `InitializeStore` (`:1835`) → kernel's storage open; `globalStore`/`GetGlobalStore` (`:1809-1813`, zero non-test callers per raw/07 §7.3) → deleted; `Book` (`:202`), `OperationChange` (`:634`) and the composites → books |
| `migrations.go` (1,425) | the `Migration` types (`:88-111`) and the runner with its version bookkeeping (`RunMigrations` `:512`, `getCurrentVersion`/`setVersion` `:579-610`) → `storage` migrations; the hand-ordered list (`:112-509`) and the 66 bodies (`:611` onward) → the books module's legacy migration step list, gated by the existing `db_version` row so none re-runs |
| `keyfamilies.go` (408) | stays in the residue: the family list holds ABO key names and is the input of the books claim generator (05-storage §5S.9.1); the census mechanism goes to `storage` with `census*.go` |
| `settings.go` (466) | `EncryptionKeyPath`/`FindEncryptionKey`/`InitEncryption`/`EncryptValue`/`DecryptValue`/mask helpers (`settings.go:51-293`) → `secrets` (legacy key imported as the keyring's legacy entry, 05-config); `GetSetting`/`SetSetting`/`GetAllSettings`/`DeleteSetting` and `UnreadableSettingsError` (`:298-466`) stay in the residue (cross-cutting resolution 7) |

Key families (`keyfamilies.go:37-345`, 258 registered): ownership and claim mode of every family
and sub-prefix are stated once, in [05-storage](05-storage.md) §5S.9.1 (DATA-2); this file does not
restate them. What follows for the file cut: `setting:`, `preference:` and `pref:` stay books
families, with core claiming only exact keys or narrow carve-outs (cross-cutting resolution 7);
the `act:` activity families stay books; the ops families are claimed by core and read in place
(books keeps `opstate:`), and ABO's run-row store is unused for writes after 05-tasks T0 and deleted at T8; the identity families are adopted by core under 09's adoption model
(09 §9.2.3). The two shared families that need a sub-key split are `idx:` and `pref:`/`preference:`.

### 4.6 What replaces `Store` in the books module

- The composition root (`internal/books` module) holds the concrete `*store.PebbleStore`; nothing
  needs a books-wide interface (CLAUDE.md "Fix It Right" worked example: remove the wide
  parameter, do not narrow it).
- Consumer interfaces stay where they are used (458 already books-only).
- **Cost surfaced:** `internal/database/mocks` (31,408 LOC mockery mock of `Store`, 114 test
  importer files) and the hand-written `mock_store.go` (4,189 LOC, `var _ Store` at
  `mock_store.go:31`) have no successor type. The end state is: tests use
  `internal/books/store/storetest` (real store on core's in-memory engine) or a mock of their own
  narrow interface. Converting 114 + ~22 test files is the largest single test cost in the
  extraction. An interim `store.Store` composite (books methods only) may be kept **inside
  `storetest`** so the mocks regenerate mechanically while the conversion runs; it is deleted at
  the end of 09 S4 (the last stage that splits `database.Store`). The owner decides whether the conversion is done in S2b–S4 or
  deferred; the interim composite is not an acceptable end state.

### 4.7 Imports that move with each side

Core-bound database files import only `internal/logger` (→ `log`), `internal/metrics`/`telemetry`
(→ `metrics`), `internal/sdnotify`, `internal/operations/state` (→ `tasks`) and `internal/cache`
(books residue only). The seven audiobook imports and `models` are used only by residue files
(`pebble_store_lsh.go:38`, `pebble_store.go:31-35`, `pebble_store_authors.go:21`,
`metadata_field_locks.go:15`, `book_own_folder.go:15`, `pebble_store_bookmarks.go:39`,
`embedding_store.go:22`), so R1 (`archtest.TestCoreImportsOnlyCore`) holds for the core side by
construction.

## 5. `internal/models`

Two files, 337 LOC (raw/02 §b). Only the dedup-score family is live (`Signal`, `SignalKind`,
`UnifiedDedupScore`, provenance helpers; `dedup_score.go:14-188`), used by `internal/database`
(`embedding_store.go:124,178,861,867,1651-1771`), `internal/dedup/unified` (aliases
`score.go:23,106,127,135,144`) and `internal/plugins/dedup` (`calibrate_composite.go:180,200,754,778`).
It is book-pair dedup evidence: it merges into `internal/dedup/unified`, which already re-exports it
as aliases, and the aliases become the definitions. `Audiobook` (`audiobook.go:24`) moves into
`internal/itunes`, its only user (`import.go:361,394`). `Author`, `Series`, the four request/response
DTOs and the generic `FileSystemItem`/`BrowseRequest`/`ExclusionRequest`/`SystemStatus`/`LogEntry`
types have zero users and are deleted. Nothing enters core.

## 6. The `pkg/` packages

`pkg/ops`, `pkg/ops/opstest` and `pkg/plugin/sdk` are the only packages that were meant to be a
public boundary, and all three expose `database.Book` (raw/02 finding 4).

| package | decision | how `database.Book` leaves |
|---|---|---|
| `pkg/ops` | **core public `mediacore/tasks`**. The Task/Batch/Fixer/Pipeline/Schedule/Effects/CancelContract/RC model is domain-neutral (raw/06 §7.2) and is the authoring model N9 adopts. | `Writer` (`pkg/ops/writer.go:46-90`: `ModifyBook`, `ModifyBookFile`, `RepointBookFile`, `ModifyBookAuthors`) moves to `internal/books` as the books typed writer; core `tasks.Writer` is generic over `tasks.Resource{Module, Kind, ID}` (G8) with typed per-module extensions registered by the module. `SubjectType` book/book_file (`writer.go:112-121`) becomes module-declared resource kinds. `LaneMac` (`lanes.go:15`) becomes a module-declared named lane. `Common.Permission auth.Permission` (`definition.go:93`) becomes `rbac.Permission`. The omitted-`Concurrency`-means-`CPU()` default (`batch.go:123`) is replaced by R7's required `tasks.Parallelism`. |
| `pkg/ops/opstest` | **core public `mediacore/tasks/taskstest`** (conformance suite, fault injection, fake world) | Book fakes (`fakes.go:74,83,93`) move to `internal/books/store/storetest`. |
| `pkg/plugin/sdk` | **delete** (N4) | `PageBooks` (`iterate.go:41`, Book-typed `BookStore` `:25`) moves to `internal/books`; `Progress` becomes a `tasks` helper; the type aliases onto the v2 registry disappear with the registry's public surface. Its 10 importers (6 plugins, `importer`, `reconcile`, `scheduler`, `server`; raw/02 §e) re-point to `mediacore/tasks` + `module.Registrar`. |

Consequences: `tools/cmd/sdkguard` (the allow-list ratchet over `pkg/plugin/sdk`,
`tools/cmd/sdkguard/main.go:77-80`) is deleted — R1/R2 `archtest` checks cover every core package,
a strictly stronger guarantee. `tools/cmd/oplint` survives as `mediacore/archtest/tasklint`; its
hard-coded module path (`analyzer.go:45`) and the ~12 fixture packages under
`tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/...` are re-rooted on the
core module path. After this, no exported core identifier mentions a books type, which
`archtest.TestCoreImportsOnlyCore` enforces mechanically (R1).

## 7. Other splits that shape the plan

- **`internal/config`** keeps its path as the books typed config section, and its
  `config.AppConfig` façade survives over core handles (SIMP-3: R5 binds core only; moving the
  634 call sites is an optional ratchet, not a stage). Core `config` takes the
  loader (layer order flags → env → file → DB overrides, `Mutate`/`Snapshot`/`Generation`,
  `viper_decode.go`, unknown/removed-key audits, protected fields, secure state dir) and core
  `secrets` the encryption of the setting rows (`internal/database/settings.go:42-52,110-125,189-235`)
  and the secret-row persistence in `persistence.go:1959-2008`; the migration mechanism and the
  keyring are 05-config's, the compatibility window 09 S3's. `config.go` is cut by function: the
  `Config` struct's platform fields (database path, backup, OAuth, otel, auto-update) become core
  sections, audiobook fields and the ten `migrate*Blob` functions (`persistence.go:359-824`) stay.
  This removes all five wrong-way `allowed` edges from config (`layering_test.go:291-295`) because
  core `config` sits below everything that used to reach up (R4).
- **`internal/operations/registry` → `mediacore/internal/executor`.** The engine moves at S5
  (05-tasks T0); 31 root packages still call the v2 API, so the path stays in ABO as the
  `opsbridge` adapter over public `tasks` until 05-tasks T8 removes the last import, then it is
  deleted (MOD-2). Residue: `deps.go` (book-field predicates) and the `Resource` constant list
  (`types.go:384-398`) move to `internal/books`; `register.go` (serviceregistry wrapper) is deleted. The registry's v3 adapter (`v3_adapter.go`)
  becomes the internal bridge that runs `tasks.Definition`s on the executor.
- **`internal/scheduler` → `mediacore/internal/scheduler`.** `tasks.go` (1,682 LOC, 38
  `TaskDefinition`s), `extra_ops.go` and `full_sweep.go` are books declarations; in the end state
  each becomes the `Schedule` of a books `tasks.Definition`, so the three files dissolve into
  their definitions.
- **`internal/server`** stays the books HTTP adapter, mounted permanently through
  `httpapi.MountLegacy` (cross-cutting resolution 2; N7). Its core concerns are re-implemented in
  core and deleted here at the stage that makes them native: peer allow-list, CSP and security
  headers at S2 (core perimeter); bootstrap, temp login, invite accept, reset, login/invite/bootstrap
  rate limits and API-key expiry at S4; the search reconciler at S7; SPA fallback and static
  serving at S9 (FE-1). The gin 410 stubs (`gone.go`) stay with the legacy mount. Books route
  groups stay on gin unless rewritten for their own reasons.

## 8. Move order

Moves are keyed by [09](09-abo-on-core.md)'s stages (S0, S1, S2, S2b, UI-0, S3–S10; 05-tasks
batches T0–T8 inside S5 and with or after C5). R4's layers (L0 `entity`, `lifecycle`, `version`,
`log`, `metrics` → L1 `storage`, `storage/index` → L2 `secrets` → L3 `config` → L4 `events`,
`outbox`, `tasks`, `library`, `auth`, `rbac`, `audit`, `compat`, `integrations/*`, `backup`,
`updater`, `health` → L6 `httpapi`, `realtime`, `ui`, `internal/<capability>http` → L7 `module` →
L8 `kernel` → L9 `app`) constrain **imports**, not landing order: a core package lands in the first
stage that needs it, and may land before a lower-layer package only as long as **everything it
imports already exists** (COR2-9). Where a package's full contract would import something that lands
later, the package lands with a **minimum surface** that imports only what exists, and grows: the
last column of §8.1 says what exists at each stage, and 06 marks each `Manifest`/`Deps` field with
the stage that adds it. Until S2b the kernel runs with `storage` absent (no P2/P4; books opens its
store in a start hook; `Deps.Storage` is not offered; 06 §6.5.1). Deferred-build packages (L5
`acquire`, `arrfacade`, `integrations/indexer`) land in no stage.

### 8.1 Stages and the core packages they create

| stage (09) | core packages that first exist | depends on | minimum surface at this stage (what the stage's consumers need; the rest arrives later) |
|---|---|---|---|
| S0 | `core/go.mod` (empty `core/go.sum` committed, MOD2-15), `archtest` (engine from `internal/arch`), `storage/keys` (pure tuple-key functions, so S0's guard writes `c/storage/install` and the served ledger in core's encoding, DATA2-6), `GO_MODULE_DIRS := . core` | — | stdlib only |
| S1 | `log`, `metrics`, `lifecycle`, `entity`, `version`, `coretest` (log capture first; it grows each stage), `library` (path-safety leaves), `health` (flight recorder, rewritten onto a `*slog.Logger`, MOD2-11) | S0 | leaves only; no package imports ABO (`TestCoreImportsOnlyCore`) |
| S2 | `module`, `kernel`, `app`, `httpapi` (listeners, perimeter, `MountLegacy`), `config` (loader over a books-provided legacy source), `internal/sdnotify`; the `rbac` **leaf types** (`Permission`, `Requirement`, `Need`, `AnyAuthenticated`, `Public`) that `httpapi` routes name; `examples/hello` skeleton (module, app, one `httpapi` route, `openapi` subcommand emitting `core.json`; MOD2-10, FE2-9) | S1 | `httpapi` without storage-backed features; `module.Deps` without `Storage`; the kernel hands books its `ExtendStart` hook (05-health §5H.3.4) and `lifecycle` gains `NotifySocketEnv`, for the five rewired `sdnotify` users (§8.3) |
| S2b | `storage`, `internal/pebble`, `compat` (counters and ledger; the store guard is `storage`'s); `examples/hello` + its keyspace and one migration | S2 | `Deps.Storage` offered from here |
| UI-0 | `internal/openapi`; npm `@mediacore/api-client` (runtime and generator) **only**; `@mediacore/ui-kit` and `@mediacore/shell` are created at S9 (07 §7.19 step 2, FE2-4) | S2 (lands after S2 and before S4, in either order with S2b and S3) | generated client from `core.json`, which `hello` emits from S2 and every stage that adds core routes regenerates in the same PR |
| S3 | `secrets` (keyring), `config` persistence (`c/config/`), `internal/keyedmutex` (a core-internal copy of ABO's `internal/keyedmutex` for `config`, `download` and `library`; ABO keeps its own, COR2-16) | S2b | — |
| S4 | the rest of `rbac`, `auth` (+`internal/authstore`; the revocation-journal types `RevocationJournal`, `Revocation`, `RestoreHook` are declared in `auth`, and `backup` imports them at S8, COR2-9), `audit`, `events` (the bus; `auth`/`rbac` publish on it, COR2-9), `internal/authhttp` and the other `internal/<capability>http` packages S4 needs; `examples/hello` + its permission | S3, UI-0 | `events` without `outbox` (S6) |
| S5 | `tasks` (+`internal/executor`, `internal/scheduler`, `tasks/taskstest`, `archtest/tasklint`; run fencing from `opswriter/fence.go`), `internal/taskhttp`; `examples/hello` + `hello.count` (its `RunItems` task) — the first stage at which every import of 06 §6.12 exists, so G6/G7 (`TestSecondModuleNoAudiobookEdit`) are proven from S5 (COR2-9) | S4 (`tasks.Definition` carries an `rbac.Permission`, `pkg/ops/definition.go:93`) | — |
| S6 | `outbox`, `realtime` (+`internal/sse`) | S5 | no `notify` yet (webhooks move at S7, SIMP2-4) |
| S7 | `integrations` root (instance store), `integrations/providerhttp`, `integrations/ai` (+`openai`), `integrations/metadata`, `integrations/download` + `download/deluge`, `integrations/notify` + `notify/mail` + `notify/webhook`, `storage/index` + `internal/bleveindex`; the rest of `library` | S6 | — |
| S8 | `backup`, `updater`; the rest of `health`; per-App metrics registry | S7 | — |
| S9 | `ui`; npm `@mediacore/ui-kit` and `@mediacore/shell` (with `/vite` and `/testing`) and hello's web package, created in the shell-adoption PR series (07 §7.19 step 4); `examples/hello` + `ui_embed.go` (UI half of G6) | S3–S8, UI-0 | — |

### 8.2 Root-module packages rewired onto each core package

The superseded "wave" table of v1.0.0–v1.1.0 is deleted (MOD2-2): §8.1 is the stage table. Which
root-module packages must be rewired onto each core package (generated from `go list -deps -test
-json`: current importers, test files included, of a source package whose path leaves the root
module; for sources that stay in ABO after a split, the callers of the moved files; for the
`internal/database` parts, the packages that declare consumer interfaces over the moved methods,
§4.3). The count is the subset of the source's §3 importer count that stays in ABO for good or
until its own path's gate (`registry`, `scheduler`); §8.3 counts the subset still in ABO at the
gate's stage point:

| first stage | core package | sources at HEAD (stage each moves in, if later) | root-module packages that must be rewired onto it (current importers of a source that stay in ABO) |
|---|---|---|---|
| S0 | `mediacore/archtest` | `internal/arch` | -; plus callers of the moved files of `internal/arch` (those paths stay in ABO) |
| S1 | `mediacore/coretest` | `internal/database/dbtest` (S2b), `internal/logger/logtest`, `internal/mail/mailtest` (S7) | 10: `internal/audiobooks`, `internal/config`, `internal/database`, `internal/httputil`, `internal/operations/registry`, `internal/organizer`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/transcribe`; plus callers of the moved files of `internal/database/dbtest` (those paths stay in ABO) |
| S1 | `mediacore/health` | `internal/diag/flight`, `internal/server` (S8), `internal/server/handlers` (S8), `internal/sysinfo` (S8) | 0; plus callers of the moved files of `internal/server`, `internal/server/handlers`, `internal/sysinfo` (those paths stay in ABO) |
| S1 | `mediacore/library` | `internal/boundedcall`, `internal/database` (S7), `internal/diskstats`, `internal/fileops` (S7), `internal/libroot`, `internal/pathutil` (S7), `internal/security/pathvalidation`, `internal/security/safepath`, `internal/util`, `internal/watcher` (S7) | 12: `internal/audiobooks`, `internal/covers`, `internal/deluge`, `internal/maintenance/jobs`, `internal/organizer`, `internal/plugins/maintenance`, `internal/reconcile`, `internal/repairs`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/audiobooks`, `internal/server/handlers/system`; plus callers of the moved files of `internal/fileops`, `internal/pathutil`, `internal/util` (those paths stay in ABO); via `internal/database` (core:library): 10 packages declare consumer interfaces over these methods: `internal/audiobooks`, `internal/metabatch`, `internal/metafetch`, `internal/plugins/maintenance`, `internal/reconcile`, `internal/scanner`, `internal/server`, `internal/server/handlers`, `internal/sysinfo`, `internal/versions` |
| S1 | `mediacore/lifecycle` | `internal/lifecycle` | 3: `internal/aiscan`, `internal/operations/registry`, `internal/scheduler` |
| S1 | `mediacore/log` | `internal/config` (S2), `internal/logger`, `internal/logging` | 12: `internal/dedup`, `internal/httputil`, `internal/importer`, `internal/metabatch`, `internal/metafetch`, `internal/plugins/dedup`, `internal/plugins/maintenance`, `internal/scanner`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/dedup`, `internal/server/handlers/operations`; plus callers of the moved files of `internal/config`, `internal/logger` (those paths stay in ABO) |
| S1 | `mediacore/metrics` | `internal/telemetry` | 8: `.`, `cmd`, `internal/ai`, `internal/database`, `internal/operations/registry`, `internal/plugins/maintenance`, `internal/repairs`, `internal/telemetry/contract` |
| S2 | `mediacore/config` | `internal/config` | -; plus callers of the moved files of `internal/config` (those paths stay in ABO) |
| S2 | `mediacore/httpapi` | `internal/httputil`, `internal/server`, `internal/server/middleware` | 8: `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/admindebug`, `internal/server/handlers/audiobooks`, `internal/server/handlers/fpworker`, `internal/server/handlers/opaccess`, `internal/server/handlers/repairs`, `internal/server/handlers/system`; plus callers of the moved files of `internal/httputil`, `internal/server` (those paths stay in ABO) |
| S2 | `mediacore/internal/sdnotify` | `internal/sdnotify` | 5: `.`, `internal/database`, `internal/operations/registry`, `internal/search`, `internal/server` |
| S2 | `mediacore/kernel` | `internal/startup` | 3: `cmd`, `internal/server`, `internal/server/handlers/system` |
| S2b | `mediacore/internal/pebble` | `internal/metrics` | -; plus callers of the moved files of `internal/metrics` (those paths stay in ABO) |
| S2b | `mediacore/storage` | `internal/compactprogress`, `internal/database` | 3: `internal/metafetch`, `internal/plugins/maintenance`, `internal/server`; via `internal/database` (core:storage): 17 packages declare consumer interfaces over these methods: `internal/audiobooks`, `internal/authority`, `internal/covertext`, `internal/decisions`, `internal/maintenance/jobs`, `internal/merge`, `internal/metafetch`, `internal/plugins/maintenance`, `internal/plugins/metafetch`, `internal/repairs`, `internal/scanner`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/admindebug`, `internal/server/handlers/system`, `internal/undo`, `internal/usermerge` |
| UI-0 | `mediacore/internal/openapi` | `tools/cmd/opsgen` (S5) | 0 |
| S3 | `mediacore/secrets` | `internal/database` | -; via `internal/database`: no consumer interface (reached through the composition root) |
| S4 | `mediacore/audit` | `internal/server/absauth` | -; plus callers of the moved files of `internal/server/absauth` (those paths stay in ABO) |
| S4 | `mediacore/auth` | `internal/auth`, `internal/config`, `internal/database`, `internal/server`, `internal/server/absauth`, `internal/server/middleware`, `internal/util` | 15: `internal/operations/registry`, `internal/plugins/dedup`, `internal/plugins/deluge`, `internal/plugins/maintenance`, `internal/repairs`, `internal/scheduler`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/admindebug`, `internal/server/handlers/audiobooks`, `internal/server/handlers/fpworker`, `internal/server/handlers/opaccess` (+3); plus callers of the moved files of `internal/config`, `internal/server`, `internal/server/absauth`, `internal/util` (those paths stay in ABO); via `internal/database` (core:auth): 7 packages declare consumer interfaces over these methods: `internal/maintenance/jobs`, `internal/merge`, `internal/organizer`, `internal/plugins/maintenance`, `internal/server`, `internal/server/handlers`, `internal/usermerge` |
| S4 | `mediacore/auth/oidc` | `internal/oauth` | 3: `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs` |
| S4 | `mediacore/internal/authhttp` | `internal/server/handlers` | -; plus callers of the moved files of `internal/server/handlers` (those paths stay in ABO) |
| S4 | `mediacore/internal/authstore` | `internal/database` | -; via `internal/database` (core:auth): 7 packages declare consumer interfaces over these methods: `internal/maintenance/jobs`, `internal/merge`, `internal/organizer`, `internal/plugins/maintenance`, `internal/server`, `internal/server/handlers`, `internal/usermerge` |
| S4 | `mediacore/rbac` | `internal/auth` | 15: `internal/config`, `internal/operations/registry`, `internal/plugins/dedup`, `internal/plugins/deluge`, `internal/plugins/maintenance`, `internal/repairs`, `internal/scheduler`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/audiobooks`, `internal/server/handlers/fpworker` (+3) |
| S5 | `mediacore/archtest/tasklint` | `tools/cmd/oplint` | 0 |
| S5 | `mediacore/internal/executor` | `internal/operations/registry` | 33: `.`, `cmd`, `internal/catalog`, `internal/dedup`, `internal/itunes/service`, `internal/maintenance/jobs`, `internal/metabatch`, `internal/metadata`, `internal/metafetch`, `internal/plugins/acoustid`, `internal/plugins/dedup`, `internal/plugins/deluge` (+21) |
| S5 | `mediacore/internal/scheduler` | `internal/scheduler` | 5: `internal/server`, `internal/server/handlers`, `internal/server/handlers/mocks`, `internal/server/handlers/operations`, `internal/server/handlers/operations/mocks` |
| S5 | `mediacore/internal/taskhttp` | `internal/server/handlers`, `internal/server/handlers/operations`, `internal/server/handlers/opsv3` | 1: `internal/server`; plus callers of the moved files of `internal/server/handlers`, `internal/server/handlers/operations` (those paths stay in ABO) |
| S5 | `mediacore/tasks` | `internal/operations`, `internal/operations/childop`, `internal/operations/freshness`, `internal/operations/opmode`, `internal/operations/opswriter`, `internal/operations/state`, `internal/opsmetrics`, `internal/walkresume`, `pkg/ops`, `pkg/plugin/sdk` | 27: `internal/audiobooks`, `internal/database`, `internal/dedup`, `internal/importer`, `internal/itunes`, `internal/itunes/service`, `internal/maintenance/jobs`, `internal/metabatch`, `internal/metafetch`, `internal/operations/registry`, `internal/organizer`, `internal/plugins/acoustid` (+15); plus callers of the moved files of `internal/operations/opswriter` (those paths stay in ABO) |
| S5 | `mediacore/tasks/taskstest` | `pkg/ops/opstest` | 0 |
| S6 | `mediacore/realtime` | `internal/realtime` | 2: `internal/itunes/service`, `internal/server` |
| S7 | `mediacore/integrations/ai` | `internal/ai/aijobs`, `internal/ai/resultjournal`, `internal/aidispatch`, `internal/config`, `internal/database` | 10: `internal/ai`, `internal/decisions`, `internal/dedup`, `internal/plugins/maintenance`, `internal/reconcile`, `internal/scanner`, `internal/server`, `internal/server/handlers/aibackends`, `internal/telemetry/contract`, `internal/transcribe`; plus callers of the moved files of `internal/config` (those paths stay in ABO); via `internal/database`: no consumer interface (reached through the composition root) |
| S7 | `mediacore/integrations/ai/openai` | `internal/ai` | -; plus callers of the moved files of `internal/ai` (those paths stay in ABO) |
| S7 | `mediacore/integrations/download/deluge` | `internal/deluge` | -; plus callers of the moved files of `internal/deluge` (those paths stay in ABO) |
| S7 | `mediacore/integrations/notify/mail` | `internal/mail` | 2: `internal/server`, `internal/server/handlers` |
| S7 | `mediacore/integrations/notify/webhook` | `internal/plugins/webhook` | 1: `internal/server` |
| S7 | `mediacore/integrations/providerhttp` | `internal/database`, `internal/metadata`, `internal/metadata/dailyquota`, `internal/metadata/providerhttp`, `internal/security/safehttp`, `internal/server` | 3: `internal/catalog`, `internal/covers`, `internal/metafetch`; plus callers of the moved files of `internal/metadata`, `internal/server` (those paths stay in ABO); via `internal/database`: no consumer interface (reached through the composition root) |
| S7 | `mediacore/internal/bleveindex` | `internal/search` | -; plus callers of the moved files of `internal/search` (those paths stay in ABO) |
| S7 | `mediacore/storage/index` | `internal/database`, `internal/search`, `internal/server` | -; plus callers of the moved files of `internal/search`, `internal/server` (those paths stay in ABO); via `internal/database`: no consumer interface (reached through the composition root) |
| S8 | `mediacore/backup` | `internal/backup` | 6: `internal/appdirs`, `internal/config`, `internal/decisions`, `internal/organizer`, `internal/server`, `internal/server/handlers/system` |
| S8 | `mediacore/updater` | `internal/server`, `internal/updater` | 0; plus callers of the moved files of `internal/server` (those paths stay in ABO) |
| S9 | `mediacore/ui` | `internal/server` | -; plus callers of the moved files of `internal/server` (those paths stay in ABO) |

Earliest stage after which each package that stays in ABO can be fully rewired (latest stage in which something it imports today moves to core): S0: 54 packages, S1: 3 packages, S2: 1 packages, S2b: 2 packages, S4: 2 packages, S7: 87 packages, S8: 7 packages, S9: 2 packages (total 158).

### 8.3 Non-importable destinations and their gates (MOD-2, R2-5)

Every destination under `mediacore/internal/` or `mediacore/kernel` that 04 maps something to, with
the ABO code that still uses it at the stage point where the ABO path stops being importable
(non-test and test files; for split sources, references to identifiers declared in the moved files,
the staying files of the same package included). The gate is evaluated at **every** stage point in
both legal stage orders (§2.1 (c)); the count is the one importer count of §3. Such a destination is
legal only because every listed user is rewired, no later than that point, to the named public
surface; the second table says how, per user. A user found by `go list` and not declared fails the
generator (it would have caught MOD2-1, where three `sdnotify` users were missing).

| source | non-importable destination | ABO user packages (from `go list -deps -test`) | public surface they are rewired to | path stops being importable at |
|---|---|---|---|---|
| `internal/sdnotify` | `mediacore/internal/sdnotify` | 5: `.`, `internal/database`, `internal/operations/registry`, `internal/search`, `internal/server` | the kernel's `ExtendStart` hook (05-health §5H.3.4) and `lifecycle.NotifySocketEnv`; readiness owned by the kernel; fast-exit flags handled by `app` | S2 |
| `internal/startup` | `mediacore/kernel` | 3: `cmd`, `internal/server`, `internal/server/handlers/system` | `module.Registrar` startup steps; `health` degraded-token constants | S2 |
| `internal/metrics` | `mediacore/internal/pebble` | 5: `internal/metafetch`, `internal/metrics`, `internal/openlibrary`, `internal/server`, `internal/telemetry/contract` | `storage` metrics for the main and auxiliary stores (`storage.OpenAuxiliary`) | S2b |
| `internal/database` | `mediacore/internal/authstore` | 7: `internal/database`, `internal/database/mocks`, `internal/merge`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/usermerge` | `auth` types, capability interfaces and errors (cut out of the implementation files) | S4 |
| `internal/server/handlers` | `mediacore/internal/authhttp` | 2: `internal/server`, `internal/server/handlers` | core-mounted `/api/core/v1/auth/*`; `auth` invite delivery API for `internal/cfaccess` | S4 |
| `internal/server/handlers` | `mediacore/internal/taskhttp` | 2: `internal/server`, `internal/server/handlers` | core-mounted `/api/core/v1/tasks/*` | S5 |
| `internal/server/handlers/operations` | `mediacore/internal/taskhttp` | 0: none | - | S5 |
| `internal/server/handlers/opsv3` | `mediacore/internal/taskhttp` | 1: `internal/server` | core-mounted `/api/core/v1/tasks/*`; `/api/v3/ops` served as a core alias | S5 |
| `tools/cmd/opsgen` | `mediacore/internal/openapi` | 0: none | - | S5 |
| `internal/scheduler` | `mediacore/internal/scheduler` | 6: `internal/scheduler`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/mocks`, `internal/server/handlers/operations`, `internal/server/handlers/operations/mocks` | `tasks.Schedule` / `ScheduleFromConfig` and the core task API | T7 |
| `internal/operations/registry` | `mediacore/internal/executor` | 37: `.`, `cmd`, `internal/catalog`, `internal/dedup`, `internal/itunes/service`, `internal/maintenance`, `internal/maintenance/jobs`, `internal/metabatch` (+29) — all of them, through 05-tasks T2–T7; T8 ships only at a generated count of 0 | `tasks` (`RunItems`, `Definition`, starter options, run context) through the `opsbridge` adapter until T8 | T8 |

| source → destination | ABO user | rewired by (no later than the stage in the table above) |
|---|---|---|
| `internal/database` → `mediacore/internal/authstore` | `internal/database` | residue (`mock_store.go`, `pebble_store.go`, `user_merge.go`) uses `auth` exported API; admin user merge follows 05-storage cross-owner writes (DATA-18) |
| `internal/database` → `mediacore/internal/authstore` | `internal/database/mocks` | regenerated against `auth` interfaces |
| `internal/database` → `mediacore/internal/authstore` | `internal/merge` | `auth` interfaces |
| `internal/database` → `mediacore/internal/authstore` | `internal/server` | `auth` invite/guest types (CF parts via `internal/cfaccess`) |
| `internal/database` → `mediacore/internal/authstore` | `internal/server/handlers` | tests use `auth` errors and types |
| `internal/database` → `mediacore/internal/authstore` | `internal/server/handlers/abs` | test uses `auth.OAuthIdentity` |
| `internal/database` → `mediacore/internal/authstore` | `internal/usermerge` | `auth` errors and identity types |
| `internal/metrics` → `mediacore/internal/pebble` | `internal/metafetch` | the Open Library store opens through `storage.OpenAuxiliary`, which registers its metrics |
| `internal/metrics` → `mediacore/internal/pebble` | `internal/metrics` | the collector registration in `metrics.go` is deleted (rewrite) |
| `internal/metrics` → `mediacore/internal/pebble` | `internal/openlibrary` | same (`store.go`) |
| `internal/metrics` → `mediacore/internal/pebble` | `internal/server` | `pebble_metrics_sources.go` deleted |
| `internal/metrics` → `mediacore/internal/pebble` | `internal/telemetry/contract` | the series test (`internal/telemetrypolicy`) reads the App registry |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/scheduler` | books declarations re-expressed (REWRITE) |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/server` | the kernel owns the scheduler; server wiring deleted |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/server/handlers` | scheduler-admin test moves with `scheduler_admin.go` (S5) |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/server/handlers/mocks` | regenerated against the core task API |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/server/handlers/operations` | `TaskInfo` from the core task API |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/server/handlers/operations/mocks` | regenerated |
| `internal/sdnotify` → `mediacore/internal/sdnotify` | `.` | `--print-capabilities` fast exit handled by `app` (`main.go:53`) |
| `internal/sdnotify` → `mediacore/internal/sdnotify` | `internal/database` | `startup_notify.go` calls the kernel's `ExtendStart` hook (`func(step string)`, 05-health §5H.3.4) handed to books (from S2b core `storage` extends the start timeout itself, 05-storage) |
| `internal/sdnotify` → `mediacore/internal/sdnotify` | `internal/operations/registry` | `subprocess.go:70` and its test use `lifecycle.NotifySocketEnv` |
| `internal/sdnotify` → `mediacore/internal/sdnotify` | `internal/search` | `register.go:55` calls the same hook (until S7 moves the index lifecycle to core) |
| `internal/sdnotify` → `mediacore/internal/sdnotify` | `internal/server` | READY/STATUS/STOPPING move into the kernel lifecycle (`server_lifecycle.go:340-343,552` deleted) |
| `internal/server/handlers` → `mediacore/internal/authhttp` | `internal/server` | the gin wiring of these handlers is deleted in the same PR (core mounts the routes) |
| `internal/server/handlers` → `mediacore/internal/authhttp` | `internal/server/handlers` | tests of the moved handlers move with them; `user_merge.go` per RESIDUE |
| `internal/server/handlers` → `mediacore/internal/taskhttp` | `internal/server` | gin wiring deleted in the same PR |
| `internal/server/handlers` → `mediacore/internal/taskhttp` | `internal/server/handlers` | `scheduler_admin_test.go` moves with the file |
| `internal/server/handlers/opsv3` → `mediacore/internal/taskhttp` | `internal/server` | `wire_ops_v3_routes.go` and its test deleted |
| `internal/startup` → `mediacore/kernel` | `cmd` | `commands_test.go` reads the token constants from `health` |
| `internal/startup` → `mediacore/kernel` | `internal/server` | the step table moves into the kernel supervisor; `startup_steps_test.go` moves with it |
| `internal/startup` → `mediacore/kernel` | `internal/server/handlers/system` | `handler_health_test.go` uses a `coretest` health double |

### 8.4 Ordering notes for the implementation plan

1. **`internal/database` cannot be split before `storage` (S2b) exists.** The books residue is
   created at S2b as `internal/books/store` on `storage.LegacyRange` with books' generated claim
   list (05-storage §5S.9.1; shadow mode for one release, then strict); the identity families are
   adopted at S4 and the task families copied/read in place at S5 under 09's compatibility rules
   (09 §9.2.3, 05-tasks §5T.6).
2. **`tasks` is the long pole of S5.** The registry's importers are counted in its §3.2 row and
   those still in ABO at T8 in §8.3 (all rewired through the 05-tasks batches T2–T7; T8 ships only
   when that generated count is 0), and ~170 `RunItems` call sites must
   state `tasks.Parallelism` (R7); `internal/plugins`'s lint stays until the last call site moves,
   then is deleted.
3. **Wrong-way `allowed` edges are re-evaluated against the mapped layer of the core package, not
   dropped** (MOD2-7). The ABO guard maps core's R4 layers onto its own: core L0–L5 → −1 (platform,
   legal from every ABO layer); core L6 (`httpapi`, `realtime`, `ui`) → ABO layer 5 (transport);
   core L7–L9 (`module`, `app`, `examples/*`) → ABO layer 6 (entry), with an explicit allowance for
   the `internal/books` composition package; `loadGraph` (`internal/arch/layering_test.go:339-381`)
   keeps imports with the `github.com/falkcorp/mediacore/` prefix instead of dropping them (03
   §3.5.1). So the five edges `dedup`, `metadata`, `reconcile`, `repairs` → registry and
   `reconcile` → sdk disappear at S5 honestly (they then point at `tasks`, L4 → −1); the two
   `plugins/* → itunes/service` edges **stay** after S6, because `itunes/service` still imports a
   transport package (core `realtime`, mapped to layer 5; `layering_test.go:298-299`); the five
   `config` edges disappear at S2/S3 as the loader and persistence move (`config`, L3 → −1).
4. **The logger façade** (`internal/logger` over `mediacore/log`) lets S1 land without touching
   342 files. It is kept: removing it is an optional ratchet (SIMP-3), as is the
   `config.AppConfig` façade's 634-site migration; neither is an extraction stage.
5. **No gin retirement.** ABO's gin engine stays under `httpapi.MountLegacy` permanently
   (cross-cutting resolution 2); the gin helpers it needs stay in ABO (§1 decision 6).
6. Package-count effect on the ABO layering guard: 55 package paths leave the root module (158
   stay; the renames in §2.4 keep their count) and 2 new paths appear (`internal/books`,
   `internal/cfaccess`), so **160** packages remain and the literal floor of 150
   (`layering_test.go:38`) would still pass with a margin of 10 — still too thin to rely on; the
   ratio floor of 03 §3.5.1 is required. (v1.0.0 counted 59 leaving and 156 remaining; the
   difference is `internal/httputil`, `internal/logger`, `internal/appdirs` and
   `internal/telemetry/contract`, which now stay.)

### 8.5 R1 cuts at each move

A file that lands in core may not import ABO code (R1; `TestCoreImportsOnlyCore` from S1). For every
file that lands in core at a stage point and imports an ABO package whose referenced declarations are
still in ABO at that point, the row below states how the import is cut in the move PR (generated per
stage; parts that are re-implemented are covered by §8.6 instead). The generic cuts apply to every
moving file: `internal/logger` → a `*slog.Logger` (child of `log`) passed in, no package-level logger
(R5); `internal/config` → a typed config section handle; `internal/database` → the destination's own
capability interface over `storage` (§4.3–§4.4); `internal/httputil` → `httpapi` helpers;
`internal/metrics` → the App's metrics registry.

| stage | file(s) landing in core | ABO import cut at the move | how |
|---|---|---|---|
| S1 | `internal/diag/flight`: `flight.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S2 | `internal/server/middleware`: `request_size.go` | `internal/fingerprint/workerapi` | books passes the worker API body cap as the route's per-route body limit (05-http-api route options) |
| S2 | `internal/server/middleware`: `ratelimit.go`, `request_size.go` | `internal/httputil` | `httpapi` helpers (N7) |
| S2 | `internal/server/middleware`: `recovery_redact.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S2b | `internal/compactprogress`: `step.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S2b | `internal/database/dbtest`: `newstore.go` | `internal/database` | `coretest` store fixture over `storage` |
| S4 | `internal/auth`: `context.go`, `seed.go`, `user_active.go` | `internal/database` | the destination's own capability interface over `storage` (§4.3-§4.4) |
| S4 | `internal/oauth`: `resolve.go` | `internal/database` | `auth` identity store interface |
| S4 | `internal/oauth`: `resolve.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S5 | `internal/operations`: `progress.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S5 | `internal/operations/childop`: `follow.go` | `internal/database` | `tasks` run-store interface |
| S5 | `internal/operations/registry`: `activity_mirror.go`, `batch.go`, `batch_hooks.go`, `batch_runner.go` (+14) | `internal/database` | the destination's own capability interface over `storage` (§4.3-§4.4) |
| S5 | `internal/operations/registry`: `registry.go`, `v3_adapter.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S5 | `internal/operations/registry`: `activity_mirror.go`, `aliases.go`, `registry.go`, `reporter_db.go` (+1) | `internal/metrics` | the App's metrics registry (`metrics`, 05-metrics) |
| S5 | `internal/operations/registry`: `batch_hooks.go`, `batch_runner.go`, `v3_adapter.go` | `internal/operations/opswriter` | `tasks` fencing (the fence moves to `tasks` at S5) |
| S5 | `internal/operations/registry`: `batch_hooks.go` | `pkg/ops` | `tasks` (pkg/ops becomes `tasks` in the same stage) |
| S5 | `internal/scheduler`: `maintenance.go`, `scheduler.go` | `internal/config` | a typed config section handle (`config`, 05-config) |
| S5 | `internal/scheduler`: `scheduler.go`, `v3_schedule.go` | `internal/database` | the destination's own capability interface over `storage` (§4.3-§4.4) |
| S5 | `internal/scheduler`: `daily_at.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S5 | `internal/server/handlers/opsv3`: `access.go`, `events.go`, `handler.go`, `rows.go` (+3) | `internal/database` | the destination's own capability interface over `storage` (§4.3-§4.4) |
| S5 | `internal/server/handlers/opsv3`: `access.go`, `counts.go`, `defs.go`, `events.go` (+4) | `internal/httputil` | `httpapi` helpers (N7) |
| S5 | `internal/server/handlers/opsv3`: `access.go`, `events.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S5 | `internal/server/handlers/opsv3`: `access.go` | `internal/repairs` | a books authz hook registered with the task API (row reason) |
| S5 | `internal/server/handlers/opsv3`: `access.go`, `defs.go`, `events.go`, `handler.go` (+1) | `internal/server/handlers/opaccess` | the task API's per-definition `Permissions` (05-tasks §5T.3.7) |
| S5 | `pkg/ops/opstest`: `conformance.go`, `faults.go`, `run.go`, `store.go` (+1) | `internal/database` | the destination's own capability interface over `storage` (§4.3-§4.4) |
| S5 | `pkg/ops/opstest`: `store.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S5 | `pkg/ops/opstest`: `run.go`, `store.go` | `internal/operations/opswriter` | `tasks` fencing |
| S5 | `pkg/ops/opstest`: `faults.go`, `run.go`, `world.go` | `pkg/ops` | `tasks` |
| S7 | `internal/ai/aijobs`: `aijobs.go` | `internal/database` | the destination's own capability interface over `storage` (§4.3-§4.4) |
| S7 | `internal/ai/aijobs`: `aijobs.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S7 | `internal/ai/resultjournal`: `journal.go` | `internal/database` | the destination's own capability interface over `storage` (§4.3-§4.4) |
| S7 | `internal/ai/resultjournal`: `journal.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S7 | `internal/aidispatch`: `dispatch.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S7 | `internal/fileops`: `service.go` | `internal/config` | a typed config section handle (`config`, 05-config) |
| S7 | `internal/fileops`: `service.go` | `internal/database` | `library` move journal over `storage` |
| S7 | `internal/plugins/webhook`: `plugin.go` | `internal/plugin` | `notify` adapter registration (05-notify §10); `internal/plugin` is deleted |
| S7 | `internal/server`: `provider_throttle_wire.go`, `search_observer.go`, `search_reconciler.go` | `internal/database` | the destination's own capability interface over `storage` (§4.3-§4.4) |
| S7 | `internal/server`: `provider_throttle_wire.go` | `internal/metadata` | instance-scoped `providerhttp` limits |
| S7 | `internal/watcher`: `watcher.go` | `internal/appdirs` | the watched roots passed in by books (`library` roots) |
| S7 | `internal/watcher`: `watcher.go` | `internal/config` | a typed config section handle (`config`, 05-config) |
| S8 | `internal/backup`: `backup.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| S8 | `internal/server/handlers`: `diagnostics_traces.go` | `internal/httputil` | `httpapi` helpers (N7) |
| S8 | `internal/sysinfo`: `service.go` | `internal/config` | a typed config section handle (`config`, 05-config) |
| S8 | `internal/sysinfo`: `service.go` | `internal/database` | `health` probes take a store statistics interface |

### 8.6 Re-implemented split parts and rewritten staying files (MOD2-3)

Split parts that do not move verbatim: the stage PR rewrites the content against core contracts,
deletes the ABO files and rewrites the staying side's references. The count is generated by the
split check of §2.1 (e) (unexported names, struct field and method accesses, foreign-type methods
and forbidden edges between the part and the rest of its package). Every other split part is a
verbatim move and type-checks; the two MOD2-3 cases are resolved by changing the mapping:
`opswriter` sends only the self-contained `fence.go` to core (public `tasks` fencing) and keeps
`intent.go` and `evidence_journal.go` with the Writer, and `user_merge.go`'s methods move onto an
ABO-owned handler type before `user.go` leaves (last row).

| package | part | stage | staying-side references the stage PR rewrites (generated) | how |
|---|---|---|---|---|
| `internal/ai` | `mediacore/integrations/ai/openai` | S7 | 46 | OpenAI client re-implemented as the `ai/openai` adapter over `providerhttp` (05-ai); ABO's `OpenAIParser` methods become calls on the adapter |
| `internal/aidispatch` | `internal/ai` | S7 | 7 | file divided: `Capability`/`Kind`/`Spec`/`Lookup` and the registration mechanism land in `integrations/ai`; the ten capability IDs stay books registrations |
| `internal/auth` | `internal/cfaccess` | S4 | 1 | owner proof consolidated into `internal/cfaccess` over the public `auth` context API |
| `internal/config` | `mediacore/auth` | S4 | 16 | invite/bootstrap settings become `core.auth.*` keys (05-authn) |
| `internal/config` | `mediacore/config` | S2 | 27 | loader re-implemented as core `config.Load` + aliases; `internal/config` becomes the `legacyconfig.Facade` (05-config stage 1) |
| `internal/config` | `mediacore/integrations/ai` | S7 | 9 | AI endpoint blob handling becomes instance-store import (05-metadata M1) |
| `internal/config` | `mediacore/log` | S2 | 6 | log-retention keys become `core.log.*` (05-logging) |
| `internal/database` | `mediacore/auth` | S4 | 5 | store capability interfaces restated over the `auth` types (`User`, `Session`, `APIKey`, `Role`, `Invite`) that are cut out of the residue's type files (§4.4) |
| `internal/database` | `mediacore/integrations/ai` | S7 | 12 | AI job rows behind the `ai` contract (05-ai) |
| `internal/database` | `mediacore/integrations/providerhttp` | S7 | 5 | throttle/budget rows behind instance-scoped `providerhttp` (05-metadata M2) |
| `internal/database` | `mediacore/internal/authstore` | S4 | 122 | identity store re-implemented as `internal/authstore` behind `auth` (05-authn §11); exported types, interfaces and errors cut out to public `auth` |
| `internal/database` | `mediacore/library` | S7 | 16 | path-history helpers behind `library` (05-library) |
| `internal/database` | `mediacore/secrets` | S3 | 5 | encryption and key file re-implemented as `secrets` (05-config); `setting:` row methods stay |
| `internal/database` | `mediacore/storage` | S2b | 140 | storage engine, census, guard and migration bookkeeping re-implemented in core `storage` (05-storage; §4.5 cut lines) |
| `internal/database` | `mediacore/storage/index` | S7 | 0 | change observers become `storage/index` dirty-set hooks (05-storage) |
| `internal/logger` | `mediacore/log` | S1 | 6 | sanitizers exported from `log`; the façade calls `log.SanitizeLine` |
| `internal/metadata` | `mediacore/integrations/providerhttp` | S7 | 17 | provider error classification re-implemented in instance-scoped `providerhttp` |
| `internal/metrics` | `mediacore/internal/pebble` | S2b | 1 | Pebble collector registered by `storage` for the main and auxiliary stores; `metrics.go`'s registration line is deleted |
| `internal/operations/registry` | `internal/books` | T8 | 12 | file divided: the requirement mechanism (`Requirement`, `AllSatisfied`, `DepStore`, cycle check) lands in the engine; book-field predicates and the `GetBookByID` adapter become books requirement declarations (05-tasks) |
| `internal/scheduler` | `internal/books` | T7 | 21 | books schedule declarations re-expressed as `tasks.Schedule` declarations (05-tasks T7); no method on the core scheduler type |
| `internal/search` | `mediacore/storage/index` | S7 | 2 | regex term expansion takes books' indexed-field table as data (`storage/index` options) |
| `internal/server` | `internal/cfaccess` | S4 | 60 | CF Access glue consolidated into `internal/cfaccess` as an authenticator extension (G11) |
| `internal/server` | `mediacore/auth` | S4 | 115 | auth routes and guards re-implemented in core `auth`/`authhttp` (05-authn); gin handlers deleted at S4 |
| `internal/server` | `mediacore/health` | S8 | 12 | flight-recorder wiring re-implemented in `health` (05-health) |
| `internal/server` | `mediacore/httpapi` | S2 | 32 | perimeter re-implemented in core `httpapi` (05-http-api §8.1); gin middleware deleted at S2 |
| `internal/server` | `mediacore/storage/index` | S7 | 50 | search index queue/reconciler re-implemented as `storage/index` (05-storage) |
| `internal/server` | `mediacore/ui` | S9 | 6 | SPA serving re-implemented by `ui.Handler` at S9 (07 §7.6.3) |
| `internal/server` | `mediacore/updater` | S8 | 9 | update handlers become core `/api/core/v1/update` (05-updater) |
| `internal/server/handlers` | `mediacore/internal/authhttp` | S4 | 8 | auth/user/key/invite handlers re-implemented as core routes (05-authn §4.13); gin routes deleted in the same PR |
| `internal/server/handlers` | `mediacore/internal/taskhttp` | S5 | 1 | scheduler admin re-implemented as core schedule routes (05-tasks §5T.3.10) |
| `internal/server/handlers/operations` | `mediacore/internal/taskhttp` | S5 | 7 | pause routes re-implemented as `/api/core/v1/tasks/pause` (05-tasks) |
| `internal/server/middleware` | `mediacore/auth` | S4 | 6 | gin auth middleware replaced by the `legacyauth` bridge over core stages 12-13 (05-http-api §8.3) |
| `pkg/ops` | `internal/books` | S5 | 5 | file divided: `tasks.Writer` becomes generic over `Resource` (with `Subject`, `SubjectType`, `Risk`); the Book-typed methods form the books `Writer` contract |
| `pkg/ops/opstest` | `internal/books/store/storetest` | S5 | 40 | Book-typed fakes re-implemented over the exported `taskstest` fake world |
| `internal/server/handlers` | `user_merge.go` (staying file) | S4 | 18 | S4, before `user.go` moves: the nine merge methods move from `*UserHandler` onto an ABO-owned merge handler type in `internal/server/handlers` over `internal/usermerge`, with `adminRole` and the merge fields (MOD2-3) |

## 9. `shared/` candidates (none promoted now)

| package | why it might be shared | trigger for promotion to `mediacore/shared/...` |
|---|---|---|
| `internal/titleutil` | title normalization | a TV/Movies release-name parser needs the same rules (03 §3.4 example) |
| `internal/personname` | "is this a human name" | TV/Movies cast/crew credit parsing |
| `internal/seqnum`, `internal/trackseq` | sequence/volume numbers in names | episode numbering reuses it rather than a dedicated S/E parser |
| `internal/querygrammar` | the search-bar value syntax | a second app adopts the same search bar |
| `internal/matcher` | fuzzy title matching | a second app's metadata matcher |
| `internal/cache`, `internal/flight`, `internal/keyedmutex` | domain-free primitives | a core package needs one (then core-internal, not shared, since they are not domain logic). **Fired for `keyedmutex` at S3** (§8.1): `core/internal/keyedmutex` is a core-internal copy for `config`, `download` and `library` (05-config, 05-download, 05-library cite it); ABO keeps its own package (COR2-16) |

## 10. Largest contributions to core

Whole files only (the top 30 sources). Partial-file cuts (`internal/database/pebble_store.go`,
`store.go`, `migrations.go`, `internal/config/config.go`, `persistence.go`, `update_service.go`,
`internal/ai/openai_parser.go`, `internal/aidispatch/capabilities.go`, `internal/metrics/metrics.go`)
are counted on the ABO side, so the core figure in §1 is a lower bound.

| package (current) | LOC to core |
|---|---|
| `internal/operations/registry` | 14695 |
| `internal/database` | 8792 |
| `internal/server` | 5813 |
| `pkg/ops/opstest` | 3443 |
| `internal/server/handlers` | 3167 |
| `internal/ai` | 3002 |
| `internal/server/handlers/opsv3` | 2929 |
| `pkg/ops` | 2925 |
| `internal/config` | 2544 |
| `internal/scheduler` | 2268 |
| `internal/server/middleware` | 1676 |
| `internal/backup` | 1582 |
| `internal/aidispatch` | 1498 |
| `internal/telemetry` | 1363 |
| `internal/fileops` | 1070 |
| `tools/cmd/oplint` | 998 |
| `internal/metadata` | 997 |
| `internal/server/absauth` | 990 |
| `internal/oauth` | 695 |
| `internal/diag/flight` | 659 |
| `internal/operations/state` | 578 |
| `internal/ai/aijobs` | 575 |
| `internal/sysinfo` | 540 |
| `internal/logging` | 529 |
| `internal/opsmetrics` | 525 |
| `internal/pathutil` | 514 |
| `internal/deluge` | 506 |
| `internal/auth` | 502 |
| `internal/watcher` | 457 |
| `internal/updater` | 453 |

## 11. Alphabetical index

Every package of `go list ./...` exactly once, linking to its row in §3.

| package | bucket | target |
|---|---|---|
| [`.`](#p-root) | abo | . |
| [`cmd`](#p-cmd) | abo | cmd |
| [`cmd/itl-audit-encoding`](#p-cmd-itl-audit-encoding) | abo | cmd/itl-audit-encoding |
| [`cmd/itl-check`](#p-cmd-itl-check) | abo | cmd/itl-check |
| [`cmd/itl-diff`](#p-cmd-itl-diff) | abo | cmd/itl-diff |
| [`cmd/mtls-bridge`](#p-cmd-mtls-bridge) | abo | cmd/mtls-bridge |
| [`cmd/pebble-inject-skip`](#p-cmd-pebble-inject-skip) | abo | cmd/pebble-inject-skip |
| [`cmd/pid-census`](#p-cmd-pid-census) | abo | cmd/pid-census |
| [`cmd/verify-suite`](#p-cmd-verify-suite) | abo | cmd/verify-suite |
| [`internal/acoustid`](#p-internal-acoustid) | abo | internal/acoustid |
| [`internal/activity`](#p-internal-activity) | abo | internal/activity |
| [`internal/ai`](#p-internal-ai) | abo | internal/ai |
| [`internal/ai/aijobs`](#p-internal-ai-aijobs) | merge | mediacore/integrations/ai |
| [`internal/ai/mocks`](#p-internal-ai-mocks) | abo | internal/ai/mocks |
| [`internal/ai/resultjournal`](#p-internal-ai-resultjournal) | merge | mediacore/integrations/ai |
| [`internal/aidispatch`](#p-internal-aidispatch) | core-public | mediacore/integrations/ai |
| [`internal/aiscan`](#p-internal-aiscan) | abo | internal/aiscan |
| [`internal/appdirs`](#p-internal-appdirs) | abo | internal/appdirs |
| [`internal/applycap`](#p-internal-applycap) | abo | internal/applycap |
| [`internal/applygate`](#p-internal-applygate) | abo | internal/applygate |
| [`internal/arch`](#p-internal-arch) | abo | internal/arch |
| [`internal/audio`](#p-internal-audio) | abo | internal/audio |
| [`internal/audiobooks`](#p-internal-audiobooks) | abo | internal/audiobooks |
| [`internal/audioext`](#p-internal-audioext) | abo | internal/audioext |
| [`internal/audioutil`](#p-internal-audioutil) | abo | internal/audioutil |
| [`internal/auth`](#p-internal-auth) | core-public | mediacore/auth |
| [`internal/authorcredit`](#p-internal-authorcredit) | abo | internal/authorcredit |
| [`internal/authority`](#p-internal-authority) | abo | internal/authority |
| [`internal/authority/authoritybuild`](#p-internal-authority-authoritybuild) | abo | internal/authority/authoritybuild |
| [`internal/authorjunk`](#p-internal-authorjunk) | abo | internal/authorjunk |
| [`internal/authormerge`](#p-internal-authormerge) | abo | internal/authormerge |
| [`internal/authorname`](#p-internal-authorname) | abo | internal/authorname |
| [`internal/backup`](#p-internal-backup) | core-public | mediacore/backup |
| [`internal/batch`](#p-internal-batch) | abo | internal/batch |
| [`internal/boilerplate`](#p-internal-boilerplate) | abo | internal/boilerplate |
| [`internal/bookfileaudio`](#p-internal-bookfileaudio) | abo | internal/bookfileaudio |
| [`internal/bookfiles`](#p-internal-bookfiles) | delete | - |
| [`internal/boundedcall`](#p-internal-boundedcall) | merge | mediacore/library |
| [`internal/cache`](#p-internal-cache) | abo | internal/cache |
| [`internal/catalog`](#p-internal-catalog) | abo | internal/catalog |
| [`internal/cfaccesspolicy`](#p-internal-cfaccesspolicy) | abo | internal/cfaccesspolicy |
| [`internal/cfservicetoken`](#p-internal-cfservicetoken) | abo | internal/cfservicetoken |
| [`internal/chaptershape`](#p-internal-chaptershape) | abo | internal/chaptershape |
| [`internal/compactprogress`](#p-internal-compactprogress) | merge | mediacore/storage |
| [`internal/config`](#p-internal-config) | abo | internal/config |
| [`internal/covers`](#p-internal-covers) | abo | internal/covers |
| [`internal/covertext`](#p-internal-covertext) | abo | internal/covertext |
| [`internal/database`](#p-internal-database) | abo | internal/books/store |
| [`internal/database/aggtest`](#p-internal-database-aggtest) | abo | internal/books/store/aggtest |
| [`internal/database/dbtest`](#p-internal-database-dbtest) | abo | internal/books/store/storetest |
| [`internal/database/mocks`](#p-internal-database-mocks) | delete | - |
| [`internal/decisions`](#p-internal-decisions) | abo | internal/decisions |
| [`internal/dedup`](#p-internal-dedup) | abo | internal/dedup |
| [`internal/dedup/dataset`](#p-internal-dedup-dataset) | abo | internal/dedup/dataset |
| [`internal/dedup/unified`](#p-internal-dedup-unified) | abo | internal/dedup/unified |
| [`internal/deluge`](#p-internal-deluge) | abo | internal/deluge |
| [`internal/diag/flight`](#p-internal-diag-flight) | merge | mediacore/health |
| [`internal/diagnosis`](#p-internal-diagnosis) | abo | internal/diagnosis |
| [`internal/diagnostics`](#p-internal-diagnostics) | abo | internal/diagnostics |
| [`internal/diskstats`](#p-internal-diskstats) | merge | mediacore/library |
| [`internal/errhandling`](#p-internal-errhandling) | abo | internal/errhandling |
| [`internal/filehash`](#p-internal-filehash) | abo | internal/filehash |
| [`internal/fileops`](#p-internal-fileops) | abo | internal/fileops |
| [`internal/fingerprint`](#p-internal-fingerprint) | abo | internal/fingerprint |
| [`internal/fingerprint/workerapi`](#p-internal-fingerprint-workerapi) | abo | internal/fingerprint/workerapi |
| [`internal/fingerprint/workerclient`](#p-internal-fingerprint-workerclient) | abo | internal/fingerprint/workerclient |
| [`internal/flight`](#p-internal-flight) | abo | internal/flight |
| [`internal/foldercover`](#p-internal-foldercover) | abo | internal/foldercover |
| [`internal/foldernames`](#p-internal-foldernames) | abo | internal/foldernames |
| [`internal/franchise`](#p-internal-franchise) | abo | internal/franchise |
| [`internal/httputil`](#p-internal-httputil) | abo | internal/httputil |
| [`internal/identification`](#p-internal-identification) | abo | internal/identification |
| [`internal/importer`](#p-internal-importer) | abo | internal/importer |
| [`internal/itunes`](#p-internal-itunes) | abo | internal/itunes |
| [`internal/itunes/service`](#p-internal-itunes-service) | abo | internal/itunes/service |
| [`internal/itunesguard`](#p-internal-itunesguard) | abo | internal/itunesguard |
| [`internal/keyedmutex`](#p-internal-keyedmutex) | abo | internal/keyedmutex |
| [`internal/libroot`](#p-internal-libroot) | merge | mediacore/library |
| [`internal/lifecycle`](#p-internal-lifecycle) | core-public | mediacore/lifecycle |
| [`internal/linkintegrity`](#p-internal-linkintegrity) | abo | internal/linkintegrity |
| [`internal/logger`](#p-internal-logger) | abo | internal/logger |
| [`internal/logger/logtest`](#p-internal-logger-logtest) | merge | mediacore/coretest |
| [`internal/logger/mocks`](#p-internal-logger-mocks) | delete | - |
| [`internal/logging`](#p-internal-logging) | core-public | mediacore/log |
| [`internal/mail`](#p-internal-mail) | core-public | mediacore/integrations/notify/mail |
| [`internal/mail/mailtest`](#p-internal-mail-mailtest) | merge | mediacore/coretest |
| [`internal/maintenance`](#p-internal-maintenance) | delete | - |
| [`internal/maintenance/jobs`](#p-internal-maintenance-jobs) | abo | internal/maintenance/jobs |
| [`internal/masterlist`](#p-internal-masterlist) | abo | internal/masterlist |
| [`internal/matcher`](#p-internal-matcher) | abo | internal/matcher |
| [`internal/mediainfo`](#p-internal-mediainfo) | abo | internal/mediainfo |
| [`internal/merge`](#p-internal-merge) | abo | internal/merge |
| [`internal/metabatch`](#p-internal-metabatch) | abo | internal/metabatch |
| [`internal/metadata`](#p-internal-metadata) | abo | internal/metadata |
| [`internal/metadata/dailyquota`](#p-internal-metadata-dailyquota) | merge | mediacore/integrations/providerhttp |
| [`internal/metadata/mocks`](#p-internal-metadata-mocks) | abo | internal/metadata/mocks |
| [`internal/metadata/providerhttp`](#p-internal-metadata-providerhttp) | core-public | mediacore/integrations/providerhttp |
| [`internal/metafetch`](#p-internal-metafetch) | abo | internal/metafetch |
| [`internal/metastate`](#p-internal-metastate) | abo | internal/metastate |
| [`internal/metrics`](#p-internal-metrics) | abo | internal/metrics |
| [`internal/models`](#p-internal-models) | merge | internal/dedup/unified |
| [`internal/mtls`](#p-internal-mtls) | abo | internal/mtls |
| [`internal/oauth`](#p-internal-oauth) | core-public | mediacore/auth/oidc |
| [`internal/openlibrary`](#p-internal-openlibrary) | abo | internal/openlibrary |
| [`internal/operations`](#p-internal-operations) | merge | mediacore/tasks |
| [`internal/operations/childop`](#p-internal-operations-childop) | merge | mediacore/tasks |
| [`internal/operations/freshness`](#p-internal-operations-freshness) | merge | mediacore/tasks |
| [`internal/operations/opmode`](#p-internal-operations-opmode) | merge | mediacore/tasks |
| [`internal/operations/opswriter`](#p-internal-operations-opswriter) | abo | internal/books/writer |
| [`internal/operations/registry`](#p-internal-operations-registry) | core-internal | mediacore/internal/executor |
| [`internal/operations/state`](#p-internal-operations-state) | merge | mediacore/tasks |
| [`internal/opscatalog`](#p-internal-opscatalog) | delete | - |
| [`internal/opsmetrics`](#p-internal-opsmetrics) | merge | mediacore/tasks |
| [`internal/organizer`](#p-internal-organizer) | abo | internal/organizer |
| [`internal/pathutil`](#p-internal-pathutil) | abo | internal/pathutil |
| [`internal/personname`](#p-internal-personname) | abo | internal/personname |
| [`internal/playlist`](#p-internal-playlist) | abo | internal/playlist |
| [`internal/plugin`](#p-internal-plugin) | delete | - |
| [`internal/plugins`](#p-internal-plugins) | delete | - |
| [`internal/plugins/acoustid`](#p-internal-plugins-acoustid) | abo | internal/plugins/acoustid |
| [`internal/plugins/dedup`](#p-internal-plugins-dedup) | abo | internal/plugins/dedup |
| [`internal/plugins/deluge`](#p-internal-plugins-deluge) | abo | internal/plugins/deluge |
| [`internal/plugins/itunes`](#p-internal-plugins-itunes) | abo | internal/plugins/itunes |
| [`internal/plugins/maintenance`](#p-internal-plugins-maintenance) | abo | internal/plugins/maintenance |
| [`internal/plugins/metafetch`](#p-internal-plugins-metafetch) | abo | internal/plugins/metafetch |
| [`internal/plugins/webhook`](#p-internal-plugins-webhook) | core-public | mediacore/integrations/notify/webhook |
| [`internal/policy`](#p-internal-policy) | abo | internal/policy |
| [`internal/quarantine`](#p-internal-quarantine) | abo | internal/quarantine |
| [`internal/querygrammar`](#p-internal-querygrammar) | abo | internal/querygrammar |
| [`internal/readstatus`](#p-internal-readstatus) | abo | internal/readstatus |
| [`internal/realtime`](#p-internal-realtime) | core-public | mediacore/realtime |
| [`internal/reconcile`](#p-internal-reconcile) | abo | internal/reconcile |
| [`internal/remux`](#p-internal-remux) | abo | internal/remux |
| [`internal/repairs`](#p-internal-repairs) | abo | internal/repairs |
| [`internal/scanlock`](#p-internal-scanlock) | abo | internal/scanlock |
| [`internal/scanner`](#p-internal-scanner) | abo | internal/scanner |
| [`internal/scheduler`](#p-internal-scheduler) | core-internal | mediacore/internal/scheduler |
| [`internal/sdnotify`](#p-internal-sdnotify) | core-internal | mediacore/internal/sdnotify |
| [`internal/search`](#p-internal-search) | abo | internal/search |
| [`internal/searchcache`](#p-internal-searchcache) | abo | internal/searchcache |
| [`internal/security/pathvalidation`](#p-internal-security-pathvalidation) | merge | mediacore/library |
| [`internal/security/safehttp`](#p-internal-security-safehttp) | merge | mediacore/integrations/providerhttp |
| [`internal/security/safepath`](#p-internal-security-safepath) | merge | mediacore/library |
| [`internal/seqnum`](#p-internal-seqnum) | abo | internal/seqnum |
| [`internal/server`](#p-internal-server) | abo | internal/server |
| [`internal/server/absauth`](#p-internal-server-absauth) | abo | internal/server/absauth |
| [`internal/server/handlers`](#p-internal-server-handlers) | abo | internal/server/handlers |
| [`internal/server/handlers/abs`](#p-internal-server-handlers-abs) | abo | internal/server/handlers/abs |
| [`internal/server/handlers/admindebug`](#p-internal-server-handlers-admindebug) | abo | internal/server/handlers/admindebug |
| [`internal/server/handlers/aibackends`](#p-internal-server-handlers-aibackends) | abo | internal/server/handlers/aibackends |
| [`internal/server/handlers/audiobooks`](#p-internal-server-handlers-audiobooks) | abo | internal/server/handlers/audiobooks |
| [`internal/server/handlers/audiobooks/mocks`](#p-internal-server-handlers-audiobooks-mocks) | abo | internal/server/handlers/audiobooks/mocks |
| [`internal/server/handlers/dedup`](#p-internal-server-handlers-dedup) | abo | internal/server/handlers/dedup |
| [`internal/server/handlers/dedup/mocks`](#p-internal-server-handlers-dedup-mocks) | abo | internal/server/handlers/dedup/mocks |
| [`internal/server/handlers/duplicates`](#p-internal-server-handlers-duplicates) | abo | internal/server/handlers/duplicates |
| [`internal/server/handlers/duplicates/mocks`](#p-internal-server-handlers-duplicates-mocks) | abo | internal/server/handlers/duplicates/mocks |
| [`internal/server/handlers/entities`](#p-internal-server-handlers-entities) | abo | internal/server/handlers/entities |
| [`internal/server/handlers/entities/mocks`](#p-internal-server-handlers-entities-mocks) | abo | internal/server/handlers/entities/mocks |
| [`internal/server/handlers/fpworker`](#p-internal-server-handlers-fpworker) | abo | internal/server/handlers/fpworker |
| [`internal/server/handlers/metadata`](#p-internal-server-handlers-metadata) | abo | internal/server/handlers/metadata |
| [`internal/server/handlers/metadata/mocks`](#p-internal-server-handlers-metadata-mocks) | abo | internal/server/handlers/metadata/mocks |
| [`internal/server/handlers/mocks`](#p-internal-server-handlers-mocks) | abo | internal/server/handlers/mocks |
| [`internal/server/handlers/opaccess`](#p-internal-server-handlers-opaccess) | abo | internal/server/handlers/opaccess |
| [`internal/server/handlers/operations`](#p-internal-server-handlers-operations) | abo | internal/server/handlers/operations |
| [`internal/server/handlers/operations/mocks`](#p-internal-server-handlers-operations-mocks) | abo | internal/server/handlers/operations/mocks |
| [`internal/server/handlers/opsv3`](#p-internal-server-handlers-opsv3) | core-internal | mediacore/internal/taskhttp |
| [`internal/server/handlers/repairs`](#p-internal-server-handlers-repairs) | abo | internal/server/handlers/repairs |
| [`internal/server/handlers/review`](#p-internal-server-handlers-review) | abo | internal/server/handlers/review |
| [`internal/server/handlers/system`](#p-internal-server-handlers-system) | abo | internal/server/handlers/system |
| [`internal/server/handlers/system/mocks`](#p-internal-server-handlers-system-mocks) | abo | internal/server/handlers/system/mocks |
| [`internal/server/handlers/tools`](#p-internal-server-handlers-tools) | abo | internal/server/handlers/tools |
| [`internal/server/middleware`](#p-internal-server-middleware) | merge | mediacore/auth + mediacore/httpapi |
| [`internal/serverdecode`](#p-internal-serverdecode) | abo | internal/serverdecode |
| [`internal/serviceregistry`](#p-internal-serviceregistry) | delete | - |
| [`internal/startup`](#p-internal-startup) | merge | mediacore/kernel |
| [`internal/sweep`](#p-internal-sweep) | abo | internal/sweep |
| [`internal/syncapi/conformance`](#p-internal-syncapi-conformance) | abo | internal/syncapi/conformance |
| [`internal/syncapi/progress`](#p-internal-syncapi-progress) | abo | internal/syncapi/progress |
| [`internal/sysinfo`](#p-internal-sysinfo) | abo | internal/sysinfo |
| [`internal/tagger`](#p-internal-tagger) | abo | internal/tagger |
| [`internal/telemetry`](#p-internal-telemetry) | core-public | mediacore/metrics |
| [`internal/telemetry/contract`](#p-internal-telemetry-contract) | abo | internal/telemetrypolicy |
| [`internal/testutil`](#p-internal-testutil) | abo | internal/testutil |
| [`internal/testutil/rapidgen`](#p-internal-testutil-rapidgen) | abo | internal/testutil/rapidgen |
| [`internal/titleutil`](#p-internal-titleutil) | abo | internal/titleutil |
| [`internal/tools`](#p-internal-tools) | abo | internal/tools |
| [`internal/trackseq`](#p-internal-trackseq) | abo | internal/trackseq |
| [`internal/transcode`](#p-internal-transcode) | abo | internal/transcode |
| [`internal/transcribe`](#p-internal-transcribe) | abo | internal/transcribe |
| [`internal/undo`](#p-internal-undo) | abo | internal/undo |
| [`internal/updater`](#p-internal-updater) | core-public | mediacore/updater |
| [`internal/usermerge`](#p-internal-usermerge) | abo | internal/usermerge |
| [`internal/util`](#p-internal-util) | abo | internal/util |
| [`internal/versionprimary`](#p-internal-versionprimary) | abo | internal/versionprimary |
| [`internal/versionprimary/vptest`](#p-internal-versionprimary-vptest) | abo | internal/versionprimary/vptest |
| [`internal/versions`](#p-internal-versions) | abo | internal/versions |
| [`internal/walkresume`](#p-internal-walkresume) | merge | mediacore/tasks |
| [`internal/watcher`](#p-internal-watcher) | merge | mediacore/library |
| [`internal/wirecontract`](#p-internal-wirecontract) | abo | internal/wirecontract |
| [`internal/work`](#p-internal-work) | abo | internal/work |
| [`internal/writeback`](#p-internal-writeback) | delete | - |
| [`pkg/ops`](#p-pkg-ops) | core-public | mediacore/tasks |
| [`pkg/ops/opstest`](#p-pkg-ops-opstest) | core-public | mediacore/tasks/taskstest |
| [`pkg/plugin/sdk`](#p-pkg-plugin-sdk) | delete | - |
| [`tools/cmd/dedup-dataset-audit`](#p-tools-cmd-dedup-dataset-audit) | abo | tools/cmd/dedup-dataset-audit |
| [`tools/cmd/itunes-group-preview`](#p-tools-cmd-itunes-group-preview) | abo | tools/cmd/itunes-group-preview |
| [`tools/cmd/merge-split-books`](#p-tools-cmd-merge-split-books) | abo | tools/cmd/merge-split-books |
| [`tools/cmd/oplint`](#p-tools-cmd-oplint) | merge | mediacore/archtest/tasklint |
| [`tools/cmd/opsgen`](#p-tools-cmd-opsgen) | merge | mediacore/internal/openapi |
| [`tools/cmd/orphan-nonprimary-census`](#p-tools-cmd-orphan-nonprimary-census) | abo | tools/cmd/orphan-nonprimary-census |
| [`tools/cmd/reconcile-book-counts`](#p-tools-cmd-reconcile-book-counts) | abo | tools/cmd/reconcile-book-counts |
| [`tools/cmd/reconcile-paths`](#p-tools-cmd-reconcile-paths) | abo | tools/cmd/reconcile-paths |
| [`tools/cmd/sdkguard`](#p-tools-cmd-sdkguard) | delete | - |

## Objections to 03

1. **Sub-packages not listed in the 03 §3.3 tree** are used where a contract needs a test kit, an
   analyzer or a pure helper: `tasks/taskstest`, `archtest/tasklint`, `integrations/ai/openai`,
   `storage/keys` (05-storage §5S.3.3), `integrations/download/deluge` and
   `integrations/notify/{mail,webhook}` (named in 03), and internal packages `internal/authstore`,
   `internal/taskhttp`, `internal/authhttp` (03's `internal/<capability>http` row covers the last
   two), `internal/pebble`, `internal/sse`, `internal/bleveindex`, `internal/keyedmutex`. 03 §3.3
   lists `internal/...` as open-ended; no rule is contradicted. (v1.1.0's objection on the 59/156
   package count is resolved: 03 §3.5.1 now states 55 leaving and 160 remaining.)
2. **03 §3.3 `lifecycle` row** names only `Component{Start, Stop}` and shutdown budget types; the
   `sdnotify` rewiring at S2 (§8.3) also needs the public constant `lifecycle.NotifySocketEnv`
   (MOD2-1). The `examples/hello` row says Build "core"; §8.1 lands it at S2 and grows it each
   stage (MOD2-10).

## Out-of-scope defects found

- None in code beyond those the raw files already record (raw/06 §1.1 dead `Cancellable`, raw/07
  §1.3 `batchpoller` never built, raw/05 §1 stale SQLite help text). The three dead packages are in
  scope and mapped above.
- Raw-data corrections: raw/01 classes `internal/server/absauth` audiobook-specific although web
  auth depends on it (§2.3); raw/04 §4 counts 591 consumer interfaces where the re-run finds 587
  (embed resolution); `internal/database/store.go:38` still claims "250" consumer interfaces and
  `internal/database/migrations.go:18` "398 methods" (both stale, raw/04 §7).

## Open items

1. **Mock conversion cost** (§4.6): 114 test files on `internal/database/mocks` plus ~22 on the
   hand-written `MockStore`. Owner decides whether conversion happens inside the books stage or is
   deferred behind the interim `storetest` composite.
2. **`idx:` and `pref:` sub-key ownership** (§4.5): owned by 05-storage §5S.9.1 (DATA-2); this
   file's file cut follows whatever that table states.
3. **`internal/cfaccess` consolidation** (§2.4) assumes core `auth` exposes an authenticator
   extension point for header-asserted identity (05-authn).
4. **`tasks.Writer` generics** (§6): 05-tasks must specify how a module registers typed writers
   (the books `ModifyBook` family) without core naming books types.
5. **Divided files** (§4.5 `settings.go`, `pebble_auth`-family interfaces cut out to `auth`, and the
   "file divided" parts of §8.6): each needs a mechanical "cut" PR before its move PR so that the
   move is a pure file move; parts that cannot be cut that way are re-implementations (§8.6).

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- MOD-2 — internal/public column re-derived from the import graph; `compactprogress` → `storage`, `oauth` → `auth/oidc`, `lifecycle` → public `lifecycle`; `sdnotify`, `startup`, `scheduler`, executor engine, `opswriter` fence files, `taskhttp`/`authhttp`, `authstore` and the Pebble collector moved only at a named gate stage; auth type files → public `auth`; §8.3 gate table and generator check (d).
- MOD-6 — moves keyed by 09 stages (§8.1); `internal/telemetry/contract` stays in ABO as `internal/telemetrypolicy` with `ratchet_test.go`; one telemetry target `mediacore/metrics`; `lifecycle` resolved to 03's public L0 package.
- MOD-21 — `internal/httputil` `respond.go` (`RespondWithApplyCapExceeded`, ABO `applycap`) stays; `internal/appdirs` stays as the config → `library.AppDirs` adapter.
- Cross-cutting 2 / SIMP-2 — no gin retirement; gin helpers of the permanent legacy mount stay (`httputil`, `gone.go`); per-stage removal of gin copies.
- Cross-cutting 1 / SIMP-1 — promotion rule as non-circular build rule; no deferred-build package in any stage.
- Cross-cutting 7 / DATA-1 — `SettingsStore` and `setting:` stay books; `settings.go` divided; method cut 108/337.
- DATA-2 — family ownership referenced to 05-storage §5S.9.1 instead of restated; `keyfamilies.go` stays ABO; activity store stays books (03 §3.3 `log` row, 05-logging §5).
- OPS-7 (consequence) — task-run store files stay as ABO legacy writers until 05-tasks T8; no `internal/taskstore`.
- SIMP-3 — `internal/logger` and `config.AppConfig` façades kept; removal is an optional ratchet.
- SIMP-13 — books metric names unchanged in the `internal/metrics` and `internal/opsmetrics` rows.
- COR-17 — `entity.Ref` in the `realtime` and `pkg/ops` rows.
- COR-19 — §8 layer list matches 03 R4 v1.3.0.
- MOD-14 (consequence) — root row names the `. -> cmd` positive control.

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-5 / MOD2-1 — MOD-2 gate rebuilt on `go list -deps -test -json` and a per-file parse, evaluated at every stage point (both UI-0 positions, T8 earliest) with every ABO user declared (§2.1 (c), §8.3); `sdnotify` moves at S2 with all five importers rewired (`ExtendStart` hook, `lifecycle.NotifySocketEnv`, kernel readiness, `app` fast exit); one generated importer count per package (in/out column, evidence prefix, "N importers" phrases, §8.2, §8.3).
- MOD2-2 — superseded wave table deleted; "earliest stage" total 158; generator check that every §8 core path is in 03 §3.3 or a §3 row.
- MOD2-3 — split check (unexported names, field/method access, foreign-type methods, forbidden edges) with move vs re-implementation parts (§2.2, §8.6); `opswriter` sends only `fence.go` to public `tasks`; `user_merge.go` moves onto an ABO-owned handler type before `user.go` leaves.
- MOD2-4 — no file of `cmd` moves; bench-tagged files listed; `cmd/` out of the history path file.
- MOD2-7 — §8.4 note 3: edges re-evaluated against the mapped core layer (03 §3.5.1), `itunes/service` edges stay.
- MOD2-10 / FE2-9 / COR2-9 — `examples/hello` from S2 (emits `core.json`), growing to S9; G6/G7 proven from S5.
- MOD2-11 — `internal/diag/flight` rewritten onto a `*slog.Logger`; R1 checked per stage (§8.5).
- MOD2-12 — Split cells are not history-path input (§2.2).
- MOD2-15 — empty `core/go.sum` committed at S0 (§8.1).
- MOD2-21 — no "wave" wording left.
- COR2-9 — §8.1 "minimum surface" column; `rbac` leaf types at S2, `events` at S4, revocation-journal types in `auth` at S4; kernel without storage until S2b.
- COR2-16 — `core/internal/keyedmutex` at S3 (§8.1, §9).
- DATA2-6 — `storage/keys` lands at S0 so S0 writes `c/storage/install` and the ledger in core's encoding.
- FE2-4 — `@mediacore/ui-kit` and `@mediacore/shell` created at S9 with shell adoption; UI-0 creates only `@mediacore/api-client` (same stage as 07 §7.19 steps 2 and 4).
- SIMP2-4 — `notify` + `notify/webhook` land at S7.
- DATA2-12, DATA2-29, SIMP2-9 — verified, no change: 04's v1 ops-store statement, census mechanism to `storage`, conformance engine stays in ABO.
