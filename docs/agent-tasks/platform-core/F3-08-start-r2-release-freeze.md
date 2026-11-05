<!-- file: docs/agent-tasks/platform-core/F3-08-start-r2-release-freeze.md -->
<!-- version: 1.0.0 -->
<!-- guid: 359f047e-e7d4-4d0b-942d-47cb2c7363b7 -->
<!-- last-edited: 2026-11-05 -->

# F3-08 — Start the R2 freeze: `RELEASE_FREEZE: 'R2'` and `ci/prefreeze_release.txt`, merged only when no release run is in flight and the newest production release equals the recorded tag

| Field | Value |
|---|---|
| ID | `F3-08` |
| Tier | **T1** (Sonnet / medium): one workflow value and a recorded tag, merged only when no release run is in flight |
| Release / wave | R2, before the config flip F3 / W31 |
| Depends on | R1-31, F2-17, F2-01, F2-02, F2-03, F2-04, F2-05, F2-06, F2-07, F2-08, F2-09, F2-10, F2-11, F2-12, F2-13, F2-14, F2-15, F2-16 (each merged on `main` before you start) |
| Spec | 09 §9.2.1 ("The release freezes": from the merge of R2's first flip PR until the R2 release; the freeze starts with the PR that turns the dispatch off; "The pre-freeze release"), 09 §9.3 ("Order": F2's foundation PRs land dark before the flips and are outside the freeze), D12 (a) (02 §2.6); 05-storage §5S.3.12 ("How": the checkpoint record's `from_release` is the pre-freeze tag the cutover build embeds) |
| Branch | `agent/platform-core-f3-08-start-r2-freeze` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the
named required checks of 09 §9.2.1. **This PR has a merge gate of its own (step 6).** It merges only when no
`prerelease.yml` or `release-prod.yml` run is queued or in progress, and only while the newest production release
still equals the tag this PR records. F3-07 (the config flip) depends on this row.

**Files the inventory assigns to this task** (touch nothing outside this list without recording it under the PR's
"Scope notes"):

- `.github/workflows/prerelease.yml`
- `ci/prefreeze_release.txt` (create)

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-f3-08" -b agent/platform-core-f3-08-start-r2-freeze origin/main
cd "$REPO/.worktrees/platform-core-f3-08"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1). Re-run every grep at your base
before editing.

## Goal

Turn the R2 freeze on before the first R2 flip (F3-07) merges, and record the pre-freeze release.

- From this merge until the R2 release, `check-rc-ordinal` dispatches no production release, so `releases/latest`
  and the `:latest` image stay on the pre-freeze release and no install runs a half-flipped `main` (09 §9.2.1;
  D12 (a), not overridable).
- **The pre-freeze release** is the last production release before F3 merges (09 §9.2.1). The cutover build embeds
  its tag as the checkpoint record's `from_release` (05-storage §5S.3.12 "How"), and `make rollback-restore` names it.
  With the freeze on, no production release can be cut between this merge and R2, so the tag this PR records stays
  exact (09 §9.2.1).
- F2's foundation PRs land dark before the freeze and are outside it (09 §9.3), so every F2 row merges first.
- F3-07 embeds `ci/prefreeze_release.txt` and, before its own merge, re-checks that the newest production release
  still equals it. This row writes the file; F3-07 does not edit it or `prerelease.yml`.

## Preconditions (STOP if any is missing)

1. Every row in **Depends on** is merged. Confirm each in the tracking issue. If one is not, STOP and report which.
2. R1-31 left the R1 freeze off (it set the value to `''`, or recorded a no-op because R1-35 already set `''`).

## Re-verify (expected at your base)

```bash
grep -c "RELEASE_FREEZE" .github/workflows/prerelease.yml                                   # expect 4
grep -n "^  RELEASE_FREEZE: ''$" .github/workflows/prerelease.yml                           # expect 1 line
grep -c "^  RELEASE_FREEZE: 'R1'$\|^  RELEASE_FREEZE: 'R2'$" .github/workflows/prerelease.yml   # expect 0
grep -c "if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''$" .github/workflows/prerelease.yml   # expect 1
grep -c "if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE != ''$" .github/workflows/prerelease.yml   # expect 1
test ! -e ci/prefreeze_release.txt && echo NO_FILE_YET                                       # expect NO_FILE_YET
ls ci/                                                                                       # expect gates.txt (and no prefreeze_release.txt)
```

