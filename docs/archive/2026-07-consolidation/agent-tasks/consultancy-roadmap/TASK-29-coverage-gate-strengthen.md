<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-29-coverage-gate-strengthen.md -->
<!-- version: 1.0.0 -->
<!-- guid: e5aaaade-6f38-4b18-91a7-5567dcbdce77 -->
<!-- last-edited: 2026-07-03 -->

# TASK-29 — Strengthen the 30% coverage gate (consultancy-roadmap, PROC-4)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Haiku · **Wave:** 2 · **Depends on:** TASK-08

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-29-coverage-gate-strengthen" -b agent/cr-29-coverage-gate-strengthen origin/main
cd "$REPO/.worktrees/cr-29-coverage-gate-strengthen"
git rebase origin/main
```

**Wave note:** This task is Wave 2 (not Wave 1) because TASK-08 also edits the
`Makefile`. Do not start this task's worktree until TASK-08 has merged to
`main` and you have rebased onto the latest `origin/main` (the `git rebase
origin/main` step above will fail loudly with conflict markers if TASK-08
hasn't landed yet — if that happens, stop and re-fetch/re-check rather than
resolving Makefile conflicts blind).

## Goal

Fix `coverage-check-short` in the `Makefile` so it:

1. Does **not** re-run the entire test suite a second time just to measure
   coverage (today it runs `go test ./...` once via `test-short` inside `ci`,
   then runs `go test ./...` again inside `coverage-check-short` — doubling
   `make ci` wall time).
2. Does **not** swallow test/compile failures silently (`>/dev/null 2>&1`
   currently hides all diagnostics when the coverage run itself fails).
3. Prints the per-package coverage summary to CI output, not just the total.
4. Keeps the 30% floor, but adds a lightweight ratchet: a committed
   coverage-floor value that can only be raised, plus a WARN (non-failing) when
   the just-measured total coverage drops versus the last recorded value.
5. Leaves `make ci` wall-clock time flat or better (the whole point is
   eliminating the duplicate test run).

## Background (verify before editing)

- The consultancy finding (PROC-4, `docs/consultancy/06-process-and-security.md`)
  cites `Makefile:309-318` and `Makefile:321`. As of this writing (verify
  yourself — line numbers drift) the relevant targets are:
  - `test-short` (around line 173) runs `go test ./... -short -race` — no
    coverprofile, not swallowed.
  - `test-all-short` (around line 251-252) = `test-short web-test`.
  - `coverage-check-short` (around line 308-318) independently re-runs
    `go test ./... -short -coverprofile=coverage.out -covermode=atomic
    >/dev/null 2>&1`, then greps `go tool cover -func=coverage.out` for the
    `total` line and compares against a hardcoded `30`.
  - `ci` (around line 321): `ci: mocks-check check-mock-fresh staticcheck
    sdkguard test-all-short coverage-check-short` — this is exactly the
    "duplicate test run" PROC-4 describes: `test-all-short` already runs
    `go test ./... -short -race` once (via `test-short`), then
    `coverage-check-short` runs `go test ./... -short` a second time (with
    `-coverprofile` instead of `-race`) purely to get a coverage number.
  - There is a separate `coverage-check` (around line 296-306, full suite, no
    `-short`) used by `test-nightly` (around line 255) — do NOT touch this one
    unless you also fix the analogous duplication there; this task is scoped
    to the `-short`/`ci` path only per the finding. If you have time, apply the
    same profile-reuse pattern to `coverage-check`/`test-nightly` too, but it
    is not required for acceptance.
  - There is no existing coverage-floor/baseline file anywhere in the repo
    (verified: no `.coverage-floor`, `.coverage-baseline`, or similar file
    exists as of this writing). You are creating this convention fresh — do
    not assume a prior format to match.

- **Re-verify these anchors before editing** — line numbers in this brief and
  in the consultancy doc are from 2026-07-02/03 and may have drifted, and
  TASK-08 (which runs first) also touches this file:
  ```bash
  grep -n "^test-short:\|^test-all-short:\|^coverage-check-short:\|^coverage-check:\|^ci:\|^test-nightly:" Makefile
  ```
  Read the full body of `coverage-check-short` and `ci` before changing
  anything:
  ```bash
  sed -n '/^coverage-check-short:/,/^$/p' Makefile
  sed -n '/^ci:/,/^$/p' Makefile
  ```

## Step-by-step

1. Re-run the grep/sed commands above in your worktree to get current line
   numbers and exact target bodies — do not trust the line numbers quoted
   above.
2. Modify `test-short` so it produces a coverage profile as a side effect of
   the one test run it already does, e.g.:
   ```makefile
   test-short: vet
   	@echo "🧪 Running backend tests (-short — slow prop tests skipped)..."
   	@go test ./... -short -race -coverprofile=coverage.out -covermode=atomic
   	@echo "✅ Short backend tests passed"
   ```
   (Confirm `-race` and `-coverprofile` can coexist in this codebase — they
   can in standard `go test`; if you hit an actual incompatibility specific to
   this repo's test setup, note it in the PR description and fall back to a
   separate `go test ./... -short -coverprofile=coverage.out -covermode=atomic`
   line appended to `test-short` instead of merging the flag into the `-race`
   invocation.)
3. Rewrite `coverage-check-short` to **consume** the `coverage.out` produced by
   `test-short` instead of re-running the suite. It must:
   - Fail with a clear error (do not silently exit 0) if `coverage.out` does
     not exist — this makes the target still runnable standalone (e.g.
     `make coverage-check-short` alone after a manual `go test ... -coverprofile=coverage.out`),
     while catching the case where someone runs it out of order.
   - Print the per-package summary, not just the total:
     ```makefile
     @go tool cover -func=coverage.out | grep -v total
     ```
     followed by the total line, so CI logs show which packages are
     contributing/dragging the number.
   - Remove the `>/dev/null 2>&1` redirect entirely so any underlying failure
     is visible in CI output.
   - Keep the existing 30% floor check (do not lower or remove it).
   - Add the ratchet: read a committed floor file (create it at
     `.ci/coverage-floor.txt` containing a single number, e.g. `30`, decided
     by measuring current total coverage and rounding down to a safe integer —
     compute it by running `go tool cover -func=coverage.out | grep total` in
     your worktree after step 2, and set the floor file to that value; do NOT
     hardcode `30` if actual coverage is meaningfully higher — set the floor
     file to the *current* measured value so it acts as a real ratchet from
     day one). Compare the measured coverage against the floor file value:
     - If below the floor file value → fail (same failure behavior as today,
       message should reference the floor file).
     - If below the previous recorded value in `.ci/coverage-last.txt` (a
       second, non-committed-required, informational file — create it if
       absent, print a `WARN:` line if coverage dropped vs. the last recorded
       run, but do not fail the build on this WARN) → print WARN only.
     - Update `.ci/coverage-last.txt` with the newly measured value at the end
       of a successful run (this file tracks recent local/CI runs; it does
       not need to be a strictly-committed ratchet artifact — keep it simple,
       a plain last-write-wins text file is fine; if you'd rather avoid an
       uncommitted-file-write footgun in CI, it is acceptable to skip the WARN
       history feature entirely and rely solely on the committed
       `.ci/coverage-floor.txt` hard floor — note whichever choice you make in
       the PR description).
   - The floor file (`.ci/coverage-floor.txt`) is committed to the repo and
     can only be *raised* by a human/agent in a follow-up commit — document
     this in a one-line comment above the target in the Makefile.
3a. Add `.ci/` to be tracked normally (it is not build output, so do not add it
    to `.gitignore`).
4. Update the `ci` target so it no longer runs `coverage-check-short` as a
   step that re-executes tests — since `test-short`/`test-all-short` now
   produces `coverage.out` as a side effect, `coverage-check-short` becomes a
   pure "read coverage.out and assert" step with no test execution. Leave the
   target ordering (`ci: mocks-check check-mock-fresh staticcheck sdkguard
   test-all-short coverage-check-short`) unchanged — `coverage-check-short`
   still needs to run *after* `test-all-short` so `coverage.out` exists by
   then; you are only changing what happens *inside* each target, not the
   dependency order.
5. Update the `## comment` lines above both targets (the `##` doc-comments
   used by `make help`) to reflect the new behavior (no more "using -short
   suite" re-run wording; mention the floor-file ratchet).
6. Bump the file header on `Makefile` if it has one (check the top of the
   file); if `Makefile` has no version-header convention (Makefiles often
   don't), skip — do not invent a header format not already used elsewhere in
   this file. Add proper headers to any new files you create
   (`.ci/coverage-floor.txt` is a plain data file — a one-line `# floor` style
   header is fine but not required if no other `.ci/*` files in the repo use
   one; check `ls .ci/ 2>/dev/null` first).
7. Time `make ci` before and after your change (or at minimum, count how many
   times `go test ./...` invocations occur in the target chain before vs.
   after) and note the comparison in the PR description — this is the
   acceptance-relevant proof that runtime is flat or better.

## How to test

```bash
go build ./...
go vet ./...
# Exercise the new flow directly (this IS the test — there's no Go test file
# for a Makefile target; verify behavior empirically):
make test-short          # should now leave coverage.out on disk
ls -la coverage.out
make coverage-check-short # should read the existing coverage.out, print
                          # per-package + total, and pass/fail against
                          # .ci/coverage-floor.txt without re-running go test
make ci                  # full gate; confirm it still passes end-to-end
```

Also verify the failure path manually:

```bash
rm -f coverage.out
make coverage-check-short   # should fail with a clear, non-empty error
                            # message (not the old silent >/dev/null failure)
```

And verify the ratchet floor actually gates:

```bash
# Temporarily set an unreachable floor to confirm the target fails correctly,
# then restore the real value before committing.
echo "99" > .ci/coverage-floor.txt
make test-short && make coverage-check-short   # expect failure, clear message
git checkout .ci/coverage-floor.txt            # restore real value
```

## Acceptance criteria

- [ ] `test-short` (and therefore `test-all-short`) produces `coverage.out` as
      part of its single test run — no second `go test ./...` invocation
      exists anywhere in the `ci` target's dependency chain.
- [ ] `coverage-check-short` no longer runs `go test`; it reads the existing
      `coverage.out` and fails clearly (non-empty error) if the file is
      missing.
- [ ] The `>/dev/null 2>&1` redirect is removed; a failing coverage run (or a
      missing `coverage.out`) produces visible diagnostic output.
- [ ] Per-package coverage lines are printed to output (not just `total`).
- [ ] A committed `.ci/coverage-floor.txt` exists, its value reflects real
      measured coverage at the time of this change (not an arbitrary
      re-statement of `30`), and `coverage-check-short` fails when coverage
      drops below it.
- [ ] `make ci` still passes end-to-end after the change.
- [ ] `make ci` wall time is flat or improved versus before (documented in the
      PR description with before/after timing or invocation counts).
- [ ] `go build ./...` and `go vet ./...` are clean.
- [ ] File headers bumped on every changed/created file that already follows
      a header convention in this repo (Makefile: only if it already has one;
      new `.ci/` files: only if sibling files in that directory use headers).

## Commit message

```
fix(ci): stop double-running tests in coverage-check-short and add a coverage floor ratchet

coverage-check-short re-ran the entire -short test suite a second time just to
measure coverage (test-all-short already ran it once), doubling make ci wall
time, and swallowed all diagnostic output via >/dev/null 2>&1. Reuse the
coverage.out produced by test-short, surface per-package coverage in CI logs,
and add a committed .ci/coverage-floor.txt ratchet so the 30% bar can only be
raised, not silently eroded.

Co-Authored-By: Claude Haiku <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-29-coverage-gate-strengthen
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `coverage-check-short` no longer contains a `go test` invocation (i.e. it
only runs `go tool cover -func=coverage.out ...` and reads a floor file), and
`test-short`/`test-all-short` already writes `coverage.out`, this task is
done — verify with:

```bash
grep -n "go test" Makefile | grep -i coverage
grep -n "coverprofile" Makefile
ls .ci/coverage-floor.txt 2>/dev/null
```

If the first grep returns a hit inside `coverage-check-short`'s body, the
duplicate-run problem still exists and the task is not done. If `.ci/` already
has a coverage-floor convention with a different filename, reuse the existing
file rather than creating a second one — check `ls .ci/ 2>/dev/null` and
`grep -rn "coverage-floor\|coverage_floor" Makefile scripts/ .github/` before
deciding the task is unstarted.

Rollback = revert the commit; the pre-existing behavior (duplicate test run,
swallowed output, flat 30% floor, no ratchet) is restored exactly, since this
task only rewrites the bodies of two existing Makefile targets and adds new
files under `.ci/` — nothing else in the repo depends on `coverage.out` being
produced by `coverage-check-short` specifically, only on it existing by the
time that target runs.
