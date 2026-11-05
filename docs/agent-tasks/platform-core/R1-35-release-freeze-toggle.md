<!-- file: docs/agent-tasks/platform-core/R1-35-release-freeze-toggle.md -->
<!-- version: 1.0.1 -->
<!-- guid: bf19256e-7fc8-4e37-8626-fe90279aff33 -->
<!-- last-edited: 2026-11-05 -->

# R1-35 — Release-freeze toggle: `RELEASE_FREEZE` and both `if:` conditions in prerelease.yml, value per OWNER-01, merged only when no release run is in flight

| Field | Value |
|---|---|
| ID | `R1-35` |
| Tier | **T1** (Sonnet / medium): one workflow toggle, but its merge timing guards production releases |
| Release / wave | R1 / W04 |
| Depends on | OWNER-01, OWNER-02 (each done before you start: an OWNER checkpoint recorded by the owner in the tracking issue) |
| Spec | 09 §9.2.1 ("The release freezes": the freeze starts with the PR that turns the dispatch off; the R1 freeze, owner-overridable), 09 §9.1 (`check-rc-ordinal` auto-dispatches the production release); 03 §3.8 (R1 and R2 each reach production as exactly one release) |
| Branch | `agent/platform-core-r1-35-release-freeze-toggle` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the
named required checks of 09 §9.2.1 that exist at your base. **This PR has a merge gate of its own (step 6): it
merges only when no `prerelease.yml` or `release-prod.yml` run is queued or in progress.**

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
at the planning base `f7ac5b11e` (HEAD of the plan checkout has the same `prerelease.yml`, header `version: 3.3.1`).
Re-run every grep at your base before editing.

## Goal

Add the one release-freeze mechanism the plan uses, in every case, before R1's first core PR (R1-01) merges.
`check-rc-ordinal` in `.github/workflows/prerelease.yml` dispatches `release-prod.yml` when a base version reaches 10
RCs (09 §9.1). The freeze stops that dispatch: RCs are still cut, none is promoted (09 §9.2.1).

- The toggle is the workflow-level variable `RELEASE_FREEZE`. This row adds it, the promote step's condition and a
  notice step. Later rows only change its value: R1-31 sets `''` after the R1 release, F3-08 sets `'R2'`, R2-02 sets
  `''` after the R2 release.
- The value comes from OWNER-01's recorded answer: **kept** gives `RELEASE_FREEZE: 'R1'`, **overridden** gives
  `RELEASE_FREEZE: ''` (09 §9.2.1: the R1 freeze is a lead extension the owner may override). The mechanism lands
  either way, because the R2 freeze (D12 (a), not overridable) must find it.
- Why the merge gate: a `workflow_dispatch` run builds the tip of `main` at dispatch time. A `prerelease.yml` run for
  an earlier commit has no freeze in its own workflow file; if it dispatches after this PR (and R1-01 after it) is on
  `main`, the production release would carry part of R1. So this PR merges only while no release run is in flight.

## Preconditions (STOP if any is missing)

1. OWNER-01 recorded the freeze answer in the tracking issue as one of the two lines
   `R1 freeze: kept (R1-35 sets RELEASE_FREEZE: 'R1')` or
   `R1 freeze: overridden (R1-35 sets RELEASE_FREEZE: '')`. Write down which. If neither line exists, STOP and report
   "OWNER-01 freeze answer missing". Do not guess.
2. OWNER-02 cut the R0 production release:

   ```bash
   gh release list --exclude-pre-releases --exclude-drafts --limit 3
   ```

   The newest non-prerelease entry is the R0 release OWNER-02 recorded in the tracking issue. If not, STOP and report
   "R0 not released yet".

In the steps below, `<VALUE>` is `'R1'` for **kept** and `''` for **overridden** (with the quotes, exactly as shown).

## Re-verify (expected at your base)