The 4 `RELEASE_FREEZE` lines are R1-35's: the `env:` entry, the two `if:` lines and the notice step's `echo`. If the
`env:` value is not exactly `''`, if `RELEASE_FREEZE` does not appear at all, or if `ci/prefreeze_release.txt`
already exists, STOP and report what you found. Do not add a second toggle.

## Steps

1. **Read the pre-freeze tag.**

   ```bash
   TAG="$(gh release list --exclude-pre-releases --limit 1 --json tagName --jq '.[0].tagName')"
   echo "$TAG"
   ```

   Expected: one tag of the form `v<major>.<minor>.<patch>` (for example `v0.230.0`), matching
   `^v[0-9]+\.[0-9]+\.[0-9]+$`. If it is empty or has another shape, STOP and report it.
2. **Create `ci/prefreeze_release.txt`** with exactly these six lines, `<TAG>` replaced by the value of step 1, and a
   trailing newline:

   ```text
   # file: ci/prefreeze_release.txt
   # version: 1.0.0
   # guid: <fresh guid from python3 -c 'import uuid;print(uuid.uuid4())'>
   # last-edited: <today, YYYY-MM-DD>
   # The pre-freeze release (09 §9.2.1): the last production release before F3 merged. One tag line; lines starting with '#' are comments.
   <TAG>
   ```

   The file has exactly one line that is neither blank nor starts with `#`, and that line is the tag.
3. **Turn the freeze on.** In `.github/workflows/prerelease.yml`, replace the one line

   ```yaml
     RELEASE_FREEZE: ''
   ```

   with

   ```yaml
     RELEASE_FREEZE: 'R2'
   ```

   Keep the comment block above it and both `if:` lines unchanged. Change nothing else in the file.
4. Bump `prerelease.yml`'s header: `# version:` patch +1, `# last-edited:` today.
5. Add a headerless fragment `changelog.d/<today>_start_r2_freeze.md` with one line under a `### Changed` heading:
   `- Automatic minor-release promotion is paused until the R2 release (the R2 freeze); the pre-freeze release is recorded in ci/prefreeze_release.txt.`
   Commit: `ci(release): start the R2 freeze and record the pre-freeze release` with the trailer of the repo rules.
6. **Merge gate (run it last, immediately before merging).** Push, open the PR, and wait until every required check is
   green on an up-to-date branch. Then run, in this order:

   ```bash
   gh run list --workflow prerelease.yml --branch main --limit 50 --json status,headSha \
     --jq '[.[] | select(.status != "completed")] | length'        # expect 0
   gh run list --workflow release-prod.yml --limit 50 --json status,headSha \
     --jq '[.[] | select(.status != "completed")] | length'        # expect 0
   test "$(gh release list --exclude-pre-releases --limit 1 --json tagName --jq '.[0].tagName')" = \
        "$(grep -v '^#' ci/prefreeze_release.txt | grep -v '^$')" && echo TAG_OK   # expect TAG_OK
   git fetch origin && git rev-list --count HEAD..origin/main     # expect 0 (branch is up to date)
   ```

   - The two `gh run list` commands print `0`, the tag check prints `TAG_OK` and the last command prints `0`: run
     `gh pr merge <n> --rebase` at once.
   - Either `gh run list` prints a number above `0`: do not merge. Every run on `main` is for a commit earlier than
     this PR. Wait for those runs to finish (`gh run watch <id>`, or re-run the commands every 5 minutes), then repeat
     this step from the top. A `release-prod.yml` run that finishes may publish a new production release; the tag
     check then catches it.
   - The tag check does not print `TAG_OK`: a production release was published after step 1. Redo steps 1 and 2 with
     the new tag (amend or add a commit), push, wait for the checks, and repeat this step.
   - The last command prints a number above `0`: `git rebase origin/main`, push, wait for the checks, repeat this
     step.
