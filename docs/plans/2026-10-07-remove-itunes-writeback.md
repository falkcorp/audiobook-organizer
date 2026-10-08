<!-- file: docs/plans/2026-10-07-remove-itunes-writeback.md -->
<!-- version: 1.2.0 -->
<!-- guid: 7372ebf8-5194-48d4-af7b-3f85421f435f -->
<!-- last-edited: 2026-10-07 -->

# Remove iTunes write-back

## Goal

The owner decided on 2026-10-07 at 20:04: "Drop itunes writeback. We will do
import only and then not care." Remove every code path that writes the iTunes
library. iTunes stays a one-way import source.

Windows iTunes opens `.itunes-writeback/iTunes Library.itl`, so any write there
lands in the live library. Write-back has been pinned off on the host since
2026-10-07, through `ITUNES_WRITE_BACK_ENABLED=false` in the local.conf drop-in.

## Keep

- **iTunes import and sync** (iTunes into the DB): reading the XML/ITL, the
  import UI, import-status, and the PID / external-id records that import creates.
- **ITL read/parse code**, but only what import uses. Anything only the
  writers use goes.
- **Audio file tag write-back** (metadata written into .m4b/.mp3 tags). It is
  not iTunes. Do not touch it, even where it is also called "writeback".

## Remove

1. **Batcher and queue** (`internal/itunes/service/`):
   - `writeback_batcher.go`, `writeback_plan.go`, `writeback_requeue.go`, and
     their tests;
   - the durable queue keys `itunes_writeback:*`, with a one-shot cleanup of the
     leftover keys at startup;
   - held removes.
2. **ITL writers** (`internal/itunes/`):
   - the `itl_le_mutate`/`itl_combined_mutate`/`itl_le_remove_by_pid`/
     `itl_le_metadata_update` mutators;
   - `itl_safe_write`, `itl_safety_contract`, `itl_le_repair`/`itl_le_verify`
     (if they are write-only);
   - `rebuild`, `relocate_oracle`, `pid_repair` (the ITL side);
   - the writer half of `itl_identity_refresh`;
   - their tests and the golden/regression fixtures that only exist for writes.
3. **Server routes and handlers** (`internal/server/`):
   - `/itunes/rebuild`, `/rebuild-full`, `/relocate`, `/adopt-base`,
     `/cleanup-merged`, `/write-back`, `/write-back-all`, `/write-back/preview`,
     `/writeback/status`, `/writeback/held/release`, `/writeback/requeue`,
     `/writeback/requeue-remove`, `/library/upload`, `/library/restore`,
     `/library/backups`;
   - the `POST /operations/itunes-path-{reconcile,repair}` ops, if they write
     the ITL;
   - the files `itl_rebuild.go`, `itl_relocate.go`, `itl_cleanup.go`,
     `itunes_writeback_*.go`, and the ITL-writing parts of `itl_pid.go` and
     `itunes_path_ops.go`;
   - the owner-route entries for these routes, with `credential_routes_test`
     updated to match.
4. **Callers that enqueue iTunes writes.** Delete the enqueue calls and the
   interface methods they go through:
   - book edits (`audiobooks/service*`);
   - merge (`merge/`);
   - quarantine;
   - the organizer;
   - batch apply / save / apply-when-scanned;
   - duplicates ops;
   - ASIN backfill;
   - Repairs.

   Fixers that guard against the iTunes link (`itunesguard`, `skipped_itunes`,
   AllowITunesPath) stay. They protect DB rows that import relies on.
5. **Config**:
   - remove `itunes.write_back_enabled`, `auto_write_back`,
     `write_back_dry_run`, `library_write_path`, the write-back allowed-root and
     the max-removes settings, plus their env bindings and defaults;
   - keep import paths and path mappings;
   - unknown keys already in the DB must load without error;
   - drop the three `ITUNES_WRITE*` lines from local.conf after deploy.
6. **Frontend** (`web/src`):
   - remove `WriteBackPreviewTable` and the write-back, rebuild, relocate,
     upload, restore and adopt-base controls in iTunes settings;
   - remove `RelocateFileDialog`'s iTunes leg;
   - remove write-back status in `PendingFileOpsBanner`/ops store, if it is
     iTunes-only;
   - keep `ITunesImport`.
7. **Metrics, docs, todo**:
   - remove the write-back Prometheus metrics and Grafana panels;
   - mark the write-back plans (`2026-10-07-itunes-writeback-drops`,
     `-requeue`) as superseded;
   - check off or close the write-back TODO items;
   - add a changelog fragment and an executive summary (wide blast radius).

