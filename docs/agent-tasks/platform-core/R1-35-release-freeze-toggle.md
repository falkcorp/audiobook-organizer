<!-- file: docs/agent-tasks/platform-core/R1-35-release-freeze-toggle.md -->
<!-- version: 1.1.0 -->
<!-- guid: bf19256e-7fc8-4e37-8626-fe90279aff33 -->
<!-- last-edited: 2026-11-05 -->

# R1-35 — Release-freeze switch: prerelease.yml's promote and notice steps read the repository variable `RELEASE_FREEZE` (D15), with a post-merge wait for earlier release runs

| Field | Value |
|---|---|
| ID | `R1-35` |
| Tier | **T1** (Sonnet / medium): one workflow edit, but its post-merge wait guards production releases |
| Release / wave | R1 / W04 |
| Depends on | OWNER-02 (done before you start: an OWNER checkpoint recorded by the owner in the tracking issue) |
| Spec | 09 §9.2.1 ("The release freezes": the R1 freeze, kept by the owner; the freeze is the owner's repository variable, D15, 02 §2.6), 09 §9.1 (`check-rc-ordinal` auto-dispatches the production release); 03 §3.8 (R1 and R2 each reach production as exactly one release) |
| Branch | `agent/platform-core-r1-35-release-freeze-toggle` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the
named required checks of 09 §9.2.1 that exist at your base. **After the merge it waits for every earlier release run
(step 5) before R1-01 may merge.**

**Files the inventory assigns to this task** (touch nothing outside this list without recording it under the PR's
"Scope notes"):

- `.github/workflows/prerelease.yml`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-35" -b agent/platform-core-r1-35-release-freeze-toggle origin/main
cd "$REPO/.worktrees/platform-core-r1-35"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1). Line numbers below were read
at the planning base `ac4368bdb` (HEAD of the plan checkout has the same `prerelease.yml`, header `version: 3.3.1`).
Re-run every grep at your base before editing.

## Goal

Add the one release-freeze switch the plan uses, before R1's first core PR (R1-01) merges. `check-rc-ordinal` in
`.github/workflows/prerelease.yml` dispatches `release-prod.yml` when a base version reaches 10 RCs (09 §9.1). The
freeze stops that dispatch: RCs are still cut, none is promoted (09 §9.2.1).

- The freeze is the **repository variable** `RELEASE_FREEZE` (D15, advisor S3/S5), read when each run executes. Its
  value is never committed. The owner sets it: `R1` at the end of OWNER-02, cleared in OWNER-03 after the R1 release,
  `R2` before F3-08, cleared in OWNER-05 after the R2 release. An unset variable reads as `''` (no freeze).
- This row adds the promote step's condition and a notice step. No later row edits the workflow for the freeze.
- Why the post-merge wait: a `prerelease.yml` run created before this merge runs the old workflow file, which ignores
  the variable. If it dispatched after R1-01 is on `main`, the production release would carry part of R1. So R1-01
  waits until every such run has completed (step 5).

## Preconditions (STOP if any is missing)

1. OWNER-02 cut the R0 production release:

   ```bash
   gh release list --exclude-pre-releases --exclude-drafts --limit 3
   ```

   The newest non-prerelease entry is the R0 release OWNER-02 recorded in the tracking issue. If not, STOP and report
   "R0 not released yet".

## Re-verify (expected at your base)

```bash
grep -c "RELEASE_FREEZE" .github/workflows/prerelease.yml                                  # expect 0
grep -n "      - name: Promote to the next minor release" .github/workflows/prerelease.yml # expect 1 line (105 at ac4368bdb)
grep -n "        if: steps.rc.outputs.at_threshold == 'true'$" .github/workflows/prerelease.yml   # expect 1 line (106)
grep -c "gh workflow run release-prod.yml" .github/workflows/prerelease.yml               # expect 1
```

If a count differs, STOP and report what you found. Do not improvise another name.

## Steps

1. **The promote condition.** Replace the promote step's line

   ```yaml
           if: steps.rc.outputs.at_threshold == 'true'
   ```

   (the one directly under `      - name: Promote to the next minor release`) with these lines

   ```yaml
           # Release freeze (09 §9.2.1; D15): the owner sets the repository variable RELEASE_FREEZE to R1 or R2.
           if: steps.rc.outputs.at_threshold == 'true' && vars.RELEASE_FREEZE == ''
   ```
