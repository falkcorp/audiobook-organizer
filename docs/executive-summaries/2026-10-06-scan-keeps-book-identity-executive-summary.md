<!-- file: docs/executive-summaries/2026-10-06-scan-keeps-book-identity-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: ccee664a-7d1f-4ac3-a3ed-80cf7935036b -->
<!-- last-edited: 2026-10-06 -->

# The nightly scan no longer renames your books

PR: (this PR)

## Executive Summary

- **What went wrong.** The nightly library scan of 2026-10-06 changed the
  title or author of 1,176 books that were already in the library. It took
  the new names from the files' tags and file names. Some of the new names
  were better ("74" became "The Jasmine Throne"). Many were worse: 299
  chapter pieces of "Shadow's Edge" went from "183 of 301" to "of 301".
- **Why it mattered.** Changing a book's title or author throws away the
  metadata matches the app had already found for it, because they were found
  for the old name. Those 1,176 books lost their matches. The scan also kept
  no record of what it changed, so nothing on the book's history page showed
  it.
- **What changed.** A scan no longer changes the title, author or series of
  a book that is already in the library, wherever the new value comes from.
  When the file says something different, the scan writes that down as a
  suggestion instead (old name, new name), so nothing it read is lost. A
  book that is missing a title, author or series still gets one filled in.
  New books are imported exactly as before.
- **Every change a scan does make is now on the book's history.** File
  details, filled-in gaps, moves: each one is recorded with the source
  "scan".
- **"183 of 301" is read correctly.** The file-name reader cut the number
  off names like "183 of 301". It now keeps them whole, for existing books
  and new imports alike.
- **Still to do.** The 1,176 books that lost their matches need them fetched
  again (the "refetch lost candidates" repair), and the "of 301" titles
  should be put back from the saved copies of the books. Neither is part of
  this change.
