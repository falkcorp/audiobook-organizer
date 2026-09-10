<!-- file: docs/agent-tasks/todo-completion-2026-09/operations/orchestration.md -->
<!-- version: 1.6.0 -->
<!-- guid: eb962f1b-836e-4399-9ba9-07b252eb6d7f -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — operations workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK316[TASK-316 S resumerestart-proceeds-to-an]
      TASK117[TASK-117 S give-prodschedulerstore-an-u]
      TASK118[TASK-118 S delete-internal-operations-m]
      TASK367[TASK-367 M operationdef-permissions-is]
      TASK116[TASK-116 M forward-iscanceled-through-r]
    end
```
