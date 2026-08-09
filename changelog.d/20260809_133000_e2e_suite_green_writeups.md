<!-- Docs/TODO-only change: two todo.d fragments, no code touched, so this
     fragment is intentionally all comments and contributes nothing to
     CHANGELOG.md.

     1. 20260809-book-detail-purge-suite-only-flake.md — the one full-suite
        failure that did not reproduce (1 in 1,136 executions). Deliberately
        NOT fixed: it passes 6/6 in isolation, so there is no measurement
        establishing the app is correct, and tolerating it in the test would be
        papering over an unknown.

     2. 20260809-stale-api-token-and-search-index-verification.md — the
        checked-in .api-token returns "invalid session" while /health returns
        200, which blocked verifying that the Bleve search index is complete. -->
