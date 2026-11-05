### Changed

#### Platform core: owner decisions D15 applied to the plan and task briefs (`docs/plans/platform-core/`, `docs/agent-tasks/platform-core/`)

The owner answered every open planning question on 2026-11-05 (D15). Required checks on `main` are enforced by a
ruleset with no bypass actors and GitHub's merge queue, and `/parallel-sweep` never admin-merges; the R1 release
freeze is kept; the split-readiness thresholds are accepted; the 15 unread v2 `Schedule` strings are deleted while
the 14 jobs they duplicate keep their real schedule. Two advisor simplifications were adopted: the per-flip upgrade
test is dropped (M8 is registered once, as `books.rewrite`), and the release freeze becomes the `RELEASE_FREEZE`
repository variable the owner toggles, so R2-02 folds into OWNER-05 and R1-31 becomes R1's executive-summary PR.
The plan now has 133 tasks (127 agent PRs, 6 owner checkpoints) in 46 waves; the critical path is unchanged at 82
working days. Planning only: no production code changed.
