<!-- file: docs/agent-tasks/todo-completion-2026-09/itunes/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: f267adb6-c908-49e8-b44a-dec3a36a9503 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — itunes workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK063[TASK-063 internal-itunes-backfill-go]
      TASK184[TASK-184 measure-itunes-xml-track-per]
      TASK185[TASK-185 report-the-itunes-listened-i]
      TASK323[TASK-323 external-id-backfill-s-done]
    end
    subgraph Wave2_M
      TASK062[TASK-062 internal-itunes-backfill-go]
      TASK064[TASK-064 add-a-part-disc-chapter-trac]
      TASK065[TASK-065 p2-relocate-only-sync-cycle]
    end
```
