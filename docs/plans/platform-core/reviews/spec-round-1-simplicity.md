<!-- file: docs/plans/platform-core/reviews/spec-round-1-simplicity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 48117f87-0a12-407a-9966-0d5cda8845ce -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 1: simplicity and scope (SIMP)

Lens: over-engineering, YAGNI, premature abstraction, duplicated mechanisms, spec length compared with its
value, and framework swaps whose cost is larger than their benefit. Every fix keeps D1–D9, R1–R8 and G1–G8.
Where a fix gives something up, the **Trade** line names it.
Code anchors are at `748fd787a`. Counts come from `git grep` at that commit unless they cite the spec.

**Counts:** 0 BLOCKER, 15 MAJOR, 12 MINOR.

## Summary table

| ID | Sev | Where | One line |
|---|---|---|---|
| SIMP-1 | MAJOR | 03 §3.3, §3.4 | The promotion rule is circular: whatever 03 §3.3 lists counts as "platform". Several packages have no consumer during S0–S11 and are not marked "later". |
| SIMP-2 | MAJOR | 05-http-api §8, 09 S10, N7 | Retiring gin couples a router swap, a path rename, OpenAPI typing and a new TS client, and the spec gives no cost estimate (184 + 262 files import gin). |
| SIMP-3 | MAJOR | 05-config §1.12 st.2–3, 05-logging §11 st.3 | Removing ABO-side globals (about 4,000 call sites) is justified only by multi-app, which is the lowest priority. |
| SIMP-4 | MAJOR | 05-config §1.12/§2.2, 05-metadata M1, 05-download §7, 05-notify §10, 06 Deps, 09 S6/S7 | Three overlapping stores for integration config and secrets, and they give contradictory key names. |
| SIMP-5 | MAJOR | 05-events, 05-notify §3, 05-realtime §4.7, 06 Registrar | Four topic catalogs, two of them with their own permission declarations and diverging names. |
| SIMP-6 | MAJOR | 05-tasks §5T.3.8/§5T.10, 05-backup, 05-updater, 05-download, 05-ai, 05-health, 05-config, 09 S5 | The "one timer" claim is false. Schedules are configured through about 12 bespoke keys in 3 formats, and books schedules have 3 different homes. |
| SIMP-7 | MAJOR | 02 Q5, 05-http-api §4.6, 05-config §1.7, 05-metrics M6, 09 §9.2.1/§9.2.7 | The compatibility window is defined 4 times. An httpapi alias turns into 410 by clock, which breaks 09's usage gate. |
| SIMP-8 | MAJOR | 05-http-api §4.3–§4.5, §4.2 st.13 | An API-polish bundle on every route: cursor+HMAC paging by default (breaks UI page counts), an idempotency store, ETags, a per-principal limiter. |
| SIMP-9 | MAJOR | 05-backup B3/§5B.3.3, 05-config §2.4 | Default backup encryption, an install identity and the keyring inside the archive form a circular design. Its motivation disappears after S3/S4. |
| SIMP-10 | MAJOR | 05-notify §1, §3.1, §4, §7 | New product features with zero consumers: inbox, per-user subscriptions, personal channels (on by default), templates, presets. |
| SIMP-11 | MAJOR | 05-ai §1, §4 items 5–6, §7 | The AI usage ledger, monthly token cap and batch usage accounting are new features. ABO is the only AI consumer. |
| SIMP-12 | MAJOR | 05-download §1, §4.1, §4.3, §4.5 | Phase 1 builds Grab, pool selection, the monitor and outbox, pending recovery and MarkImported, but "ABO never grabs". |
| SIMP-13 | MAJOR | 03 §3.6, 05-metrics §4/§13, 09 §9.2.7 | Renaming 73 books metric families buys nothing. Three files disagree on the app prefix and on the compat mechanism. |
| SIMP-14 | MAJOR | 05-config §1.10 vs 05-http-api §1.1/§7 | Two contradictory JSON Schema reflector decisions: in-house ~400 LOC vs `google/jsonschema-go`. |
| SIMP-15 | MAJOR | 05-config §1.12, §2.10 vs 09 S3/C3 | The config and secrets migration is specified twice with contradictory semantics. |
| SIMP-16 | MINOR | 03 `entity`, 05-download/notify/metadata/08, 05-storage, 05-rbac, 05-tasks | Five spellings of one `{Module, Kind, ID}` reference type. |
| SIMP-17 | MINOR | 05-tasks §5T.3.2, 05-storage §5S.3.6, 10 | `Fixed` and `IO` are the same thing; 10 uses a `tasks.Network` that does not exist; storage `Shards` duplicates `Parallelism` (and breaks R4). |
| SIMP-18 | MINOR | 09 §9.2.2 vs 05-storage §5S.3.7 | The compat-level guard is a second copy of the storage-format guard (stamp + sidecar + flag + script). |
| SIMP-19 | MINOR | 05-config §2.3–§2.7 | Keyring rotation, prune and the backup-manifest coupling ship in v1, but rotation does not exist today. |
| SIMP-20 | MINOR | 05-rbac §7.4, §7.6 | The audit hash chain gives no protection in the spec's own threat model. |
| SIMP-21 | MINOR | 05-tasks §5T.3.1 | The `Pipeline` shape has zero production definitions (v3 has zero native definitions at all). |
| SIMP-22 | MINOR | 06 §6.4.2, 05-http-api §4.9 | Multi-app residue in the SDK: `DependsOn`, `MinCoreVersion`, generalized `PrefixClaim`. |
| SIMP-23 | MINOR | 05-authn §3.3, §4.8 | The `auth/clientcert` authenticator has no consumer. |
| SIMP-24 | MINOR | 10 §10.5.3, 08 §8.6.2 | About 570 lines of `acquire` Go API and about 215 lines of wire DTOs, for work that D5 limits to proof depth. |
| SIMP-25 | MINOR | 07 §7.19 step 3, 13 E4 | The react-router 8 migration (121 files) is bundled into the shell stage instead of preceding it. |
| SIMP-26 | MINOR | 03 §3.7 | The diagram says "container"; 03 §3.3 and 06 A2 say there is no DI container. |
| SIMP-27 | MINOR | 05-http-api §7, 07 §7.8 | `books-legacy.json` has no consumer, and the "Go emitter fallback" is an unbounded in-house code generator. |

