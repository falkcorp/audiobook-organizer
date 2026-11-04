<!-- file: docs/plans/platform-core/reviews/spec-round-1-data-migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: dd199274-ace3-417d-8ece-646bc0f44f79 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 1 — lens DATA, STORAGE AND MIGRATION (`DATA-*`)

Antagonistic review of `docs/specs/platform-core/` (03 v1.2.0 normative). Every code anchor is at
commit `748fd787a`; spec anchors are `file §section (l.N)` in the current draft. Focus files:
05-storage, 05-backup, 05-tasks (persistence), 05-authn (session storage), 05-config (override
storage), 09, 04. User decisions D1–D9 are not attacked.

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 4 | DATA-1, DATA-3, DATA-19, DATA-22 |
| MAJOR | 23 | DATA-2, 4–18, 20, 21, 23–27 |
| MINOR | 3 | DATA-28, DATA-29, DATA-30 |

| ID | Sev | Title | Primary location |
|---|---|---|---|
| DATA-1 | BLOCKER | `setting:`/`preference:`/`pref:` are not config; adopting them breaks books writers | 05-storage §5S.9.1–§5S.9.2 |
| DATA-2 | MAJOR | Legacy-family ownership is specified four different ways | 05-storage §5S.9.1 vs 05-tasks, 05-logging, 05-config, 09, 04 |
| DATA-3 | BLOCKER | 05-storage adoption has no reconcile: rollback writes lost, revoked credentials return | 05-storage §5S.9.2 step 4 |
| DATA-4 | MAJOR | 09's "legacy wins" reconcile is lossy (narrows every API key used on the old build) | 09 §9.2.3 |
| DATA-5 | MAJOR | Mirror writes primary rows only; old build resolves users/keys/sessions via `idx:` rows | 05-storage §5S.9.2, 09 §9.2.3 |
| DATA-6 | MAJOR | One-shot imports (tasks, clocks, config blob) have no rollback-return path | 05-tasks §5T.6, 05-config Part 1 migration, §2.10 |
| DATA-7 | MAJOR | Storage format vs compat level vs `Breaking` steps: three window-end mechanisms | 05-storage §5S.3.5, §5S.9.2 step 5; 09 §9.2.2, §9.5 |
| DATA-8 | MAJOR | Books' 66 migrations cannot be `MigrationTx` steps; S6/G2 atomicity is false for them | 05-storage §5S.3.5, §5S.9.3 |
| DATA-9 | MAJOR | Cross-keyspace ordering cannot express 09's dependency; 06 contradicts checkpoint rule | 05-storage §5S.3.5 Order; 06 §6.5.1 |
| DATA-10 | MAJOR | Pre-migration checkpoint pruned by crash loop, no restore tool, wrong filesystem | 05-storage §5S.3.5, §5S.7 |
| DATA-11 | MAJOR | Lifecycle ratchets the Pebble format before taking the checkpoint | 05-storage §5S.4 |
| DATA-12 | MAJOR | Data-migration resume not exact; `DataMigration` has no compat class | 05-storage §5S.3.6 |
| DATA-13 | MAJOR | Online copy vs owner writes is a lost-update race; no lock/CAS API | 05-storage §5S.6, §5S.9.2 step 3 |
| DATA-14 | MAJOR | Dry run is unfaithful and unavailable for BootBlocking migrations | 05-storage §5S.3.6, §5S.4 |
| DATA-15 | MAJOR | `opstate:` is live; `:params` suffix not expressible; ReadOnly forbids retention deletes | 05-storage §5S.9.1–§5S.9.2; 05-tasks §5T.6 |
| DATA-16 | MAJOR | Overlap rule 3 makes ABO's own 89 nested family pairs fail boot | 05-storage §5S.3.4 |
| DATA-17 | MAJOR | `LegacyRange` reads are unchecked | 05-storage §5S.3.4 |
| DATA-18 | MAJOR | Cross-owner atomic writes impossible (admin user merge) | 05-storage §5S.3.2; 09 S4 |
| DATA-19 | BLOCKER | `Sequence` as specified can reissue committed IDs after a crash | 05-storage §5S.3.2; 05-events §5E storage |
| DATA-20 | MAJOR | One install-wide storage-format constant couples all apps to books' format changes | 05-storage §5S.3.7 |
| DATA-21 | MAJOR | 05-backup and 09 S8 specify incompatible archive formats | 05-backup §5B.3.3; 09 S8 |
| DATA-22 | BLOCKER | Encrypted-by-default on upgrade + retention ⇒ no backup survives host loss | 05-backup B3, §5B.3.3, §5B.10 |
| DATA-23 | MAJOR | Boot-phase restore can leave the app down with no revert; runs after config load | 05-backup §5B.3.4; 06 §6.5.1 |
| DATA-24 | MAJOR | Backup catalogue and audit trail live inside the DB being restored | 05-backup §5B.3.2, §5B.3.5 |
| DATA-25 | MAJOR | Derived-index acceptance cannot detect stale/ghost documents | 05-backup §5B.3.1; 05-storage §5S.3.8–§5S.3.9 |
| DATA-26 | MAJOR | `keys.Escape`/`Join` are not order-preserving; `ValidSegment` rejects `Escape` output | 05-storage §5S.3.3 |
| DATA-27 | MAJOR | `core.tasks.adopt-active` can fail boot permanently on a normal install | 05-tasks §5T.6 |
| DATA-28 | MINOR | Claim registry / bookkeeping lifecycle gaps | 05-storage §5S.9.1, §5S.3.5; 06 P2; 09 S0, S2 |
| DATA-29 | MINOR | Factual slips and a missing Batch hook required by 05-events | 05-storage §5S.9, §5S.13; 05-events |
| DATA-30 | MINOR | Multi-app store merge is not "copy keyspaces" | 11 §11.5 |

---

## Findings

### DATA-1 — BLOCKER — `setting:`, `preference:`, `pref:` are not config; adopting them into `core/config` breaks books writers and copies library-scale book state

