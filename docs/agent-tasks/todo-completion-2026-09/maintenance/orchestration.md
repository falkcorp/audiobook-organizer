<!-- file: docs/agent-tasks/todo-completion-2026-09/maintenance/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: b45e87e1-76b0-4530-8893-4a878d8b4522 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — maintenance workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK068[TASK-068 build-a-report-only-counter]
      TASK075[TASK-075 extend-purge-empty-authors-r]
      TASK195[TASK-195 add-a-zero-size-bucket-to-ma]
      TASK337[TASK-337 every-rescan-reverts-library]
      TASK356[TASK-356 orphan-files-hard-delete-fai]
      TASK358[TASK-358 memdb-lossy-readers-headline]
    end
    subgraph Wave2_M
      TASK066[TASK-066 wire-a-durable-freshness-sta]
      TASK070[TASK-070 add-a-user-configurable-acti]
      TASK071[TASK-071 build-a-detection-only-repor]
      TASK072[TASK-072 new-maintenance-op-merge-an]
      TASK073[TASK-073 read-through-audit-of-the-8]
      TASK074[TASK-074 build-a-report-only-census-o]
      TASK077[TASK-077 narrow-the-3-remaining-maint]
      TASK219[TASK-219 add-a-per-book-tsv-report-ar]
      TASK220[TASK-220 journal-every-duplicate-row]
      TASK341[TASK-341 author-numbering-cleanup-fol]
      TASK349[TASK-349 data-repair]
      TASK352[TASK-352 createauthor-is-check-then-c]
    end
    subgraph Wave3_L
      TASK076[TASK-076 author-narrator-swap-repair]
      TASK340[TASK-340 activity-sqlite-backend-foll]
    end
```
