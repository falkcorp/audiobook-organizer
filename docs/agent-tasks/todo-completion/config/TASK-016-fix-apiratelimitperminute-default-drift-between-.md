<!-- file: docs/agent-tasks/todo-completion/config/TASK-016-fix-apiratelimitperminute-default-drift-between-.md -->
<!-- version: 1.0.0 -->
<!-- guid: d6848b2c-3539-45d5-80b4-db02c27b4e1b -->
<!-- last-edited: 2026-08-21 -->

# TASK-016 — Fix APIRateLimitPerMinute default drift between fresh-install (0) and ResetToDefaults/.env.example (100) (CFG-AUDIT)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · config subagent · **Why:** Single-value alignment across two or three constants, no logic change. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 1317 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**CFG-AUDIT**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/config-016-fix-apiratelimitperminute-default-drift-between-" -b agent/config-016-fix-apiratelimitperminute-default-drift-between- origin/main
cd "$REPO/.worktrees/config-016-fix-apiratelimitperminute-default-drift-between-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Pick one canonical default for APIRateLimitPerMinute and make the fresh-install viper default and ResetToDefaults() agree. Recommend 100 (matches .env.example and is the documented production-safe default) rather than 0 (unlimited), since 0 as a silent fresh-install default is the more dangerous direction to drift toward.

## Background (verify before editing)

- A fresh install today gets unlimited API rate (0), but a factory reset via the Settings UI gets 100 — the two paths a user might hit disagree on what 'default' means.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'viper.SetDefault("api_rate_limit_per_minute"' internal/config/config.go   # 1 hit at L1287, value 0 — fresh-install viper default is 0
  grep -n 'APIRateLimitPerMinute:   100' internal/config/config.go   # 1 hit ~L2332 — ResetToDefaults sets 100
  grep -n 'API_RATE_LIMIT_PER_MINUTE' .env.example   # 1 hit, value 100 — .env.example says 100
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In internal/config/config.go:1287, change `viper.SetDefault("api_rate_limit_per_minute", 0)` to `viper.SetDefault("api_rate_limit_per_minute", 100)`.
2. Confirm ResetToDefaults() at L2332 and .env.example:25 already agree at 100 — no change needed there.
3. Grep for any other place `api_rate_limit_per_minute` or `APIRateLimitPerMinute` sets a numeric default (e.g. config_test.go asserted defaults) and update if it hardcodes 0.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_config_016.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Existing installs that never touched this key currently get unlimited (0) from viper; changing the default to 100 changes THEIR effective behavior on next fresh boot with no persisted value. This is a real behavior change for any zero-config install currently relying on 0 — call this out in the changelog fragment.

## Tests

- internal/config/config_test.go — update or add an assertion that a fresh (no config file, no env, no db snapshot) load yields APIRateLimitPerMinute == 100.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `grep -n 'api_rate_limit_per_minute", 100' internal/config/config.go` returns 1 hit at the SetDefault call.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_config_016.md`.

## Commit message

```
fix(config): Fix APIRateLimitPerMinute default drift between fresh-instal (CFG-AUDIT)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Low risk, but the changelog fragment must say this changes default behavior for fresh installs, not just fixes a doc mismatch.
