- [ ] **TODO-VERSION-PURGE-GUARD-FOLLOWUPS** Three follow-ups to the trash-purge
      guard (`internal/versions/serve_guard.go`, PR #4215) that were left out of
      that PR on purpose.
      (1) Another book's symlink-strategy link. When another live book holds a
      symlink (library strategy "symlink") that points at one of the trashed
      version's files, the guard compares the link's own path, not its target,
      so the purge can delete the file the link resolves to. Fix: for each of the
      version's files, find rows and FilePaths that resolve (EvalSymlinks) to it,
      not only rows that name it, and keep the version when a live book's link
      resolves to it; fail closed when the resolution cannot be completed.
      (2) The player's timeline includes `.versions/` slot rows.
      `SplitOwnFolderFiles` (`internal/database/book_own_folder.go`, the
      `s.Dir`/`inside` loop) treats any row under the book's own folder as the
      book's own, and a slot lives under that folder, so a trashed or alt
      version's slot rows are counted in the timeline the ABS mapper and stream
      build from `GetBookFiles`. Fix: skip rows with a `.versions` path component
      there (or filter on the version's status), with a test on a book that has
      an alt slot.
      (3) The frontend shows the purge 409 as a generic error.
      `purgeVersion` (`web/src/services/versionApi.ts`) turns the new 409 from
      `POST /audiobooks/:id/versions/:vid/purge-now` ("the book still serves its
      file(s)") into the same generic failure as any other error. Fix: detect the
      409 and show the server's message, and keep the version listed in the trash.
