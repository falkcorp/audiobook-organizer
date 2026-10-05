### Changed

#### Merge: a loser's version siblings now follow it into the survivor's group

- `merge.Service.MergeBooks` (every merge path: link-as-versions, `dedup.book-merge`, the dedup review and cluster endpoints, diagnostics `merge_versions`, maintenance `BookMerger`) now unites each live loser's version group into the merge's group. The loser's other live members move in as non-primary versions and stay live; only the named losers are soft-deleted. The group they leave has no live member, so no primary is elected for it. Owner decision 2026-10-05.
- The siblings are read and moved under the version-group locks the merge already takes, each write re-checking membership, so a concurrent change aborts the merge. If the membership writes fail after at least one write landed, each left group's primary is handed on; a merge refused before its first write changes nothing.
- A sibling gets the same pre-write refusals as a participant: an unscanned (provisional) file refuses the merge (`ProvisionalScanError`), and so does a file under a protected iTunes root (`ITunesProtectedError`), with nothing written. An iTunes organized_source original shares a group with its organized copy, so without this a merge whose loser was that copy would have rewritten and demoted the iTunes row.
- Every merge that moves a sibling first writes a sibling-move journal on the main store (`merge:sibling-journal:<ULID>`); a merge that cannot write it is refused. `merge.Result.SiblingJournalID` names it, and link-as-versions returns it as `sibling_journal_id`. `GET /api/v1/merge/sibling-journal` lists the journals and `POST /api/v1/merge/sibling-undo/:journal_id` puts each sibling back in its old group with its exact old primary flag (nil kept distinct), then hands that group a primary. A sibling that has moved to another group since is left there and reported.
- Journaled dedup merges also record the siblings on each loser's undo entry (`AutoMergeJournalEntry.Siblings`), and `UnmergeAuto` restores them through the same restore.

### Fixed

#### Tests: merge version-group checks are now covered

- Added a test for the second half of the soft-deleted-loser guard (`requireReplayedLosersInGroup`): a soft-deleted loser in a live loser's group, not the survivor's, is refused in every input order with nothing written. Removing the check now fails it.
- Added a test for `resolveVersionGroup`'s tie-break (equal live-member counts go to the smallest group ID).
