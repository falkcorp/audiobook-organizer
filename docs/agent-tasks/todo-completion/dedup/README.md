<!-- file: docs/agent-tasks/todo-completion/dedup/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 238540e9-e3ab-4d5f-85a2-27defb66f92e -->
<!-- last-edited: 2026-08-21 -->

# Workstream — dedup (todo-completion)

13 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-040 | MERGE-UNDO | Make UnmergeAuto reverse external-ID reassignment and iTunes write-bac | P1 | L | Opus-class | 1 |
| TASK-041 | L903 | Audit remaining 'we use the wide type because X requires it' justifica | P2 | S | Sonnet-class | 1 |
| TASK-042 | L1350 | Measure whether dedup:duration-abridged (3,573) is over-firing before  | P2 | M | Sonnet-class | 1 |
| TASK-043 | VG-DOUBLE-PRIMARY | Forward fix: demote pre-existing version-group members when a merge re | P1 | M | Opus-class | 3 |
| TASK-044 | L3966 | Add a dry-run parameter to dedup.series-dedup | P1 | S | Sonnet-class | 1 |
| TASK-045 | L4222 | Find the CreateBook path(s) that copy a dangling SeriesID onto newly-c | P1 | L | Opus-class | 2 |
| TASK-046 | L4288 | Apply the unfiltered ref-count guard to the two remaining series delet | P1 | M | Opus-class | 3 |
| TASK-047 | L4304 | Build a dry-run report-only classifier for series that look like they  | P2 | M | Sonnet-class | 1 |
| TASK-048 | L4698 | Route merge.AsExternalIDReassigner through database.AsCapability inste | P1 | S | Sonnet-class | 4 |
| TASK-049 | L4719 | Narrow CollectDuration's tagStore param from dedup.Store to database.B | P2 | S | Haiku-class | 2 |
| TASK-050 | AP-1b | Physically co-locate a Combine survivor's files under RootDir after Co | P1 | M | Opus-class | 5 |
| TASK-051 | L10750 | Acoustic-confirm signal: promote near-dupe title-leak pairs using Whol | P1 | M | Opus-class | 2 |
| TASK-052 | L10750 | Shattered-book reassembly: match fragment file-sets against the refere | P1 | L | Opus-class | 3 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/dedup/auto_resolve.go`: TASK-040, TASK-051 → serialize by wave (TASK-040=w1, TASK-051=w2)
- `internal/dedup/collectors_metadata.go`: TASK-041, TASK-049 → serialize by wave (TASK-041=w1, TASK-049=w2)
- `internal/dedup/series_dedup.go`: TASK-029, TASK-044, TASK-046 → serialize by wave (TASK-029=w2, TASK-044=w1, TASK-046=w3)
- `internal/merge/service.go`: TASK-023, TASK-040, TASK-043, TASK-048, TASK-050 → serialize by wave (TASK-023=w2, TASK-040=w1, TASK-043=w3, TASK-048=w4, TASK-050=w5)
- `internal/scanner/scanner.go`: TASK-045, TASK-110 → serialize by wave (TASK-045=w2, TASK-110=w1)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-040, TASK-041, TASK-042, TASK-044, TASK-047 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-045, TASK-049, TASK-051 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-043, TASK-046, TASK-052 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-048 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 5 | TASK-050 | wave 4 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