- **Where:** 05-storage §5S.0 S4 (l.25), §5S.9.1 "Config" and carve-out rows (l.727-728), §5S.9.2 settings/preferences rows (l.760-761), §5S.9.4 step 3 (l.830).
- **Evidence:**
  - `setting:` is the general runtime-marker store, not the config store: 56 non-test `SetSetting` call sites. Books code writes scheduler clocks `scheduled_task_last_tick.<task>` (`internal/scheduler/interval_clock.go:50,137`), `library_scan_full_last_run` (`internal/scheduler/full_sweep.go:37,117`), the pause gate (`internal/operations/registry/pause_gate.go:52,260`), scan stand-down (`internal/operations/registry/scan_standdown.go:571`), index-built flags (`internal/database/pebble_store_lsh.go:58`, `pebble_store_isbn_index.go:69`, `pebble_store_fpwinidx.go:78`), backfill-done flags (`internal/plugins/dedup/*.go`, `internal/server/embedding_backfill.go:269`), the bootstrap token hash (`internal/server/bootstrap.go:158`), and one row **per file path** `transcode_skip_<hash>` (`internal/remux/transcode.go:113-116,263`). `internal/server/handlers/operations/handler.go:392` writes an arbitrary request-supplied key. Only `setting:repairs_last_*` are carved out.
  - `preference:` holds per-book state: `preference:metadata_state_<bookID>` (`internal/metastate/metastate.go:34`), read and deleted by books (`internal/database/metadata_field_locks.go:147,302`); global prefs take IDs from `counter:preference` (`internal/database/pebble_store_preferences.go:49`), a books-claimed counter. "global: tens" (l.761) is wrong.
  - `pref:` holds per-user books data moved by the user merge (`internal/database/user_merge.go:241,425`) and the `_system` write-back outbox.
  - Strict claims (§5S.9.4 step 3) refuse every one of these books writes with `ErrOutsideKeyspace`; shadow mode would instead copy them into `c/config/` and dual-write them for the window.
  - 09 §9.6 row 26 (l.469) and S5 (l.351) say these families stay **books** claims permanently — 05-storage contradicts 09.
- **Fix:** Remove `setting:`, `preference:`, `pref:` from core adoption in 05-storage §5S.0/§5S.9.1/§5S.9.2/§5S.9.4; make them books `ClaimReadWrite`. Core claims only what it takes over, by exact key or narrow prefix: `preference:storage_format`/`storage_migration` (`core/storage`), the 4 secret rows `setting:<name>` (`core/secrets`, mirror phase per 09 S3), `scheduled_task_last_tick.` + `library_scan_full_last_run` + `maintenance_window_last_run` (`core/tasks`, adopted into `c/tasks/sched/` per 05-tasks l.773), `registry.operations_paused` (`core/tasks`), bootstrap keys (`core/auth`). Core DB config overrides are **new** rows in `c/config/` produced by the blob import (DATA-6), not an adoption of `setting:`. Recompute the S4 counts and the 258 arithmetic.

### DATA-2 — MAJOR — Ownership of legacy families is specified four different ways

- **Where / evidence:**

  | Family | 05-storage §5S.9.1 | Conflicting spec |
  |---|---|---|
  | ops v1/v2/v3 (42) | `core/tasks` | 05-tasks §5T.6 l.761 agrees; 09 S5 l.351 and §9.6 row 26: **books** (`books/legacyops` adapter, D25 mirror preserved, "stored rows never rewritten") |
  | `act:` (+12) | `core/log` + `core/audit` | 03 §3.3 `log` row (l.108) and 05-logging L5/§5 (l.23, l.247): **books**; 04 `internal/database` split row moves the activity store to `mediacore/internal/activitystore` |
  | `guestcftoken:` | `core/auth` | 09 S4 l.335 "Stay in ABO: … guest tokens (`guestcftoken:`)" |
  | `idx:` carve-outs | `idx:user:`, `idx:invite:`, `idx:role:`, `idx:sess:`, `idx:apikey:`, `idx:oauth:` | 09 §9.2.3 l.127: `idx:role:name:`, `idx:sess:user:`, `idx:invite:username:` … |
  | `sess:` | adopt (transform) + dual-write (l.758) | 05-authn §11 l.722: not copied, read fallback, mirror "owner decides"; 09 S4: raw mirror |
  | `keyfamilies.go`, `census*.go` | books generated claim list (l.742-747) | 04 `internal/database` row: → `mediacore/storage` (audiobook key names in core, against R1/§3.4) |
  | config blob → `c/config/` | (implicit adoption of `setting:`) | 05-config l.534: migration "owned by the books module" — but books holds no `c/config`/`c/secrets` handle (S1: "holding the handle *is* the permission") |

  A per-tier split of `act:` is not even expressible as claims: the index families `act:op:`, `act:bk:`, `act:src:`, `act:typ:`, `act:lvl:` index primaries of **every** tier (`internal/database/pebble_activity_store.go:231,442,518`), so an audit-tier row and its index rows would have different owners.
