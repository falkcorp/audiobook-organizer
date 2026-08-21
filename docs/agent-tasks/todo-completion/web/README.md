<!-- file: docs/agent-tasks/todo-completion/web/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1330f5b7-6165-44d4-8331-cb149e93b730 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — web (todo-completion)

20 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-161 | 2026-08-20-dual-path-settings-panel.md#1 | Add a Settings panel section to edit path_aliases | P2 | M | Sonnet-class | 1 |
| TASK-162 | 2026-08-20-dual-path-settings-panel.md#3 | Add and use a test-reset hook for the module-scope path-alias/path-var | P2 | S | Haiku-class | 1 |
| TASK-163 | SEC-9 | Move OpenAI API key validation server-side (currently sent from the br | P2 | M | Sonnet-class | 1 |
| TASK-164 | L1350 | Strip dedup:* and metadata:source:* namespaces from Browse by Tag widg | P2 | S | Sonnet-class | 1 |
| TASK-165 | L1350 | Reformat metadata:* tags in Browse by Tag: strip prefix, 'key: value'  | P2 | S | Sonnet-class | 2 |
| TASK-166 | L1727 | Harden MuiMenu against the documented React setState-drop defect (exit | P2 | S | Sonnet-class | 1 |
| TASK-167 | L1744 | Find the mechanism behind the intermittent webkit-only flake in batch- | P2 | M | Opus-class | 1 |
| TASK-168 | REVIEW-COMBINE-FIRST | Let the owner combine/merge duplicate books from the metadata chooser, | P1 | L | Opus-class | 7 |
| TASK-169 | REVIEW-PREVIEW | Play the first ~2 minutes of part 1's audio directly from the review m | P2 | M | Sonnet-class | 1 |
| TASK-170 | L2486 | Review the 17 apiFetch-callers' catch handlers for session-expiry mess | P1 | L | Opus-class | 8 |
| TASK-171 | L3156 | Make the book-detail page's Author field(s) link to a library view fil | P2 | M | Sonnet-class | 3 |
| TASK-172 | L3161 | Make the book-detail page's Series field link to a library view filter | P2 | M | Sonnet-class | 4 |
| TASK-173 | L3164 | Make Narrator, Publisher, Genre, and Release Year fields link to filte | P2 | M | Sonnet-class | 5 |
| TASK-174 | L3168 | Link version_group_id to a filtered library view (now unblocked — the  | P2 | S | Sonnet-class | 6 |
| TASK-175 | L4960 | Retarget dedup-operations.spec.ts and dedup.spec.ts resolve-production | P2 | S | Sonnet-class | 1 |
| TASK-176 | L4960 | Retarget diagnostics.spec.ts AI-submit and export status mocks to v2 | P2 | S | Sonnet-class | 1 |
| TASK-177 | L10586 | Add a frontend test asserting the book-sig coverage % badge renders | P2 | S | Haiku-class | 1 |
| TASK-178 | L10660 | Add resizable/sortable columns to the acoustic dedup candidates table | P2 | M | Sonnet-class | 1 |
| TASK-179 | L10660 | Add resizable/sortable columns to the Activity Log table | P2 | L | Sonnet-class | 1 |
| TASK-180 | L10660 | Add resizable/sortable columns to the split-book dedup candidates tabl | P2 | M | Sonnet-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci && npm --prefix web run lint && npm --prefix web test ; npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `web/src/components/bookdetail/BookDetailInfoTab.tsx`: TASK-171, TASK-172, TASK-173 → serialize by wave (TASK-171=w3, TASK-172=w4, TASK-173=w5)
- `web/src/components/bookdetail/BookDetailVersionGroup.tsx`: TASK-098, TASK-174 → serialize by wave (TASK-098=w1, TASK-174=w6)
- `web/src/components/dedup/DedupAcousticTab.tsx`: TASK-170, TASK-178 → serialize by wave (TASK-170=w8, TASK-178=w1)
- `web/src/components/library/TagCloud.tsx`: TASK-164, TASK-165 → serialize by wave (TASK-164=w1, TASK-165=w2)
- `web/src/hooks/useLibraryQuery.ts`: TASK-171, TASK-172 → serialize by wave (TASK-171=w3, TASK-172=w4)
- `web/src/pages/ActivityLog.tsx`: TASK-073, TASK-179 → serialize by wave (TASK-073=w5, TASK-179=w1)
- `web/src/pages/BookDetail.tsx`: TASK-037, TASK-104, TASK-170 → serialize by wave (TASK-037=w6, TASK-104=w1, TASK-170=w8)
- `web/src/pages/Library.tsx`: TASK-096, TASK-164, TASK-168, TASK-170, TASK-171, TASK-172, TASK-173, TASK-174 → serialize by wave (TASK-096=w2, TASK-164=w1, TASK-168=w7, TASK-170=w8, TASK-171=w3, TASK-172=w4, TASK-173=w5, TASK-174=w6)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-161, TASK-162, TASK-163, TASK-164, TASK-166, TASK-167, TASK-169, TASK-175, TASK-176, TASK-177, TASK-178, TASK-179, TASK-180 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-165 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-171 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-172 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 5 | TASK-173 | wave 4 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 6 | TASK-174 | wave 5 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 7 | TASK-168 | wave 6 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 8 | TASK-170 | wave 7 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
