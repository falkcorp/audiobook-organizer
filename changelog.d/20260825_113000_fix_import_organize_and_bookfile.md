### Fixed

- **Importing a file now actually organizes it when you ask.** The "Organize
  into library after import" checkbox sent its setting to the server, and the
  server decoded it and did nothing with it — you got a success message and the
  file never moved. It is now honored. The checkbox also **defaults to off**:
  now that ticking it really moves files on disk, that has to be something you
  choose rather than something that happens because you did not notice a box
  was already ticked.

- **An imported book now has a link to its own audio file.** Books added
  through file import were created without the row that connects a book to the
  file it plays, so nothing downstream could find the audio: not playback, not
  organize, not the file list. The link is now created at import. This is
  separate from, and additional to, the scan-path version of the same symptom
  being fixed elsewhere.

  This is also why organizing on import would have appeared to work and quietly
  done nothing: the organize pass skips any book it cannot see files for, and
  an imported book was always in that state.
