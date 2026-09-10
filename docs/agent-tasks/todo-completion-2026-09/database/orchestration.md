<!-- file: docs/agent-tasks/todo-completion-2026-09/database/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: f4a25791-ec2c-46ba-a65a-a772959df9da -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — database workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK035[TASK-035 add-deletenarrator-to-the-st]
      TASK038[TASK-038 filter-system-sourced-tags-o]
      TASK177[TASK-177 add-a-per-test-deadline-cont]
      TASK302[TASK-302 purge-empty-authors-delete-g]
      TASK315[TASK-315 the-real-pebbledb-corrupted]
      TASK326[TASK-326 dual-write-activity-migratio]
      TASK331[TASK-331 deletebook-never-deletes-the]
      TASK334[TASK-334 digest-compaction-swallows-t]
      TASK350[TASK-350 two-rows-with-the-same-filep]
    end
    subgraph Wave2_M
      TASK039[TASK-039 add-transcribe-status-to-the]
      TASK179[TASK-179 database-store-40-build-the]
      TASK305[TASK-305 migration-effect-migration-r]
      TASK322[TASK-322 no-persisted-author-books-se]
      TASK355[TASK-355 series-merge-unguarded-denom]
      TASK359[TASK-359 author-file-safety]
    end
    subgraph Wave3_L
      TASK023[TASK-023 investigate-then-evict-dirty]
      TASK037[TASK-037 omnibus-anthology-book-type]
      TASK357[TASK-357 author-membership-unguarded]
    end
```
