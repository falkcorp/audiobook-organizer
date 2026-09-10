<!-- file: docs/agent-tasks/todo-completion-2026-09/activity/orchestration.md -->
<!-- version: 1.6.0 -->
<!-- guid: 08a1797c-8557-43ef-9418-661f5f38cc45 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — activity workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK333[TASK-333 S isbatchable-s-tier-gate-sile]
    end
```
