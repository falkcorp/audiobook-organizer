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
credits or sting title, and `skipped_copy_unproven` when both paths name one file or one is a hard link of the other. A held row's id is `held-<kind>:<child>` (the kind's own `held_`/`skipped_` is not repeated). Over
the sampling threshold, where the stored hash covers only the head, tail and
size, the two files are compared by whole-file SHA-256 at plan time
(`held_partial_hash_unproven` when that read fails, `held_partial_hash_mismatch`
when the bytes differ); the proof is stored with the row and apply re-checks
both files by stat rather than re-reading them, except on a filesystem with
whole-second timestamps, where a same-size rewrite can leave the stat unchanged,
so apply reads both files again under the merge lock and refuses the row if the
digest differs. `book_ids` now scopes only the
children, so a scoped plan sees the same parents as a full one, and an iTunes
copy of the parent that the iTunes-parent rule sets aside no longer counts as a
second parent at apply.

Every Repairs retire (`fragment-consolidation`, `duplicate-copies`,
`consolidation-leftovers`) now carries the retired book's own ratings and notes
(fields the survivor left empty only; the survivor's are never overwritten) and
its places in static collections and playlists onto the survivor, as the
listening state already was. Each move is journaled as `user_content_follow`
and restored by the undo of the apply, which refuses to overwrite a rating,
collection or playlist edited since. A store that cannot reach collections or
playlists refuses the retire before its first write rather than carrying part
of the state. The lists are read afresh for every retire, so a list the owner
changed since the last one is seen. Ratings revert field by field, so
reverting one retire never undoes a field another retire filled. Each list
carry journals a marker row on the survivor (`collection-took` or
`playlist-took` when the survivor took the retired book's slot,
`collection-held` or `playlist-held` when the book was only removed because the
survivor was already listed). Reverting one of several retires into the same
survivor reads the markers other retires left on it. A survivor that one
retire borrowed (it was already listed, so the retired book was only removed)
stays in the list while any retire that put it there is still standing, and
leaves with the last of them, whichever order the operations are undone in; a
survivor a later retire borrowed from an earlier one is counted the same way.
Each carry also records the books right before and after the retired one, and
the survivor keeps a record for every retire it took part in, including a
second retire of the same pair after the first was reverted. The revert puts
the book back by those neighbours: it builds an order graph from the records of
every retire into the list, walking through the books other retires took out
(and treating a survivor that took a book's slot as that book), so operations
undone in any order, including oldest first, restore every book to its exact
original position in every collection and playlist. When several survivors
share one list the graph reads the journals of the other books it reaches
(`GetBookChanges`, at most 128 reads per book placed) and follows each
survivor's own stand-in and ghost. A book is placed by the graph only when both
of its neighbours are found in the list now and nothing but the survivor (or
another survivor's ghost) sits between them; a read that fails, or a book that
cannot be bounded that way, is never guessed: the recorded index is used and
reported. In randomised tests of two survivors sharing a list (48,000 retire and
revert orders) every order was exact with the journals readable, and with the
journal reads failing nothing was silently wrong (the books were reported by
index instead).

A marker is stamped reverted only together with the row it belongs to: a row
that failed or was refused leaves its marker standing for the retry, so the
retry is not refused and the reverts in between see the truth. The list write
and the marker stamp are one batch where the store has it (collections and
playlists on the Pebble store), so a death between them cannot leave the book
back in the list with its marker still standing. On a store without that batch,
the retry finds the book already listed and, when the row had borrowed the
survivor and nothing else stands on it, takes the survivor out of the list and
says so in `kept`; otherwise it only stamps the marker. Everything that reads a
survivor's journal to decide a list write and then makes it holds one lock per
list: the reverts of collection and playlist rows, and the retire's own marker
journaling and list write, so two operations undone at once cannot each see the
other still standing, and an undo cannot read the journal between a retire's
marker and its write. When no recorded neighbour can be found in the list (the
owner reordered or emptied it since), or the row was journaled before
neighbours were recorded, the recorded index is used instead and the revert
reports it, never silently: the revert result counts these in
`restored_by_index`, names each in `kept`, and the summary says so.
Marker rows restore nothing, so the revert result counts them in a
separate `markers` figure, not in `restored`. The undo preflight predicts a
refusal for a carried collection, playlist or rating that was edited since (it
gives no prediction when the store cannot read lists).
