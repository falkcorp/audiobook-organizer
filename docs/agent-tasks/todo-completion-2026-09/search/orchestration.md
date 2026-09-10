<!-- file: docs/agent-tasks/todo-completion-2026-09/search/orchestration.md -->
<!-- version: 1.6.0 -->
<!-- guid: fd0e34c8-9277-482a-8f5f-a2b8ab577fe7 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — search workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK126[TASK-126 S surface-to-the-user-when-all]
      TASK125[TASK-125 M index-track-names-on-bookdoc]
    end
```
