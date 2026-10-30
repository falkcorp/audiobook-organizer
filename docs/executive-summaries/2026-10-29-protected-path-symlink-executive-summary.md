<!-- file: docs/executive-summaries/2026-10-29-protected-path-symlink-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 33552e57-ccc8-45c9-95a3-eafe9d145b0e -->
<!-- last-edited: 2026-10-29 -->

# A file reached through a shortcut can no longer slip past the iTunes protection

Branch: fix/protected-path-symlink-spelling (pull request link and merge commit to be added by whoever opens it).
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
  folder the app may not read) is no longer waved through: the purge keeps the
  file and retries, and tag writes and previews treat the path as protected.
- The organizer's move-or-copy decision and the metadata write-back used their
  own copies of the same check with the same gap; they now use the shared one,
  and the shortcut-following code that already protected the merge tools moved to
  a shared place so there is one copy of it.
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
same hole, and reading it as "protected" for the purge would have let a trashed
book go while its file stayed behind with no record of it.

**The fix.** For tag writes and previews such a path counts as protected. For the
purge it counts as a path that cannot be decided: the book stays in the trash and
the file is kept, and the purge tries again on the next pass. A path that is a
loop gives up after a few passes with a message, like any other path that can
never be resolved. If the trouble is in a configured protected folder instead of
the path, every due book is held until the configuration is fixed, and the
message names that folder.

## What to know

- Left as they were, for a later decision: the list of Deluge seeding folders and
  the explicitly configured protected paths (still compared as written), the
  classification used when restoring a trashed book, the fs-regroup and
  iTunes-repoint maintenance tools (they change database rows, not files), and a
  few maintenance checks that look only at the frozen iTunes tree. The merge
  tools and the deduplication jobs already followed shortcuts.
- The purge check still does not apply the frozen iTunes tree rule on its own;
  that rule is enforced by the merge tools and the maintenance tools.
