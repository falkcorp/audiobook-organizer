<!-- file: docs/plans/platform-core/digests/S2b.md -->
<!-- version: 1.0.1 -->
<!-- guid: d75bb966-3b64-4a12-b028-e2d1fad564cf -->
<!-- last-edited: 2026-11-04 -->

# Digest S2b — Core opens the store; books on claims (releases `S2br1` shadow, `S2br2` strict)

Normative digest of `docs/specs/platform-core/09-abo-on-core.md` §9.4 "S2b" and what it points at (03, 04 §8.1, 05-storage, 05-backup, 05-config, 05-health, 06). No review
provenance in the body; `[n]` marks a footnote with spec sections and review IDs. Precedence: 03 normative; 09 owns sequencing; the owning 05-*/06 file owns how a mechanism
works; 04 is a planning list. Counts are informative and regenerated at the stage's base commit [1]. Section 10 records the cross-file conflicts found while writing it and how the spec resolved them.

## 1. Goal

- Core `storage` opens the one Pebble store; books reaches its families only through declared claims; the guard, census, checkpoint and compat bookkeeping are core's. No
  family moves, nothing is copied, no claim changes owner or mode, **no grant exists**: books holds every family in HEAD's stored format.
- **Releases, IDs, trailers** [2]:

| Release | What | PR that starts it | Trailer |
|---|---|---|---|
| `S2br1` (shadow) | core `storage` opens the store; claims enforced in log-only mode (`core.storage.claims.mode=shadow`); core config reads through books' legacy config store | the first core-storage PR (core `storage` opens the store); sets `shipped_stage: S2b` and `release: S2br1`; `compat-rehearsal` runs on it | content `Stage-Release: S2b 1`; the flip PR `Stage-Release: S2b flip` |
| `S2br2` (strict) | `core.storage.claims.mode=strict` becomes the default | the PR that changes `release:` to `S2br2` (a dial step; `shadow` undoes it without a rollback) | `Stage-Release: S2b 2` on every PR of the release |

- S2b **lands live, PR by PR**: the compat ledger and the reset change (both legacy reset routes) are separate PRs, each with the per-PR prev-build tests; they can reach
  production through the 10-RC auto-promotion, so every PR is shippable on its own.
- User-visible: a factory reset or a database reset takes effect at the next restart (row 1 release note, section 7); legacy `POST /system/factory-reset` and `POST
  /system/reset` only stage.

## 2. Prerequisites

- S2 r3 merged and released, with the current `S2r3` row in `ci/soak.txt` ≥ 14 days old (UI-0 is off the chain: if `UI0` landed first, the predecessor is still the release
  `release:` named before it).
- `S2br2` needs the `S2br1` row, whose evidence is the persisted claim-violation record with **no entry whose `last_at` is at or after the row's `<since>`** (an entry for a
  writer fixed earlier stays in the record). `check_soak.py` refuses the `release:` change to `S2br2` and every `Stage-Release: S2b 2` PR until then.
- The S0-written fixture store is committed (input of `TestS2bOpensStoreWrittenByS0`). The S0 sudoers lines (restore-probe `find` over `restore/`) are already in place.
- Owner: P12 (free space ≥ store size before any boot that runs a migration step); P1 census counts.

## 3. Code moves and new packages

04 §8.1 row S2b (planning list; the PR's build and 03 §3.5's guards decide). Layers per 03 R4.

| New or moved | 03 layer | Note |
|---|---|---|
| `internal/database` storage files → `mediacore/storage` + `mediacore/internal/pebble` | L1 | `keyfamilies.go` **stays in ABO** as the claim generator's input; `internal/compactprogress` → `storage`; the Pebble collector → `internal/pebble` (gate: ABO's Open Library store opens through `storage.OpenAuxiliary`); its series keep HEAD's names |
| `storage/restorejournal` | L0 | record types, kinds and intent of the one swap journal (see S1 digest §4.2; adds `ResetKind`: `ResetStore`, `ResetFactory`; reset fields `delete_library_files`, `delete`, `hook_args`, `hook_paths`, `fresh_rows`) |
| `core/internal/restoreswap` | L1 | the one journaled directory swap (restore, revert, checkpoint, `KindReset`); imports only `storage` and its L0 leaves (03 R4); imported only by `kernel`; never imported by `storage` |
| `mediacore/compat` | L4 | headers, the process-local use counter, `Ledger` from `app.Options.CompatLedger` (nothing is persisted) |
| `config` grows | L3 | native `OverrideStore` over `c/config/`; `Manifest.LegacyStores.Config`; `config/configtest` (`OverrideStoreSuite`) |
| `legacy` grows | L5 | `legacy.Stores` (+`Handles`), `ConfigOpener`, `ConfigResetArgs`, `ConfigResetPlan`, `ConfigResetRun`, `ConfigOpenEnv.Reset`, `ConfigOpenEnv.Files` with `StateFiles`, `Maintenance`; `legacy/legacytest` config suite |
| `coretest/crashmatrix` | test-support | the public crash-matrix kit |
| `examples/hello` | L9 | gains its keyspace and one migration |
| `internal/database/dbtest` `newstore.go` | → `coretest` | |

