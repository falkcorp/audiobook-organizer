<!-- file: docs/plans/platform-core/reviews/spec-round-2-correctness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0b412cb5-21cb-4a12-824b-59baed9445d6 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 2: correctness and contracts

Lens: **correctness and contracts** (ID prefix `COR2`). Inputs: the revised spec in
`docs/specs/platform-core/` (03 v1.4.0 normative), `spec-round-1-correctness.md`, and
`spec-round-1-dispositions.md` (cross-cutting resolutions treated as binding). Code anchors are at
`748fd787a`. Spec anchors are `file:line` in `docs/specs/platform-core/`.

Method: four flows were traced end to end through the Go blocks of the revised files, and every
type a block names was looked up in the file that owns it. Imports were checked against R4 (L0–L9)
and against the landing order in 04 §8.1:

1. ABO scan → `RunItems` → keyspace writes → outbox → realtime → UI.
2. Module route → `httpapi` → `rbac` → `audit`.
3. `app.New` with the `hello` module and the `books` module (including the `examples/hello` blocks,
   checked against the current 05-*/06 definitions).
4. TV acquisition at proof depth, from grab through the download events to the import.

**Verdict.** The round-1 blockers are fixed in their core shape: there is now one download poller,
`storage` and `storage/index` no longer import each other, and modules have scoped handles. What
remains is mostly in the wiring between files:

- Modules still cannot reach two services ABO needs: the realtime legacy projections and `notify`.
- The outbox relay's own task definition cannot deliver.
- The retention rule drops messages that are meant only for core consumers.
- Seven files use schedule keys that 05-tasks rejects.
- The boot-phase order disagrees across 06, 05-config and 05-storage.
- The acquire import path reads one topic through two unordered consumers.
- `hello` compiles, but boot refuses it (undeclared settings permission).

No finding below is a BLOCKER on its own. Each one is a contract that fails when implemented as
written.

## Regression check (round-1 BLOCKER and MAJOR, correctness lens)

| R1 ID | Sev | Status in round 2 | Where it went |
|---|---|---|---|
| COR-1 | BLOCKER | **Partly fixed.** `Deps` gained Downloads/AI/Backup/`Storage.Aux`; `Registrar` gained ChangeSink/OutboxHandler/Realtime/Topics; the acquire construction cycle was removed (A8). Still unreachable: realtime legacy projections (`Hub.Projection`) and `notify.Service.Publish`. | COR2-1, COR2-3 |
| COR-2 | BLOCKER | Fixed. `StorageHandles` lives in `module` (06:470-476); `storage` imports neither `storage/index` nor `tasks` (05-storage:95). | — |
| COR-3 | BLOCKER | Fixed for ownership: one poller, one recovery path; `GrabRequest.Owner`/`Refs`; `RecordFunc` writes the TD in the grab-record batch. New defects on the consumer side. | COR2-10 |
| COR-4 | MAJOR | Fixed in signature (`ImportTx`, `TransferIn` on `Manager` and `Scope`). Residues: the import runs under a core task kind (G8), and the doc names the wrong handle. | COR2-11, COR2-19 |
| COR-5, COR-6 | MAJOR | Fixed (`Engine.Grade`, `CutoffUnmet`, `HeldFacts`, three-valued `Propers`; `ix/avail` evaluated at read time). | — |
| COR-7 | MAJOR | **Half fixed.** Two lock mechanisms are named. The lock package stays in ABO. `AppendKey` does not exist. Import "partitioning" is per run, but runs are per message. | COR2-16, COR2-11 |
| COR-8 | MAJOR | Fixed: one order in 06 §6.5.2, referenced by 05-events and 05-tasks. Residue: core components that call module code are not placed in that order, and derived-index hooks run before `Register`. | COR2-15 |
| COR-9 | MAJOR | Fixed for outbox consumers. The same defect reappears in other core tasks that run module write callbacks. | COR2-11 |
| COR-10, COR-11 | MAJOR | Fixed (commit-ordered intake; `ConsumeRaw`/`ConsumeIntoTaskRaw`; `outbox.Topic.Spec` returns an `events.TopicSpec`). | — |
| COR-12 | MAJOR | Fixed for module consumers. The new rule ignores core consumers. | COR2-4 |
| COR-13, COR-14, COR-15 | MAJOR | Fixed (held runs; enablement in the boot record; hash chain dropped). | — |
| COR-16 | MAJOR | **Half fixed.** `hello` now compiles, but boot refuses it. `rbac.Scope` survives in 05-health. | COR2-14 |
| COR-17 | MAJOR | Fixed: no `EntityRef` identifier remains in the spec set. | — |
| COR-18 | MAJOR | Fixed (`internal/<capability>http` at L6). Residue: `ui` registers on `CoreGroup` against 05-http-api's owner rule. | COR2-19 |
| COR-21 | MAJOR | Fixed: every scoped handle type is defined in its owning file (06:494-510). | — |

## Flow traces (where each one breaks now)

