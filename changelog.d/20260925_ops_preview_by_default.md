### Fixed

#### Operations preview by default: an omitted dry-run flag no longer runs live

Owner decision 2026-09-25: an operation that writes runs as a preview unless its
request says `dry_run: false`. Ten ops ran live on a request that stated no mode:

- `itunes.path-repair` had a plain `DryRun bool`, so `{}` (the generic trigger, or a
  retry of a row saved without the key) rewrote locations in the live iTunes library.
- `dedup.split-book-bulk-merge` merged and soft-deleted books when sent `items` without
  `dry_run`; only its HTTP handler defaulted to preview.
- Eight maintenance jobs advertised `dry_run: false`, which the dispatcher applies on
  omission: `backfill-file-hashes`, `backfill-itunes-positions`,
  `backfill-metadata-source-hash`, `backfill-sync-ids`, `cleanup-backups`,
  `enrich-book-files`, `recompute-itunes-paths`, `sweep-pebble-metrics-ttl`. They now
  advertise `true`; the Manual Fixes "Run" button takes the existing preview-then-apply
  flow for them, and the buttons that were already sending `dry_run: false` are unchanged.

No scheduler entry, startup hook, internal enqueue or script called any of these ops
without a mode, so no automated caller needed `dry_run: false` added.

#### Shared dry-run resolver and guards

New leaf package `internal/operations/opmode` (`ResolveDryRun`, `ParseDryRun`) replaces
the duplicated `parseAuthorOpDryRun` / `parseSchedulerAuthorDryRun` and the inline copies
in author-id-repair, author-path-link and repoint-missing-to-folder-audio. Seven plugin
ops that accepted only camelCase `dryRun` (itunes-regroup, itunes-playlist-import,
tag-backfill, booksig-sidecar-migrate, fs-regroup-xml, booksig-recovery-audit,
title-backfill) and `operations.backfill-legacy-status` now take `*bool` with both
spellings; `{}` already previewed there, but `dry_run: false` was silently ignored.

`TestGuard_NoPlainBoolDryRunParams` scans `internal/` and `pkg/` and fails on any new
plain-bool `dry_run`/`dryRun` params field; `TestMaintenanceJobs_PreviewByDefault`
iterates the job registry and fails on a job that runs live on omission without a
recorded reason. Full inventory: `docs/audits/2026-09-25-op-preview-default-inventory.md`.
