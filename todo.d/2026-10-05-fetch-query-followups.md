- [ ] **FETCH-QUERY-1** Series-and-number matching for titles that name only a
      series slot ("Some Series 03", "Series Book NN" with no name): the
      2026-10-05 census's 20-sample probe still misses these after #3775
      (10/20 found; the rest are slot-only names, wrong authors, or titles
      Audible does not carry). Needs a query by series + position and a
      `keepSeriesSlot` answer check, not a better parse.
- [ ] **FETCH-QUERY-2** Fold the fetch-side slot gates (`usableSlotName`,
      `BareSlot`, the two-word colon rule in `parseSearchTitle`) into
      `metadata.ParseBookName` so the two sides share one slot reader, not
      just the cleaning (#3775 left them layered on top of the shared parse).
- [ ] **FETCH-QUERY-3** Count, on prod, the first scheduled `candidate_fetch`
      selection (no cache row + current-version empty rows whose inputs
      changed) and the reparsed books that already hold candidates (~320 by a
      title-only estimate): those keep candidates for their old query until a
      stale refetch, and a pre-2026-09-28 row (hashed with no author) fails the
      apply identity's fingerprint leg until refetched.
- [ ] **FETCH-QUERY-4** `StripRipJunk` reads only bracketed rip groups; the
      unbracketed "(Narrator) 64k hh.mm.ss {size}" tail is read by
      `ParseBookName` only, so `ripFolderPartRow`/`folderTitle` do not treat
      such folders as rip-detail folders. Decide whether part-row detection
      should learn it (changes skip decisions).
- [ ] **FETCH-QUERY-5** `scanner.FolderNameEvidence` reuses one
      `foldernames.Snapshot` for 10 minutes, so a series or author row
      created mid-scan is not evidence for other books until the next
      reload, and the junk-series verdict is memoised for the snapshot's
      life. Decide on invalidation (bump on CreateSeries/CreateAuthor) versus
      the TTL (#3775 round-3 review, SHOULD-FIX).
- [ ] **FETCH-QUERY-6** Repair the existing author-junk series rows on prod:
      11,713 series rows letters-equal an author row's name (3,188 filed
      under that same author, 671 of those holding books; 2026-10-06). The
      ones whose author has books elsewhere no longer act as series
      evidence, but books are still linked to them, and many of the rest are
      real series under a junk AUTHOR row ("Rogue Merchant"). A reviewed
      fixer should sort the two and repair each side. Also trace which
      creator minted them: the scanner and importer now refuse a series
      naming the book's author, but the iTunes importer, the metadata apply
      path and `audiobooks/service_mutation.go` still create series without
      that check.
