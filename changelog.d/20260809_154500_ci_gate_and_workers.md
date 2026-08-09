<!-- CI/test-infrastructure change: adds scripts/gate-merge-pr.sh (a merge gate
     that requires a positive green signal rather than an absence), and sets
     Playwright to one worker on CI after measuring that two workers on a
     2-CPU runner starve MUI transitions and produce false failures.

     No product behaviour changed, so this fragment is intentionally all
     comments and contributes nothing to CHANGELOG.md. -->
