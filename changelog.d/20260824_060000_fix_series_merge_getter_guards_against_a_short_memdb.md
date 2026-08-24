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