2. **The notice step.** Append, after the promote step's last line
   (`          echo "::notice::Dispatched a minor Production Release to reset the RC ordinal."`), at the end of the
   `check-rc-ordinal` job's `steps:` list, this step (six-space indent for `- name:`):

   ```yaml
         - name: Report the release freeze
           if: steps.rc.outputs.at_threshold == 'true' && vars.RELEASE_FREEZE != ''
           run: |
             echo "::notice::Release freeze ${{ vars.RELEASE_FREEZE }} is on (09 §9.2.1): not dispatching a production release."
   ```

   Nothing else in the workflow changes. RCs are still cut.
3. Bump the file header: `# version:` patch +1 and `# last-edited:` today.
4. Add a headerless fragment `changelog.d/<today>_release_freeze_switch.md` with one line under a `### Changed`
   heading: `- CI: automatic minor-release promotion now honours the RELEASE_FREEZE repository variable (the release freezes).`
   Commit: `ci(release): add the release-freeze switch` with the trailer of the repo rules. Push, open the PR, merge
   once every required check is green (through the merge queue, D15).
5. **After the merge,** record the merge time and wait until every `prerelease.yml` run created before it has
   completed, then check that no production release started from an earlier run:

   ```bash
   M=$(gh pr view <n> --json mergedAt --jq .mergedAt)
   gh run list --workflow prerelease.yml --limit 50 --json status,createdAt \
     --jq "[.[] | select(.createdAt < \"$M\" and .status != \"completed\")] | length"   # repeat every 5 minutes until 0
   gh run list --workflow release-prod.yml --limit 5 --json createdAt,status,headSha,event
   gh variable get RELEASE_FREEZE                                                         # expect R1 (OWNER-02's last step)
   ```

   Expected, once the first command prints `0`: no `release-prod.yml` run whose `createdAt` is later than `mergedAt`,
   and the variable reads `R1`. If a release run started after the merge, STOP: do not cancel it yourself; post in the
   tracking issue at once with its run ID, for the lead and the owner. If the variable is not `R1`, report it to the
   lead (R1-01 waits; never set it yourself). Otherwise post the merge commit SHA in the tracking issue so R1-01 can
   start.

## Code contract

The promote step runs only when `steps.rc.outputs.at_threshold == 'true' && vars.RELEASE_FREEZE == ''`. The notice step
runs only when `steps.rc.outputs.at_threshold == 'true' && vars.RELEASE_FREEZE != ''`. The name `RELEASE_FREEZE` is
fixed: F3-08, F3-07 and the owner rows read it with `gh variable get RELEASE_FREEZE`.

## Acceptance

```bash
grep -c "vars.RELEASE_FREEZE" .github/workflows/prerelease.yml                              # expect 3 (2 if:, the notice's echo)
grep -c "if: steps.rc.outputs.at_threshold == 'true' && vars.RELEASE_FREEZE == ''$" .github/workflows/prerelease.yml   # expect 1
grep -c "if: steps.rc.outputs.at_threshold == 'true' && vars.RELEASE_FREEZE != ''$" .github/workflows/prerelease.yml   # expect 1
grep -c "if: steps.rc.outputs.at_threshold == 'true'$" .github/workflows/prerelease.yml     # expect 0 (no unguarded promote left)
grep -c "      - name: Report the release freeze" .github/workflows/prerelease.yml          # expect 1
python3 -c "import yaml;yaml.safe_load(open('.github/workflows/prerelease.yml'))" && echo YAML_OK   # expect YAML_OK
git diff --stat origin/main -- .github/workflows/                                           # expect 1 file changed (prerelease.yml)
make ci                                                                                     # expect exit 0 and "All CI checks passed"
```

Step 5, once every earlier `prerelease.yml` run has completed, finds no `release-prod.yml` run created after the merge.

## STOP conditions

- OWNER-02's R0 release is missing (Preconditions).
- A re-verify count differs, or `RELEASE_FREEZE` already appears at your base.
- Step 5 finds a production release run created after the merge.

## Out of scope

R1's first core PR (R1-01, which depends on this row and does not edit `prerelease.yml`). Setting or clearing the
variable (the owner's: OWNER-02, OWNER-03, before F3-08, OWNER-05). Any change to `release-prod.yml` or any other
workflow. Cutting, promoting or cancelling any release or run (OWNER work).

## Rollback

Revert the commit: `check-rc-ordinal` dispatches as before. To end a freeze early, the owner clears the variable;
keep the switch, because the R2 freeze needs it.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then, after every required check is green, `gh pr merge <n> --rebase` (through the merge queue). Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **GitHub workflows.** Push workflow changes with `git push`, never through the MCP contents API (CLAUDE.md). Keep every action pinned to its SHA.
- **No `go.work`.** Never run `go work init` in a checkout or worktree (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
