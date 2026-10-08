### Removed

- **iTunes write-back.** The organizer no longer writes the iTunes library
  (owner decision 2026-10-07: iTunes is an import-only source). Gone: the
  write-back batcher, its durable queue, requeue and held removes; the ITL
  mutators, safe-write and identity refresh; the rebuild, relocate,
  adopt-base, cleanup-merged, export-partial, write-back, requeue, status,
  library upload/restore/backups routes; the `itunes.path-reconcile` and
  `itunes.path-repair` ops; the enqueue calls from book edits, metadata apply,
  merge, quarantine, organize, batch apply/save, duplicates, dedup and
  repoint; the `itl-repair`, `itl-roundtrip`, `itl-write-test` and
  `itunes-sync-tests` tools; the `itunes_location_unmappable_total` metric;
  and the write-back, upload and restore UI.
- **Config:** `itunes.write_back_enabled`, `itunes.auto_write_back` and
  `itunes.write_back_dry_run` (and `ITUNES_WRITE_BACK_ENABLED`,
  `ITUNES_AUTO_WRITE_BACK`, `ITUNES_WRITEBACK_DRYRUN`) are gone. A stored
  config that still holds them loads without error. Setting
  `itunes.library_write_path` no longer turns write-back on; the path is kept
  as the read-only `.itl` the PID check and the library download use.
- **Startup cleanup:** leftover `itunes_writeback:*` and
  `pref:_system:outbox:writeback:*` keys are deleted once at startup.

### Changed

- **iTunes position sync no longer stamps the book** when it seeds a finish
  from the iTunes play count. That stamp only kept the removed push from
  counting the finish twice.
