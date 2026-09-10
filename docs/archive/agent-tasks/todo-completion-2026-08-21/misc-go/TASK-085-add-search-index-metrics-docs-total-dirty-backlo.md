<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-085-add-search-index-metrics-docs-total-dirty-backlo.md -->
<!-- version: 1.1.0 -->
<!-- guid: 0abf3e85-4ff3-484d-9905-86f14c78ea15 -->
<!-- last-edited: 2026-09-02 -->

# TASK-085 — Add search-index metrics (docs total, dirty backlog) to /metrics — the search index has zero metrics today (TODO.md L3433)

> **Status 2026-09-02:** ✅ DONE — PR #2758 merged 2026-08-23 (e4c1d4195).

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · misc-go subagent · **Why:** Follows an established registration pattern but needs a new gauge specifically for Bleve's DocCount(), read at the right cadence (boot + periodic, not just once) to be useful as a live divergence signal. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 3433 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**The search index has ZERO metrics.**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-085-add-search-index-metrics-docs-total-dirty-backlo" -b agent/misc-go-085-add-search-index-metrics-docs-total-dirty-backlo origin/main
cd "$REPO/.worktrees/misc-go-085-add-search-index-metrics-docs-total-dirty-backlo"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a `search_index_docs_total` gauge (Bleve's live DocCount()) to internal/metrics/metrics.go alongside books_total, updated on the same ticker cadence as books_total (or on its own ticker if none is convenient to share), so a Grafana/Prometheus dashboard can graph search_index_docs_total against books_total and immediately see a divergence like the one that caused a quarter of the library to be unfindable with nobody noticing. This is the 'half the comparison is exported already' fix the item names directly.

## Background (verify before editing)

- audiobook_organizer_books_total already exists — this item is specifically about adding its missing counterpart, search_index_docs_total, so the two can be compared on a dashboard.
- Also re-confirms and should be delivered together with todo_line 3384 (SearchIndexDroppedCount + dirty-backlog gauge) — both are 'the search index has no metrics' findings, differing only in which specific numbers are missing.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'search\|bleve\|dirty' internal/metrics/metrics.go   # 0 hits — zero search-related metrics exist in the metrics package
  sed -n '45,49p' internal/metrics/metrics.go   # booksGauge NewGauge with Name: 'books_total' — books_total exists as a working precedent to follow
  ```

### Reuse — don't invent

- Use `booksGauge / SetBooks pattern` in `internal/metrics/metrics.go` (verify: `grep -n 'booksGauge\|func SetBooks' internal/metrics/metrics.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/metrics/metrics.go, add `searchIndexDocsGauge = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "audiobook_organizer", Name: "search_index_docs_total", Help: "Current document count in the Bleve search index"})` alongside booksGauge.
2. Add it to the MustRegister/collector list at ~line 171.
3. Add `func SetSearchIndexDocs(n int) { searchIndexDocsGauge.Set(float64(n)) }` mirroring SetBooks (line 207).
4. Find the periodic metrics-gathering loop that currently calls metrics.SetBooks (internal/server/server_lifecycle.go, near the CountPrimaryBooks call fixed in todo_line 3443) and add a call to the search index's AllDocIDs()/DocCount() there, feeding metrics.SetSearchIndexDocs.
5. Land this in the same PR as todo_line 3384's search_index_dropped_total / search_index_dirty_backlog gauges — both touch the same metrics.go registration block.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_misc-go_085.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- DocCount() on a nil/not-yet-initialized search index (s.searchIndex == nil, same guard reconcileSearchIndexCoverage already checks) must not panic the metrics-gathering loop — skip the update, don't crash, matching the existing nil-guard pattern in search_coverage.go.

## Tests

- internal/metrics/metrics_test.go: assert the new gauge registers and updates.
- A /metrics scrape test asserting the metric name appears in output.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/metrics/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] A local /metrics scrape after the change includes 'audiobook_organizer_search_index_docs_total'.
- [ ] The value tracks the search index's actual DocCount() (verify by comparing scrape output to a direct s.searchIndex.DocCount() call in a test).
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/metrics/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_misc-go_085.md`.

## Commit message

```
feat(misc-go): Add search-index metrics (docs total, dirty backlog) to /met (TODO L3433)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'search\|bleve\|dirty' internal/metrics/metrics.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Do together with todo_line 3384 — same file (internal/metrics/metrics.go), same registration block, same periodic-update call site.
