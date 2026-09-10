<!-- file: docs/agent-tasks/todo-completion-2026-09/docs/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: b02ad3b6-f15e-49d7-8605-ed6819387476 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — docs workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK182[TASK-182 record-the-docs-system-vs-to]
    end
    subgraph Wave2_M
      TASK057[TASK-057 phase-8-write-the-abs-topolo]
    end
```
