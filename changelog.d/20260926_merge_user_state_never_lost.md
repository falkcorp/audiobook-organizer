### Fixed

#### Merges no longer lose a listener's state

When books were merged, moving each user's position, finished flag, progress,
last-played time and hide-from-continue-listening flag onto the surviving book
was best-effort: a failure was only logged, the state stayed under the
merged-away id, and GET progress on the survivor answered 404. Journaled merges
also skipped any user whose rows could not be read.

- Every merge follow now writes a durable pending-repair record
  (`merge_user_state_pending:<loser>:<winner>`) before it moves anything and
  deletes it only after everything moved. A failed move leaves the record; a
  failure that cannot even write the record fails the merge step (the loser is
  not retired, or the merge reports an error).
- User state no longer depends on sync identity: a missing sync store or a
  failed syncID mint used to skip every user's progress too.
- Conflict rule when both books have state for one user: finished is sticky,
  otherwise the newest lastUpdate wins the position (it was "furthest progress
  wins"), last played is the later of the two, the hide flag is kept if either
  side has it, and reset tombstones are unioned. Chapter / split-part merges
  keep their slice rule for finished and position but now also carry last
  played and the hide flag. A drained loser row is no longer mistaken for live
  state, so a replayed merge moves nothing twice.
- Bookmarks are copied onto the surviving item, de-duplicated by time with the
  original CreatedAt kept, instead of being reachable only through the alias
  read (which still works). The merged-away copies stay, so undo still gives
  the restored book its own bookmarks.

### Added

#### `maintenance.repair-merged-user-state`

The manual op (preview by default, `{"apply": true}` to move) finds ubs, upos
and bookmark rows still stored under merged-away, soft-deleted or purged books
whose live survivor is known (sync redirect chain, then
`merged_into_book_id`) and moves them with the same rule. Books with no known
survivor are counted, not guessed. It also completes the pending-repair
records. Trigger with `POST /api/v1/operations/v2` and
`{"def_id": "maintenance.repair-merged-user-state", "params": {"apply": true}}`
(`"params": {}` previews).

#### Pending user-state moves complete on their own

A server ticker (every 15 minutes, outside the op system, stopped on shutdown)
completes pending-repair records older than 5 minutes, one at a time under the
merge lock, and every merge first completes records involving its own books.
A record whose merged-away book is live again is deferred. The gauge
`audiobook_organizer_merge_user_state_pending` reports how many remain. The
scanner's version-link path (`FollowBookIDChange`) now writes the same record,
and undoing a combine restores positions with their original timestamps.

#### Automatic survivor election prefers the book a listener uses

When a merge elects its survivor automatically and exactly one candidate has
client-visible user state (progress, a finished flag, a bookmark, or sync
aliases pointing at it), that candidate is kept, so no new alias is created.
An explicit keep id, the audio-route rule, the iTunes-ghost rule and
`library_state=organized` still win. The merge result reports the book the old
rule would have kept in `elected_without_user_state`.
