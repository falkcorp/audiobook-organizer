<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/orchestration.md -->
<!-- version: 1.6.0 -->
<!-- guid: e9f436ef-bb62-4ce6-8453-8b425205ead8 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — server-handlers workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK306[TASK-306 S post-backup-restore-caller-r]
      TASK308[TASK-308 S sse-handler-unconditionally]
      TASK318[TASK-318 S publisheddecades-filter-list]
      TASK319[TASK-319 S delete-operations-history-de]
      TASK214[TASK-214 S cap-get-api-v1-audiobooks-me]
      TASK328[TASK-328 S ipratelimiter-sweeps-the-ent]
      TASK143[TASK-143 S n-3-stop-advertising-delete]
      TASK148[TASK-148 S re-capture-the-series-abs-fi]
      TASK346[TASK-346 M series-normalize-trashed-gap]
      TASK365[TASK-365 M sec-2-bootstrap-still-writes]
      TASK366[TASK-366 M sec-4-residue-no-csp-header]
      TASK142[TASK-142 M expose-unmergeauto-through-a]
      TASK149[TASK-149 M detect-multi-file-books-whos]
      TASK150[TASK-150 M audit-apply-shaped-endpoints]
      TASK339[TASK-339 M there-is-no-delete-one-op-en]
      TASK321[TASK-321 M search-index-bulk-backfill-i]
      TASK147[TASK-147 M align-abs-conformance-fixtur]
      TASK336[TASK-336 L full-application-database-re]
    end
    subgraph Wave2
      TASK337[TASK-337 M add-a-dry-run-count-mode-to]
      TASK154[TASK-154 M implement-post-api-session-l]
      TASK157[TASK-157 M parallelize-the-per-candidat]
      TASK345[TASK-345 L series-phantom-repair-repair]
    end
```
