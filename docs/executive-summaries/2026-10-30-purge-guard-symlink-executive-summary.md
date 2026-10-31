<!-- file: docs/executive-summaries/2026-10-30-purge-guard-symlink-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: f0500b1a-d04a-4a76-9491-9d387618b6bc -->
<!-- last-edited: 2026-10-30 -->

# The trash purge can no longer delete a file that another book plays through a shortcut

## Executive Summary

- When a book is kept in the library as a shortcut (a symbolic link) to a file that belongs to another book's older version, emptying the trash could delete that older version's file. The shortcut then pointed at nothing, and the book that used it could no longer play. The purge now recognises the shortcut, keeps the file, and leaves the version in the trash.
- If the purge cannot work out where some other book's shortcut leads (for example the shortcut loops back on itself), it now refuses to delete anything rather than guess.
- A book's older or trashed versions are stored in a hidden folder inside the book's folder. The player, the Audiobookshelf view and the book's stored length were counting those stored files as if they were part of the book. They are now left out, so a book with an older version no longer reads as longer than it is.

## The purge deleted files that shortcuts pointed to

**What it was.** Before deleting a trashed version's files, the app asks whether any other live book still uses each file. It asked by comparing file names exactly. A shortcut has its own name, different from the file it leads to, so the question never found it.

**Why it mattered.** This is silent data loss: the file was deleted, nothing reported a problem, and the other book failed to play only when someone next opened it. The folders where this can happen are exactly the ones the "shortcut" library setting creates.

**The fix.** The app now looks up where every other live book's files and folders really lead, once per purge, and keeps any file that one of them reaches, including through a chain of shortcuts or a shortcut to a whole folder. The lookup runs on all processor cores. A path that cannot be followed stops the purge; the log names the path. One cost to know about: a single broken shortcut anywhere in the library holds up all trash purges until it is fixed.

## Older versions were counted as part of the book

**What it was.** A book's older versions are kept in a hidden subfolder of the book's own folder. The code that adds up a book's files treated everything inside the book's folder as part of the book.

**Why it mattered.** A book with a spare version showed a longer running time and a larger size than it really has, and the player's timeline included files that are not meant to play.

**The fix.** Files in those hidden version folders are now set aside before anything is added up or compared. The stored length and size of such books drop to the true figure the next time they are recalculated, which is the intended correction.
