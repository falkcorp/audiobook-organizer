- [ ] **SEC-BACKUP-ABSPATH** Decide whether the backup restore path should
      *reject* absolute tar entry names rather than normalise them.
      `internal/backup/backup.go:267` strips leading slashes from
      `header.Name`, so an archive entry called `/etc/passwd` is written to
      `restoreDir/etc/passwd`. TASK-082's brief asked for outright rejection;
      the current behaviour was left in place deliberately because it is
      standard tar semantics (GNU tar strips leading `/`), the containment
      property still holds — nothing is written outside the restore root — and
      flipping to reject is a behaviour change on a prod-data restore path that
      would break legitimate archives. `TestRestoreBackupHandlesAbsolutePathInArchive`
      in `internal/backup/backup_test.go` currently locks the normalising
      behaviour in, so changing it means changing that test too. Owner
      decision; not a live vulnerability either way. Raised by TASK-082 / PR #2774.
