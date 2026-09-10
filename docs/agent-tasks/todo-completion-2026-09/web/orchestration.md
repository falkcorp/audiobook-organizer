<!-- file: docs/agent-tasks/todo-completion-2026-09/web/orchestration.md -->
<!-- version: 1.7.0 -->
<!-- guid: 36f3a33b-f8a3-45a5-86f4-99d9e287cd9e -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — web workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK304[TASK-304 S author-merge-preview-popover]
      TASK329[TASK-329 S dashboard-count-widgets-sile]
      TASK330[TASK-330 S operations-timeline-fetch-sw]
      TASK161[TASK-161 S strip-dedup-and-metadata-sou]
      TASK167[TASK-167 S make-the-book-detail-page-s]
      TASK169[TASK-169 S link-version-group-id-to-a-f]
      TASK171[TASK-171 S retarget-diagnostics-spec-ts]
      TASK160[TASK-160 M move-openai-api-key-validati]
      TASK368[TASK-368 M react-router-ghsa-qwww-vcr4]
      TASK189[TASK-189 M play-the-first-2-minutes-of]
      TASK173[TASK-173 M add-resizable-sortable-colum]
      TASK332[TASK-332 M no-vitest-or-playwright-cove]
    end
    subgraph Wave2
      TASK162[TASK-162 S reformat-metadata-tags-in-br]
      TASK166[TASK-166 S make-the-book-detail-page-s]
      TASK170[TASK-170 S retarget-dedup-operations-sp]
      TASK174[TASK-174 M add-resizable-sortable-colum]
      TASK217[TASK-217 M evidence-panel-explain-a-mis]
    end
    subgraph Wave3
      TASK324[TASK-324 M authors-page-fetches-the-ent]
      TASK168[TASK-168 M make-narrator-publisher-genr]
      TASK175[TASK-175 M add-resizable-sortable-colum]
    end
    subgraph Wave4
      TASK327[TASK-327 M series-page-fetches-the-enti]
    end
    subgraph Wave5
      TASK158[TASK-158 M add-a-settings-panel-section]
    end
    subgraph Wave6
      TASK165[TASK-165 M review-the-17-apifetch-calle]
    end
```
