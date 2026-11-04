<!-- file: docs/agent-tasks/platform-core/PC-07-05-s2b-flip-kernel-opens-store.md -->
<!-- version: 1.0.0 -->
<!-- guid: 65928f73-e614-4235-a906-ed29ada2de69 -->
<!-- last-edited: 2026-11-04 -->

# PC-07-05 — S2b flip: the kernel opens the store

**Full title:** S2b flip: the kernel opens the store; books on generated shadow claims; books' legacy config
store over the kernel handle; ABO's crash-matrix instance.

| Field | Value |
|---|---|
| ID | PC-07-05 |
| Tier | **T4 (Opus, xhigh to max).** The store changes owner on every production boot. HEAD's init-phase writers, settings load, encryption init and key-regeneration guard must keep HEAD's semantics exactly, each failing where HEAD's step class is Fatal, with no second Pebble open and no default written over the stored blob. |
| Wave | W07 (stage S2b). **Flip PR of release `S2br1`.** |
| Depends on | PC-07-04, PC-08-01 |
| Trailer | `Stage-Release: S2b flip` on every commit (never `S2b 1` as well) |
| Marker | `ci/stage.txt`: `shipped_stage: S2r3` → `S2b`; `release: UI0` → `S2br1` (`release:` reads `UI0` because PC-08-01 merged first; check (a) then needs the current `S2r3` row, 09 §9.2.1) |
| Required checks | `compat-rehearsal` (flip), `unit-lifecycle` (flip), `extraction-gates` (now runs `TestBootCrashMatrix_ABO`), `core-ci`, `docker-image`, `vet-darwin`, `vet-integration` |
| Normative digest | `docs/plans/platform-core/digests/S2b.md` (all of it) |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-07-05 -b feat/pc-07-05-s2b-flip origin/main
cd "$REPO/../audiobook-organizer-pc-07-05"
git rebase origin/main
git worktree list
make web-deps          # CLAUDE.md worktree recipe (UI-0 wired core/web into it)
```

## Goal (09 S2b "Goal", "Moves", "Data/keys", "Crash matrix", "Done when")

ABO's `serve` stops opening Pebble. The kernel opens the one store at `core.storage.path` (ABO maps
`database_path` / `DATABASE_PATH`), runs P0a over any intent ABO's S1 code staged, and hands books a
`LegacyRange` over books' generated claims in **shadow** mode. Books' legacy config store runs HEAD's
settings load inside the kernel's handle. ABO's crash-matrix instance becomes a required gate.

## Exact files

Create:

- `internal/books/storage_claims.go` — the generated books claim list (05-storage §5S.9.1 fixes this path;
  the row says `internal/books/store/claims_gen.go`, see Planner notes) and its `go generate` tool under
  `internal/books/internal/claimgen/` (name coined here). Input: `KeyFamilies()` in
  `internal/books/store/keyfamilies.go`; the reserved `c/` and `m/` entries skipped; every row emitted in
  the mode and `Serialized` flag of §5S.9.1's table.
- `internal/books/store/claims_test.go` — `TestBooksClaimsGenerated`.
- `internal/books/crashmatrix_abo_test.go` — `TestBootCrashMatrix_ABO`.
- `internal/books/legacy_families_writers_test.go` — `TestLegacyFamiliesWrittenOnlyByLegacyStores` (config
  instance: only `internal/books/configstore` and `internal/books/store` write `setting:config_blob` and the
  four legacy secret rows).
- `internal/books/store/legacyrange.go` — the thin adapter mapping the used `GatedDB` subset (`Get`, `Set`,
  `Delete`, `DeleteRange`, `NewIter`, `NewBatch`, `NewIndexedBatch`, `NewSnapshot`, `LogData`) onto
  `storage.LegacyRange` (05-storage §5S.9.3; not in the row).

Modify:

- `main.go`, `cmd/root.go` — `serve` builds `app.New(ctx, opts, books.Module(...))` without opening a store;
  ABO's S1 boot-time swap runner is removed (the kernel's P0a runs instead); `--print-restore-journal`
  ships; the boot log names books' legacy config store as the `config` backend.
- `core/app/options.go` — delete `Options.LegacyConfigStore` (06 §6.4.10 S2b row; SIMP4-11).
- `internal/books/module.go` — `Manifest.Storage` (`Claims` from `storage_claims.go`, `Legacy` steps
  1..`LegacyCap`, the new `Migration`s below, `LegacyGuard`), `Manifest.LegacyStores{Config: ...}`, the start
  hook that runs `ReconcileOpsV3WithV2` (until S5), `ReconcileVerifiedEmails` (until S4 r2), the
  journal-clock repair and the identification-index stamp, in HEAD's order, before books serves.
- `internal/books/store/` — `NewPebbleStore(h module.StorageHandles)` becomes the serve constructor;
  HEAD's path-based `InitializeStore` stays **only** for the CLI subcommands (`scan`, `playlist`, `tag`,
  `organize`, `diagnostics`, `seed`, the `Isolate` child, `staging-*`) until PC-07-08 moves them to
  `app.OpenOffline` (Planner notes); `cmd/pid-census/main.go` (a permanent foreign open, 09 S2b) switches to
  a path-based constructor kept for it, `OpenPathForeign(path)` (name coined here); `globalStore` deleted; the engine files 05-storage §5S.9.3 lists as moving to core
  (`pebble_gate.go`, the open/format split of `storage_format.go` and `pebble_store.go:597-700`) deleted;
  HEAD's init-phase writers (`initPebbleStore`, `pebble_store.go:689-786` at HEAD) re-homed as §5S.9.3's row
  says: the `library:` → `import_path:` key migration and the list-index backfill become books
  `Migration`s (`Additive`, P4b, numbered from `LegacyCap+1`); counters treat an absent counter as HEAD's
  seed `1` (no seeding write); the storage-format stamp becomes the guard's legacy projection;
  `PebbleStore.Reset` is deleted (its two callers go 503, below).
- `internal/books/configstore/store.go` — implements `legacy.ConfigOpener.Open(ctx, env legacy.ConfigOpenEnv)`:
  HEAD's `loadConfigFromDB`, `initEncryption`, `guardAgainstKeyRegeneration`, then `syncConfigFromEnv` and
  `config.Validate`, over the kernel's one handle, each failing exactly where HEAD's step class is Fatal (04
  §8.3 per-step table); `InitEncryption`'s key-file write goes through `env.Files.WriteFile` with a label.
  `Open` writes nothing to the store.
- `internal/config/persistence.go` — the `migrate*Blob` functions become pure read transforms that consult
  the marker or the step's applied record; the three marker-guarded one-shots (`log_retention_zero_migrated`,
  `embed_cover_art_default_on_migrated`, `maintenance_window_migrated`) leave the load path and become books
  `Migration`s (`Additive`, `Seed: true`, P4b, writing HEAD's marker); the corrupt-secret repair is not run
  inside `Open` (05-config §1.12.2). `saveRawBlob` (`:883-885`) and `SaveConfigToDatabase`'s secret loop
  (`:1989-2006`) are **unchanged**: they keep HEAD's lock discipline.
- `internal/metrics/metrics.go` — stop registering ABO's Pebble collector; core's `internal/pebble` collector
  registers the same `audiobook_organizer_pebble_*` families under the namespace ABO passes.
- `internal/updater/updater.go` — read `restore_pending` / `restore_inflight` through core storage's
  `CheckReader` via the kernel binding (OPS8-8).
- `internal/arch/core_key_names_test.go` — `TestCoreHoldsNoAppKeyNames` reads `KeyFamilies()` at
  `internal/books/store` and the generator's output for owner `books`.
- `internal/server/handlers/system/handler.go` (not in the row) — `ResetSystem` and `FactoryReset` answer
  **503** with problem code `reset_unavailable` after HEAD's admin, owner and confirm checks, until PC-07-07
  replaces them (plan decision: no build runs HEAD's whole-range `PebbleStore.Reset` under shadow claims
  and a live kernel).
- `ci/stage.txt` — the marker change above.
- Core side not in the row (06 §6.4.10 S2b row; 04 §8.1 S2b): `core/config/` native `OverrideStore` over
  `c/config/` (read half) and `config/configtest` (`OverrideStoreSuite`); `core/legacy/` `Stores`, `Handles`,
  `ConfigOpener`, `ConfigOpenEnv{StateDir, KeyringLegacy, Reset, Files, Logger}`, `StateFiles`;
  `legacy/legacytest` config suite; `core/kernel/` builds `Manifest.LegacyStores.Config` at P4c and calls
  `Open` at P4d step 1. If PC-07-03 already added any of these, extend rather than duplicate.

Plus a `changelog.d/` fragment and the release-note line for S2br1 in the stage record (row 1's text lands
with PC-07-07).

## Re-verify before editing

```bash
grep -E '^(shipped_stage|release):' ci/stage.txt            # shipped_stage: S2r3 / release: UI0 (PC-08-01 merged)
ls internal/books/store/keyfamilies.go internal/books/configstore/store.go internal/books/module.go   # PC-07-04, PC-04-05
ls core/kernel/storage.go core/coretest/crashmatrix         # PC-07-03 (name coined there)
grep -n 'func KeyFamilies' internal/books/store/keyfamilies.go                  # 1 hit (:330 at HEAD's old path)
grep -n 'func saveRawBlob' internal/config/persistence.go                       # :883 at HEAD
grep -n 'configBlobSaveMu.Lock()' internal/config/persistence.go                # :1943 at HEAD
grep -n 'func (p \*PebbleStore) Reset()' internal/books/store/pebble_store.go   # :6310 at HEAD
grep -n 'store.Reset()' internal/server/handlers/system/handler.go              # 2 hits (:479, :551 at HEAD)
grep -n 'ReconcileOpsV3WithV2' internal/books/store/store.go                    # :1868 at HEAD
grep -n 'ReconcileVerifiedEmails' internal/books/store/pebble_store.go          # :759 at HEAD
grep -n 'LegacyConfigStore' core/app/options.go                                 # >= 1 hit (deleted here)
ls internal/books/store/testdata/s0-store                   # the S0-written fixture store (PC-02-01, moved by PC-07-04)
grep -c 'Prefix:\|Key:' internal/books/store/keyfamilies.go # informative; 258 families at 748fd787a
```

## Invariants

1. **One open.** No ABO code opens Pebble in `serve`; books' P6 constructor commits no write
   (`TestBooksConstructorWritesNothing`).
2. **Claims never change** owner, mode or flag; books holds all §5S.9.1 families; no core subsystem holds a
   `LegacyRange`; shadow mode records, never refuses; strict mode in CI refuses nothing (`TestS2bBooksWritesEveryFamily`
   in both modes).
3. **Config writes nothing before S3**; `Open` writes nothing to the store; a stored `embed_cover_art=false` or
   `log_retention_days=0` is never rewritten, including after a rollback.
4. **HEAD's writers keep HEAD's lock discipline** (`saveRawBlob`, the secret loop, the import-root writers at
   `pebble_store_importpaths.go:166,199-208`): no storage write check, no violation recorded for them.
5. **Every boot-time durable write is labelled** and in 06 §6.5.1's writer table; ABO's instance covers
   books' P4d step 1 `Open` (key-file write), books' start hooks and the hookless `marker_written` batch.
6. The `S2br1` default of `core.storage.claims.mode` for ABO is `shadow`; every other app's default stays
   `strict` (05-storage §5S.7). The key is `ProtectOperator` and not hot-reloadable. See Planner notes on
   where the default lives.
7. The metrics golden removes and renames no family; it adds only `<ns>_storage_level`,
   `<ns>_storage_checkpoint_bytes`, `<ns>_storage_claim_violations_total` (and `<ns>_compat_*` once PC-07-06
   lands).

## TDD: tests first (spec names)

`TestBooksClaimsGenerated`, `TestABOClaimsMatchOwnershipTable`, `TestS2bOpensStoreWrittenByS0`,
`TestS2bAppConfigFromBlob` (a blob-only value is effective in `AppConfig`; a `PUT /api/v1/config` changing one
field leaves every other non-default blob field byte-identical), the runtime half of
`TestPerimeterHonoursBlobOnlyValues`, `TestBootCrashMatrix_ABO` (S2b scenarios: a staged restore without hooks
reaching `applied` through `marker_written`; a kill between the first rename and `swapped` then `prev` with
`ROLLBACK_IGNORE_RESTORE=1`, still refused; plus the S1-journal resume of `TestS2bResumesS1Journal` through the
real P0a), `TestLegacyFamiliesWrittenOnlyByLegacyStores` (config instance), `TestFreshInstallPostFloorBootsTwice`,
`TestFreshInstallWritesHEADMarkers`, `TestFreshInstall_EmbedCoverArtFalseSurvivesRestart`,
`TestS2bBootRepairsOpsAndEmails` (the row's "a stale opv3 record and an unstamped email row are repaired on the
first S2b boot"), `TestS2bBooksWritesEveryFamily` (shadow and strict), `TestBooksConstructorWritesNothing`,
`TestLegacySteps59to66RunFirst`, `TestLegacyMigrationsReplayIdempotent`, `TestNoNewLegacyMigrations`,
`TestLegacyStoresAreSingletons` (config family), `TestLegacyStoresBuiltFromOwnerHandles`,
`TestLegacyConfigHonoured` (S2b half), `TestProjectionKeepsRecordID`, and two coined here:
`TestLegacyResetRoutesAnswer503` (both routes, after HEAD's guards and confirm, answer 503
`reset_unavailable` and change nothing) and `TestBootLogNamesLegacyConfigBackend`.

## Steps

1. Re-verify. Read 05-config §1.12.2 and 04 §8.3's per-step table before touching `persistence.go`.
2. Write the claim generator; generate; write `TestBooksClaimsGenerated` and `TestABOClaimsMatchOwnershipTable`
   counting by group (no literal 258).
3. Core side: native `OverrideStore` read half, `configtest`, the `legacy` S2b config members; kernel P4c/P4d
   step 1 for `LegacyStores.Config`.
4. Books store over handles: `legacyrange.go`, the new constructor, the re-homed init writers, `Reset` deleted.
5. Books' legacy config store `Open`; `persistence.go` pure transforms; the three `Seed` migrations.
6. `main.go` / `cmd/root.go` serve wiring; remove ABO's own P0a; `--print-restore-journal`.
7. Updater, metrics, arch test; the two reset handlers to 503.
8. ABO crash-matrix instance; extend 06 §6.5.1's writer table with every books label (spec doc edit, same PR).
9. Marker change in `ci/stage.txt` as the **last** commit's content (the whole PR carries `S2b flip`).
10. Acceptance; then `make compat-rehearsal` locally with `PREV_REF` from `ci/prev_ref.txt`.

## Acceptance

```bash
go build ./... && go -C core build ./...                                    # exit 0
go test -count=1 -run 'TestBooksClaimsGenerated|TestS2bOpensStoreWrittenByS0|TestS2bAppConfigFromBlob|TestFreshInstall' ./internal/books/...   # ok
go test -count=1 -run TestBootCrashMatrix_ABO ./internal/books/             # ok (PREV_REF set as in extraction-gates)
go test -count=1 -run TestCoreHoldsNoAppKeyNames ./internal/arch/           # ok
sed -n '/Use: *"serve"/,/^}/p' cmd/root.go | grep -c 'initializeStore\|pebble.Open'   # 0: serve opens no store
grep -n 'LegacyConfigStore' core/app/options.go main.go                      # no output
grep -E '^(shipped_stage|release):' ci/stage.txt                             # shipped_stage: S2b / release: S2br1
make compat-rehearsal                                                        # exit 0 (both PREV_REF rows)
make -C core ci && make ci                                                   # exit 0
```

`unit-lifecycle` green (the committed unit restarts a staged restore through exit 75). The boot-budget A/B
(`scripts/boot_budget.py --prev <bin> --cur <bin> --db-copy <dir> --runs 5`) on a staging copy is the owner's
(OWNER-07), not this PR's.

## STOP conditions

- A HEAD init-phase writer has no home that 05-storage §5S.9.3 or 05-config §1.12.2 names.
- Any test shows a default written over the stored blob, or a second Pebble open.
- `TestS2bBooksWritesEveryFamily` in strict mode refuses a write: register or fix the writer in this PR only
  if §5S.9.1 already lists its family; a family missing from `keyfamilies.go` is a STOP and report.
- `check_soak.py` refuses the marker change (the `S2r3` row is missing or not 14 days old).

## Out of scope

Reset staging (PC-07-07); `compat` (PC-07-06); offline opens and `seed`'s refusal (PC-07-08: until then the CLI
subcommands keep their own open, which the guard reads as a foreign open, costing one round of fallbacks);
strict default (PC-07-09); `internal/compactprogress` and `newstore.go` (Planner notes of PC-07-04).

## Rollback

Binary swap (`make rollback`): no data shape changes; the `c/storage/` keys are ignored by older builds. Dial:
none needed in r1. A revert of this PR is admitted by `check_soak.py` as an exact inverse.

## Repo rules

- Headers on every new or changed file; bump `06-app-sdk.md` for the writer-table rows.
- Every commit `Stage-Release: S2b flip` and `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n>
  --rebase`; never squash; never `--admin`.
- From this PR every boot-time durable write extends `TestBootCrashMatrix_ABO` in its own PR (09 §9.3).
- CLAUDE.md concurrency: the claim generator and census code add no runtime loop; the start hooks keep HEAD's
  order and parallelism.

## Planner notes

- **Generated-claims path.** 05-storage §5S.9.1 fixes `internal/books/storage_claims.go`; the row lists
  `internal/books/store/claims_gen.go`. The brief uses the spec's path.
- **`handler.go` missing from the row.** The plan decision (503 until PC-07-07) needs
  `internal/server/handlers/system/handler.go` in this PR; PC-07-07 also edits it (serial, so no collision).
- **`PebbleStore.Reset` deleted here, not in PC-07-07,** because the 503 handlers leave it without callers and
  `lint-deadcode-ratchet` in `make ci` would fail on new dead code.
- **`cmd/pid-census/main.go` missing from the row:** it calls `NewPebbleStore(path)`, whose signature this PR
  changes, so it moves to a kept path-based foreign-open constructor here.
- **Core files missing from the row:** `core/config/` (native read half, `configtest`), `core/legacy/` S2b config
  members, `legacy/legacytest`, `core/kernel/` P4c/P4d step 1, and `internal/books/store/legacyrange.go`.
- **Where ABO's `shadow` default lives is not specified.** 05-storage §5S.7 gives `strict` as the core default
  and "books' first release on core: shadow". PC-07-09 lists `core/storage/claims.go`, which suggests a core
  default change; that would make every app shadow. The brief requires an app-supplied default (coined
  mechanism, marked) so only ABO's builds default to `shadow`; PC-07-09's brief follows the same rule.
- **Offline opens are split from the flip against 09's wording.** 09 S2b says every ABO subcommand that writes
  the store opens through `app.OpenOffline` "from this stage's PR" (the flip). The plan puts that in PC-07-08,
  three PRs later, so builds between PC-07-05 and PC-07-08 (which the 10-RC auto-promotion can ship) keep the
  CLI's path-based open, which the guard reads as a foreign open (one round of fallbacks per CLI write).
  Either fold PC-07-08 into this PR or accept that window explicitly.
- **"Flip first" in the wave table** reads as "first state-writing PR"; PC-07-01 to PC-07-04 merge before it
  as dark content under `S2b 1`, which `check_soak.py` admits on the `S2r3` row.
