<!-- Test-only change: no user-visible behaviour changed, so this fragment is
     intentionally all comments and contributes nothing to CHANGELOG.md.

     The three webkit-only pagination tests in library-browser.spec.ts were
     flaky (11 failures in 24 runs). Measurement showed the application is
     correct and Playwright's synthesised pointer click on MUI's PaginationItem
     is what is unreliable on webkit: driving the identical buttons with an
     in-page DOM click passed 6 runs of 6, while the Playwright click failed
     4 of 4. The tests now use a clickPagination helper that re-checks the URL,
     clicks, asserts, and retries once — actionability checks preserved, so a
     genuinely broken control still fails.

     todo.d/20260809-library-double-fetch-swallows-clicks.md carries the full
     evidence trail, including the two earlier causal claims this corrects. -->