```bash
grep -c "RELEASE_FREEZE" .github/workflows/prerelease.yml                                  # expect 0
grep -n "^env:$" .github/workflows/prerelease.yml                                          # expect 1 line (28 at f7ac5b11e)
grep -n "GHCOMMON_SCRIPTS_DIR: " .github/workflows/prerelease.yml                          # expect 1 line (29), the only env: entry
grep -n "      - name: Promote to the next minor release" .github/workflows/prerelease.yml # expect 1 line (105)
grep -n "        if: steps.rc.outputs.at_threshold == 'true'$" .github/workflows/prerelease.yml   # expect 1 line (106)
grep -c "gh workflow run release-prod.yml" .github/workflows/prerelease.yml               # expect 1
```

If a count differs, STOP and report what you found. Do not improvise another toggle name.

## Steps

1. **The env entry.** In `.github/workflows/prerelease.yml`, directly after the line
   `  GHCOMMON_SCRIPTS_DIR: ${{ github.workspace }}/.github/workflows/scripts`, insert these lines (two-space indent,
   inside the workflow-level `env:` block):

   ```yaml
     # Release freeze (09 §9.2.1). Non-empty: check-rc-ordinal dispatches no production release.
     # R1-35 sets 'R1' (or '' when the owner overrode the R1 freeze) and R1-31 clears it after the R1 release;
     # F3-08 sets 'R2' and R2-02 clears it after the R2 release.
     RELEASE_FREEZE: <VALUE>
   ```

   so the last line reads exactly `  RELEASE_FREEZE: 'R1'` or `  RELEASE_FREEZE: ''`.
2. **The promote condition.** Replace the promote step's line

   ```yaml
           if: steps.rc.outputs.at_threshold == 'true'
   ```

   (the one directly under `      - name: Promote to the next minor release`) with

   ```yaml
           if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''
   ```
3. **The notice step.** Append, after the promote step's last line
   (`          echo "::notice::Dispatched a minor Production Release to reset the RC ordinal."`), at the end of the
   `check-rc-ordinal` job's `steps:` list, this step (six-space indent for `- name:`):

   ```yaml
         - name: Report the release freeze
           if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE != ''
           run: |
             echo "::notice::Release freeze ${RELEASE_FREEZE} is on (09 §9.2.1): not dispatching a production release."
   ```

   Nothing else in the workflow changes. RCs are still cut.
4. Bump the file header: `# version:` patch +1 (from `3.3.1` to `3.3.2` if no other row bumped it; otherwise +1 from
   what you find) and `# last-edited:` today.
5. Add a headerless fragment `changelog.d/<today>_release_freeze_toggle.md` with one line under a `### Changed`
   heading. For **kept**: `- Automatic minor-release promotion is paused until the R1 release (the R1 freeze).`
   For **overridden**: `- CI: added the release-freeze switch to the prerelease workflow (off; automatic minor-release promotion continues).`
   Commit: `ci(release): add the release-freeze toggle` with the trailer of the repo rules.
6. **Merge gate (run it last, immediately before merging).** Push, open the PR, and wait until every required check
   is green on an up-to-date branch. Then run, in this order:

   ```bash
   gh run list --workflow prerelease.yml --branch main --limit 50 --json status,headSha \
     --jq '[.[] | select(.status != "completed")] | length'        # expect 0
   gh run list --workflow release-prod.yml --limit 50 --json status,headSha \
     --jq '[.[] | select(.status != "completed")] | length'        # expect 0
   git fetch origin && git rev-list --count HEAD..origin/main     # expect 0 (branch is up to date)
   ```

   - All three print `0`: run `gh pr merge <n> --rebase` at once.
   - Either `gh run list` prints a number above `0`: do not merge. Every run on `main` is for a commit earlier than
     this PR. Wait for those runs to finish (`gh run watch <id>` on each, or re-run the two commands every 5 minutes),
     then repeat this step from the top.
   - The third prints a number above `0`: `git rebase origin/main`, push, wait for the checks, repeat this step.
