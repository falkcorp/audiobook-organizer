Added the undo journal for retired book versions, the fifth of nine stacked
changes. A retire is now journaled in three new change types
(`book_version_create`, `book_file_retire` and `retired_version_rehome`) with
a matching revert for each: reverting puts every retired file back on the book
it came from, with its flag and version id as before, and deletes the retired
version. Of the index keys only the unique iTunes persistent id key comes back
exactly as it was; a path or hash key that another live row also carries stays
with that live row (a live row always wins a shared key), and a key nothing else
claims goes back to the restored row. The writer gained `RetireVersion` and
`RehomeRetiredVersion`, which journal first and refuse (voiding the journal)
rather than half apply. A new `RevertOperationForBooks` reverts one shell of a
multi-shell operation at a time (it accepts shell ids only, and a scoped run
keeps its own owed-settle record instead of clearing the whole operation's), and a shell that a purge already deleted is
recreated from its purge snapshot before its rows are reverted once the purge
change provides one (until then the revert refuses it by name). Every by-id
revert of an earlier operation now refuses a file that has since been retired
(changed since) instead of overwriting it, and the revert and combine-undo
path and sibling reads count retired rows. Undoing a combine first reverses
the retires linked to each absorbed shell (the new `merge:combine-retire:` key
family, one link per shell and retire op, so a shell retired twice is reversed
newest first) and refuses the whole undo, writing nothing, if any retire cannot
be reversed; each retire op is read once per link under the merge lock. A
retire is refused unless its shell is still in the trash, and its link is
written even when the combine left the shell no files. The revert of a chain
rehome moves the version back onto an old holder that is in the trash (a new
`RestoreRetiredVersionHolder` store method) and recreates a purged one from its
purge snapshot once one exists, and the operation revert lock is now keyed by
the exact operation id rather than a shared stripe. Restoring a trashed book
decides its library state on its live files, not on retired versions it holds. The retire journal rows are exempt from journal pruning, so an old retire is never mistaken for a reversed one. Nothing retires a row yet, so running behavior does not change.