| Flow | Path | Breaks at |
|---|---|---|
| 1 ABO scan → UI | `books.Register` → `r.Tasks` → `RunItems(rc, tasks.CPU(), …)` → `ic.Writer().Do` → `Legacy.NewBatch()` + `index.MarkDirty(b)` + `outbox.Stage(d.Outbox, b, …)` → commit → `events.Publish(d.Events, …)` → hub raw subscription → SSE. From S6 the UI is still on the legacy URLs. | No path to mount the legacy projections (COR2-1). The task-run redactor is not allowed on a core topic (COR2-2). Webhooks cannot be published (COR2-3). The relay never delivers (COR2-5). Core-only durable topics are dropped (COR2-4). Durable+Realtime topics never stream (COR2-17). |
| 2 Route → RBAC → audit | `httpapi.Handle(r.HTTP(), Route{Perm})` → stage 12 `auth.Service.Middleware` (`rbac.WithChecker`, `audit.WithActor`) → stage 13 `Checker.Satisfies` → handler → `d.Audit.RecordIn(ctx, b, e)` | `RecordIn` takes `storage.Batch` by value (COR2-12). Otherwise it composes. |
| 3 `app.New(hello, books)` | P0a → P0 → P1 → P2 → P3 → P4 → P5 → P6 → P7 → P8 | The phase order contradicts 05-config and 05-storage (COR2-7). `DataDir` versus the storage path (COR2-8). Landing order: at S2 the books module opens the store and `hello` cannot exist yet (COR2-9). `Describe` (COR2-13). `hello` is refused at boot (COR2-14). Schedule keys are refused (COR2-6). |
| 4 TV acquire | `Engine.Grab` → `download.Pool.Grab(…, rec)` → `core.download.poll` → `core.download.item.completed.v1` → acquire `ConsumeTx` (TD transition) **and** `ConsumeIntoTask(core.acquire.import)` → `Importer.Import(…, ImportTx)` → `MarkImported` | Two unordered consumers of one topic, and no filter by owner (COR2-10). Module writes run under a core kind (COR2-11). The lock mechanism is inconsistent (COR2-16). `core.acquire.rss.interval` is refused (COR2-6). |

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 0 | — |
| MAJOR | 15 | COR2-1 … COR2-15 |
| MINOR | 4 | COR2-16 … COR2-19 |

## Findings

### COR2-1: MAJOR. COR-1 not fully fixed: the books module cannot obtain or mount the realtime legacy projections

**Where:** 06 §6.4.3 (`06-app-sdk.md:360-367`) and A8/P7 (`:72`, `:795`); 05-realtime §3
(`05-realtime.md:82-87`, `:119-121`), §4.9 (`:308-311`) and §5 step 1 (`:332`); 05-http-api §3.1
`Alias` (`05-http-api.md:205-208`); 09 S6 (`09-abo-on-core.md:489-490`).

**Evidence:**
- 06 says the books module "attaches its legacy stream projections (`Hub.Projection`)" through
  `Registrar.Realtime()`. The `realtime.Registrar` interface has one method, `Redact`. No module
  holds a `*realtime.Hub`.
- `Hub.Projection(p)` returns an `http.Handler`. Books must serve that handler at `/api/events`,
  `/api/v1/operations/events` and `/api/v3/ops/events`, and today those paths are routes in the gin
  manifest.
- 05-realtime §4.9 says the projections are registered "as aliases (05-http-api §4.6)". `httpapi.Alias`
  only re-exposes an **already-registered native route of the same group** ("same handler, same
  permission"). A projection has a different handler from the native stream, and the native stream
  is on `CoreGroup`, not on the books group. So `Alias` cannot express a projection.
- 05-realtime §5.1 builds the Hub "after the events catalog is frozen", which is after P6/P7.
  Modules use their realtime registrar during P6, so the Hub that would hand out the handler does
  not exist yet.
- 09 S6 cannot ship ("SPA keeps using the legacy streams through the projections") without one of
  these mechanisms.

**Fix:**
- Add `Projection(p realtime.LegacyProjection) http.Handler` to `realtime.Registrar`. During P6 it
  returns a lazy handler bound to the hub; the hub is built in P5 (its subscriptions to the catalog
  are attached in P7), and the handler answers 503 until P8. Books mounts it in its gin manifest at
  the three legacy paths (`OwnAuth: false`, `Perm: rbac.AnyAuthenticated` as for the native
  stream; per-topic permissions are applied per event inside the hub, 05-realtime §4.4).
- Alternatively, have `internal/eventhttp` register the three paths as core-owned legacy routes,
  with a `LegacyProjection` list supplied by books through `Registrar.Realtime()`, and remove those
  paths from the books gin manifest in the same S6 PR.
- Either way, change 05-realtime §5.1 to "Hub built in P5; redactors and projections recorded in P6
  and attached in P7", and replace "as aliases (§4.6)" in §4.9 with the mechanism chosen.

### COR2-2: MAJOR. The task-run redactor is placed on a core topic, which the registrar contract forbids (privacy regression versus HEAD)

**Where:** 05-realtime §4.7 (`05-realtime.md:272`) versus §3 `Registrar.Redact` (`:82-87`) and 06
(`06-app-sdk.md:362-363`, "one of the module's OWN Realtime topics"); 05-tasks `RunView.Current`
(`05-tasks.md:769`).

**Evidence:**
- HEAD withholds run error text and every worker's current-item label from callers without
  `users.manage`, because a label can name a user (`internal/server/handlers/opsv3/events.go:609-616`,
  `view.go:59-64`).
- The revised spec gives that job to "books' redactor" on `core.task.run-changed.v1`.
  `Registrar.Redact` accepts only the caller's own topics. A foreign namespace aborts boot.
- Implemented as written, one of two things happens: books' registration fails boot, or the
  redactor is dropped and every `AnyAuthenticated` principal receives user-identifying labels on the
  native stream and on projection C.
- The REST path (`GET /runs`) has the same need ("redacted per 05-realtime"), and 05-realtime has no
  hook for REST.
- 05-realtime also names `core:users.view` where HEAD gates on `users.manage`.

**Fix:**
- Make redaction of `RunView.Current` (and of any error text added later) a property of the core
  topic and of the tasks read model. `tasks` sets a `Redact`/`Visible` pair on the
  `core.task.run-changed.v1` catalog entry that blanks `Current` unless the subscriber holds
  `core:users.view`. The same function runs in the `GET /runs[/{id}]` serializer.
- Delete "books' redactor" from 05-realtime §4.7.
- State which scope is the canonical successor of HEAD's `users.manage` gate (05-rbac §6.1 maps
  `users.manage` → `core:users.manage`, which implies `core:users.view`).

