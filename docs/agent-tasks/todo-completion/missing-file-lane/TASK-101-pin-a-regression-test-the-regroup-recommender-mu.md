<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-101-pin-a-regression-test-the-regroup-recommender-mu.md -->
<!-- version: 1.0.0 -->
<!-- guid: e87bf32c-0400-4843-bc75-df34428f7a2a -->
<!-- last-edited: 2026-08-21 -->

# TASK-101 — Pin a regression test: the regroup recommender must not default to duplicate-of on equal-runtime alone, using the 3 real multidisc holds as fixtures (TODO.md L8245)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** small, targeted regression test using three concrete real IDs; needs enough regroup-domain context to construct a realistic hold fixture, so not pure-mechanical · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 8245 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**The 3 dangerous multidisc holds are DUPLICATES, " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-101-pin-a-regression-test-the-regroup-recommender-mu" -b agent/missing-file-lane-101-pin-a-regression-test-the-regroup-recommender-mu origin/main
cd "$REPO/.worktrees/missing-file-lane-101-pin-a-regression-test-the-regroup-recommender-mu"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a regression test using the three real production holds (01KXF8BNKENR530AKMMKJYD5E1 'Brother Wulf' 6.30h pair, 01KXF8BNKACGA6ZAEBPCQK09FX 'Sevenfold Sword' 20.56h/21.47h pair, 01KXF8BNHY7AE56CPZWY9VW9VF 'The Warring Son' 11.77h/11.77h pair) asserting the recommender's current default verdict for two-member, near-identical-runtime multidisc holds is 'separate', not 'duplicate-of' — locking in today's safe default against future tuning drift, since duplicate-of hard-deletes the absorbed row.

## Background (verify before editing)

- The item is explicit that tuning the recommender toward duplicate-of on runtime similarity would be wrong — two different books can share a runtime, and duplicate-of destroys data via ApplyDuplicateOf's hard delete.
- This test does not require the never-delete-re-associate feature (L8943) to exist first — it only pins TODAY's recommender behavior against real fixture data, independent of that larger unbuilt work.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'recommend\|Recommend' internal/plugins/maintenance/regroup_shattered_ai.go   # ≥1 hit — the recommender's decision logic is in regroup_shattered_ai.go
  grep -n 'duplicate-of\|separate' internal/plugins/maintenance/regroup_apply.go | head -6   # ≥2 hits, including the L18-24 comment describing duplicate-of as a merge that absorbs debris — regroup_apply.go documents the verdict vocabulary (separate/duplicate-of/combine/insufficient-evidence) and that duplicate-of hard-deletes the absorbed row
  ```

### Reuse — don't invent

- Use `regroup_apply.go's duplicate-of/separate verdict shapes (for fixture construction reference)` in `internal/plugins/maintenance/regroup_apply.go` (verify: `grep -n 'duplicate-of\|separate' internal/plugins/maintenance/regroup_apply.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read internal/plugins/maintenance/regroup_shattered_ai.go to find the recommender's entry point and its input/output shape (what a 'hold' with member books looks like, and the exact verdict constant names — confirm before writing assertions, do not guess the string 'separate').
2. Construct three test fixtures matching the three real holds' shape: two member books each, with the given titles and durations (convert hours to DurationSec, e.g. 6.30h = 22680s).
3. Write TestRecommend_EqualRuntimeMultidiscHolds_DefaultToSeparate asserting the recommender's verdict for each of the three fixtures matches the non-duplicate-of default.
4. Add a companion fixture with a much larger duration gap (e.g. 3h vs 20h) proving the test isn't vacuously true — the recommender must be able to emit something different when the evidence differs, so an always-returns-separate stub doesn't pass by accident.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_101.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the recommender needs more input than title+duration (e.g. AcoustID fingerprints, usually absent for these three per the item's data), fixtures must set those fields explicitly empty/nil rather than omitting them, matching this codebase's 'absent evidence means insufficient-evidence, never refuted' rule applied elsewhere.

## Tests

- TestRecommend_EqualRuntimeMultidiscHolds_DefaultToSeparate — the three real fixtures, asserts verdict == separate (or the confirmed equivalent constant) for all three.
- TestRecommend_DistinctRuntimes_ReturnsDifferentDefaultThanEqualRuntimes — companion case proving the test isn't vacuous.

Anti-over-suppression test: `TestRecommend_DistinctRuntimes_ReturnsDifferentDefaultThanEqualRuntimes` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TestRecommend_EqualRuntimeMultidiscHolds passes.
- [ ] Anti-over-suppression test: `TestRecommend_DistinctRuntimes_ReturnsDifferentDefaultThanEqualRuntimes` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_101.md`.

## Commit message

```
fix(missing-file-lane): Pin a regression test: the regroup recommender must not defa (TODO L8245)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/plugins/maintenance/... -run TestRecommend_EqualRuntimeMultidiscHolds passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is a narrow slice; the item's real ask ('feed them to the duplicate-detection track') is the still-unbuilt L8943 (never-delete-re-associate) — this test just locks in today's safe default so it can't silently drift while that larger work is pending.
