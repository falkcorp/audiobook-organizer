<!-- file: docs/agent-tasks/todo-completion/ci-tooling/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 45636265-9c32-4126-92c9-b814660428f0 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — ci-tooling (todo-completion)

10 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-005 | L46 | Add a scheduled detect-only backstop workflow for auto-revert.yml | P2 | M | Sonnet-class | 1 |
| TASK-006 | L50 | Wire scripts/test_check_memory_leaks.py into a CI job (repo-guards) | P2 | S | Haiku-class | 1 |
| TASK-007 | L921 | Bump the ghcommon reusable-workflow pins in at least two PRs, low-cons | P2 | M | Sonnet-class | 1 |
| TASK-008 | L2568 | Teach the ABS fixture-capture harness to record request headers | P2 | S | Sonnet-class | 1 |
| TASK-009 | SEC-CODEQL-BACKLOG | Add top-level `permissions:` blocks to the 3 workflows flagged by acti | P2 | S | Haiku-class | 2 |
| TASK-010 | SEC-8 | Pin SHA256 checksums for Dockerfile-fetched utfcpp/taglib tarballs | P2 | S | Haiku-class | 1 |
| TASK-011 | L4312 | scripts/setup-prometheus-auth.py does NOT share the server-side shell  | P2 | S | Haiku-class | 1 |
| TASK-012 | L4844 | Build a report-only scan for book rows that may have been spuriously c | P2 | M | Sonnet-class | 1 |
| TASK-013 | REPO-SIZE-1 | Remove committed mtls-bridge build artifact and gitignore it | P2 | S | Haiku-class | 1 |
| TASK-014 | REPO-SIZE-1 | Stop committing series_dedup.py's generated dump/fix cache files | P2 | S | Haiku-class | 2 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `.github/workflows/ci.yml`: TASK-006, TASK-021 → serialize by wave (TASK-006=w1, TASK-021=w2)
- `.github/workflows/hard-burndown.yml`: TASK-007, TASK-009 → serialize by wave (TASK-007=w1, TASK-009=w2)
- `.github/workflows/nightly-burndown.yml`: TASK-007, TASK-009 → serialize by wave (TASK-007=w1, TASK-009=w2)
- `.github/workflows/prerelease.yml`: TASK-007, TASK-103 → serialize by wave (TASK-007=w1, TASK-103=w2)
- `.github/workflows/triage-poll.yml`: TASK-007, TASK-009 → serialize by wave (TASK-007=w1, TASK-009=w2)
- `.gitignore`: TASK-013, TASK-014 → serialize by wave (TASK-013=w1, TASK-014=w2)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-005, TASK-006, TASK-007, TASK-008, TASK-010, TASK-011, TASK-012, TASK-013 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-009, TASK-014 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
