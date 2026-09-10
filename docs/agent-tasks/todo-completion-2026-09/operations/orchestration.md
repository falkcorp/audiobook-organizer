<!-- file: docs/agent-tasks/todo-completion-2026-09/operations/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: eb962f1b-836e-4399-9ba9-07b252eb6d7f -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — operations workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK117[TASK-117 give-prodschedulerstore-an-u]
      TASK118[TASK-118 delete-internal-operations-m]
      TASK316[TASK-316 resumerestart-proceeds-to-an]
    end
    subgraph Wave2_M
      TASK116[TASK-116 forward-iscanceled-through-r]
      TASK362[TASK-362 update-2026-08-16-one-of-the]
    end
```
