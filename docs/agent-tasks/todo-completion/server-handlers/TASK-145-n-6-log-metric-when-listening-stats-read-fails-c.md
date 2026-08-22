<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-145-n-6-log-metric-when-listening-stats-read-fails-c.md -->
<!-- version: 1.0.0 -->
<!-- guid: 22723df6-67cb-4607-a914-47f08e27775d -->
<!-- last-edited: 2026-08-21 -->

# TASK-145 — N-6: log + metric when listening-stats read fails (currently silent 0) (ABS-N6)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server-handlers subagent · **Why:** Small, localized addition of a log line and a metric increment inside an existing error branch — mechanical once an existing metrics helper pattern is located to copy. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 53 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🔌 **ABS coverage gaps N-1 … N-10** (audit:" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-145-n-6-log-metric-when-listening-stats-read-fails-c" -b agent/server-handlers-145-n-6-log-metric-when-listening-stats-read-fails-c origin/main
cd "$REPO/.worktrees/server-handlers-145-n-6-log-metric-when-listening-stats-read-fails-c"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Keep the 200/total=0 response on a listening-stats read failure (deliberately, per the existing comment — do not change the status code), but add slog.Warn(...) with the user ID and error, and increment a Prometheus counter, so the silent failure becomes observable without changing client-visible behavior.

## Background (verify before editing)

- Confirmed via CLAUDE.md's own MEMORY.md note ('Prometheus gap — bare /metrics 401 is BY DESIGN') that this repo does have a metrics surface; find its existing helper conventions before adding a new one from scratch.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'ListenedSeconds\|slog.Warn\|metric' internal/server/handlers/abs/stats.go   # ListenedSeconds hit inside an if err == nil branch; no slog.Warn/metric hit anywhere in the file — ListeningStats swallows the ListenedSeconds error with no logging or metric
  ```

### Reuse — don't invent

- Use `an existing Prometheus counter pattern in internal/metrics/metrics.go (not internal/server — metrics live in their own package)` in `internal/metrics/metrics.go` (verify: `grep -n 'prometheus.NewCounterVec\|promauto' internal/metrics/metrics.go`) — do NOT write a parallel helper.

## Step-by-step

1. Run the reuse grep above to find how other handlers register/increment Prometheus counters in this codebase (likely a shared internal/server/metrics.go or similar).
2. In internal/server/handlers/abs/stats.go's ListeningStats, inside the `if seconds, err := h.userData.ListenedSeconds(user.ID); err == nil { total = seconds }` block, add an `else` branch: `slog.Warn("abs listening-stats read failed, reporting 0", "user_id", user.ID, "err", err)` plus an increment of a new/reused counter (e.g. `absListeningStatsReadFailures.Inc()` or labeled by endpoint).
3. Bump stats.go's version header.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_145.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- h.userData itself nil (no store configured): falls into the same total=0 path today without even attempting ListenedSeconds — decide whether that also deserves a warn/metric (it's a config gap, not a transient read failure) or is out of scope; note the distinction in the log message rather than conflating the two causes.

## Tests

- internal/server/handlers/abs/stats_test.go: TestListeningStats_ReadFailureLogsAndCountsButReturns200 — stub h.userData.ListenedSeconds to return an error, assert response is still 200 with TotalTime:0 (unchanged behavior), and assert the new counter incremented (or capture log output if no metric helper is reused).

Anti-over-suppression test: `N/A — logging addition, not a filter.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/handlers/abs/... -run TestListeningStats_ReadFailure -v` passes.
- [ ] Anti-over-suppression test: `N/A — logging addition, not a filter.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_145.md`.

## Commit message

```
feat(server-handlers): N-6: log + metric when listening-stats read fails (currently (ABS-N6)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'ListenedSeconds\|slog.Warn\|metric' internal/server/handlers/abs/stats.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Do not change the HTTP status code — the existing comment's reasoning (5xx flips the client's connection dot) is a deliberate, still-valid decision per the TODO text itself ('Keep the 200... but log at warn + add a metric').
