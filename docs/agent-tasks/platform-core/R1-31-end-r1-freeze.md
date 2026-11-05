<!-- file: docs/agent-tasks/platform-core/R1-31-end-r1-freeze.md -->
<!-- version: 1.1.6 -->
<!-- guid: f6be479f-8c49-424f-8d3e-ef33dbe233e8 -->
<!-- last-edited: 2026-11-05 -->

# R1-31 — End the R1 freeze after the R1 release: check-rc-ordinal dispatches again

| Field | Value |
|---|---|
| ID | `R1-31` |
| Tier | **T0** (Haiku / low): one workflow toggle |
| Release / wave | R1 / W24 |
| Depends on | OWNER-03 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 09 §9.2.1 (R1 freeze: the R1 release turns it off) |
| Branch | `agent/platform-core-r1-31-end-r1-freeze` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `.github/workflows/prerelease.yml`

Also, outside the inventory's file list like the `changelog.d/` fragment (plan §(n) item 4: R1's closing row carries
them, so no R1 task PR edits them and siblings in a wave never collide): R1's executive summary
`docs/executive-summaries/<today>-r1-core-runtime-executive-summary.md` (create) and `TODO.md` (check-offs only).

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-31" -b agent/platform-core-r1-31-end-r1-freeze origin/main
cd "$REPO/.worktrees/platform-core-r1-31"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `ac4368bdb`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

End the R1 freeze. R1-35 turned automatic promotion off by setting the workflow-level variable
`RELEASE_FREEZE` (the plan's one toggle name: R1-35 adds it and sets it to `'R1'` if the owner kept the R1 freeze or to `''` if the owner overrode it, this row clears it, F3-08 sets `'R2'`, R2-02 clears it) in `.github/workflows/prerelease.yml`, so the
`check-rc-ordinal` job dispatches no production release. The R1 release turns it on again (09 §9.2.1:
"the R1 series' first PR turns it on and R1's release turns it off"; 09 §9.4 R1 Owner row). Between R1 and
F3, production minors ship as usual (09 §9.2.1). This is one line. **If the owner overrode the R1 freeze, R1-35 already left the value at `''`: this row is then a recorded no-op** (see the Re-verify and Steps below).

**Precondition (do not skip).** OWNER-03 has cut the R1 production release. This PR merges only after that
release exists (09 §9.2.1; plan §(d) W24: "the freeze-off PR merges only after its release is cut"). Check:

```bash
gh release list --exclude-pre-releases --exclude-drafts --limit 5   # a draft is not a cut release
```

The newest non-prerelease entry must be the R1 release OWNER-03 recorded in the tracking issue. If it is
not, STOP and report "R1 not released yet"; do not open the PR.

## Re-verify (expected at your base, after R1-35 merged)

```bash
grep -n "RELEASE_FREEZE" .github/workflows/prerelease.yml
```

Expected: exactly 4 lines: the `env:` entry, the promote step's
`if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''`, the notice step's
`if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE != ''` and the notice step's `echo` (it names the variable too). The `env:` entry is `  RELEASE_FREEZE: 'R1'` when the owner kept the R1 freeze, and `  RELEASE_FREEZE: ''` when the owner overrode it (R1-35 always adds all four lines). If the count is not 4, or the `env:` value is neither of those two, STOP and report what you found.

**Recorded no-op.** If the `env:` value is already `''`, the workflow edit is a no-op. Record in the tracking issue "R1-31: no-op, the R1 freeze was overridden and R1-35 left `RELEASE_FREEZE: ''`". Steps 1-3 and the `RELEASE_FREEZE` acceptance lines apply only when the value is `'R1'`; steps 4-5 (R1's executive summary and TODO check-offs) apply either way, so the PR is then opened with only those files and the fragment, and `prerelease.yml` is not touched.

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
   heading: `- Automatic minor-release promotion is on again after the R1 release (the R1 freeze ends).` (In the
   no-op case, instead: `- R1 executive summary and TODO check-offs.`)
4. **R1's executive summary** (CLAUDE.md "Post-Task Hygiene"; plan §(n) item 4). Create
   `docs/executive-summaries/<today>-r1-core-runtime-executive-summary.md` with the 4-line header, following the
   name pattern and rules of `docs/process/executive-summaries.md`. Plain language, no file paths or function names:
   what R1 changed for the user and why (the core runtime underneath, with no change to stored data; the first
   checkpoint; the R1 release note's user-visible lines, which OWNER-03 prepended to the R1 release), and that
   automatic promotion is on again. Take the user-visible facts from the R1 release's notes
   (`gh release view <R1 tag>`), not from memory.
5. **TODO check-offs.** For every R1 row (R1-01 … R1-37) and the R0 rows, grep `TODO.md` for its ID and title and
   check off each open entry it finished, by hand (CLAUDE.md: the daily assembly may already have folded a
   `todo.d` fragment in, so grep `TODO.md`, not `todo.d/`). Do not add new entries.
6. Commit: `ci(release): end the R1 freeze after the R1 release` (no-op case: `docs(release): R1 executive summary and TODO check-offs`) with the trailer of the repo rules.

## Code contract

None.

## Acceptance

```bash
grep -c "RELEASE_FREEZE: ''" .github/workflows/prerelease.yml          # expect 1
grep -c "RELEASE_FREEZE: 'R1'" .github/workflows/prerelease.yml        # expect 0
grep -c "env.RELEASE_FREEZE == ''" .github/workflows/prerelease.yml    # expect 1
python3 -c "import yaml,sys;yaml.safe_load(open('.github/workflows/prerelease.yml'))" && echo YAML_OK   # expect YAML_OK
git diff --stat origin/main -- .github/workflows/prerelease.yml        # expect 1 file changed (0 in the no-op case)
ls docs/executive-summaries/ | grep -c -- '-r1-core-runtime-executive-summary\.md$'   # expect 1
git diff origin/main -- TODO.md | grep -c '^+.*\[x\]'                   # expect >= 1 (R1 entries checked off)
make ci                                                                # expect exit 0 and "All CI checks passed"
```

## Out of scope

The R2 freeze (F3-08 sets the variable to `'R2'`; R2-02 clears it). Adding the toggle (R1-35). Any other workflow. The release
itself (OWNER-03). R2's and R3's executive summaries (R2-01, R3-04).

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
