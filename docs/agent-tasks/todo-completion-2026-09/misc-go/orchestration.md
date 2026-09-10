<!-- file: docs/agent-tasks/todo-completion-2026-09/misc-go/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: a2d5e791-8594-476f-bd60-cd506e9d0231 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — misc-go workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK086[TASK-086 collapse-internal-whitespace]
      TASK345[TASK-345 move-database-backups-off-th]
      TASK348[TASK-348 the-unknown-author-repair-is]
      TASK353[TASK-353 sec-backup-abspath]
      TASK363[TASK-363 missing-op-now-built-run-pen]
    end
    subgraph Wave2_M
      TASK083[TASK-083 fix-or-verify-the-4-still-op]
      TASK186[TASK-186 measure-the-real-double-prim]
      TASK335[TASK-335 activity-log-reset-feature-r]
      TASK342[TASK-342 library-scan-killed-by-the-w]
      TASK346[TASK-346 prod-has-chapter-consolidati]
      TASK351[TASK-351 repair-the-book-rows-that-we]
    end
    subgraph Wave3_L
      TASK197[TASK-197 audit-every-registry-runitem]
    end
```
