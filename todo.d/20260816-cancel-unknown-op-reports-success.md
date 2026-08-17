- [ ] **Cancelling an operation the registry has never heard of reports success.**
      `DELETE /operations/v2/<unknown-id>` returns `204 No Content`. The handler
      calls `registry.Cancel(id)`, which returns `nil` for an id with no entry,
      so the route cannot distinguish "asked a running op to stop" from "did
      nothing at all". Measured 2026-08-16 in
      `TestOperationEndpointsErrors` — the assertion was written expecting 500
      and the test disagreed.
      This is the same shape as the legacy route it replaced, which answered 204
      after force-updating a legacy `operations` row that nothing was reading.
      Retiring that route did not fix the lie, it just stopped the write.
      Cancel should 404 for an unknown id and 204 only when something was
      actually signalled. Check whether the UI treats 204 as "cancelled" and
      shows a confirmation for an op that is still running.
