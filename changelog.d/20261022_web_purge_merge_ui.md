The Library page now follows the new server behaviour. The "Purge soft-deleted"
dialog runs a preview first ("N purgeable, M kept, by reason"), starts the purge
as a background operation, and ends with the full breakdown (purged, kept: owns
files, kept: undo possible, kept: listening progress, failed, and whether it
stopped early and why). The trash section shows the purgeable versus kept
counts. "Merge as versions" now has a keeper picker (radio choice, title, file
count, full path, "Kept" and "Linked as a version" chips), leaves trashed books
out of the request and says so, runs the merge operation in the background and
reports its real outcome when it ends: the failure reason, or which losers were
skipped and why, instead of "Merged" as soon as the request was queued. "Merge
into one book" does the same. A deleted book is dropped from the selection. The
"block this file hash" checkbox is off by default in the Library single and
bulk delete and on the book page, and the toast names the live books that kept
the hash from being blocked.
A failed status poll is not treated as a failed operation: the page says "still
checking", keeps the books (or the purge) in flight and keeps polling with
backoff until the operation ends, so the buttons are not re-enabled while the
server may still be working. A purge that is refused or unreadable shows its
reason once, in the dialog when it is open and in a toast only when it is not.
A combine outcome whose result has no `operation_id` says "undo id unavailable"
and where to look instead of silently dropping Undo; the server always stores
`operation_id` on a successful combine.
