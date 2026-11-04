<!-- file: docs/plans/platform-core/reviews/spec-round-2-simplicity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2eba4871-94bc-4a7e-a193-fa9ca1f27830 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 2: simplicity and scope (SIMP2)

Lens: over-engineering, YAGNI, duplicated mechanisms, scope creep. Round 2 has two jobs. First, check
that each "one X" resolution (cross-cutting 4) holds in **every** file, not only in the file that owns
it. Second, check that the deferred-build items of cross-cutting 1 (`acquire`, `arrfacade`,
`integrations/indexer`, the download grab/monitor pipeline, the notify extras and the AI ledger) are not
re-required by another file or by a 09 stage. New complexity introduced by the revision is a finding
**only** when a simpler design meets every stated goal and constraint. Each such finding names the
trade.

Code anchors are at `748fd787a`. Spec line numbers are from the revised files at this review
(03 v1.4.0, 09 v1.1.0).

**Counts:** 0 BLOCKER, 7 MAJOR, 10 MINOR.

## Regression check: round-1 MAJORs

| Round-1 | Status | Where it fails, if it does |
|---|---|---|
| SIMP-1 promotion rule | **Fixed at package level, leaks at feature level** | Facade-only features ship inside built packages (SIMP2-6). The conformance engine moves for a deferred consumer (SIMP2-9). A multi-instance download migration is required for a deferred need (SIMP2-7). |
| SIMP-2 gin | Fixed | One residue: 05-download plans to rewrite books Deluge routes to native without its own reason (SIMP2-17). |
| SIMP-3 globals | Fixed | — |
| SIMP-4 one instance store | **Half-fixed** | One store, but a second lock model that persists env values (SIMP2-5). 09 C7 contradicts 05-metadata M1.4 (SIMP2-5). |
| SIMP-5 one topic catalog | Fixed | 05-events lists deferred download topics as core topics, 3 of 6 (SIMP2-10). |
| SIMP-6 one schedule mechanism | **Not fixed in 8 files** | 05-tasks' key rule refuses the keys that 05-backup, 05-ai, 05-metadata, 05-download, 05-logging, 05-rbac, 05-library and 10 register (SIMP2-1). |
| SIMP-7 one compat window | Fixed in substance | Each subsystem still has its own alias class, and dates are duplicated in Go (SIMP2-12). |
| SIMP-8 API polish | **Half-fixed** | Two idempotency stores serve one route (SIMP2-8). |
| SIMP-9 backup encryption | **Fixed, but reintroduces a second key** | The install backup identity sits beside the one keyring, so recovery needs two exports, and 09 confuses them (SIMP2-2). |
| SIMP-10 notify extras | Fixed | The two-stage dispatch built for per-user delivery stays, although per-user delivery is deferred (SIMP2-11). |
| SIMP-11 AI ledger | Fixed | — (05-ai §1, §11 item 4; 06 `Deps.AI` carries the router only) |
| SIMP-12 download phase 1 | Fixed | Residue only (SIMP2-17); SIMP2-7 is new. |
| SIMP-13 metric rename | Fixed | The new single/multi-module rule keys on *enabled* modules (SIMP2-13). |
| SIMP-14 one reflector | **Decision fixed, wiring broken** | The config-owned override table names `rbac.Permission`, which is a layer above `config`. There are also two access paths (SIMP2-3). |
| SIMP-15 one config/secrets migration | Fixed | 05-config §1.12.2 is the only specification; 09 S3 and 14 Q-E point to it. |

Checked and holding: one entity reference (`entity.Ref`; the only other `*Ref` is `download.ItemRef`,
which is a different concept); one guard (05-storage §5S.3.7 owns it, 09 §9.2.2 the policy, 05-backup and
05-updater use `CheckReader`); one keyring for `secrets` (05-config C8). The deferred handles in 06
(`Deps.Acquire`, `Deps.Indexers`, `Registrar.ArrFacade`, `DownloadWatch`) are comments only, and S7's
done-check asserts that the deferred directories do not exist.

## Summary table

