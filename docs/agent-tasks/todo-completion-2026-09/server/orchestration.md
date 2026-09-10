<!-- file: docs/agent-tasks/todo-completion-2026-09/server/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: bcebd5a5-c14d-48e7-9636-9c6c49889e12 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — server workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

```mermaid
flowchart LR
    subgraph Wave1_S
      TASK129[TASK-129 fix-wipeactivity-dry-run-cou]
      TASK130[TASK-130 register-searchindexdroppedc]
      TASK131[TASK-131 fix-audiobook-organizer-book]
      TASK138[TASK-138 exempt-the-abs-router-group]
      TASK140[TASK-140 retire-the-unsafe-cleanup-me]
      TASK205[TASK-205 replace-testserverstartgrace]
    end
    subgraph Wave2_M
      TASK134[TASK-134 add-a-wiring-level-test-prov]
      TASK136[TASK-136 convert-reconcile-apply-from]
      TASK208[TASK-208 migrate-internal-server-test]
      TASK209[TASK-209 migrate-internal-server-test]
    end
    subgraph Wave3_L
      TASK206[TASK-206 split-or-speed-up-the-intern]
      TASK210[TASK-210 migrate-internal-server-test]
      TASK211[TASK-211 migrate-internal-server-test]
    end
```
