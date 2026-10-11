- [ ] **RELINK-PRE-FIX-IMPORTS** (owner decision) Files imported before the
      IMPORT-ORPHANED-VERSIONS fix each have an orphaned active version and a
      book_file row with no `VersionID` and, unless the hash backfill reached
      them since, no `FileHash`. That leaves them invisible to exact-duplicate
      matching and `unverifiable` for torrent cleanup.
      `versions.CreateIngestVersion` adopts a stranded version (exactly one
      live, unreferenced version of the book with the same torrent hash) and
      refuses (`ErrTorrentLinkedElsewhere`) to link a torrent a different live
      version holds, so a relink pass can be built on it. The pass must hold
      `versions.LockTorrent(hash)` across the call, and a row whose
      `VersionID` names another book's version (merged rows keep the absorbed
      book's) needs a decision of its own: the function relinks it for the
      keeper but will not take that version's torrent. None exists yet, and
      running one in prod needs the owner's go-ahead.
