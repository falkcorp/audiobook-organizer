<!-- file: docs/agent-tasks/todo-completion-2026-09/audiobooks/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 20207939-5470-4cb5-a956-c94753398417 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — audiobooks workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK004[TASK-004 add-a-conformance-test-asser]
    end
    subgraph Wave2_M
      TASK001[TASK-001 add-a-short-ttl-cache-to-the]
      TASK005[TASK-005 wire-onlyparsedtranscription]
    end
```
