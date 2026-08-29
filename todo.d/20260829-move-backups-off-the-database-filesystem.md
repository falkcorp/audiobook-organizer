## Move database backups off the database's own filesystem

`autoBackup` writes archives to `backups/` resolved relative to the database
directory, so on production the ~15 GB archive lands on the same filesystem the
live PebbleDB writes its WAL to. The pre-flight space guard now stops that from
killing the database, but co-locating them is still the underlying design
problem: a backup exists to survive the loss of what it backs up.

- [ ] Make `BackupConfig.BackupDir` configurable to an absolute path on another
      filesystem (on the reference deployment `/mnt/bigdata` has 11 TB free
      versus 141 GB for `/var/lib`).
- [ ] Decide whether a backup that lands on the same filesystem should warn at
      startup.
- [ ] Revisit `defaultMaxTotalBytes` (currently 40 GiB) once the destination is
      no longer the constraint.

Context: `.claude/notes/2026-08-29-prod-outage-disk-full.md`.
