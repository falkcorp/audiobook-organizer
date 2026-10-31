### Fixed

- A hand-picked metadata apply now claims the repair locks of the fields it changed while it still holds the book's field-state lock, and takes the lock again as the person's when a revert already lifted it. Before, a revert landing between the apply's write and its claim left the person's new value unlocked, so a rescan could overwrite it.
