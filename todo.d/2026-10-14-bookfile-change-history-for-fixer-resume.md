- [ ] **05-PR5b follow-up: per-book_file change history for the Fixer resume check.** A
      resumed v3 Fixer counts a row as applied only while each journal row its marker names
      is still the newest journal row of its book on what it wrote (`opswriter.changesStand`,
      read through `opchange_by_book`). That cannot see a book_file write that recorded no
      journal row at all, and many book_file writers outside the ops Writer record none (the
      scanner, the AcoustID fingerprint and LSH backfills, the tag backfill, the Deluge link
      backfill). The window is narrow: the resume also requires every `Proposed` value to
      hold, so only a foreign write that set the same value is misread. Closing it needs a
      new family, e.g. `bookfile_change:<book>:<file>:<field>:<nanos>`, written in the same
      batch as the row by every book_file mutator in `internal/database` (Update/Upsert,
      `ModifyBookFile`, the patch, merge, import-mark, scan-cache and relocation paths: about
      ten store files, so no caller package changes), a key-family registration and census
      test, a capability reader, a pruning op with a retention setting (the scanner rewrites
      thousands of rows per scan, so it grows fast), and the evidence check switched to it.
      No migration: the family starts empty, and a marker older than it reads as today. Owner
      call whether the write amplification is worth it.
