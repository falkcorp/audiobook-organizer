<!-- file: docs/agent-tasks/todo-completion-2026-09/itunes/orchestration.md -->
<!-- version: 1.7.0 -->
<!-- guid: f267adb6-c908-49e8-b44a-dec3a36a9503 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — itunes workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK063[TASK-063 S internal-itunes-backfill-go]
      TASK185[TASK-185 S report-the-itunes-listened-i]
      TASK340[TASK-340 M internal-itunes-service-writ]
      TASK064[TASK-064 M add-a-part-disc-chapter-trac]
      TASK065[TASK-065 M p2-relocate-only-sync-cycle]
    end
    subgraph Wave2
      TASK323[TASK-323 S external-id-backfill-s-done]
      TASK184[TASK-184 S measure-itunes-xml-track-per]
    end
    subgraph Wave3
      TASK062[TASK-062 M internal-itunes-backfill-go]
    end
```

**Held for the owner (not dispatchable as code):** TASK-375 (HOLD-FOR-OWNER)
