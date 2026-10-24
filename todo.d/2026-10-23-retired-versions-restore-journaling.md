- [ ] **TODO-RETIRED-RESTORE-JOURNAL** `POST /audiobooks/:id/retired-versions/:vid/restore`
      (`RestoreRetiredVersion`, PR 9) calls `UnretireBookFiles` directly and then
      deletes the empty retired `book_versions` row. It writes no journal rows, so
      the restore cannot be undone and the retire operation is not marked reverted,
      and each row comes back with an empty `VersionID` (the prior value lives in the
      retire journal). Replace the direct call with `RevertOperationForBooks` for the
      retire op (PLAN.md section 6.4) once PR 5/7 land, which also lets it recreate a
      purged shell instead of answering 409. Add a test that the restore writes its
      journal rows and restores each row's prior `VersionID`.
