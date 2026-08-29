<!-- file: PLAN.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5a1f8e34-72c9-4b60-8d1e-93af4c25b7e0 -->
<!-- last-edited: 2026-08-29 -->

# A database backup must never be able to kill the database

## Goal

Stop the production failure measured 2026-08-29: the pre-organize auto-backup
filled `/var/lib` to 100%, which made PebbleDB take a fatal commit error and the
process exit. systemd restarted it, the scan resumed, the backup ran again, and
the cycle repeated every ~17 minutes for hours. No library scan has ever
completed. Full evidence in `.claude/notes/2026-08-29-prod-outage-disk-full.md`.

Three defects combine:

1. **No pre-flight space check.** `CreateBackup` / `CreateBackupWithCheckpoint`
   `os.Create` the archive and stream into it until the write fails. On a full
   filesystem that consumes the last free bytes, and Pebble — writing its WAL to
   the same filesystem — dies with `no space left on device`.
2. **Retention runs only on success** (`backup.go:162`). When the disk is full
   the backup fails, so the prune that would have freed room never executes.
   Exactly backwards.
3. **Retention is count-based on files that grew ~60x.** `MaxBackups: 10` was
   written when an archive was 247 MB (2.5 GB total). Archives are now 15 GB, so
   the policy *targets 150 GB on a 141 GB disk* — retention now guarantees the
   outage instead of preventing it.

## Files to change

- `internal/diskstats/` (new) — extract the existing build-tagged helper from
  `internal/server/diskstats_{unix,windows}.go` so `internal/backup` can use it
  without importing `internal/server`. Move, do not duplicate.
- `internal/server/diskstats_{unix,windows}.go` — delegate to the new package.
- `internal/backup/backup.go` — pre-flight guard, prune-before-backup, and a
  size-aware retention bound.

## Ordered steps

1. Create `internal/diskstats` with `Stats(path) (total, free uint64, err error)`
   and both build-tagged implementations, moved verbatim.
2. Reduce `internal/server`'s two files to a delegating call so the injected
   `getDiskStats` func value and its one call site are untouched.
3. Add `estimateArchiveBytes(dir)` — sum of regular-file sizes under the source.
   A Pebble DB is already-compressed SSTs, so gzip is ~1:1; treating the source
   size as the space requirement is the honest estimate, not a pessimistic one.
4. Prune to retention *before* writing, then check free space, then write.
5. Add `MaxTotalBytes` to `BackupConfig` and enforce it in retention alongside
   `MaxBackups`, oldest-first.
6. Refuse the backup with a clear, non-fatal error when free space is under
   estimate + margin. `autoBackup` already logs a warning and continues on
   error, so organize proceeds rather than the process dying.

## Test strategy

- Unit: retention drops oldest-first by count and by total bytes; a zero value
  for either bound means unlimited.
- Unit: the pre-flight guard refuses when free space is short and, critically,
  **creates no file** — a guard that refuses after `os.Create` still consumes
  space.
- Unit: prune runs before the write, so a backup that only fits after pruning
  succeeds.
- Mutation-test each guard at final HEAD, including the call sites.

## Rollback

Revert the PR. The change is additive — a refused backup logs a warning and
organize continues, which is strictly safer than the current behaviour of
filling the disk and killing the database.
