<!-- file: docs/agent-tasks/todo-completion-2026-09/scanner/orchestration.md -->
<!-- version: 1.6.0 -->
<!-- guid: df933511-7d68-496e-8e54-165381206928 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — scanner workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK309[TASK-309 S inline-ai-parse-phase-result]
      TASK351[TASK-351 L stage-3-durable-deferral-whe]
    end
```
