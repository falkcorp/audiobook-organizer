<!-- file: docs/agent-tasks/todo-completion-2026-09/scanner/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: df933511-7d68-496e-8e54-165381206928 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — scanner workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK309[TASK-309 inline-ai-parse-phase-result]
    end
    subgraph Wave2_L
      TASK347[TASK-347 finish-the-llm-fallback-chai]
    end
```