---

## Findings

### SIMP-1 — MAJOR — The promotion rule is circular, and several core packages have no consumer during the extraction

**Where:** 03 §3.3 (package table), §3.4 (promotion rule), R4 layer list; 09 §9.3–§9.4.

**Evidence:** 03 §3.4 says "a package enters core only if it is a platform concern (the list in §3.3) or
if it has two real consumers". The first condition points back at the same list, so it filters
nothing. 04 applies the second condition carefully to `shared/` (0 packages promoted, 04 §1 item 2) but
never applies it to the §3.3 rows. Only `acquire` is marked "public, later". These rows have no ABO
consumer and no stage in 09 that builds them:

| Item named by the lens | Consumers during S0–S11 | Planned consumers | Verdict |
|---|---|---|---|
| `acquire` | 0 (05-download §1: "ABO … never grabs") | TV, Movies | already later: keep |
| `arrfacade` | 0 (05-http-api §4.9 item 1: "ABO ships no *arr-v3 facade") | TV, Movies | **later**; S0–S11 keep only the `/api/v3/` reservation and the move of `syncapi/conformance` (08 §8.14) |
| `integrations/indexer` | 0 (05-indexer §10: "Nothing to migrate … The package is new") | TV, Movies | **later** (acquire wave) |
| download adapters beyond Deluge | 0 | TV, Movies | already phased (05-download §4.1): keep; but see SIMP-12 for the Pool |
| notify inbox, subscriptions, personal channels | 0 | none stated | **later** (SIMP-10) |
| AI usage ledger and monthly cap | 0 (new feature) | none (10 §10.1.2 has no AI row) | **later or books** (SIMP-11) |
| compat-level guard | ABO (G5) | all | **now**, but merge it with the storage-format guard (SIMP-18) |
| keyring rotation | 0 | none | **later**; key-ID format now (SIMP-19) |
| OpenAPI 3.1 + TS generation | shell (D6) + separate app repos | all | **now for core routes**; drop `books-legacy.json` (SIMP-27) |
| cursor pagination | 0 (UI uses offset + total) | none required | **opt-in** (SIMP-8) |
| RFC 9457 problem+json | every new native route | all | **now** (it is one type on new routes, and legacy routes keep their own envelope) |
| `auth/clientcert` | 0 | none | **drop** (SIMP-23) |

**Fix:** (1) Rewrite 03 §3.4's first condition as "a platform concern is code the kernel itself needs,
or code that at least one shipping app consumes **and** every planned app needs". (2) Add a column
"lands in" to the §3.3 table (stage S*n* or "acquire wave"). Set `arrfacade` and `integrations/indexer` to
"public, later (acquire wave)", like `acquire`. (3) Make 09 S7's "Done when" state that the indexer and
facade packages do not exist yet. (4) Add a 14 K6 mitigation: the "lands in" column is reviewed at every
stage gate.

**Trade:** The facade and indexer contracts are not exercised by compiled code until a TV/Movies repo
starts. The specs (05-indexer, 08) remain as design records, so no stated goal is dropped.

### SIMP-2 — MAJOR — Retiring gin (S10) bundles four changes and has no cost/benefit

**Where:** 02 N7; 05-http-api §1.1, §8.1 (H2..Hend), §8.3; 09 S10 "Done when: `go list -deps . | grep -c gin-gonic` = 0"; 07 §7.19 step 6.

**Evidence:** The cost at HEAD is: 962 `*gin.Context` lines in 158 non-test files; **184 non-test and 262 test
files import `gin-gonic/gin`**; 497 routes plus 75 ABS routes (05-http-api §2); 150 `ShouldBindJSON` and 80
`binding:` tags to rewrite as `Validate()` (§1.1); and the 8,747-line hand-written `web/src/services/api.ts`
to replace with generated clients. The benefit to **core** is zero: core never imports gin under N7 either
way. The benefit to ABO is 4 fewer modules in `go.mod` (`go.mod:12-15,31`), one error envelope, and typed
OpenAPI for books routes. S10 couples four separable decisions in one stage: (a) router swap, (b) path rename
`/api/v1/…` → `/api/books/v1/…`, (c) typed OpenAPI for books, (d) replacing `api.ts`. No file estimates the
effort. "Hend" makes a temporary mechanism the success criterion: `MountLegacy` is described as
"(shrinking)" (§8.3).

