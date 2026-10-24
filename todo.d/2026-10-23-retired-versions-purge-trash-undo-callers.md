- [ ] **TODO-RETIRED-PURGE-TRASH-UNDO-CALLERS** the retired-versions reader
      census (PLAN.md section 4.3) lists INCLUDE sites that are not covered by
      the callers change (3b) and belong with the purge, trash and undo work
      (PR 4 and 5): `audiobooks/service_single.go` `purgeOne` owns-files
      pre-check (report a new reason `holds_retired_versions`),
      `audiobooks/service_mutation.go` `DeleteAudiobook` owns-files pre-check
      (it runs before the hash-block side effect, so with the live-only read it
      would pass and leave a block behind before `DeleteBook` refuses),
      `audiobooks/trash_progress.go` (two reads, same as purge),
      `merge/trash_restore.go` (restoring a trashed holder must carry its
      retired versions), `versions/lifecycle.go` `PurgeVersion` (refuse a
      `retired` version explicitly instead of relying on the default filter).
      (The revert and combine-undo path/sibling reads in
      `audiobooks/revert.go` and `merge/combine_journal.go` landed with the
      undo change, PR 5.) All of them are safe while nothing
      retires a row (the fixer's Apply is not built yet), and must land before
      the first prod retire. Also open from 3b: the two `AcoustIDScan` retired
      checks in `dedup/engine.go` have no unit test (the collectors do).
      Open question for the owner: `deluge/discovery.go` decides whether a
      torrent's intro fingerprint is "already tracked" through
      `GetBookFileByAcoustID` and `GetBookFileByAcoustIDFuzzy`, which both skip
      retired rows, so content the library holds only as a retired version would
      be offered for import as new (the ingest path refuses it, see
      `ErrHeldAsRetiredVersion`). Counting it as tracked needs an
      include-retired fingerprint lookup that does not exist yet.
