The purge now refuses to run while any combine-journal row cannot be decoded. A
combine journal records which trashed books (the absorbed shells) the
combine's undo still needs; a row that cannot be read cannot say which, so
skipping it could let the purge delete a book an undo needs. Preview, run, the
nightly purge, "Purge now" and discard-and-purge all refuse with a message that
names the unreadable row's key and the operation that lists such rows; the
reason shows in the purge dialog and toast (`purge refused: ...`). This matches
how an unreadable auto-merge journal row already behaved. New read-only
operation `maintenance.combine-journal-check` lists every unreadable
combine-journal row with its full key and decode error (it fails while any
exist, so it shows red in the operations list) and modifies nothing. Repair is
manual: fix or remove the row with the key shown, then purge again. Cost: one
corrupt row blocks every purge, the nightly one included, until it is repaired.
The lenient combine-journal listing (the history list and the pointer backfill)
still skips an unreadable row with a warning.
