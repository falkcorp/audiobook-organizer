<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-093-log-a-warning-when-getallseriesbookcounts-itself.md -->
<!-- version: 1.0.0 -->
<!-- guid: b8f9228c-7dfc-465d-b004-073302b1ac7a -->
<!-- last-edited: 2026-08-21 -->

# TASK-093 — Log a warning when GetAllSeriesBookCounts() itself errors in LibrarySeries (TODO.md L5494)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · missing-file-lane subagent · **Why:** One-line addition to an existing, well-understood error branch. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 5494 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**AudiobookShelf-compatible API: series are broken" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-11.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-093-log-a-warning-when-getallseriesbookcounts-itself" -b agent/missing-file-lane-093-log-a-warning-when-getallseriesbookcounts-itself origin/main
cd "$REPO/.worktrees/missing-file-lane-093-log-a-warning-when-getallseriesbookcounts-itself"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a slog.Warn call at internal/server/handlers/abs/browse.go's GetAllSeriesBookCounts() error branch (~L502-506) so a total failure of the count query is observable, matching the exact ask in the TODO item: 'add a slog.Warn here; a silent zero is how this went unnoticed.'

## Background (verify before editing)

- The mismatch-per-series case already logs via logSeriesBookCountMismatch (browse.go:724), but the upstream call failing entirely (counts = map[int]int{}) does not log at all.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "counts, err := h.library.GetAllSeriesBookCounts()" internal/server/handlers/abs/browse.go   # 1 hit ~L502 — the GetAllSeriesBookCounts error path has no logging, only a comment
  grep -n "slog.Warn" internal/server/handlers/abs/browse.go   # >=1 hit (L725 etc.) — package already imports log/slog for use elsewhere in this file
  ```

### Reuse — don't invent

- Use `logSeriesBookCountMismatch (existing slog.Warn pattern in same file)` in `internal/server/handlers/abs/browse.go` (verify: `grep -n "func (h \*Handler) logSeriesBookCountMismatch" internal/server/handlers/abs/browse.go`) — do NOT write a parallel helper.

## Step-by-step

1. Open internal/server/handlers/abs/browse.go and find the block starting `counts, err := h.library.GetAllSeriesBookCounts()` (~L502).
2. Inside the `if err != nil { ... }` branch, before `counts = map[int]int{}`, add: `slog.Warn("abs: series book counts unavailable, reporting 0 for all series", "err", err)`.
3. Confirm `log/slog` is already imported in this file (it is, used elsewhere in the same file for other slog.Warn calls).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_093.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- counts == nil vs counts == map[int]int{} — both should be treated as 'query failed', log once per request, not once per series.

## Tests

- Add a table-driven or standalone test in internal/server/handlers/abs/browse_test.go that injects a failing GetAllSeriesBookCounts on a fake library store and asserts (via a captured slog handler or httptest response) that a warning is logged; name it TestLibrarySeries_CountsErrorLogsWarning.

Anti-over-suppression test: `TestLibrarySeries_CountsErrorLogsWarning must assert the log line fires on error and does NOT fire on the happy path (counts populated normally) to avoid a permanent noisy warning.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] grep -n 'series book counts unavailable' internal/server/handlers/abs/browse.go returns 1 hit
- [ ] go test ./internal/server/handlers/abs/... -run TestLibrarySeries passes
- [ ] Anti-over-suppression test: `TestLibrarySeries_CountsErrorLogsWarning must assert the log line fires on error and does NOT fire on the happy path (counts populated normally) to avoid a permanent noisy warning.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_093.md`.

## Commit message

```
feat(missing-file-lane): Log a warning when GetAllSeriesBookCounts() itself errors in (TODO L5494)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`grep -n 'series book counts unavailable' internal/server/handlers/abs/browse.go returns 1 hit`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Small, isolated, low risk — safe for a haiku-tier agent.
