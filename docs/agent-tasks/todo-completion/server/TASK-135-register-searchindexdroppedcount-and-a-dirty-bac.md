<!-- file: docs/agent-tasks/todo-completion/server/TASK-135-register-searchindexdroppedcount-and-a-dirty-bac.md -->
<!-- version: 1.0.0 -->
<!-- guid: e10b1e69-ef20-4138-b3c0-c5248c027523 -->
<!-- last-edited: 2026-08-21 -->

# TASK-135 — Register SearchIndexDroppedCount (and a dirty-backlog gauge) as Prometheus metrics (TODO.md L3384)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Mechanical addition following an existing, well-established gauge-registration pattern in the same file (booksGauge/foldersGauge/SetBooks). · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 3384 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`SearchIndexDroppedCount` is not actually expose" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-135-register-searchindexdroppedcount-and-a-dirty-bac" -b agent/server-135-register-searchindexdroppedcount-and-a-dirty-bac origin/main
cd "$REPO/.worktrees/server-135-register-searchindexdroppedcount-and-a-dirty-bac"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new prometheus.Counter (search_index_dropped_total) and a prometheus.Gauge (search_index_dirty_backlog) to internal/metrics/metrics.go, following the exact registration pattern already used for booksGauge (declared in the var block, added to the MustRegister/collector list, exposed via a setter function), then call the new setter(s) from wherever SearchIndexDroppedCount() and the dirty-set size are currently only logged (search_reconciler.go), so both are periodically pushed to the registered metric.

## Background (verify before editing)

- This is the direct reason a quarter of the library was unfindable for an unknown period with nobody noticing per todo_line 3433's framing — same root issue, this item is specifically about the drop-counter piece of it.
- audiobook_organizer_books_total already exists as a working example of exactly this registration pattern (internal/metrics/metrics.go:45-48, 207).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func SearchIndexDroppedCount' internal/server/search_reconciler.go   # 1 hit at L86 — the getter exists in search_reconciler.go
  grep -rn 'SearchIndexDropped' internal/metrics/*.go   # 0 hits in internal/metrics/ — nothing registers it as a Prometheus metric in internal/metrics/
  ```

### Reuse — don't invent

- Use `booksGauge / SetBooks pattern (existing gauge registration + updater)` in `internal/metrics/metrics.go` (verify: `grep -n 'booksGauge\|func SetBooks' internal/metrics/metrics.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/metrics/metrics.go, add `searchIndexDroppedCounter = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "audiobook_organizer", Name: "search_index_dropped_total", Help: "Total search index events dropped since process start"})` and `searchIndexDirtyGauge = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "audiobook_organizer", Name: "search_index_dirty_backlog", Help: "Current number of books marked dirty and pending re-index"})` near booksGauge/foldersGauge.
2. Add both to the collector-registration list (the one booksGauge/foldersGauge/memoryAllocGauge/goroutinesGauge appear in, ~line 171).
3. Add setter functions `func SetSearchIndexDropped(n int64) { searchIndexDroppedCounter.Add(n) }` and `func SetSearchIndexDirtyBacklog(n int)  { searchIndexDirtyGauge.Set(float64(n)) }` (matching SetBooks's style at line 207).
4. In internal/server/search_reconciler.go, find where SearchIndexDroppedCount()'s underlying counter is incremented and where the dirty-set size is known (likely in the reconciler's ticker loop or wherever markIndexDirty is called), and call the new metrics.SetSearchIndexDropped/SetSearchIndexDirtyBacklog setters alongside the existing slog calls.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_135.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- The dirty-backlog gauge should reflect the CURRENT size of the durable dirty set, not a cumulative count — use Set(), not Add(), matching the item's own framing ('dirty-set backlog').

## Tests

- internal/metrics/metrics_test.go (or nearest existing test): assert the new metrics are registered and update correctly when their setters are called.
- A /metrics HTTP test (if one exists for booksGauge) extended to also assert search_index_dropped_total and search_index_dirty_backlog appear in the scrape output.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] A local /metrics scrape after the change includes 'audiobook_organizer_search_index_dropped_total' and 'audiobook_organizer_search_index_dirty_backlog'.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_135.md`.

## Commit message

```
feat(server): Register SearchIndexDroppedCount (and a dirty-backlog gauge) (TODO L3384)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`A local /metrics scrape after the change includes 'audiobook_organizer_search_index_dropped_total' and 'audiobook_organizer_search_index_dirty_backlog'.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Do this together with todo_line 3433 (which asks for the same class of missing metrics more broadly, including search_index_docs_total) — likely the same PR, since both touch internal/metrics/metrics.go.
