### Added

- New Prometheus counter `audiobook_organizer_sort_by_requested_total{field}` counts
  library list requests by the `sort_by` they asked for (TASK-095). Each field the
  server can sort by keeps its own label. A request with no `sort_by` is counted as
  `default`, and anything unrecognised is counted as `other`, so a client cannot
  create new series. A week of this data answers which fields are worth adding to
  `enabled_sort_indexes`, where each field costs about 146 MB of memdb heap.
