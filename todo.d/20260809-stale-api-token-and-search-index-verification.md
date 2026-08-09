<!-- file: todo.d/20260809-stale-api-token-and-search-index-verification.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9d3f6a15-4c87-4b20-8e51-7a0b93c26e4d -->
<!-- last-edited: 2026-08-09 -->

- [ ] **The checked-in `.api-token` no longer authenticates, and it blocked a real
      verification.** Found 2026-08-09 while grounding
      `docs/design/2026-08-09-search-backend-options.md`.

      `.api-token` (the shared per-worktree API key created by the `server-bootstrap`
      skill and documented in `CLAUDE.md`) returns:

      ```
      {"error":"invalid session","code":"UNAUTHORIZED","status":401}
      ```

      while `/api/v1/health` returns 200 — so the server is up and it is the credential
      that is stale, not the endpoint. The file dates from 2026-07-14.

      **Why this matters beyond convenience.** It blocked a specific question that is worth
      answering: **is the Bleve search index complete?** The engine is confirmed *open* in
      production (`msg="Search index opened"` on the current process and every restart back
      to Aug 07), but an index that opens fine while missing books produces confidently
      wrong results. The other route to that answer — reading the index directory — needs
      root, and `sudo` on the prod host requires interactive authentication.

      **Do:**
      1. Regenerate `.api-token` via the documented bootstrap path.
      2. With it, compare a broad search's result count against the same term reached
         through a filter-only path. A large gap means index drift.
      3. Consider whether a *silent* search degradation is acceptable: `Open()` failures
         are downgraded to warnings so the server boots without search
         (`internal/search/register.go`), so a fallback to the O(N) substring scan would
         run indefinitely with only a startup warning to show for it. With `/metrics`
         currently unscraped (see the Prometheus gap), nothing would surface it. That is
         the same failure shape as the six e2e specs that sat disabled for four months.

      See §6 Q1 of `docs/design/2026-08-09-search-backend-options.md`.
