- [ ] **BULK-META-STALE** Two leftover races in `BulkMetadataSearchDialog.tsx` (follow-ups on
      #3306, not introduced by it):
      (1) Within one session, a metadata search for the previous book can land after the user
      moves to the next book, overwrite the current results, and clear `loading` early. The
      guard added in #3306 is per session, not per book. Key it by book id as well.
      (2) A late "No match" from a closed session writes server state (`api.markNoMatch`) but
      never calls `onLibraryChanged`, unlike late applies and undos, so the list can stay stale.
