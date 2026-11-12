- [ ] **TODO-F7-03-VALIDATE-ENDPOINT** Call `ai.Router.ValidateEndpoint` on every
      Put of an `ai`-kind instance in the F7 flip (F7-09). F7-03 ships the hook and
      nothing calls it, so a wildcard capability tick (`"*"`) would be stored and
      never honoured. Acceptance: a wildcard `PUT` through the kernel answers 422
      and stores nothing. Decide whether the larger fix (a descriptor-level
      validation hook in `integrations`, about 40 lines) replaces the kernel's duty
      to remember the call. Brief: `docs/agent-tasks/platform-core/F7-09-f7-flip-integrations.md`.
- [ ] **TODO-F7-03-CLEAR-BENCH** Have the `core.integrations.health` task call
      `ai.Router.ClearBench` on a `HealthOK` probe (F7-09), without clearing a quota
      bench: `GET /v1/models` succeeds on a spent balance.
- [ ] **TODO-F7-01-HOLD-PERSIST** `integrations.HoldPolicy.Persist` is a dead flag:
      `providerhttp` writes every hold to the store unconditionally and merges the
      field with an OR (`providerhttp/scope.go`), so `false` cannot be expressed and
      the OpenAI adapter's `Persist: false` changes nothing. Make it a real choice
      (a `NoPersist` field or a tri-state, with merge semantics and a test), or
      delete the field.
- [ ] **TODO-F7-03-POLL-DAMAGED-ROW** `JobStore.Open` ends the listing at the first
      damaged job row, so jobs after it in ID order are not polled until the row is
      repaired (the jobs before it now are). The complete fix is for `Open` to yield
      the error for that row and continue; every `Open` consumer (the poll and two
      `aitest` suites) then has to treat a per-row error as non-terminal.
