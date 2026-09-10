<!-- file: docs/agent-tasks/todo-completion-2026-09/dedup/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4cccb504-0df8-46b3-b655-8d061a1bf917 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — dedup workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK046[TASK-046 route-merge-asexternalidreas]
      TASK180[TASK-180 measure-whether-dedup-durati]
      TASK300[TASK-300 mergesplitbookcluster-perfor]
      TASK325[TASK-325 two-ops-scan-the-whole-embed]
    end
    subgraph Wave2_M
      TASK045[TASK-045 build-a-dry-run-report-only]
      TASK048[TASK-048 physically-co-locate-a-combi]
      TASK049[TASK-049 acoustic-confirm-signal-prom]
      TASK192[TASK-192 clamp-composescore-against-p]
      TASK193[TASK-193 wire-round-2-confidence-boun]
      TASK301[TASK-301 unattended-auto-merge-paths]
      TASK354[TASK-354 dedup-series-dedup-s-apply-p]
    end
    subgraph Wave3_L
      TASK040[TASK-040 make-unmergeauto-reverse-ext]
      TASK050[TASK-050 shattered-book-reassembly-ma]
      TASK364[TASK-364 sec-origin-is-reachable-from]
    end
```
