<!-- file: docs/agent-tasks/todo-completion-2026-09/metadata/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: d1362b79-49c5-46b1-b1c2-c263ba2b8b30 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — metadata workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK310[TASK-310 isbn-asin-enrichment-sweep-d]
      TASK317[TASK-317 auto-merge-primary-selection]
    end
    subgraph Wave2_M
      TASK080[TASK-080 assess-the-2-critical-go-req]
    end
```
