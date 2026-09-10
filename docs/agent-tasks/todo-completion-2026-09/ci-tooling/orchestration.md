<!-- file: docs/agent-tasks/todo-completion-2026-09/ci-tooling/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 26cd9f1b-4bc6-4db5-a343-77522635bb1a -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — ci-tooling workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK014[TASK-014 remove-committed-mtls-bridge]
      TASK191[TASK-191 bump-the-github-common-reusa]
      TASK307[TASK-307 frontend-ci-yml-grants-an-un]
      TASK311[TASK-311 deploy-deploy-debug-guard-ch]
      TASK312[TASK-312 node-version-drift-security]
      TASK313[TASK-313 the-only-go-version-consiste]
      TASK314[TASK-314 no-concurrency-guard-between]
      TASK320[TASK-320 frontend-job-gate-is-a-compu]
    end
    subgraph Wave2_M
      TASK009[TASK-009 teach-the-abs-fixture-captur]
      TASK339[TASK-339 real-reflink-detection-via-z]
      TASK360[TASK-360 c716-resolved-the-3-954-book]
    end
```
