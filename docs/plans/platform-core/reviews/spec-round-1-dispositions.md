<!-- file: docs/plans/platform-core/reviews/spec-round-1-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: f4a3b106-a8ae-4488-b86e-59f0ad47a284 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 1 — dispositions

Inputs: the seven round-1 reviews in this directory (`spec-round-1-{correctness,data-migration,frontend,modules-build,ops-rollback,security,simplicity}.md`).
Totals: **192 findings — 20 BLOCKER, 118 MAJOR, 54 MINOR.**
Dispositions: **192 accepted** (some modified, as noted), **0 rejected**.
No finding was dropped silently. Rejections, if any, carry evidence in the note.

Why no rejections: each finding cites a spec location and, where it concerns current code, a code
anchor. The lead re-checked a sample of the blockers against the code: the Dockerfiles' `COPY go.mod
go.sum` + `go mod download && go mod verify` (`Dockerfile:54-55`, `Dockerfile.build-cgo:52-53`, MOD-1/OPS-1);
65 non-test `SetSetting(` call sites under `internal/` (DATA-1); `DATABASE_PATH` in
`deploy/audiobook-organizer.service` and `deploy/com.audiobook-organizer.plist` (OPS-5); empty API-key
scopes meaning "all role permissions" (`internal/server/middleware/auth.go:405-408`, SEC-1/OPS-4). All
reproduced. The rest were accepted on the reviewers' evidence. **Revisers must reject a finding, with
evidence, if applying it shows it to be wrong**; any such reversal is recorded in round 2's log.
Where two reviewers proposed different fixes for one defect, or a fix would have traded away a stated
goal, the lead chose one resolution, recorded below as a **cross-cutting resolution**. Every affected
file applies that resolution, not the individual fixes.

## Reviser clusters (no two revisers edit the same file)

| Cluster | Files |
|---|---|
| R-A | 03, 02, 01, 12, 13, 14, 15, README |
| R-B | 05-storage, 05-backup, 05-library |
| R-C | 05-tasks, 05-events, 05-health, 05-updater |
| R-D | 05-http-api, 05-realtime, 05-authn, 05-rbac |
| R-E | 05-config, 05-logging, 05-metrics |
| R-F | 05-ai, 05-download, 05-indexer, 05-metadata, 05-notify, 10 |
| R-G | 06, 07, 08, 11 |
| R-H | 09, 04 |

## Cross-cutting resolutions (binding on all revisers)

1. **Promotion / build scope (SIMP-1, SIMP-10–12, SIMP-24, COR-3).** Core builds only what ABO consumes during S0–S11 or what core itself needs. `acquire`, `arrfacade`, `integrations/indexer` and the download grab/monitor pipeline are **deferred-build**: contracts stay (proof depth), the plan builds none of them. Notify v1 = contract + mail + webhook. AI usage ledger deferred.
2. **HTTP (SIMP-2, FE-1, SEC-13, OPS-11).** Core `httpapi` is stdlib. ABO's gin router is mounted permanently via `httpapi.MountLegacy`, behind core's perimeter middleware (peer allowlist, recovery, headers, CSRF, rate limit) with gin's own auth chain unchanged inside; the auth engine swap happens in exactly one stage (09 S4). No gin-retirement stage. `/` belongs to the legacy mount until ABO adopts the shell build.
3. **Globals (SIMP-3).** R5 binds core only; ABO façades over core handles are allowed and per-App.
4. **One integration-instance store (SIMP-4)**, **one topic catalog (SIMP-5)**, **one schedule mechanism (SIMP-6)**, **one compat window (SIMP-7)**, **one compat/format guard (SIMP-18)**, **one JSON-Schema reflector (SIMP-14)**, **one entity reference `entity.Ref` (COR-17)**, **one keyring (SEC-20)**, **one config/secrets migration (SIMP-15)**.
5. **Identity-family adoption (DATA-3, DATA-4, OPS-19, SEC-1, SEC-19, DATA-5).** Legacy rows stay authoritative for the window; new build writes legacy + core (incl. `idx:` rows the old build resolves through) in one batch and rebuilds the core copy from legacy on every boot; native-only data in core side records; keys whose scopes have no exact legacy equivalent are mirrored disabled; no raw-session mirror (one re-login on rollback).
6. **Releases can be skipped (OPS-2, OPS-3).** Every contract step requires the stamped level to prove the prior expand/adopt ran; otherwise adopt first or refuse to start. Never delete legacy data on an unadopted store.
7. **Settings families (DATA-1).** `setting:`/`preference:`/`pref:` stay books legacy; core config overrides use `c/config/`.
8. **Backups (SIMP-9, DATA-22, DATA-21, OPS-14, SEC-16, DATA-23, DATA-24).** One archive format (05-backup owns; 09 references). Encryption opt-in on upgrade; never prune the last verified plaintext archive until an encrypted one is verified and the identity export confirmed; restore verifies archive provenance (install identity) and re-applies revocations recorded after the archive; the backup catalogue and restore audit live outside the restored DB.
9. **Docker/build (MOD-1, OPS-1, MOD-9, FE-13).** S0 updates both Dockerfiles, `.dockerignore`, every `npm ci` entry point, and adds a PR job that builds the image.
10. **Module visibility (MOD-2).** A package still imported by ABO moves to a public core path or stays in ABO.
11. **genproto exclude (MOD-3, MOD-4)** goes in `core/go.mod`.
12. **Auth-off, confinement, user management (SEC-2, SEC-3, SEC-4)** as in the rows below.

## Findings

| ID | Sev | Finding (reviewer's title) | Disposition | Owner cluster | Note |
|---|---|---|---|---|---|
| COR-1 | BLOCKER | Modules cannot reach services the flows depend on (`module.Deps` and `Registrar` gaps) | Accepted | R-G, R-C, R-F | 06 `Deps`/`Registrar` gain every handle the flows need (acquire hook placeholder, integration pools, realtime topic registration, change-journal hook, auxiliary store opener, outbox handlers); construction order made acyclic (services built before `Register`; registrations consumed after). |
| COR-2 | BLOCKER | `storage` and `storage/index` import each other | Accepted | R-B | `storage/index` depends on `storage`, never the reverse; index wiring lives in kernel. |
| COR-3 | BLOCKER | Two owners for download tracking, recovery and completion (05-download vs 10/acquire) | Accepted | R-F | Single owner: `integrations/download` owns client polling, item state and completion events; `acquire` (deferred build) owns grab decisions, tracked-download records and import hand-off and consumes download events; no second poller. |
| COR-4 | MAJOR | The "history write in the same batch as the module's file write" cannot be expressed | Accepted | R-F, R-B | Reviewer's concrete fix applied as written. |
| COR-5 | MAJOR | Held files' format score cannot be computed, so CF upgrades loop and cutoff-unmet cannot be found | Accepted | R-F | Reviewer's concrete fix applied as written. |
| COR-6 | MAJOR | Movies minimum availability: the wanted index freezes a time- and config-dependent predicate | Accepted | R-F | Reviewer's concrete fix applied as written. |
| COR-7 | MAJOR | Double grab across concurrent tasks, and import partitioning is inconsistent with multi-ref downloads | Accepted | R-F, R-C | Reviewer's concrete fix applied as written. |
| COR-8 | MAJOR | Start/stop order of bus subscriptions, outbox relay and module hooks contradicts itself across 06, 05-events and 05-tasks | Accepted | R-G, R-C | Reviewer's concrete fix applied as written. |
| COR-9 | MAJOR | Outbox handlers run inside `core.outbox.relay` and bypass the task write gate | Accepted | R-C | Reviewer's concrete fix applied as written. |
| COR-10 | MAJOR | The outbox gap-safe horizon is global, depends on an API storage does not have, and one leaked batch stalls all delivery | Accepted | R-C, R-B | Reviewer's concrete fix applied as written. |
| COR-11 | MAJOR | Cross-module outbox consumption requires importing the producer (R3 violation) | Accepted | R-C, R-G | Reviewer's concrete fix applied as written. |
| COR-12 | MAJOR | Disabling a module silently loses its correctness-critical outbox messages | Accepted | R-C | Disabled modules' outbox messages are retained (not delivered, not dropped) until re-enable or explicit purge. |
| COR-13 | MAJOR | Disabling any module with task history trips the op-ledger boot gate, and the `held` state is undefined | Accepted | R-C, R-G | Disabling a module never trips the ledger gate: its task records are `held` (defined state, read-only, resumable on re-enable). |
| COR-14 | MAJOR | Module enablement is decided in P1, but the UI stores it in the DB layer, which loads in P3 | Accepted | R-G, R-E | Module enablement is a boot-time setting read in the phase that decides it (file/env/flag or a pre-storage enablement record read before module registration); the UI writes that record and shows "restart required". |
| COR-15 | MAJOR | `audit.RecordIn` into caller batches breaks the "gap-free" sequence and the hash chain | Accepted (resolved by SIMP-20) | R-D | Hash chain dropped; audit sequence allocated by `Sequence` per DATA-19. |
| COR-16 | MAJOR | SDK type vocabulary disagrees between 06 and its owning files, and the `hello` proof does not compile | Accepted | R-G, R-D | Reviewer's concrete fix applied as written. |
| COR-17 | MAJOR | The polymorphic entity reference has four names and two cardinalities | Accepted | R-A, all | One reference type `entity.Ref{Module, Kind, ID}` (03 v1.2.0); all files use it; multi-entity grabs carry `[]entity.Ref`. |
| COR-18 | MAJOR | Core capability HTTP routes have no layer-legal home under R4 | Accepted | R-A, R-G | Reviewer's concrete fix applied as written. |
| COR-19 | MINOR | 05 files use pre-v1.2.0 layer numbers, and internal bridge packages break the "internal takes its public package's layer" rule | Accepted | R-A, all | Reviewer's concrete fix applied as written. |
| COR-20 | MINOR | events/realtime/metrics signature mismatches and task-topic mapping | Accepted | R-C, R-D, R-E | Reviewer's concrete fix applied as written. |
| COR-21 | MAJOR | Handle scoping (06 A3) is not enforceable with the types given | Accepted | R-G | Reviewer's concrete fix applied as written. |
| COR-22 | MINOR | `storage.Compat` names two different things; the read-adapter type is undefined | Accepted | R-B | Reviewer's concrete fix applied as written. |
| COR-23 | MINOR | R7 enforcement rejects pass-through parallelism, and some callers cannot call `RunItems` at all | Accepted | R-C | Reviewer's concrete fix applied as written. |
| COR-24 | MINOR | Context is not threaded into `Register`, and `app.New` claims to start no goroutines | Accepted | R-G | Reviewer's concrete fix applied as written. |
| COR-25 | MINOR | TV and Movies in one app: one rss-sync cannot serve two RSS query types, and the vocabulary owner is undefined | Accepted | R-F | Reviewer's concrete fix applied as written. |
| COR-26 | MINOR | Naming contradictions with 03 §3.6 (task-kind grammar, metric prefix, app ID field) | Accepted | R-A, all | Reviewer's concrete fix applied as written. |
| COR-27 | MINOR | 10 and 08 reference library and indexer identifiers that do not exist | Accepted | R-F, R-G | Reviewer's concrete fix applied as written. |
| DATA-1 | BLOCKER | `setting:`/`preference:`/`pref:` are not config; adopting them breaks books writers / 05-storage §5S.9.1–§5S.9.2 | Accepted | R-B, R-E | `setting:`, `preference:`, `pref:` stay books-owned legacy families; core config overrides live in a new `c/config/` keyspace; no adoption of those families. |
| DATA-2 | MAJOR | Legacy-family ownership is specified four different ways / 05-storage §5S.9.1 vs 05-tasks, 05-logging, 05-config, 09, 04 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-3 | BLOCKER | 05-storage adoption has no reconcile: rollback writes lost, revoked credentials return / 05-storage §5S.9.2 step 4 | Accepted (resolution chosen) | R-B, R-H, R-D | Adoption model for small identity families during the window: **legacy keys stay authoritative**; the new build writes legacy rows and core rows in one batch, and **rebuilds the core copy from legacy at every boot** (families are small; over-cap falls back to the resumable path). Rows changed by an older build (deactivations, revocations) are therefore always honoured; no lossy merge. Native-only data (e.g. native scopes) lives in core side records keyed by the legacy ID and is merged at rebuild; if the legacy row was revoked/deleted, the side record is dropped. |
| DATA-4 | MAJOR | 09's "legacy wins" reconcile is lossy (narrows every API key used on the old build) / 09 §9.2.3 | Accepted (superseded by DATA-3 resolution) | R-H | No "legacy wins" field merge; full rebuild from legacy per DATA-3. |
| DATA-5 | MAJOR | Mirror writes primary rows only; old build resolves users/keys/sessions via `idx:` rows / 05-storage §5S.9.2, 09 §9.2.3 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-6 | MAJOR | One-shot imports (tasks, clocks, config blob) have no rollback-return path / 05-tasks §5T.6, 05-config Part 1 migration, §2.10 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-7 | MAJOR | Storage format vs compat level vs `Breaking` steps: three window-end mechanisms / 05-storage §5S.3.5, §5S.9.2 step 5; 09 §9.2.2, §9.5 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-8 | MAJOR | Books' 66 migrations cannot be `MigrationTx` steps; S6/G2 atomicity is false for them / 05-storage §5S.3.5, §5S.9.3 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-9 | MAJOR | Cross-keyspace ordering cannot express 09's dependency; 06 contradicts checkpoint rule / 05-storage §5S.3.5 Order; 06 §6.5.1 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-10 | MAJOR | Pre-migration checkpoint pruned by crash loop, no restore tool, wrong filesystem / 05-storage §5S.3.5, §5S.7 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-11 | MAJOR | Lifecycle ratchets the Pebble format before taking the checkpoint / 05-storage §5S.4 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-12 | MAJOR | Data-migration resume not exact; `DataMigration` has no compat class / 05-storage §5S.3.6 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-13 | MAJOR | Online copy vs owner writes is a lost-update race; no lock/CAS API / 05-storage §5S.6, §5S.9.2 step 3 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-14 | MAJOR | Dry run is unfaithful and unavailable for BootBlocking migrations / 05-storage §5S.3.6, §5S.4 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-15 | MAJOR | `opstate:` is live; `:params` suffix not expressible; ReadOnly forbids retention deletes / 05-storage §5S.9.1–§5S.9.2; 05-tasks §5T.6 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-16 | MAJOR | Overlap rule 3 makes ABO's own 89 nested family pairs fail boot / 05-storage §5S.3.4 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-17 | MAJOR | `LegacyRange` reads are unchecked / 05-storage §5S.3.4 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-18 | MAJOR | Cross-owner atomic writes impossible (admin user merge) / 05-storage §5S.3.2; 09 S4 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-19 | BLOCKER | `Sequence` as specified can reissue committed IDs after a crash / 05-storage §5S.3.2; 05-events §5E storage | Accepted | R-B, R-C | `Sequence` allocates by persisting the high-water mark **before** handing out IDs (block reservation committed first), so a crash can only skip IDs, never reissue. |
| DATA-20 | MAJOR | One install-wide storage-format constant couples all apps to books' format changes / 05-storage §5S.3.7 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-21 | MAJOR | 05-backup and 09 S8 specify incompatible archive formats / 05-backup §5B.3.3; 09 S8 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-22 | BLOCKER | Encrypted-by-default on upgrade + retention ⇒ no backup survives host loss / 05-backup B3, §5B.3.3, §5B.10 | Accepted | R-B | Encryption opt-in on upgrade; retention never deletes the newest verified plaintext archive until an encrypted archive is verified **and** the identity export is confirmed; host-loss restore documented. |
| DATA-23 | MAJOR | Boot-phase restore can leave the app down with no revert; runs after config load / 05-backup §5B.3.4; 06 §6.5.1 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-24 | MAJOR | Backup catalogue and audit trail live inside the DB being restored / 05-backup §5B.3.2, §5B.3.5 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-25 | MAJOR | Derived-index acceptance cannot detect stale/ghost documents / 05-backup §5B.3.1; 05-storage §5S.3.8–§5S.3.9 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-26 | MAJOR | `keys.Escape`/`Join` are not order-preserving; `ValidSegment` rejects `Escape` output / 05-storage §5S.3.3 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-27 | MAJOR | `core.tasks.adopt-active` can fail boot permanently on a normal install / 05-tasks §5T.6 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-28 | MINOR | Claim registry / bookkeeping lifecycle gaps / 05-storage §5S.9.1, §5S.3.5; 06 P2; 09 S0, S2 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-29 | MINOR | Factual slips and a missing Batch hook required by 05-events / 05-storage §5S.9, §5S.13; 05-events | Accepted | R-B | Reviewer's concrete fix applied as written. |
| DATA-30 | MINOR | Multi-app store merge is not "copy keyspaces" / 11 §11.5 | Accepted | R-B | Reviewer's concrete fix applied as written. |
| SEC-1 | BLOCKER | Rollback mirror can turn a narrow API key into a full-access key | Accepted | R-D, R-H, R-B | Never widen on rollback: a key whose scopes have no exact legacy equivalent is mirrored to legacy **disabled** (revoked flag) with a reason; the old build can never read it as unscoped. Native scopes live in a core side record. See DATA-3 resolution. |
| SEC-2 | BLOCKER | The auth-disabled principal holds `core:admin`, which defeats `AuthRequired` | Accepted | R-D | Auth-off principal never holds `core:admin` or any `Sensitive`/`AuthRequired` scope; data-destroying and credential routes remain closed with auth off (today's `admin-routes-auth-off` semantics). |
| SEC-3 | BLOCKER | Worker-only confinement is lost, and the authn and rbac files contradict each other about it | Accepted | R-D | Worker-only confinement restored: credential kinds (fingerprint worker) carry an explicit route allowlist enforced before permission checks; `AnyAuthenticated` excludes confined credentials; 05-authn and 05-rbac aligned. |
| SEC-4 | BLOCKER | Users with `core:users.manage` can take over more-privileged accounts (reset link, temp-login, set password) | Accepted | R-D | `core:users.manage` cannot act on an account holding any permission the actor lacks (reset link, temp login, set password, deactivate, delete) — same bound as role assignment. |
| SEC-5 | MAJOR | Facade-key confinement exists only in 08; 05-indexer requires the opposite scope | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-6 | MAJOR | Config keys for sign-in, proxies and the updater carry no `Protection`, so `core:settings.manage` can become `core:admin` | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-7 | MAJOR | OIDC linking by verified email across arbitrary generic providers lets one account take over another | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-8 | MAJOR | Seeding `core:system.view` gives viewers the system logs and status, which today need `settings.manage` | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-9 | MAJOR | Removing the task-start floor lets viewers start `settings.manage`-free jobs (bulk-apply preview) | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-10 | MAJOR | No CSRF or cross-origin defence in the chain; SameSite=Strict does not cover same-site sibling apps | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-11 | MAJOR | One default session-cookie name for every app on a host: tokens leak between apps and logins lock out | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-12 | MAJOR | Stage 11 is specified both as "never rejects" and as "a presented invalid credential is a 401" | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-13 | MAJOR | During the transition the legacy mount is authorized only by gin; the bridge is unspecified; S2 and H0 conflict | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-14 | MAJOR | The Basic-auth perimeter exemption becomes mount-wide, widening today's per-route ABS exemption | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-15 | MAJOR | SSRF and credential theft through integration instances (facade-pushed URLs, URL change keeps the secret) | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-16 | MAJOR | Restore trusts any age-encrypted archive; revoked API keys come back; the authn hook does not exist | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-17 | MAJOR | Audit gaps: restore erases its own audit record; no audit for integrations, diagnostics or export | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-18 | MAJOR | Client-IP trust migration (`TUNNEL_CONNECTOR_ADDRS`, zoned link-local) is missing; tunnels collapse throttles | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-19 | MAJOR | Session migration: 05-authn hashes only, but 09 mirrors raw tokens and keeps the fallback for the whole window | Accepted | R-D, R-H | No raw-session mirror (owner question Q-D recommendation): sessions are hashed; a rollback costs one re-login. 09 updated. |
| SEC-20 | MAJOR | Secrets key migration: 05-config, 09 S3 and 14 Q-E describe three incompatible keyrings | Accepted | R-E, R-H | One keyring design (05-config) referenced by 09 S3 and 14 Q-E. |
| SEC-21 | MAJOR | Invite tokens sit in a core URL path that no redactor covers, and viewers can read the journal | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-22 | MINOR | `apikey` and `token` query values can reach trace span attributes | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-23 | MINOR | How `Sensitive` interacts with `core:admin` is undefined; 08 uses permission fields that 05-rbac does not have | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-24 | MINOR | Task approval: 05-tasks lets an API key approve, 05-rbac says approval needs an interactive method | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-25 | MINOR | Seed merge and `DefaultGrants` can silently push Sensitive or AuthRequired scopes into existing roles | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-26 | MINOR | `auth.System` and `rbac.All` are public SDK, so any module can mint a superuser principal | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-27 | MINOR | The updater signature does not bind app, version or channel; the signing key may be shared across apps | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-28 | MINOR | `DeriveKey` from the active master changes on rotation; the AAD binding to AppID makes a rename fatal | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-29 | MINOR | Secret naming and scoping disagree (06 `secrets.Scope` `<ID>/…` vs 05-config path names) | Accepted | R-D | Reviewer's concrete fix applied as written. |
| SEC-30 | MINOR | Setup's loopback exception is defeated by a local reverse proxy or tunnel connector | Accepted | R-D | Reviewer's concrete fix applied as written. |
| OPS-1 | BLOCKER | S0 breaks every Docker build, and only the production release finds out | Accepted (duplicate of MOD-1) | R-H | Same. |
| OPS-2 | BLOCKER | The model assumes consecutive deployed builds, but upgrades skip releases, and a contract can then delete rows that were never adopted | Accepted | R-H, R-B | Upgrades may skip releases: every contract step checks the stamped level proves the preceding expand/adopt ran; a store that never adopted runs adoption first (or refuses to start with a clear message), never a contract delete. |
| OPS-3 | BLOCKER | The compat guard lets a build open a store whose config it cannot read | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-4 | BLOCKER | Rollback can widen an API key's access | Accepted (duplicate of SEC-1) | R-D, R-H | Same fix as SEC-1. |
| OPS-5 | BLOCKER | After W, dropping legacy env names silently points ABO at a new, empty database | Accepted | R-H, R-E | Env aliases that are the only source of a required setting in a shipped deploy file (e.g. `DATABASE_PATH`) are permanent (compat ledger "permanent" class), and boot refuses to start on an empty store when a legacy DB path is set but unread. |
| OPS-6 | MAJOR | The updater's automatic rollback bypasses the compat and storage-format guard that `make rollback` enforces | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-7 | MAJOR | 09 and 05-tasks describe two incompatible S5 storage designs | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-8 | MAJOR | 05-storage contradicts 09's compat machinery on five points | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-9 | MAJOR | The S3 config/secrets model is specified three ways | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-10 | MAJOR | HTTP aliases have a wall-clock time bomb and the wrong counter | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-11 | MAJOR | The auth engine moves in S2 or in S4, depending on which file you read | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-12 | MAJOR | Large steps in the sub-specs have no stage, rollback entry or done-check in 09: (1) books moving onto `storage.LegacyRange`, with core openi | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-13 | MAJOR | The shutdown budget rests on a wrong premise, and S2 changes it without saying so | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-14 | MAJOR | The backup format conflicts with 09, and a pending restore can apply later by surprise | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-15 | MAJOR | The broken live restore stays in service while contracts start deleting rows | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-16 | MAJOR | The removal gate cannot be measured as specified | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-17 | MAJOR | The "no feature flags" claim is already false, and it also removes cheap operational levers | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-18 | MAJOR | A truth flip spans many PRs, and intermediate main builds get deployed | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-19 | MAJOR | Reconcile is lossy and can take the whole service down | Accepted (superseded by DATA-3 resolution) | R-H | Same. |
| OPS-20 | MAJOR | The rollback rehearsal skips the clients that actually break | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-21 | MAJOR | External ABS clients are smoke-tested only before S10, but their request path changes in S2 and S4 | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-22 | MAJOR | Boot time is budgeted only in S2 | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-23 | MAJOR | At S8, existing auto-update users silently stop receiving updates | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-24 | MINOR | Several done-checks are not mechanically measurable | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-25 | MINOR | Disk headroom for pre-migration checkpoints is never checked | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-26 | MINOR | The compat sidecar has operational gaps | Accepted | R-H | Reviewer's concrete fix applied as written. |
| OPS-27 | MINOR | Smaller consistency and observability gaps | Accepted | R-H | Reviewer's concrete fix applied as written. |
| MOD-1 | BLOCKER | Both Docker builds break the moment the `replace` lands (wave 0) | Accepted | R-H, R-A | S0 updates both Dockerfiles (copy `core/go.mod`, `core/go.sum` before `go mod download`; then `core/`), `.dockerignore`, and adds a PR CI job that builds the Docker image. |
| MOD-2 | BLOCKER | The wave plan rewires ABO packages onto `mediacore/internal/*` and `mediacore/kernel`, which Go's internal rule and R2 forbid, so ABO stops  | Accepted | R-A, R-H | Packages still imported by ABO move to **public** core paths (or stay in ABO until their last ABO importer is rewired). 04's internal/public column is re-derived from the import graph; rule added to 03 §3.4. |
| MOD-3 | MAJOR | The root `exclude` also breaks `go install github.com/falkcorp/audiobook-organizer@vX`, so cutover step 7 cannot pass | Accepted | R-A, R-H | The genproto `exclude` goes in `core/go.mod` (tested by the reviewer), not the root; root `go install` path is unaffected after the split. |
| MOD-4 | MAJOR | A root-only exclude does not cover the workspaces developers will actually create | Accepted | R-A | Same as MOD-3; documented developer workspace command. |
| MOD-5 | MAJOR | `core-v0.Y.Z` annotated tags on the ABO release commit take over `git describe` | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-6 | MAJOR | 09 S1's move list contradicts 04 (and 03 §3.2.1) for 8 packages, and it moves the exact test package that caused the E2 hazard | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-7 | MAJOR | `examples/hello` is a package in three files and a separate module in two, and the separate-module form defeats the reason S3 exists | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-8 | MAJOR | "Run every Go target twice via `GO_MODULE_DIRS`" does not define what happens to `make ci` (14 steps, `Makefile:785`) or to the CI jobs | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-9 | MAJOR | Every non-Make web build path breaks once `web/` depends on `@mediacore/*`, whose `exports` point at the built `dist/` (07 §7.3.1) | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-10 | MAJOR | `core/go.mod` lacks the `ignore ./web` that the root needed for the same reason | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-11 | MAJOR | Dependency version skew: the binary ships dependency versions that core CI never tested | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-12 | MAJOR | `mediacore.Version` has no defined source, and the obvious one breaks boot before the split | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-13 | MAJOR | The S1 kernel filter never matches, so S1 cannot pass while `kernel/` changes | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-14 | MAJOR | Both positive controls of the redesigned ABO guard are unsound | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-15 | MINOR | Bullet 4 ("computed layer below … an ABO package it re-exports to core through a callback") cannot be checked from an import graph, because  | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-16 | MINOR | `go list -deps` excludes test imports, and the E2 hazard came from a test file. | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-17 | MINOR | `core/Makefile` is never specified (03 has only a root variable), and a core Makefile would carry a 35th `GOTOOLCHAIN` pin | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-18 | MINOR | The second `cd core` fails after the first in one shell | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-19 | MINOR | `govulncheck@latest` is unpinned and violates the 14-day release-age rule and the repo's `tools/*/go.mod` pinning pattern | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-20 | MINOR | `core/examples/hello/cmd/hello/ui_embed.go` uses `//go:embed all:web/dist`, which resolves to `cmd/hello/web/dist` | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-21 | MINOR | Two moved files import code that 04 keeps in ABO | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-22 | MINOR | The filtered clone carries every ABO tag (`v0.2xx.x`, RC tags, checkpoints) | Accepted | R-A | Reviewer's concrete fix applied as written. |
| MOD-23 | MINOR | "`go.work` stays banned (03 §3.2 item 1)", but 03 §3.2 item 1 and 02 N1 explicitly allow an uncommitted developer `go.work`. | Accepted | R-A | Reviewer's concrete fix applied as written. |
| FE-1 | BLOCKER | Nothing in the spec serves ABO's SPA between S2 and S9; as written ABO either fails boot or hits a `/` pattern conflict | Accepted | R-G, R-H, R-D | `ui.Handler` mounts only from the stage where ABO adopts the shell build; until then the legacy gin mount keeps serving the SPA at `/`; `httpapi` reserves no `/` pattern while `MountLegacy` owns it; meta-tag requirement only applies to shell builds. |
| FE-2 | BLOCKER | Step 2 of 07 §7.19 (switch to `createShell`) cannot ship before step 3 (react-router 8) | Accepted | R-G | Order: react-router 8 migration → shell adoption. |
| FE-3 | MAJOR | `ui.Handler` drops today's SPA-fallback protections and has no hook for app SPA middleware, which regresses ABS clients and CF recovery | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-4 | MAJOR | CSP scope narrows from "every response" to "SPA only", and the directive set is left unspecified | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-5 | MAJOR | No file owns CSRF, and 13 claims a mechanism that 05-http-api does not include | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-6 | MAJOR | Two apps on one host overwrite each other's session cookie and log each other out | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-7 | MAJOR | The global "any 401 → reset and go to /login" rule breaks the Public auth flows that return 401 by design | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-8 | MAJOR | As specified, the realtime client cannot combine backoff with resume, cannot see 401/429/503/SSO, and loses named events | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-9 | MAJOR | Stream caps are smaller than the number of streams the UI opens, both during the projection phase and with several tabs | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-10 | MAJOR | The three files disagree on when and how the UI moves to the native stream, and the shutdown banner is wired to a topic that doesn't exist | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-11 | MAJOR | In S4 the server switches permissions to canonical names, but the UI's hard-coded legacy atoms are not updated, so buttons disappear | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-12 | MAJOR | The generated client arrives at S9, but S4–S6 move UI calls to native routes "in the same build", and the native JSON naming is undefined | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-13 | MAJOR | Eleven-plus build entry points run `cd web && npm ci` and break once `web/` depends on built `core/web/packages/*/dist`; `.dockerignore` let | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-14 | MAJOR | 07 and 12 contradict each other on npm publishing and consumption after the split | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-15 | MAJOR | G3 checks module versions but never compares the shell's core version with the server's; the build-ID check misfires in dev | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-16 | MAJOR | The extension API lacks slots that ABO's own page split needs, so the "move unchanged" step would have `core/web` import ABO code, and nothi | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-17 | MAJOR | The react-router migration is undercounted (52 files vs 122), 07 and 13 disagree, and the test harness rework is missing | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-18 | MAJOR | The `mediacore` chunk group pulls lazy shell pages into one eagerly loaded chunk, and it matches nothing in-repo | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-19 | MAJOR | `core.ui.base_path` exists only on the UI side; the API, cookies and SSE stay absolute, so a sub-path deployment cannot work | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-20 | MINOR | O4 is answerable today: the override works on TS 6, fails on TS 7, and the Go-emitter fallback is not needed; also undecided is where `core. | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-21 | MINOR | 07's frontend version targets violate 13's release-age rule, and 13 is wrong about knip | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-22 | MINOR | `badge: () => useReviewStore(...)` calls a hook from a function not named `use*`, so ABO's lint/compiler gate rejects it | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-23 | MINOR | 03's reserved module IDs don't cover the shell's own top-level routes | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-24 | MINOR | 07's "Objections to 03" is stale | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-25 | MINOR | The SPA handler's place in the route chain (permission, rate class) is unspecified, and R6 or the rate limiter would trip on it | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-26 | MINOR | The peer-dependency list is incomplete, and how packages are compiled is unstated | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-27 | MINOR | The schema-driven settings forms that 07 relies on are not designed anywhere | Accepted | R-G | Reviewer's concrete fix applied as written. |
| FE-28 | MINOR | The `api.ts` migration ignores the seven sibling hand-written service files, and the module client is called a "package" in an app with no p | Accepted | R-G | Reviewer's concrete fix applied as written. |
| SIMP-1 | MAJOR | 03 §3.3, §3.4 / The promotion rule is circular: whatever 03 §3.3 lists counts as "platform". Several packages have no consumer during S0–S11 | Accepted (modified) | R-A, R-F, R-G | Promotion rule made non-circular: a package is built in core only when (a) ABO consumes it during S0–S11 or (b) core itself needs it. `acquire`, `arrfacade`, `integrations/indexer` keep their *contracts* in the spec but are marked **deferred-build** (built when the first TV/Movies app starts); the plan builds none of them. |
| SIMP-2 | MAJOR | 05-http-api §8, 09 S10, N7 / Retiring gin couples a router swap, a path rename, OpenAPI typing and a new TS client, and the spec gives no co | Accepted (modified) | R-D, R-H, R-A | Core `httpapi` stays stdlib `net/http` (N7 kept: core gains no gin dependency). ABO's gin router is mounted through `httpapi.MountLegacy` **permanently**; gin retirement (old S10) is removed from the extraction; a books route group moves to native `httpapi` only when it is rewritten for its own reasons. Trade: ABO keeps gin indefinitely; core is unaffected. |
| SIMP-3 | MAJOR | 05-config §1.12 st.2–3, 05-logging §11 st.3 / Removing ABO-side globals (about 4,000 call sites) is justified only by multi-app, which is th | Accepted (modified) | R-E, R-H | R5 (no globals) binds **core** only. ABO's `config.AppConfig` and logger shim survive as books-module façades over core handles; the 676-site / 342-file migrations become optional ratchets, not extraction stages. Multi-app (lowest priority) is not ruled out: the façades are per-App values held by the books module. |
| SIMP-4 | MAJOR | 05-config §1.12/§2.2, 05-metadata M1, 05-download §7, 05-notify §10, 06 Deps, 09 S6/S7 / Three overlapping stores for integration config and | Accepted | R-F, R-E, R-G, R-H | One store for integration instances: `integrations` owns instance records (kind, instance, settings, `secrets.Ref`s). `config` holds no integration instances. All files (05-config, 05-metadata, 05-download, 05-notify, 06, 09) point at it. |
| SIMP-5 | MAJOR | 05-events, 05-notify §3, 05-realtime §4.7, 06 Registrar / Four topic catalogs, two of them with their own permission declarations and diverg | Accepted | R-C, R-F, R-D, R-G | One topic catalog: `events` topics declared once (via the manifest); `notify` and `realtime` reference event topics and their declared permission; no second catalog. |
| SIMP-6 | MAJOR | 05-tasks §5T.3.8/§5T.10, 05-backup, 05-updater, 05-download, 05-ai, 05-health, 05-config, 09 S5 / The "one timer" claim is false. Schedules  | Accepted | R-C, all | One schedule mechanism: `tasks.Schedule` on the definition, with its interval bound to a declared config key by the standard `tasks.ScheduleFromConfig(key)` helper; no bespoke timers in backup/updater/download/ai/health/config. |
| SIMP-7 | MAJOR | 02 Q5, 05-http-api §4.6, 05-config §1.7, 05-metrics M6, 09 §9.2.1/§9.2.7 / The compatibility window is defined 4 times. An httpapi alias tur | Accepted | R-H, R-A, R-D, R-E | Compatibility window defined once, in 09 §9.2.1, implemented once in `compat`; httpapi aliases, config aliases and metric aliases reference it (no wall-clock sunset, see OPS-10). |
| SIMP-8 | MAJOR | 05-http-api §4.3–§4.5, §4.2 st.13 / An API-polish bundle on every route: cursor+HMAC paging by default (breaks UI page counts), an idempoten | Accepted (modified) | R-D, R-G | Offset pagination stays the default (UI page counts); cursor paging is opt-in per route for unbounded feeds; `Idempotency-Key` only on routes that create tasks or external side effects; RFC 9457 errors kept for native routes only. |
| SIMP-9 | MAJOR | 05-backup B3/§5B.3.3, 05-config §2.4 / Default backup encryption, an install identity and the keyring inside the archive form a circular des | Accepted | R-B, R-E | Backup encryption is opt-in on upgrade (default on for fresh installs only after the operator exports the backup identity); keyring is never stored only inside the archive it protects. Resolves DATA-22 together with retention rules. |
| SIMP-10 | MAJOR | 05-notify §1, §3.1, §4, §7 / New product features with zero consumers: inbox, per-user subscriptions, personal channels (on by default), tem | Accepted | R-F | Notify v1 = contract + mail + webhook adapters + global routing. Inbox, per-user subscriptions, personal channels and templating are deferred. |
| SIMP-11 | MAJOR | 05-ai §1, §4 items 5–6, §7 / The AI usage ledger, monthly token cap and batch usage accounting are new features. ABO is the only AI consumer | Accepted | R-F | AI usage ledger and monthly cap deferred; v1 records usage metrics only. |
| SIMP-12 | MAJOR | 05-download §1, §4.1, §4.3, §4.5 / Phase 1 builds Grab, pool selection, the monitor and outbox, pending recovery and MarkImported, but "ABO  | Accepted | R-F | Download phase 1 = contract + Deluge adapter + what ABO uses today; grab/monitor/pending-recovery/MarkImported are the deferred `acquire` build (see COR-3). |
| SIMP-13 | MAJOR | 03 §3.6, 05-metrics §4/§13, 09 §9.2.7 / Renaming 73 books metric families buys nothing. Three files disagree on the app prefix and on the co | Accepted | R-E, R-A, R-H | Books metric names unchanged (no rename, no alias layer for them). New core metrics use `mediacore_*`. |
| SIMP-14 | MAJOR | 05-config §1.10 vs 05-http-api §1.1/§7 / Two contradictory JSON Schema reflector decisions: in-house ~400 LOC vs `google/jsonschema-go`. | Accepted | R-E, R-D | One JSON Schema reflector, owned by `config`; 05-http-api reuses it. Reviser verifies `github.com/google/jsonschema-go` (licence, latest stable, maintenance); if acceptable it is used, else in-house; both files state the same choice. |
| SIMP-15 | MAJOR | 05-config §1.12, §2.10 vs 09 S3/C3 / The config and secrets migration is specified twice with contradictory semantics. | Accepted | R-E, R-H | Config/secrets migration specified once (05-config owns mechanism; 09 S3 references it). |
| SIMP-16 | MINOR | 03 `entity`, 05-download/notify/metadata/08, 05-storage, 05-rbac, 05-tasks / Five spellings of one `{Module, Kind, ID}` reference type. | Accepted (duplicate of COR-17) | all | Same. |
| SIMP-17 | MINOR | 05-tasks §5T.3.2, 05-storage §5S.3.6, 10 / `Fixed` and `IO` are the same thing; 10 uses a `tasks.Network` that does not exist; storage `Shar | Accepted | R-C, R-B, R-F | Reviewer's concrete fix applied as written. |
| SIMP-18 | MINOR | 09 §9.2.2 vs 05-storage §5S.3.7 / The compat-level guard is a second copy of the storage-format guard (stamp + sidecar + flag + script). | Accepted | R-B, R-H | The compat-level guard and the storage-format guard are merged into **one** mechanism (owned by 05-storage, policy in 09): a single stamped level + sidecar + flag + rollback-script check. |
| SIMP-19 | MINOR | 05-config §2.3–§2.7 / Keyring rotation, prune and the backup-manifest coupling ship in v1, but rotation does not exist today. | Accepted (modified) | R-E | Keyring with key IDs ships (needed so the ciphertext format never changes); rotation/prune tooling is deferred. |
| SIMP-20 | MINOR | 05-rbac §7.4, §7.6 / The audit hash chain gives no protection in the spec's own threat model. | Accepted | R-D | Audit hash chain dropped (no protection in the threat model); also resolves COR-15. Audit is an append-only keyspace with retention. |
| SIMP-21 | MINOR | 05-tasks §5T.3.1 / The `Pipeline` shape has zero production definitions (v3 has zero native definitions at all). | Accepted | R-C | `Pipeline` dropped from v1 tasks API. |
| SIMP-22 | MINOR | 06 §6.4.2, 05-http-api §4.9 / Multi-app residue in the SDK: `DependsOn`, `MinCoreVersion`, generalized `PrefixClaim`. | Accepted (modified) | R-G | `DependsOn` dropped (no consumer). `MinCoreVersion` kept with the semantics fixed in MOD-12. Generalized `PrefixClaim` narrowed to what books needs. |
| SIMP-23 | MINOR | 05-authn §3.3, §4.8 / The `auth/clientcert` authenticator has no consumer. | Accepted | R-D | `auth/clientcert` dropped; the mTLS bridge stays in ABO. |
| SIMP-24 | MINOR | 10 §10.5.3, 08 §8.6.2 / About 570 lines of `acquire` Go API and about 215 lines of wire DTOs, for work that D5 limits to proof depth. | Accepted (modified) | R-F, R-G | `acquire` and facade wire DTOs trimmed to proof depth: interfaces and key types stay; exhaustive DTOs removed. |
| SIMP-25 | MINOR | 07 §7.19 step 3, 13 E4 / The react-router 8 migration (121 files) is bundled into the shell stage instead of preceding it. | Accepted | R-G | react-router 8 migration precedes the shell stage (also FE-2). |
| SIMP-26 | MINOR | 03 §3.7 / The diagram says "container"; 03 §3.3 and 06 A2 say there is no DI container. | Accepted | R-A | Reviewer's concrete fix applied as written. |
| SIMP-27 | MINOR | 05-http-api §7, 07 §7.8 / `books-legacy.json` has no consumer, and the "Go emitter fallback" is an unbounded in-house code generator. | Accepted | R-D, R-G | Reviewer's concrete fix applied as written. |
