<!-- file: docs/agent-tasks/todo-completion/config/TASK-018-fix-chapterconsolidationthresholdmin-omitted-fro.md -->
<!-- version: 1.0.0 -->
<!-- guid: 170d7eaa-ea12-431e-b866-7fe82e6df2ae -->
<!-- last-edited: 2026-08-21 -->

# TASK-018 — Fix ChapterConsolidationThresholdMin omitted from ResetToDefaults (factory reset silently disables consolidation) (CFG-AUDIT)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · config subagent · **Why:** One missing field in a large struct literal — add it. · **Depends on:** none · **Wave:** 3

Source: `TODO.md` line 1317 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**CFG-AUDIT**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/config-018-fix-chapterconsolidationthresholdmin-omitted-fro" -b agent/config-018-fix-chapterconsolidationthresholdmin-omitted-fro origin/main
cd "$REPO/.worktrees/config-018-fix-chapterconsolidationthresholdmin-omitted-fro"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add `ChapterConsolidationThresholdMin: 10,` to the ResetToDefaults() struct literal in internal/config/config.go so a factory reset restores the documented default instead of silently disabling chapter consolidation.

## Background (verify before editing)

- (none)

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'Default 10. Set to 0 to disable' internal/config/config.go   # 1 hit at L762 — doc comment defines 0 as disabled, default as 10
  grep -n 'chapter_consolidation_threshold_min", 10' internal/config/config.go   # 1 hit at L1282 — fresh-install viper default is correctly 10
  awk '/^func ResetToDefaults/,/^}/' internal/config/config.go | grep -c ChapterConsolidation   # 0 — ResetToDefaults never sets this field
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In internal/config/config.go, inside `func ResetToDefaults()` (starts L2280), find the struct-literal block that also sets ScanProgressEvery / CoalesceShatteredSiblings (neighboring fields per the declaration order at ~L758-765) and add `ChapterConsolidationThresholdMin: 10,` alongside them, matching the surrounding field's formatting/alignment.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_config_018.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A user who deliberately set this to 0 (to disable consolidation) and then does a factory reset will now get consolidation re-enabled at 10 — this is the CORRECT behavior per the documented default, but worth a changelog note since it changes what 'reset' does for anyone currently relying on the buggy 0.

## Tests

- internal/config/config_test.go — add an assertion that after calling ResetToDefaults(), AppConfig.ChapterConsolidationThresholdMin == 10.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `awk '/^func ResetToDefaults/,/^}/' internal/config/config.go | grep -c ChapterConsolidationThresholdMin` returns 1.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_config_018.md`.

## Commit message

```
fix(config): Fix ChapterConsolidationThresholdMin omitted from ResetToDef (CFG-AUDIT)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Simplest, lowest-risk sub-item of the CFG-AUDIT triage — good haiku candidate.
