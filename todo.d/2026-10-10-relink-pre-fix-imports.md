- [ ] **RELINK-PRE-FIX-IMPORTS** (owner decision) Files imported before the
      IMPORT-ORPHANED-VERSIONS fix each have an orphaned active version and a
      book_file row with no `VersionID` and no `FileHash`. That leaves them
      invisible to exact-duplicate matching and `unverifiable` for torrent
      cleanup. `versions.CreateIngestVersion` now adopts a stranded version
      (exactly one live, unreferenced version with the same torrent hash), so a
      one-time relink pass over those rows is safe to build. None exists yet,
      and running one in prod needs the owner's go-ahead.
