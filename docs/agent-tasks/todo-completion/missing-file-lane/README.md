<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2a63555b-9f21-45cf-9745-80a34e12eedf -->
<!-- last-edited: 2026-08-21 -->

# Workstream — missing-file-lane (todo-completion)

26 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-098 | L5494 | Log a warning when GetAllSeriesBookCounts() itself errors in LibrarySe | P2 | S | Haiku-class | 3 |
| TASK-099 | L5722 | Give Change Log row 'Compare snapshot' keyboard/a11y affordance | P2 | S | Sonnet-class | 1 |
| TASK-100 | L5736 | Remove dead expanded state in TagComparison | P2 | S | Haiku-class | 1 |
| TASK-101 | L5742 | Delete the unreachable Bulk Fetch Metadata dialog and its handler | P2 | M | Sonnet-class | 2 |
| TASK-102 | L5758 | Audit remaining setupMockApi startsWith() catch-alls for shadowed spec | P2 | S | Haiku-class | 1 |
| TASK-103 | L6252 | Restore version-group count and current-version marker on Book Detail | P2 | S | Sonnet-class | 1 |
| TASK-104 | L6701 | Instrument sort_by usage to inform the enabled_sort_indexes decision | P2 | S | Haiku-class | 1 |
| TASK-105 | L7435 | Require every mutating operation to declare and enforce dry_run suppor | P1 | L | Opus-class | 1 |
| TASK-106 | TODO-MUI-3 | Remove the now-redundant react-is override from web/package.json | P2 | S | Haiku-class | 1 |
| TASK-107 | L7736 | Echo which filters the server actually applied in the /audiobooks list | P2 | S | Sonnet-class | 2 |
| TASK-108 | L8044 | Fail/warn CI when the RC ordinal for a version hits 10 | P2 | S | Sonnet-class | 2 |
| TASK-109 | L8177 | Validate the two unvalidated client-side navigation sinks (Login.tsx f | P2 | S | Sonnet-class | 1 |
| TASK-110 | L8245 | Pin a regression test: the regroup recommender must not default to dup | P2 | S | Sonnet-class | 1 |
| TASK-111 | L8273 | TypeScript 6.0.3 → 7.0.2 migration (the one remaining piece of the fro | P2 | L | Opus-class | 2 |
| TASK-112 | L8433 | Build a report-only op categorizing the transcribe_status vs IntroTran | P2 | M | Sonnet-class | 1 |
| TASK-113 | L8551 | Build the version-group acoustic audit op (tier 2 of First Aid) | P1 | L | Opus-class | 1 |
| TASK-114 | L8611 | Build chapters backfill from a near-exact-acoustic-match duplicate (or | P1 | L | Opus-class | 3 |
| TASK-115 | L8646 | Import found playlist files (.m3u/.m3u8/.pls/.cue/.xspf) during scan,  | P2 | L | Opus-class | 2 |
| TASK-116 | L8646 | Export a playlist back to .m3u | P2 | S | Sonnet-class | 1 |
| TASK-117 | L8675 | Add the review/rating half of app-to-server reading-state sync (readin | P2 | M | Sonnet-class | 1 |
| TASK-118 | L8707 | Parse Deluge torrent release names into structured candidate metadata  | P2 | L | Opus-class | 1 |
| TASK-119 | L8738 | Audit book/file grouping against Deluge torrent file-list membership ( | P2 | L | Opus-class | 2 |
| TASK-120 | L8837 | Build the pre-apply snapshot tool for the 138 pending multidisc holds | P1 | M | Opus-class | 1 |
| TASK-121 | L8890 | Build the First Aid orchestrator + frontend trigger button (dry-run by | P1 | L | Opus-class | 2 |
| TASK-122 | L8890 | Missing-input triggering: enqueue the producer op when a waiting_deps  | P2 | M | Opus-class | 1 |
| TASK-123 | L8943 | Never delete — re-associate: combine debris books into a template matc | P1 | L | Opus-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only' ; make ci ; make ci && npm --prefix web run lint && npm --prefix web test ; npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `.github/workflows/prerelease.yml`: TASK-009, TASK-108 → serialize by wave (TASK-009=w1, TASK-108=w2)
- `internal/operations/registry/registry.go`: TASK-105, TASK-124 → serialize by wave (TASK-105=w1, TASK-124=w2)
- `internal/scanner/scanner.go`: TASK-047, TASK-115 → serialize by wave (TASK-047=w1, TASK-115=w2)
- `internal/server/handlers/abs/browse.go`: TASK-087, TASK-088, TASK-098, TASK-154 → serialize by wave (TASK-087=w1, TASK-088=w2, TASK-098=w3, TASK-154=w4)
- `internal/server/handlers/audiobooks/handler.go`: TASK-006, TASK-039, TASK-104, TASK-107 → serialize by wave (TASK-006=w4, TASK-039=w3, TASK-104=w1, TASK-107=w2)
- `web/package-lock.json`: TASK-096, TASK-101 → serialize by wave (TASK-096=w1, TASK-101=w2)
- `web/package.json`: TASK-106, TASK-111 → serialize by wave (TASK-106=w1, TASK-111=w2)
- `web/src/components/bookdetail/BookDetailVersionGroup.tsx`: TASK-103, TASK-179 → serialize by wave (TASK-103=w1, TASK-179=w8)
- `web/src/pages/BookDetail.tsx`: TASK-039, TASK-109, TASK-175 → serialize by wave (TASK-039=w3, TASK-109=w1, TASK-175=w5)
- `web/src/pages/Library.tsx`: TASK-101, TASK-169, TASK-173, TASK-175, TASK-176, TASK-177, TASK-178, TASK-179 → serialize by wave (TASK-101=w2, TASK-169=w1, TASK-173=w4, TASK-175=w5, TASK-176=w3, TASK-177=w6, TASK-178=w7, TASK-179=w8)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-099, TASK-100, TASK-102, TASK-103, TASK-104, TASK-105, TASK-106, TASK-109, TASK-110, TASK-112, TASK-113, TASK-116, TASK-117, TASK-118, TASK-120, TASK-122, TASK-123 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-101, TASK-107, TASK-108, TASK-111, TASK-115, TASK-119, TASK-121 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-098, TASK-114 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
