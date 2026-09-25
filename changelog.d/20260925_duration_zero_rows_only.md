### Added

- `maintenance.duration-backfill` gains `zero_rows_only` (or `zeroRowsOnly`): it examines only books ABS lists (primary + organized), ignores the verified stamp, writes only file rows stored as 0, and skips books whose rows span several folders, so duplicate rows are never summed into an inflated total. This is the repair for books that read "0" in ABS.
