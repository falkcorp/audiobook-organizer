<!-- file: docs/plans/platform-core/reviews/spec-round-5-correctness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 36e0d40e-387f-4e91-b47c-e43e6cff7e57 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 5: correctness and contracts

Lens: **correctness and contracts** (ID prefix `COR5`). Inputs: the revised spec in
`docs/specs/platform-core/` (03 v1.7.1 normative; 06 v1.5.1, 05-config v1.5.1, 05-storage v1.5.1,
05-tasks v1.5.1, 05-authn v1.5.1, 05-rbac v1.5.1, 05-http-api v1.5.1, 05-backup v1.4.0, 05-realtime
v1.4.0, 05-health v1.5.0, 05-ai v1.5.0, 05-logging v1.4.1, 04 v1.4.1, 09 v1.4.1, 10 v1.4.0). Binding:
R3-1…R3-5 as refined by R4-1…R4-6 and the round-4 lead decisions (legacy stores on
`module.Manifest.LegacyStores`; the frozen reach snapshot is books data). Code anchors are at
`748fd787a`; spec anchors are `file:line` in `docs/specs/platform-core/`.

Method: the four flows were traced through the current Go blocks and stage tables, and every
cross-file name on each path was looked up in its owning file:

1. ABO scan → `RunItems` → books' legacy `tasks.Stores` / books families → outbox → realtime → UI.
2. Route → `httpapi` stages 0–13 → `rbac` (legacy reach, auth-off) → `audit`.
3. `app.New(books, hello)` through P−1…P8 and Run (1)…(8), per stage, including the store-open
   writers HEAD runs inside `InitializeStore` and the served-ledger bookkeeping.
4. `hello` against core's native stores (config S2b, auth/rbac S4, tasks/journal S5), symbol by symbol,
   plus what its test asserts on a fresh store.

