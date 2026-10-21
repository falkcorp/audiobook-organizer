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