**Fix:** (1) Make `httpapi.MountLegacy` a **permanent, supported SDK feature** for app-owned compatibility
surfaces (ABS already needs this permanently, 08 §8.15). (2) Rewrite 09 S10 as "opportunistic": a books
route group moves to native only when it is rewritten for another reason or when the UI needs a typed client
for it. A group's path rename and its router move are separate, optional decisions. (3) Delete "gin = 0" from
S10 "Done when" and replace it with "every route is in the legacy manifest with a permission (R6)". (4) Before
any S10 PR, record a measured estimate in 09: handlers × average diff size, from one pilot group (e.g.
`playlists`, 10 routes). (5) Keep `/api/v1` as a permanent `books` legacy prefix unless a group is rewritten.
This also removes most of the alias/410 churn in 09 §9.6 for books routes.

**Trade:** ABO keeps two routing stacks (gin under `MountLegacy` plus `httpapi`) and the
`legacyauth.FromCorePrincipal` bridge indefinitely, and it keeps the gin dependencies (not core). Risk 14 K3
(two auth stacks) becomes permanent. The 572-route permission-equivalence test (09 S4) must then be a
permanent CI test, not a migration test. R6 and G-gates are unaffected because the legacy manifest is checked
by `archtest.RoutesHavePermissions`.

### SIMP-3 — MAJOR — Removing ABO's globals is justified only by multi-app

**Where:** 05-config §1.12 stages 2–3 (accessor, delete); 05-logging §11 stage 3 (inject `*slog.Logger` everywhere); 11 §11.3 row 3.

**Evidence:** R5 governs **core** only (03 §3.5). For the config façade, stage 1 already makes core the
source of truth and fixes the races (stage 0). For logging, stage 1 (`InstallProcessDefaults`) already routes
every call into the App pipeline. Stages 2–3 then rewrite **634 non-test `config.AppConfig` lines in 215 files
plus 2,456 test occurrences** (05-config §1.2) and **1,460 `slog.*` calls in 261 files, 322 `logger.New`, 231
`logging.*`** (05-logging §2). No stated benefit applies to a single-app binary. 11 §11.3 itself says these
globals are "harmless while only `books` uses them". Goal 7 and D9 make multi-app the lowest priority.

**Fix:** Mark 05-config stages 2–3 and 05-logging stage 3 as **deferred**, and add them to the 11 §11.5
checklist ("books: remove `config.AppConfig` façade and package-level loggers", size L). Keep the
ceilings-only ratchets now (`TestAppConfigRatchet`, the re-pointed slog ratchet), because they are cheap and
stop growth. Keep the stage 0 fix of the 8 bypassing writes.

**Trade:** Until the deferred work is done, ABO cannot be one module among several in a suite binary, and
ABO tests keep mutating a façade global, so they cannot use `t.Parallel()` on config. The door stays open
(11's test: no core contract changes).

### SIMP-4 — MAJOR — Integration configuration has three overlapping stores with contradictory names

**Where:** 05-config §1.12 key map and §2.2 secrets table; 05-metadata M1.4 step 2 and M1.7; 05-download §7 and §10; 05-ai §7; 05-notify §7 and §10; 06 §6.4.4 `Secrets secrets.Scope // names under "<ID>/"`; 09 S6 and S7 Config rows.

**Evidence:** One fact, the Deluge client password, has four destinations in the spec:
`core.download.clients[<id>].password` (05-config §2.2), a `download/deluge` instance row in
`c/integrations/instances/download/<id>` with its own file shadowing (`core.integrations.download.instances.<id>.*`,
05-metadata M1.4, 05-download §7/§10), `core.integrations.download.deluge.*` (09 S7), and secret names
under `"<ID>/…"` (06). SMTP goes to `core.notify.mail.*` (05-config) or to a `notify/email` instance
(05-notify §10). AI endpoints go to `core.ai.*` (05-config) or to `core.integrations.ai.instances.<id>.*`
(05-ai §7, 09 S7). Webhooks go to `modules.books.notify.webhooks[]` with `format: legacy` (09 S6) or to
`notify/webhook-<n>` instances plus a global `Rule` (05-notify §10).

The mechanism is duplicated as well. M1 re-implements config's "an operator file value shadows the DB row,
which is then read-only, and the lock reason is surfaced" logic (M1.4 step 2 cites
`config.SettingLockedBy` as its model). It also re-implements "row and secrets in one batch" (M1.4 step 4
vs 05-config §1.4 step 7).

**Fix:** Choose **one** store. Recommended: integration instances are a keyed collection section of
`core/config` (`core.integrations.<kind>.instances.<id>`), persisted by config's `OverrideStore`, with
config's lock, origin, reload and `Apply` semantics. M1 keeps descriptors, build, lease, hot swap and health,
but it does not store anything. Secret names follow the config path
(`core.integrations.download.instances.<id>.password`). Then delete `c/integrations/instances/`, the
`core.download.clients` / `core.notify.mail.*` / `core.ai.*` instance rows in 05-config §1.12, the
`"<ID>/"` scope in 06, and the 09 S6/S7 rows that disagree.

**Trade:** `core/config` must support keyed collections, where adding and removing an entry is one `Apply`.
That is one more config feature, but it removes a parallel store, a parallel lock model, and two migrations of
the same secrets to different names.

### SIMP-5 — MAJOR — Four topic catalogs, each with its own permission and name

