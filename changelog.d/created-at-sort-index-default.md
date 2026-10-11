### Changed

- The `created_at` memdb sort index is now part of the default `enabled_sort_indexes` (now `["year", "created_at"]`), so ABS "Recently Added" and the Library tab's sort-by-date-added walk an ordered index instead of materialising and sorting the whole visible set on every request (about 30-39 ms and 78 MB allocated per request before, about 7 µs after). The cost is roughly 19 MB of memdb heap and slightly slower inserts. Ordering is unchanged, including rows with no creation time. A config file or environment override of `enabled_sort_indexes` still replaces the default.
