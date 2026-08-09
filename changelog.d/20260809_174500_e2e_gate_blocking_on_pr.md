<!-- CI change: restores the pull_request trigger on the E2E workflow and makes
     the job BLOCKING on that path, after measuring the PR configuration green
     on the real runner (272 passed / 0 failed / 8 skipped). continue-on-error
     is now conditional so the both-engine nightly, which still has one webkit
     failure, stays advisory rather than being handed a green light.

     No product behaviour changed, so this fragment is intentionally all
     comments and contributes nothing to CHANGELOG.md. -->