## Order

1. Inventory: grep and LSP `findReferences` on the batcher and the writer
   entry points. Classify each hit as a writer (remove), shared with import
   (keep), or file tags (keep). Commit the list into this plan.
2. Remove the callers (step 4), then the routes (3), then the batcher (1), then
   the writers (2), then config (5), then the UI (6). Each step builds:
   `go build ./... && go vet ./...`.
3. Run `go test ./...` short, the web build, and vitest.
4. Open one PR, merge, `make deploy-debug`. Confirm in the logs that import
   still runs and that no `itunes_writeback` lines appear.

## Tests

- **Import:** existing import/sync tests stay green.
- **Removed routes:** a router test asserts each removed route returns 404.
- **Config:** loading a DB config that still holds the removed keys
  succeeds.
- **Writers really gone:** `grep -r ApplyITLOperations` (and the other writer
  names) returns nothing outside deleted files.

## Rollback

Revert the PR. Write-back stays off on the host through local.conf until this
PR is deployed, so a revert puts back code that does nothing.

## Final inventory (2026-10-07)

Branch `refactor/remove-itunes-writeback`. Diff against `origin/main`: 85 files
deleted (47 Go production files, 34 Go test files, 2 web files, 2 PowerShell
scripts). About 29,000 lines removed.

### Removed

- **Routes (18).** `removed_itunes_routes_test.go` asserts each one returns 404
  and is absent from the router:
  - `/itunes/`: `rebuild`, `rebuild-full`, `export-partial`, `relocate`,
    `adopt-base`, `cleanup-merged`, `write-back`, `write-back-all`,
    `write-back/preview`, `writeback/status`, `writeback/held/release`,
    `writeback/requeue`, `writeback/requeue-remove`, `library/upload`,
    `library/restore`, `library/backups`;
  - `/operations/`: `itunes-path-reconcile`, `itunes-path-repair`.
- **Ops.** `itunes.path-reconcile` and `itunes.path-repair`, from the server and
  from the plugin stubs. Both are listed in `retiredOpIDs`, and their lines are
  gone from `write_op_modes.golden`. The `generate-itl-tests` maintenance job is
  removed and its ID is also retired, so `wantJobCount` drops from 38 to 37.
- **Batcher and queue** (`internal/itunes/service`):
  - the batcher, plan, requeue, enqueuer and register;
  - the track provisioner;
  - path reconcile, path repair and the resolver;
  - location normalize and lifecycle.
  - Startup cleanup: `purgeLegacyITunesWriteBackKeys` deletes
    `itunes_writeback:*` and `pref:_system:outbox:writeback:*`.
- **ITL writers** (`internal/itunes`):
  - the combined/LE mutators: remove-by-PID, metadata update, repair and verify;
  - safe-write, identity refresh, writeback root and the backup-name rotation;
  - the library-activity quiescence gate;
  - rebuild, relocate, relocate oracle and the sync cycle;
  - cleanup-merged and `GuardRebuildTarget`;
  - from `itl.go`: the update, insert, rewrite and playlist writers;
  - from `itl_le.go` / `itl_be.go`: the LE and BE location rewriters;
  - the test-ITL generators.
  - `grep ApplyITLOperations|SafeWriteITL|UpdateITLLocations|InsertITLTracks`
    now finds only comments.
  - The encoders that only test fixtures used moved to `itl_fixtures_test.go`.
- **Tools.** `cmd/itl-repair`, `itl-roundtrip`, `itl-write-test` and
  `itunes-sync-tests`. In `pid-census`, the `--sync-dry-run` and `--sync-apply`
  modes are gone.
- **Enqueue callers.** The calls are removed from:
  - book update/delete and metadata fetch/apply (handlers and metafetch);
  - merge (the loser PID read and `EnqueueRemove`);
  - quarantine (purge-pending and dirty marks) and organize;
  - batch apply, save and apply-when-scanned, and file-op recovery;
  - series normalize (duplicates), dedup-books, relink-missing-to-itunes and
    repoint-missing-to-folder-audio;
  - the importer's deferred ITL updates and track provisioning.
- **Config.**
  - Removed: `write_back_enabled`, `auto_write_back` and `write_back_dry_run`,
    with their env bindings and the rule that turned write-back on when
    `library_write_path` was set.
  - Old stored keys still load: `TestLoadConfigFromDatabase_RemovedITunesWriteBackKeysLoad`.
