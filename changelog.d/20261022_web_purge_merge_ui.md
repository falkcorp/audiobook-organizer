The Library page now follows the new server behaviour. The "Purge soft-deleted"
dialog runs a preview first ("N purgeable, M kept, by reason"), starts the purge
as an operation with a progress bar, and ends with the full breakdown (purged,
kept: owns files, kept: combine undo possible, kept: listening progress,
failed). The trash section shows the purgeable versus kept counts. "Merge as
versions" now has a keeper picker (radio choice, title, file count, full path,
"Kept" and "Linked as a version" chips), leaves trashed books out of the request
and says so, polls the merge operation, and reports its real outcome: the
failure reason, or which losers were skipped and why, instead of "Merged" as soon
as the request was queued. A deleted book is dropped from the selection. The
"block this file hash" checkbox is off by default in the Library single and
bulk delete and on the book page, and the toast names the live books that kept
the hash from being blocked.
