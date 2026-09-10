<!-- file: docs/agent-tasks/todo-completion-2026-09/dedup/orchestration.md -->
<!-- version: 1.7.0 -->
<!-- guid: 4cccb504-0df8-46b3-b655-8d061a1bf917 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — dedup workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK300[TASK-300 S mergesplitbookcluster-perfor]
      TASK325[TASK-325 S two-ops-scan-the-whole-embed]
      TASK046[TASK-046 S route-merge-asexternalidreas]
      TASK180[TASK-180 S measure-whether-dedup-durati]
      TASK301[TASK-301 M unattended-auto-merge-paths]
      TASK358[TASK-358 M dedup-series-dedup-s-apply-p]
      TASK192[TASK-192 M clamp-composescore-against-p]
      TASK373[TASK-373 L abs-sync-task-12-p1-data-los]
      TASK050[TASK-050 L shattered-book-reassembly-ma]
    end
    subgraph Wave2
      TASK049[TASK-049 M acoustic-confirm-signal-prom]
      TASK045[TASK-045 M build-a-dry-run-report-only]
      TASK048[TASK-048 M physically-co-locate-a-combi]
    end
    subgraph Wave3
      TASK193[TASK-193 M wire-round-2-confidence-boun]
    end
```

**Held for the owner (not dispatchable as code):** TASK-040 (DEFER)
