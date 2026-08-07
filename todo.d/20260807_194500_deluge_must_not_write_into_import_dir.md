<!-- file: todo.d/20260807_194500_deluge_must_not_write_into_import_dir.md -->
<!-- version: 1.0.0 -->
<!-- guid: c9e4b7d2-51a8-4f36-b0e7-2d84a1f6c093 -->
<!-- last-edited: 2026-08-07 -->

- [ ] **Stop Deluge writing in-progress downloads directly into the new-books
      import directory.** A torrent that is still downloading is visible to the
      scanner as a book, so a partial file gets imported as if it were complete:
      wrong duration, wrong file size, a truncated or absent intro clip, and a
      transcription/fingerprint pass that runs against bytes which will change
      underneath it.

      Fix: give Deluge a staging directory OUTSIDE the watched tree and have it
      **move** the completed torrent into the import directory only on
      completion. A move within the same filesystem is an atomic rename, so the
      scanner can never observe a half-written book. A copy across filesystems
      is NOT atomic — if staging and import must live on different filesystems,
      copy to a dotfile/temp name inside the import dir and rename into place as
      the final step.

      Deluge supports this natively: set "Download to" = staging path and "Move
      completed to" = import path.

      Also worth adding as defence in depth, since Deluge is not the only way
      files arrive:
      - Scanner ignores partial-download suffixes (`.part`, `.!ut`, `.tmp`) and
        dotfiles.
      - Quarantine a candidate whose size or mtime changed between the scan and
        the import rather than importing it.

      🔴 Suspected to be a real source of existing bad rows — worth measuring how
      many books have a duration or file size inconsistent with their format
      before assuming this is only a forward fix. Silently-truncated books would
      also explain some fraction of the `[SILENCE]` sentinels and short/failed
      intro transcriptions.
