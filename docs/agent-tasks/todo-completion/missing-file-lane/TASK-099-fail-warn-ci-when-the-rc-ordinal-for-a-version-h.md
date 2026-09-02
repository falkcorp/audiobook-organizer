<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-099-fail-warn-ci-when-the-rc-ordinal-for-a-version-h.md -->
<!-- version: 1.1.0 -->
<!-- guid: 60c1333c-9bec-4933-b62d-ba33672b1ebd -->
<!-- last-edited: 2026-09-02 -->

# TASK-099 — Fail/warn CI when the RC ordinal for a version hits 10 (TODO.md L8044)

> **Status 2026-09-02:** ✅ DONE — PR #2742 merged 2026-08-23 (d16bddcd7).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** one new step in an existing thin wrapper workflow, using a gh CLI pattern already used elsewhere in this repo's workflows · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 8044 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Never accumulate more than 10 RCs on a version —" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-099-fail-warn-ci-when-the-rc-ordinal-for-a-version-h" -b agent/missing-file-lane-099-fail-warn-ci-when-the-rc-ordinal-for-a-version-h origin/main
cd "$REPO/.worktrees/missing-file-lane-099-fail-warn-ci-when-the-rc-ordinal-for-a-version-h"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a step to .github/workflows/prerelease.yml, running after the reusable prerelease job, that counts how many -rc.N prereleases exist for the current base version (via gh release list) and fails the job when the count reaches 10, per the owner's 2026-08-08 'never above 10 RCs' directive.

## Background (verify before editing)

- prerelease.yml (50 lines) is a thin wrapper that calls falkcorp/github-common's reusable-release.yml at L31 — there is nowhere in THIS repo today that inspects the RC ordinal it just minted.
- cleanup-rc-releases.yml already prunes superseded RCs to the latest 3 on stable promotion — the 'clean up on promotion' half of this TODO item is already done; only the threshold-enforcement half remains.
- The duplicate-draft bug mentioned in the item ('three identical broken drafts for v0.217.9') was fixed by pinning .github/ghcommon-ref.txt per the item's own text; verified that pin is still in place (SHA d0c3326b96557c8ea9117c1c196b628e5e028186) and matches prerelease.yml's uses: line.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'rc\.' .github/workflows/prerelease.yml   # 0 hits — prerelease.yml has no RC-ordinal counting logic
  grep -n 'on:' -A3 .github/workflows/cleanup-rc-releases.yml; grep -n 'Keep latest 3 RCs' .github/workflows/cleanup-rc-releases.yml   # release: types: [published]; and a 'Keep latest 3 RCs for released version' job name — cleanup-rc-releases.yml already runs on stable release publish and keeps only the latest 3 RCs
  cat .github/ghcommon-ref.txt; grep -n 'uses:' .github/workflows/prerelease.yml   # both show the same SHA d0c3326b96557c8ea9117c1c196b628e5e028186 — .github/ghcommon-ref.txt still pins a fixed SHA matching prerelease.yml's uses: line (the fix for the duplicate-draft bug the item names)
  ```

### Reuse — don't invent

- Use `gh release list (RC tag pattern already used for counting/pruning)` in `.github/workflows/cleanup-rc-releases.yml` (verify: `grep -n 'gh release list' .github/workflows/cleanup-rc-releases.yml`) — do NOT write a parallel helper.

## Step-by-step

1. Confirm .github/ghcommon-ref.txt's SHA still matches prerelease.yml's `uses:` pin (both currently d0c3326b96557c8ea9117c1c196b628e5e028186) — if they've drifted apart by the time this is picked up, flag that separately rather than silently 'fixing' it as part of this task.
2. Add a new job (or a final step in the existing `prerelease` job, `needs: prerelease`) to .github/workflows/prerelease.yml that runs after the reusable workflow completes.
3. In that step, use `gh release list --repo ${{ github.repository }} --limit 200 --json tagName,isPrerelease` (same pattern as cleanup-rc-releases.yml) to list releases, strip the `-rc.N` suffix to get the base version for the tag the reusable workflow just minted, and count how many `-rc.*` releases share that base.
4. If the count is >= 10, fail the job with `exit 1` and a message naming the base version and instructing to cut a stable release instead of another RC.
5. Bump the file-header version and last-edited date on prerelease.yml.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_099.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- First RC of a brand-new base version (0 existing RCs) must not trip the >=10 check.
- A `gh release list` result spanning multiple unrelated base versions must only count RCs matching the CURRENT base version, not the grand total.

## Tests

- No Go/Vitest unit test applies to a GitHub Actions workflow directly; validate the counting logic by dry-running the new step's shell against a captured `gh release list` JSON fixture containing 10+ fake -rc entries for one base version and fewer for another, confirming only the matching base is counted.

Anti-over-suppression test: `N/A — this is a threshold check, not a filter/skip; the failure-direction to guard is under-counting (missing RCs for the current base), covered by the multi-base-version edge case above.` — a known-good input still passes with the new guard active.

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `git diff --check` exits 0 (run it as its own command, not chained before an echo).
- [ ] `grep -L 'last-edited: ' .github/workflows/prerelease.yml` prints nothing (the header is present and bumped) — verified the file has a header block at HEAD: `head -6 .github/workflows/prerelease.yml` shows `# file:`/`# version: 2.10.4`/`# last-edited: 2026-07-06`.
- [ ] `grep -n 'rc' .github/workflows/prerelease.yml` matches the new counting step.
- [ ] `bash .github/scripts/check-rc-ordinal.sh testdata/gh-release-list-10rc.json v0.217` exits 1 and names the base version; the same script against testdata/gh-release-list-1rc.json exits 0 (the anti-vacuous case).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_099.md`.
- [ ] Anti-over-suppression test: `N/A — this is a threshold check, not a filter/skip; the failure-direction to guard is under-counting (missing RCs for the current base), covered by the multi-base-version edge case above.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_099.md`.

## Commit message

```
feat(missing-file-lane): Fail/warn CI when the RC ordinal for a version hits 10 (TODO L8044)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — ``grep -L 'last-edited: ' .github/workflows/prerelease.yml` prints nothing (the header is present and bumped) — verified the file has a header block at HEAD: `head -6 .github/workflows/prerelease.yml` shows `# file:`/`# version: 2.10.4`/`# last-edited: 2026-07-06`.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Owner also floated 'consider auto-promoting' — that's a bigger, more consequential decision (auto-cutting a stable release with no human gate) and should be a separate needs_design conversation with the owner, not bundled into this fail-loud CI check.