| ID | Sev | Where | One line |
|---|---|---|---|
| SIMP2-1 | MAJOR | 05-tasks §5T.3.8 vs 05-backup, 05-ai, 05-metadata, 05-download, 05-logging, 05-rbac, 05-library, 05-http-api, 10 | SIMP-6 not fixed: 8 files bind schedule keys that `ScheduleFromConfig` refuses at registration, so S7/S8 builds fail boot as written. |
| SIMP2-2 | MAJOR | 05-backup B3/§5B.3.3, 05-config §2.4, 09 §9.11 S3, O3 | Two key artifacts (keyring + install backup identity), two exports, two health tokens. 09 confuses them. A derived identity removes one. |
| SIMP2-3 | MAJOR | 05-config §1.10.2, C10; 05-http-api §1.1, §4 `Deps`; 13 §13.5.2 | The "one reflector" override table is config-owned (L3) but names `rbac.Permission` (L4). Two access paths. 13 is stale. |
| SIMP2-4 | MAJOR | 09 S6 vs S7, §9.3, §9.4.1; 05-notify §1, §4, §10 | S6 writes webhook instances into the instance store and sends through providerhttp, both of which S7 introduces. |
| SIMP2-5 | MAJOR | 05-metadata M1.4 step 2; 05-config C11; 09 S7, C7 | `Origin=pinned` persists env values into stored rows: a second lock model, a regression of a deliberate HEAD behaviour, and contradicted by 09 C7. |
| SIMP2-6 | MAJOR | 05-authn §3.2, §4.4; 05-rbac §4.10 tests; 05-http-api §4 `FacadeGroup`, stage 13; 08 §8.5 | Facade-only key machinery (`PurposeFacade`, `Module`, query-key extraction, facade groups) is built in S2/S4 for the deferred `arrfacade`. |
| SIMP2-7 | MAJOR | 05-download §10 row `TorrentHash`; 09 (absent) | A library-scale books migration (`TorrentHash`/`DelugeHash` → `ItemRef`) serves a deferred multi-instance need and has no stage, rollback or done-check in 09. |
| SIMP2-8 | MINOR | 05-http-api §4.5; 05-tasks §5T.3.10 `StartRequest.IdempotencyKey`, §5T.6 `c/tasks/idem/` | Two idempotency stores (24 h and 7 d) guard the one route that uses either. |
| SIMP2-9 | MINOR | 08 §8.0, §8.14 vs 04 row `internal/syncapi/conformance`, 03 §3.3 | 08 moves the conformance engine into core during the extraction, justified by a deferred consumer. 04 keeps it in ABO. |
| SIMP2-10 | MINOR | 05-events §5E.3.1 core-topic table vs 05-download §4.5 | Phase-2 download topics are listed as core topics, with no deferred marker, and only 3 of the 6. |
| SIMP2-11 | MINOR | 05-notify §4 steps 2–4, §9; 07 §7.1 | The two-stage dispatch outlived its reason (per-user delivery is deferred). 07's "notifications" is undefined. |
| SIMP2-12 | MINOR | 05-config §1.3.1 `AliasClass`; 05-http-api §4 `Deprecation`; 09 §9.2.9 `compat.Entry`; 05-tasks §5T.10 | Every subsystem keeps its own alias classes and dates beside `compat.Entry`. One duration key is "the compatibility window". |
| SIMP2-13 | MINOR | 05-metrics §3 `Options.Modules`; 03 §3.6 Metric row | The metric segment depends on the *enabled* module list, so toggling a module renames another module's series. |
| SIMP2-14 | MINOR | 05-tasks §5T.8.2 row T0+T1 vs §5T.6, 09 §9.2.2 | "raises the level" contradicts the one-guard policy that only contracts raise levels. |
| SIMP2-15 | MINOR | 09 §9.2.8, S5, §9.11 vs 05-tasks §5T.3.2, §5T.10 | The operational dial has two names: `…parallelism` and `…parallelism_cap`. |
| SIMP2-16 | MINOR | 09 §9.2.3 rule 2; 13 §13.5.2; 05-config §2.3; review-history lines | Stale cross-references left by the revision. |
| SIMP2-17 | MINOR | 05-download §4.1, §10; 06 §6.4.4 | Download residue: `Remove` is listed as an ABO need (it has 0 callers), and books Deluge routes are rewritten to native without a reason of their own. |

---

## Findings

### SIMP2-1 — MAJOR — SIMP-6 not fixed: eight files bind schedule keys that 05-tasks refuses

**Where:** 05-tasks §5T.3.8 (lines 609–614, 655–662, 664–670), §5T.10 (line 1119). The violating files
are listed under Evidence.

**Evidence:** 05-tasks makes one key form normative: "key must be `core.schedules.<name>` or
`modules.<id>.schedules.<name>` (own module only); anything else, or an undeclared key, **fails
registration**". The value type is `tasks.ScheduleConfig`. 05-tasks lists the bespoke keys that "become
schedule keys" by name: `core.backup.schedule`/`verify_schedule`, `core.download.poll_interval`,
`core.ai.batch_poll_interval`, `core.integrations.health_interval`, `core.acquire.*.interval`. The owning
files still register exactly those keys, most of them typed `duration`:

| File | Registration | Key type stated |
|---|---|---|
| 05-backup lines 515–517, 632, 640 | `ScheduleFromConfig("core.backup.schedule")`, `("core.backup.verify_schedule")` | "schedule", default "daily, jittered" |
| 05-ai lines 466–467, 515 | `ScheduleFromConfig("core.ai.batch_poll_interval")` | duration `5m` |
| 05-metadata lines 441–442, 478 | `ScheduleFromConfig("core.integrations.health_interval")` | duration `5m` |
| 05-download lines 455–456, 526 (phase 2) | `ScheduleFromConfig("core.download.poll_interval")` | duration `60s` |
| 05-logging lines 131, 306, 354 | `core.log.journal.prune_every` | duration string `24h` |
| 05-rbac lines 632, 882 | `core.audit.prune_interval` | duration `24h` |
| 05-library lines 415, 565 | `core.library.quarantine.prune_schedule` | "schedule", daily |
| 10 lines 676, 760 | `ScheduleFromConfig("core.acquire.rss.interval")` | duration `15m` |

