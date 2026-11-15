- [ ] **TODO-F5-18-CREDITS-CLASS-LISTS-RUN-ROWS** Route the native credits-backfill
      class id lists (`SaveClassLists`/`ClearClassLists`) to `RunStore.ReplaceRows`
      on the run's own id, not through `rc.Writer()`: run rows are not gated by
      Preview, so a native preview keeps them with no Preview hole in the Writer.
      HEAD's dry run keeps them (`TestT6G3_CreditsBackfillHeadShellDryRunWritesNoBook`).
- [ ] **TODO-F5-18-CREDITS-OPS-LISTER** The ops lister must return native runs of
      `books.maintenance.credits-backfill`: `otherCreditsBackfillOps` filters on
      `row.DefID == "maintenance.credits-backfill"` and would stop seeing earlier
      attempts.
- [ ] **TODO-F5-18-CREDITS-JUDGED-MARKERS-HOME** Judged markers are op rows written
      under an earlier (terminal) operation's id, and native `ReplaceRows` refuses a
      closed ledger (`core/tasks/native.go`). Give them a home outside the judged
      run's ledger (rows on the judging run keyed by the source id, or a journal
      annotation) and have the lister read them from there; the marker failure is
      only logged, so every later apply would silently re-judge.
- [ ] **TODO-F5-18-FOLDER-COVER-SCAN-GUARD** The folder-cover scan guard reads the
      v2 queue for `DefID == "library.scan"` (`cover_ops.go`,
      `fs_regroup_xml.go`) and goes dead once the scan is native. It must move to
      `NotWhile`/`StandDown`; this is a must, not a may.
- [ ] **TODO-F5-18-JOURNAL-ALLOWLIST** Delete the `kinds_t6_g3.go` entry from
      `journalDirectAllowed` in `internal/arch/journal_direct_writers_test.go` once
      F5-18 wires a real writer for `CreateOperationChange`.
