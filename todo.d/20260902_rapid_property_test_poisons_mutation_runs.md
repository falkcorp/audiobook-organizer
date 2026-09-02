### A `rapid` property test can poison every later run in the same working tree

`TestProp_ChromemMatchesSqlite` (`internal/server/dedup_engine_prop_test.go`) is a
`pgregory.net/rapid` property test. When it finds a failing input it **persists the
seed** to `internal/server/testdata/rapid/<TestName>/<TestName>-<ts>-<pid>.fail`,
and rapid **replays saved failures first** on every subsequent run. The directory
is untracked, so nothing cleans it up.

Consequences, in increasing order of nastiness:

1. The test flips from nondeterministically-failing to **deterministically**
   failing, in that tree only, until someone deletes the file.
2. `go test ./internal/server/` then fails for a reason unrelated to whatever the
   developer is working on.
3. **Inside `scripts/mutation-matrix.sh` it manufactures FALSE KILLS.** The
   harness verifies the baseline is green *once, at the start*, and never
   re-checks. If a property test persists a failure partway through, every
   remaining mutant is reported KILLED — indistinguishable from a real kill.

Observed for real on 2026-09-02 during the `series-denumber-writetime` server
table: the `.fail` file appeared at 18:27:23, and the two mutants that ran after
it (M06, M07) both listed `TestProp_ChromemMatchesSqlite` among their killers.
M06 is a **documented equivalent mutant that cannot be killed**, which is what
exposed the taint — without a known-unkillable row in the table, a fully tainted
run reads as a clean 7/7.

Fixes worth considering, cheapest first:

- Clear `*/testdata/rapid` before a harness run (and/or `.gitignore` it).
- **Have `mutation-matrix.sh` re-verify the baseline between mutants**, or at
  least once at the end, and fail the run if the unmutated suite is no longer
  green. This is the transferable fix — it closes the whole class, not this one
  test.
- Consider whether a known-equivalent "canary" row belongs in every table, since
  that is what caught this one.