### COR2-3: MAJOR. Modules have no handle to `notify`, and 05-events and 05-notify describe two incompatible delivery models

**Where:** 05-notify §3 `Service.Publish(ctx, b *storage.Batch, n)` (`05-notify.md:116`) and §4
steps 2–4 (`:156-165`); 05-events (`05-events.md:209`, "Core subscribers (the realtime hub, notify
routing)"); 06 `Deps` (`06-app-sdk.md:435-466`); 09 S6 (`09-abo-on-core.md:486-493`).

**Evidence:**
- 05-notify has modules call `notify.Service.Publish` with their own batch, so the notification is
  staged atomically with the state change. `module.Deps` has no `Notify` field, and no other `Deps`
  field reaches a `*notify.Service`.
- 05-events instead lists "notify routing" as a kernel bus subscriber. That is the lossy channel
  which 05-events §5E.5.4 forbids for external-system effects such as webhook delivery.
- 09 S6 moves ABO's webhook plugin onto notify, and its done-check requires a webhook golden that is
  byte-equal to S5. Under either reading, books has no specified way to emit the 12 legacy event
  types as notifications.

**Fix:**
- Add `Notify notify.Publisher` to `module.Deps`, scoped to the module's own catalog topics that
  carry `Notify` metadata (`Publish(ctx, b, n)`; refuses other topics). Add the row to 06's owner
  table.
- Delete "notify routing" from 05-events' list of core bus subscribers.
- State in 05-notify §4 that the only entry point is `Publish` (outbox-backed), and that a topic's
  `NotifyMeta` alone produces no notification.

### COR2-4: MAJOR. Outbox retention counts only manifest-declared consumers, so durable topics consumed only by core are deletable at once

**Where:** 05-events §5E.5.2 Retention (`05-events.md:368`); core consumers in 05-events §5E.4
(`:284-297`), 05-notify §4 (`core.notify.dispatch.v1`/`core.notify.deliver.v1`, `05-notify.md:159-165`),
05-download §4.5 and 10 §10.5.4 (acquire consumes `core.download.*`).

**Evidence:**
- A message "is deleted when every consumer **declared** for its topic — in the manifests of enabled
  and disabled modules — has a persisted cursor past it".
- Core consumers (notify's dispatch and deliver, acquire, the webhook adapter) are registered by the
  kernel, not declared in any manifest.
- For a topic consumed only by core (`core.notify.dispatch.v1`, `core.notify.deliver.v1`, and every
  `core.download.*` topic in a TV app), the condition holds vacuously, so a retention sweep may delete
  a message before its consumer has run. Read the other way, such a topic counts as "topics with no
  declared consumer", and `orphan_retention` (7 d) drops its messages whether or not they were acked.
- Either reading silently loses notifications. In TV/Movies it loses completion events, which are
  the correctness-critical path.

**Fix:**
- Define the declared-consumer set as `Manifest.OutboxConsumers` of every manifest in the build ∪
  the kernel's static core consumer list (`kernel/catalog` declares core consumers by name, e.g.
  `core.notify.dispatch`, `core.notify.deliver`, `core.acquire.download-events`, `core.acquire.import`).
- Apply `orphan_retention` only to names in neither set.
- Add `TestCoreOnlyTopicRetainedUntilAck` to 05-events §5E.12.

### COR2-5: MAJOR. The relay's task definition cannot deliver, and outbox-triggered writer runs default to Preview

**Where:** 05-events §5E.5.3 (`05-events.md:373-389`); 05-tasks registration rules
(`05-tasks.md:223-224`), the mode default (`:589`), the timeout default (`:547`), the watchdog (`:549`),
"what is not a task" (`:668`); `outbox.ConsumeIntoTask` params (`05-events.md:275-282`).

**Evidence:**
- The relay's effects are given as "`ReadOnly` plus `Writes(tasks.Res("core","outbox"))`". These are
  two mutually exclusive constructors.
  - Read as `ReadOnly`: `rc.Writer()` is nil, so "the batch goes through the relay run's fenced write
    path" (§5E.5.2) is impossible.
  - Read as `Writes`: the definition is a scheduled writer whose schedule
    `tasks.Every(time.Minute).OnStart()` says neither `Live()` nor `Preview()`. 05-tasks rejects that
    at registration, so boot fails. A relay started by a `Stage` nudge has an unset mode, which
    means **Preview** for writers (`:589`), so it records intents and delivers nothing.
- The relay drains for up to `relay_rotate` = 1 h. Its definition sets no `Timeout`, so the Task
  default of 30 min applies. Timeout is terminal (`timed_out`), so every relay run ends as a failure
  halfway through its rotation.
- An idle relay reports no progress, so it collects `stuck`/`never_reported` watchdog strikes
  (`:549`).
- 05-tasks §5T.3.8 lists "the outbox intake poll" as a non-task component, but 05-events puts the
  intake tick inside the relay run.
- The same Preview trap applies to every `ConsumeIntoTask` whose `params` callback returns the zero
  `tasks.ModeRequest`: the import run would be a Preview, and the message is consumed anyway
  (05-tasks §5T.3.12: a failed or empty run is not a redelivery).

**Fix:**
- Specify the relay exactly: `Effects: tasks.Writes(tasks.Res("core","outbox"))`,
  `Schedule: tasks.Every(time.Minute).OnStart().Live()`, `Timeout: relay_rotate + 5m` (or rotate at
  25 min), and `ProgressTimeout` satisfied by a heartbeat on each intake scan (or `Uninterruptible`
  with a reason).
- State that a nudge start uses `ModeLive`.
- Move "outbox intake poll" out of 05-tasks' non-task list.
- Make a run whose `Trigger` is `Outbox` default to Live, or make `ConsumeIntoTask` reject an unset
  mode at registration through a required `outbox.Live()`/`outbox.Preview()` option.

