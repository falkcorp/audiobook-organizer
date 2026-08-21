<!-- file: docs/agent-tasks/todo-completion/server/TASK-141-fix-audiobook-organizer-books-total-to-report-th.md -->
<!-- version: 1.0.0 -->
<!-- guid: cb2c9c96-c300-4341-89be-69e5bbc3d92f -->
<!-- last-edited: 2026-08-21 -->

# TASK-141 — Fix audiobook_organizer_books_total to report the true total, not just primary books (or rename it) (TODO.md L3443)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Small, precisely located fix — either swap one function call or add a second gauge; the only judgment needed is which resolution the owner prefers. · **Depends on:** none · **Wave:** 4

Source: `TODO.md` line 3443 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`audiobook_organizer_books_total` reports the PR" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-141-fix-audiobook-organizer-books-total-to-report-th" -b agent/server-141-fix-audiobook-organizer-books-total-to-report-th origin/main
cd "$REPO/.worktrees/server-141-fix-audiobook-organizer-books-total-to-report-th"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Either (preferred, since it's the smaller footprint and preserves the existing metric name's meaning for anything already dashboards on 'primary books') rename booksGauge's metric Name/Help to make clear it counts PRIMARY books only (e.g. Name: 'primary_books_total', Help: 'Current total number of primary-version books in library') and add a NEW gauge 'books_total' fed by CountAllBooks() for the true total — OR (if the owner prefers not to break the existing metric name for any consumer already keyed on it) add the new true-total gauge under a different name (e.g. 'books_total_all_versions') while leaving the existing (misleadingly-named) one alone. Either way, both values should end up exported so a dashboard built on the current name is not silently wrong forever.

## Background (verify before editing)

- Live production value: 40,841 (primary count) against 67,824 live books in the store — under-reporting the library by ~40%. Any dashboard built on the current metric is currently wrong about library size.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '495,509p' internal/server/server_lifecycle.go   # 'if bc, err := s.Ops().CountPrimaryBooks(); err == nil { bookCount = bc }' ... 'metrics.SetBooks(bookCount)' — the metric is fed by CountPrimaryBooks, not a true total
  grep -n 'func (p \*PebbleStore) CountAllBooks' internal/database/pebble_store.go   # 1 hit at L3028 — a true-total counter already exists and is unused for this metric
  sed -n '45,49p' internal/metrics/metrics.go   # Help: 'Current total number of books in library' — the gauge's Help text claims it is the true total
  ```

### Reuse — don't invent

- Use `CountAllBooks` in `internal/database/pebble_store.go` (verify: `grep -n 'func (p \*PebbleStore) CountAllBooks' internal/database/pebble_store.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/metrics/metrics.go, add a second gauge (e.g. `booksTotalAllGauge`) with Name: 'books_total_all' (or the owner's preferred final name) and Help: 'Current total number of books in library, all versions' — following the exact same NewGauge/MustRegister/setter pattern as booksGauge.
2. In internal/server/server_lifecycle.go's metrics-gathering loop (~line 495-509), add a second call: `if bc, err := s.Ops().CountAllBooks(); err == nil { metrics.SetBooksTotalAll(bc) }` alongside the existing CountPrimaryBooks call.
3. Update booksGauge's Help text (internal/metrics/metrics.go:48) to explicitly say 'primary-version books' rather than the current ambiguous 'total number of books', since it will continue to report the primary-only count.
4. Add a CHANGELOG fragment noting the metric's Help text was corrected and a new true-total metric was added, since any existing dashboard reading the OLD Help text's claim was wrong and should be flagged.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_141.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If CountAllBooks() and CountPrimaryBooks() ever return an error independently, each metric update must be independently guarded (matching the existing `if err == nil` pattern) so a failure in one does not suppress the other.

## Tests

- internal/metrics/metrics_test.go: assert both gauges reflect the correct, DIFFERENT values when primary and total book counts differ in a test fixture.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] A /metrics scrape shows both the (corrected-Help) primary-books gauge and the new true-total gauge, with the true-total gauge's value equal to a direct CountAllBooks() call in a test.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_141.md`.

## Commit message

```
fix(server): Fix audiobook_organizer_books_total to report the true total (TODO L3443)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`A /metrics scrape shows both the (corrected-Help) primary-books gauge and the new true-total gauge, with the true-total gauge's value equal to a direct CountAllBooks() call in a test.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Do together with todo_line 3433/3384 if practical — all three touch internal/metrics/metrics.go's registration block and internal/server/server_lifecycle.go's same periodic metrics-gathering loop.
