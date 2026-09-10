<!-- file: docs/agent-tasks/todo-completion-2026-09/docs/orchestration.md -->
<!-- version: 1.6.0 -->
<!-- guid: b02ad3b6-f15e-49d7-8605-ed6819387476 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — docs workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK182[TASK-182 S record-the-docs-system-vs-to]
      TASK057[TASK-057 M phase-8-write-the-abs-topolo]
    end
```