ABO side: the residue of `internal/database` becomes `internal/books/store` on `storage.LegacyRange` (books' legacy guard projection, version-source adapter and exact census
stay in it; 04 §4.5). Every consumer of `database.Store` is rewritten (04 §8.6; about 850 mechanical call sites): split in reviewable PRs, import-path-only PRs apart from
behaviour PRs, every commit builds, `bench`-tagged files rewritten too. `ci/gates.txt` rows `store-width` and `store-references` are retired in the PR that deletes
`database.Store`, reason recorded. Storage opens at `core.storage.path` (05-storage §5S.4 step 1; Docker's `--db`, the unit's `DATABASE_PATH`), not under a state-directory
root. Mock conversion (`internal/database/mocks`) is in-stage or deferred behind `storetest` (owner decides) [3].

## 4. Contracts

**4.1 Claims and handles (05-storage §5S.3.4; 06 §6.4.4).**
```go
type Claim struct {
	Prefix string; Key string; Except []string; Mode ClaimMode; Serialized bool; TakesOver string; Description string
}
type ClaimMode uint8 // ClaimInvalid(0, rejected), ClaimReadWrite, ClaimReadOnly, ClaimAgingOut, ClaimRetired
type LegacyRange struct{ /* unexported */ } // Get, Scan, NewIterator, NewBatch, NewIndexedBatch, Snapshot, Set, Delete, DeleteRange, Barrier
type Spec struct {
	Keyspace bool; Level, MinVersionToServe int; Claims []Claim; Serialized []string; Migrations []Migration
	Legacy []LegacyMigration; Derived []DerivedIndex; ReadAdapters []ReadAdapter; LegacyGuard *LegacyGuardProjection; UpgradeFloor *UpgradeFloor
}
type StorageHandles struct { // module.Deps.Storage; Maintenance only for the module that declares legacy claims, nil offline
	Keyspace storage.Keyspace; Legacy storage.LegacyRange; Indexes index.Opener; Aux storage.AuxOpener
	Derived map[string]storage.Derived; Maintenance legacy.Maintenance
}
```
Claims resolve by longest match; boot fails on any overlap between owners; claim modes read-write/read-only/aging-out/retired; claims are of three kinds (prefix, exact key, prefix-with-exceptions). `storage` refuses reads **and** writes outside the handed keyspace and claims (`ErrOutsideKeyspace`).

**4.2 Reset staging (05-storage §5S.3.11, §5S.3.15; 06 §6.4.11).**
```go
type Maintenance interface { StageFactoryReset(ctx context.Context, requestedBy string, req storage.ResetRequest) (string, error) } // package legacy
type ConfigResetArgs interface { ResetArgs(ctx context.Context, req storage.ResetRequest) (ConfigResetPlan, error) }
type ConfigResetPlan struct { Hook []byte; Rows []storage.KV; LibraryRoot string; LibraryFiles int } // plus the hook directories recorded as hook_paths
type ResetRequest struct { // package storage
	Kind restorejournal.ResetKind `json:"kind"`; Confirm string `json:"confirm"` // must be "RESET"
	DeleteLibraryFiles bool `json:"delete_library_files,omitempty"` // refused for ResetStore
	LibraryRoot string `json:"library_root,omitempty"`; LibraryFileCount int `json:"library_file_count,omitempty"`
	LegacyConfirm bool `json:"-"`
}
func (r ResetRequest) Validate() error
type ResetStaged struct { ID string `json:"reset_id"`; LibraryRoot string `json:"library_root,omitempty"`; LibraryFileCount int `json:"library_file_count,omitempty"` }
// errors: ErrResetConfirmRequired, ErrResetRequestInvalid, ErrLibraryConfirmRequired, *LibraryConfirmError{Root string; Files int}, storage.ErrSwapPending
```
**4.3 Legacy config opener (05-config §1.3.3).** `type ConfigOpener interface{ Open(ctx, env ConfigOpenEnv) error }`; `ConfigOpenEnv{StateDir *os.Root; KeyringLegacy func() ([]byte, bool, error); Reset *ConfigResetRun; Files StateFiles; Logger *slog.Logger}`; `StateFiles{WriteFile(label, path string, data []byte, perm fs.FileMode) error; Rename(label, from, to string) error; Remove(label, path string) error; RemoveAll(label, path string) error}` (the labelled state filesystem; every state-file effect goes through it). `ConfigExternalWriter{Changed() <-chan struct{}}` stays until S3.

