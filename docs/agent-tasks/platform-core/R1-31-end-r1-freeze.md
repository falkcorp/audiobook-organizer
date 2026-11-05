<!-- file: docs/agent-tasks/platform-core/R1-31-end-r1-freeze.md -->
<!-- version: 1.0.0 -->
<!-- guid: f6be479f-8c49-424f-8d3e-ef33dbe233e8 -->
<!-- last-edited: 2026-11-05 -->

# R1-31 — End the R1 freeze after the R1 release: check-rc-ordinal dispatches again

| Field | Value |
|---|---|
| ID | `R1-31` |
| Tier | **T0** (Haiku / low): one workflow toggle |
| Release / wave | R1 / W22 |
| Depends on | OWNER-03 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 09 §9.2.1 (R1 freeze: the R1 release turns it off) |
| Branch | `agent/platform-core-r1-31-end-r1-freeze` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `.github/workflows/prerelease.yml`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-31" -b agent/platform-core-r1-31-end-r1-freeze origin/main
cd "$REPO/.worktrees/platform-core-r1-31"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

End the R1 freeze. R1-01 turned automatic promotion off by setting the workflow-level variable
`RELEASE_FREEZE` (name coined in R1-01's brief) in `.github/workflows/prerelease.yml`, so the
`check-rc-ordinal` job dispatches no production release. The R1 release turns it on again (09 §9.2.1:
"the R1 series' first PR turns it on and R1's release turns it off"; 09 §9.4 R1 Owner row). Between R1 and
F3, production minors ship as usual (09 §9.2.1). This is one line.

**Precondition (do not skip).** OWNER-03 has cut the R1 production release. This PR merges only after that
release exists (09 §9.2.1; plan §(d) W22: "the freeze-off PR merges only after its release is cut"). Check:

```bash
gh release list --exclude-pre-releases --limit 5
```

The newest non-prerelease entry must be the R1 release OWNER-03 recorded in the tracking issue. If it is
not, STOP and report "R1 not released yet"; do not open the PR.

## Re-verify (expected at your base, after R1-01 merged)

```bash
grep -n "RELEASE_FREEZE" .github/workflows/prerelease.yml
```

Expected: exactly 3 lines: the `env:` entry `  RELEASE_FREEZE: 'R1'`, the promote step's
`if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''` and the notice step's
`if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE != ''`. If the `env:` value is not
exactly `'R1'`, or the count is not 3, STOP and report what you found.

## Steps

1. In `.github/workflows/prerelease.yml`, replace the one line

   ```yaml
     RELEASE_FREEZE: 'R1'
   ```

   with

   ```yaml
     RELEASE_FREEZE: ''
   ```

   Keep the comment block above it unchanged. Change nothing else in the file (the two `if:` lines stay).
2. Bump the file header: `# version:` patch +1, `# last-edited:` today.
3. Add a headerless fragment `changelog.d/<today>_end_r1_freeze.md` with one line under a `### Changed`
   heading: `- Automatic minor-release promotion is on again after the R1 release (the R1 freeze ends).`
4. Commit: `ci(release): end the R1 freeze after the R1 release` with the trailer of the repo rules.

## Code contract

None.

## Acceptance

```bash
grep -c "RELEASE_FREEZE: ''" .github/workflows/prerelease.yml          # expect 1
grep -c "RELEASE_FREEZE: 'R1'" .github/workflows/prerelease.yml        # expect 0
grep -c "env.RELEASE_FREEZE == ''" .github/workflows/prerelease.yml    # expect 1
python3 -c "import yaml,sys;yaml.safe_load(open('.github/workflows/prerelease.yml'))" && echo YAML_OK   # expect YAML_OK
git diff --stat origin/main -- .github/workflows/prerelease.yml        # expect 1 file changed
make ci                                                                # expect exit 0 and "All CI checks passed"
```

## Out of scope

The R2 freeze (F3-07 sets the variable to `'R2'`; R2-02 clears it). Any other workflow. The release
itself (OWNER-03).

## Rollback

Revert the commit: the variable returns to `'R1'` and promotion stops again.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.

## Planner notes

- The freeze toggle's form is not fixed by the spec (09 §9.2.1 says only that `check-rc-ordinal` dispatches no
  production release). R1-01's brief coins the workflow-level variable `RELEASE_FREEZE` in
  `.github/workflows/prerelease.yml`; this brief, F3-07 (sets `'R2'`) and R2-02 (clears it) must use the same name.
