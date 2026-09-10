<!-- file: docs/agent-tasks/todo-completion-2026-09/missing-file-lane/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: a5a75440-5b9c-43c5-89f5-0137346abaf5 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — missing-file-lane workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK095[TASK-095 instrument-sort-by-usage-to]
      TASK098[TASK-098 echo-which-filters-the-serve]
    end
    subgraph Wave2_M
      TASK103[TASK-103 build-a-report-only-op-categ]
      TASK108[TASK-108 add-the-review-rating-half-o]
      TASK111[TASK-111 build-the-pre-apply-snapshot]
      TASK113[TASK-113 missing-input-triggering-enq]
      TASK201[TASK-201 wire-per-file-intro-classifi]
    end
    subgraph Wave3_L
      TASK096[TASK-096 require-every-mutating-opera]
      TASK102[TASK-102 typescript-6-0-3-7-0-2-migra]
      TASK106[TASK-106 import-found-playlist-files]
      TASK109[TASK-109 parse-deluge-torrent-release]
      TASK110[TASK-110 audit-book-file-grouping-aga]
      TASK112[TASK-112 build-the-first-aid-orchestr]
      TASK114[TASK-114 never-delete-re-associate-co]
      TASK200[TASK-200 build-the-tiered-per-file-in]
    end
```