**Where:** 05-events §5E.3 `TopicSpec{Name, Schema}`; 06 §6.4.3 `Topics`, `OutboxTopics`, `NotificationTopic`; 05-notify §3 `TopicSpec{ID, Permission, DefaultSeverity, …}`; 05-realtime §4.7 (`realtime.Register`/`Bridge` with its own `Perm`, `Coalesce`).

**Evidence:** A module that wants "book imported" to appear on the bus, in the outbox, in notifications and
on SSE declares it up to four times. Two of those declarations carry independent permissions, so one event
can be visible on SSE under one permission and notified under another. The names already drift. The
realtime topic is `core.notify.v1` while the notify bus topic is `core.notify.inbox.added.v1` (05-notify §4
step 5). Realtime's `core.task.run.v1` and `books.book.changed.v1` (§4.7) do not follow 03 §3.6
`<moduleID>.<noun>.<verb-past>.v1`.

**Fix:** Use one declaration:
`events.TopicSpec{Name, Schema, Permission rbac.Requirement, Durable bool, Notify *NotifyMeta, Realtime *RealtimeMeta}`,
registered once through `Registrar.Topics`. Outbox, notify and realtime read this catalog. Delete
`OutboxTopics` and `NotificationTopic`, and drop the separate permission in `realtime.Register`. Fix the
realtime names to follow 03 §3.6, or amend 03 §3.6 to allow a `<noun>.<aspect>` form for streams.

**Trade:** `events` imports `rbac`. Both are R4 L4, and same-layer imports are allowed, so no rule is
broken.

### SIMP-6 — MAJOR — "One timer" is not true; schedule configuration is spread over bespoke keys in three formats

