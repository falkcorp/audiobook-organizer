<!-- file: docs/agent-tasks/todo-completion-2026-09/maintenance/orchestration.md -->
<!-- version: 1.7.0 -->
<!-- guid: b45e87e1-76b0-4530-8893-4a878d8b4522 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — maintenance workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK338[TASK-338 S fix-or-unregister-fix-librar]
      TASK360[TASK-360 S orphan-files-hard-delete-fai]
      TASK362[TASK-362 S memdb-lossy-readers-headline]
      TASK068[TASK-068 S build-a-report-only-counter]
      TASK072[TASK-072 M new-maintenance-op-merge-an]
      TASK347[TASK-347 M series-denumber-trashed-gap]
      TASK343[TASK-343 M author-numbering-cleanup-fol]
      TASK071[TASK-071 M build-a-detection-only-repor]
      TASK077[TASK-077 M narrow-the-3-remaining-maint]
      TASK342[TASK-342 L duplicate-placeholder-book-r]
    end
    subgraph Wave2
      TASK075[TASK-075 S extend-purge-empty-authors-r]
      TASK195[TASK-195 S add-a-zero-size-bucket-to-ma]
      TASK220[TASK-220 M journal-every-duplicate-row]
    end
    subgraph Wave3
      TASK066[TASK-066 M wire-a-durable-freshness-sta]
      TASK074[TASK-074 M build-a-report-only-census-o]
    end
    subgraph Wave4
      TASK070[TASK-070 M add-a-user-configurable-acti]
      TASK076[TASK-076 L author-narrator-swap-repair]
    end
    subgraph Wave5
      TASK073[TASK-073 M read-through-audit-of-the-8]
    end
    subgraph Wave6
      TASK219[TASK-219 M add-a-per-book-tsv-report-ar]
    end
```

**Held for the owner (not dispatchable as code):** TASK-353 (HOLD-FOR-OWNER), TASK-356 (HOLD-FOR-OWNER)
