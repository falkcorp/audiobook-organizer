### Removed

- CI/tests: `TestR0OpensR1Store`, the check that the R0 release binary opens a store this build wrote, and the R0 release download that fed it (`scripts/ci/fetch_r0.sh`, and its steps in `ci.yml` and `boot-crash-matrix-abo`). Rolling back to R0 belongs to the release before the identity switch; after it, the store's accounts and keys are core's, which R0 cannot read, so that rollback is not a path this build supports.
