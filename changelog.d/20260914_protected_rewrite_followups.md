### Fixed

- The malformed-M4B remux and transcode passes no longer rewrite protected files. Both walk
  all of RootDir, and a protected directory (a Deluge save path, the iTunes library) under
  it had its unreadable files replaced by ffmpeg's output while they were seeding. They now
  use the same protected-path predicate as the tag-write guard and report such files as
  `skipped_protected`.
- A book_file row already marked imported from Deluge but still naming the protected copy is
  imported again, instead of having its protected path returned as the library path. Every
  tag write to it used to be refused, so the book could never be tagged. A protected file
  whose library destination is itself (a protected directory under RootDir) is refused with
  a clear error rather than returned as the write target.
- Tag writers count a protected-path refusal as a skip, not a failure: cover embed,
  movement-atom cleanup, scan-composer-tags (category `skipped_protected`, not listed as a
  problem) and write-back. Write-back's per-file writes now go through the protected-path
  guard; they used to write the file they were given, so a file under a Deluge save path
  that no import root covered was rewritten in place.
- The Deluge discovery import endpoint reports a file whose source is already its library
  destination as skipped, not imported: nothing was copied and no row was updated.
