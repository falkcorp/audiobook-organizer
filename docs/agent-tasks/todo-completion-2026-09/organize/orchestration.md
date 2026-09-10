<!-- file: docs/agent-tasks/todo-completion-2026-09/organize/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: cb675bf1-028e-4703-813a-e9f497898418 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — organize workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK122[TASK-122 add-an-edition-suffix-folder]
      TASK203[TASK-203 add-a-detection-only-counter]
      TASK303[TASK-303 single-file-organize-no-op-p]
    end
    subgraph Wave2_M
      TASK121[TASK-121 make-resolveorganizedfilepat]
    end
```
