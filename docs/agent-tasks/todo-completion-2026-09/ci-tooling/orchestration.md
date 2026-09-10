<!-- file: docs/agent-tasks/todo-completion-2026-09/ci-tooling/orchestration.md -->
<!-- version: 1.7.0 -->
<!-- guid: 26cd9f1b-4bc6-4db5-a343-77522635bb1a -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — ci-tooling workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK307[TASK-307 S frontend-ci-yml-grants-an-un]
      TASK311[TASK-311 S deploy-deploy-debug-guard-ch]
      TASK312[TASK-312 S node-version-drift-security]
      TASK313[TASK-313 S the-only-go-version-consiste]
      TASK314[TASK-314 S no-concurrency-guard-between]
      TASK014[TASK-014 S remove-committed-mtls-bridge]
      TASK191[TASK-191 S bump-the-github-common-reusa]
      TASK364[TASK-364 M ca12-wave-2-model-logging-sa]
      TASK009[TASK-009 M teach-the-abs-fixture-captur]
    end
    subgraph Wave2
      TASK320[TASK-320 S frontend-job-gate-is-a-compu]
    end
```

**Held for the owner (not dispatchable as code):** TASK-341 (HOLD-FOR-OWNER)