Only 05-updater (lines 216, 353) and 05-health (lines 272, 380) use the normative form. Three more
items break the boundary rule of 5T.3.8:

- 05-http-api §4.5 (lines 568–570) schedules `core.http.idempotency.prune` as "hourly, `tasks.Schedule`"
  with no key. `tasks.Every` is reserved for "the outbox relay's ensure-running tick".
- 10 line 678 uses `tasks.Every(24 * time.Hour)` for `core.acquire.housekeeping`.
- 05-library lines 478, 561 runs a 15-minute watcher poll as an in-process component. 5T.3.8 says that
  in-process ticks are only those **shorter** than the 1-minute poll, and that "a new one is added to
  this list or it is a review finding". The list does not include it.

As written, the S7 build (05-metadata health task, 05-ai batch poll) and the S8 build (backup create and
verify) fail registration at boot.

**Fix:** Apply 05-tasks' form in each file, typed `tasks.ScheduleConfig` with the current default:
`core.schedules.backup.create`, `core.schedules.backup.verify`, `core.schedules.ai.batch-poll`,
`core.schedules.integrations.health`, `core.schedules.download.poll` (phase 2),
`core.schedules.log.journal-prune`, `core.schedules.audit.prune`, `core.schedules.library.quarantine-prune`,
`core.schedules.http.idempotency-prune` (or delete it with SIMP2-8), `core.schedules.acquire.rss-sync`
and `core.schedules.acquire.housekeeping` (10). Delete the old key rows from each config table. Where an
ABO alias exists (`backup_*`, `scheduled.*`), the 05-config alias row targets the new key. Add the watcher
poll to 5T.3.8's in-process list with its reason ("fallback poll when the platform watcher is
unavailable; per-root, not durable work"), or make it a schedule. Add a doc-lint to the spec CI: every
`ScheduleFromConfig("…")` literal in `docs/specs/platform-core/*.md` must match 5T.3.8's grammar.

**Trade:** None. The owning files keep their defaults; only the key path and type change.

### SIMP2-2 — MAJOR — Backup encryption adds a second key artifact beside the one keyring

**Where:** 05-backup B3 (line 25), B4 (line 26), §5B.3.3 (lines 400–427), floor 2 (line 534), tests (lines 696,
708); 05-config §2.4 (lines 1106–1109); 09 §9.11 S3 row (line 730), §9.12 O3 (line 754).

**Evidence:** The revision correctly keeps key material out of archives. It also introduces an
**install backup identity**: an X25519 identity in its own 0600 file, with its own export CLI
(`backup identity export`), its own exported marker (`backup.identity.exported`) and its own health reason
(`identity_not_exported`). It exists beside the secrets keyring, which has its own export
(`secrets export-keyring`), marker and health token (`secrets_keyring_not_exported`). Host-loss recovery
therefore needs **both** files (§5B.3.3 "the keyring export … and, for an encrypted archive, the identity
export"). The provenance MAC already derives from the keyring (B5: "keyring-derived key";
`DeriveKey("core.backup/manifest/v1")`, line 564), so the backup subsystem depends on the keyring
anyway. 09 already mixes the two artifacts up: its S3 risk "Encryption key file missing on restore to a
new host" is mitigated by "05-backup identity export", and O3 says the `.encryption_key` restore "needs the
identity export". Under 05-backup, the keyring export recovers that key, not the identity export.

**Fix:** Derive the install age identity from the keyring's derivation key: X25519 scalar =
`DeriveKey("core.backup/age-identity/v1")`. That key is stable across data-key rotation (05-config line 1042,
SEC-28). Delete the identity file, `backup identity export`, `backup.identity.exported` and the
`identity_not_exported` reason. Encryption becomes active when `core.backup.encryption = age`, **the keyring
export is confirmed** (the existing `secrets_keyring_not_exported` record covers every key ID), and the
legacy restore bound is passed. Retention floor 2 waits for "an encrypted archive is verified and the keyring
export is confirmed". Host-loss restore: put the exported keyring in place, then
`<app> restore --archive <file>`. `--identity` stays only for operator-recipient identities. Fix 09 S3 risk
and O3 to say "05-config keyring export". Cross-cutting 8's "identity export confirmed" is met, because
the keyring export *is* the identity export.

**Trade:** Whoever holds the keyring can decrypt the archives. That holder can already decrypt every
secret inside them. What it adds is the non-secret data (library, user rows, password hashes). An operator
who wants backup decryption separated from secret access configures an operator recipient, which is
already supported.

