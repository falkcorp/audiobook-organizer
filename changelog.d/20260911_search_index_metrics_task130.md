### Added

#### `search_index_dropped_total` and `search_index_dirty_backlog` on `/metrics` (TASK-130)

The search index's drop counter lived only in a process atomic and a WARN line, so the 56,537 drops seen on prod were findable only by grepping journald. `audiobook_organizer_search_index_dropped_total` now increments at the drop site, and `audiobook_organizer_search_index_dirty_backlog` reports the durable dirty-set size at every reconcile tick, so a backlog that is not draining is visible on a dashboard without knowing to look for it.