**Where:** 05-tasks §5T.3.8 ("`Schedule` is the only timer") and §5T.10
`modules.<id>.schedules.<kind-suffix>.*`; 05-config §1.12 ("each `scheduled.<task>` entry moves to its task
kind's namespace"); 09 S5 (`scheduled.*` → `modules.books.schedules.*`). Per-subsystem keys:
`core.backup.schedule` (cron), `core.backup.verify_schedule` (cron), `core.secrets.rotate.schedule`
(duration), `core.updater.check_interval`, `core.download.poll_interval`, `core.ai.batch_poll_interval`,
`core.integrations.health_interval`, `core.acquire.rss.interval`, `core.acquire.refresh.interval` (10
§10.5.7). Sub-minute loops: `core.health.check_interval` 30 s (05-health §5H.7, one goroutine per
component, §5H.4), `core.index.reconcile.interval` 5 s (05-storage §5S.7), `core.ai.usage_flush_interval`
30 s.

**Evidence:** The scheduler polls every 1 minute (`core.tasks.schedule_poll`), so 5 s and 30 s loops cannot
be tasks. The "only timer" claim is therefore false, and the spec does not state which mechanism applies
when. Core task kinds have no generic schedule override. Every subsystem invents its own key and format:
cron in one place, duration in another, and "daily, jittered" in a third.

**Fix:** (1) Use one generic override for **every** scheduled kind, core and module:
`<core|modules.<id>>.schedules.<kind>.{enabled, every | cron}`, applied by `tasks` itself. Delete the
bespoke schedule keys listed above and keep only non-schedule tuning keys. (2) State the rule in 05-tasks:
durable work of 1 minute or more uses `tasks.Schedule`, and in-process ticks shorter than the poll use
`Registrar.Worker` or a `lifecycle.Component`. Then list the latter (health probes, index reconcile, AI
usage flush, rate-limiter sweeper). (3) Make 05-config §1.12 and 09 S5 point to that single key form.

**Trade:** Operators lose subsystem-local key names such as `core.backup.schedule`. Aliases cover the
compat window.

### SIMP-7 — MAJOR — The compatibility window is defined four times, and one definition breaks 09's usage gate

**Where:** 02 §2.5 Q5 ("two minor releases"); 05-http-api §4.6 ("sunset date … checked into the alias
registration; at `Sunset` the alias turns into a `Gone` stub automatically (the router compares
`time.Now()` at boot)"); 05-config §1.7 `core.config.legacy_aliases` bool; 05-metrics M6 ("gather-time
aliases") vs 09 §9.2.7 ("both names are emitted"); 09 §9.2.1 (W = the later of 2 minors and 30 days,
**plus** `mediacore_compat_legacy_use_total` at zero for 14 days) and §9.2.9 (ledger,
`TestNoContractBeforeSunset`).

**Evidence:** A clock-triggered 410 fires whether or not the usage counter is zero, which contradicts
09 (c). `core.config.legacy_aliases` is a runtime flag, which contradicts 09 §9.2.8 ("**None are added**").
Each subsystem carries its own window logic.

**Fix:** The 09 ledger is the single source. `core/compat` exposes `compat.Surface` handles carrying
"since / earliest sunset / counter". httpapi, config, rbac, metrics and tasks aliases are registered against
these handles. Nothing is removed or turned into 410 by a clock; only a contract PR does that, gated by
`TestNoContractBeforeSunset`. Delete the window text in 05-http-api §4.6, 05-config §1.7 and 05-metrics M6,
and point to 09. Delete `core.config.legacy_aliases`. Choose one metrics mechanism (gather-time alias is the
cheaper one: no double instrumentation) and fix 09 §9.2.7 to match.

**Trade:** None functional. Removal always needs a PR (it already does in 09).

### SIMP-8 — MAJOR — API polish on every route, including a default that breaks UI paging

**Where:** 05-http-api §4.4 (cursor default with `HMAC-SHA256`, `total` only where a counter exists), §4.5
(Idempotency-Key store with `in_flight`/`done` records and a prune task), §4.3 (ETag/If-Match), §4.2
stage 13 (per-principal limiter).

**Evidence:** The UI pages by count today. `web/src/pages/Library.tsx:909-913` uses
`totalCount`/`totalPages`, 11 `.tsx` files use `Pagination`, and `api.ts` has 39 `offset` uses. The current
server contract is offset + `total` (`internal/httputil/parse.go:74-90`, `types.go:23-29`). Library lists are
sorted by title, author and date through memdb/bleve, not in Pebble key order, so "Pebble scans are ordered,
`Paginate` reads `limit+1`" does not apply to the lists users page through. A forged cursor is not a security
issue, because the server re-checks permissions per row, so the HMAC needs a key and buys nothing. The one
named idempotency consumer, `POST /api/core/v1/tasks`, is already covered by `tasks.Common.Dedupe` and
concurrency keys (05-tasks §5T.3.1, §5T.3.4).

**Fix:** (1) The default is offset/limit + `total` (current contract). Cursor is **opt-in per route** for
append-only feeds (audit 05-rbac §7.6, journal 05-logging §5). The cursor is opaque base64 of
(position, filter hash) **without HMAC**; a mismatched filter → 400. (2) Drop `Idempotency-Key` from v1;
task starts rely on `Dedupe`. (3) ETag/If-Match is opt-in per route where `Version` exists. (4) Defer the
per-principal limiter (stage 13) until abuse is observed; the per-client limiter exists today.

**Trade:** Deep offsets on very large lists are slower, and non-task POSTs get no generic replay protection.
Both match today's behaviour.

### SIMP-9 — MAJOR — Default backup encryption is circular and has lost its motivation

**Where:** 05-backup B3, §5B.3.3 (install identity "stored as a 0600 file beside the secrets master key in the
credential state directory", keyring inside encrypted archives as `secrets/master.key`); 05-config §2.4 ("a
lost keyring is recoverable from any encrypted backup"), §2.3 `KeyFile` default `secrets.keyring`;
`KeyIDsInUse`, `PruneKeys`.

**Evidence:** The install identity and the keyring live in the same state directory. If that directory is
lost, both are lost, and the encrypted archive that "recovers the keyring" cannot be decrypted unless the
operator configured an external recipient or saved the one-time recovery string. So the 05-config claim
holds only in that case. B3's motivation ("every ABO backup holds bearer credentials and plaintext secrets
today") is removed by S3 (every secret encrypted, N10) and by session hashing (05-authn §4.3). The archive
file name also disagrees: `secrets/master.key` vs `secrets.keyring`.

**Fix:** (1) Archives are unencrypted by default and **never** contain key material, so secrets inside stay
ciphertext. (2) age encryption is optional, to **operator recipients only**: no install identity, no
recovery-string UI. (3) Keyring recovery is an explicit action: the `secrets export-keyring` CLI already in
§2.4, plus a UI "download recovery kit". A health warning `secrets_keyring_not_exported` stays until the
action is done. (4) Delete the coupling between the keyring and backup manifests (`KeyIDsInUse`, `PruneKeys`
consulting manifests; see SIMP-19).

**Trade:** A stolen default archive exposes non-secret user data (library, user emails, password hashes)
unless the operator sets recipients. This is documented as a choice. Losing the state directory without an
exported keyring means re-entering secrets, the same outcome as the spec's own "unencrypted backups" branch.

### SIMP-10 — MAJOR — Notify ships new product features with no consumer

**Where:** 05-notify §1, §3.1 (`core.inbox` always on, presets ntfy/gotify/apprise/custom), §4 (two-stage
dispatch → per-target deliver, per-user subscriptions, delivery-time RBAC resolution), §4.1
(per-instance/per-topic templates with a 100 ms execution budget), §7 (`core.notify.personal_channels`
default **true**); 05-realtime §4.7 `core.notify.v1`; 07 §7.1 "notifications".

**Evidence:** HEAD has one webhook plugin, no inbox, no per-user channels (05-notify §2). TV and Movies only
need "topics offered to notification rules" (10 §10.1.2 Notify row). Personal channels on by default let any
user create outbound webhooks. That is new egress surface in a spec that otherwise defaults to deny.

**Fix:** Phase 1 (S6): `core.webhook` (`json` preset + legacy headers) and `core.email`, global rules only,
delivered via outbox with retries. These fix the real HEAD defects: no retry, publisher context, replayable
HMAC. Phase 2, when a second app or the owner asks: inbox, per-user subscriptions, personal channels,
ntfy/gotify/apprise presets, custom templates. `personal_channels` defaults to **false** in any phase. 07's
notification bell becomes an optional shell feature enabled when the inbox ships.

**Trade:** Shell v1 has no in-app notification list, and users cannot route events to their own channels.

### SIMP-11 — MAJOR — The AI usage ledger and monthly cap are new features in the extraction path

**Where:** 05-ai §1 ("usage accounting"), §3 `Router.Usage`, `EndpointSettings.MonthlyTokenCap` (line 272),
§4 item 5 (batch usage), item 6 (ledger `c/ai/usage/<yyyy-mm-dd>/…`, flush loop), §7
`usage_flush_interval`, `usage_retention` 400d, §10 `GET /api/core/v1/ai/usage`.

**Evidence:** HEAD has "OTel counters only; … no persisted ledger" (05-ai §2). ABO is the only AI consumer.
TV and Movies consume no AI (10 §10.1.2 has no AI row), so the §3.4 two-consumer condition fails. The
router itself is an existing, generic mechanism (`internal/aidispatch`) and moving it is justified.

**Fix:** Core `integrations/ai` in S7 = contract + router (moved, globals removed) + OTel counters as today.
The ledger, `Usage` query, monthly cap and the `/ai/usage` route are listed in 05-ai §11 as "later, on owner
request". If they are wanted earlier, they live in `books`, built from the router's attempt hook.

**Trade:** No persisted cost report or hard spend cap in v1 (same as HEAD).

### SIMP-12 — MAJOR — Download phase 1 builds the grab and monitor machinery nobody uses

**Where:** 05-download §1 ("ABO needs only the first level (it never grabs)"), §4.1 (phase 1 = Deluge
"Add/Status added"), §4.3 Grab (grab records, `c/download/grabs/by-key`, pending recovery by tag/hash/name),
§4.5 monitor (`core.download.poll`, snapshots, outbox per transition, `removed_after`), `MarkImported`,
`SeedGoal`; §9 tests `TestGrab_*`, `TestMonitor_*`.

**Evidence:** ABO's needs are list, list by label, details/files, move and remove (05-download §2, §10). The
phase table phases adapters but not Pool features. So S7 would build grab idempotency, pool selection,
snapshots and outbox handoff with no production caller and no live test.

**Fix:** Add a Pool feature phase table. Phase 1 = `Client` interface (Items, Files, Relocate, Remove,
Test, and `Add` declared so adapter conformance covers it), Deluge adapter, instances, path mapping, and
`Pool.Items/Files/Relocate`. Phase 2 (acquire wave) = `Pool.Grab`, grab records and recovery, monitor +
outbox handoff, `MarkImported`, seed goals, `core.download.*` poll keys.

**Trade:** The grab path is first exercised when acquire is built.

### SIMP-13 — MAJOR — Renaming books metrics buys nothing, and three files disagree on how

**Where:** 03 §3.6 Metric name (`<app>` = `app.Options.AppID`, e.g. `abo` → `abo_books_scan_items_total`);
05-metrics §4 and Objection 2 (`AppPrefix` `audiobook_organizer`), §13 item 3 ("every other
`audiobook_organizer_<x>` → `audiobook_organizer_books_<x>` … `audiobook_organizer_books_total` keeps its
exact name"), M6 (gather-time aliases); 09 §9.2.7 (dual emission;
"`audiobook_organizer_books_books_total`: predictability beats euphony").

**Evidence:** `internal/telemetry/contract/testdata/series.golden` has **73** `audiobook_organizer_*`
families. Inserting `books_` disambiguates nothing in a single-module app whose prefix is already
app-specific. It still costs aliases or dual emission, recording rules, alert duplication and dashboard
edits. The core-subsystem renames (ops→`mediacore_tasks_*`, pebble→`mediacore_storage_*`, ai) do have a
consumer: one dashboard set across apps (05-metrics §4).

**Fix:** Rename only core-subsystem series. Module series follow `<app>_<moduleID>_*` only when an app hosts
two or more modules; otherwise they follow `<app>_*`, so ABO's 73 families keep their names. Amend 03 §3.6
to name the option (`MetricPrefix`) and ABO's value (`audiobook_organizer`), not `abo`. Choose one compat
mechanism (see SIMP-7).

**Trade:** If ABO later shares a binary with other modules, its module series must be renamed then (an
11 §11.5 checklist row).

### SIMP-14 — MAJOR — Two contradictory decisions for one JSON Schema reflector

**Where:** 05-http-api §1.1 ("Accepted: `github.com/google/jsonschema-go` v0.4.3 (`jsonschema.For[T]` …)") and
§7 (`TypeSchemas` overrides, `Resolve`+`Validate`); 05-config §1.10 ("generated by `core/internal/jsonschema`,
the **same reflector** `internal/openapi` uses … No third-party reflector … one in-house reflector (~400 LOC)").

**Evidence:** Each file claims to share the other's reflector, but they name different ones. 05-config's
reason ("vendor keywords and shared type rules would need extension hooks anyway") is what jsonschema-go's
`TypeSchemas` overrides provide, per 05-http-api §7.

**Fix:** Use one reflector: `google/jsonschema-go` with a shared core override table (`config.Duration`,
`secrets.Ref`, `rbac.Permission`, `time.Time`, `x-mediacore-*` keywords), used by both config schema and
OpenAPI. Delete the ~400 LOC in-house plan from 05-config §1.10. Record the dependency in 13 §13.5.2.

**Trade:** Core takes a pre-1.0 dependency (v0.4.3), pinned exactly. If jsonschema-go's hooks turn out
insufficient, the in-house reflector is the fallback, decided by a wave-0 spike rather than by two
specs.

### SIMP-15 — MAJOR — The config and secrets migration is specified twice, with contradictory semantics

**Where:** 05-config §1.12 (four strangler stages; "Data migration (one backed-up, idempotent boot migration
…): `setting:config_blob` → per-key override rows, importing only values that differ from the current
shipped default … The original `config_blob` row is kept untouched for one release"); §2.10 step 1 (blank
plaintext secrets in the retained blob); 09 S3 ("Config stays persisted in the flat `config_blob`; canonical
docs are written only at contract C3"; plaintext kept through W, Mirror phase) and §9.5 C3.

**Evidence:** One plan converts at boot in S3. The other keeps the blob as truth until C3, after W. They
differ in when per-key rows exist, whether plaintext is blanked at expand (05-config) or kept for rollback
(09 S3, with the owner question 14 Q-E), and whether the blob is kept "one release" or until W. 09 §9.0 says
09 is "the single owner of ABO's compatibility windows".

**Fix:** 05-config describes only the target design and the key map. Delete the "Data migration" paragraph
of §1.12 and §2.10 step 1, and point to 09 S3/C3. Resolve Q-E once, in 09.

**Trade:** None.

### SIMP-16 — MINOR — Five spellings of one reference type

**Where:** 03 §3.3 `entity.Ref{Module, Kind, ID}`; 05-download §3 `events.EntityRef` (lines 103, 277, 296);
05-notify §3 line 71; 08 §8.6.1 (lines 277–308); 05-metadata line 451; 05-storage line 276
`core.EntityRef`; 05-rbac §3.5 `Target` ("polymorphic EntityRef"); 05-tasks §5T.3.4
`Resource{Module, Kind, ID}` and open item 6.

**Fix:** Use `entity.Ref` everywhere. Write `type Resource = entity.Ref` in tasks, or delete `Resource`, and
have `rbac.Target` embed `entity.Ref`. Replace every `events.EntityRef` / `core.EntityRef` mention.

**Trade:** None.

### SIMP-17 — MINOR — Parallelism is declared three ways, two of which do not exist or break R4

**Where:** 05-tasks §5T.3.2 (`Fixed(n)` "exactly n", `IO(n)` "1 <= n <= MaxIO"; "`IO`, `Fixed` and
`Sequential` take no CPU slot"); 10 lines 152, 954, 955, 956, 1010 (`tasks.Network(n, reason)`, which does not
exist); 05-storage §5S.3.6 `Shards` (`ShardsCPU`, `ShardsFixed`, `Sequential`, "mirror of tasks.Parallelism")
with the runner in "layer 3, which imports both storage and tasks".

**Evidence:** `Fixed` and `IO` differ only by a clamp. R4 puts `tasks` at L4, so a layer-3 runner importing
it violates R4, and that violation is why a second type was invented.

**Fix:** Merge them into `Fixed(n int, reason string)` with a mandatory reason (as `Sequential` already has)
and clamp at `MaxIO`. Fix 10 to use `tasks.Fixed`. Make `DataMigration.Shards` a `tasks.Parallelism`, and
place `storagemigrate` at L4 or above (inside `tasks/internal` or as `core/internal/storagemigrate` at L4).

**Trade:** None.

### SIMP-18 — MINOR — The compat-level guard copies the storage-format guard

**Where:** 09 §9.2.2 (`c/compat/required_level`, `.compat-level` sidecar, `--print-compat-level`,
`storage_format_guard.py --prev-compat`); 05-storage §5S.3.7; code
`internal/database/storage_format.go:30-50,114-182`, `pebble_store.go:670-680`.

**Evidence:** Both guards are "stamp key + fsynced sidecar + fast-exit print flag + rollback script check",
and the second adds a parallel copy of each piece. Only the comparison differs (exact vs minimum reader). The
on-disk names cannot be merged, because old builds parse the existing stamp and sidecar as one integer
(`storage_format.go:181-182`).

**Fix:** Keep both on-disk names but implement them once. Use one internal
`guard{key, sidecarSuffix, printFlag, compare: exact|minReader}` used twice. Use one fast-exit flag
`--print-guards` that prints both values, with `--print-storage-format` kept for old scripts. Make
`storage_format_guard.py` table-driven over guards. 09 §9.2.2 then specifies only the new level semantics.

**Trade:** None functional. It is less code than two guards.

### SIMP-19 — MINOR — Keyring rotation ships in v1, but rotation does not exist today

**Where:** 05-config §2.3 (`Rotate`, `PruneKeys`, `KeyIDsInUse`, `DeriveKey`), §2.4 items 4–5 (rotate task,
straggler re-encryption goroutine, prune consulting backup manifests), §2.7 `core.secrets.rotate.schedule`,
§2.8 `active_key_age_seconds`.

**Evidence:** 05-config §2.2: "Rotation | none". Secret counts are "tens to low hundreds" (§2.6). The prune
feature couples secrets to backup manifests (05-backup).

**Fix:** v1 = the keyring file format with key IDs (`kid` in every record) and one active key, which keeps
the format future-proof at no cost. Move `Rotate`, `PruneKeys`, `KeyIDsInUse`, the rotate schedule and the
straggler goroutine to "later". Keep `k0-legacy` import, which is a one-time migration.

**Trade:** After a suspected key leak the operator re-enters secrets instead of running a rotation task.

### SIMP-20 — MINOR — The audit hash chain protects nothing in the spec's own threat model

**Where:** 05-rbac §7.4 ("does not detect truncation of the tail by someone with storage access — out of
scope for a single-host app"), §7.5 anchor on prune, §7.6 `POST /api/core/v1/audit/verify`.

**Evidence:** An attacker who can edit Pebble rows can recompute every `Hash`, because there is no secret
key and no external anchor. Accidental corruption is already caught by Pebble block checksums. The chain
also adds canonical-JSON hashing on every synchronous audit write, the `c/audit/anchor` key, and an
endpoint.

**Fix:** Delete §7.4, the anchor, and `/audit/verify`. Keep append-only + `seq`.

**Trade:** Naive in-place edits by someone with storage access are no longer detected.

### SIMP-21 — MINOR — The Pipeline shape has no consumer

**Where:** 05-tasks §5T.3.1 (`ShapePipeline`, `PipelineSpec`, `Stage`), 5T.0 ("the four shapes … kept
verbatim").

**Evidence:** The only `ops.Pipeline(` call at HEAD is lint testdata
(`tools/cmd/oplint/testdata/src/.../unbundled_def_nobundle/nobundle.go:11`). Every other `ops.Task/Batch/Fixer`
call outside `pkg/ops` is also oplint testdata, which matches 05-tasks §5T.2 ("v3 has 0 native defs").
05-tasks A6 already drops `Isolate`/`Synchronous` on the principle "a mode nothing can use is dead weight".

**Fix:** Drop `Pipeline` from core v1 under the same A6 reasoning. Keep Task/Batch/Fixer: the 233 defs and
24 fixers map onto them.

**Trade:** A future DAG def needs an additive API change (allowed within a major).

### SIMP-22 — MINOR — Multi-app residue in the SDK without a consumer

**Where:** 06 §6.4.2 `Manifest.DependsOn`, `MinCoreVersion`; 05-http-api §4.9 `PrefixClaim{Prefix, Owner, Kind}` + `YieldsTo`.

**Evidence:** R3 forbids modules from importing each other, and no planned module depends on another (10).
`MinCoreVersion` repeats Go's MVS: an app whose `go.mod` requires core vX cannot build against an earlier
version, and in-repo the two are the same commit. The `PrefixClaim` generality exists to resolve the
`/api/v3` collision, which 05-http-api §4.9 item 1 says cannot happen in the priority deployment.

**Fix:** Drop `DependsOn` and `MinCoreVersion`, and add them to the 11 §11.5 checklist. Reduce `PrefixClaim`
to "no two owners may register overlapping subtree prefixes", checked in `Build`, which also covers
facade vs legacy.

**Trade:** A suite binary needs them added later (additive).

### SIMP-23 — MINOR — `auth/clientcert` has no consumer

**Where:** 05-authn §3.3 line 64, lines 302–307; §4.8 ("a new optional authenticator for operators who want mTLS").

**Fix:** Delete the authenticator from the spec. The `Authenticator` extension point (§3.3) already leaves
room for it.

**Trade:** None for any current deployment.

### SIMP-24 — MINOR — Proof-depth sections are written at implementation depth

**Where:** 10 §10.5.3 (`acquire` Go API, lines 367–935, about 570 lines); 08 §8.6.2 (wire DTOs, lines 311–526).

**Evidence:** D5 limits TV/Movies to proof depth. These APIs have zero consumers until a new repo exists,
and they will be stale by then. Meanwhile they dominate review cost, as this round shows.

**Fix:** Reduce 10 §10.5.3 to the type and interface sketch (`MediaType[T]`, `Target`, the spec, decision
and tracker interfaces) plus invariants, about 120 lines. Keep the gap list §10.4 in full. Replace 08 §8.6.2's
Go DTOs with a field table that references raw/16 §5.2, since the fixtures in §8.14 are the real contract.

**Trade:** Less precision handed to the future implementer, mitigated by raw/16.

### SIMP-25 — MINOR — The react-router 8 migration should precede the shell, not ride inside it

**Where:** 07 §7.19 step 3; 13 E4 ("ABO and the shell must move together"; 121 files); 07 §7.3.3 says 52 files.

**Evidence:** `git grep -l "from 'react-router-dom'" web/src` gives 121 files (53 non-test). ABO can move
to `react-router@8` (D8) on its own before S9. Nothing forces the swap to happen in the same step as
`createShell`.

**Fix:** Make it a separate pre-S9 PR series in ABO. Fix 07's count to "121 (53 non-test)".

**Trade:** None.

### SIMP-26 — MINOR — 03's diagram contradicts "no DI container"

**Where:** 03 §3.7 diagram "build services (container, explicit, no init())" vs 03 §3.3 `kernel/...` row and 06 A2.

**Fix:** Change the wording to "build the typed services struct (explicit constructors, no init())".

**Trade:** None.

### SIMP-27 — MINOR — The OpenAPI pipeline carries an untyped document and an open-ended fallback

**Where:** 05-http-api §7 (`api/openapi/books-legacy.json` "untyped bodies"); 07 §7.8 and O4 (`openapi-typescript`
peer `typescript ^5.x`; "fallback is a Go emitter over the same OpenAPI document").

**Evidence:** The legacy document cannot generate types. Its information (method, path, permission) is
already in the reviewed legacy manifest checked by `archtest.RoutesHavePermissions`. A Go TypeScript emitter
is an unbounded in-house generator.

**Fix:** Drop `books-legacy.json`. Add a wave-0 spike: run `openapi-typescript` 7.13.0 under the npm
`overrides` on the TS 6 alias against a generated core document. Only if the spike fails, specify the
Go emitter with a scope cap (paths + components only, no runtime).

**Trade:** Legacy routes are not discoverable through OpenAPI. They are visible in the manifest instead.
