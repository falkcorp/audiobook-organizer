<!-- file: docs/agent-tasks/platform-core/R2-02-end-r2-release-freeze.md -->
<!-- version: 1.1.5 -->
<!-- guid: ec2f7009-4e6e-4b0f-8a0b-df22dfc2d023 -->
<!-- last-edited: 2026-11-05 -->

# R2-02 — End the R2 freeze after the R2 release: check-rc-ordinal dispatches again

| Field | Value |
|---|---|
| ID | R2-02 |
| Release | R2 (closing PR) |
| Tier | **T0** (Haiku / low): one workflow toggle; a grep proves it |
| Wave | W44 (beside R3-02, R3-04; the three are file-disjoint) |
| Depends on | OWNER-05 (the owner has cut the R2 production release) |
| Spec | 09 §9.2.1 ("The release freezes": the freeze ends with the R2 release, which turns the dispatch on again) |
| Branch | `agent/platform-core-r2-02-end-r2-freeze` |

**Gate:** this PR merges **only after** the R2 production release exists. OWNER-05 is NOT AGENT WORK. Do not cut,
tag or promote anything yourself.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>                       # the audiobook-organizer checkout
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r2-02" -b agent/platform-core-r2-02-end-r2-freeze origin/main
cd "$REPO/.worktrees/platform-core-r2-02"
git rebase origin/main
```

## Goal

End the R2 freeze. The freeze toggle is the workflow-level variable `RELEASE_FREEZE` in
`.github/workflows/prerelease.yml` (the plan's name, plan §(l) item 3). R1-35 always added it, with both `if:`
conditions; its value was `'R1'` if the owner kept the R1 freeze and `''` if the owner overrode it (OWNER-01). R1-31
cleared it, or recorded a no-op when it was already `''`. F3-08 set it to `'R2'` before R2's first flip (F3-07)
merged. While it is non-empty, `check-rc-ordinal` dispatches no
production release (09 §9.2.1). This PR clears it, so promotion is on again. This is one line.

`ci/prefreeze_release.txt` (written by F3-08, embedded by F3-07) stays: the restore drill and `make rollback-restore` still read it.

**Precondition (do not skip).** OWNER-05 has cut the R2 production release. Check:

```bash
gh release list --exclude-pre-releases --exclude-drafts --limit 5
```

The newest non-prerelease entry must be the R2 release OWNER-05 recorded in the tracking issue. If it is not, STOP
and report "R2 not released yet"; do not open the PR.

## Files

| Action | Path |
|---|---|
| modify | `.github/workflows/prerelease.yml` (one line; header version bumped, `last-edited` updated) |
| create | one `changelog.d/` fragment (no file header) |

## Re-verify (expected at your base)

```bash
grep -n "RELEASE_FREEZE" .github/workflows/prerelease.yml
```

Expected: exactly 4 lines: the `env:` entry `  RELEASE_FREEZE: 'R2'`, the promote step's
`if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''`, the notice step's
`if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE != ''`, and the notice step's `echo`.

STOP and report what you found if the count is not 4, if the `env:` value is not exactly `'R2'`, or if
`RELEASE_FREEZE` does not appear at all (then R1-35 or F3-08 used another toggle: report it, do not improvise).

## Steps

1. In `.github/workflows/prerelease.yml`, replace the one line

   ```yaml
     RELEASE_FREEZE: 'R2'
   ```

   with

   ```yaml
     RELEASE_FREEZE: ''
   ```

   Keep the comment block above it unchanged. Change nothing else in the file (the two `if:` lines stay).
2. Bump the file header: `# version:` patch +1, `# last-edited:` today. Keep `# guid:` unchanged.
3. Add a headerless fragment `changelog.d/<today>_end_r2_freeze.md` with one line under a `### Changed` heading:
   `- Automatic minor-release promotion is on again after the R2 release (the R2 freeze ends).`
4. Commit: `ci(release): end the R2 freeze after the R2 release` with the trailer of the repo rules.

## Code contract

After the change the workflow `env:` block holds exactly `  RELEASE_FREEZE: ''`; the promote step runs when
`steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''`.

## Tests

No Go test. The checker is the acceptance block.

## Acceptance (commands and expected output)

```bash
grep -c "RELEASE_FREEZE: ''" .github/workflows/prerelease.yml          # expect 1
grep -c "RELEASE_FREEZE: 'R2'" .github/workflows/prerelease.yml        # expect 0
grep -c "env.RELEASE_FREEZE == ''" .github/workflows/prerelease.yml    # expect 1
python3 -c "import yaml;yaml.safe_load(open('.github/workflows/prerelease.yml'))" && echo YAML_OK   # expect YAML_OK
git diff --stat origin/main -- .github/workflows/prerelease.yml        # expect 1 file changed
make ci                                                                # expect exit 0 and "✅ All CI checks passed!"
```

## Out of scope

- `ci/prefreeze_release.txt` (stays).
- Cutting, tagging or promoting any release (OWNER-05).
- The R1 freeze (R1-35, R1-31) and any other job or workflow.

## Rollback

Revert the commit: the variable returns to `'R2'` and promotion stops again. No stored data is involved.

## Repo rules

- Worktree off fresh `origin/main`; never edit the primary checkout.
- Header bumped on every changed file (YAML uses `# file:`, `# version:`, `# guid:`, `# last-edited:`);
  `changelog.d/` fragments carry no header.
- Conventional commit ending with `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr create`, then `gh pr merge <n> --rebase` after every required check is green; never squash; never `--admin`.
- 09 names no release or flip trailer (09 §9.2.1); add none.
- Push workflow changes with `git push`, never through the MCP contents API (CLAUDE.md).

