### Removed

- `maintenance.series-prune` and `maintenance.series-normalize` are merged into `dedup.series-prune` and `dedup.series-normalize`. The old IDs keep resolving as aliases, so stored rows and schedules still work, and they now run under the survivors' stricter `library.edit_metadata` gate, with cancellation and longer timeouts. The series-list cache is now invalidated after a series normalize that renames anything.
