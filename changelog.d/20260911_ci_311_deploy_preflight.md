### Fixed

#### Deploy pre-flight now refuses unpushed commits, not just unpulled ones (CI-01)

`make deploy` / `make deploy-debug` (from `Makefile.local.example`) checked `git merge-base --is-ancestor origin/main HEAD`, which passes when HEAD is strictly ahead of `origin/main`, so a checkout with local, unreviewed commits could ship to production. Both targets now call the new `scripts/deploy-preflight.sh`, which requires `git rev-list --left-right --count HEAD...origin/main` to be `0 0` after a fetch and says which direction is off; `scripts/tests/test_deploy_preflight.py` covers equal, ahead, behind, diverged and stale-ref cases, pins the old guard's false pass, and fails if either target stops calling the script. Re-copy the two lines into your gitignored `Makefile.local` after pulling.
