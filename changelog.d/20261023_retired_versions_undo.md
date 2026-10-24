Added the undo journal for retired book versions, the fifth of nine stacked
changes. A retire is now journaled in three new change types
(`book_version_create`, `book_file_retire` and `retired_version_rehome`) with
a matching revert for each: reverting puts every retired file back on the book
it came from, with its flag, version id and path and PID index keys exactly as
before, and deletes the retired version. The writer gained `RetireVersion` and
`RehomeRetiredVersion`, which journal first and refuse (voiding the journal)
rather than half apply. A new `RevertOperationForBooks` reverts one shell of a
multi-shell operation at a time, and a shell that a purge already deleted is
recreated from its purge snapshot before its rows are reverted once the purge
change provides one (until then the revert refuses it by name). Every by-id
revert of an earlier operation now refuses a file that has since been retired
(changed since) instead of overwriting it, and the revert and combine-undo
path and sibling reads count retired rows. Undoing a combine first reverses
the retire linked to each absorbed shell (the new `merge:combine-retire:` key
family) and refuses the whole undo, writing nothing, if any retire cannot be
reversed. Nothing retires a row yet, so running behavior does not change.