- **Fix:** Put one normative ownership table in 05-storage §5S.9.1 (one owner + mode per family/sub-prefix) and make 04, 05-tasks, 05-logging, 05-authn, 05-config, 09 reference it instead of restating. Recommended resolution: `act:` books (core/audit gets new rows only); `guestcftoken:` books; a single `idx:` carve-out list (09's narrower one); `sess:` per the owner's 05-authn decision; blob import owned by kernel `configstore`, not books; ops families: pick either 09's model (books claim, core reads through an injected run store) or 05-tasks' (core claim with books carve-outs for live writers, DATA-15) and delete the other. The claim generator emits exactly this table; `keyfamilies.go` stays ABO.

### DATA-3 — BLOCKER — 05-storage's adoption has no reconcile: rollback writes are lost and revoked credentials come back

- **Where:** 05-storage §5S.9.2 step 4 (l.776-778, "Reads use only the new rows"); no reconcile anywhere in 05-storage; tests list only `TestAdoptionDualWriteLegacyReadable` (l.855).
- **Evidence:** the previous release writes only legacy rows (`internal/database/pebble_store_auth.go:132-190` users, `:1100-1131` API keys, `:1651-1675` sessions). After roll-forward → rollback → roll-forward, the adoption step is already recorded, so nothing re-reads legacy: a user deactivated, a password reset (`CredentialEpoch` bump) or an API key revoked on the previous release stays valid in `c/auth/`. 09 §9.2.3 (l.155-163) names exactly this hazard ("a deactivated user could log in again") and requires `Reconcile` every boot, and says 05-storage owns the final API — 05-storage does not carry it.
- **Fix:** Add a normative mirror/reconcile contract to 05-storage §5S.3 and §5S.9.2: native rows record the mirrored legacy image (see DATA-4); `Reconcile` runs synchronously every boot while the claim is `ClaimMigrating`, before listeners, and handles legacy-changed, legacy-deleted (delete native) and legacy-new (adopt — 09 omits this case); deletes before adopts so unique indexes cannot collide (user "bob" deleted and recreated with a new ULID on the old build); reconcile failure = boot error. Tests: `TestReconcileAdoptsRollbackWrites`, `TestReconcileDeletesRollbackDeletes`, `TestReconcileAdoptsRowsCreatedOnRollback`.

### DATA-4 — MAJOR — 09's reconcile ("legacy wins, re-adopt") is lossy and narrows every API key used on the previous release

- **Where:** 09 §9.2.3 l.132-158 (`Mirrored[T]` with a `Reverse` projection; "legacy wins and is re-adopted"), §9.2.6 (reverse mapping drops canonical-only atoms), §9.8 l.504.
- **Evidence:** the mirrored legacy row is a lossy projection. The previous release rewrites whole rows on routine traffic: every API-key request calls `TouchAPIKeyLastUsed` (`internal/server/middleware/auth.go:382`), which rewrites `apikey:<id>` (`internal/database/pebble_store_auth.go:1284-1297`); Go decode→modify→encode also strips any JSON field the old struct lacks. Hash mismatch → the projection is re-adopted → the native key loses canonical scopes / `LegacyFullScope` / `PublicID` permanently. Same for users touched on login.
- **Fix:** Specify reconcile as a field-level three-way merge: base = the legacy image recorded in the native row at mirror time (store bytes, not only `LegacyHash`), theirs = current legacy row, ours = native row. Apply only fields the legacy build can change (password hash, status, credential epoch, last-used/use-count, roles/atoms forward-mapped through §9.2.6 expansion, deletion); never overwrite native-only fields. Test `TestReconcileKeepsCanonicalScopesAfterTouch`.

### DATA-5 — MAJOR — The mirror writes primary rows only; the previous release finds users, keys and sessions through `idx:` rows nobody mirrors

- **Where:** 05-storage §5S.9.2 step 4 ("every write goes to the new row and to the legacy row"); 09 §9.2.3 `Mirrored[T]` has one `Legacy Codec[T]` (`u:<id> ↔ T`).
- **Evidence:** legacy lookups go through index rows: username `idx:user:username:` (`pebble_store_auth.go:142,173`), email `idx:user:email:` (`:67,178`), key by hash `idx:apikey:hash:` (`:1131,1158-1159`), keys by user `idx:apikey:user:` (`:1126,1178`), sessions by user `idx:sess:user:` (`:1670`), plus `idx:oauth:user:`/`idx:oauth:sub:`, `idx:invite:username:`, `idx:role:name:`. A key created on the new release with only `apikey:<id>` mirrored is not found by `GetAPIKeyByHash` on the previous release; a new user cannot log in by username; deletes leave dangling index rows. Also 13 l.129 makes `encoding/json/v2` the codec for new stored values — mirror rows and legacy read adapters decoded/encoded with v2 defaults (case-sensitive names, `omitzero`, nil-slice encoding) silently differ from what the old build expects.
- **Fix:** Mirror contract = `LegacyProjection(T) []KV` (primary row **and** every legacy index row) plus `LegacyDeletes(T) [][]byte`, written in the same batch; reconcile re-derives native indexes. Legacy codecs (mirror projections, read-in-place adapters) are v1 `encoding/json` permanently; v2 only for native rows. Tests: `TestMirrorLegacyIndexesResolve` (create on cur, resolve by username/hash with the prev build's store code), golden decode of HEAD fixtures.

### DATA-6 — MAJOR — One-shot imports outside auth have no rollback-return path: work and config written on the previous release are stranded

- **Where:** 05-tasks §5T.6 l.770-773 (`core.tasks.adopt-active` once; clocks adopted once; D25 mirror ends at the copy); 05-config l.534-541 and §2.10 l.843-847 (blob imported once, kept "untouched for one release", secrets blanked in it); 09 S3 l.326 assumes "reconcile re-adopts secrets changed on S2".
- **Evidence (roll-forward → rollback → roll-forward):** runs enqueued, interrupted or `awaiting_decision` on the previous release sit in `opv2:act:` and are only read as terminal history (`legacyhist`, read-only) — neither executor resumes them; clocks advanced in `setting:scheduled_task_last_tick.*` are ignored by `c/tasks/sched/` → tasks double-run or skip; config/secret edits made in the previous release land in `config_blob` and are never re-imported; secrets re-entered there are plaintext in the blob and never imported. "Kept for one release" is also shorter than 09's window W.
- **Fix:** For each one-shot import specify a boot-time return path: tasks — every boot adopt `opv2:act:` runs lacking `migrated_to` (same copy-and-mark) and adopt clocks newer than `c/tasks/sched/<kind>.last_due`; config — keep writing the blob in mirror phase (09 S3 model) and at boot re-import keys whose blob value differs from the recorded import hash, re-importing and re-blanking plaintext secrets; blob retention = W, not "one release". Add each case to the R-test (09 §9.10).

### DATA-7 — MAJOR — Three incompatible "end of window" mechanisms: storage format, compat level, `Breaking` keyspace steps

- **Where:** 05-storage §5S.9.2 step 5 (l.779-781, contract "ships in a release that raises `SupportedStorageFormat`"), §5S.3.5 (`Compat`, `Format` required for `Breaking`, l.359-369), §5S.3.7; 09 §9.0 l.23 ("this sequence never raises `SupportedStorageFormat`"), §9.2.2 (`c/compat/required_level`, `RaiseCompat`), §9.5 l.437-438 (contract deletes in ≤ 10 000-key batches); 03 §3.3 `compat` row puts the guard in package `compat` (L4), 09 in package `storage`.
- **Evidence:** a storage-format raise makes the previous build refuse an *older* store too (serve mode refuses `effective < build`, `internal/database/pebble_store.go:675`), which is why 09 needed a separate guard. 05-storage never mentions `c/compat/required_level`, `.compat-level`, `--print-compat-level` or `RaiseCompat`. A 09 contract migration is neither a `MigrationTx` step (one batch ≤ 64 MiB) nor a `DataMigration` (only `RaisesFormat`).
- **Fix:** In 05-storage: define the compat-level guard in `storage` (key, sidecar, fast-exit flag, open-time check after the format check); window-end/contract steps raise **compat level**, never storage format (format only for physical changes); `Breaking` requires `RaisesCompat: N`; add `RaisesCompat int` to `DataMigration` with stamp+sidecar-before-first-delete ordering so contracts are BootBlocking data migrations; `ErrKeyspaceTooNew` remains a per-keyspace secondary check. Amend 03 §3.3 `compat` row to point at `storage` for the guard.

### DATA-8 — MAJOR — Books' 66 migrations cannot be `MigrationTx` steps; the S6/G2 atomicity claim is false for them

- **Where:** 05-storage §5S.3.5 (`Up func(ctx, *MigrationTx)`, l.361; Atomicity row l.391), §5S.9.3 l.814 ("books' 66 migrations stay ABO code registered as books `Migration`s … `Compat: Additive`").
- **Evidence:** `MigrationFunc func(store migrationStore) error` (`internal/database/migrations.go:87`) writes through store methods that commit their own batches; several are library-scale repairs (063 junction book-IDs, 064/065 backfills, 066 plan-row strip; `migrations.go:487-506`); store methods allocate IDs via `nextID` with its own `pebble.Sync` write (`internal/database/pebble_store.go:966-990`). Inside a 64 MiB / 250 000-op `MigrationTx` they are either refused (`ErrMigrationTooLarge` for an install upgrading from < 63) or must all be rewritten.
- **Fix:** Add a `LegacyMigration` kind (books only): `Up func(ctx, LegacyRange) error`, explicitly non-atomic, crash safety by the existing idempotency test (`TestMigrationUpFunctionsAreIdempotent`), bookkeeping through the version-source adapter after `Up`; mandatory pre-migration checkpoint; a ratchet forbids new ones. Reword S6/G2 mapping: "one atomic batch per step, except books legacy steps 1..66".

### DATA-9 — MAJOR — Cross-keyspace ordering cannot express what 09 needs, and 06 contradicts the checkpoint rule

- **Where:** 05-storage §5S.3.5 Order row (l.389: core keyspaces "in R4 layer order", then modules); 09 S4 l.336 ("core migration `core.auth#1` after books track ≥ 66"); 03 R4 places auth, rbac, audit, tasks, backup all in L4 (so layer order is not an order); 05-storage calls `storage` layer 2 and `tasks` layer 3 (l.81, §5S.12) vs 03 L1/L4; 06 §6.5.1 P4 ("for each enabled module: checkpoint, run its pending migrations") vs 05-storage one checkpoint per boot (§5S.11 item 5); `DataMigration.Barrier` (l.414) needs steps 8 and 9 of §5S.4 interleaved, but the lifecycle runs all versioned steps first.
- **Fix:** Replace the Order row with a DAG: `Migration`/`DataMigration` may declare `After []StepRef{Owner, Version}` across keyspaces (e.g. `core.auth#1 After books#66`); the runner topologically sorts all pending steps (ties: an explicit core subsystem order list, then module topo order, then version); BootBlocking data migrations are DAG nodes so `Barrier` works. Define module behaviour when an Online migration's `Barrier` holds its keyspace below the build's latest version (`MinVersionToServe`; below it the module is not started, health degraded). Align 06 P4 to one checkpoint per boot and the layer numbers to 03.

### DATA-10 — MAJOR — The pre-migration checkpoint is pruned by a crash loop, cannot be restored by any tool, and may land on the wrong filesystem

- **Where:** 05-storage §5S.3.5 checkpoint row (l.393), Failure row ("the checkpoint is the rollback", l.394), §5S.7 `checkpoint_keep` "int (boots) 3" (l.685).
- **Evidence:** the shipped unit restarts on failure every 5 s with burst 5/60 s (`deploy/audiobook-organizer.service:81-82,111-112`). Boot 1 checkpoints the pristine store and fails at step N; boots 2–4 each take a new checkpoint (steps < N committed) and keep=3 deletes the pristine one within ~20 s. No procedure restores a checkpoint (`make rollback` swaps binaries; 05-backup `StageRestore` takes archives). `<data-dir>/.checkpoints/` with "same device enforced": ABO's DB path is a free flag (`cmd/root.go:487`; Docker `--db /data/audiobooks.pebble`, `Dockerfile:104`) and ABO's backup staging deliberately sits beside the DB (`internal/backup/backup.go:569-571`); if data-dir and DB are on different devices every upgrade with a pending step halts boot.
- **Fix:** (1) Key checkpoints by the pre-boot version vector: reuse an unconsumed checkpoint with the same "from" vector instead of taking another; prune only after a boot that completed every step and served for `core.storage.migration.checkpoint_min_age` (e.g. 7 d). (2) Place checkpoints beside the DB (`<db-parent>/.mediacore-checkpoints/`). (3) Add an offline `<app> storage restore-checkpoint <name>` that goes through the 05-backup boot-phase swap (sidecars, derived-index invalidation). (4) Free-space precheck sized to the DB (hard links diverge as compaction rewrites).

### DATA-11 — MAJOR — The lifecycle ratchets the Pebble format before taking the checkpoint

- **Where:** 05-storage §5S.4 (l.606-612: step 4 ratchet, step 7 checkpoint, step 9 cut-over whose checkpoint §5S.3.7 l.476 takes even later).
- **Evidence:** Pebble ratchets never go back; ABO's own comment says a ratchet ahead of the checkpoint makes "a pre-migration checkpoint unopenable by the previous build" (`internal/database/storage_format.go:61-80`); today the init phase ratchets with no checkpoint at all (`pebble_store.go:692`), so the hazard is introduced by this design. D8 makes Pebble bumps routine.
- **Fix:** Reorder §5S.4: open without ratchet → format/compat checks → validate specs → **if** (ratchet needed ∨ pending versioned step ∨ pending RaisesFormat/RaisesCompat) take the one checkpoint → ratchet → migrations; the §5S.3.7 cut-over reuses that checkpoint. Test `TestCheckpointPrecedesRatchet` (open the checkpoint at the previous pinned format).

### DATA-12 — MAJOR — Data-migration resume is not exact, and `DataMigration` has no compatibility class

- **Where:** 05-storage §5S.3.6 (l.405-458: `Shards` "partitions Source into disjoint key ranges", `ShardsCPU()`, cursor at `c/storage/dm/<id>/shard/<n>`), §5S.6 iterator-refresh rule (l.657-660), test `TestDataMigrationResumesExactly`.
- **Evidence:** (a) `runtime.NumCPU()` and boundaries computed from the current key distribution can differ on the next boot (container CPU limit, restore onto another host, online writes) → `shard/<n>` cursors refer to different ranges → keys skipped or done twice. (b) Steps that rewrite keys inside `Source` (in-place re-encode, re-key forward) are revisited after the mandatory 30 s iterator re-open, which sees the migration's own commits. (c) No `Compat` field: an Online migration that rewrites legacy rows into a shape the previous release cannot read is caught by no guard (only `RaisesFormat` exists).
- **Fix:** Persist the shard plan (`{n, boundaries}`) in `c/storage/dm/<id>` at first start and reuse it on resume; require `Step` to be idempotent for its own outputs and to count `already-current`; forbid writes into the unvisited part of `Source` unless the migration declares `RewritesInPlace` (runner then materialises a chunk's keys before writing). Add `Compat` (Additive/Breaking) to `DataMigration`; Breaking ⇒ BootBlocking + RaisesCompat/RaisesFormat.

### DATA-13 — MAJOR — Online copy vs owner writes is a lost-update race; Pebble gives no isolation and the runner gets no lock

- **Where:** 05-storage §5S.6 l.663-665 ("the owner's dual-write path takes the same per-key stripe lock … the converter re-reads inside the indexed batch"), §5S.9.2 step 3 (over-cap online adoption, new-first reads).
- **Evidence:** an indexed batch reads the live DB (no snapshot isolation) and Pebble has no compare-and-set; `core/internal/storagemigrate` has no access to owner locks, which in ABO are ad-hoc mutexes (`inviteMu`, `apiKeyMu`, `sessionWriteMu`: `pebble_store_auth.go:136,760,1285,1697`; `user_merge.go:508-528`). Interleaving: copier reads legacy v1 → owner commits native v2 + legacy v2 → copier commits native v1 → v2 lost (e.g. a password change during the copy). Holding up to `chunk_items` (2 000) stripe locks across one commit invites deadlock with multi-stripe owner writes.
- **Fix:** Add a core-owned striped lock to the storage API (`Keyspace.Lock(keys ...[]byte) (unlock func())`, sorted acquisition) used by owner writes and by `StepWriter`; give `StepWriter` `SetIfAbsent` / `SetIfUnchanged(key, expect, value)` evaluated under that lock at commit. Adoption copy rule: never overwrite an existing native row (owner writes win). Per-item commits for online copies of locked families.

### DATA-14 — MAJOR — Dry run is neither faithful nor available for the migrations that most need it

- **Where:** 05-storage §5S.3.6 l.453-456 (each chunk written into an indexed batch that is discarded), §5S.4 steps 9 and 12, test `TestDataMigrationDryRunWritesNothing`.
- **Evidence:** discarding per chunk hides earlier chunks' writes from later ones (index builds, dedup maps, counters, `already-current` detection), so counts diverge from a live run; `Verify` cannot run; `Step` is a closure that can capture the module's `Keyspace`/`LegacyRange` and write outside `StepWriter`; BootBlocking migrations (cut-over, contracts) run at step 9 before the task executor exists (step 12), so `tasks` preview cannot reach them.
- **Fix:** Define dry run as "real runner against a throw-away checkpoint": offline `<app> storage migrate --dry-run [--id]` (and a preview task for Online ones) checkpoints into scratch on the DB's filesystem, runs every step incl. `Verify`, reports counts/held/bytes, deletes the scratch. Stray writes become harmless and cross-chunk state is faithful. Keep the in-batch mode only as a labelled "estimate".

### DATA-15 — MAJOR — `opstate:` is live, not write-dead; a `:params` suffix cannot be claimed; `ClaimReadOnly` forbids the retention deletes the spec relies on

- **Where:** 05-storage §5S.9.1 l.738-740, §5S.9.2 ops v1 row l.762 ("write-dead … `ClaimReadOnly` except `opstate:*:params`"), §5S.3.4 claim kinds (prefix or exact key), §5S.5 l.634 (`ErrReadOnlyClaim`), S5 "age out by retention" (l.26), "Retention runs over both" (l.788); 05-tasks §5T.6 l.771 ("Never written. Pruned by `core.tasks.legacy-prune`"), l.777 (helper for `:params` only).
- **Evidence:** `operations.SaveCheckpoint` writes `opstate:<opID>` (`internal/operations/state.go:59-74`) from live callers in `internal/itunes/service/importer.go` (4), `internal/dedup/drain_stale.go`, `internal/maintenance/jobs/recompute_book_aggregates.go`, `internal/server/metadata_ops.go`, `internal/maintenance/jobs/backfill_file_hashes.go`; `internal/metabatch/upgrade.go:162,199` keeps its sweep cursor at `opstate:metabatch:metadata-upgrade-cursor`; the hash-backfill journal is `opstate:<opID>:params` (`backfill_file_hashes.go:730,767`). A suffix carve-out is neither prefix nor exact key. Retention, `legacy-prune` and `DeleteOperationState` (`internal/database/pebble_store_operations.go:338-348`) delete under families declared read-only.
- **Fix:** (1) `opstate:` stays `ClaimReadWrite` for its writers until 05-tasks T2 moves them to `rc.Checkpoint`; list the callers in T2 with a ratchet. (2) Add claim mode `ClaimAgingOut` (reads + `Delete`/`DeleteRange`, `Set` refused) for read-in-place history (ops terminal history, `syslog:`, legacy `act:` if core-owned) and use it wherever retention runs; keep `ClaimReadOnly` for frozen families.

### DATA-16 — MAJOR — Claim overlap rule 3 makes ABO's own nested families fail boot

- **Where:** 05-storage §5S.3.4 rule 3 (l.343-346: overlap legal **only** when the shorter lists the longer in `Except` and "the longer is claimed by exactly one *other* owner"); 03 R8 fails boot on overlap "between owners".
- **Evidence:** 89 nested pairs among the 258 families (computed from `internal/database/keyfamilies.go:37-345`: `book:` ⊃ `book:asin:` …, `act:` ⊃ 12, `opv2:` ⊃ 13, `pref:_system:` ⊃ 6), all same owner; some children need a different `Mode` than the parent (`book:hash:`, `book:organizedhash:`, `book:originalhash:` retired; `book:author:`, `book:series:` dead under read-write `book:`).
- **Fix:** Rule 3: same-owner nesting is legal without `Except`; longest match selects the claim and therefore its `Mode`; `Except` is required only when the longer claim belongs to another owner; cross-owner overlap without it fails boot. Test `TestSameOwnerNestedClaimsSelectMode`.

### DATA-17 — MAJOR — `LegacyRange` reads are unchecked: books keeps reading core-owned rows (stale projections) and can read `c/`

- **Where:** 05-storage §5S.3.4 l.300-314 ("Every write is checked"), S1 ("holding the handle *is* the permission").
- **Evidence:** books reads auth families directly today — user merge scans `idx:apikey:user:`, `apikey:`, `idx:oauth:user:`, `oauthid:`, `invite:` (`internal/database/user_merge.go:241-242,454-486`). After adoption these legacy rows are reverse-mapped projections (09 §9.2.6), so books reads narrowed/stale data; nothing stops `LegacyRange.Get` on `c/secrets/…` or a full-range `Scan` across `c/auth/session/`.
- **Fix:** Claim-check reads (`Get`, `Scan`, `NewIterator` bounds clamped to the holder's claims); add explicit, reviewed `ReadShare` claims for window-only cross-owner reads; census and backup use `storage.Admin`, not `LegacyRange`.

### DATA-18 — MAJOR — Cross-owner atomic writes become impossible: the admin user merge needs one CAS batch across books and adopted auth rows

- **Where:** 05-storage §5S.3.2 (`Bind` only keyspaces the caller holds); 09 S4 l.335 (`internal/usermerge` stays in ABO); 09 risk row for S4 covers journal **undo** only.
- **Evidence:** `ApplyUserMergeOps` commits, after a compare-and-set check under `userMergeApplyMu`/`inviteMu`/`apiKeyMu`, one batch (`internal/database/user_merge.go:507-531`) touching `ubs:`, `upos:`, `bookmark:`, `sync_alias_use:`, `pref:`, `playp:`, `playe:` (books) **and** `u:`, `apikey:`, `idx:apikey:user:`, `oauthid:`, `idx:oauth:user:`, `invite:`, `inviteallow:` (adopted) plus `usermerge:journal:` (`:241-242,421-486,719-722`).
- **Fix:** Specify a batch-participant pattern for core subsystems (same shape as `outbox.Stage`): e.g. `auth.Admin.StageUserMerge(b *storage.Batch, plan)` writes native + mirror rows into the caller's batch under core's own locks and CAS, so books keeps one atomic commit without holding `c/auth/`. Put it in 05-authn; test `TestUserMergeAtomicAcrossOwners` with `CrashClone`.

### DATA-19 — BLOCKER — `Sequence` as specified can reissue committed IDs after a crash

- **Where:** 05-storage §5S.3.2 l.219-225 (lease of 64, "never costs its own fsync", `NextIn(b)` "persists the high-water mark in b"), test `TestSequenceNeverReusesAfterCrash`; 05-events storage table (`c/outbox/meta/seq` "allocator high-water mark").
- **Evidence:** if a lease refill's new high-water mark rides in caller batch b1 while another goroutine takes an ID from that lease and commits b2 first, a crash (or `b1.Close()` without commit) leaves the persisted mark at the old lease end; on restart the same IDs are handed out and the next write overwrites a committed row. Concurrent `NextIn` batches committing out of order also regress the mark (last writer wins on one key).
- **Fix:** A refill is its own `Sync` commit of `hw = max(hw, new)` **before** any ID of the new lease is returned (one fsync per lease); `NextIn` only consumes from a durable lease and writes nothing — or `hw` uses a Pebble merge operator with max semantics. `CrashClone` test at every point between refill and consuming commits; apply the same rule to the outbox seq allocator.

### DATA-20 — MAJOR — One install-wide storage-format constant couples every app (and core) to books' format changes

- **Where:** 05-storage §5S.3.7 (l.462-482: stamp `preference:storage_format` owned by `core/storage`; storage-efficiency release B becomes `books DataMigration{RaisesFormat: 2}`; "registration fails if the build's `SupportedStorageFormat` is lower"), §5S.5 (serve mode refuses older stores).
- **Evidence:** serve mode refuses any store below the build's format (`internal/database/pebble_store.go:675`). When core raises `SupportedStorageFormat` to 2 for books, a TV/Movies app on that core version opening its existing format-1 DB has no `RaisesFormat: 2` migration and cannot boot; books cannot raise its format without a core release (worse after the split, D3). Core would also write the ABO-named key `preference:storage_format` (outside `c/`) into every app's DB.
- **Fix:** Per-owner formats: `c/storage/format/<owner>`; the books module mirrors its value to `preference:storage_format` and the `<db>.storage-format` sidecar for ABO's rollback guard; serve-mode "older" refusal applies only when that owner has a registered pending `RaisesFormat`; `--print-storage-format` prints the vector and `scripts/storage_format_guard.py` compares per owner.

### DATA-21 — MAJOR — 05-backup and 09 S8 specify incompatible archive formats; the 05-backup one breaks restore after rollback

- **Where:** 05-backup B3, §5B.3.3 (l.248-262: `<app>_<ts>_<id>.tar.zst.age`, `manifest.json` first, `storage/main.pebble/…`, artifacts inside, encrypted by default); 09 S8 l.391 (v2 = legacy layout unchanged + `mediacore-manifest.json`, artifacts in a companion archive during W).
- **Evidence:** the previous release's restore sniffs only compression codecs (`internal/backup/codec.go:204-222`) and extracts entries under the target by the DB dir's basename (`internal/backup/backup.go:777-870`); it cannot read an age stream or the `storage/main.pebble/` layout. During every W (09 §9.2.1) an operator who rolls back cannot restore any archive made since the upgrade.
- **Fix:** Adopt 09's staging in 05-backup: until the last window a pre-core binary can roll back into has closed, the default archive is legacy-compatible (legacy layout + manifest entry, compression only, secrets policy per DATA-22); `mediacore-backup/1` (encrypted) becomes default afterwards or is produced alongside; add `<app> backup export --legacy <id>` (decrypt + rewrap). Record this in §5B.10.

### DATA-22 — BLOCKER — Encrypted-by-default on upgraded installs plus retention leaves no backup that survives host loss

- **Where:** 05-backup B3 (l.22), §5B.3.3 l.269-276 (install identity "stored as a 0600 file beside the secrets master key in the credential state directory"; recovery string "shown once in the UI"; operator recipients default empty), §5B.10 (legacy `audiobooks_*` archives listed **and pruned** by retention, l.266), `retention.max_count` 10 (l.422).
- **Evidence:** ABO upgrades are headless (`make deploy`); the first daily `core.backup.create` writes an archive only the on-host identity can decrypt; ~10 runs later count retention deletes the last plaintext legacy archive. Operators who copy the backup dir off-host (the normal disaster-recovery setup) do not copy the credential state dir, so after a disk loss every remaining archive is undecryptable. `secrets/master.key` inside the archive does not help — it is encrypted to the same lost identity.
- **Fix:** Encryption activates only after an explicit acknowledgement (recovery string confirmed, or ≥ 1 operator recipient configured); until then archives use B3's unencrypted mode with secret-class items omitted, and health is `degraded` ("backups not recoverable off-host / not encrypted"). Retention never prunes the newest archive decryptable without the install identity until acknowledged. Add `<app> backup identity export` (0600) and a release note. Test `TestUpgradeDoesNotSwitchToUnrecoverableArchives`.

### DATA-23 — MAJOR — Boot-phase restore can leave the app down with no revert path, and runs after config has already been loaded

- **Where:** 05-backup §5B.3.4 boot steps b–g (l.307-319), `RevertLastRestore` (API only), compatibility step 3 (no compat level); 05-storage §5S.4 step 0; 06 §6.5.1 P0 (config resolved before P2 opens storage).
- **Evidence:** (a) step b renames the live DB first, then config file and master key; a config file on another filesystem, read-only, or bind-mounted as a single file (usual in containers) fails `rename` after the DB has moved, and journal "resume" retries the same rename every boot. (b) Failures after the swap — compat level too new (09 S8 says the guard refuses it "at next boot"), `ErrKeyspaceTooNew`, a failing migration on an older archive — halt boot with live data in `previous/`; `RevertLastRestore` needs a running app. (c) The restore runs inside storage open (P2) but P0 already loaded the config file, so the applying boot runs with pre-restore config. (d) Step d rewrites only the storage-format sidecar, not 09's `.compat-level` sidecar. (e) 05-backup relies on `auth.OnRestore` in 05-authn, which defines no such hook; during the window revocation must also delete legacy `sess:` fallback rows (05-authn l.722) or a rollback resurrects them.
- **Fix:** API phase pre-validates every rename (same device as target, writable, not a mount point) and refuses otherwise; config file restored to `restore/<id>/config.restored` for operator merge unless `core.backup.restore.replace_config=true`; move the boot phase before P0 (kernel pre-boot reading only the data dir from flags/env); add compat level to step 3 and rewrite `.compat-level` in step d; on any open/migration failure after a swap, swap back automatically and boot degraded with the restore marked `failed`; add `<app> restore --revert` CLI; add `OnRestore` (covering legacy `sess:`) to 05-authn.

### DATA-24 — MAJOR — State that must outlive a restore lives inside the DB being restored

- **Where:** 05-backup §5B.3.2 `List` "from the c/backup index; never hashes files" (l.221), `c/backup/archive/<id>` (l.252, l.339), retention floor from recorded verification (§5B.3.5); audit `core.backup.restore.staged` written before the swap (step 5); 05-rbac `c/audit/` chain anchor (l.640-659).
- **Evidence:** after restoring archive X, `c/backup/` reflects X's time: newer archives (including the safety archive taken just before the restore) vanish from `List`, from the byte budget and from the "newest verified" floor; the audit chain loses every record since X, including the staged-restore record.
- **Fix:** The archive catalogue is derived from the backup directory (a `<archive>.manifest.json` sidecar re-read and reconciled into `c/backup/` at boot), never authoritative in Pebble. Restore re-writes its staged/applied audit records after the swap from `restore/<id>/journal.json`; 05-rbac states how audit records newer than the archive are retained (export from `previous/`, or keep audit outside the restored set).

### DATA-25 — MAJOR — Derived-index acceptance cannot detect stale or ghost documents

- **Where:** 05-backup §5B.3.1 Derived row (bleve `CopyTo` "accepted only if mapping version matches and the index reconcile from storage is run"), consistency rule (l.74), boot step e; 05-storage §5S.3.8 dirty set, §5S.3.9 `Load` "must verify against storage truth".
- **Evidence:** the bleve copy is captured after the Pebble checkpoint (§5B.3.5 steps 4 then 5): docs created/updated after the checkpoint are in the copy but absent from Pebble, and the checkpoint holds no dirty mark for them → ghost/stale hits are never reconciled. The same happens after a checkpoint rollback (DATA-10). A truth *count* (hnsw rule, `internal/server/server.go:845-866`) passes when an add and a delete cancel.
- **Fix:** Record a storage generation (e.g. the Pebble sequence number at checkpoint, or a counter bumped in every dirty-mark batch) inside each derived artifact at snapshot time and in the manifest; `Load` accepts only an artifact whose generation equals the restored store's, otherwise full rebuild or full reconcile scan. For backups capture derived snapshots before the checkpoint and re-mark everything touched since their generation.

### DATA-26 — MAJOR — `keys.Escape`/`Join` are not order-preserving, and `ValidSegment` rejects `Escape` output

- **Where:** 05-storage §5S.3.3 l.234-243 (`Escape` "order-preserving: 0x00 → 0x00 0x01, '/' → 0x00 0x02"; segments may not contain 0x00), guidance row "Order-preserving range scans" (l.272).
- **Evidence:** bytes 0x01–0x2E are not escaped: "a b" < "a/b" before escaping (0x20 < 0x2F), but "a\x00\x02b" < "a b" after; same for "a\x01" vs "a/". `Join`'s '/' separator mis-orders siblings: segment "a b" sorts before "a" because ' ' < '/'. `Escape` emits 0x00, which `ValidSegment` forbids.
- **Fix:** Use a tuple encoding (separator 0x00, in-segment 0x00 → 0x00 0xFF, FoundationDB tuple-layer style) so segment order equals byte order and prefixes stay contiguous; `ValidSegment` validates unescaped input; property test `TestJoinOrderMatchesSegmentOrder` over random strings.

### DATA-27 — MAJOR — `core.tasks.adopt-active` can fail boot permanently on a normal install

- **Where:** 05-tasks §5T.6 l.770 (active set "small: bounded by the active set (tens)", copied with `snap:`/`ledger:`/`rowmark:`/`opv2:row:` in one batch; over the atomic limits "fails boot rather than splitting").
- **Evidence:** the payload is not small: `opv3:snap:` holds a run's frozen item-key pages, `opv2:row:` a repairs plan's blob rows, `opv3:ledger:` chunk ledgers (`internal/database/keyfamilies.go:271-345`); 05-storage itself sizes these "library-scale per run" (§5S.9.2 l.763). One interrupted full-library run can exceed `core.storage.adopt.max_atomic_bytes` (64 MiB), and then every boot of the new release fails; the remedy (cancel the run on the previous release) is not documented.
- **Fix:** Copy run records and checkpoints atomically; leave immutable per-run pages (`snap:`, `row:`, `ledger:`) in place behind a RunID-keyed read-through adapter. Add an upgrade preflight (09 §9.7 P-command or `--print-upgrade-blockers`) and a drain step; if still over the cap, mark those runs `interrupted_dropped` with a UI notice instead of failing boot.

### DATA-28 — MINOR — Claim registry and bookkeeping lifecycle gaps

- **Where:** 05-storage §5S.9.1 l.742-747 (claims generated from `keyfamilies.go`), rule 2, `TestABOClaimsAvoidCorePrefixes` ("all 258"), §5S.3.5 Bookkeeping (l.390), Newer-version rule (l.396); 06 §6.5.1 P2 ("on-disk claim ledger (05-storage)"); 09 S0 l.287 (register `c/` and `m/` in `keyfamilies.go`), S2 l.310 (`c/storage/migrations/`).
- **Evidence:** after S0 the registry has 260 entries incl. `c/` and `m/`, which the generator would emit as books claims and rule 2 rejects. 05-storage defines no on-disk claim ledger, so a module removed from the build (not just disabled) leaves its legacy claims unowned for a later module to take. `c/storage/ks/<prefix>/…` embeds a '/'-terminated prefix (invalid segment) and 09 names a different key. The books version-source adapter stores only `{version, description, applied_at}` (`internal/database/migrations.go:96-100`), so an older core build cannot tell whether an unknown books step is `Additive`.
- **Fix:** Generator skips reserved families; tests count by group, not a literal 258. Define `c/storage/claims/<owner>` (claims, mode, build) written at boot; claims present on disk but absent from the build are "orphaned" until an explicit adoption migration. Key bookkeeping by owner ID (`c/storage/ks/<owner>/…`) in both 05-storage and 09. The adapter adds a `compat` field to `preference:migration_<n>` (ignored by pre-core builds); an unknown step without it is treated as Breaking.

### DATA-29 — MINOR — Factual slips, and a Batch hook 05-events requires

- §5S.13 row 1: a transient `db_version` read error does not re-run all 66 migrations — `RunMigrations` skips every version whose `preference:migration_<n>` record exists (`internal/database/migrations.go:542`, `migration_bookkeeping.go:132-138`); only record-less versions re-run.
- §5S.9.1: "`opv2:` (+14)" — 13 children (`keyfamilies.go:271-284`); the total 42 is right.
- §5S.9.2 sizing: "settings ~251 config keys" live in one `config_blob` row; `setting:` rows are markers (DATA-1). "preference: global: tens" is wrong (DATA-1).
- §5S.3 / §5S.12 layer numbers (storage 2, tasks 3) disagree with 03 v1.2.0 R4 (L1, L4).
- 05-events l.208-212 requires `(*storage.Batch).OnResolve(func(committed bool))` and cites 05-storage "open item 3"; §5S.3.2 has no such method and open item 3 is about `met:`.
- **Fix:** correct the text; add `OnResolve` to `Batch` (called exactly once with the commit outcome, or on `Close` of an uncommitted batch, never under engine locks) or record its rejection, and fix the cross-reference.

### DATA-30 — MINOR — Multi-app store merge is not "copy keyspaces"

- **Where:** 11 §11.5 item 4 ("copy keyspaces; legacy claims stay with `books`") and the closing claim that no row requires a core contract change; 05-backup §5B.11 item 4.
- **Evidence:** two installs each hold `c/auth/`, `c/rbac/`, `c/tasks/`, `c/config/`, `c/secrets/` (different keyrings), `c/storage/install`, per-keyspace versions, compat level/storage format and `Sequence` high-water marks; copying collides. An install-wide format/compat level (DATA-20) couples every app's rollback in a suite.
- **Fix:** In 11 §11.5 list a core storage export/import capability (per keyspace, version check, ID-remap hooks for core subsystems: users by username, roles by ID, keyring merge, sequence rebase) as a required core addition, and state that it changes a core contract (14-risks).
