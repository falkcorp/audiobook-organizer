<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-160-bound-the-itunes-search-handler-s-unbounded-sear.md -->
<!-- version: 1.0.0 -->
<!-- guid: 19cddce8-7404-4712-9428-007c3c49b2a4 -->
<!-- last-edited: 2026-08-21 -->

# TASK-160 — Bound the iTunes search handler's unbounded SearchBooks(search, 0, 0) call (PERF-4)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Why:** Requires picking a sane bound and wiring a truncation warning without breaking the existing PID post-filter's correctness (a bound that's too small could hide legitimate iTunes-linked matches). · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 3918 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "If it returns everything: that is the opposite fai" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-06.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-160-bound-the-itunes-search-handler-s-unbounded-sear" -b agent/server-handlers-160-bound-the-itunes-search-handler-s-unbounded-sear origin/main
cd "$REPO/.worktrees/server-handlers-160-bound-the-itunes-search-handler-s-unbounded-sear"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace the unbounded SearchBooks(search, 0, 0) call in ListBooks (internal/server/handlers/itunes.go:709) with a bounded call (mirroring the existing searchPostFilterWindow=10000 precedent in internal/audiobooks/service_query.go), and log a warning when the bound is hit so a truncated result is never silently reported as complete.

## Background (verify before editing)

- This call is unbounded on purpose in one sense -- it must over-fetch because the actual filter (has a non-empty ITunesPersistentID) narrows AFTER the substring search, so a small limit could return zero PID-tagged results even when PID-tagged matches exist further down the scan. A hard local limit (not literally 0/unlimited) with an explicit truncation warning fixes the DoS-shaped worst case without breaking that narrowing logic.
- internal/audiobooks/service_query.go already solved this exact shape of problem for the Bleve-backed search + post-filter combo: it over-fetches to searchPostFilterWindow=10000 and logs a Warn when the window is exhausted (service_query.go:133-138), rather than fetching literally everything.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "SearchBooks(search, 0, 0)" internal/server/handlers/itunes.go   # 1 hit L709 — The unbounded call is still live at HEAD
  grep -n "const searchPostFilterWindow" internal/audiobooks/service_query.go   # 1 hit L649, value 10000 — An established over-fetch-with-warning precedent already exists elsewhere in the codebase for this exact shape of problem
  ```

### Reuse — don't invent

- Use `searchPostFilterWindow pattern (bound + warn-on-truncation)` in `internal/audiobooks/service_query.go` (verify: `grep -n "searchPostFilterWindow" internal/audiobooks/service_query.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/handlers/itunes.go, define a local const (e.g. `const itunesSearchOverfetchWindow = 10000`) near the top of the file or next to ListBooks, with a comment citing the internal/audiobooks/service_query.go precedent.
2. Change line 709 from `h.store.SearchBooks(search, 0, 0)` to `h.store.SearchBooks(search, itunesSearchOverfetchWindow, 0)`.
3. After the call, if `len(allBooks) >= itunesSearchOverfetchWindow`, log a slog.Warn (e.g. 'itunes ListBooks: search over-fetch window exhausted; iTunes-tagged results may be a lower bound') including the search query and window size.
4. Bump the file's version header.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_160.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- search="" -- this branch is not entered at all (the `else` branch at itunes.go:719 uses the pushdown ListBooksByITunesPID(0,0) path instead), so this fix only affects the search!="" path.
- A library with fewer than itunesSearchOverfetchWindow total books -- the bound never triggers, behavior unchanged.

## Tests

- internal/server/handlers/itunes_test.go (or wherever ListBooks is already tested): add a case with >itunesSearchOverfetchWindow matching books in a mock store, asserting the handler still returns the PID-tagged subset it can see within the window and does not error.

Anti-over-suppression test: `N/A -- this is a perf/DoS bound, not a filter/skip.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] grep -n 'itunesSearchOverfetchWindow' internal/server/handlers/itunes.go returns >=2 hits (const def + call site).
- [ ] go test ./internal/server/handlers/... -run ListBooks -v exits 0.
- [ ] Anti-over-suppression test: `N/A -- this is a perf/DoS bound, not a filter/skip.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_160.md`.

## Commit message

```
refactor(server-handlers): Bound the iTunes search handler's unbounded SearchBooks(sear (PERF-4)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Route-through-Bleve-IDs alternative mentioned in the TODO is a larger rewrite (would need a Bleve query for ITunesPersistentID != "" ANDed with the text query) -- the bounded-call fix above is the smaller, sufficient fix for the currently-measured behavior (unbounded materialization), not a cosmetic shortcut: it directly addresses the actual defect PERF-4 measured.
