### Fixed

- **Series merges no longer read their book list from a memory index known to be
  missing rows.** Every path that merges one series into another first asks which
  books belong to the series being merged away, repoints them, and then deletes the
  old series. That question was answered from an in-memory index without ever
  checking whether the index was complete. When rows had gone missing from it — which
  the database already detects and records — the merge repointed only the books it was
  shown, reported success, and deleted the series anyway, leaving the books it never
  saw pointing at a series that no longer existed. Those books then render with no
  series and are effectively unfindable by series. An earlier instance of this same
  shape left 13,322 books holding 6,893 series IDs that had been deleted out from
  under them.

  The lookup now notices when the index is short and reads from the authoritative
  on-disk store instead, so the merge completes **correctly** rather than either
  silently stranding books or refusing to run. Seven merge paths are covered, not
  just the one where the problem was found.

  Series *listing* pages deliberately keep reading from the fast in-memory index even
  when it is short: a missing row there is a slightly incomplete page, not a deleted
  series, and a lost row is not repaired until the next restart — so making every
  series page fall back to a full scan would be a permanent slowdown in exchange for
  nothing.

  Books in the trash are unaffected by this change and are still covered separately.

- **The authoritative series scan that fall-through relies on no longer answers
  short in silence.** Making the merge getter fall through to the on-disk scan
  (above) promoted that scan from a listing read to the read a `DeleteSeries` is
  authorized against, and it had none of the hardening that role requires. It
  skipped book rows it could not decode — which is the *same* condition that
  makes the in-memory index refuse, so the repair path was blind to exactly one
  of the three triggers that send callers down it. It scanned
  `["book:0","book:;")`, a range that only covers IDs whose first character is a
  digit, so a caller-supplied letter-leading book ID was invisible to every
  series merge (latent: generated IDs are ULIDs and start with a digit — the
  same bounds bug fixed in the version-group backfill one day earlier). And it
  never checked the iterator's error, so a truncated scan and a complete one
  both returned success. All three now fail closed. Widening the bounds admits
  the secondary indexes, whose values are bare book IDs rather than book JSON,
  so the structural one-colon key filter and the now-fatal decode are a single
  change and must not be separated.
- Fixed the same iterator-bounds defect in `getAllSeriesBookRefCountsPebble`,
  the unfiltered counter that three series-delete sites consult before removing
  a row. It scanned `["book:0", "book:;")`, so a book whose ID does not begin
  with a digit was absent from the returned map — and absence is exactly the
  signal those callers read as "referenced by nothing, safe to delete".
- Added fault-injection tests (`vfs/errorfs`) for both hardened scans, proving
  each refuses rather than returns a partial answer when a storage-layer read
  fails mid-scan. This closes the one gap a mutation-testing pass had flagged
  as untestable without machinery the codebase didn't have — it did, one
  constructor argument away.
