### Changed

- CI: the "Require changelog fragment" check passes Dependabot PRs straight away, logging "dependabot PR — changelog fragment not required", so dependency bumps no longer sit red waiting for a fragment Dependabot cannot write. It goes by the PR's author, so a re-run or rebase by someone else still skips. The scriv collect dry run still runs on every PR.
