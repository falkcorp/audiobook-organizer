<!-- file: docs/agent-tasks/todo-completion-2026-09/config/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 59d518bb-2621-4562-b8d8-8603918491fd -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — config workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK020[TASK-020 delete-the-fully-inert-enabl]
    end
    subgraph Wave2_M
      TASK016[TASK-016 rename-write-back-metadata-c]
      TASK344[TASK-344 mask-the-remaining-secrets-r]
    end
```
