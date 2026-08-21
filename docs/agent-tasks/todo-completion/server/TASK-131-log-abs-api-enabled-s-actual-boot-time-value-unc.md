<!-- file: docs/agent-tasks/todo-completion/server/TASK-131-log-abs-api-enabled-s-actual-boot-time-value-unc.md -->
<!-- version: 1.0.0 -->
<!-- guid: 25ced942-981e-4d11-8f4a-7ae58b390856 -->
<!-- last-edited: 2026-08-21 -->

# TASK-131 — Log ABS_API_ENABLED's actual boot-time value unconditionally (currently silent when disabled) (N-11)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server subagent · **Why:** One log line added to an already-identified branch. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 90 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "⚙️ **Decide `ABS_API_ENABLED` for production (N-11" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-131-log-abs-api-enabled-s-actual-boot-time-value-unc" -b agent/server-131-log-abs-api-enabled-s-actual-boot-time-value-unc origin/main
cd "$REPO/.worktrees/server-131-log-abs-api-enabled-s-actual-boot-time-value-unc"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make wireABSRoutes log its enabled/disabled state unconditionally at boot (not just when enabled), so `journalctl`/log-grepping for 'abs:' on a running prod instance always answers the N-11 question definitively without needing repo access to deploy/local.conf.

## Background (verify before editing)

- This does not answer 'is ABS enabled in prod today' by itself — it only makes FUTURE boots self-report. The current-state question is a separate prod_run check (see notes).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'if !snap.ABSAPIEnabled' -A 3 internal/server/wire_abs_routes.go   # 1 hit ~L325, body is just `return` — the disabled branch returns silently with no log
  grep -n 'abs: Audiobookshelf-compatible surface enabled' internal/server/wire_abs_routes.go   # 1 hit ~L545 — the enabled branch does log
  grep -n 'abs_api_enabled.*false' internal/config/abs_config.go   # 1 hit ~L28 — abs_api_enabled defaults false and deploy/local.conf (where prod would set it) is gitignored, so the tree cannot answer the question
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In internal/server/wire_abs_routes.go's wireABSRoutes function, before the `if !snap.ABSAPIEnabled { return }` check, add: `slog.Info("abs: Audiobookshelf-compatible surface", "enabled", snap.ABSAPIEnabled)`.
2. Remove or keep the existing enabled-only log line at ~L545 (redundant once step 1 lands, but it carries more detail — e.g. condense both into the single new line if the extra detail is easy to include there, otherwise leave both).
3. Bump the file's version header.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_131.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- N/A — pure logging addition, no behavior change.

## Tests

- internal/server/wire_abs_routes_test.go: TestWireABSRoutes_LogsStateEvenWhenDisabled — capture slog output (or use a test logger handler) with ABSAPIEnabled=false, assert a log record with msg containing 'Audiobookshelf-compatible surface' and enabled=false is emitted.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test ./internal/server/... -run LogsStateEvenWhenDisabled -v` passes.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_131.md`.

## Commit message

```
feat(server): Log ABS_API_ENABLED's actual boot-time value unconditionally (N-11)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (``go test ./internal/server/... -run LogsStateEvenWhenDisabled -v` passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Separately, and NOT a code task: the owner should check the ACTUAL current prod value once this ships and a restart has happened, by grepping prod logs for the new line (see the server-logs skill / reference_prod_log_access.md in this repo's memory). Until then, prod state genuinely cannot be determined — this code change only fixes it going forward.
