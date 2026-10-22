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
original position in every collection and playlist, as long as the survivors
are not themselves retired. When several survivors share one list the graph
reads the journals of the other books it reaches, of every book named by a
record it uses, and of every listed book left between the bounds it found
(`GetBookChanges`, at most 128 reads per book placed) and follows each
survivor's own stand-in and ghost. A book is placed by the graph only when both
of its neighbours are found in the list now and nothing but the survivor (or
another survivor's ghost) sits between them; a read that fails, a history that
needs more than 128 reads (the report names the cap), or a book that cannot be
bounded that way is never guessed: the recorded index is used and reported.

A survivor can itself be retired into another survivor (the duplicate-copies
fixer does). A survivor that was listed only because retires stood on it is
put back by the revert of its own retire only while one of those retires still
stands; once they have all been reverted it never belonged to the list on its
own, so it stays out and the revert says so (`books_left_out`, named in `kept`).
Reverting such a history out of order can also refuse, when a survivor that
stands in a book's slot has been retired since and cannot be swapped back, or
fall back to the recorded index and say so; it never leaves a wrong list
unreported.

A row is stamped reverted together with its survivor-side marker, in the same
batch as the list. A marker stands until its row is restored, so a row that
failed or was refused leaves it standing for the retry, so the retry is not
refused and the reverts in between see the truth; and a row stamped with its
list is never retried against a list a newer retire has changed since. A row
left standing with its marker already stamped (an older build wrote them apart)
whose book a newer retire has taken out again is stale: the revert writes
nothing, marks it done and counts it in `superseded`, naming it in `kept`. The
revert of a collection or playlist row requires a store that writes the list
and the stamp in one batch (`database.CollectionMembershipMarkStore`,
`database.UserPlaylistMarkStore`; the Pebble store has both) and refuses one
that does not, rather than fall back to two writes. A retry that finds the book
already listed (a list an older build wrote without its stamp) takes the
survivor out when the row had borrowed it and nothing else stands on it, counts
it in `survivors_released` and says so in `kept`; otherwise it only stamps the
marker. Everything that reads a survivor's journal to decide a list write and
then makes it holds one lock per list: the reverts of collection and playlist
rows, and the retire's own marker journaling and list write, so two operations
undone at once cannot each see the other still standing, and an undo cannot
read the journal between a retire's marker and its write. The carry's
`user_content_follow` rows are kept by journal pruning like the other rows an
undo cannot be decided without, since the order graph reads every record,
reverted or not. When no recorded neighbour can be found in the list (the owner
reordered or emptied it since), or the row was journaled before neighbours were
recorded, the recorded index is used instead and the revert reports it, never
silently: the revert result counts these in `restored_by_index`, names each in
`kept`, and the summary says so. Marker rows restore nothing, so the revert
result counts them in a separate `markers` figure, not in `restored`. The undo
preflight predicts a refusal for a carried collection, playlist or rating that
was edited since (it gives no prediction when the store cannot read lists).
