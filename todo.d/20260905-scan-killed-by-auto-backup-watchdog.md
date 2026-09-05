## Library scan killed by the watchdog while its own auto-backup ran (2026-09-05)

- [ ] The weekly `library.scan` (`01M1M51FCKD1KA4XQ9AQ63XZWD`) organized a book at 02:31
      EDT, which tripped the 6-hourly organizer auto-backup (`autoBackupMinInterval`,
      `backup.CreateBackupWithCheckpoint`: Pebble checkpoint → `vfs.CopyAcrossFS` of the
      27 GB store to `/mnt/bigdata/.../.backups` → tar). The copy phase reports no
      progress, so after the registry's 5-minute `ProgressTimeout` the watchdog recorded
      a `stuck` strike, cancelled the op at 02:37 and spawned a replacement worker; the
      scan went `interrupted_quiesced` (it resumes at the next restart) while the
      abandoned goroutine kept archiving until 03:41 (tarball closed 03:37, 26.05 GB).
      Two defects: (1) a backup started from inside an op must report progress through
      the op's reporter or run outside the op's watchdog window — the archive phase
      already logs `Backing up: archived N files`, the checkpoint-copy phase before it
      is silent; (2) cancelling the op does not stop the backup, so the "replacement
      worker" runs beside a 27 GB copy it knows nothing about. Add a regression test
      that drives an organize with the backup interval elapsed and asserts the op is
      not cancelled.
