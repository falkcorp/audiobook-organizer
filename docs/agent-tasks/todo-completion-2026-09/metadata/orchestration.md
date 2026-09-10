<!-- file: docs/agent-tasks/todo-completion-2026-09/metadata/orchestration.md -->
<!-- version: 1.7.0 -->
<!-- guid: d1362b79-49c5-46b1-b1c2-c263ba2b8b30 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — metadata workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK310[TASK-310 S isbn-asin-enrichment-sweep-d]
      TASK317[TASK-317 S auto-merge-primary-selection]
      TASK080[TASK-080 M assess-the-2-critical-go-req]
    end
```
