The `fragment-consolidation` Repairs fixer has a new row class, `hash-copy`: a
live single-file book of ANY title or file name whose one present file row has
the same file hash as a present row of exactly one other live multi-file book is
proposed for retirement into that book (one row per child, so the plan's
`hash-copy` count is the number of proposals). Until now only books with
chapter-shaped names or titles were considered, so single-file copies of a
multi-file book's tracks were never offered. Apply retires the child the way a
merge does (listening state and positions follow to the parent at the track's
offset, external ids move, soft-deleted with `merged_into_book_id` set), every
step journaled and undoable from the apply operation; no `book_file` row is
deleted and the parent's rows are not written. A child whose hash is carried by
two or more live multi-file books is listed as `held_ambiguous_parent`; one that
matches a parent row by name and size where a hash is missing on either side is
listed as `skipped_no_hash`. Neither is ever applied. Apply re-checks the whole
library for a second parent under the merge lock and refuses the row if one
appeared since the plan. Trial: `POST /api/v1/repairs/fragment-consolidation/plan`,
then `GET /api/v1/repairs/fragment-consolidation/plan/<op_id>/rows?class=hash-copy`.

A hash only retires a book when it can stand as proof. A hash-copy child is held
(never applied, listed with its reason) as `held_file_absent` when its file or
the parent row's file is not on disk, `held_degenerate_hash` for a zero-length
file, the empty-file digest, or two files of different sizes,
`held_short_or_boilerplate` for a known duration under 60 seconds or an intro,
credits or sting title, and `copy_unproven` when both paths name one file. Over
the sampling threshold, where the stored hash covers only the head, tail and
size, the two files are compared by whole-file SHA-256 at plan time
(`held_partial_hash_unproven` when that read fails, `held_partial_hash_mismatch`
when the bytes differ); the proof is stored with the row and apply re-checks
both files by stat rather than re-reading them. `book_ids` now scopes only the
children, so a scoped plan sees the same parents as a full one, and an iTunes
copy of the parent that the iTunes-parent rule sets aside no longer counts as a
second parent at apply.

Every Repairs retire (`fragment-consolidation`, `duplicate-copies`,
`consolidation-leftovers`) now carries the retired book's own ratings and notes
(fields the survivor left empty only; the survivor's are never overwritten) and
its places in static collections and playlists onto the survivor, as the
listening state already was. Each move is journaled as `user_content_follow`
and restored by the undo of the apply, which refuses to overwrite a rating,
collection or playlist edited since.
