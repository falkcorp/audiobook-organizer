### Fixed

- Tasks cutover step (M4, not yet registered in any build): a v1 operation that also has an orphan v2 operation row and no v3 record is now always set aside whole, with both rows and the operation's history. The step runs its shards in parallel. Before this fix, when the v2 row's shard committed first, the v2 row was quarantined on its own and the v1 operation was then imported as a run, so the outcome depended on scheduling. The v1 operation row is now the only row that decides such an ID's outcome, and the v2 row is read and quarantined with it.
