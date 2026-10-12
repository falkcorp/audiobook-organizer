<!-- file: docs/executive-summaries/2026-10-11-deluge-import-safety-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f088091-676f-47c5-a818-16c82fb85387 -->
<!-- last-edited: 2026-10-11 -->

# Deluge imports can no longer overwrite a book or point two books at one file

Branch `task/10-pr2` (10-PR2, PR #3972). Found in code review of the new Deluge link backfill.

## Executive Summary

- **What could go wrong.** Three tools copy audiobooks that Deluge downloaded into
  your library folder: the bulk Deluge import, "centralize", and the one-at-a-time
  import. Only the one-at-a-time import had the safety checks. The other two had
  three problems:
  - **Overwriting.** If a file with the same name was already in the library
    folder, the bulk import wrote over it, even when it was a different book's
    audio.
  - **Two books, one file.** If two downloads had the same file name and the same
    contents, both books ended up pointing at one file. Deleting, merging or moving
    either book later would then delete or move the other book's audio as well.
  - **Copying out of iTunes.** They would copy files out of the iTunes library or
    other protected folders and repoint the book at the copy. That skipped the
    preview-and-approve step the proper copy-in tool uses, and could ask Deluge to
    move the torrent.
- **The fix.** All three tools now go through the same import path. That path
  locks the destination while it works, refuses a destination another book already
  uses, refuses protected sources such as iTunes, and removes its own copy if saving
  the database change fails. A file already at the destination is reused only when
  it is a regular file with exactly the same bytes. Otherwise the import stops and
  tells you to remove the leftover copy.
- **Clearer reporting when Deluge is down.** If the app can't reach Deluge to find
  out which files are still seeding, those books are now reported as "Deluge list
  not loaded" instead of "protected". The bulk import also checks once at the start
  instead of asking Deluge again for every book.
- **The new link backfill keeps its progress.** The new tool that links library
  files to their Deluge torrents now works in rounds. It stops early enough to save
  what it found before its 4-hour limit, so the next run continues instead of
  starting over.
- **Nothing is deleted.** No book_file row is removed by any of this. A refused
  import leaves the book where it was, still waiting to be imported.