### SIMP2-3 — MAJOR — The one reflector's override table cannot live in `config`, and it has two access paths

**Where:** 05-config C10 (line 33), §1.3 imports (lines 75–79), §1.10.2 (lines 663–667), §1.11
`TestConfigImportsOnlyAllowed` (line 699); 05-http-api §1.1 (lines 79–87), §4 `Deps.Config` (line 424);
03 R4 (line 266); 13 §13.5.2 (line 223).

**Evidence:** 05-config §1.10.2 defines the **core override table** as "`config.Duration` …,
`secrets.Ref` …, `time.Time` …, `rbac.Permission` → string pattern, owned by config". `TypeSchemas` is
keyed by `reflect.Type`, so config would have to import `rbac`. But `config` is L3 and `rbac` is L4
(03 R4). 05-config §1.3 lists config's imports (stdlib, yaml, jsonschema-go, `secrets`, `metrics`), and
`TestConfigImportsOnlyAllowed` enforces that list. The table as specified does not compile under the
guards that 05-config itself ships. The table also has two access paths. 05-config says it is "exported to
`httpapi` through `core/internal/jsonschemax`". 05-http-api says "`httpapi` (L6) imports `config` (L3) for
it", and puts `Config *config.Store // the shared JSON Schema reflector` in `httpapi.Deps`. Under the
second path, the HTTP router depends on a live config store instance just to reflect types, so a
`coretest` router or the `examples/hello` route test needs a config store. 13 still lists the dependency
as "**only if** 05-config §1.10 adopts it over the in-house fallback", although 05-config adopted it.

**Fix:**
1. `core/internal/jsonschemax` (an L0 leaf: it imports only jsonschema-go) owns the reflector wrapper,
   the tag pass and an **extensible** override table: `type Overrides map[reflect.Type]*jsonschema.Schema`
   and `func For[T any](o Overrides) (*jsonschema.Schema, error)`.
2. Each package contributes the overrides for its own types at its own layer. `config` contributes
   `config.Duration` and `secrets.Ref` (L2/L3). `httpapi` adds `rbac.Permission`, which it already imports.
3. `httpapi.Deps` drops `Config *config.Store`.
4. In 05-config C10 and §1.10.2, replace "config-owned table" with "shared reflector in
   `internal/jsonschemax`; config owns the config types' entries".
5. 13: remove "only if", and record the dependency as adopted.

**Trade:** None. There is still one reflector, and the override entries now sit with the types they
describe.

### SIMP2-4 — MAJOR — S6 needs the instance store and providerhttp, which S7 introduces

**Where:** 09 S6 Data/keys (line 488), §9.3 table (S7 row: "instance store `c/integrations/`", line 340),
§9.4.1 (lines 567–568), S7 Moves (`providerhttp`, `safehttp`, `internal/mail`); 05-notify §1 "built in 09
S6/S7" (line 24), §4 step 4 (`Gate.Acquire (instance providerhttp limits)`), §3.1 (`EgressConfigured`,
`Local`, 05-metadata M1.3), §10 SMTP row (line 237).

**Evidence:** S6 states: "Each configured webhook URL becomes one `notify/webhook-<n>` instance **in the
instance store** …; its secret moves into `secrets`". The webhook adapter is an `integrations.Descriptor`
built with `integrations.Env` and delivered through `Gate.Acquire` (providerhttp) and the SEC-15 egress guard.
All of these are S7 material. S7 introduces the instance store (§9.3) and moves
`providerhttp`/`safehttp` (S7 Moves), and §9.4.1 assigns "05-metadata M1 instance store" to S7.
§9.3 allows S6 and S7 to overlap "only if they touch disjoint package groups", but S6's webhook work needs
S7's packages. 05-notify §10 also says "auth invites read the same instance's sender through `notify`
(05-authn)". 05-authn never mentions notify or SMTP, and S4 (auth) precedes S7 (the `notify/email` import).

**Fix:** Move the webhook→notify step and the `notify/webhook-<n>` import from S6 to S7, next to
`internal/mail` → `notify/mail`. Notify v1 is then built in one stage (05-notify §1: "built in 09 S7"). S6 keeps
events, outbox and realtime. Its rollback row loses the webhook sentence, and its done-check loses the
webhook golden (which moves to S7). In 05-notify §10, delete "auth invites read … through `notify`
(05-authn)", or add it to 05-authn as an S7-or-later change, with invites keeping `mail.Sender` from the
legacy SMTP settings until then.

**Trade:** The HEAD webhook defects (no retry, publisher ctx, replayable HMAC) are fixed one stage
later.

### SIMP2-5 — MAJOR — `Origin=pinned` persists env values: a second lock model, a HEAD regression, and a 09 contradiction

**Where:** 05-metadata M1.4 step 2 (lines 424–433), Origin docs (lines 154–160); 05-config C11 (line 34); 09 §9.2.3
row "Integration instances" (line 177), S7 Data/keys (line 501), C7 (line 589); 05-metadata review history (lines
1000–1002).

