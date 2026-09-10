<!-- file: docs/agent-tasks/todo-completion-2026-09/search/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: fd0e34c8-9277-482a-8f5f-a2b8ab577fe7 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — search workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK126[TASK-126 surface-to-the-user-when-all]
    end
    subgraph Wave2_M
      TASK125[TASK-125 index-track-names-on-bookdoc]
    end
```
