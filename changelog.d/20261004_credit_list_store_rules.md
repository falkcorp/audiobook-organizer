### Changed

- A book's author and narrator credit lists are now stored in canonical form
  on every write: sorted by position, one row per person, positions numbered
  0, 1, 2 and so on. Ties keep their stored order, so rows that an older copy
  path wrote all at position 0 come out in the order they were credited.
  Narrator credit writes now take a per-book lock, as author credit writes
  already did.

### Added

- Store groundwork for credit lists (PR 1 of the plan in
  `docs/plans/2026-10-04-author-narrator-credit-lists-audit.md`):
  - `JoinCreditNames` ("A", "A and B", "A, B and C") for tags and display, and
    `JoinCreditNamesABS` (", ") for the ABS wire format;
  - a batched, position-ordered `GetBookCredits`;
  - `ModifyBookNarrators`;
  - `ModifyBookCredits`, which writes a book row and both of its credit lists
    in one batch and queues the book for search reindexing.

  Nothing calls these yet.
