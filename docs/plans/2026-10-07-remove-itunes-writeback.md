<!-- file: docs/plans/2026-10-07-remove-itunes-writeback.md -->
<!-- version: 1.0.0 -->
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
