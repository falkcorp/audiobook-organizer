<!-- file: changelog.d/20260820_204500_reap_report_outcomes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e2c48b1-05af-4d39-9c6e-3b81d5a027f4 -->
<!-- last-edited: 2026-08-20 -->

### Fixed

#### The reaper's report now records what happened, not what was planned

Found by reading a real production report after the first apply: all 3,354
deleted rows carried the reason `would reap`. The decision is recorded during
the plan phase and the file is written after the apply, so the wording never
caught up with the outcome. On a dry run that is accurate. On an apply it tells
someone auditing the deletion that nothing was deleted.

The sharper problem was underneath it. A row that was SPARED — because the book
came back on the delete-time re-check, or because the delete itself failed — was
written to the report identically to one that was destroyed. Those counts existed
only in the summary line, so the file could say which rows were *candidates* but
not which ones actually went. For an op whose report is the only record of what
it destroyed, that is the one question it has to answer.

Each attempted row is now restamped with its real outcome: `reaped` / `DELETED`,
`spared-revived`, `delete-error`, or `recheck-error`, with the underlying error
text where there was one. Rows the apply never attempted keep the plan's wording,
and a dry run is unchanged — restamping those would claim deletions that never
happened. The log line's samples are restamped too, so they cannot contradict the
file they point at.
