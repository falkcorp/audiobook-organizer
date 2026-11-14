- [ ] **TODO-F5-18-CREDITS-PREVIEW-CLASS-LISTS** Native credits-backfill preview
      must keep the class id lists: `SaveClassLists` and `ClearClassLists` have to
      be real writes in a preview (`rc.Writer()` intent-recording,
      `core/tasks/rc.go`). HEAD's dry run keeps them
      (`TestT6G3_CreditsBackfillHeadShellDryRunWritesNoBook`).
- [ ] **TODO-F5-18-CREDITS-OPS-LISTER** The ops lister must return native runs of
      `books.maintenance.credits-backfill` with their judged markers under the run
      id: `otherCreditsBackfillOps` filters on `row.DefID ==
      "maintenance.credits-backfill"` and would stop seeing earlier attempts.
- [ ] **TODO-F5-18-FOLDER-COVER-SCAN-GUARD** The folder-cover scan guard reads the
      v2 queue for `DefID == "library.scan"` (`cover_ops.go`,
      `fs_regroup_xml.go`) and goes dead once the scan is native. It must move to
      `NotWhile`/`StandDown`; this is a must, not a may.
- [ ] **TODO-F5-18-JOURNAL-ALLOWLIST** Delete the `kinds_t6_g3.go` entry from
      `journalDirectAllowed` in `internal/arch/journal_direct_writers_test.go` once
      F5-18 wires a real writer for `CreateOperationChange`.
