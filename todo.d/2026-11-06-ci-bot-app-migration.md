- [ ] **TODO-CI-BOT-APP** Finish moving this repo's bot pushes to the org CI bot GitHub App
      (ruleset 26660812's only bypass; org var `CI_BOT_APP_CLIENT_ID`, org secret
      `CI_BOT_APP_PRIVATE_KEY`). Plan: `.claude/notes/2026-11-06-github-app-auth-migration.md`.
  - [ ] Draft PR `ci/bot-app-auth`: `todo-collect`, `auto-revert`, `memory-leak-scan` and
        `ratchet-lower` mint an app token. todo-collect also gets `persist-credentials: false`,
        because checkout's extraheader made it push as github-actions[bot] no matter which
        token was set.
  - [ ] After the github-common `reusable-release.yml` fix merges, move
        `.github/ghcommon-ref.txt` and every `falkcorp/github-common/...@<sha>` pin together.
  - [ ] Verify each with `workflow_dispatch`: the push or PR comes from the app's bot user,
        and release-prod's collect-changelog lands on main without GH013.
  - [ ] Then drop this repo's `CI_APP_*` secrets and its `JF_CI_GH_PAT` references
        (org-wide retirement is tracked in github-common's TODO).
