<!-- file: docs/decisions/2026-10-30-repairs-visibility-aggregates.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4f9a7c12-d83e-4b05-a1c6-2e7b90d35f48 -->
<!-- last-edited: 2026-10-30 -->

# Repairs visibility on overview lists, digest aggregates and legacy-run cancel

Three TODO items, one branch (`fix/repairs-visibility-on-overview-and-digests`), one commit each. The owner
asked for no questions, so each open choice below took the recommended option; the alternatives are listed so a
reviewer can reverse a choice.

## Decision log

### TODO-REPAIRS-OPERATIONS-PAUSE-SOURCES

- Chosen: one shared filter, `opaccess.Reader.ReadableOps` (v2 rows) and `ReadableLegacyOps` (the retired v1
  table, whose rows state no params so the fixer comes from the stored result alone). Timeline, pause (all three
  answers) and stale call it.
- Chosen: the pause POST answers filter too. `settings.manage` does not imply every fixer's permissions, so the
  answer to a pause or resume must not name a running `repairs.apply` to a manager who may not use its fixer.
- Chosen: the guard is a walk over the wired router (`repairs_overview_lists_test.go`) with a classification table.
  A new GET route under `/operations` or `/op-defs` that is not in the table fails the build, and a route marked
  as a list needs a driver that seeds hidden Repairs runs. Rejected: an AST check that handlers call
  `ReadableOps`, which proves a call exists and not that the response is clean.
- Not changed: `/op-defs` serves registered definitions, not rows, so it is classified as no-rows.

### TODO-REPAIRS-DIGEST-AGGREGATES

- Chosen: store each operation's share of the digest's totals at compaction time (`DigestDetails.OpAggregates`,
  `NoOpAggregate`, `AggregatesVersion` 1) and subtract the hidden operations' shares per caller. The served copy
  never carries the shares. Rejected: serving no aggregates to anyone below "every fixer permission" for every
  digest, which would blank the counts on every day for nearly every user for a leak that touches a few runs.
- Chosen: an operation's verdict for its aggregate is the one its rows get. A known record that denies the caller
  subtracts the whole share; an unknown or unreadable record, or an unwired handler, subtracts only the part
  from rows that look like the Repairs lane (`RepairsLike`: type or `def:` tag). The database package cannot
  import `repairs`, so `database.EntryLooksRepairs` keeps its own copy of the two definition ids, and a handlers
  test pins it to `looksRepairs` over a table of entry shapes.
- Chosen: a digest with no shares (`aggregates_version` 0) is served its totals only when nothing can be hidden
  from the caller: they hold every permission, or the digest's items are every row of the day and none is hidden.
  Otherwise the totals are removed and `aggregates_withheld` is set. A digest is "complete" only when
  `original_count == len(items)` and not truncated; nearly every real digest exceeds the 500-item cap, so old
  digests are in practice withheld from everyone below admin. Rejected: estimating the hidden share from the
  sampled items, which gives a wrong number that looks exact.
- Chosen: a day that names more than 1000 operations stores no shares (falls back to version 0), and merging
  compaction rows into a stored version 0 digest keeps it version 0. Rejected: an unbounded map on the digest row.
- Chosen: `RecompactDigests` rebuilds a version 1 digest's totals and shares from its items only when the items
  are every row (`original_count == len(items)`), and leaves a sampled one alone, so the recompaction op cannot
  put the totals and the shares out of step.
- Chosen (security review): a row is filed under an operation by `EntryOperationIDs`, the membership the reader
  uses to hide a row, not by `OperationID` alone. A Summarize row names its operations in `op:` tags and is
  hidden when any of them is, so one that names several makes the day unattributable: no shares are stored
  (`aggregates_version` 0) and the digest's totals are withheld from a restricted caller. One that names a single
  operation is filed under it. Rejected: a per-row share keyed by the sampled operation ids, which needs a
  second per-operation structure and a rule for the rows a Summarize pass already folded away.
- Chosen (security review): a digest with no details, or details with no date, fails closed. A caller who does
  not hold every permission gets a summary with no total (the day comes from the row's timestamp when the details
  carry none) and `aggregates_withheld`. The stored summary states the unredacted total, so it is never kept.
- Chosen (security review): recompaction re-derives a legacy item's type and tags from its message, which cannot
  say the row belonged to the Repairs lane or to which operations. It keeps a `repairs.*` type and the item's
  existing `def:` and `op:` tags beside the derived ones. An item of a pruned operation therefore still looks
  like a Repairs row, and a digest rebuilt from items that carry `op:` tags drops its shares rather than guess
  (items keep no Source, and only Summarize rows carry `op:` tags).
- Chosen (security review, second pass): digest items get their Source back before they are judged. An item with
  an `op:` tag is the Summarize row it came from (`database.DigestItemSource`, shared by the writer's
  attribution and the reader), so an item naming a hidden run in a tag is dropped like the row would be and
  counts as a hidden item for the legacy totals rule. Rejected: a second copy of the rule in the handler.
- Known and accepted: an operation that names no fixer is hidden from everyone, but a legacy digest served to a
  caller who holds every permission still counts its rows. Rows hidden only because their text names other users
  (`activity.NamesOthersUserData`) are not subtracted from aggregates: that judgement reads each row's text,
  which an aggregate does not keep. It belongs to the other-users rule, not the Repairs rule.
- Cost: one `GetOperationV2` per distinct operation id per digest per request, cached for the request (the same
  shape as the existing item redaction).
- Not examined: the live batcher's `original_count` (`internal/activity/batcher.go`, shown by
  `BatchActivityEntry.tsx`) is a different count, over rows still in the feed, and was not checked for Repairs
  leakage.
