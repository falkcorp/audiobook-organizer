### Changed

#### Merge: a loser's version siblings now follow it into the survivor's group

- `merge.Service.MergeBooks` (every merge path: link-as-versions, `dedup.book-merge`, the dedup review and cluster endpoints, diagnostics `merge_versions`, maintenance `BookMerger`) now unites each live loser's version group into the merge's group. The loser's other live members move in as non-primary versions and stay live; only the named losers are soft-deleted. The group they leave has no live member, so no primary is elected for it. Owner decision 2026-10-05.
- The siblings are read and moved under the version-group locks the merge already takes, each write re-checking membership, so a concurrent change aborts the merge. If the membership writes fail part-way, each left group's primary is handed on as before.
- `merge.Result.MovedSiblings` lists each moved sibling with its old group and its exact old primary flag (nil kept distinct). Journaled merges record them on the loser's undo entry (`AutoMergeJournalEntry.Siblings`), and `UnmergeAuto` puts each sibling back in its old group with its old flag; a sibling that has moved to another group since is left there and reported.
- Merges that write no undo journal (`applyBookMergeReroute`, link-as-versions, diagnostics `merge_versions`, maintenance `BookMerger`) move siblings with no undo record.

### Fixed

#### Tests: merge version-group checks are now covered

- Added a test for the second half of the soft-deleted-loser guard (`requireReplayedLosersInGroup`): a soft-deleted loser in a live loser's group, not the survivor's, is refused in every input order with nothing written. Removing the check now fails it.
- Added a test for `resolveVersionGroup`'s tie-break (equal live-member counts go to the smallest group ID).