### COR2-6: MAJOR. Seven files declare schedule keys that 05-tasks refuses at registration

**Where:** 05-tasks §5T.3.8 `ScheduleFromConfig` (`05-tasks.md:612`, "key must be
`core.schedules.<name>` or `modules.<id>.schedules.<name>` … anything else … fails registration"),
the registration rule (`:224`: "declared with type `tasks.ScheduleConfig`") and `:654-659` (which
names `core.backup.schedule`, `core.download.poll_interval`, `core.ai.batch_poll_interval`,
`core.integrations.health_interval` as keys that must **become** schedule keys).

**Evidence:** these keys remain in their owning files.

| File | Key used | Problem |
|---|---|---|
| 05-backup:515-517, :632, :640 | `core.backup.schedule`, `core.backup.verify_schedule` | wrong prefix |
| 05-library:415, :565 | `core.library.quarantine.prune_schedule` | wrong prefix |
| 05-logging:447, :354 | `core.log.journal.prune_every`, typed `duration` | wrong prefix and type |
| 05-metadata:442, :478 | `core.integrations.health_interval`, typed `duration` | wrong prefix and type |
| 05-ai:467, :515 | `core.ai.batch_poll_interval`, typed `duration` | wrong prefix and type |
| 05-download:456 | `core.download.poll_interval` | wrong prefix |
| 10:676, :760 | `core.acquire.rss.interval`, typed `duration` | wrong prefix and type |

Only 05-health (`core.schedules.diagnostics.prune`) and 05-updater (`core.schedules.updater.*`)
comply. As written, the S7 and S8 builds fail `app.New` with a registration error, and ABO does not
boot.

**Fix:**
- Rename to `core.schedules.<subsystem>.<name>` with type `tasks.ScheduleConfig` and defaults
  expressed as `{enabled, every|daily_at}`: `core.schedules.backup.create`,
  `core.schedules.backup.verify`, `core.schedules.library.quarantine-prune`,
  `core.schedules.log.journal-prune`, `core.schedules.integrations.health`,
  `core.schedules.ai.batch-poll`, `core.schedules.download.poll`, `core.schedules.acquire.rss`.
- Keep the old names as `AliasWindow` config aliases with a `Convert` from duration to
  `ScheduleConfig` (05-config §1.3.6).
- Note `acquire`'s "0 disables" as `enabled: false`.

### COR2-7: MAJOR. The boot-phase order disagrees across 06, 05-config and 05-storage: writes land before the checkpoint, and config reads its keyspace before its own migrations

**Where:** 06 §6.5.1 P1–P5 (`06-app-sdk.md:789-793`; `:193` "kernel feeds them to rbac's Declarer in
P1"); 05-config §1.4 steps 3–7 (`05-config.md:510-520`); 05-storage §5S.4 steps 6, 8, 12
(`05-storage.md:1019-1026`) and §5S.3.12 ("If it cannot be taken, boot halts before anything is
written"); 05-storage §5S.3.6 (`:604-640`).

**Evidence:**
- **Order.** 06 puts storage open (P2) before `Store.Load` plus DB overrides (P3) before the
  checkpoint and migrations (P4). 05-config puts `Load` (step 3) before storage open (step 5) before
  `secrets.Open` (6) before "configstore runs the migration step for this boot … then
  `AttachOverrides`" (7). 05-storage issues handles to core subsystems only at step 12, after the
  checkpoint (6), the DAG (8) and the mirror rebuilds (9).
- **Writes before the checkpoint.** If 06 and 05-config are followed, configstore's per-boot legacy
  import or re-import (05-config §1.12.2) writes `c/config/` and `c/secrets/` before P4's single
  pre-migration checkpoint. That breaks G2 ("checkpoint before anything is written") and puts
  post-upgrade rows into the restore point.
- **Reads before migrations.** `config` and `secrets` read their own keyspaces before their own
  versioned steps, which sit in the same P4 DAG (05-storage tie-break list `storage, secrets,
  config, …`).
- **Handles before they exist.** Config would also need its `c/config/` handle before 05-storage
  step 12 issues it.
- **RBAC.** 06 feeds manifest permissions to `rbac.Service.Declarer` in P1, but `rbac.New` needs
  `Store: c/rbac/` (05-rbac:236-243), which exists only after P2/P4.
- **Data migrations.** 05-storage §5S.3.6 says a data migration "execut[es] the core task kind
  `core.storage.migrate`" with one run per migration. BootBlocking ones run at 5S.4 step 8 (P4),
  before the tasks service is built (P5) and before the executor starts (Run step 5). `c/tasks/`
  handles are not issued until step 12.

**Fix:**
- Write one phase table in 06 and make 05-config §1.4 and 05-storage §5S.4 reference it:
  - P2 open + guard + spec validation;
  - P3 `Store.Load` from operator layers and the boot record only (no DB);
  - P4 checkpoint → ratchet → DAG → mirror rebuilds;
  - P4b `secrets.Open` → configstore import (as a versioned `core/config` step inside the DAG, or
    strictly after the checkpoint) → `AttachOverrides`;
  - P5 services, including `rbac.New` and then `Declarer` per manifest.
- Move the "rbac Declarer in P1" sentence to P5. P1 keeps only the static namespace checks.
- State that BootBlocking data migrations run in-process through `tasks.ForEach` with persisted
  shard cursors, not as executor runs. Only `Online` ones are `core.storage.migrate` runs.

### COR2-8: MAJOR. Three sources for the data and database paths; P2 as written cannot open ABO's database in the shipped Docker layout

**Where:** 06 `app.Options.DataDir` (`06-app-sdk.md:534-536`, "the state directory … Pebble dir"),
P0a (`:787`) and P2 (`:790`, "Open storage under `DataDir` (`os.OpenRoot`)"); 05-config
`core.paths.state_dir`/`core.paths.data_dir` (`05-config.md:587-588`) and `Options.BootRecord`
opened on `core.paths.state_dir` (`:250`); 05-storage `core.storage.path` (`05-storage.md:1101`,
§5S.4 step 1 `:1010`); 05-backup P0a (`05-backup.md:470`, "data directory and the DB path are
resolved, from flags and env").

**Evidence:**
- The Docker image passes `--db` with a path outside any state directory (`Dockerfile:104`). The unit
  and the plist set the DB path only through `DATABASE_PATH` (09:55).
- Opening storage "under `DataDir`" through `os.OpenRoot` cannot reach such a path.
- 06 and 05-config name two different roots for the boot record and the keyring: `DataDir` versus
  `core.paths.state_dir`.
- P0a resolves paths "from flags/env" only. A DB path or data directory set in the YAML file (ABO
  maps `database_path` → `core.storage.path`, 05-config:816) therefore makes 05-backup step a)
  reject every staged restore ("recorded targets ≠ the paths resolved now"). The restore silently
  never applies (degraded `restore_rejected`).
- `app.Options` also has no `EnvPrefix`, which `config.Options` requires (05-config:246), and no
  derivation rule is given.

**Fix:**
- Drop `app.Options.DataDir`, or redefine it as the compiled default of `core.paths.state_dir`.
- Make P2 resolve `core.storage.path` from the `BootView` (05-storage §5S.4 step 1) and `os.OpenRoot`
  the DB's parent directory.
- Make P0a read the operator layers (file, env, flags) through `config.Store.Boot` for
  `core.paths.*` and `core.storage.path` before the swap. The config file is the restored one only
  when `ReplaceConfig`, and then it is swapped last.
- Add `app.Options.EnvPrefix`, defaulting to `strings.ToUpper(AppID)` (ABO: `ABO`).

### COR2-9: MAJOR. The landing order contradicts the import graph that the 05 files specify; S2's storage ownership and the `hello`/G6 landing are unspecified

**Where:** 04 §8.1 (`04-package-mapping.md:642-662`) and §8.2 waves (`:668-675`); 06 §6.11.1 Wave K0
(`06-app-sdk.md:958-960`); 09 S2 (`09-abo-on-core.md:409-416`) and S6 (`:488`); 05-http-api §3
(`05-http-api.md:122`); 05-authn (`05-authn.md:67`, `:231`); 05-rbac (`05-rbac.md:240`).

**Evidence:** 04's own rule is "may land before a lower-layer package as long as everything it
imports already exists". These edges break it:

| Package (stage per 04 §8.1) | Imports or types it needs | Lands at |
|---|---|---|
| `httpapi` (S2): `Route.Perm`/`LegacyRoute.Perm rbac.Requirement`; 09 S2 annotates 36 routes `AnyAuthenticated` | `rbac`, `auth`, `audit` | S4 |
| `httpapi` (S2) | `storage` (`Deps.Store`) | S2b |
| `auth` and `rbac` (S4) | `events` (`Deps.Events`) | S5 |
| `auth` and `rbac` (S4) | `backup` (`RevocationJournal`, `RestoreHook`) | S8 |
| `module`/`app` (S2): `Manifest`/`Deps` of 06 | `storage`, `rbac`, `tasks`, `events`, `outbox`, `datamigrate`, `storage/index`, `integrations`, `download`, `ai`, `backup` | S2b–S8 |
| `notify` webhook (S6) | `integrations` root (instance store and Descriptor); 09 S6 imports webhook URLs "into the instance store" | S7 |
| `examples/hello` ("Wave K0", with module/kernel/app = S2) | `storage`, `rbac`, `tasks` | up to S5 |

- 04 §8.2's "wave" table orders module/kernel/app **last** (wave 5, after rbac/auth/tasks), which
  contradicts §8.1 and 09 (S2).
- 09 S2 says "the store is still opened by books", but 06 P2 has the kernel open the store under
  `DataDir`. 06 defines no kernel mode that leaves storage to a module. That mode is needed at S2 to
  avoid a second Pebble open (LOCK conflict) or a new empty store (OPS-5 refusal).
- The G6 test `TestSecondModuleNoAudiobookEdit` and the `hello` landing appear in no stage.

**Fix:**
- Add to 04 §8.1 a "minimum surface per stage" column, and have 06 mark each `Manifest`/`Deps`
  field with the stage that adds it.
- Land the leaf types early: `rbac` `Permission`/`Requirement`/`Need`/`AnyAuthenticated`/`Public` at
  S2, `events` at S4, and move `RevocationJournal`/`Revocation`/`RestoreHook` into a leaf
  `backup/journal` package (L4, imports `entity`, `storage`) that lands at S4. Alternatively, define
  them in `auth` and have `backup` import them.
- Move `integrations` root to S6, or move the webhook migration to S7.
- Delete or align 04 §8.2's wave table.
- State in 06 §6.5.1 that until S2b the kernel runs with `storage` absent: no P2/P4, books opens its
  store in a start hook, and `Deps.Storage` is not offered.
- Schedule `examples/hello` and the G6 test at S5 (the first stage at which every import exists),
  and record that G6/G7 are proven from S5.

### COR2-10: MAJOR. acquire consumes `item.completed` through two unordered consumers, and no consumer form can filter by owner

**Where:** 10 §10.5.4 tasks table and consumer paragraph (`10-tv-movies-proof.md:677`, `:680-684`)
and the transition table (`:697-707`); 05-download §4.5 "Routing by owner" (`05-download.md:470-475`);
05-events `ConsumeIntoTask`/`ConsumeTx` (`05-events.md:275-297`) and §5E.5.2 ordering (per consumer
and partition only).

**Evidence:**
- acquire applies the TD transition `Downloading → ImportPending` (and sets `OutputPath`) with a
  `ConsumeTx` consumer of `core.download.item.completed.v1`.
- It starts `core.acquire.import` with a **second** consumer of the same topic, `ConsumeIntoTask`.
- Two consumer names have independent cursors and no relative order. The import run can therefore
  start while the TD is still `Downloading`, with no `OutputPath`. `Downloading → Importing` is not
  in the transition table (`ErrIllegalTransition`, "transition refused, error logged").
- The message has already been consumed, and a failed run is "the run's outcome, not a redelivery"
  (05-tasks §5T.3.12). The download sits in `ImportPending` with no import ever scheduled.
- Separately, 05-download says "a consumer ignores messages it does not own" and 10 says acquire is
  "filtered to `Owner == core.acquire`".
  - `ConsumeIntoTask[Raw]` has no filter. Its `params` callback can only return a run or an error,
    and an error is retried and then dead-lettered.
  - So every completion of every owner creates a `core.acquire.import` run, and every module
    consumer (which must be `ConsumeIntoTask` under COR-9) creates a run for other owners'
    downloads too.

**Fix:**
- Add a filter option to every consumer form: `outbox.Where(func(Message[T]) bool)` (raw:
  `func(RawMessage) bool`). Unmatched messages are acked without effect.
- Give core `ConsumeTx` handlers a way to stage a run in the relay batch, for example
  `TxHandlerFunc(ctx, tx *outbox.Tx, m)` with `tx.Batch()` and `tx.StartRun(kind, req)` (backed by
  `Executor.StartIn`).
- Rewrite 10 §10.5.4: **one** acquire consumer (`core.acquire.download-events`, `ConsumeTx`,
  `Where(Owner == "core.acquire")`) applies the transition and, for `ImportPending`, stages the import
  run in the same batch.
- Add `TestImportNeverRunsBeforeTransition` to 10 §10.5.9.

### COR2-11: MAJOR. Core task kinds execute module write callbacks, so the module's writes bypass G8 (COR-9's rule is not applied beyond the relay)

**Where:** 10 §10.5.4 `core.acquire.import` runs the module `Importer` (`10-tv-movies-proof.md:677`)
and §10.5.6 imports "partitioned by union-find over `TD.Refs`" (`:745-746`); 05-ai §4 step 5,
`core.ai.batch-poll` "runs the module's handler" (`05-ai.md:465-468`); 05-tasks registration rule
"`Effects.Writes` naming another module's resource" is refused (`05-tasks.md:221`) and §5T.3.12.

**Evidence:**
- COR-9 established that a module's entity writes must run under a run whose `Effects`, write-set
  conflict rule, fence and ref locks are the module's own.
- `core.acquire.import` (a `core.` kind) calls `Importer.Import`, which writes `m/tv/` rows and moves
  files. `core.ai.batch-poll` calls books' `BatchHandler`, which writes books entities.
  - Neither core kind may declare the module's resources (`:221`).
  - So the dispatch conflict rule never serialises an import against `tv.disk.rescan`, or a batch
    apply against `books.dedup.*`. `Writer.Do` would refuse (`ErrOutsideEffects`), or must be
    bypassed.
- 10's "partitioned by union-find, `Fixed(2)`" describes one Batch run. `ConsumeIntoTask` creates
  one run per message. Cross-run serialisation therefore needs `SubjectsFunc` or `LockRefs`, which
  10 does not declare.

**Fix:**
- Make the import a **module** kind. `acquire.ImportTask[T](engine, effects tasks.Effects)
  tasks.Definition` returns `<module>.acquire.import` with
  `Subjects: func(params) → TD.Refs`. The module registers it with `r.Tasks(...)`, and acquire's
  consumer stages it (COR2-10's `tx.StartRun`).
- Do the same for AI: `ai.BatchApplyTask(cap, h, effects)` → `<module>.ai.batch-apply`, started by
  `core.ai.batch-poll` through `Client.Start`/`StartIn` instead of calling the handler inline.
- Replace "partitioned by union-find" in 10 §10.5.6 with "serialised by `SubjectsFunc` over
  `TD.Refs` (dispatch conflict rule) plus `ic.LockRefs` in the importer".
- Add a rule to 05-tasks §5T.3.12: a core run never invokes a module callback that writes module
  entities.

### COR2-12: MAJOR. Participant APIs take `storage.Batch` by value, so the `Bind` grant and the staged writes are made on a copy

**Where:** 05-rbac `audit.Logger.RecordIn(ctx, b storage.Batch, e)` (`05-rbac.md:391`; the `audit`
import block at `:337-343` also omits `storage`); 05-authn `Store.PutUser`/`PutSession`/
`DeleteSession`/`PutAPIKey`/`PutOneTime`/`PutIdentity`/`PutSide(b storage.Batch, …)`
(`05-authn.md:403-426`); 05-config `secrets.(*Store).Stage(b storage.Batch, …)` and `StageLegacy`
(`05-config.md:1036-1037`). Owner: 05-storage, where every method is on `*Batch` (`05-storage.md:219-245`).

**Evidence:**
- The participant pattern (DATA-18, COR-4) works because each participant `Bind`s its own handle to
  the caller's batch and writes through it. Commit then checks every write against the bound set.
- On a by-value copy (`RecordIn(ctx, *b, e)`), `Bind` mutates the copy. The initiator's `Commit`
  either refuses the audit, auth or secret rows with `ErrOutsideKeyspace`, or, if the copy shares the
  engine batch, applies them without the binding check. `go vet` copylocks flags the copy only if
  `Batch` happens to embed a lock.
- These are exactly the writes whose atomicity the spec relies on: "the change and its record commit
  or fail together" (05-rbac §7.2, `:789`); the mirror and side writes of 05-storage §5S.3.11; the
  configstore commit.

**Fix:** change every listed signature to `b *storage.Batch`, add `storage` to the `audit` import
list, and add a sweep to `TestSpecGoBlocksCompile` (or an `archtest` check) that rejects a
`storage.Batch` value parameter in any core API.

### COR2-13: MAJOR. `app.Describe` runs only P1 and P6, which violates `Register`'s own precondition

**Where:** 06 `Describe` (`06-app-sdk.md:596-600`) versus `Module.Register` doc (`:152-160`: "after
core and module migrations have run, config is loaded and every core service exists (A8)") and
`Deps` ("Every field is non-nil", `:431-435`); consumers: the app `openapi` subcommand, `core.json`
freshness (`:611-614`), 07 §7.8 `manifest.gen.ts`.

**Evidence:**
- Without P3 there is no loaded config, so `module.Setting` returns defaults or records
  `ProblemSettingsUndeclared`.
- Without P5 there are no services behind `Deps`.
- Without P7 route permissions cannot be validated against the frozen catalog (`ErrUnknownScope`).
- Books' `Register` at K1 "builds today's `*server.Server`" (06:962-963), which does real work, for
  example loading the HNSW snapshot in `NewServer` (`internal/server/server.go:839-841`).
- The CI generators that depend on `Describe` would therefore either touch a real data directory or
  fail.

**Fix:**
- Define `Describe` as P0–P7 against `vfs.NewMem`, a temp `os.Root` and defaults only (no P0a, no
  operator file, no listeners, no `Run`), then `Router.Build` without serving.
- Add to the `Module` contract: "`Register` must be side-effect-free outside the handles in `Deps`
  (no file or network I/O)".
- Note that books meets this only once its server construction moves into start hooks. Until then
  ABO's `openapi` subcommand runs on the core document only.

### COR2-14: MAJOR. COR-16 half fixed: `hello` compiles but boot refuses it, and stale vocabulary remains

**Where:** 06 §6.12.1 (`06-app-sdk.md:1079-1170`), §6.10 `TestHelloEndToEnd`; 05-config
"Default section permissions" (`05-config.md:411-413`); 06 §6.6 (route or task "uses undeclared
permission" → fail-closed); 05-health `Viewer.Can func(rbac.Scope) bool` (`05-health.md:231`); 06
§6.4.1 import block (`:101-126`).

**Evidence:**
- `hello` declares the section `modules.hello` but no `hello:settings.manage`. 05-config makes
  `<id>:settings.manage` the default read and write permission of a module section, "registered by
  the module in its permission list". The kernel then fails boot on an undeclared permission. If it
  did not, the settings panel and `TestHelloEndToEnd`'s "settings default/override" would be
  unreachable for everyone except `core:admin`.
- `rbac.Scope` does not exist; 05-rbac §3.1 names it `rbac.Permission`.
- The `module` import block omits `storage/index` (`StorageHandles.Indexes`),
  `storage/datamigrate` (`Manifest.DataMigrations`) and `ui` (needed by `ReservedIDs`).

**Fix:**
- Add `{Name: "hello:settings.manage", Title: "Change hello settings"}` to hello's `Permissions`
  (no `DefaultGrants`; restricted by SEC-25), or pass `config.Permissions("hello:read",
  "hello:admin")` to `config.Define`.
- Change `rbac.Scope` → `rbac.Permission` in 05-health.
- Complete the 06 import block.
- Have `TestSpecGoBlocksCompile` also **boot** the extracted `hello` (`coretest.New`), so that a
  manifest-level failure is caught, not only a compile failure.

### COR2-15: MAJOR. Module callbacks are invoked outside the module's lifecycle: `DerivedIndex` hooks are manifest data that storage calls before `Register`

**Where:** 05-storage §5S.3.9 `DerivedIndex{Build, Snapshot, Load}` inside `storage.Spec.Derived`
(`05-storage.md:470`, §5S.3.9), §5S.4 steps 11–13 (`:1025-1027`); 06 `Manifest.Storage`
(`06-app-sdk.md:211-215`) and `Manifest()` "must be pure" (`:147`) versus 06 `Registrar.Backup`
("registrar calls, not manifest data, because their Snapshot/Restore hooks close over services built
in Register", `:383-386`); 06 §6.5.2
(`:802-825`); 05-storage §5S.3.8 reconciler (`:855-860`).

**Evidence:**
- `DerivedIndex.Build`, `Snapshot` and `Load` are closures inside `storage.Spec`, which a module
  returns from `Manifest()`. `Manifest()` runs in P1, "pure, cheap and deterministic", before any
  service exists.
- 05-storage §5S.4 step 11 checks versions and generations, and so calls `Load`, during P4. That is
  before `Register` (P6) builds the services those closures need: books' hnsw store and memdb tables
  are constructed in books' wiring.
- 06 already moved backup artifacts to the registrar for exactly this reason. Derived indexes were
  not moved.
- `Build` is "run as a tasks definition", but no file names the kind. If it is a core kind, it runs
  module code under core `Effects` (the COR2-11 defect).
- 06 §6.5.2 still places only bus handlers and outbox consumers relative to module hooks.
  05-storage starts the index reconciler "with the task executor" (step 13), which is after hooks on
  start. Nothing places its stop, or the stop of module health checks and of config `OnChange`
  subscribers of module sections, relative to module `Stop` hooks.

**Fix:**
- Move derived indexes out of `storage.Spec` into a registrar call,
  `Registrar.DerivedIndex(storage.DerivedIndex)`, consumed in P7. The kernel runs the
  version/generation check and `Load` after P7. `Build` runs as a **module** kind
  `<module>.derived.<name>.build` that the registrar registers on the module's behalf.
- Keep only the declarative part (name, version, `Blocking`) in the manifest, so that the claim
  ledger and readiness gating still know about the index while the module is disabled.
- Add to 06 §6.5.2 the list of core components that invoke module callbacks (index reconciler,
  derived loader, module health checks, `OnChange` of module sections). Start them after step (3)
  and stop them before module hooks.

### COR2-16: MINOR. COR-7 residue: the grab-lock mechanism, `keyedmutex`'s home and the `Decide` pool are inconsistent

**Where:** 10 §10.5.6 (`10-tv-movies-proof.md:737-744`); 05-tasks §5T.3.4 (`05-tasks.md:468-474`);
05-config (`:1143`), 05-download (`:507`), 05-library (`:539`) versus 03 §3.2.1 (`03-architecture.md:146-149`),
04 (`:301`, `:914`), 09 S1 (`:398`), 12 (`:404`).

**Evidence:**
- 10 locks with "core `keyedmutex` … taken in `entity.Ref.AppendKey` order". `entity.Ref` has no
  methods (03:175), and 03, 04, 09 and 12 keep `internal/keyedmutex` **in ABO**: "E2c's package
  choice tested the mechanism, not a placement".
- 05-config, 05-download and 05-library (all built) cite "keyedmutex moves to core per 03 §3.2.1 E2".
  Followed literally, that is an R1 violation, because core cannot import ABO's package.
- 05-tasks says the grab holds the executor's ref-lock table, and calls it `Service.Grab`. 10 says
  `Engine.Grab` with a separate mutex, so module `ExclusiveRefs` and the grab lock do not interlock.
- `Decide` "sized by the calling task's `tasks.CPU()`" takes no `Parallelism` (COR-23's point).

**Fix:**
- Add a core-internal copy `core/internal/keyedmutex` to 04 (§9 row "a core package needs one",
  `:790`) at S3. Change the three 05 citations to it.
- In 10, use the executor ref-lock table (`tasks.LockRefs`-equivalent for ctx callers, sorted by
  `tasks.FormatRef`), and name the method `Engine.Grab` in 05-tasks.
- Give `Decide` a `par tasks.Parallelism` argument, or state that it calls
  `tasks.ForEach(ctx, tasks.CPU(), …)`.

### COR2-17: MINOR. A Durable topic with Realtime metadata silently streams nothing

**Where:** 05-events catalog validation (`05-events.md:116-118`) and `Publish` (refuses Durable topics,
`:172-176`); 05-realtime §4.5 (`05-realtime.md:226`, the hub holds "one raw bus subscription per
Realtime topic").

**Evidence:**
- Durable topics never reach the bus. The hub subscribes only to the bus.
- Validation does not reject `Durable != nil && Realtime != nil`, so such an entry boots and never
  delivers to the UI.
- 05-download relies on a separate notify-only topic (`core.download.item.changed.v1`) for this,
  which shows the gap is known but not enforced.

**Fix:** add "Realtime on a Durable topic" to the registration-time rejections in 05-events, with
the message "declare a companion bus topic". Alternatively, have the relay publish each sequenced
durable message to the bus after its intake batch commits; pick one.

### COR2-18: MINOR. `ui.WriteManifestTS` takes an `app` type: an L6 → L9 import in a normative Go block

**Where:** 07 §7.6.1 (`07-ui-shell.md:567-568`) versus the prose at `:571-573` and R4.

**Evidence:** the block declares `func WriteManifestTS(w io.Writer, m app.UIManifest) error`. The
next paragraph says it cannot, and that the type is `ui.ModuleManifest`. `TestSpecGoBlocksCompile`
and the core layering test would both fail on the block as written.

**Fix:** change the signature to `m ui.ModuleManifest` and define `ModuleManifest` in the block.

### COR2-19: MINOR. Naming and vocabulary contradictions across files

**Where / Evidence / Fix** (one line each):

| Item | Where | Fix |
|---|---|---|
| Audit action grammar | 03 §3.6 `<moduleID>.<noun>.<verb>` (`03-architecture.md:329`) versus `core.role.updated`/`books.user.merged` (05-rbac:365) and the 4-segment `core.system.backup.restore_staged` (05-backup:446) | pick present-tense `<ns>.<noun>.<verb>` and fix the examples |
| Importer doc | 10 `Importer` comment says `Manager.TransferIn` (`10-tv-movies-proof.md:551-552`); modules hold `library.Scope` (06:448) | "`d.Library.TransferIn(tx.Batch(), …)`" |
| `config LegacyKeys` | 11 §11.3 (`11-multi-app.md:46`); no such field (05-config uses `config.Aliases`) | "config `Aliases`" |
| Core topic table | 05-events lists only `grab.added`/`grab.failed`/`item.changed` for 05-download (`05-events.md:128`); 05-download declares `item.{completed,failed,removed}.v1` too | list all six |
| `httpapi.ReservedSegments()` | said to include every `LegacyMount.Segments` (05-http-api §4.1), but it is a static function evaluated at P1, before any `MountLegacy` (P6) | state that compat top-level segments reach P1 only through `app.Options.ReservedAPINamespaces` |
| `CoreGroup` owner rule | "used only by core/internal/<capability>http" (05-http-api:431) versus `ui` registering `ui/diagnostics` and `ui/csp-report` on it (06:630, 07 §7.6.2) | add `ui` to the rule, or move its routes to `internal/uihttp` |
| "S" collisions | 09 stages S0–S10, 12 readiness criteria S1–S10, 05-storage decisions S1–S11 (`05-storage.md:24-36`) | rename 12's criteria `R1–R10` (or `RC1–RC10`) and 05-storage's decisions `D-S1…` |
| Stale gin note | 06 §6.11.4 "the eight `gin.SetMode` disappear with gin" (`06-app-sdk.md:1041`); gin is permanent (cross-cutting 2) | "stay with the books legacy mount" |
