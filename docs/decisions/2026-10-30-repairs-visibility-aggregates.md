<!-- file: docs/decisions/2026-10-30-repairs-visibility-aggregates.md -->
<!-- version: 1.0.0 -->
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
