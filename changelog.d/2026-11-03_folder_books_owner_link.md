### Added

- When the owner applies a folder-book row, a file with no book of its own
  whose title and author already name exactly one live book (or exactly one
  version group) is no longer a reason to refuse the row. The new book is added
  to that group as a non-primary version, and the live book stays the primary.
  If the live book was in no group, the apply makes one for it and the live book
  becomes its primary. A normal apply still skips such a file as a duplicate,
  and its preview now names the book the owner's apply would add it to. Only
  matches that carry the same author count; two matching books, a book already
  sitting at the new book's folder, or a match with no author stay held back.
- Each of those writes is recorded in the operation before it is made, so the
  operation reverts from the Operations page. The revert puts the live book's
  group and primary flag back exactly as they were, and refuses, leaving
  everything as it is, if the live book or the new version was changed since
  (crowned, moved to another group, or joined by another book).
- An owner apply that was cut off partway leaves a hidden book behind. The next
  apply of the row now finishes that book instead of refusing the row, but only
  when the earlier operation's record says it created exactly that book. A
  reverted apply is never finished this way and its files stay "previously
  resolved". Reverting the earlier operation afterwards does nothing a second
  time.
