<!-- file: docs/plans/2026-10-08-itunes-manual-import-only.md -->
<!-- version: 1.0.0 -->
<!-- guid: dace0be1-1032-44aa-8ef3-04b8fa9baf7f -->
<!-- last-edited: 2026-10-08 -->

# iTunes: manual import only

Owner request 2026-10-08 00:13: "we allow them to do a manual import of their
itunes library, we still keep our rules for moving the itunes id, and all of
that so if they try to import it again, it does a best effort to not create
duplicates and we just warn them what could happen".

## Goal

There is one iTunes action, **Import iTunes library**, and it runs only when
someone clicks it. Nothing runs on its own. Running it again is safe as far as
we can make it: it matches existing books rather than adding them twice, and a
warning says what can still go wrong.

## What changes

1. **Remove every automatic sync.**
   - Remove `syncITunesBeforeOrganize` (internal/organizer/service.go). It
     runs a sync before each organize.
   - Remove the `itunes_sync` scheduled-task binding
     (internal/server/handlers/scheduler_admin.go).
   - Remove the unused `LibraryWatcher` (internal/itunes/library_watcher.go).
   - Remove `itunes.sync_enabled` and `itunes.sync_interval` from config,
     persistence and the env bindings. Old saved values are still read
     without error and then ignored.
2. **Remove incremental Sync.** This covers `Importer.Sync` / `syncLibrary`,
   the `itunes.sync` op, its route, and the conflict-resolution flow. Import's
   matching already covers it:
   - an iTunes ID tombstone is skipped;
   - an iTunes ID already mapped to a book links to that book;
   - a matching file path or per-track iTunes ID on book_file links to that
     book;
   - anything else is added as a new book.

   Linking refreshes the iTunes fields: play count, rating, bookmark and last
   played. Re-importing never moves files and never changes a stored
   FilePath, the same as #3836.
3. **Keep the iTunes ID rules.** iTunes IDs move with merges, retires and
   repoints, tombstones stop re-adding deleted books, and the external-ID map
   stays. No changes here.
4. **UI (Settings → iTunes Import):**
   - Remove the "Force Sync Options" section ("Sync Now", "Retry Failed
     Sync") and the conflict dialog.
   - The import button becomes **Import iTunes library**.
   - When books are already linked to iTunes, clicking it first shows a
     confirm dialog: *"You've imported from iTunes before. We match each album
     to your existing books by iTunes ID, then by file path. Albums iTunes has
     re-created with new IDs, or whose files moved, can't be matched and will
     be added as new books, so you may see duplicates. Nothing in iTunes is
     changed, and no files are moved."*
     The buttons are **Import anyway** and **Cancel**.
   - After the run, the result shows: linked N, added N, skipped N.
5. **Docs and tests:**
   - Remove the tests for the removed code.
   - Add a test: importing the same synthetic library twice adds 0 new books
     the second time.
   - Add a changelog fragment.

## Not changing

- No iTunes writes, which are already removed (#3834).
- The book_file iTunes-ID matching.
- The fragments fixer (separate PR).

## Rollback

Revert the PR. No stored data changes shape: the `itunes_sync_status` column
and the external-ID map are untouched.