- **Owner-route gate.** `ownerRoute` / `ownerRoutes` are removed: every route on
  it was an iTunes writer. `ownerGateWhenOwnerSet` stays, for backup restore
  and reset.
- **Metrics.** `itunes_location_unmappable_total`. There are no Grafana panels
  in the repo.
- **Frontend.**
  - In `ITunesImport`: the write-back dialog (preview, sync-all and browse),
    the confirm and overwrite dialogs, and "Force Sync to iTunes";
  - `WriteBackPreviewTable`;
  - in `ITunesTransfer`: upload, install, backups and restore (download only
    now);
  - in `api.ts`: the write-back client and the config fields;
  - the e2e mock and the skipped write-back e2e test.
- **Position sync.** Seeding a finish no longer stamps
  `ITunesPlayCountBumpedAt`. The new test
  `TestPullITunesBookmarks_FinishSeedDoesNotWriteTheBook` covers it.

### Kept, and why

- **Import and sync** (iTunes into the DB), with the XML and ITL parsers,
  position pull, playlist pull, PID / external-id records, `pid-integrity`,
  `pid-repair` (DB rows only) and the library download.
- **`itl_safety_contract.go` + `itl_identity.go`.** The read-only audit tools
  `itl-check` and `itl-diff` call `AuditITL`. The identity sidecar guard (K13)
  is now reached only by tests.
- **`itunes.library_write_path`** (Go field renamed to `LibraryITLPath`; JSON
  and mapstructure tag unchanged). This is a deviation from step 5 of the plan.
  The value is the `.itl` path, and these read it:
  - the delete-protection guards: `audiobooks/helpers.go`,
    `metafetch/helpers.go` and `server_middleware.go`;
  - `itl_pid.go` and the stale-path fixer;
  - the deluge import, `fs_regroup_xml` and the download handler.
  Dropping it would have weakened the guards.
- **`itunesguard` / `skipped_itunes` / AllowITunesPath** in the fixers.
- **Audio file tag write-back.**
- **Merge `survivorHasPID` / HiddenFromABS iTunes-survivor logic and
  `combine_journal` `ITunesRemovals`.** These are journal and ABS behavior, not
  writers.
- **GET `/itunes/books`.** It is read-only, but its only UI was the removed
  browse tab.
- **`isAudiobookITL`** stays for the cross-type PID census. The target-shape
  guard around it (`LibraryShape`, `InspectLibraryShape`) is removed, along
  with the rebuild writers it protected.

### Not done / follow-ups

1. **`internal/writeback/`** (`enqueuer.go`, `outbox.go`) is imported by
   nothing. The session's permission classifier blocked `git rm -r` on it, so
   the owner should delete it by hand.
2. **ITunesPath drift reverts organized paths (confirmed by reading the code,
   not reproduced in a test).** Organize, rename, metafetch and repoint still
   write a computed `ITunesPath` onto `book_file` rows, and nothing moves
   iTunes to match it now.
   - iTunes sync matches a track by PID and skips it only when the stored
     `ITunesPath` equals the track's Location (`importer.go` near line 1057).
     Otherwise it upserts a row whose `FilePath` is the decoded iTunes
     location.
   - `BatchUpsertBookFiles` matches that row by PID. `FilePath` is
     `bfUpsertOwned` (`bookfile_merge.go:185`), so the iTunes path replaces the
     organized one.
   - This branch does not widen the exposure: write-back was already off on
     the host.
   - Two fix shapes:
     - (a) stop writing a computed `ITunesPath` (leave the imported Location);
     - (b) on a PID match, have sync leave `FilePath` alone.
3. **`ITunesPlayCountBumpedAt`** is now written only by merge's
   `carryPlayCountMark`. Both can be removed.
4. **Dead store methods:**
   - `GetITunesPurgePendingBooks`, `MarkITunesSynced` and `GetITunesDirtyBooks`;
   - the three deferred-iTunes-update methods;
   - `ListDirtyUserPlaylists`.
   They have no production callers but remain on `database.Store` and its mocks.
5. **`internal/logger` `TestGuard_NoDirectSlogCalls`** still fails, on eight
   files whose slog calls are not in this branch's diff. This branch
   lowered five ratchet counts and routed its new logging through
   `internal/logger`.
6. **Host:** drop the three `ITUNES_WRITE*` lines from `local.conf` after the
   deploy.

