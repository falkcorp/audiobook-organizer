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
undone in any order, including oldest first, put every book back in its exact
position in every collection and playlist, survivors that are themselves
retired later included (below). The records of a list are read from one place:
a new index over the journal, `opchange_by_list:<kind>:<list id>:<op>:<change>`,
written in the same batch as each collection or playlist carry row (ratings and
notes are not indexed) and removed only together with its row, so one call (`GetListChanges`) returns every row any retire
into the list wrote, whichever survivor or retired book it names. There is no
per-book journal walk and no read cap any more, so no history can need more
reads than the cap allowed. A carry's index entry is written in the same batch
as its row, so a row never exists without its entry. The first open of the store
after this release reads the whole journal once, under its own completion key
(`system:backfill:opchange_by_list_index_v1_done`), and stages the entry of any
carry row that lacks one (rows written by a build without the index), in chunks
of 2,000, logging progress every 50,000 rows and the row count, entries staged
and duration at the end. The key is written with the last chunk, so a start that
dies part way repeats the scan, which is idempotent, and a store that has the key
is never scanned for the index again.

A book is placed by the graph only when both of its neighbours are found in the
list now and nothing but the survivor (or another survivor's ghost) sits between
them. The records also have to agree with the list: two books they order must be
listed in that order, or the owner has rearranged the list since and nothing
found from the records is trusted. A book that cannot be bounded that way is
never placed by a guess: the recorded index is used and reported. A record
without neighbour anchors (from an earlier build of this feature, or damaged) is not restored by
index any more: the revert refuses that row with a reason, the undo preflight
shows it as not restorable automatically, and nothing is written.

A survivor can itself be retired into another survivor (the duplicate-copies
fixer does), and the operations can be undone in any order, the older ones
first included. A survivor that was listed only because retires stood on it is
put back by the revert of its own retire only while one of those retires still
stands; once they have all been reverted it never belonged to the list on its
own, so it stays out and the revert says so (`books_left_out`, named in `kept`).
When a book's retire is reverted while the survivor that took its slot has been
retired out of the list since, the slot is not the survivor's to give back: the
book goes back beside its neighbours, found through the survivor's stand-in, and
the revert of the survivor's own retire places it later.

A survivor is in a list on a retire's account from the retire that put it there
(a "took" marker) until the last retire of that epoch is reverted. A later "took"
marker ends every earlier epoch, so a standing marker of an earlier epoch (the
survivor left the list by hand, or a revert removed it and died before stamping
the marker) no longer counts: its rows have no say over the survivor, in the
revert, the preflight, and the order graph.

Journal rows are ordered by a store-level sequence that never steps back, not
by the wall clock. Each row carries `CreatedSeq` (and a row stamped reverted
carries `RevertedSeq`, never below its own `CreatedSeq`, also for a row born
reverted): the larger of the wall clock and one past the last stamp. A wall clock
that steps back (NTP, a restore, a VM resume) between two retires therefore
cannot reorder the journal, and the undo code orders markers by the sequence,
falling back to the wall time only for rows written before it existed. The
stamp is taken under a short lock that is not held across the commit, so
concurrent writers commit in parallel. To keep the sequence from ever repeating
after a restart, the store persists a CEILING (`system:journal_clock`): a value
above every stamp handed out, written with one synced write when the sequence
reaches it and moved one minute ahead (about one extra write a minute under
sustained load, none when idle). The next open starts the sequence at the
ceiling exactly, with no margin and no journal scan, and a clean close lowers the
ceiling to the last stamp so a normal restart wastes nothing. A crash leaves the
ceiling at most one minute ahead, which only skips unused numbers. The journal
is scanned at open only when the ceiling is missing or unreadable (a store from
before this release, or a damaged key); the scan derives the sequence from the
rows and rewrites the key, also when the key was unreadable and the journal is
empty, so a damaged key is repaired once and never stops the store opening.

Lists the owner arranged by hand since the retire are reported, not silently
rewritten. A survivor found listed somewhere other than the slot the retire gave
it is left where it was put and the book goes back by the records; the revert
names it (`kept`: "left where you placed it"). A slot the records cannot confirm
(a book they do not order sits beside it, or the list was rearranged) is given
back as recorded and named too. Both count in `lists_edited_by_hand`, and the
summary says "list position(s) restored beside books arranged by hand since".
A position placed by the recorded index counts in `restored_by_index`. These
reports are what the revert says; it does not claim to know the owner's intent,
and a list the owner never touched is never reported.

Verified by simulation (the real revert code against a model of the journal,
random histories, "exact" meaning every list ends as it was before the retires):
140,000 histories (two and three survivors, crashes part way through an
operation's revert, survivors retired into survivors, undone in any order) all
exact, none reported, refused or silent. A further 47,000 adversarial histories
(shared timestamps, a wall clock stepping back, one step back, a row journaled
before its marker, deep chains with crashes), now run with the sequence fields
stamped and the wall time left non-monotonic, all exact, none reported, refused
or silent. A further 14,500 histories in which a survivor marker is left behind
with no live row of its retire (a crash between the marker and the row, or a row
voided), over 40,000 orphan markers injected in all, none left unreported: 11,500 exact, and in the 3,000 where the owner also edited
the lists by hand 1,314 exact, 1,485 reported and 201 refused for manual review;
0 silent wrong lists. 12,000 histories in which the owner added, removed or reordered list
entries by hand since the retires: 0 silent wrong lists (final or
intermediate); 5,716 exact, 5,544 reported (named in `kept`), 740 refused for
manual review with a reason (nothing written for a refused row).

A row is stamped reverted together with its survivor-side marker, in the same
batch as the list. A marker stands until its row is restored, so a row that
failed or was refused leaves it standing for the retry, so the retry is not
refused and the reverts in between see the truth; and a row stamped with its
list is never retried against a list a newer retire has changed since. A carry
needs a store that commits several journal rows in ONE batch
(`database.ChangeBatchJournal`, which the Pebble store has) and is refused before
its first write otherwise. All the rows of a carry step (the row and its marker)
are created in one commit and the index is updated only after it, so a failure
leaves neither; a refused carry voids its row and its marker in one commit, so a
failure can no longer leave one of them stale. A survivor marker that has no
live row of its retire (an orphan: a crash left the marker without its row, or
the row was voided) is ignored by the revert, the preflight and the order graph,
so it can never keep a survivor listed; the revert reports it in
`orphan_markers` and in `kept`, once per marker. The revert of a collection
or playlist row requires a store that writes the list and the stamp in one batch
(`database.CollectionMembershipMarkStore`, `database.UserPlaylistMarkStore`; the
Pebble store has both) and refuses one that does not. A revert that finds the
book already listed (the owner put it back by hand while the retire stood) takes
the survivor out when the row had borrowed it and nothing else stands on it,
counts it in `survivors_released` and says so in `kept`; otherwise it only
stamps the marker. Everything that reads a list's journal to decide a list write
and then makes it holds one lock per list: the reverts of collection and
playlist rows, and the retire's own marker journaling and list write, so two
operations undone at once cannot each see the other still standing. The carry's
`user_content_follow` rows are never removed by journal pruning, like the other
rows an undo cannot be decided without. Marker rows restore nothing, so the revert
result counts them in a separate `markers` figure, not in `restored`.

The undo preflight decides a collection or playlist row from the same list
journal the revert reads, so a row the revert restores exactly is predicted
restorable. A journal read that fails refuses the row as unreadable
(`field_unreadable`) and never as restorable; so does a store that cannot read
the list journal at all, since no prediction can be made without it. Rows that fail the check are
never filed under `check_failed`, which never counts toward offering Undo.
