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
a later delete fail with "already soft deleted". The two trash paths that
relabel a book `deleted` (single delete, reconcile) now record the prior state
in the new `pre_trash_library_state` column, and every restore path uses one
rule: keep a state the trash did not overwrite, else the recorded state, else
(books trashed before this change) `organized` when every present file is
inside the library root and outside the iTunes library, otherwise `imported`.
A user restore (single, bulk, batch update) also clears `MergedIntoBookID`, so a
restored merge loser shows again; operation revert and combine undo leave that
pointer to their journals. A restored former primary yields to the member that
held the flag while it was in the trash.
