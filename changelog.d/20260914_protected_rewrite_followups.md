### Fixed

- The malformed-M4B remux and transcode passes no longer rewrite protected files. Both walk
  all of RootDir, and a protected directory (a Deluge save path, the iTunes library) under
  it had its unreadable files replaced by ffmpeg's output while they were seeding. They now
  use the same protected-path predicate as the tag-write guard and report such files as
  `skipped_protected`. A run that skipped a protected file, or was canceled, is no longer
  marked done, so the files it left are considered next time; and neither pass runs while
  the Deluge save-path list has never loaded.
- The protected-path cache no longer fails open when Deluge is unreachable. The static
  protected paths (the iTunes library, `protected_paths`) were merged in only after a
  successful Deluge refresh, so with Deluge down from startup nothing was protected. They
  are now always checked, and the cache reports whether the Deluge list has loaded; tag
  writes, write-back, library-copy creation and renames refuse to proceed until it has.
- Write-back never imports files one by one. It briefly (earlier in this change) sent each
  Deluge-protected file through the importer, which placed it at the library root with no
  book folder. Write-back now refuses a protected file, and its protected-path check knows
  the Deluge save paths, so a Deluge-seeding book is written through its library copy or
  skipped and counted, like an iTunes book. Protected version-linked copies are counted too.
- A write-back in which every attempted file write failed now returns an error instead of
  success, and batch save stamps `last_written_at` only when a file was written.
- `WriteMetadataToFile` checks the protected path before any writer runs and returns a
  refusal at once. It used to treat the refusal as a native-writer failure and fall back to
  the command-line writers on the original protected file.
- File renames refuse any plan that would move a protected file, including Deluge seeding
  files, before anything moves. The apply rename leg, the book edit write-back and the
  organizer now also recognise Deluge save paths as protected.
- Importing from Deluge refuses a file under a static protected path (only a Deluge save
  path may be imported), refuses when the path's book_file row is not the caller's row, and
  never moves torrent storage for an import the tag-write guard triggered. A row already
  marked imported but still naming the protected copy is imported again; a protected file
  that is its own library destination is refused with a clear error.
- Tag writers count a protected-path refusal as a skip, not a failure: cover embed,
  movement-atom cleanup, scan-composer-tags (the finding's category is kept, flagged
  `protected`, and not listed as a problem) and write-back.
- The Deluge discovery import endpoint reports a file whose source is already its library
  destination as skipped, not imported, and marks its row imported so it stops coming back.
