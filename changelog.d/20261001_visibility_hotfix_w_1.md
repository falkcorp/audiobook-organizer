### Fixed

#### Maintenance ops scoped to "what ABS lists" now use `ABSLibraryFilter`

`maintenance.repoint-missing-to-folder-audio` and `duration-backfill`'s
zero-rows mode hand-wrote the ABS scope as an explicit primary flag plus
`organized`, with no quarantine check. Organized books with a nil primary flag
(about 18,000 ungrouped books in prod), which ABS lists, were never repaired, and
quarantined books ABS hides were. Both now call `database.ABSLibraryFilter`
(visibility audit 2026-10-01 #1, #2).

#### AI filename parse lands on the primary the UI shows

`primaryVersionOf` looked for an explicit-true member only, so in a group whose
primary had a nil flag the parse was written to the demoted sibling. It could
also redirect to a trashed member. It now reads the group's incumbent through
the new `versionprimary.Incumbent` (audit #6).

#### Restoring a book from the trash restores its library state

`RestoreAudiobook` always set `imported`, so a restored book from the library
folder dropped out of ABS. The bulk restore, `marked_for_deletion=false` batch
updates, and operation revert left the `deleted` label in place, which also made
a later delete fail with "already soft deleted". The three trash paths that
relabel a book `deleted` (single delete, reconcile, a batch update writing
`library_state=deleted`) now record the prior state in the new
`pre_trash_library_state` column, and every restore path uses one rule: keep a
state the trash did not overwrite, else the recorded state, else (books trashed
before this change) `organized`. In every case a book only comes back
`organized` when it has present files and all of them are inside the library
root and outside the iTunes library; otherwise it comes back `imported`. That
keeps a combine's absorbed shell, which owns no files, from returning as an
empty book in ABS.

A user restore (single, bulk, batch update) goes through one helper,
`merge.RestoreFromTrash`. It is a no-op on a book that is not in the trash, so
"restoring" a live book can no longer demote it as its group's primary. On a
trashed book it clears `MergedIntoBookID` and removes the merge's sync-identity
redirect (and any pending user-state move onto the old survivor), so a restored
merge loser or combine shell is listed by ABS as a book of its own; listening
progress already moved to the survivor stays there. Operation revert and combine
undo leave the merge pointer to their journals. A restored former primary yields
to the member that held the flag while it was in the trash, by the same
`versionprimary.Incumbent` rule in every path, including combine undo, which
until now ignored a primary whose flag was unset; combine undo also decides the
library state after the files are moved back.