**Evidence:** On every boot, M1.4 writes operator-layer values onto stored instance rows
(`Origin=pinned`, read-only). When the operator value is gone, "the row reverts to `Origin=ui` with its
last values". That is a second lock model beside config's in-memory operator-layer lock (05-config
§1.3.5), whatever "the import writes through `Put`" claims. It also reverses a deliberate HEAD
behaviour. HEAD does **not** freeze an env-supplied Deluge password into storage, so that removing the
env entry falls back to the operator's own stored password: "writing "" instead would destroy the
operator's own stored password … So the previously stored blob value is carried forward unchanged"
(`internal/config/persistence.go:1959-1971` @748fd787a). Under M1.4 the env value overwrites the stored row
and survives the env var's removal, and an env-only `OPENAI_API_KEY` is written into `c/secrets/`. The files
also disagree on lifetime. 05-config C11 and M1.4 make the env names permanent ("a pinned row exists exactly
while an operator value for it is set"). 09 C7 says "`Origin=pinned` import ends per 05-metadata M1.4",
and 05-metadata's own history says pinned rows are "for the compat window".

**Fix:** Pinned values become an **in-memory overlay** applied at `Load` and at hot swap. They are
never persisted. For each instance, the effective settings are the stored row (if any) with the
`Passthrough` operator values on top, and the API shows overlaid fields read-only with
`OperatorValue.By`. An operator value for an instance with no row yields an effective instance with a
stable ID (from the M1.10 table) and no stored row. Removing the env var reverts to the stored row
(HEAD semantics), or removes the instance if there is no stored row. Delete `"pinned"` from `Origin`
(a field-level `Locked` flag replaces it). 09 C7: delete "`Origin=pinned` import ends". The env names are
permanent (C11). Fix the 05-metadata history line.

**Trade:** An env-only instance has no stored record. A notify `Rule` or a books setting refers to it
by its fixed `Ref`, which is unchanged. Local trust (`Local`) for an env-only loopback instance is implied
by the operator origin, which holds the same authority as `lan_allow`.

### SIMP2-6 — MAJOR — Facade-only key machinery is built in S2/S4 for the deferred `arrfacade`

**Where:** 05-authn §3.2 `KeySpec.Purpose`/`Module`, `PurposeFacade` (lines 285–295), §4.4 step 2 query-key
extraction "only on facade groups" (line 475), Purpose rule (lines 539–542), §4.8 (lines 666–669), test
`TestAPIKey_FacadePurposeBoundToModule` (line 878); 05-rbac test `TestConfining_FacadeKeyRefusedOnNative`
(line 920); 05-http-api `Router.FacadeGroup` (line 432), `RouteKind … Facade` (line 454), stage 13 facade
refusal (line 503); 08 §8.5 (lines 100–106).

**Evidence:** Cross-cutting 1 defers `arrfacade`, and 03 §3.4 says the plan builds none of it. But
`auth` and `httpapi`, which are built in S4 and S2, carry facade-only features with no ABO consumer:
a key purpose enum and module binding, `X-Api-Key`/`apikey` query extraction (HEAD reads neither:
`APIKeyFromRequest`, `internal/server/middleware/auth.go:83-98`, reads only Bearer and the session cookie), a facade route group and kind,
refusal rules in stage 13, and two required tests. 08 relies on these ("Tests for the refusal live with the
chain"). The 03 §3.3 Build column works per package, and the S7 deferred-build done-check
(`test ! -e core/arrfacade`) cannot see a feature inside a built package. `Confining` itself is needed
(the fingerprint worker, SEC-3) and stays.

**Fix:** Mark these as **deferred-build members** of `auth`, `rbac` and `httpapi`, in the same way as 06
§6.4.9: `KeySpec.Purpose`/`Module`, `PurposeFacade`, query-key extraction, `Router.FacadeGroup`,
`RouteKind Facade`, the stage-13 facade clause and the two facade tests. In each file, keep the contract
text under a "Deferred-build (lands with `arrfacade`, 08)" heading. Extend the S7 done-check with
`! grep -rn 'PurposeFacade\|FacadeGroup' core --include=*.go`. Add to 03 §3.4: "deferral applies to
features inside built packages as well as to packages; each 05 file lists its deferred members".

**Trade:** When the facade lands, `KeySpec` gains an optional field, and the stored key record gains a
field that defaults to general. Both are additive and allowed by the API-stability gate. SEC-5's guarantee
holds trivially until then, because no facade key can be minted.

### SIMP2-7 — MAJOR — The `TorrentHash` → `ItemRef` books migration serves a deferred need and has no stage

**Where:** 05-download §2 (line 64), §3 `ItemRef` (lines 87–92), §10 row (line 571); 09 S7 (no such step);
§9.4.1 (no row).

**Evidence:** 05-download §10: "`BookVersion.TorrentHash`, `BookFile.DelugeHash` → books stores
`download.ItemRef`; legacy hashes resolved to the migrated Deluge instance … **by a books migration**". The
fields are library-scale: one per book file. 28 non-test Go files reference them (`git grep -l
'TorrentHash\|DelugeHash'` @748fd787a; `internal/database/bookfilecore.go:95`, `store.go:808`). The stated
reason, "fixes BookVersion.TorrentHash ambiguity", only matters with **more than one** download instance.
Multiple instances and non-Deluge adapters are phase 2 / deferred (05-download §4.1; 03 §3.4). 09 has no
stage, compat row, rollback entry or done-check for the migration. A rewrite of these rows is the "large
step with no stage" class that OPS-12 fixed elsewhere, and the previous build reads `TorrentHash`, so the
migration needs a mirror or a read-in-place design that nobody specified.

**Fix:** In phase 1, books keeps its hash fields unchanged and builds
`download.ItemRef{Instance: <the migrated Deluge ref>, ID: ItemID(hash)}` at the call site
(`Pool.Files`, `Pool.Relocate`). Move the migration to phase 2 in the 05-download §1 table ("when a second
download instance can exist"). When it ships, it needs its own 09-style stage entry. Add a 09 S7 note:
"no books row rewrite".

**Trade:** Until phase 2, books can link to a torrent on only one configured instance, which is today's
behaviour.

### SIMP2-8 — MINOR — Two idempotency stores guard the one route that uses either

**Where:** 05-http-api §4.5 (lines 553–570: `c/http/idem/`, 24 h, `in_flight`/`done` records, response replay,
prune task), `Route.Idempotent`, error codes, metric; 05-tasks `StartRequest.IdempotencyKey` (line 748),
`c/tasks/idem/<kind>/<key>` (line 949), `core.tasks.idempotency_ttl` 7 d (line 1116); 05-config line 1066.

**Evidence:** 05-http-api declares `Idempotent` on exactly one core route,
`POST /api/core/v1/tasks/definitions/{kind}/runs`. The route "complements the definition's
`Dedupe`/concurrency keys". tasks already has a persisted start-idempotency store with its own TTL. The
other cited consumer, "a download-client grab", is deferred (cross-cutting 1). A retried click therefore
passes through two independent stores with different TTLs, and they can disagree (an HTTP replay after
24 h, but a task dedupe for 7 d). Separately, 05-config line 1066 still says derived keys keep "cursor
HMACs" alive, but SIMP-8 removed the cursor HMAC.

**Fix:** Delete httpapi's idempotency store (§4.5 steps 1–7, `Route.Idempotent`, `ErrIdempotencyNotPOST`,
`c/http/idem/`, `core.http.idempotency.ttl`, the prune task, `mediacore_http_idempotency_total`). The task-start
handler copies the `Idempotency-Key` header into `StartRequest.IdempotencyKey`. On a duplicate it returns
`200` with the existing `RunView` and `Idempotent-Replayed: true`. When a non-task side-effect route needs
it later, add it then. Remove "cursor HMACs" from 05-config line 1066.

**Trade:** A replay returns the current run view, not a byte-identical stored response. For a task start,
that is the more useful answer.

### SIMP2-9 — MINOR — 08 moves the conformance engine into core for a deferred consumer; 04 keeps it in ABO

**Where:** 08 §8.0 (lines 17–20), §8.14 (lines 541–546), review history (line 596); 04 row
`internal/syncapi/conformance` (line 368: "unchanged", app bucket); 03 §3.3 (no `httpapi/conformance`).

**Evidence:** 08 says that during the extraction "only two things exist: … the `/api/v3/` reservation and
the move of ABO's conformance engine to core", promoted "as `httpapi/conformance` — it then has two consumers
(ABS compatibility in ABO and this facade)". The facade is deferred, so during S0–S10 the engine has one
consumer (ABO), and 04, which is authoritative for moves, keeps it in ABO.

**Fix:** 08 §8.0 and §8.14: "the conformance engine moves to core (`httpapi/conformance`) **when
`arrfacade` is built**. Until then it stays `internal/syncapi/conformance` in ABO (04)". Delete "the
conformance-engine move" from the extraction list.

**Trade:** None.

### SIMP2-10 — MINOR — 05-events lists deferred download topics as core topics, and only half of them

**Where:** 05-events §5E.3.1 core-topic table (line 128) and example (line 70); 05-download §4.5 table
"Topic (phase 2)" (lines 352–360); 03 §3.6 event-topic example (line 324).

**Evidence:** 05-events lists `core.download.grab.added.v1`, `core.download.grab.failed.v1` and
`core.download.item.changed.v1` under "**Core topics** (declared by core …)" with no phase marker.
05-download marks all six download topics **phase 2** (deferred). 05-events omits
`item.{completed,failed,removed}`, which `acquire` consumes (10 lines 681–682). If core declares them in
phase 1, the catalog has topics without producers, and the UI's topic pickers (notify rules) would offer
them.

**Fix:** Move the row to a "Deferred-build topics (05-download phase 2)" sub-table that lists all six,
or delete the row and point at 05-download §4.5. Replace the 03 §3.6 example with a built topic, for
example `core.integration.health.changed.v1`.

**Trade:** None.

### SIMP2-11 — MINOR — Notify keeps a two-stage dispatch built for the deferred per-user delivery

**Where:** 05-notify §4 steps 2–4 (`core.notify.dispatch.v1` → per-target `core.notify.deliver.v1`), §9
`TestDispatch_ResolvesAtDeliveryTime`, §11 item 1; 07 §7.1 (line 20), 03 §3.3 `@mediacore/shell` row (line 220).

**Evidence:** v1 routing is global admin `Rule`s only (§1, §4 last paragraph). Resolving targets "at
delivery time" mattered for per-user subscriptions and per-recipient RBAC, which are now deferred (§11
item 2). v1 still writes two outbox generations per notification, and one test exists only to prove
that a rule edited in the milliseconds between publish and dispatch is honoured. Separately, 07 §7.1 and 03
list "notifications" in the shell with no section behind them. The inbox and the bell are deferred
(05-notify §1).

**Fix:** In `Publish`, resolve enabled rules from the in-memory rule set (rules are small admin data,
refreshed on `PutRule`/`DeleteRule`). Stage one `core.notify.deliver.v1` per target directly in the
caller's batch, and delete the dispatch stage and its test. Record in §11 that a digest window (item 1)
or per-user delivery reintroduces a dispatch stage. In 07 §7.1 and 03, define the v1 shell scope as
"notification channel and rule admin pages; toasts", with the inbox/bell deferred (05-notify §1).

**Trade:** A rule change applies to notifications published after it, not to ones already queued.

### SIMP2-12 — MINOR — Alias classes and dates are duplicated around `compat.Entry`

**Where:** 09 §9.2.9 `compat.Entry{Surface, Legacy, Canonical, Ledger, Gate}`; 05-config §1.3.1
`Alias.Class` / `AliasClass{Window, Permanent, Retired}` (lines 150–164); 05-http-api §4
`Deprecation{Shipped, Sunset}` (lines 176–185); 05-metrics `Alias` (line 130); 05-tasks `core.tasks.legacy_history_window` "duration … the compatibility window (09 §9.2.1)"
(line 1118).

**Evidence:** The window has one definition (good). But each subsystem's alias type carries its own
classification, which overlaps `compat.Gate` (`AliasPermanent` vs `GatePermanent`), and its own dates.
httpapi's `Deprecation.Shipped` and `Sunset` must be typed into Go code after the release ships, while
`docs/compat-surfaces.md` already holds "shipped release and date, earliest removal date", and
`TestNoContractBeforeSunset` reads those dates from the ledger. W is not a duration ("the later of two
minors and 30 days, plus the usage gate"), yet 05-tasks declares a duration key whose default is W.

**Fix:** Subsystem alias types carry `Ledger string` (the row ID) and nothing else about lifetime.
Class, gate and dates come from the ledger row. `TestCompatLedgerMatchesCode` already joins them, and the
`Sunset` header is rendered from the embedded ledger. Replace `AliasClass` with `compat.Gate` plus a
`Retired` state on the ledger row. Delete `Deprecation.Shipped`/`Sunset`. Replace
`core.tasks.legacy_history_window` with "legacy history is read until contract C5 (09), then aged out by
`history_retention`".

**Trade:** The `Sunset` header needs the ledger embedded in the binary, which is one `//go:embed` of
`docs/compat-surfaces.md` (or a generated Go table).

### SIMP2-13 — MINOR — The metric segment depends on the *enabled* module list

**Where:** 05-metrics §3 `Options.Modules []string // enabled module IDs; one module => module scopes use
MetricPrefix alone` (line 75), test (line 301); 03 §3.6 Metric row; 06 A6 (enablement is a restart-bound
setting).

**Evidence:** The SIMP-13 fix ties the `<moduleID>_` segment to the number of **enabled** modules. In a
two-module app, disabling one module at the next restart renames every series of the other, which
breaks dashboards. That is the cost SIMP-13 set out to remove.

**Fix:** Decide the segment by the modules **compiled into** the app (the `app.New` module list), which
does not change at run time. 03 §3.6: "while the app *ships* one module".

**Trade:** None for ABO (one module either way).

### SIMP2-14 — MINOR — 05-tasks says the T0+T1 release "raises the level"

**Where:** 05-tasks §5T.8.2 row T0+T1 (line 1049) vs §5T.6 (lines 989–994), review history (line 1222); 09
§9.2.2 ("Only a `Breaking` step with `RaisesLevel` raises a level, and in this sequence only contracts do").

**Evidence:** 5T.6 says that the adopt step "is `Additive` and raises no level". C5 raises `core` to 4. The
batch table says the flip release "raises the level (5T.6)". Under the one-guard policy, the S5 build
*declares* reader level 4. A reader who follows the batch table would raise the store's level at the flip,
and rollback to S4 would then be refused.

**Fix:** Change the row's text to "declares reader level 4 (09 §9.2.2); raises no store level (C5 does)".

**Trade:** None.

### SIMP2-15 — MINOR — The operational dial has two names

**Where:** 09 §9.2.8 (line 271), S5 Config (line 475), §9.11 S5 risk (line 737): `core.tasks.kinds.<kind>.parallelism`;
05-tasks §5T.3.2 (line 272), §5T.10 (line 1105): `core.tasks.kinds.<kind>.parallelism_cap`.

**Fix:** Use `parallelism_cap` in 09, because 05-tasks owns the key.

**Trade:** None.

### SIMP2-16 — MINOR — Stale cross-references left by the revision

**Where and fix:**
- 09 §9.2.3 rule 2 (line 192): "(05-rbac §6.3 and 05-authn §11 still state the unconditional drop;
  cross-cluster request to align them.)" Both files are aligned (05-rbac lines 717–721, 05-authn lines 926–928).
  Delete the parenthetical.
- 13 §13.5.2 (line 223): "**only if** 05-config §1.10 adopts it". Adopted (SIMP2-3 item 5).
- 05-config line 1066: "cursor HMACs" (SIMP2-8).
- Review-history lines still give slash-form secret names that the integration pass replaced:
  05-metadata line 1000 (`integrations/<kind>/<id>/<field>`), 05-notify line 274, 05-download line 609. Mark
  them "superseded by the dotted form", so that a reader who greps history does not take the old name.

**Trade:** None.

### SIMP2-17 — MINOR — Download residue: `Remove` as an ABO need, and a native rewrite of books routes

**Where:** 05-download §4.1 Deluge row ("ABO's needs first (list, by label, details/files, move,
**remove**)"), §1 table (`Pool.Remove` phase 2), §10 route row (line 575: `/api/v1/deluge/*` →
`/api/books/v1/downloads/*`); 06 §6.4.4 `Downloads *download.Pool // … Items, Files, Relocate, Remove`
(line 460); 09 S7 Endpoints (line 503); cross-cutting 2.