7. **After the merge,** record the merge time, wait until every `prerelease.yml` run created before it has completed
   (a run that started before the merge dispatches `release-prod.yml` only at its end), then check that no production
   release started from an earlier run:

   ```bash
   M=$(gh pr view <n> --json mergedAt --jq .mergedAt)
   gh run list --workflow prerelease.yml --limit 50 --json status,createdAt \
     --jq "[.[] | select(.createdAt < \"$M\" and .status != \"completed\")] | length"   # repeat every 5 minutes until 0
   gh run list --workflow release-prod.yml --limit 5 --json createdAt,status,headSha,event
   ```

   Expected, once the first command prints `0`: no `release-prod.yml` run whose `createdAt` is later than `mergedAt`. If there is one, STOP: do not cancel
   it yourself. Post in the tracking issue at once: "a production release run started after R1-35 merged", with its
   run ID, for the lead and the owner. Then report the merge commit SHA and the `<VALUE>` in the tracking issue so
   R1-01 can start.

## Code contract

After the change the workflow-level `env:` block holds exactly one `RELEASE_FREEZE` entry. The promote step runs only
when `steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''`. The notice step runs only when
`steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE != ''`. The name `RELEASE_FREEZE` is fixed: R1-31,
F3-08 and R2-02 grep for it.

## Acceptance

```bash
grep -c "RELEASE_FREEZE" .github/workflows/prerelease.yml                                   # expect 4 (env entry, 2 if:, the notice's echo; the comment lines do not contain the name)
grep -c "^  RELEASE_FREEZE: 'R1'$" .github/workflows/prerelease.yml                         # expect 1 if kept, 0 if overridden
grep -c "^  RELEASE_FREEZE: ''$" .github/workflows/prerelease.yml                           # expect 0 if kept, 1 if overridden
grep -c "if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''$" .github/workflows/prerelease.yml   # expect 1
grep -c "if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE != ''$" .github/workflows/prerelease.yml   # expect 1
grep -c "if: steps.rc.outputs.at_threshold == 'true'$" .github/workflows/prerelease.yml     # expect 0 (no unguarded promote left)
grep -c "      - name: Report the release freeze" .github/workflows/prerelease.yml          # expect 1
python3 -c "import yaml;yaml.safe_load(open('.github/workflows/prerelease.yml'))" && echo YAML_OK   # expect YAML_OK
git diff --stat origin/main -- .github/workflows/                                           # expect 1 file changed (prerelease.yml)
make ci                                                                                     # expect exit 0 and "All CI checks passed"
```

The two `gh run list` commands of step 6 print `0` at merge time, and step 7, once every earlier `prerelease.yml` run
has completed, finds no `release-prod.yml` run created after the merge.

## STOP conditions

- OWNER-01's freeze answer or OWNER-02's R0 release is missing (Preconditions).
- A re-verify count differs, or `RELEASE_FREEZE` already exists at your base.
- Step 7 finds a production release run created after the merge.

## Out of scope

R1's first core PR (R1-01, which depends on this row and no longer edits `prerelease.yml`). Ending the R1 freeze
(R1-31). The R2 freeze (F3-08) and its end (R2-02). Any change to `release-prod.yml` or any other workflow. Cutting,
promoting or cancelling any release or run (OWNER work).

## Rollback

Revert the commit: `check-rc-ordinal` dispatches as before. If the R1 freeze must end early, R1-31's one-line change
(`RELEASE_FREEZE: ''`) is the rollback for the value; keep the mechanism, because F3-08 needs it.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then, after every required check is green on an up-to-date branch and step 6's gate prints `0` three times, `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **GitHub workflows.** Push workflow changes with `git push`, never through the MCP contents API (CLAUDE.md). Keep every action pinned to its SHA.
- **No `go.work`.** Never run `go work init` in a checkout or worktree (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
