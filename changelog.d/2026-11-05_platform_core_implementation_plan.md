### Added

#### Platform core implementation plan and task briefs (`docs/plans/platform-core/`, `docs/agent-tasks/platform-core/`)

The implementation plan for the revised platform-core spec: 134 tasks (128 agent PRs, each with a brief, and 6
owner checkpoints) in 46 file-disjoint waves across R0 (hotfixes), R1 (core runtime), R2 (dark foundations F2, then
the flips F3–F8 and the cutover release) and R3 (cleanup, the upgrade floor and the shell). Each brief gives the
task's tier, dependencies, file list, spec sections, START HERE re-verification, tests written first, STOP
conditions and acceptance commands. `docs/plans/platform-core/task-summary.md` lists every task in plain language,
and `docs/plans/platform-core/tools/` holds the machine-readable inventory and the wave checker. Three plan review
rounds (blockers 4 → 1 → 0) and an independent advisor review are recorded in
`docs/plans/platform-core/reviews/plan-rounds-1-3.md`. Planning only: no production code changed.
