### Added

#### `maintenance.audible-read-status` — import read status from an Audible library export

A new Repairs-lane fixer applies an Audible library export's read status to
the matched books, for one explicit user only. The plan params are the
`target_user_id` and the export's `items`. The target user is required and is
never defaulted to the caller. The iTunes sync user is refused.

Matching:

- An item matches by ASIN to exactly one version-group primary. When several
  primaries carry the ASIN, the one whose duration is within ±10% of Audible's
  runtime is used.
- An item whose ASIN no book carries matches by normalized title (a subtitle
  may be dropped on one side, series numbers must agree) plus an overlapping
  author surname.
- These become review rows and are never applied: ambiguous matches, runtime
  mismatches, junk-titled targets, a title found only in an author field, and
  two items claiming one book.

What it writes:

- **Finished titles** are marked finished: StatusManual, 100%, and `finished_at`
  and last activity set to Audible's timestamp, never "now". A book that is
  already finished or abandoned is left alone, and so is one with local
  activity (a state, position or progress reset) newer than Audible's
  timestamp. A finished book that has no position row gets one at its end,
  on the ABS whole-book segment and stamped with Audible's time, because the
  ABS progress list that AudioBooth reads is built from position rows.
- **In-progress titles** get Audible's position, scaled to the local duration,
  only when the user has no local progress on the book at all.
- **Not-started titles** are left alone. Nothing is ever un-finished.

The plan is the dry run. Each class (`would_finish`, `would_progress`,
`skipped_*`, `review_*`, `unmatched_*`) is a filter of the rows endpoint.

Every write is journaled before it is made, as the new
`user_book_state_set` change type, with the user's whole state and positions
before and after. The apply operation's revert restores each part only while
it still holds what the import wrote, and it refuses a book the user has
listened to since. A state row the import created is deleted on revert,
through the new `DeleteUserBookState` store capability. The Repairs `Writer`
gains `WithUserState` and `SetUserState`, which re-read the state just before
writing and refuse with `changed_since_plan` if it moved.
