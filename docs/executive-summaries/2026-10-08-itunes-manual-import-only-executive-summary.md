<!-- file: docs/executive-summaries/2026-10-08-itunes-manual-import-only-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: a6961ae2-2a14-43f3-8bf9-72525a681bcb -->
<!-- last-edited: 2026-10-08 -->

# iTunes: one manual import, safe to run again

Branch `feat/itunes-manual-import-only` (PR not yet opened). Plan:
[2026-10-08 iTunes manual import only](../plans/2026-10-08-itunes-manual-import-only.md).

## Executive Summary

- **One iTunes action.** There is now a single button, **Import iTunes
  library**. Nothing iTunes-related runs on its own: the background sync,
  the "sync iTunes first" step before organizing, and the scheduled sync
  setting are gone.
- **Running it again is safe as far as matching allows.** Each album is
  matched to a book already in the library before anything is added, so a
  second import of the same library adds no books.
- **A re-import never moves files.** Matching an existing book updates only
  its iTunes play count, rating, bookmark and last-played date. Its files
  and stored locations stay where they are, even when the import is set to
  organize.
- **A clear warning before a repeat import.** When books are already linked
  to iTunes, the button first explains what can still go wrong and asks
  **Import anyway** or **Cancel**.
- **A plain result.** When an import finishes, the panel says how many
  albums were linked to existing books, how many were added, and how many
  were skipped.
- **The iTunes ID rules are unchanged.** Deleted books stay deleted, and
  iTunes IDs still follow books through merges and repairs.

## The background sync and its settings

**What it was.** Besides the import, the app had an incremental "sync" that
could run before every organize, from a settings switch that was on by
default, and from a "Sync Now" button. It also had a conflict dialog wired to
a server endpoint that never existed.

**Why it mattered.** Two ways to bring iTunes in meant two sets of matching
rules to keep correct, and one of them could run without anyone asking for
it. The owner wants iTunes brought in only when someone chooses to.

**The fix.** The sync, its settings, its schedule hook, its server endpoint
and its buttons were removed. Old saved settings and environment variables
that mention the sync still load without error and are ignored.

## Re-importing matches before it adds

**What it was.** The import already matched albums to existing books, but it
filled in iTunes play data only where a book had none, so a re-import could
not update a play count or bookmark.

**Why it mattered.** With the sync gone, re-importing is the only way to
bring newer play data across.

**The fix.** Each album is skipped if its iTunes ID (the persistent ID, or
PID, iTunes gives every track) belongs to a deleted book. Otherwise it is
linked to the book that ID already belongs to, or to the one book at the
same file path or holding one of its tracks. Only an album with no match
becomes a new book. A link now refreshes the play data, but only the fields
the library file actually carries, so importing from the binary `.itl`
format never wipes a bookmark. A test imports the same made-up library
twice and checks the second run adds nothing.

## A re-import in organize mode no longer moves linked books

**What it was.** In organize mode the import copied every book in the
"imported" state into the library folder, not just the books that run had
added.

**Why it mattered.** A re-import would have moved books it had only linked,
and changed where the app thinks their files are.

**The fix.** The organize and metadata-lookup steps now act only on the
books the current run added.

## The warning and the result

**What it was.** The settings panel had an import button, a "Force Import"
button with a misleading warning, and sync buttons.

**Why it mattered.** Matching cannot catch every case: an album iTunes
re-created with new IDs, or whose files moved, looks new.

**The fix.** The panel has one **Import iTunes library** button. If any book
is already linked to iTunes it shows: "You've imported from iTunes before.
We match each album to your existing books by iTunes ID, then by file path.
Albums iTunes has re-created with new IDs, or whose files moved, can't be
matched and will be added as new books, so you may see duplicates. Nothing
in iTunes is changed, and no files are moved." If the app cannot tell, it
shows the warning anyway. The finished run reports linked, added and skipped
counts.

## One safety check now keys on a different setting

**What it was.** The merge safety check refuses merges when an iTunes
library is known to exist but its folder is not configured. It used the
removed sync switch, which was on by default, as the "library exists" sign.

**Why it mattered.** Without a replacement, the check would have lost its
trigger.

**The fix.** The check now uses the configured `.itl` library file path as
that sign. On an install with no iTunes paths at all, merges are no longer
refused, and the built-in guard on the standard iTunes media folder still
applies.