**Verdict.** R4-1's store-interface model is coherent at the type level: every interface named in
03 §3.3's table exists in its owner file under that name and section, every `LegacyStores` field in
06 §6.4.2 has a matching constructor type, `tasks.LegacyStoresFunc` is defined, the layer table holds
for every store package (`log.JournalStore` names only `log` types), and `hello` compiles. Nine of ten
round-4 BLOCKER/MAJOR items in this lens landed. What breaks now is again **stage timing and boot
order**: a claim flag that refuses HEAD's own writers from S2b (COR5-1), a normative P4d order that
runs `secrets.Open` before the store whose key it needs (COR5-2), a served-ledger write that erases
rollback evidence before it is consumed (COR5-3), and three contracts that have no mechanism behind
them (auth-off legacy reach COR5-4, the alias table's home COR5-5, a seed path on fresh stores COR5-8).

## Regression check (round-4 BLOCKER and MAJOR, correctness lens)

| ID | Sev | Status in round 5 | Where |
|---|---|---|---|
| COR4-1 | BLOCKER | **Fixed in substance** (R4-6): ABO's key guard, encryption init and settings load run in `OverrideStore.Open` at P4d step 1 (`05-config.md:375-378,719-724,1104-1124`; `06-app-sdk.md:1322,1340-1346`); `TestS2bAppConfigFromBlob` is an S2b done-check (`09-abo-on-core.md:736`). Residues: the normative table orders `secrets.Open` before `Open` (COR5-2); the done-check's `PUT /api/v1/config` cannot pass under S2b strict claims (COR5-1). | COR5-1, COR5-2 |
| COR4-2 | MAJOR | **Fixed**: `OverrideStore.Changed` is the S2–S3 source (`05-config.md:387-392,738-741`; `06-app-sdk.md:1246,1342-1346`; `TestOverrideStore_ChangedRepublishesAtS2AndS2b`). | — |
| COR4-3 | MAJOR | **Fixed**: `LegacyConfig` stays at S3 (`06-app-sdk.md:1247`; `09-abo-on-core.md:754`). | — |
| COR4-4 | MAJOR | **Fixed**: r1 skips P4d steps 2–4 and P7 seed writes (`06-app-sdk.md:1355-1358`); `journal` and `auth` duties from r2 only (`05-backup.md:40,714`; `05-storage.md:949`); tests named. Residue: the r1 census has no row in the boot table (COR5-15). | COR5-15 |
| COR4-5 | MAJOR | **Fixed**: `Serialized` + `SetIfUnchanged`/`SetIfAbsent`, attribution-only kind, own `ConcurrencyKey` (`06-app-sdk.md:289-309`; `05-tasks.md:1019-1042`; `05-storage.md:748-751`). | — |
| COR4-6 | MAJOR | **Fixed** (synthesised definition table, `05-tasks.md:1026-1034`). Residue: three test names, one of which assumes a hello migration that does not exist (COR5-17). | COR5-17 |
| COR4-7 | MAJOR | **Half-fixed**: S6–S7 agree (`books.system.status-ticked.v1`). From S8, 05-realtime says the status topic is **never** added to projection A; 09 S6 and 05-health say it **is** added and the frame payload changes. | COR5-9 |
| COR4-8 | MAJOR | **Fixed**: readiness core lands with `tasks` at S5 (`05-health.md:557`; `06-app-sdk.md:1249`; `04-package-mapping.md` §8.1 S5). | — |
| COR4-9 | MAJOR | **Fixed**: `LegacyRoute.BasicExempt`, `LegacyMount.BasicExemptAssets` (`05-http-api.md:1003,1012`). | — |
| COR4-10 | MAJOR | **Key fixed** (`core.backup.dir` from S4, `06-app-sdk.md:1248`; `05-backup.md:35-37,887`). New residue: the journal's `c/backup/` keyspace and its `Serialized` head are still declared only by `backup` (S8). | COR5-6 |

## Flow traces (where each one breaks now)

| Flow | Breaks at |
|---|---|
| 1 scan → UI | S2b r2 (strict): HEAD's blind writers of `import_path:` (every scan) and the config rows refused (COR5-1). S2b–S4: HEAD's every-boot `ReconcileOpsV3WithV2` has no home (COR5-7). S8: projection A's status frame contradicts itself across files (COR5-9). Otherwise composes: `RunStore` writes stage into the executor's batch by participant binding (`05-tasks.md:1101-1107`, `05-storage.md:344-356`). |
| 2 route → audit | S4 r2 with auth off: stage 13 refuses every `settings.manage`/`users.manage` legacy route (`Perm` = `{core:admin}`, `AuthRequired`); from S5 also every adapted op def with the default atom (COR5-4). S4: ABO's 13 atom aliases have no consistent declarer (COR5-5). S4–S7: a revocation stages `c/backup/journal_head` in an undeclared keyspace (COR5-6). |
| 3 boot | P4d step 1 order (COR5-2); served ledger `last` written at step 9, before every duty consumer (COR5-3); store-open writers without a home S2b–S4 (COR5-7). |
| 4 hello | Compiles: `config.Define`/`Docs`, `Keyspace.Sub/Get/Scan`, `storage.Migration{Up}`, `MigrationTx.Set`, `httpapi.Handle/In/Invalid/Field`, `tasks.Task/TaskSpec/Common/Permissions/RunItems/ItemsOptions/ItemRC.Count/Res/ReadOnly/CPU/ResumeFromZero`, `rbac.CoreAdmin`, `Registrar.Deps/HTTP/Resources/Tasks/Err` all resolve. **Fails at run time**: on any fresh store (`coretest`, a new install) migration 1 is stamped applied and never runs, so `TestHello`'s `g.Seen == 1` fails (COR5-8). |

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 1 | COR5-1 |
| MAJOR | 7 | COR5-2 … COR5-8 |
| MINOR | 9 | COR5-9 … COR5-17 |

## Findings

### COR5-1: BLOCKER. `CheckedWrites` on the config rows and `import_path:` refuses HEAD's own blind writers from S2b strict, years before those writers are converted

**Where:** 05-storage §5S.9.1 rows 7 and 19 (`05-storage.md:1638,1651`), `CheckedWrites` contract
(`05-storage.md:54,475-481,1499`), `TestS2bBooksWritesEveryFamily` (`:565,1762`); 09 §9.2.3 "the same
table at every stage" (`09-abo-on-core.md:339-342`); 09 S2b Releases and Done when (`:733-736`); 09 S7
(`:816`: the field-merge conversion of the root writers happens **at S7**); 05-config §1.6
(`05-config.md:794-797`: config's `GetForUpdate` writer exists from S3).

**Evidence:**
- `CheckedWrites` makes Commit refuse any `Set`/`Delete` of the claim without an `Expect` on that key
  (`ErrBlindWrite`, closed, counted). Rows 7 (`setting:config_blob` + the four secret rows) and 19
  (`import_path:`) carry it, and 09 says the claim table is identical at every stage from S2b.
- HEAD writes all five config rows with a blind `s.db.Set` (`internal/database/settings.go:316-339`,
  called by `SaveConfigToDatabase`, `internal/config/persistence.go:884`), and import roots with blind
  batches (`internal/database/pebble_store_importpaths.go:166,199-208`). From S2b to the S3 façade ABO's
  legacy config path **is** that code ("books code, the writer until the S3 façade",
  `06-app-sdk.md:1343-1344`); the scanner/autoscan/filesystem root writers are converted to
  `GetForUpdate` only at S7.
- So at S2b release 2 every settings save and every scan's root-statistics write fails with
  `ErrBlindWrite`; in release 1 (shadow) each is a recorded violation, so the S2b done-check ("no
  writer for ≥ 14 days") can never be met. `TestS2bAppConfigFromBlob` (a `PUT /api/v1/config` at S2b)
  and `TestS2bBooksWritesEveryFamily` ("zero violations in strict mode") cannot pass as specified.

**Fix:** Give the flag a start stage in §5S.9.1 (it is enforcement only, so adding it later is
rollback-safe):
- row 7: `CheckedWrites` **from S3**, the stage whose façade replaces `SaveConfigToDatabase` with
  `Store.Apply` (a `GetForUpdate` writer) and whose internal-flag route refuses these keys; S2b–S2b r2
  keep `Serialized` only, with HEAD's single writer;
- row 19: `import_path:` `CheckedWrites` **from S7**, in the PR that converts the root writers (09 S7).
- Amend 09 §9.2.3's sentence to "the same families and modes at every stage; `CheckedWrites` turns on
  at the stage named in the row". State that an `ErrBlindWrite` in shadow mode is a recorded violation,
  and extend `TestS2bBooksWritesEveryFamily` with a settings save and a scan's root update.
- Alternative: convert both HEAD writers in the S2b PR (books code) and say so in 09 S2b Moves; then
  the rows can stay unstaged.

### COR5-2: MAJOR. The normative P4d step 1 runs `secrets.Open` before `OverrideStore.Open`, so ABO's key is not loaded when secrets captures `k0-legacy` and re-imports re-entered secrets

**Where:** 06 P4d step 1 (`06-app-sdk.md:1322`: "1. `secrets.Open` (the keyring only: no import …),
then `AttachOverrides` … For ABO this is the one point where books' legacy config store runs ABO's
key-regeneration guard, settings-encryption init …"); 05-storage §5S.4 step 11 (a)
(`05-storage.md:1437-1441`, same order); 05-authn §5 step 2 (`05-authn.md:1109-1110`, same order);
against 05-config §1.4 step 5 (`05-config.md:719-724`: `OverrideStore.Open` → `secrets.Open` with
`OverrideStore.Secrets()` → `SetSecrets`), the `Open` contract ("before Load and before
secrets.Open", `:375-378`) and §2.4 step 1 (`:1649-1671`: "after `config.OverrideStore.Open` ran ABO's
own key guard"); 06 §6.4.3 comment (`06-app-sdk.md:592-597`: "At S3 … `secrets.Open` … take[s] over the
… encryption parts") against 05-config §1.12.2 (`05-config.md:1122-1124`: "the guard and the encryption
init stay in `Open` until cutover").

**Evidence:**
- 06's table is "the **only** normative statement of boot order" (`06-app-sdk.md:1273`). Under it,
  `secrets.Open` calls `LegacyStore.KeyMaterial()` and reads the legacy primary copies before books'
  `Open` has run HEAD's `initEncryption` (`cmd/root.go:353-359` loads the key; a constructor may do no
  I/O, `06-app-sdk.md:358-359`).
- Consequences: `KeyMaterial()` is nil on every boot, so `k0-legacy` is never recorded and the exported
  keyring never covers the legacy rows (host-loss recovery of those secrets fails); every legacy
  primary copy is undecryptable, so the R4-2 roll-forward re-import never fires
  (`secrets_legacy_unreadable`, `05-config.md:1670-1671`) and a secret an operator re-entered on the
  older build is silently ignored.
- 06 also says `secrets.Open` does "no import", while 05-config makes it the one place the config
  secrets' roll-forward re-import happens (`05-config.md:1192-1199`); 06 step 1b covers integrations
  only. And 06 §6.4.3's comment would drop ABO's guard at S3, which 05-config keeps.

**Fix:** In 06 P4d step 1, 05-storage step 11 (a) and 05-authn §5 step 2 write the order once:
`OverrideStore.Open` (ABO's key guard and encryption init; S2b–S3 also the settings load) →
`secrets.Open(…, OverrideStore.Secrets())` (keyring, `k0-legacy` capture, the R4-2 roll-forward
re-import of config secrets — its only store writes besides `c/secrets/meta`) → `Store.SetSecrets` →
`Store.Attach` (`Load`, read-only). Replace "no import" by "no derive-key retirement (P5)". Rewrite
06 §6.4.3's S3 sentence: the façade replaces the settings load; the guard and the encryption init stay
in `Open` until cutover; `secrets.Open` covers core records only. Add
`TestP4dStep1_LegacyOpenBeforeSecrets` (first S3 boot with ABO's key file: `k0-legacy` recorded; a
legacy copy re-entered on the older build is re-imported).

### COR5-3: MAJOR. The served ledger's `last` record and the foreign-open identity are overwritten at step 9, before any duty consumer runs; a failed boot in between erases the rollback evidence

**Where:** 05-storage §5S.4 step 9 (`05-storage.md:1427-1430`, in 06 P4b) and the served-ledger and
foreign-open rows (`:949-950`); consumers: the `gap` line at P4d step 2 (`05-backup.md:715,717`),
revoke-after-rollback in `auth.BootWrites` at P4d step 4 (`05-authn.md:699-706`), the derived loader's
`gen` check at Run (3a) (`05-storage.md:1168`), the `usage` window rule (09 §9.2.9).

**Evidence:**
- The ledger keeps only `last` = `{build, supported, duties, at}`; a consumer decides "the previous boot
  lacked the duty" by reading it at step 2, and step 9 replaces it with this boot's record (and records
  the new sidecar identity) unconditionally.
- Everything between step 9 and the consumers can fail boot: a P4c constructor error, P4d step 1
  (ABO's key guard is Fatal; an override fails validation), the journal move at step 2, any P5–P8
  problem. The next boot then reads `last` = the failed boot, which lists every duty, and sees no
  foreign open.
- Results: no `gap` line, so a later restore whose boundary precedes the rollback interval gets
  `Revocations` without the older build's unjournaled revocations and re-activates those credentials;
  no `RevokeSignInsBefore`, so sessions a user ended on the older build come back (exactly what SEC4-9
  fixed, `TestNativeSessionsRevokedAfterRollback`); and a derived artifact staled by the older build is
  accepted. A failed first boot after a roll-forward is the common case, not an edge.

**Fix:** Write the served ledger's `last` record and the foreign-open identity **after the last duty
consumer**, in one batch before listeners (06 Run step 6), and state that each consumer is idempotent
on re-detection (a second `gap` line is harmless; `RevokeSignInsBefore` uses the same `t`; a rebuild
re-runs). Alternative: at step 9 also write `c/storage/served/<owner>/pending = {duties_missing,
since, foreign_open}` when a fallback is due, and let each consumer clear its entry in the batch that
performs it. Add `TestFailedBootAfterRollbackKeepsFallbacks` (rollback boot, roll forward, fail at
P4d step 1, boot again: gap line appended, pre-rollback sessions revoked) to 05-storage §5S.10.

### COR5-4: MAJOR. With auth off, stage 13 refuses every `settings.manage` and `users.manage` legacy route from S4 r2, and from S5 every adapted op def with the default atom; the "judged against the expansion" exception has no mechanism

**Where:** 05-http-api `LegacyRoute.Perm` (`05-http-api.md:1008`: "`Catalog.LegacyReach(atom)`") and
stage 13 (`:627`: "`Perm` via the principal's checker"); bridge row (`:1076`); 05-rbac §4.11 auth-off
(`05-rbac.md:715-731`); 09 §9.2.6 recorded change (`09-abo-on-core.md:450-455`); 05-tasks start rule
(`05-tasks.md:609`: a def declaring `settings.manage` "requires `core:admin`"); `TestLegacyRoleReachUnchanged`
(`09-abo-on-core.md:779`).

**Evidence:**
- `Catalog.LegacyReach("settings.manage")` and `("users.manage")` are `AllOf(core:admin)`
  (`05-rbac.md:198-201,785`). `core:admin` is `AuthRequired`, and `authz.AuthDisabled` holds no
  `AuthRequired` scope and has "no implication from `core:admin`" (`05-rbac.md:715-718`).
- `LegacyRoute` carries only `Perm`; stage 13 has no input that names the atom, so it cannot judge the
  auth-disabled principal "against the atom's expansion" as 05-rbac, 05-http-api and 09 promise. Every
  one of the 83 `settings.manage` routes (settings, backups, logs, import paths, filesystem browse)
  answers 401 with auth off, contrary to HEAD (`internal/server/server_lifecycle.go:1693-1698`:
  `perm` returns `c.Next()` when auth is off), and the auth-off half of `TestLegacyRoleReachUnchanged`
  cannot pass, so S4 r2's done-check fails.
- The same holds from S5 for task starts: the adapted op-def atom translates through `LegacyReach`
  with no auth-off clause, so an auth-off install cannot start any op that kept the old default
  `settings.manage`.

**Fix:** Add `LegacyRoute.Atom string` (the `s.perm` atom; empty for admin-kind and `OwnAuth` routes)
and, in stage 13, evaluate `rbac.AllOf(Catalog.Legacy(Atom)...)` instead of `Perm` when the principal's
checker is the auth-disabled one; or define one requirement value, `rbac.LegacyAtom(atom)`, which the
role/key checker resolves through `LegacyReach` and `authz.AuthDisabled` through `Legacy`. Use the same
value for adapted op defs' `Perms.Start` (05-tasks 5T.3.6). Update 05-http-api §4.2 stage 13 and §8.2,
05-rbac §4.11/§6.3, 05-tasks; `TestLegacyRoleReachUnchanged`'s auth-off half asserts 200 on a
`settings.manage` route and a started `settings.manage` op with auth off.

### COR5-5: MAJOR. ABO's 13 legacy atom aliases (with `Reach`) have no consistent declarer: books' manifest is refused by 05-rbac's escalation rule, and 05-rbac's own home puts ABO atoms in core through an option 06 does not define

**Where:** 06 `Manifest.LegacyAliases` (`06-app-sdk.md:223-236`: "legacy permission-string aliases … the
kernel feeds them to rbac's Declarer in P5") and §6.4.10 S4 (`:1248`); 05-rbac `Declarer.LegacyAliases`
("core-only for aliases that map to core:*", `05-rbac.md:192`), §4.9 (`:681-698`: `ErrCoreGrant`; "the
alias table of §6 is declared by **core** (`core/rbac/legacy_abo.go`, enabled by the compile-time
`app.Options.LegacyPermissionAliases`)"), §6.3 (`:912-927`: the snapshot and `LegacyAlias.Reach` are
"app data, supplied by the app … never under `core/`"), `:969`; 06 `app.Options` (`06-app-sdk.md:785-940`:
no `LegacyPermissionAliases`); `RoleSeed.Name` and §4.9 cite a module `LegacySurfaces`
(`05-rbac.md:168,685,697`) that 06's `Manifest` does not have.

**Evidence:**
- ABO's aliases target `core:*` (`settings.manage` → `core:settings.manage`, `core:tasks.manage`,
  `core:system.view`; `Reach` `{core:admin}`, `05-rbac.md:785`). Declared in books' manifest, as 06
  says, they fail `Freeze` with `ErrCoreGrant`, so ABO's S4 boot fails.
- Declared in `core/rbac/legacy_abo.go`, as 05-rbac §4.9 says, core holds ABO's atom names and the
  frozen `Reach` table — what the round-4 lead decision moved out of core (05-rbac §6.3) and what 03 R1
  forbids in spirit (`TestCoreHoldsNoAppKeyNames` only checks key prefixes, so nothing catches it).
- The switch named for it, `app.Options.LegacyPermissionAliases`, does not exist in 06, and nor does
  `LegacySurfaces` (needed for `fp-worker`).

**Fix:** One home, owned by the app: `app.Options.LegacyPermissionAliases []rbac.LegacyAlias` (06
§6.4.5, S4), filled by ABO's `main` from books' snapshot package; the kernel declares them as
**app-level** aliases, which may target `core:*` (the app's `main` is the trust root, like every other
`app.Options` field), validated at `Freeze` (each target in the catalog, `Reach` dominates `New`).
`Manifest.LegacyAliases` stays own-namespace only (§4.9 unchanged). Delete `core/rbac/legacy_abo.go`;
add `Manifest.LegacyRoleIDs []rbac.RoleName` (or define `LegacySurfaces`) for `fp-worker`. Update
06 §6.4.2/§6.4.10, 05-rbac §3.2, §4.9, §6.3, §12 and 09 §9.2.6.

### COR5-6: MAJOR. From S4 to S7 every revocation stages `c/backup/journal_head` (and, on failure, `c/backup/journal_gap/`) in a keyspace that only `backup` declares, at S8

**Where:** 05-backup `RevocationJournal.Append` (`05-backup.md:405-414`: "reading the head with
`GetForUpdate` (`c/backup/journal_head` is Serialized)"), row "Head in the store" (`:713`: "`Serialized`
(backup's `storage.Spec`)"), "Append failure" (`:716`); `backup/journal` "imports `entity` and `storage`
only" (`:17,354`) yet "declares the restart-bound key `core.backup.dir`" (`:35-37`); 06 S4 row
(`06-app-sdk.md:1248`); 05-storage `Batch.Expect`/`GetForUpdate` (`05-storage.md:269-272`) and commit
rule 1 (`:324-326`).

**Evidence:**
- `backup` lands at S8 (`05-backup.md:16-23`); its `storage.Spec` is the only declaration of `c/backup/`
  and of `journal_head` as `Serialized`. `auth` and `rbac` revoke from S4 r2.
- At S4–S7 `GetForUpdate` on an undeclared/non-`Serialized` key returns `ErrNotSerialized`; the
  documented fallback, `Gap(b, at)`, writes `c/backup/journal_gap/<since>` into the same batch, which
  then fails commit with `ErrOutsideKeyspace`. Either every journaled revocation fails to commit (a
  disable or key revoke that does not happen), or the journal is silently off for four stages.
- Declaring a config key needs `config`, which "imports `entity` and `storage` only" excludes.

**Fix:** `backup/journal` (S4) declares core keyspace `c/backup/` (owner `backup`, already in the
fixed core order, `05-storage.md:713`) with `Serialized: ["journal_head"]` and the `journal_gap/` and
`restore_hooked/` prefixes, registered by the kernel from S4; `backup` adopts the Spec at S8, as for
`core.backup.dir`. Either let `backup/journal` import `config` for the section (L4 → L3 is legal) and
say so, or move the `core.backup.dir` declaration into the kernel's core-section list. Add S4
done-check `TestRevocationStagesJournalHeadAtS4`.

### COR5-7: MAJOR. Two of HEAD's every-boot store-open writers have no home between S2b and S4, and 06's write rule contradicts 05-storage on where the others go

**Where:** 05-storage §5S.9.3 (`05-storage.md:1723-1724`); 06 write rule (`06-app-sdk.md:1283-1290`:
"counter seeding in the fresh-install batch (P4a)"; "the one later every-boot writer is the run store's
start-time repair") and §6.11.5 (`:1674`); 05-storage step 6 (`05-storage.md:1415-1416`: "No app row is
seeded here"); 06 Stages (`06-app-sdk.md:1346`: "`serve` runs no startup step from S2b"); 09 §9.2.3
tasks row (`09-abo-on-core.md:329`: "no reconcile").

**Evidence:**
- HEAD runs `ReconcileOpsV3WithV2` on every boot, synchronously, "before the server and the operations
  registry exist, because the registry's boot resume acts on opv3 records"
  (`internal/database/store.go:1861-1871`; `pebble_store_ops_v3_reconcile.go:80-89`). Its successor is
  `RunStore.Recover` inside `Executor.Run` — S5. From S2b to S4 the kernel opens the store, `serve` runs
  no step, `Register` writes nothing, and HEAD's v2/v3 registry still runs inside books: nothing runs it.
- HEAD runs `ReconcileVerifiedEmails` at every open (`internal/database/pebble_store.go:755-760`) to
  stamp rows an older build wrote and to rewrite the `idx:user:email` cache that older builds use to
  link SSO logins (`pebble_store_verified_email_backfill.go:12-16,310-315`). Its successor is
  `auth.BootWrites`, which exists from S4 and is skipped at S4 r1: nothing runs it at S2b, S3 or S4 r1.
- 06 says counters are seeded in the fresh-install batch; 05-storage says nothing is seeded (absent =
  `1`). 06 says the only post-P4d every-boot writer is `RunStore.Recover`; 05-storage puts the
  journal-clock repair and the identification-index stamp in books' start hook (Run step 2).

**Fix:** In 05-storage §5S.9.3 and 06 §6.5.1 "Stages": until `tasks` lands (S5) books' start hook runs
`ReconcileOpsV3WithV2` before it builds its ops registry; until S4 r2 it runs `ReconcileVerifiedEmails`;
each then moves to `RunStore.Recover` / `auth.BootWrites`. Rewrite 06's write rule to list the later
every-boot writers (books' start-hook repairs at Run step 2, `RunStore.Recover` at Run step 5) and drop
"counter seeding in the fresh-install batch" there and in §6.11.5. In 09 §9.2.3 replace "no reconcile"
by "HEAD's `ReconcileOpsV3WithV2` as `RunStore.Recover`, no new reconcile". Add an S2b done-check: a
store with a stale opv3 record and an unstamped email row is repaired on the first S2b boot.

### COR5-8: MAJOR. The fresh-install rule stamps every versioned step applied without running it, so seed migrations never run: hello's test fails and TV/Movies default profiles never exist

**Where:** 06 P4 (a) (`06-app-sdk.md:1322`: "every registered versioned step recorded as applied");
05-storage §5S.4 step 6 (`05-storage.md:1411-1416`) and `TestFreshInstallStampsLevelsAtomically`
(`:1775`); hello's migration (`06-app-sdk.md:1836-1840,1876-1884`) and `TestHello`
(`:2150`: `g.Seen != 1` fails); 10 §10.5 (`10-tv-movies-proof.md:704-705`: "Seed data (default profiles
per module) is the module's migration").

**Evidence:**
- On an empty store P4a writes one batch that records every registered step as applied; no step runs.
  `coretest.New` opens an empty in-memory store, so hello's `seed-names` never writes `name/world`, the
  greeting returns `Seen: 0`, and `TestHello` (G6/G7's in-tree proof from S5, `06-app-sdk.md:1266`)
  fails. A real hello install behaves the same.
- A TV or Movies app is always a fresh install, so "default profiles per module" seeded by a migration
  never exist.
- No seed mechanism is specified anywhere else (`grep -i seed` over 05-storage finds only the
  counter remark).

**Fix:** Add a seed path in 05-storage §5S.3.5: `Migration.Seed bool` (allowed only with `Additive`).
On a fresh install the stamp batch records non-seed steps as applied and leaves seed steps pending;
they then run in P4b as ordinary steps (each its own batch; no checkpoint, the store held nothing).
Or `storage.Spec.Seed func(ctx, *MigrationTx) error`, staged inside the fresh-install batch. Mark
hello's step `Seed: true` in 06 §6.12.1, point 10 §10.5 at the seed step, and add
`TestFreshInstallRunsSeedSteps`.

### COR5-9: MINOR. COR4-7 residue: three files disagree on stream A's `system.status` frame from S8

**Where:** 05-realtime §4.9 (`05-realtime.md:382-385`, and §4.7 row `:298`: from S8 the status topic "is
**not** added to projection A, so stream A's `system.status` payload never changes"); 09 S6 Endpoints
(`09-abo-on-core.md:804`: "from S8 `core.system.status-changed.v1` is added as a separate legacy frame
type (its payload `{ready, not_ready}` is recorded in the compat ledger)"); 05-health status topic
(`05-health.md:196-198`: "from S8, the legacy stream A `system.status` projection read it … the frame's
payload then changes").

**Fix:** Keep 05-realtime's rule (no S8 change; the topic is native-stream only; no compat-ledger row)
and rewrite 09 S6's "from S8" clause and 05-health's last sentence to match; `TestProjectionTopicsExistAtStage`
already asserts it. Add `books.system.status-ticked.v1` to 05-realtime §4.7's table.

### COR5-10: MINOR. An "`ai` result-journal store interface" is cited by 04 and 09 but defined nowhere

**Where:** 04 §2.3 (`04-package-mapping.md:236-238`), row `internal/ai/resultjournal` (`:343`), §8.1 S7
(`:791`: "with `ai.JobStore` and the result-journal store, native over `c/ai/`"); 09 §9.2.3
(`09-abo-on-core.md:332`) and S7 (`:816`); 05-ai defines only `JobStore` (`05-ai.md:435-447`); 03's store
table and 06's `LegacyStores` have no such field.

**Evidence:** every importer of `internal/ai/resultjournal` is a books package (`internal/transcribe`,
`internal/scanner`, `internal/plugins/maintenance`, `internal/reconcile`), so no core service persists
through it.

**Fix:** Drop "result-journal store" from 04 (three places) and 09 (two places): the package stays ABO
as plain books code over books' own `aijournal:` claim, not a core store implementation.

### COR5-11: MINOR. 09's tasks row puts definition snapshots and idempotency records in books' extension family; 05-tasks and 06 put them in core's `c/tasks/`

**Where:** 09 §9.2.3 (`09-abo-on-core.md:329`: extension family holds "definition snapshots …, HTTP
idempotency records"); 05-tasks 5T.6 (`05-tasks.md:1299-1304`: `c/tasks/def/<kind>`, `c/tasks/idem/…`
"core's whichever implementation serves runs"), step 1 (`:1213`); 06 P7 (`06-app-sdk.md:1325`);
`tasks.Stores` has no snapshot or idempotency method (`05-tasks.md:1108-1176`). 05-tasks also says "no
boot reconcile" (`:1297`) beside `Recover` = HEAD's reconcile (`:1218`).

**Fix:** In 09's row move those two items to "core's `c/tasks/def/` and `c/tasks/idem/`"; reword
05-tasks `:1297` to "no P4d reconcile".

### COR5-12: MINOR. "Every write method takes the caller's `*storage.Batch`" has unstated exceptions

**Where:** 06 `LegacyStores` comment (`06-app-sdk.md:361-362`); 05-storage §5S.3.11 "Store interfaces"
(`05-storage.md:1218`); `log.JournalStore.Append/Prune` (`05-logging.md:191-197`, no batch, by design: `log`
is L0); `RunStore.Recover/Prune` (`05-tasks.md:1147-1148`).

**Fix:** State the exception in both places: `log.JournalStore` and the maintenance methods
(`Recover`, `Prune`) commit their own batches and never join a multi-owner commit.

### COR5-13: MINOR. The package that holds books' legacy stores has three names

**Where:** 06 §6.11.6 (`06-app-sdk.md:1690-1692`: `internal/books/legacystores`, "the only package an
ABO archtest lets write those families"); 04 §2.4 (`internal/books` holds the constructors "over
`internal/books/store`"; `internal/books/store` holds the implementations; `internal/books/taskstore`
the tasks one) and the `dailyquota`/`resultjournal` rows; 03 §3.5.1 (`03-architecture.md:410-416`: four
new paths, none of them `legacystores`).

**Fix:** Use 04's packages in 06 §6.11.6 (constructors in `internal/books`, implementations in
`internal/books/store`, `internal/books/taskstore`, `internal/metadata/dailyquota`), and give
`TestLegacyFamiliesWrittenOnlyByLegacyStores` that explicit allow-list per family; or add
`internal/books/legacystores` to 04 §2.4 and recount 03 §3.5.1 (167).

### COR5-14: MINOR. `config.OverrideStore` is used at S2 but "lands" at S2b

**Where:** 06 §6.4.10 S2 (`06-app-sdk.md:1245`: `Options.LegacyConfigStore`, type `config.OverrideStore`);
05-config §1.4 step 3 (`05-config.md:709-712`); against 09 S2b Moves (`09-abo-on-core.md:732`: "Core
`config` gains `config.OverrideStore`"), 04 §8.1 S2b and 03's store table ("Lands S2b").

**Fix:** Say the interface lands at S2 with `Open`, `Load`, `Changed`, `Suspended` (no `storage` or
`secrets` type in them), its native implementation and `LegacyStores.Config` at S2b, and the write
methods at S3; fix 09 S2b, 04 §8.1 and 03's "Lands" cell.

### COR5-15: MINOR. The S4 r1 decode census has no step in the boot table

**Where:** 06 §6.5.1 Stages (`06-app-sdk.md:1355-1358`) and P4d's numbered steps (`:1322`); 05-authn §5
(`05-authn.md:1117-1121`: "after step 1, the read-only decode census"); 09 S4 Done when (`:779`).

**Fix:** Add to P4d "1c. (S4 r1 only) read-only decode census over `auth.Store`/`rbac.Store` scans;
writes nothing" so the one normative table names it.

### COR5-16: MINOR. The `Deferred-build members` generator input is not parseable as specified

**Where:** 03 §3.4 (`03-architecture.md:357-366`: heading "named exactly `Deferred-build members`",
package-qualified identifiers); headings carry section numbers (`05-authn.md:578`, `05-http-api.md:558`,
`05-rbac.md:484`, `05-backup.md:754`, `05-storage.md:1360`, `06-app-sdk.md:1198`); entries that are not
identifiers (`05-authn.md:586-589`: "`X-Api-Key` header and `apikey` query-key extraction", "Feed
token"; `05-storage.md:1367-1369`: health tokens, the `.storage-applied` line format, `06 ProblemGrant`).

**Fix:** Define the match as "a heading whose text ends with `Deferred-build members`", and require
each table's first column to hold only package-qualified identifiers (`auth.KeySpec.Purpose`,
`module.ProblemGrant`), moving prose and non-Go items to a second column.

### COR5-17: MINOR. One data-migration test has three names, and 06's version assumes a hello migration that does not exist

**Where:** 06 `TestOnlineDataMigrationIsModuleKind` (`06-app-sdk.md:1537`: "hello's `Online` data
migration"); 05-storage `TestModuleOnlineMigrationRunsAsModuleKind` (`05-storage.md:1827`); 05-tasks
`TestModuleOnlineMigration_OwnerWritesSurvive` (`05-tasks.md:1569`); hello declares no `DataMigrations`
and no `Serialized` family (`06-app-sdk.md:1835-1841`).

**Fix:** Keep one name (05-storage's, the owner) and run it over a test module fixture, not hello.
