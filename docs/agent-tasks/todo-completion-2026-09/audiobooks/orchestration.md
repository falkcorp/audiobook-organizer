<!-- file: docs/agent-tasks/todo-completion-2026-09/audiobooks/orchestration.md -->
<!-- version: 1.7.0 -->
<!-- guid: 20207939-5470-4cb5-a956-c94753398417 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — audiobooks workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK004[TASK-004 S add-a-conformance-test-asser]
    end
    subgraph Wave2
      TASK005[TASK-005 M wire-onlyparsedtranscription]
    end
    subgraph Wave3
      TASK001[TASK-001 M add-a-short-ttl-cache-to-the]
    end
```
