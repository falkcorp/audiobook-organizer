<!-- file: docs/agent-tasks/todo-completion-2026-09/web/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 36f3a33b-f8a3-45a5-86f4-99d9e287cd9e -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — web workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK161[TASK-161 strip-dedup-and-metadata-sou]
      TASK162[TASK-162 reformat-metadata-tags-in-br]
      TASK166[TASK-166 make-the-book-detail-page-s]
      TASK167[TASK-167 make-the-book-detail-page-s]
      TASK169[TASK-169 link-version-group-id-to-a-f]
      TASK170[TASK-170 retarget-dedup-operations-sp]
      TASK171[TASK-171 retarget-diagnostics-spec-ts]
      TASK304[TASK-304 author-merge-preview-popover]
      TASK329[TASK-329 dashboard-count-widgets-sile]
      TASK330[TASK-330 operations-timeline-fetch-sw]
    end
    subgraph Wave2_M
      TASK158[TASK-158 add-a-settings-panel-section]
      TASK160[TASK-160 move-openai-api-key-validati]
      TASK165[TASK-165 review-the-17-apifetch-calle]
      TASK168[TASK-168 make-narrator-publisher-genr]
      TASK173[TASK-173 add-resizable-sortable-colum]
      TASK174[TASK-174 add-resizable-sortable-colum]
      TASK175[TASK-175 add-resizable-sortable-colum]
      TASK189[TASK-189 play-the-first-2-minutes-of]
      TASK217[TASK-217 evidence-panel-explain-a-mis]
      TASK324[TASK-324 authors-page-fetches-the-ent]
      TASK327[TASK-327 series-page-fetches-the-enti]
      TASK332[TASK-332 no-vitest-or-playwright-cove]
    end
```
