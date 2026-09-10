<!-- file: docs/agent-tasks/todo-completion-2026-09/server/orchestration.md -->
<!-- version: 1.7.0 -->
<!-- guid: bcebd5a5-c14d-48e7-9636-9c6c49889e12 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — server workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

```mermaid
flowchart LR
    subgraph Wave1
      TASK140[TASK-140 S retire-the-unsafe-cleanup-me]
      TASK129[TASK-129 S fix-wipeactivity-dry-run-cou]
      TASK138[TASK-138 S exempt-the-abs-router-group]
      TASK130[TASK-130 S register-searchindexdroppedc]
      TASK134[TASK-134 M add-a-wiring-level-test-prov]
      TASK136[TASK-136 M convert-reconcile-apply-from]
      TASK208[TASK-208 M migrate-internal-server-test]
      TASK206[TASK-206 L split-or-speed-up-the-intern]
      TASK210[TASK-210 L migrate-internal-server-test]
      TASK211[TASK-211 L migrate-internal-server-test]
    end
    subgraph Wave2
      TASK205[TASK-205 S replace-testserverstartgrace]
      TASK209[TASK-209 M migrate-internal-server-test]
    end
    subgraph Wave3
      TASK131[TASK-131 S fix-audiobook-organizer-book]
    end
```
