<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: e9f436ef-bb62-4ce6-8453-8b425205ead8 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — server-handlers workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK143[TASK-143 n-3-stop-advertising-delete]
      TASK148[TASK-148 re-capture-the-series-abs-fi]
      TASK214[TASK-214 cap-get-api-v1-audiobooks-me]
      TASK306[TASK-306 post-backup-restore-caller-r]
      TASK308[TASK-308 sse-handler-unconditionally]
      TASK318[TASK-318 publisheddecades-filter-list]
      TASK319[TASK-319 delete-operations-history-de]
      TASK328[TASK-328 ipratelimiter-sweeps-the-ent]
    end
    subgraph Wave2_M
      TASK142[TASK-142 expose-unmergeauto-through-a]
      TASK147[TASK-147 align-abs-conformance-fixtur]
      TASK149[TASK-149 detect-multi-file-books-whos]
      TASK150[TASK-150 audit-apply-shaped-endpoints]
      TASK154[TASK-154 implement-post-api-session-l]
      TASK157[TASK-157 parallelize-the-per-candidat]
      TASK321[TASK-321 search-index-bulk-backfill-i]
      TASK336[TASK-336 get-operations-timeline-sile]
      TASK338[TASK-338 terminal-ops-never-get-compl]
      TASK343[TASK-343 re-calibrate-the-absolute-ti]
      TASK361[TASK-361 2026-06-22-security-sweep-th]
    end
```
