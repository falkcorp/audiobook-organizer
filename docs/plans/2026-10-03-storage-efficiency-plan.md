<!-- file: docs/plans/2026-10-03-storage-efficiency-plan.md -->
<!-- version: 1.4.0 -->
<!-- guid: e18dc87d-ee27-4372-a90a-e904900a79c1 -->
<!-- last-edited: 2026-10-03 -->

# Storage efficiency: implementation plan

Design: `docs/design/2026-10-03-storage-efficiency-design.md` (v1.3). This plan
turns it into tasks. Status: awaiting owner approval; no code written. Revised
2026-10-03 after the red-team workflow: 45 confirmed findings (F01-F43, F45,
F46) applied to design and plan together; F44 ("`DeleteBook` history is not
undo data") and F47 ("the Doctor Who exclusion and the archive rule do not
apply") were refuted and dropped. A critic fix pass followed the same day.

## Goal

Store changes, not copies; no request-path full scans; retention for every
unbounded family; fingerprints and transcripts in a separate signal store.
Cut over with no backward compatibility.

## How the work is run

- One worktree and one PR per task. Never edit main. Version headers bumped on
  every changed file. A changelog fragment per PR.
- **Releases B and C use an integration branch**, `storage/release-b` and
  `storage/release-c`. Every B and C task PR targets that branch, not main,
  and the review gate, CI and GitHub-only checks run against it. The branch is
  rebased on main at least daily (gate each step with `&&`, never a pipe).
  After each rebase run `go build ./...`, the B1 property test and the
  converter cut-at-every-step tests. Release A and all other work merge to
  main as usual. Nothing from B or C reaches main in pieces: main is deployed
  routinely (urgent fixes, local CI), and a merged B2 without B4/B6/B7 would
  put mixed formats on prod with no stamp change and no checkpoint, which P3
  forbids.
- Every task brief lives in `docs/plans/storage-efficiency/TASK-<id>.md` and is
  self-contained: goal, files, steps, the grep that re-verifies each anchor,
  tests, exit criteria, what not to touch.
- **Single-writer files:** `.interface-width-baseline` (one ratcheted count;
  two parallel PRs that change interface widths both edit it and conflict),
  `internal/database/iface_book.go`, `internal/database/mock_store.go` and
  `internal/database/mocks/mock_store.go`. At most one PR in a wave may touch
  them. The plan-auditor collision matrix lists them explicitly.
- Agent cap 6. Every agent prompt says "Do NOT spawn subagents". Agents commit
  work in progress every 15 minutes and push to their own branch.
- **Review gate before any push that changes a write path:** an adversarial
  reviewer agent runs probes against the branch. Findings are fixed before
  merge. Releases B and C get a second review by a different model.
- Deploy only with `make deploy-debug` from the primary checkout at `0 0`.
  After each deploy, confirm new ops and endpoints exist in the prod binary.
  The B and C cut-over deploys go through `scripts/deploy-cutover.sh` (A9).
- Destructive data steps: dry run, reviewable list, owner approval, apply by
  explicit ids.
- B and C cut-over PRs each add a dated `docs/executive-summaries/` entry and
  check off their `TODO.md` items, by the implementing agent, not a separate
  docs agent.

## Agents and models

| Role | Agent type | Model | Used for |
|---|---|---|---|
| Design owner, final review of B and C | main session | fable | spec, plan, converter and chokepoint review |
| Core storage implementer | `audiobook-organizer:go-specialist` | opus | chokepoints, converters, signal store, indexes |
| Mechanical implementer | `audiobook-organizer:go-specialist` | sonnet | metrics export, throttle, call-site moves, settings plumbing |
| Frontend | `typescript-specialist` | sonnet | version list UI, census page, held-list page |
| Adversarial reviewer | `code-reviewer` | opus | every write-path PR, with probe tests |
| Schema and index audit | `audiobook-organizer:schema-auditor` | opus | new key families, converters, purges |
| Error-path audit | `pr-review-toolkit:silent-failure-hunter` | sonnet | converters, reconcile, purge ops, cut-over framework |
| Test coverage audit | `pr-review-toolkit:pr-test-analyzer` | sonnet | B and C PRs |
| Evidence scout | `plan-op:repo-scout` | sonnet | call-site inventories before a task starts |
| Brief check | `plan-op:brief-verifier` | sonnet | each brief, cold read |
| Plan audit | `plan-op:plan-auditor` | sonnet | anchors, collision matrix, headers |
| Docs | `audiobook-organizer:docs-agent` | haiku | schema doc regeneration, changelog wording |

## Preconditions

- P-1. Merge before the release A tasks that share their files: #3698
  (merged 2026-10-03) and #3704 (merged since: `373ba19d2` on `main`) before A1, A3, A4, A5 and A7
  (`pebble_store.go`, `metrics/metrics.go`, `server_lifecycle.go`; A5 edits
  `server_lifecycle.go`, which #3704 also edits). A2, A6, A8 and A9 share no
  files with them and are not gated. Merge #3698, #3700, #3704 and #3699 (in that order; #3699
  rebases on #3698) before B0 starts, because they edit the book save path and
  the journal that B0 inventories.
- P-2. Record `zfs list -o space` for each store's dataset (both, once the
  signal store exists) and the pool. Measured 2026-10-03: 6.42 TB available;
  72.5 GB used, of which 38.3 GB is snapshots. Check the free-space rule from
  design section 9: free space on each store's dataset is at least that
  store's live size, because the checkpoint pins the pre-migration sstables
  until release E.

## Release A: measure and speed up (no format change)

| Task | What | Main files | Depends | Agent / model | Review |
|---|---|---|---|---|---|
| A1 | Export Pebble metrics to Prometheus (cache hit rate, read amp, L0 sublevels, compaction debt, write amplification per level: cumulative bytes in, bytes flushed and bytes compacted per level as counters, so write amplification over any window = (flushed + compacted) / bytes in, pebble's `LevelMetrics.WriteAmp`; per-store) | `internal/database/pebble_metrics_export.go` (new), `internal/metrics/metrics.go`, server wiring | P-1 (#3698, #3704) | go-specialist / sonnet | code-reviewer |
| A2 | Key-family registry (incl. `book:work:`) and `GET /diagnostics/db-census` (sstable properties, `EstimateDiskUsage`, retired-row counts, history-per-book distribution, cached; figures labelled "on-disk entries incl. tombstones and shadowed versions") | `internal/database/keyfamilies.go` (new), `internal/database/census.go` (new), `internal/server/handlers/diagnostics.go`, `wire_media_routes.go` | none | go-specialist / opus | schema-auditor |
| A3 | db-health and `/cache/stats` read the census; AI-scans size by prefix; `ScanPrefix`/`CountPrefix` safe upper bound | `handlers/diagnostics.go`, `handlers/cache.go`, `pebble_store.go` (`CountPrefix`, `ScanPrefix`, `KeyCount`), `ai_scan_store.go` | A2, P-1 | go-specialist / sonnet | code-reviewer |
| A4 | `storage_format` stamp plus sidecar checked in the shared store open, before the counter writes (`newPebbleStore` split into open, which opens and checks and writes nothing, and init); empty-at-open check stamps a fresh store current, a store with data and no stamp reads as 1; refuse a stamp or sidecar above the build (`StorageFormatTooNewError`); refuse an older stamp or a present `storage_migration` marker with "start serve to migrate", through an unexported open option that only `RunCutover` (B6a) will pass; `--print-storage-format`; `make rollback` rule: swap in `.prev` only when the store's stamp (sidecar) is not above `.prev --print-storage-format`, otherwise refuse, swap nothing, and print the restore-from-checkpoint steps with the `checkpoint_dir` read from `<store path>.migration-checkpoint` (B6a writes it; it does not exist in release A); a shared `FormatMajorVersion` constant replaces `pebble.FormatNewest` at `pebble_store.go:416` and `cmd/diagnostics.go:209` (lines at `373ba19d2`) (A7 applies it to `ai_scan_store.go:109` and `openlibrary/store.go:34`, because A1 and A3 edit those files first); `cmd/diagnostics` raw mode and `cmd/pebble-inject-skip` exempt from the guard by name (design 9) | `storage_format.go` (new), `pebble_store.go` (open, split into open/init), `store.go:1454-1477` (read only: every CLI entry point reaches the checked open through `InitializeStore`), `main.go`, `Makefile`, `Makefile.local.example`, `scripts/storage_format_guard.py` (new; Python, repo rule 4), `cmd/diagnostics.go` | P-1 (#3698, #3704) | go-specialist / opus | code-reviewer + silent-failure-hunter |
| A5 | Timeline: `stageOpRow(batch, old, new)` helper stages `opv2:open:` and `opv2:done:` in the same batch as the op row; every `opv2OpKey` writer goes through it (InsertOperationV2 with old=nil in one batch; UpdateOperationV2Status, ResetOperationV2ForResume, the CAS transition ~:400-450, the stamp at :1309, the record pruner's delete; the empty-change sites :382, :569, :586, :619, :670, :687); CI grep ratchet on raw Set/pebbleSetJSON of `opv2OpKey`; startup reconcile only when the stamp is missing, or when `AORG_OPSV2_TIMELINE_RECONCILE=1` is set for the first start after rolling forward past a rollback to a pre-A5 binary; exact predicate re-applied; `GetOpLogsV2` tail read | `pebble_store_ops_v2.go`, `pebble_store_ops_v2_timeline.go` (new), `server_lifecycle.go`, `docs/database-pebble-schema.md` (no change to `handlers/operations_v2.go`: `matched` and `scan_capped` keep their meaning because the store still sorts the whole window before truncating, as the briefs' README says) | P-1 (#3704) | go-specialist / opus | code-reviewer with an equivalence probe (old scan vs new, random op histories; kill between row write and commit or inject a batch failure, old and new must agree) |
| A6 | Progress-log throttle (shape change, 30 s, terminal) | `internal/operations/registry/reporter_db.go` | none | go-specialist / sonnet | code-reviewer |
| A7 | Store-open settings from environment, defaults unchanged, exact v2.1.7 names: `Options.Cache` (`pebble.NewCache(size)`, one cache shared by main and OpenLibrary; the creator keeps its reference for the process lifetime rather than calling `Unref` after both opens, because the OpenLibrary store closes and reopens and tests reopen stores in one process), `Options.MemTableSize`, `Options.CompactionConcurrencyRange func() (lower, upper int)`, `Options.Levels[i].FilterPolicy` (`bloom.FilterPolicy(10)` for 0-5, `pebble.NoFilterPolicy` for 6), `DB.Compact(ctx, start, end, parallelize)` with parallelize taken from an environment toggle at `pebble_store.go:5356`, `ai_scan_store.go:175` and `openlibrary/store.go:50` (default false; the operator step turns it on); the A4 `FormatMajorVersion` constant applied to the AI-scan and OpenLibrary opens (`ai_scan_store.go:109`, `openlibrary/store.go:34`); raise `MemoryMax` in the unit using the formula in design section 8 with a comment stating it | `pebble_store.go` (open), `ai_scan_store.go`, `openlibrary/store.go`, `plugins/maintenance/db.go`, `deploy/audiobook-organizer.service` | A1, A3, A4, P-1 | go-specialist / sonnet | code-reviewer |
| A8 | Rebuild the rehearsal sandbox (torn down 2026-07-18): update `falkcorp/infra-docs scripts/dedup-sandbox` so (1) the DB image is an atomic ZFS snapshot of the ao-appdata dataset cloned to a sandbox dataset on the same pool, not copied to `/var/lib` or `/tmp` (16 GB tmpfs; the clone replaces the rsync and both `/tmp` copies; reset = destroy and re-clone); (2) the instance runs with prod's `GOMEMLIMIT` and `GOGC` under `systemd-run --scope -p MemoryMax=<prod value>`; (3) the existing unshare mount namespace and media clone stay, and the scheduler, op resume, scans and outbound metadata/LLM fetchers are disabled for the run; (4) the July isolation test is re-run. For C9 only, the signal store sits on the sibling dataset C8a chooses | `infra-docs/scripts/dedup-sandbox/*` | A2 | main session | owner |
| A9 | `scripts/deploy-cutover.sh` (a wrapper under 20 lines, repo rule 4) over `scripts/deploy_cutover.py`, called by `Makefile.local` on every deploy: (1) after the binary is copied to the host and before the `mv`, run the new binary's `--print-storage-format` on the host (N) and read the store's sidecar stamp; only when N is above the stamp, `sudo cp $(DEPLOY_BIN) $(DEPLOY_BIN).pre-format-<N>`, and refuse (non-zero exit, nothing installed) if a file of that name exists; when N is not above the stamp (every routine deploy), no copy; (2) install and restart; (3) poll the app's status endpoint (B6b listener while migrating, normal API afterwards) and print step and progress; (4) exit 0 only when the app reports the new version, not migrating, and the sidecar reads N; (5) exit non-zero on `stopped:<reason>` or timeout (rehearsal duration x 2 once B9 has measured it, a stated default before; an unreachable endpoint counts toward the timeout, which also covers a crash loop); (6) keep `deploy-preflight` in front. Change `Makefile.local.example`'s deploy targets to call it, and its post-deploy check to accept a matching version while `migrating` is true. Owner adds the one-line call to `deploy-debug` in the untracked `Makefile.local` | `scripts/deploy-cutover.sh` (new), `scripts/deploy_cutover.py` (new) and its test, `Makefile.local.example` | A4, A8 | go-specialist / sonnet | silent-failure-hunter |

Waves: W1 = A2 and A6 now; A1, A4 and A5 once #3704 is merged. W2 = A3 and
A8. W3 = A7 and A9. A3 and A7 both edit `pebble_store.go` and
`ai_scan_store.go`, so A7 waits for A3. A1 and A4 share no file because the
OpenLibrary and AI-scan format pins are in A7. A9 waits for A4 (the flag and
the sidecar) and A8 (its sandbox check).

Exit criteria per task, beyond tests:

- A4: the guard refuses a store whose stamp is above the build; a test shows
  that opening a store at the pinned `FormatMajorVersion` leaves its on-disk
  format version unchanged; a fresh in-memory store and a testutil store come
  out stamped current; the guard script swaps only when the store's stamp is
  not above `.prev`'s format, and otherwise refuses and prints the checkpoint
  restore steps. The TASK-A4 doc comment says the converter raises the stamp
  before its first delete, not at the end.
- A7: the runbook includes installing the changed unit on the server (copy to
  `/etc/systemd/system`, daemon-reload, restart), because `deploy/` is not the
  installed copy; the cache step is not taken until `systemctl show -p
  MemoryMax` on the server shows the new value; a sandbox timing of one
  parallel full compaction compared with a serial one is recorded before the
  operator step that turns parallel manual compaction on; write amplification
  recorded before and after each step.
- A8: the sandbox opens, and A2's census on the sandbox matches prod's census
  taken at snapshot time.
- A9: stub tests (the Python script's injected command runner and HTTP
  client) cover: N above the stamp makes the copy; N equal to the stamp skips
  it; an existing `.pre-format-<N>` refuses and installs nothing; an
  unreachable endpoint times out with a non-zero exit; a canned
  `stopped:<reason>` exits non-zero; serving at N exits 0. One routine deploy
  to the A8 sandbox exits 0 and creates no `.pre-format-*` file. The checks
  that need release B (a `.pre-format-<N>` file from a real format change, and
  a non-zero exit on a forced 1% stop while polling the B6b listener) are B9
  pass criteria.

Operator steps after A7 is deployed, one per deploy, each with the metric it
should move and 24 hours of baseline before the first: install the unit with
the raised `MemoryMax` and confirm it; block cache 4 GB (record the service
cgroup's `memory.peak` before and 24 h after; roll back if peak is above 80%
of `MemoryMax`); bloom 10 bits on L0-L5; memtable 64 MB; compaction
concurrency 1-4 with parallel manual compaction.

Exit: census numbers recorded as the before-figures (per family keys and
bytes; retired books and files; history entries per book; fingerprints and
transcripts counted); timeline under 100 ms on prod; db-health under 1 s.
Compare the census bytes for `book_ver:` against the design's estimate (25-37
GB for primary books, section 4.9). A code comment at `pebble_store.go:3363` (2026-08-29; also `:3387`) puts the whole library at about 7.65 GB. If the census total is
below 12 GB, restate section 4.9 and the expected-effect claims with the
measured figure before writing the B briefs. Release B still goes ahead for
write amplification and history correctness. If any legacy record shape is
held or cannot be decoded, the B7 brief names a handler for it, or the owner
accepts it, before B7 starts.

## Release B: book history cut-over

All B tasks merge into `storage/release-b`.

| Task | What | Depends | Exact files | Agent / model | Review |
|---|---|---|---|---|---|
| B0 | Scout: every writer of `book:` rows; every caller that relies on a write's side effects (`updated_at` readers, memdb resync, reindex, notify) | A, P-1 (all four PRs) | none (read-only scout; its inventory goes under `.claude/notes/`) | repo-scout / sonnet | none |
| B1 | `bookhist` package: `unversionedBookKeys` and its reflection test, post-hash, raw-JSON diff and apply, entry codec carrying `n`, `reconstruct` taking a `pebble.Reader` with exact-id check (`ErrVersionNotFound`), snapshot reads; property test against a full-copy oracle (`n` resets at every keyframe; pruning never changes `n` on kept entries) | B0 | `internal/bookhist/*.go` (new package: key list, codec, diff and apply, post-hash, `reconstruct`) with their `_test.go` files, the reflection test in an external `bookhist_test` package (no import cycle with `database.Book`) and the property test | go-specialist / opus | code-reviewer / opus, then fable |
| B2 | Book chokepoint: monotonic ids, change entry or keyframe from the newest entry's `n` (no backward walk; test: a write reads exactly one history entry), no entry on no change with the counter split (byte-identical vs unversioned-only), optional pin reason laying a pinned keyframe in the same batch and returning its id, `PinBookVersion` under the stripe and the history capability interface it sits on, signature sidecar Set only when its bytes differ (test: a one-field edit to a signed book writes no `booksig` key), `CreateBook` refusal, signature migration routed through it, CI ratchet | B1 | `internal/database/pebble_store.go` (`CreateBook` `:2511`, `UpdateBook` `:2750`), `pebble_store_book_lock.go` (`ModifyBook` `:89`, `ClearBookSignature` `:231`), `pebble_store_booksig.go` (sidecar Set `:199`), `pebble_store_booksig_migrate.go` (`:243`, `:355`), `keyfamilies.go` (register `bookhist:`), `book_history.go` (new: history capability, `PinBookVersion`) and its tests incl. the ratchet; single-writer files `iface_book.go`, `mock_store.go`, `mocks/mock_store.go`, `.interface-width-baseline` only if a `Store` signature changes (B2 is alone in its wave) | go-specialist / opus | code-reviewer / opus with a probe that runs `reconstruct` (live-row base) alongside a stream of `UpdateBook` calls and never sees `ErrVersionChainBroken`, and a probe that pin and a chokepoint write give distinct increasing ids; then fable |
| B3 | Merge pins: `MergeBooks` passes the pin reason on its first write to every participant and returns pre-merge ids; journal patch stores them; delete `newestSnapshotNanos`, `preMergeSnapshotNanos`, per-id baselines; remove `GetBookSnapshots` from `dedup.Store` (`internal/dedup/store.go:69`) and update the dedup mocks and tests; pins never released by code in this release (prune and archive treat journal-referenced pins as permanent) | B2 | `internal/dedup/auto_resolve.go` (`newestSnapshotNanos` `:367`, `preMergeSnapshotNanos` `:378`, `UnmergeAuto` `:407`), `internal/dedup/merge_journaled.go` (`:133`, `:180`, `:190`), `internal/dedup/store.go` (`:69`), `internal/database/dedup_automerge_journal.go` (journal patch fields), `internal/merge/service.go` (`MergeBooks` `:326`, `MergeBooksWithOptions` `:341`), `internal/dedup/merge_journaled_paths_test.go` and the `internal/merge/*_test.go` files that stub the old baselines | go-specialist / opus | code-reviewer with merge-undo probes: (1) a concurrent `ModifyBook` lands between the provisional journal write and `MergeBooks`' first write and `UnmergeAuto` keeps it; (2) a 3-book merge whose predicted winner differs from the real one gets correct ids for all three; (3) a participant whose merge write changes nothing still gets a pinned id; (4) pin alongside `DeleteBook` then `CreateBook` with the same id leaves the new book's `bookhist` range empty |
| B4 | Store surface and consumers: list, get (exact id), revert, `LastHistoryValue`; handler; audit; tag comparison; `handlers/metadata/handler.go` (revert-metadata, must keep failing on a non-version timestamp), `ChangeLog.tsx` (check only), `service_single.go` (fallback reached on error); delete `GetBookSnapshots`, `BookSnapshot`, `PruneBookSnapshots` (`pebble_store.go:3392`, `iface_book.go:315`, `metadata/interfaces.go:86`, `maintenance/job.go:266`; and the other consumers a grep finds at `373ba19d2`: `plugins/maintenance/deps.go:52` and `booksig_recovery_audit.go:250` call `GetBookSnapshots`; `audiobooks/service.go:53` and `service_single.go:169` call `GetBookAtVersion`; `dedup/store.go:101` and `dedup/auto_resolve.go:421`, `:428` call `RevertBookToVersion`), old prune job, the `cow-versions/prune` route (`wire_metadata_routes.go:36`), its handler (`exported.go:52-53`, `handler.go:766-789`) and its `openapi.json` entry (`:2429`); test that the route returns 404; mocks regenerated | B2, B3 | `internal/database/pebble_store.go` (`GetBookSnapshots` `:3231`, `GetBookAtVersion` `:3272`, `RevertBookToVersion` `:3297`, `PruneBookSnapshots` `:3392`), `book_history_read.go` (new), `iface_book.go` (`:235`, `:236`, `:314`, `:315`), `mock_store.go`, `mocks/mock_store.go`, `.interface-width-baseline`, `store.go` (`BookSnapshot`), `pebble_store_test.go`, `store_coverage_test.go`; `internal/server/handlers/metadata/{handler.go,exported.go,interfaces.go,handler_test.go,mocks/mock_metadata_store.go}`; `internal/server/wire_metadata_routes.go`; `internal/maintenance/job.go`, `internal/maintenance/jobs/prune_book_snapshots.go` and its test (delete); `internal/plugins/maintenance/{deps.go,booksig_recovery_audit.go,booksig_recovery_audit_test.go}`; `internal/audiobooks/{service.go,service_single.go,audiobook_service_tags_test.go}`; `internal/dedup/{store.go,auto_resolve.go,merge_journaled.go}`; test doubles in `internal/batch/service_test.go`, `internal/sweep/sweeper_test.go`, `internal/itunes/rebuild_test.go`; `docs/api/openapi.json` (`:2429`) | go-specialist / sonnet | code-reviewer / opus with revert probes (revert to a keyframe-based and to a change-entry-based version equals the full-copy oracle and `post_hash` matches; the next ordinary write after a revert produces a correct entry; revert and get with a non-entry T return `ErrVersionNotFound` and leave the row unchanged; a T older than the oldest retained entry returns `ErrVersionNotFound`; tag comparison with a non-exact `snapshot_ts` reaches the activity-log fallback; merge undo after its pin is pruned reports an error) + pr-test-analyzer |
| B5 | UI: version list shows kind, pinned, changed fields; remove the Prune button, `handlePrune` (`MetadataHistory.tsx:182`, `:382`) and `pruneBookVersions` (`api.ts:4954`). Ships in the same release B build as B4 so the button never points at a 404 | B4 | `web/src/components/MetadataHistory.tsx` (`handlePrune` `:182`, button `:382`), `MetadataHistory.test.tsx`, `web/src/services/api.ts` (`pruneBookVersions` `:4954`); `ChangeLog.tsx` is checked, not edited | typescript-specialist / sonnet | code-reviewer |
| B6a | Cut-over framework, serve-only (`database.RunCutover`, called from serveCmd after the config file and flags, logging and TLS settings are loaded and before `initializeStore`; stamp-driven, not a `migrations.go` versioned entry): uses A4's open/init split and its RunCutover-only open option; checkpoint per store beside each store under `.migration-backups/` with the device check and link-count checks, only when no marker exists (`ensureSameDevice` and `deviceIDFn` are unexported in `internal/backup`, `backup.go:557-577`; export the check and a test seam for the device function; `internal/backup` does not import `database`, so there is no cycle); test: with `deviceIDFn` stubbed to report a cross-device layout, the migration refuses and converts nothing (stops in the migrating state with the reason; `.migration-backups/` empty; stamp, marker, sidecar and `.migration-checkpoint` file unchanged; legacy key count unchanged); stamp raised plus `storage_migration` marker written, sidecar rewritten and `<store path>.migration-checkpoint` written before the first delete; group-committed indexed-batch converter pool (bounded, `NumCPU` default, disjoint book-id ranges, correct when workers finish out of order) with resume from remaining legacy; rolling 1% stop over live-row records (orphan class excluded); stop states keep the process alive; invariant gates with bounded counts; marker cleared after the gates. Every other `InitializeStore` caller (CLI subcommands in `cmd/root.go`, `seed`, `dedup_bench`, `diagnostics` in its store-backed mode, child mode, testutil, `pid-census`) refuses an older or in-progress stamp, exits non-zero and prints "start serve to migrate". Exempt by name: `cmd/diagnostics` raw Pebble mode and `cmd/pebble-inject-skip`, which are recovery tools (raw mode only reads; inject-skip writes two `setting:` keys outside every changed family; neither can ratchet the on-disk format, design 9); test: both still open a store the guard refuses. Signal-store pairing check lives in the same shared open function (C1 extends it) | A4, B2 | `internal/database/cutover.go` (new: `RunCutover`, the step-registration seam B7 uses, an in-memory held recorder B6c later backs with the persisted list) with its tests; `internal/backup/backup.go` (`ensureSameDevice`, `deviceIDFn`: export the check and a test seam) and `internal/backup/checkpoint_staging_test.go`; `cmd/root.go` (`serveCmd` `:250`, the `initializeStore` call inside it) and `cmd/root_test.go`; `cmd/cutover_refusal_test.go` (new: the refusal tests for the other `InitializeStore` callers) | go-specialist / opus | silent-failure-hunter + schema-auditor |
| B6b | Minimal read-only status listener started before `server.NewServer` from the port and TLS settings the cmd layer passes in: `/api/v1/system/version` with real version plus `migrating: true`; JSON state, step, progress, held count, stop reason, ETA; no record contents, no writes, nothing that needs the store-backed persisted config and settings, settings encryption, sessions or API keys, or telemetry (the config file, flags, logging and TLS settings are loaded); honours the A9 polling contract | B6a | `internal/server/migration_status_listener.go` (new, with its test); `cmd/root.go` and `cmd/root_test.go` (start the listener before `server.NewServer`); reads `RunCutover`'s status accessor and edits nothing in `cutover.go` | go-specialist / opus | silent-failure-hunter + code-reviewer |
| B6c | Persisted held list and orphan list (reason, first differing field, idempotent re-hold on resume), read-only count in the listener, authenticated read API served after normal startup, refused-write (`ErrRecordHeld`) metric. Runs after B4: both may touch the single-writer files (B4 deletes from `iface_book.go` and regenerates the mocks; B6c's read API may be a store method; a capability interface is preferred). The page is B6d | B6a, B4, B6b | `internal/database/storage_held.go` (new: persisted held and orphan lists, `ErrRecordHeld`), `internal/database/cutover.go` (swap B6a's in-memory recorder for the persisted list through its seam), `internal/database/keyfamilies.go` (register the new families), `internal/metrics/metrics.go` (refused-write counter), `internal/server/migration_status_listener.go` (read-only held count), `internal/server/handlers/system/storage_held.go` (new read API) and `internal/server/wire_system_routes.go` (route beside `:28`), `docs/api/openapi.json`; a capability interface, so no `iface_*.go` | go-specialist / sonnet | silent-failure-hunter |
| B6d | Held-list page: read-only list of held and orphan records from the B6c API, grouped by reason with a count per reason, first differing field, filter by reason, link to the book; shows the refused-write count; no actions (rulings go through `storage.convert-held`, B7a); Vitest for empty, loading, error and populated states. After B5 because both edit `web/src/services/api.ts` | B6c, B5 | `web/src/pages/StorageHeld.tsx` (new, with its Vitest file), `web/src/App.tsx` (lazy import beside `Diagnostics` `:37`, and its route), `web/src/components/layout/Sidebar.tsx`, `web/src/services/api.ts` | typescript-specialist / sonnet | code-reviewer |
| B7 | History converter (legacy copies to change entries with `n`; pins for every nonzero journal TS in every `dedup:automerge:` entry; "journal references with no version" list for zero or missing TS; inline signature move finished; orphan ranges listed, not converted), verified through `reconstruct` over the indexed batch with raw-byte compare, point deletes of verified keys only, per-book raw post-commit count, `N_read = N_verified + N_held`; cut-at-every-step tests | B1, B6a | `internal/database/bookhist_convert.go` (new, with the cut-at-every-step tests), `internal/database/pebble_store_booksig_migrate.go` (finish the inline signature move); registers through B6a's seam, so no edit to `cutover.go`; reads `dedup_automerge_journal.go` without editing it | go-specialist / opus | code-reviewer / opus, then fable |
| B7a | `storage.convert-held` op (authenticated, post-startup, explicit book ids, dry run / list / apply, reconvert or purge-with-loss-record; anchors the legacy chain on `reconstruct(oldest new entry)` for a book that already has `bookhist:` entries; orphan ranges to orphan record or purge by owner ruling); anchoring test: a held book is written to after cut-over, then converted, and its full chain verifies | B7, B6c | `internal/plugins/maintenance/storage_convert_held.go` (new, with its test), `internal/plugins/maintenance/plugin.go` (defs list, beside `pruneAIJournalDef` `:98`); reads B6c's list API | go-specialist / opus | silent-failure-hunter + schema-auditor |
| B8 | `DeleteBook` removes history in the stripe-held batch except for books whose row or history holds a transcript, which keep a final keyframe and the range and are counted; nightly prune op (one book at a time under the stripe, explicit `Concurrency`, first run dry), reached through B2's history capability interface, not `database.Store`, so B8 touches no single-writer file | B4 | `internal/database/pebble_store.go` (`DeleteBook` `:3423`), `book_history_prune.go` (new), `internal/plugins/maintenance/bookhist_prune.go` (new, with its test) and `plugin.go` (defs list `:98`), `internal/scheduler/tasks.go` (`ai_journal_prune` precedent `:1177`), `internal/scheduler/maintenance.go` (`:187`), `internal/scheduler/scheduler.go` (`:202`), `internal/config/config.go` (retention setting, precedent `:1424`) | go-specialist / opus | code-reviewer with delete probes (bookhist range empty for a non-transcript book; a transcript book keeps its range and is counted; pin release racing `DeleteBook` never resurrects a key) + schema-auditor + silent-failure-hunter |
| B9 | Sandbox rehearsal on the rebuilt sandbox (A8) of the tip of `storage/release-b` rebased on current main: time per phase (read, reconstruct/verify, write) in records/s and MB/s, total synced commits, peak L0 sublevels and compaction debt, cgroup `memory.peak` with the prod cache and memtable settings and the planned worker count, census, invariants, held count by reason and orphan count, deploy through `scripts/deploy-cutover.sh`, restore drill | B7, B7a, B8, B6d, A8, A9 | no code: `docs/executive-summaries/<date>-storage-release-b-executive-summary.md` and the `TODO.md` check-offs, in the cut-over PR | main session | owner |

Waves: B0; B1; B2; then B3 and B6a in parallel; then B4, B6b and B7; then
B5, B6c and B8; then B7a and B6d; then B9. All merged into
`storage/release-b`. B9 rehearses the tip of that branch, rebased on current
main.

**Exact files and the collision check (computed at `373ba19d2`).** Existing
files in the "Exact files" column come from grep of the symbols each task
names (`GetBookSnapshots`, `GetBookAtVersion`, `RevertBookToVersion`,
`newestSnapshotNanos`, `ensureSameDevice`, and the `pruneAIJournalDef`
registration precedent); new files carry the names given there, and every new
file has a `_test.go` sibling that is not listed separately. A brief may rename
a new file but keeps it out of every other task's list. Pairwise intersection
of the lists inside each wave:

| Wave | Pairs checked | Shared files |
|---|---|---|
| 4 | B3 / B6a | none (B3: `dedup`, `merge`, `dedup_automerge_journal.go`; B6a: `cutover.go`, `backup`, `cmd/root.go`) |
| 5 | B4 / B6b, B4 / B7, B6b / B7 | none (only B4 edits `pebble_store.go`; B7 adds `bookhist_convert.go` and touches `pebble_store_booksig_migrate.go`, which B4 does not) |
| 6 | B5 / B6c, B5 / B8, B6c / B8 | none (only B8 edits `pebble_store.go`; B6c adds `storage_held.go` and a capability, so it edits no `pebble_store.go` line) |
| 7 | B7a / B6d | none |

The three pairs the audit asked about (B3 / B6a, B4 / B7, B6c / B8) do not
collide on `pebble_store.go` or on any other file, so **no wave is
re-sequenced**. Two constraints keep it that way and go into the B6a and B6c
briefs: B6a exposes the step-registration seam, so B7 and B6b add files
instead of editing `cutover.go`; and B6c reaches the held list through a
capability interface, not a `Store` method. Files shared by tasks in different
waves are already ordered by wave and `Depends`, and the later task rebases
onto the earlier one: `pebble_store.go` (B2, B4, B8),
`pebble_store_booksig_migrate.go` (B2, B7), `keyfamilies.go` (B2, B6c),
`iface_book.go`, `mock_store.go`, `mocks/mock_store.go` and
`.interface-width-baseline` (B2, B4), the three `dedup` files (B3, B4),
`docs/api/openapi.json` (B4, B6c), `cmd/root.go` and `cmd/root_test.go` (B6a,
B6b), `cutover.go` (B6a, B6c), `migration_status_listener.go` (B6b, B6c),
`web/src/services/api.ts` (B5, B6d) and the maintenance plugin's `plugin.go`
(B8, B7a). The matrix found two plan gaps, both fixed above: B6c reads the
listener that B6b creates, so B6c now lists B6b under Depends; and B4's
consumer list was incomplete (the maintenance plugin's `deps.go` and
`booksig_recovery_audit.go`, `audiobooks/service.go` and `service_single.go`,
and the `dedup` call sites of `RevertBookToVersion`), now named in its "What"
cell and its file list. One design dependency to settle in the B6a brief, not a
file collision: B7 holds records before B6c exists, so B6a's in-memory held
recorder (a seam B6c later backs with the persisted list) is what B7's held
tests run against.

Single-writer files per wave (`.interface-width-baseline`,
`internal/database/iface_book.go`, `internal/database/mock_store.go`,
`internal/database/mocks/mock_store.go`):

| Wave | Tasks | Task that may touch them |
|---|---|---|
| 1-3 | B0; B1; B2 | B2 (alone in its wave) |
| 4 | B3, B6a | none (B3 edits `dedup.Store` and the dedup mocks, not the database mocks) |
| 5 | B4, B6b, B7 | B4 only (deletes `GetBookSnapshots` `iface_book.go:235` and `PruneBookSnapshots` `:315`, regenerates the mocks, lowers the baseline) |
| 6 | B5, B6c, B8 | B6c only; B8 goes through B2's history capability |
| 7 | B7a, B6d | none |

B9 pass criteria, each one blocking the prod cut-over if it fails: migration
completes; held count reported by reason (as C9 does), and it is 0 or the
held list, by reason, goes to the owner; orphan count reported; the
rehearsal deploys through `scripts/deploy-cutover.sh`, which leaves
`.pre-format-<N>` on the box, and a forced 1% stop makes it exit non-zero
while it polls the B6b listener (the A9 checks that need release B); every invariant count is equal; wall time is recorded and becomes
the downtime figure the owner approves; the restore drill (stop, swap in the
checkpoint named by the marker's `checkpoint_dir`, start the previous
release's binary `.pre-format-<N>`, not the new build, version list renders)
completes and is timed, and passes only if that binary opens the store and
serves; peak cgroup memory is under 80% of the new `MemoryMax`; Pebble
write-stall count recorded; the drill records what a restore after serving
would discard (operation records and iTunes write-backs since the cut-over)
and rehearses one record-level repair read from the checkpoint. B6a/B7
tests: kill at every staged write, restart, exactly one checkpoint exists and
is byte-equal to the pre-migration store; kill after one converted batch,
open with the previous `SupportedStorageFormat`, `StorageFormatTooNewError`
from the stamp and from the sidecar; force the 1% stop, process stays up,
version endpoint reports migrating, no new checkpoint on restart;
`WarmFromPebble` / `beginMemWarmupBuffering` never run while the stamp is
older; a non-serve entry point refuses, and `diagnostics query --raw` and
`pebble-inject-skip` still open the refused store; with `deviceIDFn` stubbed
to report a cross-device layout the migration refuses and converts nothing
(no checkpoint, no stamp or marker change, legacy key count unchanged);
`TestMigrationUpFunctionsAreIdempotent`
does not reach the converter; a staged batch that differs from committed
state is seen by verification; a legacy copy with a key absent from `Book`
survives; a non-numeric suffix key and a book id containing `:` are held and
present after commit.

Cut-over: in the owner-approved downtime window, fast-forward main to the
rehearsed branch tip, then immediately `make deploy-debug` (through
`deploy-cutover.sh`) from the primary checkout. Main never holds an undeployed
format change. If main moved after the rehearsal, rebase and re-run B9's
invariant check before the merge.

## Release C: file records and signal store

All C tasks merge into `storage/release-c`, which branches from main after
release B is cut over.

| Task | What | Depends | Agent / model | Review |
|---|---|---|---|---|
| C0 | Scout: all 119 uses of `.AcoustIDFingerprint`, every use of the other moved fields (incl. `Book.IntroTranscription` writers, every reader of the four failure fields outside dedup/maintenance/scanner/reconcile/organizer/metafetch), every raw writer of `book_file:` rows, every reader of the version and duration fields; every `fpwin` caller (Put/Get/Delete/CarryOver/WindowsForFile, the cascade, the backfill planner) and the prod `fpwin:` key count; `hydrateBookSig` callers and every `BookSig*` consumer (dedup `bookSignature` and conflict cache, dataset builder, registry `ReqFieldSet`, acoustid synthesize, recovery audit, search payload coverage); every caller relying on a `book_file` write's side effects or `updated_at` (`UpsertBookFileToMemDB`, `InvalidateLibraryStats`, `MarkQuickQueryDirty("no_fingerprints")`; `abs/mapper.go:343-344`, `:809`, `:1033`; `handler_files.go:96`, `:164`; `GetUpdatedAt` in `fingerprint.FileWithFingerprint`); flag every `GetBookFileByID` call with an empty book id (today the two Tier-0 LSH reads) as a known-dead path for Q7 | B | repo-scout / sonnet | none |
| C1 | Signal store: open and close order, environment settings per design section 8 (4 KB blocks, bloom, cache sized from A1 metrics), explicit `FormatValueSeparation` pin and `Experimental.ValueSeparationPolicy` (test: write a value of at least 64 KiB, flush, `Checkpoint`, open the checkpoint with `ErrorIfNotExists`, read back with matching checksum), pairing stamp in the shared open function (so pid-census and CLI subcommands get it), `ErrorIfNotExists`, content-addressed put with read-back, batched put for the converter, verified get, content-addressed `orphan:file:`/`orphan:book:` and `booksig:<bookID>:<sum>` keys, `held:file:` and `txb:` families | A4 | go-specialist / opus | code-reviewer / opus + silent-failure-hunter |
| C2 | Accessor API on the store: `bfsig:<fileID>:<kind>` keys (not a row field) with memdb side map; signal accessor takes a main-store `pebble.Reader`; `Set`/`Clear` take the owner stripe, re-read, refuse on `file_hash` change (`ErrStaleFingerprint`), stage old-key deletes, `fpidx`, `fpidx_meta` (recording its sum) and `bfsig` in one batch; move reads `fpidx_meta` inside the `commitBookFileBatch` locked section; `Get/Set/ClearBookSignature` (put before stripe, `sigref` under stripe; Clear row-only), `Get/Set/ClearBookTranscript`, `GetBookFileFingerprintForRow`; orphan write inside the three delete primitives (read back before the main delete commits), and each of the three stages deletion of the file's `bfsig:` keys, the `fpidx` keys its `fpidx_meta` names, and its `fpidx_meta` in the same main batch as the row delete (test per primitive: afterwards those keys are gone and an `orphan:file:` record exists for every former `bfsig:` sum; a cut between the orphan write and the main commit leaves row, references and index intact); `DeleteBook` orphan write before the stripes with re-read; window API moved to the signal store, cascade/`DeleteFingerprintWindows`/`CarryOver` write orphan markers and never delete bytes | C1 | go-specialist / opus | code-reviewer / opus with race probes via `bookFileBeforeCommitHook` ((a) stale batch upsert across a `Set`, (b) move across a `Set`, (c) two concurrent `Set`s on one file, (d) `Set` refused on a `file_hash` change; cut between orphan write and main delete), then fable |
| C3 | Call-site moves, in four disjoint slices: (1) dedup and fingerprint packages, including the dataset builder (`internal/dedup/dataset/builder.go:145`, `:273-284`, onto `GetBookSignature`) (Tier-0 reads follow the Q7 decision: revive with pair lookup plus the row overload, or delete; never moved mechanically onto `GetBookFileFingerprint(fileID)`; `WindowsForFile` builds the head row from the accessor); (2) maintenance plugins (incl. `intro_transcribe.go`, `intro_migrate_single_file.go`, the recovery audit `booksig_recovery_audit.go`); (3) scanner, reconcile, organizer, metafetch; (4) server/handlers/audiobooks `handler_files.go` (drop `intro_transcription` and `fingerprint_diagnostic_json`, add `has_fingerprint`, `fp_version`, `transcript_len` from `bfsig`), `server/fingerprint_diagnosis_handler.go`, `diagnosis/probe.go`, `plugins/acoustid/lsh_backfill.go`, `database/signal_coverage.go`, and the remaining `BookSig*` consumers C0 lists: acoustid signature synthesis (`plugins/acoustid/backfill.go`, `plugin.go`), the registry `book_sig_v1` `ReqFieldSet` predicate (`operations/registry/deps.go:106-112`; it has no store handle, so it reads `sigref` from the row and never calls the signal store) and the search payload's coverage (`audiobooks/service_search_cache.go:119`, from `sigref.coverage_pct` on the row; removing the `withSig` parameter of `GetBooksForSearch` and `abs/handler.go:177` stays in C4). Every `BookSig*` consumer C0 lists has exactly one owning slice. No slice edits `iface_*.go`, the mocks or `.interface-width-baseline`; C4 owns them | C2 | 4 x go-specialist / sonnet | code-reviewer per slice |
| C4 | Type change: `AcoustIDFingerprint`, `IntroTranscription`, `FingerprintDiagnosticJSON`, raw version/duration removed from `BookFile` (`FailedAt`/`Reason`/`Detail` stay); `IntroTranscription` and the six `BookSig*` fields removed from `Book` (`sigref` added); `hydrateBookSig` deleted from `GetBookByID`, `GetBooksByIDs` and the search hydrate; guards deleted; memdb projection and reflection tests updated; merge judge unreferences under the owner stripes; admindebug `bookFileSpec` test that no signal key is PATCHable; test that a corrupt or missing `booksig:` entry has no effect on `GetBookByID` or search results | C3 | go-specialist / opus | code-reviewer / opus, then fable |
| C5 | File chokepoint changed-detection on the projection minus `updated_at` (stamp only after a difference is found); row-only write when no indexed field changed; file-row would-skip counter (row still written in C); recompute projection and its reflection test (fingerprint duration out; `Set`/`Clear` trigger recompute); memdb write-through moved into `commitBookFileBatch` after commit and before `unlock()`, no post-commit `UpsertBookFileToMemDB`/`DeleteBookFileFromMemDB` left in any file writer (grep check) | C4 | go-specialist / opus | code-reviewer with stale-index probes and a move-vs-`UpdateBookFile` probe under a hook that stalls the move after commit (memdb must equal Pebble before and after a following no-op write) |
| C6 | Reconcile op and metrics for the row-to-signal invariants: four-way classification (live reference: a `bfsig:` key, a `sigref` or a book transcript reference; archived `zarch:bfsig:` once G lands; orphan-recorded; unexplained); `fpidx_meta` iff current `bfsig`, `fpidx_meta.sum = bfsig.sum`, every `fpidx` value = current `book_id`, each its own count and metric; window invariants; `sigref`; test that deletes a row through each primitive and asserts its signal is classed orphan-recorded and the file's `bfsig:`, `fpidx` and `fpidx_meta` keys are gone | C2 | go-specialist / sonnet | silent-failure-hunter |
| C7 | File converter: raw JSON key surgery with raw Pebble batches (never memSync or the store chokepoints), group-committed signal batch then verified indexed main batch through the production signal accessor, read-back compared with the base64-decoded raw legacy value, `bfsig.version`/`duration_sec` copied verbatim (absent gives 0; version-or-duration-without-bytes held), `HasCurrentPrint` equality per record; `held:file:` verbatim copy for held rows; book signature move to `booksig:<bookID>:<sum>` + `sigref`; book transcript move (live and history values) to `txb:`, orphan records for the B-kept history ranges then release; `fpwin:`/`fpwin_fail:` moved raw with checksum appended; undo-ledger scan (`book_file_delete`, `book_file_repoint` OldValue blobs) storing fp/tx/diag and `orphan:file:` for rowless files, ledger value unchanged, gate count ledger-held found = stored; cut-at-every-step tests; fixtures for version-0 (key absent), current-version, version-with-no-bytes rows; test that a held row written to with `UpdateBookFile`/`UpsertBookFile` keeps its bytes, returns `ErrRecordHeld`, and `held:file:<id>` matches | C4, C1, B6a | go-specialist / opus | code-reviewer / opus, then fable |
| C8a | Signal store location and dataset layout (sibling dataset); per-store checkpoint paths under `.migration-backups/` beside each store; exit: same-device check passes, link-count check passes, free space on each dataset at least its live size | C1 | main session | owner |
| C8b | Two-store backup and restore: `CreateBackupWithCheckpoint` checkpoints main first then signals, each staged beside its own store, both in one archive; `RestoreBackupIn` always restores main, extracts the signal store only when none exists at the target, refuses with none anywhere; backup/restore handlers (`handlers/system/handler.go:615`, `:708`) and `organizer/service.go` `autoBackup` updated; reconcile runs once after restore. Tests: main is checkpointed before signals and an injected write between the two still resolves after restore; restore keeps an existing signal store; restore refuses with no signal store anywhere | C1, C8a | go-specialist / opus | silent-failure-hunter |
| C9 | Sandbox rehearsal on the rebuilt sandbox with the signal store on the C8a sibling dataset: time per phase, synced commits, L0/compaction debt, cgroup `memory.peak`, warmup before and after, `memdb warmup starting` logged only after the marker-cleared line then `memdb warmup published`, rows written by a no-change rescan with the would-skip count, timed forced full `lsh-index-build` and one acoustid signature-synthesis pass before (B build) and after (C build) with signal-store block-cache hit rate, Tier-0 numbers if revived (dedup full-scan wall time, candidate count, signal-store reads per print), held count by reason and orphan count, invariants, restore drill (previous binary; plus restore of an app-made archive onto an empty target with reconcile reporting 0 missing) | C7, C8b, C5, C6, A8, A9 | main session | owner |

Waves: C0; C1; C2; then C3 (four slices) and C6 and C8a in parallel; then
C4 and C8b; then C5 and C7; then C9. C9 pass criteria are B9's plus the C
items above. The cut-over merge and deploy follow the release B procedure.

## Release D: consolidate and purge

| Task | What | Depends | Agent / model |
|---|---|---|---|
| D1 | `maintenance.compact-op-logs`: eligibility only for `completed`/`failed`/`canceled` with `CompletedAt` > 1 day, re-checked under `opsMu` in the same critical section as the read and commit; chunked pack (`opv2:logpack:<op>:<chunk%06d>`, ~1 MB raw each, first seq and line count) and digest; `DeleteRange` bounded to `[first read, successor(last read))`; merge-on-repack for loose rows; transparent readers with reverse-seek tail and chunk-seeking paged reads; age-out tier (first run dry); explicit `Concurrency`. Tests: (a) a row in each `interrupted_*` status with `CompletedAt` two days old is not packed; (b) `RetryInterrupted` racing the pack never loses rows; (c) a row written after the read survives and the next run merges it so the full log equals every row in key order; (d) age-out skips `interrupted_*`; (e) a tail read of N lines from a 300k-line pack decompresses at most ceil(N / lines-per-chunk) + 1 chunks; (f) paged reads across a chunk boundary equal the unpacked rows | C (release) | go-specialist / opus |
| D2 | Operation record retention excluding `interrupted_*` and non-terminal statuses (test: an `interrupted_ask` row older than 180 days survives); own retention setting for the undo journal (the undo window); by-book journal index pruned in the same batch; the journal prune refuses to delete a `book_file_delete`/`book_file_repoint` row until the C7 ledger scan has stored its signal bytes (test: a pre-C ledger row holding a fingerprint survives the prune until moved) | C7 | go-specialist / sonnet |
| D3 | Legacy fingerprint-index rows: census against `fpidx_meta` and current prints (read-only, `NumCPU`), then purge op | C6 | go-specialist / sonnet |
| D4 | Fetch-cache rows whose book is gone; dead `operationlog:` family and its code | C | go-specialist / sonnet |

Each purge: dry run, list or aggregate with exceptions, owner approval,
capped runs, refuses on an empty reference set. Review: schema-auditor.

## Releases E, F, G

- E. Gate: held = 0 and orphan-listed = 0 for releases B and C, each by owner
  ruling (Q9); the converter deletion checks this and refuses otherwise. Then
  delete converters and migration-only readers; checkpoint and snapshot purge
  runbook; one parallel full compaction; after-census. (go-specialist /
  sonnet; owner signs off the purge.)
- F. Full no-op skip for book and file rows using the counters from B2 and C5
  and the inventories from B0 and C0, relying on the memdb write-through
  ordering from C5; merge `scanner.go:1069` `UpdateScanCache` and `:1093`
  `MarkNeedsRescan` into one store call (one book write per processed file);
  remaining per-row file loops moved to batch variants. C9's "rows written by
  a no-change rescan" is measured again after F; the target is near zero.
  (go-specialist / opus; code-reviewer with side-effect probes.)
- G. Archive: eligibility predicate (journal pins permanent, 4.6), move op and
  restore op per book under the book's write stripe with explicit
  `Concurrency`, `zarch:bfsig:` carried, scanner tombstone; reconcile reads
  `zarch:bfsig:`. Brief written after release A reports the retired counts.
  (go-specialist / opus; schema-auditor.)

Exit criteria shared by B7, B7a, B8, C6, C7, D1-D4 and G: concurrency is
explicit (a `RunItems` `Concurrency` field or a bounded pool); a `-race` test
runs the op with more than one worker; ops that write book rows while the app
is live take the stripe, and a test writes the same book concurrently. B6a
gets "the pool and the resume are correct when workers finish out of order".

## Test strategy

As in the design, section 10. Per task the brief names the tests. Gates per
PR: `go build ./...`, `go vet`, package tests with `-race`, `make ci` on
Woodpecker, the GitHub-only checks (interface width ratchet, errcheck
ratchet, coverage floor, leak scan).

## Rollback

There is none by design (owner decision 2026-10-03). Release A changes no
format and can be reverted like any PR. For B and C the way back is the
Pebble checkpoint the app took before migrating, at the path recorded in the
`storage_migration` marker's `checkpoint_dir` (never the newest
`.migration-backups/` entry), restored together with the previous build,
which is `<bin>.pre-format-<N>`, saved by `scripts/deploy-cutover.sh` before
it replaced the binary; that binary must open the checkpoint, which the
pinned Pebble `FormatMajorVersion` guarantees. `make rollback` swaps in `.prev`
only when the store's stamp is not above the format `.prev` supports;
otherwise it refuses, swaps nothing, and prints these steps with the
`checkpoint_dir` read from `<store path>.migration-checkpoint`.
Restore of the checkpoint is for failures before service resumes; afterwards
fix forward with `storage.convert-held`, using the checkpoint as a read-only
reference, unless the owner approves a restore after seeing the operations
and iTunes writes since the cut-over (Q6).

## Order of briefs

Briefs for release A are written now. Briefs for B and C are written after
release A's census is in, because the measured numbers decide batch sizes,
worker counts and whether any legacy shape needs its own handling. The
decision rules in release A's Exit apply before any B brief is written. Every
brief for a whole-library op carries the design's section 9 concurrency
paragraph.
