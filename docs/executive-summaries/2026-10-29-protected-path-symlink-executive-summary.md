<!-- file: docs/executive-summaries/2026-10-29-protected-path-symlink-executive-summary.md -->
<!-- version: 1.7.0 -->
<!-- guid: 33552e57-ccc8-45c9-95a3-eafe9d145b0e -->
<!-- last-edited: 2026-10-30 -->

# A file reached through a shortcut can no longer slip past the iTunes protection

Branch: fix/protected-path-symlink-spelling, pull request https://github.com/falkcorp/audiobook-organizer/pull/4256 (merge commit to be added once it merges).
Found by the kept-file sweep work (branch fix/purge-kept-files-orphaned-sweep), whose
skipped test, TestSweepKeptFiles_RealGuardSymlinkIntoITunesFolder, reproduced it.

## Executive Summary

- The app never deletes or moves files that belong to the iTunes library, the
  folders watched for imports, or the quarantine folder. The check behind that
  rule did not follow shortcuts (symbolic links), so a file named through a
  shortcut into one of those folders looked harmless.
- The permanent purge of trashed books, and of old versions of a book, relied on
  that check. A shortcut into the iTunes folder could therefore get a real iTunes
  file deleted.
- The check now looks at the location as written and at the location the shortcut
  really leads to, for the file and for each protected folder. It can only protect
  more than before; nothing that was protected stops being protected.
- A path whose shortcuts cannot be followed (one that loops back on itself, or a
  folder the app may not read) is no longer waved through: the purge never
  deletes the file, and tag writes and previews treat the path as protected.
- The organizer's move-or-copy decision and the metadata write-back used their
  own copies of the same check with the same gap; they now use the shared one,
  and the shortcut-following code that already protected the merge tools moved to
  a shared place so there is one copy of it.
- The organizer's re-organize in place (the move that renames a book's folder
  without copying) now also refuses a file that sits in the iTunes tree or a
  protected folder only because a shortcut leads there, and refuses when the
  shortcuts cannot be followed. It relies on the server having installed its
  protected-path check; without one only the iTunes tree rule applies.
- The protected folders are looked up through their shortcuts once every few
  seconds instead of once per file, and a folder that cannot be resolved is
  reported once, not once per file, while every path stays protected meanwhile.
  The permanent purges are the exception: for every file they look the folders up
  afresh and read the list of import folders straight from the database, not from
  the copies kept for a few seconds. So a shortcut that was re-pointed a moment ago,
  or an import folder added a moment ago inside the library, can never make a purge
  protect less than a fresh look would. A folder that stays unresolvable is
  reported again after an hour, so it does not go quiet for weeks, and is not
  reported over and over once it has started resolving again.
- When the app cannot reach the download client to learn which folders are being
  seeded, it still refuses to rename or re-organize in place, as before. It now
  says why ("the seeding list has not loaded yet; try again once it has") instead
  of claiming the file is in a protected location, counts those books as skipped,
  not failed, and moves them on the next run once the list is back. A file that is
  protected is still reported as protected, and the app reads whether the list is
  back before it looks at the file, so the list arriving halfway through cannot
  make a seeded file look free to move. If the protection check ever gives an
  answer the app does not understand, the file is left alone.
- Verified with tests for a shortcut to a file, a shortcut to a folder, a
  protected folder that is itself a shortcut, a shortcut to a file that no longer
  exists, a new file under a shortcut, and a loop; each fails when the new
  behavior is removed.

## A shortcut into a protected folder was not protected

**What it was.** The protection check compared the written-out location of a file
with the list of protected folders. It never asked where a shortcut in the path
actually leads. A file reached as "library / Gotham / link / hush.m4b", where
"link" is a shortcut into the iTunes folder, did not match the iTunes folder by
name, so it was treated as an ordinary library file.

**Why it mattered.** The purge that permanently removes trashed books and trashed
versions asks this check before it deletes anything. For a file named through
such a shortcut it got the answer "not protected" and could delete the real iTunes
file behind it, which is the one thing the app promises never to do. The
organizer, which decides between moving and copying a file, and the metadata
write-back asked the same question and could have moved or rewritten that file.

**The fix.** One shared check tests the written-out location first, exactly as
before, then the location with every shortcut followed, against the protected
folders as written and as they really are. That covers a shortcut to a file, a
shortcut to a folder, a protected folder that is itself a shortcut, a shortcut
whose target file is gone, and a not-yet-existing file under a shortcut, and it
also catches an "iTunes Media" or quarantine folder that is only reached through
a shortcut. The purge, the version purge, the revert path, the organizer and the
metadata write-back all use it.

## A path that cannot be followed is not treated as safe

**What it was.** Following shortcuts can fail: a shortcut can loop back on itself,
or a folder can be unreadable.

**Why it mattered.** Reading such a failure as "not protected" would reopen the
same hole. Reading it as "protected" for the purge would have let a trashed book
go quietly even when the cause was a passing one, such as a folder that was
briefly unreadable, which a second look would have settled.

**The fix.** For tag writes and previews such a path counts as protected. For the
purge the file is never deleted, and what happens to the book depends on why the
path cannot be followed. If the cause may pass (a folder the app is not allowed
to read, a disk error), the book stays in the trash and the purge asks again on
the next pass. If the path loops back on itself, trying again cannot help, so the
book is purged, the file is left exactly where it is, and the purge report says
"book purged, file kept" with the reason; the file then has no database entry
pointing at it, and a person has to deal with the shortcut. If the trouble is in a
configured protected folder instead of the path, every due book is held until the
configuration is fixed, and the message names that folder.

## What to know

- Follow-up 2026-10-30: the list of Deluge seeding folders and the explicitly
  configured protected paths (a separate check the callers consult next to the
  shared one) now follow shortcuts the same way, and the shared check now
  protects the frozen iTunes tree (any "books/itunes" folder) on its own, whether
  or not an iTunes library path is configured. The importer's own check of those
  prefixes follows shortcuts too, and the scheduled job that refreshes the
  seeding-folder list now really refreshes it (it had been doing nothing). See
  docs/decisions/2026-10-30-deluge-protected-paths-symlinks.md.
- Left as they were, for a later decision: the classification used when
  restoring a trashed book, the fs-regroup and iTunes-repoint maintenance tools
  (they change database rows, not files), and a few maintenance checks that look
  only at the frozen iTunes tree. The merge tools and the deduplication jobs
  already followed shortcuts.
