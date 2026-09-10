<!-- file: docs/agent-tasks/todo-completion-2026-09/missing-file-lane/orchestration.md -->
<!-- version: 1.6.0 -->
<!-- guid: a5a75440-5b9c-43c5-89f5-0137346abaf5 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — missing-file-lane workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK095[TASK-095 S instrument-sort-by-usage-to]
      TASK108[TASK-108 M add-the-review-rating-half-o]
      TASK113[TASK-113 M missing-input-triggering-enq]
      TASK201[TASK-201 M wire-per-file-intro-classifi]
      TASK103[TASK-103 M build-a-report-only-op-categ]
      TASK111[TASK-111 M build-the-pre-apply-snapshot]
      TASK109[TASK-109 L parse-deluge-torrent-release]
      TASK106[TASK-106 L import-found-playlist-files]
      TASK112[TASK-112 L build-the-first-aid-orchestr]
      TASK102[TASK-102 L typescript-6-0-3-7-0-2-migra]
    end
    subgraph Wave2
      TASK098[TASK-098 S echo-which-filters-the-serve]
      TASK096[TASK-096 L require-every-mutating-opera]
      TASK110[TASK-110 L audit-book-file-grouping-aga]
    end
    subgraph Wave3
      TASK114[TASK-114 L never-delete-re-associate-co]
      TASK200[TASK-200 L build-the-tiered-per-file-in]
    end
```