**4.4 `app` at S2b (06 §6.4.5, §6.4.10).**
```go
func OpenOffline(ctx context.Context, opts Options, readOnly bool, mods ...module.Module) (*Offline, error)
func (o *Offline) Storage(id module.ID) (module.StorageHandles, error); func (o *Offline) Close() error
func (o *Offline) StagingMarker() (m storage.StagingMarker, ok bool, err error); func (o *Offline) WriteStagingMarker(archiveSHA256 string) error
func ReadStagingMarker(ctx context.Context, opts Options) (m storage.StagingMarker, ok bool, err error)
func OfflineCommands(opts Options, mods ...module.Module) []Command // Command{Name, Short, Long string; Run func(ctx, args []string, stdout io.Writer) error}
func BinaryID(opts Options) (string, error) // delegates to buildid.Binary
```
`Options.CompatLedger []byte` is added; `Options.LegacyConfigStore` is **deleted** in the S2b PR that lets core `storage` open the store. `Manifest.Storage` (a `storage.Spec`) and `Manifest.LegacyStores` (`legacy.Stores{Config func(Handles) (config.OverrideStore, error); …}`) are added; `Deps.Storage` is offered.

**4.5 `compat` (09 §9.2.9).** `Surface string` (`"http"`, `"config"`, `"env"`, `"rbac_atom"`, `"task_kind"`, `"sse"`, `"webhook"`, `"ui_route"`); `Gate` (`GateWindow`,
`GatePermanent`); `Entry{Surface; Legacy, Canonical, Ledger string}`; `Ledger.Register(Entry) (*Handle, error)` (`ErrDuplicate`, `ErrUnknownSurface`, `ErrNoLedgerRow`);
`(*Handle).Use(entry string)` increments `<namespace>_compat_legacy_use_total{surface,name,entry}` (lock-free, process-local, never fails a request). The ledger is built by
the kernel from `app.Options.CompatLedger` plus core's embedded `core/compat/core-ledger.md` (permanent rows only) before listeners open; a row ID in both is a boot error.

**4.6 Config keys landing here (05-storage §5S.7):** `core.storage.path`, `core.storage.claims.mode` (`strict`/`shadow`; books' first release on core is `shadow`),
`core.storage.claims.violation_flush` (1m), `core.storage.swap.intent_ttl` (24h), `core.storage.swap.keep_previous` (168h), `core.storage.swap.reset_hook_timeout` (30m, not
hot-reloadable); all `ProtectOperator`.

## 5. Behaviour rules — storage, config, claims

1. **Boot (06 §6.5.1).** P0a runs the kernel's journaled swap over ABO's staged intent (resume, swap, swap-back, completion) and reports a restore boot; P2 opens storage and
  runs the guard (nothing written); P4a checkpoint or fresh-install batch; P4b migration DAG (books legacy steps 1..`LegacyCap` first, on books' own claims through its
  `LegacyRange`); P4c claims ledger, owed duties, pending sidecar identity; P4d step 1 runs books' legacy config store `Open` (HEAD's `loadConfigFromDB`, `initEncryption`,
  `guardAgainstKeyRegeneration`, then `syncConfigFromEnv` and `config.Validate`, inside that store, each failing exactly where HEAD's step class is Fatal; never a second
  Pebble open, never defaults written over the stored blob); Run (3b) commits the served ledger and `c/storage/boot`. `serve` runs no startup step of its own. The boot log
  names books' legacy config store as the `config` backend.
