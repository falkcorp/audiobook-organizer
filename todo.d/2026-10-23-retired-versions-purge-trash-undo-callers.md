- [ ] **TODO-RETIRED-PURGE-TRASH-UNDO-CALLERS** the retired-versions reader
      census (PLAN.md section 4.3) lists INCLUDE sites that are not covered by
      the guards change (4) and belong with the undo work (PR 5): the revert
      and combine-undo path/sibling reads in
      `audiobooks/revert.go` and `merge/combine_journal.go` (a journaled row
      that is now retired is changed-since). All of them are safe while nothing
      retires a row (the fixer's Apply is not built yet), and must land before
      the first prod retire.
      Open question for the owner: `deluge/discovery.go` decides whether a
      torrent's intro fingerprint is "already tracked" through
      `GetBookFileByAcoustID` and `GetBookFileByAcoustIDFuzzy`, which both skip
      retired rows, so content the library holds only as a retired version would
      be offered for import as new (the ingest path refuses it, see
      `ErrHeldAsRetiredVersion`). Counting it as tracked needs an
      include-retired fingerprint lookup that does not exist yet.
