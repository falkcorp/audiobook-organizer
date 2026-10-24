- [ ] **TODO-RETIRED-RESTORE-JOURNAL** `POST /audiobooks/:id/retired-versions/:vid/restore`
      (`RestoreRetiredVersion`, PR 9) answers 501 Not Implemented and changes
      nothing, and the panel shows its Restore button disabled with "Restore will
      be available once restores are journaled". The direct restore it replaced
      (`UnretireBookFiles` with no prior-`VersionID` map, then `DeleteBookVersion`)
      is not shipped: it cleared every row's `VersionID` (the torrent/ingest link),
      deleted the version row that carries `RetireOpID`, wrote no journal or
      activity entry and had no undo. Build the real restore with
      `RevertOperationForBooks` for the retire op (PLAN.md section 6.4), which also
      recreates a purged shell instead of refusing, then: replace the 501 body;
      make `restoreBlockedReason` in `RetiredVersionsPanel.tsx` return null for a
      version that can be restored, wire the button (confirm, POST, then refetch
      and call `onRestored` on success AND on a non-4xx failure, since the rows may
      have moved), and add the `restoreRetiredVersion` client function back; wrap
      the not-found error `UnretireBookFiles` raises for a row a concurrent restore
      already moved (`retired_book_files.go`, "file not found ... under <holder>")
      in a sentinel mapped to 409, not 500; answer 404 (not 400) for a version of
      another book; and write a Pebble-backed test that the restore writes its
      journal rows and returns each row's prior `VersionID`. Until then nothing
      calls the restore, so none of this is reachable.
