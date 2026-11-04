<!-- file: docs/agent-tasks/platform-core/PC-09-06-s3-flip-config-facade.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b39b4de-7f1a-4f74-9ae9-10808ad59e41 -->
<!-- last-edited: 2026-11-04 -->

# PC-09-06 — S3 flip: ABO's internal/config becomes the Facade over core handles; viper leaves the boot path; the Basic gate leaves gin

**ID:** PC-09-06 · **Wave:** W09 (S3, release `S3`) · **Priority:** P1 · **Effort:** XL
**Tier:** T4 Opus/xhigh-max — the effective config of every install changes owner in this PR; legacy auth spellings
become fail-closed boot errors; the Basic gate moves.
**Depends on:** PC-09-04 (write path), PC-09-05 (stage 8). This is the stage's **flip PR**: it lands after every other
S3 content PR and before PC-09-07.
**Stage-Release trailer:** `Stage-Release: S3 flip` on every commit (09 §9.2.1 "The trailer").
**Marker:** sets `shipped_stage: S3` and `release: S3` in `ci/stage.txt`. Merges only with a current soak row of at least
14 days for the release `release:` names when it merges (normally `S2br2`; never `UI0`; 09 §9.2.1 check (a)).
**Required checks beyond `make ci`:** `compat-rehearsal` (every flip PR and every `release:` change), `unit-lifecycle`
(every flip PR), `extraction-gates`, `docker-image`, `core-ci` (09 §9.2.1 required-checks table).
**Normative digest:** `docs/plans/platform-core/digests/S3.md` (all rules; Rollback costs; Done-when).
**Owning spec:** 09 S3 (Releases, Mechanism, Basic gate, Config compatibility, Endpoints, Rollback, Done when);
05-config §1.12.1 stage 1 (the façade and the S3 actor), §1.12.2, §1.3.5, §1.3.6, §1.3.7; 05-http-api §8.1 S3 row;
09 §9.2.5, §9.2.6.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-09-06" -b agent/pc-09-06-s3-flip origin/main
cd "$REPO/../audiobook-organizer-pc-09-06"
git rebase origin/main
git worktree list
npm ci --prefix web
```

Before starting: `gh pr list --state open` shows no other open S3 content PR (the flip lands last). If one is open,
STOP and report.

## Goal

1. ABO's `internal/config` keeps its exported surface and becomes a **`Facade`** (type in `internal/config`, no new
   package): one value per App, built by books from the handles it registered and the core handles it reads, installed
   as the target of `AppConfig`, `Snapshot`, `Mutate`, `Generation`, `SettingLocks`, `EnvLockedSettings`. `AppConfig` is
   a projection rebuilt under the façade's lock on every core generation change; secret fields are resolved from
   `secrets` in memory only. `Mutate(fn)` diffs before/after `Config` and turns the diff into **one** `Store.Apply`
   through the alias table.
2. viper leaves the boot path: `configFromViper` and `initConfig`'s viper wiring are replaced by core `Load` plus the
   generated aliases (PC-09-03), whose legacy names decode with HEAD's per-type rules; legacy bools on `core.auth.*` or
   `ProtectAdmin`+ keys with a spelling `ParseBool` rejects stop boot with a message naming the variable, the spelling
   and the canonical name.
3. Every write goes through `Store.Apply` (books' store renders the blob in place under `configBlobSaveMu`):
   `PUT /api/v1/config`, the façade's `Mutate`, and every remaining `SaveConfigToDatabase` caller.
4. The Basic gate moves: stage 8 (PC-09-05) is inserted into the kernel's chain and
   `internal/server/middleware/basicauth.go` is deleted with its `router.Use` line, in this PR.
5. `legacy.ConfigExternalWriter` is deleted (05-config §1.12.2 "the capability is deleted in the S3 PR").

## Exact files

Modify: `internal/config/config.go`, `internal/config/persistence.go`, `internal/config/facade.go` (see Planner
notes: no row creates it — create it here), `cmd/root.go`, `main.go`, `internal/server/middleware/basicauth.go`
(delete, with its tests), `internal/server/server.go`, `ci/stage.txt`.
Also expected (Planner notes): the HEAD writers `internal/config/update_service.go` and
`internal/server/handlers/scheduler_admin.go`; the kernel chain file in `core/internal/httpmw/` or `core/kernel/` that
lists the stages.

## Re-verify (expected at the plan checkout `f07f516be`; rerun at your base)

```bash
grep -n 'viper.AutomaticEnv()' cmd/root.go                                   # 1 hit (removed by this PR)
grep -rn '"github.com/spf13/viper"' --include=*.go cmd internal main.go | grep -v _test | wc -l   # record N at base; boot-path importers go to 0 here
grep -n 'func configFromViper\|configFromViper(' internal/config/*.go | head   # >= 1 hit (replaced by core Load + aliases)
grep -n 'router.Use(servermiddleware.BasicAuth())' internal/server/server.go  # 1 hit (removed)
test -f internal/server/middleware/basicauth.go && echo present               # present (deleted)
grep -n 'shipped_stage:\|release:' ci/stage.txt                               # shipped_stage: S2b, release: S2br2 expected at base
grep -n 'func NewBasicGate' -r core/internal/httpmw/                          # 1 hit (PC-09-05)
grep -n 'func (s \*Store) Apply' -r core/config/                              # 1 hit (PC-09-04)
grep -n 'func LegacyAliases() \[\]config.Alias' internal/config/aliases_gen.go   # 1 hit (PC-09-03)
grep -n 'ConfigExternalWriter' -r core/ internal/ | grep -v _test | head      # >= 1 hit (deleted here)
grep -rln 'SaveConfigToDatabase(' --include=*.go internal cmd | grep -v _test # the remaining HEAD writers; this PR converts every caller outside the books store
test -f internal/config/testdata/legacy_decode.golden && echo ok              # PC-09-02
```

## Steps

1. **Tests first:** the done-check list below, red.
2. **Loader.** `cmd/root.go`'s `initConfig` and `internal/config`'s `InitConfig` stop calling viper; ABO calls the core
   loader with `app.Options.LegacyConfig` carrying the generated aliases. Flags map to paths (`--dir`, `--db`,
   `--db-type`, `--playlists`, `--log-level` and the other flags of 09 §9.6 row 18). `--log-level` and `--playlists`
   now beat a stored value (09 §9.2.6).
3. **Façade** (`facade.go`): per 05-config §1.12.1 stage 1. One `OnChange` per section; the projection is rebuilt under
   the façade's lock. `Mutate(fn)` → one `Apply`. The **S3 actor** (05-config §1.12.1): from gin's request context,
   `Can("core:admin")` holds iff the request's permission set came from the seed `admin` role or auth is off;
   `Interactive` is `auth.Method.MayChangeCredentials()` or auth off; `Owner` is HEAD's owner proof.
4. **Protection (09 §9.2.6, 05-config §1.3.7).** Every integration-bound blob field (URL, host, credentials, headers,
   `auth_ref`, row creation) is `ProtectAdmin` for authenticated principals; a URL-origin change clears that instance's
   stored secret in place unless the same request re-supplies it. `HEADAuthOffWritable` keys stay writable by the
   auth-disabled principal after the owner-trust-root check, into HEAD's fields (the password into HEAD's encrypted
   row). File-only keys are `ProtectOperator` while the books store is attached.
5. **Precedence and reports.** `BelowStored` names feed the default layer; a `BelowStored` name of a `ProtectAdmin`+
   key that differs from the stored value goes to `AttachReport.Shadowed`, warns, and health reports
   `config_legacy_name_shadowed` through HEAD's degraded token list; compose's defaults (`BASIC_AUTH_ENABLED=false`,
   `BASIC_AUTH_USERNAME=admin`) go to `DeployDefaults` at info with no token. Two values for one protected key under a
   canonical and a legacy name stop boot (`TestAliasConflictOnProtectedKeyRefusesBoot`).
6. **Writers.** Convert every non-store writer of the blob (`update_service.go`, `scheduler_admin.go`, any
   `system/handler.go` remainder, `persistence.go`'s exported save) to `Mutate`/`Apply`, then empty the allow-list of
   `TestOnlyLegacyConfigStoreWritesSettingsRows` (PC-09-04).
7. **Basic gate.** Insert stage 8 into the chain at its position (05-http-api §4.2: after stage 7, before 9), delete
   `basicauth.go`, its tests and `router.Use(servermiddleware.BasicAuth())`. `TestPreMountRejection_RecordedByStage2`
   gains the stage-8 401 cell if it exists at your base.
8. **Decode oracle.** Switch `TestLegacyEnvDecodeMatchesHEAD`'s current side to core `Load` + aliases; every cell equals
   `testdata/legacy_decode.golden`, and each `expect: boot_error` cell now asserts the boot error.
9. **Marker.** `ci/stage.txt`: `shipped_stage: S3`, `release: S3`.
10. **Release notes** (prepend to the generated notes in the PR description; the coordinator carries them): 09 §9.2.1
    rows 1, 2, 3 with their texts (digest "Rollback costs"); 05-config §1.12.2 "Release note (S3)"; "A value such as
    `ENABLE_AUTH=yes`, which older releases silently read as off, now stops the service at start with a message naming
    it; use `true` or `false`." (09 §9.2.5); "API keys can no longer change integration URLs or credentials in
    Settings; use an interactive admin session." and "From this release `--log-level` and `--playlists` override the
    value saved in Settings." (09 §9.2.6); row 12's Basic-gate 401 metric line.
11. **Stage ledger.** Record both values of every generated count you touched and the boot-budget run
    (`scripts/boot_budget.py --prev <bin> --cur <bin> --db-copy <dir> --runs 5` on a fixture copy) in
    `docs/plans/platform-core/stage-ledger/S3.md` if the coordinator asks you to; otherwise list them in the PR.
12. Headers, `changelog.d/` fragment, acceptance.

## Code contract

`Facade` (name fixed by 05-config §1.12.1; fields coined by you, unexported). Package-level names keep their HEAD
signatures: `AppConfig`, `Snapshot()`, `Mutate(fn func(*Config)) error` (keep HEAD's exact signature), `Generation()`,
`SettingLocks()`, `EnvLockedSettings()`. `config.Actor{Subject, Interactive, Owner, Can}` (05-config §1.3.3).

## Done-check tests (names fixed by 09 S3 and 05-config; all green in this PR)

`TestLegacyEnvDecodeMatchesHEAD` (current side = core `Load`), `TestComposeEnvKeepsUIBasicGate` (compose fixture on S3:
stage 8 on, `<username>:` 401, stored password 200), the `config_legacy_name_shadowed` health case
(`TestAttach_LegacyNameShadowedReported` and `TestLegacyAuthEnablePrecedenceMatchesHEAD`),
`TestAliasConflictOnProtectedKeyRefusesBoot`, `TestPreS7Facade_IntegrationURLIsProtectAdmin` (S3 variant),
`TestRollback_BasicGateNeverOpenOnPrev`, `TestRollback_BasicGateClearedStaysClosed`,
`TestRollback_RotatedKeyNotResurrected`, `TestRollback_PlaintextSecretSavedOnCurWorksOnPrev`,
`TestRollback_ClearedOnPrevClearedOnCur`, `TestRollback_BlobEditOnPrevEffectiveOnCur`, `TestLegacyBool_SilentFalseOnAuthKeyFailsBoot`,
`TestFileOnlyLegacyKeysNeverDBStored`, `TestAliasBelowStoredMatchesHEAD`, `TestP4dStep1_LegacyOpenBeforeSecrets`,
`TestNoStoreWriteBeforeCheckpoint`, `TestBasicGate_ExemptSetEqualsHEAD` and the Basic-gate half of
`TestPerimeterHonoursBlobOnlyValues` (now through the real chain), `TestEveryLegacyConfigKeyHasCanonical`,
`TestAppConfigRatchet` (05-config §1.12.1 item 3, added with stage 1 if absent). R-test config cells of 09 S3 Done when,
in `make compat-rehearsal`: save a new Basic password on cur, roll back (200 with it, 401 for `<username>:`), change it
on prev, roll forward (effective); save a webhook secret or Deluge password on cur, roll back (prev uses it), change on
prev, roll forward (cur uses it); rotate a gap-fill key on cur, roll back, save an unrelated setting on prev, roll
forward (cur uses the rotated key); clear on prev a secret cur wrote, roll forward (cleared); edit a key on prev, roll
forward (effective); a value set only through its `ABO_CORE_*` name on cur is unread on prev.

## Invariants (STOP conditions)

- The compose effective-config golden (PC-09-02) still equals HEAD's after the switch; a diff = STOP.
- No value moves storage: every key with a legacy blob field is still read and written in place; a test that shows a
  write into `c/config/` for such a key = STOP.
- The Basic gate is never open on any build of the R-test matrix.
- If `compat-rehearsal` shows any R-test cell red, do not mark it flaky: STOP and report.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                       # exit 0
grep -rn 'viper\.' --include=*.go cmd/root.go internal/config/ | grep -v _test | grep -v 'internal/config/legacy_' | wc -l   # 0 on the boot path (record any remaining non-boot use and justify it in the PR)
test -f internal/server/middleware/basicauth.go || echo deleted                      # deleted
grep -n 'shipped_stage: S3' ci/stage.txt && grep -n 'release: S3$' ci/stage.txt      # 2 hits
go test ./internal/config/... ./internal/server/... ./cmd/... -count=1 -race         # ok
make compat-rehearsal                                                                # exit 0 (both PREV_REF rows)
make compat-ledger                                                                   # exit 0
make -C core ci                                                                      # exit 0
make ci                                                                              # exit 0
```

## Out of scope

Deploy files (PC-09-07); identity (S4); the audit record (S4); the integrations split of `PUT /api/v1/config` (S7);
deleting `AppConfig` (optional ratchet, 05-config §1.12.1 item 3).

## Rollback

Binary swap (09 S3 Rollback; 05-config §1.12.2 rollback matrix): the blob, its plaintext secrets and the four encrypted
rows are current on every older build; rows 1-3 of 09 §9.2.1 apply. A revert PR is admissible before a production tag
contains the flip.

## Repo rules

Headers on every changed file; deleted files need none. `changelog.d/` fragment. Conventional commits, each with
`Stage-Release: S3 flip` and `Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase` only after the
soak gate passes; never `--admin`. Boot writers you add extend `TestBootCrashMatrix_ABO` in this PR.

## Planner notes

- `internal/config/facade.go` is listed as a file to modify, but it does not exist at the plan checkout and no earlier
  row creates it; this PR creates it.
- The row omits `internal/config/update_service.go` and `internal/server/handlers/scheduler_admin.go`, which call
  `SaveConfigToDatabase` at the plan checkout and must go through `Apply` at the flip, and the kernel chain file that
  lists the perimeter stages (stage 8's insertion point).
- `main.go` and `cmd/root.go` are also edited by W02, W03, W04, W07 and W11 tasks; serialized by wave order, no
  same-wave collision.
