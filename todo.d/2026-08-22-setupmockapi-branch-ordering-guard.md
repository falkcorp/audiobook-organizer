- [ ] **TODO-MOCKORDER** Decide whether to add a permanent guard against shadowed
      branches in the `setupMockApi` dispatcher
      (`web/tests/e2e/utils/test-helpers.ts`). TASK-093 audited the 10
      `pathname.startsWith(...)` catch-alls by hand and found 0 shadowed
      branches, but an audit decays the moment someone adds a new branch below a
      catch-all — which is exactly how the `/api/v1/audiobooks/batch` POST bug
      got in. **Caveat that makes this a decision, not a task:** the dispatcher
      mixes three branch forms — 67 `pathname === '...'`, 10
      `pathname.startsWith(...)`, and 24 `pathname.match(/.../)` — and a
      literal-parsing guard reads the first two accurately but can only
      approximate a regex by its leading literal prefix (one of the 24, at
      ~L1584, is even split across lines and unreadable by a line-based parser).
      A guard blind to a third of the branches would advertise more coverage than
      it has. Either accept that limit explicitly, or restructure the dispatcher
      into a route table that can be checked exactly.

- [ ] **TODO-MOCKWORKS** `web/tests/e2e/utils/test-helpers.ts` ~L1750:
      `pathname.startsWith('/api/v1/works')` has no trailing slash, so it also
      matches any future sibling path with that prefix (`/api/v1/workspaces`,
      `/api/v1/works-queue`, ...). Nothing is shadowed today; add the trailing
      slash (plus a separate exact branch for bare `/api/v1/works`) before any
      such endpoint is mocked. Same file ~L732: `/api/v1/backup/list` has no
      HTTP-method guard, so it answers a `DELETE /api/v1/backup/list` ahead of
      the `/api/v1/backup/` DELETE catch-all below it.
