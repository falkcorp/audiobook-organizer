<!-- file: docs/agent-tasks/todo-completion-2026-09/config/orchestration.md -->
<!-- version: 1.7.0 -->
<!-- guid: 59d518bb-2621-4562-b8d8-8603918491fd -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — config workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK020[TASK-020 S delete-the-fully-inert-enabl]
    end
    subgraph Wave2
      TASK348[TASK-348 M mask-the-remaining-secrets-r]
    end
    subgraph Wave3
      TASK016[TASK-016 M rename-write-back-metadata-c]
    end
```
