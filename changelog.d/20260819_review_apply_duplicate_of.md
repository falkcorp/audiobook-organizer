### Added

- **The review queue's `duplicate-of` action now applies.** Approving a hold as
  `duplicate-of` used to return `501 Not Implemented`, leaving the only way to clear
  such a hold a rejection. It now merges the folder's debris into the canonical book
  through `CombineBooks` — the same merge the `combine` action and the duplicates UI
  already use. The canonical book is read from the dedup track's candidate rows,
  which is where that relationship is recorded; the regroup classifier only ever sees
  one folder at a time, which is why it could not name it. When the dedup track names
  no book outside the folder, names more than one, or names a book that has since
  been deleted, the apply refuses with a message saying which case it hit, so the
  hold lands in "failed" rather than being marked done while nothing happened.