2. **New core keys only under `c/storage/`:** level stamps, served ledger, core migration bookkeeping `c/storage/ks/<owner>/`, claims ledger `c/storage/claims/<owner>`,
  persisted violations `c/storage/claimviol/<owner>/<op>/<family>` = `{first_at, last_at, count, mode, build}` (one coalescing writer, at most once per `violation_flush`,
  never inside the caller's batch; never a value), boot lineage `c/storage/boot` mirrored in `<state_dir>/storage.boot.json`. Fresh install = **one** batch (install marker,
  epoch, every owner's stamp at the build's level, applied record of every versioned step below each owner's lowest `Seed` step and of books legacy steps 1..`LegacyCap`,
  `done` per registered data migration, claim ledger, and on a reset boot the intent's `fresh_rows`); an empty store is exempt from the upgrade-floor check.
3. **Legacy lock discipline unchanged.** `saveRawBlob` (`internal/config/persistence.go:883-885`), `SaveConfigToDatabase`'s secret loop (`:1989-2006`) and the import-root
  writers (`pebble_store_importpaths.go:166,199-208`) write as at HEAD inside books' claimed families; neither release refuses them and shadow mode records no violation for
  them; no legacy family gets a storage write check. HEAD's every-boot writers move to books' start hook until their successors land: `ReconcileOpsV3WithV2` until S5,
  `ReconcileVerifiedEmails` until S4 r2.
4. **Config writes nothing before S3.** Core config writes nothing to the store; books' `Open` writes nothing either. HEAD's load-time writes: the `migrate*Blob` reshaping
  functions become pure read transforms (each consults the marker or the step's applied record); the marker-guarded one-shots (`log_retention_zero_migrated`,
  `embed_cover_art_default_on_migrated`, `maintenance_window_migrated`) become books versioned `Migration`s (`Additive`, `Seed: true`, run at P4b, writing HEAD's marker so an
  older build skips them; a fresh install runs them and so writes HEAD's three markers); the corrupt-secret repair is not run inside `Open`. A stored `embed_cover_art=false`
  or `log_retention_days=0` is never rewritten, on rollback either.
5. **Claims rollout.** Release 1: claims enforced in **shadow**; every violation (read or write, counted by `<namespace>_storage_claim_violations_total{owner,op,mode}`) is
  fixed by registering or correcting the writer; ABO's full test suite also runs in strict mode in CI from the start. Release 2: strict default after release 1 served
  production 14 days with no new persisted violation. `TestBooksClaimsGenerated`: books' claim list = 05-storage §5S.9.1's table.
6. **Both resets only stage (S2b on).** Books' gin handlers for `POST /system/factory-reset` (`ResetFactory`) and `POST /system/reset` (`ResetStore`, store and settings only,
  HEAD's `ResetSystem`, no file effects), behind HEAD's own admin and owner guards (gin's chain decides until S4 r2), call `legacy.Maintenance.StageFactoryReset(ctx,
  requestedBy, req)`; the handle is `module.StorageHandles.Maintenance` (nil offline; deleted with the gin handlers in the S4 r2 PR); **there is no core route before S4 r2**.
  `kernel.stageReset` calls `legacy.ConfigResetArgs.ResetArgs` for both kinds, then `restoreswap.Swapper.StageReset` with the captured hook arguments. A reset is `KindReset`
  of the one swap: one slot `restore/pending.json` with `kind`, created atomically (a second staging of any kind, in any process, gets `storage.ErrSwapPending`); one rejection
  rule (`expires_at`, `staged_by_build`, a moved Pebble open stamp); one journal; one token pair. Staging pre-validates every target and refuses a dangerous `RootDir` with
  400, nothing staged, when the reset will wipe it. Nothing destructive runs in the request and the handler writes no setting. `PebbleStore.Reset` (whole-range delete) is
  deleted with both callers (`handler.go:479,551`).
7. **Reset boot (05-storage §5S.3.15).** P0a rejects only a `pending.json`; once the intent is renamed to `restore/<id>/inflight.json` (before `set_aside_started`) the journal
  is **never rejected**, whichever core build boots it, however late: it resumes forward to `set_aside`. Each target is set aside beside itself on its own filesystem by
  idempotent journaled `moved` renames (a `Recreatable` auxiliary DB on another filesystem, e.g. books' Open Library store under `<RootDir>/openlibrary-dumps`, is handed to
  the hook for deletion); the operator config file and the gap-fill file `<db parent>/config.yaml` stay. A failing set-aside moves back what it moved **before** journaling
  `rejected` and boots the untouched store `degraded` (`factory_reset_failed`). The boot is a **fresh install** with a new install ID whose fresh-install batch stages HEAD's
  factory defaults (`factoryDefaultConfig`: `root_dir` empty, `setup_complete` false, so a stale `ENABLE_AUTH=false` loses). The swap journal stays `set_aside` until P4d step
  1 has rewritten the keyring header. For `ResetFactory`, HEAD's side effects (gap-fill file reset, Open Library data, library `RootDir` wipe) run in books' post-reset hook on
  the reset boot after the journal reads `applied`: it **never fails boot** (per-entry errors are logged, audited and reported as `factory_reset_cleanup_incomplete`), always
  records `hook_done` (only a crash reruns it), is bounded by `core.storage.swap.reset_hook_timeout` and exempt from the 60 s start limit, skips kept paths (the DB parent, the
  state directory, the set-aside destinations); with no app hook the kernel records `hook_done` itself. The manual undo (moving `previous/` back) also renames the old
  install's set-aside revocation journal back and restores the keyring header's install ID with `<app> secrets set-install-id`. P4d sweeps expired `previous/` directories
  after readiness.
8. **Tokens and rollback tooling.** While a restore or reset is staged or in flight, `CheckReader` reports `restore_pending` or `restore_inflight` (naming the kind; for a
  reset kept until `hook_done`), so `make rollback`, `deploy` and the updater refuse; `ROLLBACK_IGNORE_RESTORE=1` passes `restore_pending` only. From this PR ABO's
  `internal/updater` reads them through core `storage`'s `CheckReader` via the kernel binding. `--print-restore-journal` ships here. Staging either reset requests a restart
  through the lifecycle supervisor (exit 75).
9. **Offline opens.** From the PR that lets core open the store, every ABO subcommand that writes the store — `scan`, `playlist`, `tag`, `organize`, `diagnostics
  cleanup-invalid`, `staging-scrub`, `staging-restore`'s target, and the `Isolate` child mode — opens it through `app.OpenOffline` (guard, floor and claims checked, migrations
  refused with `ErrOfflinePendingSteps`, boot verdicts computed; a foreign open, missing or unreadable record or behind lineage gets its owed entries staged first and only
  then the open stamp and sidecar identity promoted, so an offline open never launders a foreign open). `diagnostics query` and the `bench`-tagged `dedup-bench` open read-only
  through it. `seed` is **not** an offline-open user: it is refused with "start `serve` once first" (help text and `docs/` say so). `diagnostics reserved-prefs` stays a
  break-glass direct open beside `pebble-inject-skip` and also repairs core's reserved records; `pebble-inject-skip`, `reserved-prefs` and `pid-census` remain foreign opens.
  `staging-restore` writes the marker through `app.Offline.WriteStagingMarker`; `staging-scrub` reads it through `ReadStagingMarker`.
10. **Pre-migration checkpoint.** Taken only when a migration step will actually run; free space ≥ live store size is checked first and fails closed; the checkpoint metric
  `<namespace>_storage_checkpoint_bytes` and the alert snippet ship with S2b; a checkpoint is reused only by a crash loop of the same upgrade attempt on the same store
  identity.
11. **Compat ledger.** `docs/compat-surfaces.md` stays the source; `internal/books/compat-surfaces.md` is a generated copy (`make compat-ledger`, `TestCompatLedgerCopyFresh`)
  that `internal/books` embeds and `main` passes as `app.Options.CompatLedger`; the ledger-row check of config and env aliases starts here.
12. **Concurrency rule (CLAUDE.md).** The `database.Store` split is mechanical and adds no loop; any new library-scale loop (including checkpoint or census code) declares its
  parallelism; legacy writers keep HEAD's keyed mutexes and the single-writer AST ratchet `TestLegacyFamiliesWrittenOnlyByLegacyStores` from the first PR in which a core
  service calls books' legacy store implementation.
13. **Crash matrix.** `coretest/crashmatrix` is required from the flip PR in two instances: core's `TestBootCrashMatrix` (over hello, `core-ci`) and ABO's
  `TestBootCrashMatrix_ABO` (root module, `extraction-gates`, books with HEAD fixtures and `prev` built from `PREV_REF`, using the complete writer table of 06 §6.5.1). With
  Pebble `vfs` error injection and a shim for state-directory files it kills the boot at every durable write, rename and fsync from P0a to Run (3b) and asserts: no duty lost,
  the journal resumes or rejects, the sidecars agree with the store, and both `cur` and `prev` boot. Every later PR that adds a boot-time durable write extends the instance
  that owns it, and adds its row to 06 §6.5.1's writer table, in the same PR.
14. **Boot budget.** Steady state p50(cur) ≤ max(1.10 × p50(prev), p50(prev) + 15 s); first-migration boot reports `<namespace>_kernel_boot_phase_seconds{phase,step}` against
  the owner-recorded ceiling.

## 6. Metrics

Pebble collector series keep their HEAD names under the namespace ABO passes; the seeded series contract golden diff removes and renames no family and adds only the kernel's
new storage families (`<namespace>_storage_level{owner,kind}`, `<namespace>_storage_checkpoint_bytes`, `<namespace>_storage_claim_violations_total`, `<namespace>_compat_*`);
the credentialed smoke scrape passes.

## 7. Rollback costs (09 §9.2.1)

- Binary swap (no data-shape change; `c/` keys are ignored by older builds). The dial `core.storage.claims.mode=shadow` undoes strict mode without a rollback.
- **Row 1 (from S2b): resets.** A core build's reset only stages; it takes effect at the next restart; `make rollback` refuses while one is staged or in flight; an older
  build's factory reset (`PebbleStore.Reset`) deletes the whole key range, `c/` and `m/` included, so core records go too; an older build's settings reset does not touch
  core-only keys (S3). Undoing a factory reset restores the store and settings but not the files the hook deleted.
- Release note (S2b): "A factory reset or a reset of the database on this release takes effect at the next restart; undoing a factory reset restores the database but not the
  library files or Open Library data it deleted, and files it could not delete are listed in the log and in health."

## 8. Done when (09 S2b "Done when", every item)

- Release 1's soak row `S2br1` in `ci/soak.txt` (evidence: no persisted violation newer than `<since>`).
- Crash matrix: `TestBootCrashMatrix`, `TestBootCrashMatrix_ABO` with the S2b scenarios (a staged restore without hooks reaching `applied` through `marker_written`; a kill
  between the first rename and `swapped` followed by `prev` with `ROLLBACK_IGNORE_RESTORE=1`, still refused because `inflight.json` exists from before the first rename).
- Storage and config: `TestBooksClaimsGenerated`; `TestS2bOpensStoreWrittenByS0`; `TestS2bAppConfigFromBlob` (a value set only in the blob is effective in `AppConfig`; a `PUT
  /api/v1/config` changing one field leaves every other non-default blob field byte-identical); the runtime half of `TestPerimeterHonoursBlobOnlyValues`;
  `TestS2bBooksWritesEveryFamily` in strict mode (includes a scan's root update and a settings save that changes a secret); `TestFreshInstallPostFloorBootsTwice`;
  `TestFreshInstallWritesHEADMarkers`; `TestFreshInstall_EmbedCoverArtFalseSurvivesRestart`; a store with a stale opv3 record and an unstamped email row is repaired on the
  first S2b boot; free-space check before checkpoints and the checkpoint metric present.
- Restore: staged-restore rehearsal on core storage (the kernel's journaled swap runs at P0a, a crash mid-swap resumes or swaps back, the boot reports a restore boot);
  `TestS2bResumesS1Journal`.
- Reset: `TestFactoryResetUnderClaims` (shadow and strict: no `ErrOutsideKeyspace`, next boot fresh); `TestFactoryResetPostFloorBoots`; `TestFactoryResetFromLegacyRoute_S2b`;
  `TestSystemResetStagesStoreOnlyReset_S2b`; `TestFactoryResetRejectedLeavesStoreAndSettings`; `TestFactoryResetIntent_RejectedAfterInterveningOpen`;
  `TestCheckReaderRefusesPendingReset`; `TestSwapSlotExclusive`; `TestStageFactoryReset_DangerousRootRefused`; `TestFactoryResetCrossDeviceTargets`;
  `TestFactoryResetSetAsideFailureKeepsStore`; `TestPostResetHook_RemoveErrorsStillDone`; `TestFactoryResetReadOnlyRootDirCompletes`; `TestPostResetHookSkipsKeptPaths`;
  `TestFactoryResetNativeAppCompletes`; `TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn`; `TestHotfixUpdater_RefusesWhileResetPending`.
- Offline opens: `TestOfflineOpenAfterForeignOpenKeepsEvidence`; `TestOfflineOpenIsNotForeign`; `TestCLISubcommandsUseOfflineOpen`.
- `unit-lifecycle`: under the committed unit, staging a reset brings the service back without operator action (`TestRestartRequest_ExitsRestartCode`).
- The ledger-row check of config and env aliases passes; the boot log names books' legacy config store as the `config` backend.
- Series: the contract test's golden diff removes and renames no family and adds only the kernel's new storage families; the smoke scrape passes. Every PR passes `make ci`;
  `compat-rehearsal` is required on the first core-storage (flip) PR and on the `release:` change; every PR carries the per-PR prev-build one-format tests.

## 9. Owner checkpoints (NOT AGENT WORK)

- The `S2r3` soak row before the first S2b flip; the `S2br1` row (14 days, claim-violation evidence) before `S2br2`; the `S2br2` row (no refusal after `<since>`) before S3.
- P12 disk headroom; P1 census counts; deploys of each S2b PR that writes state to the reference host; committing `ci/prev_ref.txt`; ticking the stage record's done-checks;
  the owner-recorded ceiling for the first-migration boot.
- Installing the checkpoint alert snippet that ships with S2b.

## 10. Conflicts (resolved in the spec)

- C1 (resolved, 03 owns layers): `core/internal/restoreswap` (L1) imports `storage` and its L0 leaves, never the reverse (03 R4, 05-backup). 04 §8.1 S2b's "never
  `storage`" is stale; 04 is generated and is corrected at its next regeneration.
- C2 (resolved, 05-storage owns the request type): `legacy.Maintenance.StageFactoryReset(ctx, requestedBy, req storage.ResetRequest)`, as section 4.2 quotes; 03 §3.3 now
  says so. 04 §8.1 S2b's `kind` form is stale until its next regeneration.

## Footnotes

[1] 09 §9.3 "Counts in done-checks are generated"; ADV-11. [2] 09 S2b "Releases", "Flip PR"; §9.2.1 trailer and soak; OPS6-8, OPS7-6, OPS7-8, OPS10-3, A-9, A-13, R9-8, R10-7.
[3] 04 §8.1 S2b, §4.5, §8.6; 04 open item 1 (mocks); MOD-6, MOD9-7; advisor wave plan splits S2b into engine and `LegacyRange` adapter, claim table and shadow mode, kernel
phases with the crash matrix, the swap with reset staging and `OpenOffline`, and `compat`. Also: data/keys, R4-6, COR4-1, COR6-5, DATA6-11, DATA7-13, COR5-7; lock discipline,
A-3, DATA5-11, R9-1; resets, R7-2, R8-1, SIMP8-1, SIMP8-4, DATA6-6, COR7-1, COR8-1, COR8-2, COR8-3, COR8-4, DATA8-1, OPS8-5, OPS8-6, OPS8-7, OPS8-8, OPS8-12, SEC7-4, SEC7-5,
SEC8-5, SEC9-4, R9-6, R9-7, DATA9-3, DATA9-4, DATA9-11, DATA8-12; offline opens, R7-4, R8-2, DATA7-11, DATA8-3, DATA8-4, SEC8-4, R9-9, R11-5; crash matrix, A-8, R9-7, ADV-4,
COR9-5; claims rollout, 05-storage §5S.9.4, A-3; compat, SIMP3-3, R4-4, A-14, MOD4-11, SIMP2-12; checkpoint, OPS-25, DATA2-18, DATA3-1, OPS3-14; metrics, A-5, OPS6-3;
`restoreswap` placement, COR7-10, COR8-5, COR8-6, SIMP8-8; staging marker via `app.Offline`, A-10, COR9-6.

Plan-phase consistency:

- PC-8, PC-9 — `restoreswap` imports `storage` and its L0 leaves (03 R4); `StageFactoryReset(ctx, requestedBy, req storage.ResetRequest)`; section 10 resolved; 04 is stale on both until regenerated.