7. **After the merge,** check that no production release started after it:

   ```bash
   gh pr view <n> --json mergedAt --jq .mergedAt
   gh run list --workflow release-prod.yml --limit 5 --json createdAt,status,headSha,event
   gh release list --exclude-pre-releases --limit 1 --json tagName --jq '.[0].tagName'   # expect the tag in ci/prefreeze_release.txt
   ```

   Expected: no `release-prod.yml` run whose `createdAt` is later than `mergedAt`, and the newest production release is
   still the recorded tag. If either fails, STOP: do not cancel or delete anything yourself. Post in the tracking issue
   at once, with the run ID or the new tag, for the lead and the owner (F3-07 must not merge until they decide). Then
   record the merge commit SHA and the pre-freeze tag in the tracking issue so F3-07 can start.

## Code contract

- `.github/workflows/prerelease.yml`: the workflow-level `env:` block holds exactly `  RELEASE_FREEZE: 'R2'`; the
  promote step's condition `steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''` is now false at every
  threshold, and the notice step runs instead.
- `ci/prefreeze_release.txt`: the four-line `#` header, one `#` comment line and one tag line. A reader takes the only
  line that is neither blank nor starts with `#` (F3-07's embed; the restore drill and `make rollback-restore` read
  the same tag, 09 §9.2.1).

## Acceptance

```bash
grep -c "^  RELEASE_FREEZE: 'R2'$" .github/workflows/prerelease.yml                         # expect 1
grep -c "^  RELEASE_FREEZE: ''$" .github/workflows/prerelease.yml                           # expect 0
grep -c "RELEASE_FREEZE" .github/workflows/prerelease.yml                                   # expect 4
grep -c "if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''$" .github/workflows/prerelease.yml   # expect 1
python3 -c "import yaml;yaml.safe_load(open('.github/workflows/prerelease.yml'))" && echo YAML_OK   # expect YAML_OK
grep -v '^#' ci/prefreeze_release.txt | grep -v '^$' | wc -l                                 # expect 1
grep -v '^#' ci/prefreeze_release.txt | grep -v '^$' | grep -cE '^v[0-9]+\.[0-9]+\.[0-9]+$'  # expect 1
head -1 ci/prefreeze_release.txt                                                            # expect "# file: ci/prefreeze_release.txt"
git diff --stat origin/main -- .github/ ci/                                                 # expect 2 files changed (prerelease.yml, prefreeze_release.txt)
make ci                                                                                     # expect exit 0 and "All CI checks passed"
```

At merge time step 6's two `gh run list` commands print `0` and its tag check prints `TAG_OK`; step 7 finds no
`release-prod.yml` run created after the merge.

## STOP conditions

- A dependency is not merged, or R1-31 did not leave the value `''` (Preconditions, Re-verify).
- Step 1's tag is empty or not `v<major>.<minor>.<patch>`.
- Step 7 finds a production release run created after the merge, or a newer production release.

## Out of scope

- The config flip (F3-07), which embeds the file and re-checks the tag before its own merge.
- Ending the R2 freeze (R2-02). The R1 freeze (R1-35, R1-31).
- `release-prod.yml`: the optional belt-and-braces of making it refuse an automated dispatch while the tree is frozen
  is not taken, because `release-prod.yml` is not in this row's files.
- Cutting, promoting, cancelling or deleting any release or run (OWNER work).

## Rollback

Before F3-07 merges: revert the commit (the value returns to `''`, the file goes). After F3-07 merges, the freeze must
stay on until R2 ships (D12 (a)); a revert is a decision for the lead.

## Planner notes

- `ci/prefreeze_release.txt` gets the repo's four-line `#` header (like `ci/gates.txt`), so it is not a bare tag. F3-07's
  embed must read the only non-blank line that does not start with `#`, not the whole file. F3-07's brief says only
  "embed it"; it should name this rule.
- R2-02's re-verify expects "exactly 3" `RELEASE_FREEZE` lines; the count R1-35 produces is 4 (the notice step's `echo`
  names the variable too).

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then, after every required check is green on an up-to-date branch and step 6's gate passes, `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **GitHub workflows.** Push workflow changes with `git push`, never through the MCP contents API (CLAUDE.md). Keep every action pinned to its SHA.
- **No `go.work`.** Never run `go work init` in a checkout or worktree (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
