- [ ] **CI-BACKLOG-ROUTER** Build a router that watches the Woodpecker queue
      (`GET /api/queue/info` pending_count, plus `/api/agents` capacity and
      `no_schedule`) and, when the backlog exceeds what the online agents can
      drain in a set time, dispatches the queued heavy steps to GitHub-hosted
      runners instead. Observed 2026-10-30 19:55: 328 steps pending, 5 slots
      live (u0 1, u0-local 1, llm1 2, mac 1), u1's 10 slots paused by the
      owner's schedule, 40 PRs waiting with nothing merging. Shape: the same
      step definitions exist as GHA jobs already (`make ci-woodpecker` mirrors
      them), so the router only needs to (1) read the backlog and the agent
      window from `ci-agent-schedule.py`, (2) pick the pipelines whose PR has
      no GHA run for the same step and `gh workflow run` the matching workflow
      with the PR head SHA, (3) cancel or skip the Woodpecker duplicate so the
      merge gate (`feedback_merge_gate_require_woodpecker_statuses`) still
      sees a status, by posting the GHA result back as a commit status under
      the Woodpecker context name or by teaching the gate to accept either.
      Guard the GitHub minutes budget (cap per hour, owner-set) and never move
      Mac-only steps (TagLib, decodes). Owner asked for this 2026-10-30.
