- [ ] **CI-NATIVE-TAGLIB** Add a CI job that builds and vets `-tags native_taglib`
      (needs the TagLib C library on the runner). Today the cgo writer
      `internal/metadata/taglib_cgo.go` is only vetted by hand, so a change to
      its signatures or its create_backups handling can break that build
      unnoticed (found in the PR #3919 review, N5).
- [ ] **ORGANIZE-FETCH-REFETCH** `organizer.PerformOrganizeStats` with
      `fetch_metadata_first` re-reads the WHOLE library (`GetAllBooksCore`
      pages) after the fetch even when the request named specific `BookIDs`,
      so a fetch-first organize of a few books goes on to organize every book.
      Re-read only the requested IDs.
- [ ] **BACKUP-SWEEP-CONSOLIDATE** Three "delete old backups" implementations
      exist: `maintenance.cleanup-old-backups`, `scheduler.cleanup-old-backups`
      (both age by `fileops.BackupTime`) and the manual `cleanup-backups` job,
      whose regex does not match the `.bak-<unix>[-N]` names create_backups
      now writes. Consolidate on one predicate.
