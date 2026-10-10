- [ ] **CREATE-BACKUPS-BULK-APPLY** Decide whether the bulk metadata-apply
      paths also skip `create_backups` siblings (owner decision D69 named the
      bulk write-back ops). Since 01-P79b they keep a `<file>.bak-<unix>`
      copy per file when the setting is on, because they reach the tag and
      cover writes through `FinishApplyFileWork`/`FinishApplyFileWorkTimed`
      and `FinishAutoFetchFileWork`, which take no ctx and so cannot carry
      `tagger.WithoutBackup`: batch apply (`internal/server/batch_apply_one.go`
      757, 773), apply-when-scanned (`internal/server/apply_when_scanned_op.go`
      437), the file-I/O pool replay (`internal/server/file_io_pool.go` 454,
      474) and the bulk auto-fetch pool (`internal/metafetch/service_fetch.go`
      374). Opting them out means threading ctx through those methods and the
      two handler interfaces that declare them (mocks regenerate).
      Separately, `metafetch.backupFileBeforeWrite` (the older
      `write_backup_before_tag_write` setting, `.bak-YYYYMMDD-HHMMSS`) still
      runs beside it and ignores the bulk opt-out: with both settings on a
      single-book write leaves two backups.
