<!-- Workflow-comment correction plus TODO evidence — no code, no behaviour
     change, so this fragment is deliberately a no-op comment. See
     changelog.d/README.md.

     e2e.yml's header comment asserted that the E2E job "BLOCKS on every
     trigger". It never has. Verified via
     `gh api repos/falkcorp/audiobook-organizer/rules/branches/main`, which
     returns org-inherited rules too: the only rules on main are
     required_linear_history, deletion, repository_delete and
     repository_transfer, all from the falkcorp Organization ruleset. No
     required_status_checks rule, no pull_request rule, and classic branch
     protection 404s. So a red E2E run merges as easily as a green one, and
     `gh pr merge --admin` bypasses nothing.

     Files E2EGATE-NOTREQUIRED (enable enforcement, but only after measuring
     the required-check-plus-paths-filter interaction, which can strand
     filtered-out PRs at "Expected — Waiting for status") and TODO-HEADERLEAK
     (75 todo.d fragment headers folded verbatim into TODO.md, which
     todo.d/README.md forbids and nothing checks).

     The paths filter itself was checked and is sound: every spec plus
     playwright.config.ts, global-setup.ts and utils/test-helpers.ts lives
     under web/tests/e2e/, which web/** matches. No enforcement was changed in
     this PR — that is an owner decision. -->
