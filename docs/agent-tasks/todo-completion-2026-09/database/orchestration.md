<!-- file: docs/agent-tasks/todo-completion-2026-09/database/orchestration.md -->
<!-- version: 1.6.0 -->
<!-- guid: f4a25791-ec2c-46ba-a65a-a772959df9da -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — database workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK302[TASK-302 S purge-empty-authors-delete-g]
      TASK354[TASK-354 S two-rows-with-the-same-filep]
      TASK315[TASK-315 S the-real-pebbledb-corrupted]
      TASK326[TASK-326 S dual-write-activity-migratio]
      TASK035[TASK-035 S add-deletenarrator-to-the-st]
      TASK177[TASK-177 S add-a-per-test-deadline-cont]
      TASK334[TASK-334 S digest-compaction-swallows-t]
      TASK359[TASK-359 M series-merge-unguarded-denom]
      TASK363[TASK-363 M author-file-safety-purge-emp]
      TASK179[TASK-179 M database-store-40-build-the]
    end
    subgraph Wave2
      TASK038[TASK-038 S filter-system-sourced-tags-o]
      TASK331[TASK-331 S deletebook-never-deletes-the]
      TASK305[TASK-305 M migration-effect-migration-r]
    end
    subgraph Wave3
      TASK322[TASK-322 M no-persisted-author-books-se]
    end
    subgraph Wave4
      TASK039[TASK-039 M add-transcribe-status-to-the]
    end
    subgraph Wave5
      TASK361[TASK-361 L author-membership-unguarded]
    end
    subgraph Wave6
      TASK023[TASK-023 L investigate-then-evict-dirty]
    end
    subgraph Wave7
      TASK037[TASK-037 L omnibus-anthology-book-type]
    end
```
