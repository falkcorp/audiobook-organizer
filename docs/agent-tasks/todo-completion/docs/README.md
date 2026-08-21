<!-- file: docs/agent-tasks/todo-completion/docs/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3061dcb1-d6f0-4958-99c2-9244ce96d2f4 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — docs (todo-completion)

12 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-055 | L101 | Record the docs/system vs top-level architecture classification decisi | P2 | S | Sonnet-class | 1 |
| TASK-056 | L101 | Write file-header for the 35 current live docs still missing one | P2 | M | Haiku-class | 1 |
| TASK-057 | L296 | Delete the 34 group-relative duplicate paths from docs/api/openapi.jso | P2 | S | Sonnet-class | 1 |
| TASK-058 | L296 | Triage the 16 removed POST /maintenance/* paths in openapi.json — dele | P2 | M | Sonnet-class | 1 |
| TASK-059 | L296 | Delete the /torrents group-relative fragment from openapi.json | P2 | S | Haiku-class | 1 |
| TASK-060 | L497 | Re-verify docs/reference/abs-target-client-contract.md §11's 'safe to  | P2 | S | Sonnet-class | 1 |
| TASK-061 | L1852 | Document the todo.d fragment race (assembled between filing and finish | P2 | S | Haiku-class | 1 |
| TASK-062 | L4463 | Consolidate the August executive-summary roundup through 2026-08-19 | P2 | L | Sonnet-class | 1 |
| TASK-063 | ABS-SYNC-Phase8 | Phase 8 — write the ABS topology, runbook, and migration guide (Cloudf | P1 | M | Opus-class | 1 |
| TASK-064 | L10635 | Update execution-manifest doc to reflect the now-settled human gates | P2 | S | Haiku-class | 1 |
| TASK-065 | L10706 | Close out the 2026-05-01 re-audit block (TEST-2/DEP-1/DEAD-1/CTX-4/LOG | P2 | S | Haiku-class | 1 |
| TASK-066 | T13 | Docs truth-up with measured sandbox/prod dedup numbers | P2 | S | Haiku-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- No same-file collisions inside this workstream.

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-055, TASK-056, TASK-057, TASK-058, TASK-059, TASK-060, TASK-061, TASK-062, TASK-063, TASK-064, TASK-065, TASK-066 | none | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
