<!-- Test/CI infrastructure only — no user-visible behaviour change, so this
     fragment is deliberately a no-op comment rather than a changelog entry.
     See changelog.d/README.md.

     Two ways an e2e run can report a number that does not mean what it says,
     both now closed.

     1. A spec file that stops being DISCOVERED shrinks the suite instead of
        failing it, and Playwright exits 0 either way. Six spec files were
        disabled by accident and went unnoticed for four months; nothing was
        red the whole time. web/tests/e2e/check-spec-discovery.mjs now fails if
        any spec file on disk contributes no runnable test to the project CI
        actually runs. It carries no hard-coded exclusion list — discovery runs
        without --project and the union across all projects must cover every
        file on disk — so it cannot drift out of sync with playwright.config.ts.

     2. A worktree without its own `npm ci` gets whatever Playwright `npx`
        finds by walking up past it, because a git worktree is a sibling of the
        main checkout rather than a child. Here that was an unrelated project's
        1.57.0 in $HOME while CI ran the pinned 1.62.1. Resolution is now
        asserted in both entry points: the discovery script, and global-setup.ts
        (which covers a bare `npx playwright test`, the command people actually
        type when iterating on one spec).

     Wired into every layer that was silent: the `test:e2e` npm script, a
     dedicated step in e2e.yml before the suite, and globalSetup for direct
     invocations. -->