**Evidence:** `internal/deluge/client.go:483` defines `RemoveTorrent`, and it has **zero** callers
(`git grep '\.RemoveTorrent('` @748fd787a is empty). 05-download §1 correctly puts `Pool.Remove` in phase 2.
The §4.1 row and 06's comment contradict that. 05-download §10 moves ABO's Deluge behaviour routes to a new
native books group with aliases. Cross-cutting 2 and 09 S7 keep books routes on the legacy mount "unless
rewritten for their own reasons", and none is given.

**Fix:** Remove "remove" from the §4.1 ABO-needs list and `Remove` from 06's comment. §10 route row:
"`/api/v1/deluge/*` books behaviour routes stay on the legacy mount (cross-cutting 2); generic client
routes become core instance routes with aliases (09 S7)".

**Trade:** None.

---

## Considered and not raised

These are new or kept complexity that the lens examined. Each one either rests on a binding resolution
or has no simpler design that meets every goal.

- **Per-owner reader levels (05-storage §5S.3.7, 09 §9.2.2).** Only owner `core` rises in S0–S10. The
  `books` owner is still used: the storage-efficiency release B is a books `Breaking` step (05-storage
  "`RunCutover` is resolved"). DATA-20's goal ("books can raise its level without a core release";
  a books change never stops a TV app on the same core) needs two counters after the split. A single
  integer would couple core releases to app releases across repos. The legacy projection is the only
  way pre-guard builds refuse. Not raised.
- **Mirror + side records + rebuild at every boot (05-storage §5S.3.11).** Cross-cutting 5 prescribes this
  and binds it. For the lead: the over-cap branch already specifies a complete read-through view (legacy
  row + side record, "always correct"). That is a second read path for the same data. One option within
  the resolution is to make the read-through view the only read path during W, and to materialise the
  native copy once at the contract (the contract already "runs one more rebuild"). That would delete the
  per-boot rebuild and the native half of the dual write. Its trade is a legacy-backed `authstore`
  implementation for the length of W. This is offered as an option, not raised as a defect.
- **Served ledger, adopt-applied record, `RequiresServed`, persisted compat usage** (09 §9.2.1–§9.2.2).
  These are required by cross-cutting 6 (skipped releases) and OPS-16 (gates a restart cannot reset).
  No simpler design meets "the unit of compatibility is the store". Not raised.
- **Claim modes and the shadow→strict release (05-storage §5S.3.4, 09 S2b).** The six modes map to
  distinct real families (live, aging-out history, read-share `idx:`/`pref:`). The shadow release is the
  cheapest proof that no unregistered writer exists. Not raised.
