Added the Apply of the `retired-versions` repair, the seventh of nine stacked
changes for retired book versions, behind the new setting
`retire_versions_apply_enabled` (env `RETIRE_VERSIONS_APPLY_ENABLED`, default
false). With the setting off the fixer stays plan-only and refuses every apply
with a message naming the setting, exactly as before. With it on, each
applicable trashed book is re-checked under the merge lock, then its live
`book_file` rows are re-keyed in one atomic batch as a retired version of the
book it lost to (nothing is deleted), its external ids move to that book, its
merged-into link is set, and any retired versions it held move too. Every write
is journaled first (`repair_plan_record`, `book_version_create`,
`book_file_retire`, `external_id_reassign`, `book_merged_into`,
`retired_version_rehome`); the undo package does not yet understand the three
new change types, which land with the undo change. Applies are a dry run unless
explicitly told to write, partition by holder so no two workers touch one book,
skip a book already retired, and finish a run that was cut off after its rows
moved. A trashed book an unreversed combine absorbed is held as retry_later
until the combine-undo link exists. The plan no longer counts a retired row as
a live owner of its path, so sibling shells keep stable fingerprints after the
first one retires.

The purge, hard delete and restore paths now refuse a retire shell, a trashed
book whose file rows were retired onto another book and whose retired versions
still hold rows (`merge.ErrBookIsRetiredShell`). The nightly auto-purge and
"Purge now" skip it (new `skipped_retired_shell` count and ids in the purge
result, also in the auto-purge log line), "Discard progress and purge", the
single and batch hard delete and `RestoreFromTrash` refuse it (HTTP 409, code
`RETIRED_SHELL`), and a failed retired-version lookup fails closed. This holds
until the journaled purge (PR 6) can snapshot a shell, and it keeps a restore
from bringing back an empty imported book.
