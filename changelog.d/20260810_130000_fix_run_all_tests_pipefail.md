### Fixed

#### `scripts/run-all-tests.sh` reported "All tests passed" no matter what failed

All three steps in the script had the shape

    if <test command> 2>&1 | tee <log>; then

and a shell pipeline's exit status is that of its **last** command — `tee`,
which essentially always succeeds. So `GO_TESTS_PASSED`, `FRONTEND_UNIT_PASSED`
and `E2E_TESTS_PASSED` were unconditionally `true`, which made the script's
careful three-way summary and its final `exit 0` / `exit 1` logic completely
inert. `set -e` does not help — commands in an `if` condition are exempt from it
by design.

Measured on the real script with all three test commands stubbed to fail:

| Variant | Summary | Exit |
|---|---|---|
| before | 🎉 All tests passed! | **0** |
| after (`set -o pipefail`) | ❌ Go / Frontend / E2E: FAILED | **1** |

The Go step also now passes `-timeout 25m`, matching the Makefile's `./...`
targets. Go's default is 10 minutes **per package** and `internal/server` alone
runs ~500s, so a contended run dies with `panic: test timed out` naming
whichever test happened to be mid-flight — which reads as a failure in an
unrelated test.

The script is a manual runner, not wired into CI or the Makefile, so no CI
result was ever affected. A repo-wide sweep found no other shell script using a
pipe inside an `if`/`while` condition.
