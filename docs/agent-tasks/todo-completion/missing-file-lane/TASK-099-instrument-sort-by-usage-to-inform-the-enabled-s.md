<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-099-instrument-sort-by-usage-to-inform-the-enabled-s.md -->
<!-- version: 1.0.0 -->
<!-- guid: 10283bce-794e-4f91-9648-fc2c18491510 -->
<!-- last-edited: 2026-08-21 -->

# TASK-099 — Instrument sort_by usage to inform the enabled_sort_indexes decision (TODO.md L6701)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · missing-file-lane subagent · **Why:** One log line at an existing, well-understood call site. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 6701 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**⚖️ DECIDE which sort indexes to enable — the des" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-11.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-099-instrument-sort-by-usage-to-inform-the-enabled-s" -b agent/missing-file-lane-099-instrument-sort-by-usage-to-inform-the-enabled-s origin/main
cd "$REPO/.worktrees/missing-file-lane-099-instrument-sort-by-usage-to-inform-the-enabled-s"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a low-cardinality log/metric of sort_by values actually requested by clients, over a representative window, to replace guesswork with real usage data before deciding which of the nine indexed sort fields (option 2 in the item) to enable — per the item's cheapest-first option list.

## Background (verify before editing)

- EnabledSortIndexes defaults to empty ([]string), matching option 1 (enable none for now) as the current behavior (`grep -n 'Empty (the default) reproduces' internal/config/config.go` confirms this comment exists ~L1022).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'ParseQueryString(c, "sort_by")' internal/server/handlers/audiobooks/handler.go   # 1 hit ~L519 — sort_by is parsed from the query string here
  grep -rn 'slog.*sort_by\|sort_by.*slog' internal/   # 0 hits — no existing instrumentation logs sort_by values
  grep -n 'func CanPushDownSort' internal/database/memdb_sort_indexers.go   # 1 hit ~L336 — CanPushDownSort consults only the enabled set, as the item states
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In internal/server/handlers/audiobooks/handler.go near the SortBy parsing (~L519), add a low-cardinality counter/log, e.g. `slog.Debug("audiobooks: sort_by requested", "sort_by", filter.SortBy)` — or, if the project has a metrics counter pattern already (check internal/metrics/ for an existing Prometheus counter helper), prefer incrementing a `sort_by_requested_total{field=...}` counter over a log line, since /metrics gaps are a known project issue (see project memory: Prometheus gap) and a log line risks being as unobserved as the GetAllSeriesBookCounts silent-zero case elsewhere in this scope.
2. If using a counter, register it near other request-scoped metrics in the same package; keep the label cardinality bounded to known sort field names (reject/bucket unknown values as "other").

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_099.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Empty/default sort_by (title, the always-streamed field) should still be counted, as a baseline to compare other fields against.

## Tests

- internal/server/handlers/audiobooks/handler_test.go: assert the instrumentation fires (metric increments, or a captured log record) once per request with the requested SortBy value, including for the default/empty case.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/server/handlers/audiobooks/... passes
- [ ] A week of prod logs/metrics shows a distribution of sort_by values, closing the 'nobody has measured which sorts real users pick' gap the item cites.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_099.md`.

## Commit message

```
feat(missing-file-lane): Instrument sort_by usage to inform the enabled_sort_indexes  (TODO L6701)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/server/handlers/audiobooks/... passes`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is the item's own recommended cheapest-first option (#2, 'instrument first'); it does not itself decide which fields to enable — see part 2 for that separate design decision.
