<!-- Test/CI infrastructure only — no user-visible behaviour change, so this
     fragment is deliberately a no-op comment rather than a changelog entry.
     See changelog.d/README.md.

     Adds web/tests/e2e/check-spec-discovery.mjs, which fails if any e2e spec
     file on disk contributes no runnable test. Six spec files were disabled by
     accident and went unnoticed for four months; nothing was red the whole
     time, because a file that stops being discovered shrinks the suite rather
     than failing it, and Playwright exits 0 either way. Wired into both layers
     that were silent: the `test:e2e` npm script (local) and a dedicated step in
     e2e.yml (CI). -->
