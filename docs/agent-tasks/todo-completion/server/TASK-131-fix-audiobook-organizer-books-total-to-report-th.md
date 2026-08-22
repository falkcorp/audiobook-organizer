<!-- file: docs/agent-tasks/todo-completion/server/TASK-131-fix-audiobook-organizer-books-total-to-report-th.md -->
<!-- version: 1.0.0 -->
<!-- guid: b07ad2e7-6b95-47b7-be8f-f5b65dc0b29a -->
<!-- last-edited: 2026-08-21 -->

# TASK-131 — Fix audiobook_organizer_books_total to report the true total, not just primary books (or rename it) (TODO.md L3443)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Small, precisely located fix — either swap one function call or add a second gauge; the only judgment needed is which resolution the owner prefers. · **Depends on:** none · **Wave:** 4

Source: `TODO.md` line 3443 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`audiobook_organizer_books_total` reports the PR" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-131-fix-audiobook-organizer-books-total-to-report-th" -b agent/server-131-fix-audiobook-organizer-books-total-to-report-th origin/main
cd "$REPO/.worktrees/server-131-fix-audiobook-organizer-books-total-to-report-th"
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
  grep -n 'CountPrimaryBooks()\|metrics.SetBooks(' internal/server/server_lifecycle.go   # 2 hits: L499 `if bc, err := s.Ops().CountPrimaryBooks(); err == nil {` and L508 `metrics.SetBooks(bookCount)` — the metric is fed by CountPrimaryBooks, not a true total
  grep -n 'CountAllBooks' internal/database/pebble_store.go internal/server/server_ops_store.go   # hit at pebble_store.go:3028; ZERO hits in server_ops_store.go — CountAllBooks exists on PebbleStore but is NOT on the ServerOpsStore interface s.Ops() returns
  grep -n 'Name: *"books_total"\|Current total number of books in library' internal/metrics/metrics.go   # 2 hits: L47 `Name: "books_total"` and L48 `Help: "Current total number of books in library"` — booksGauge's Help text falsely claims a true total
  ```

### Reuse — don't invent

- Use `CountAllBooks` in `internal/database/pebble_store.go` (verify: `grep -n 'func (p \*PebbleStore) CountAllBooks' internal/database/pebble_store.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/server_ops_store.go, add `CountAllBooks() (int, error)` to the `serverStatsReader` sub-interface (declared at internal/server/server_ops_store.go:128, the one that already declares `CountPrimaryBooks() (int, error)` at L129), immediately after that line — verify placement with `grep -n 'CountPrimaryBooks' internal/server/server_ops_store.go`. PebbleStore already satisfies it (internal/database/pebble_store.go:3028) so `var _ ServerOpsStore = (*database.PebbleStore)(nil)` at server_ops_store.go:262 keeps compiling; both MockStores already implement CountAllBooks (internal/database/mock_store.go:957, internal/database/mocks/mock_store.go).
2. In internal/metrics/metrics.go, add a second gauge `booksTotalAllGauge` with Name 'books_total_all' and Help 'Current total number of books in library, all versions', following the exact NewGauge/MustRegister/setter pattern used by booksGauge (metrics.go:45-49), plus a `SetBooksTotalAll(n int)` setter mirroring SetBooks.
3. In internal/metrics/metrics.go, change booksGauge's Help text (L48) from 'Current total number of books in library' to 'Current total number of PRIMARY-version books in library'. Do NOT rename its Name ('books_total') — existing dashboards key on it.
4. In internal/server/server_lifecycle.go, inside the existing `if s.Ops() != nil {` block at L498-505, add `if bc, err := s.Ops().CountAllBooks(); err == nil { metrics.SetBooksTotalAll(bc) }` alongside the existing CountPrimaryBooks call at L499. Guard each independently so one error does not suppress the other.
5. Add internal/metrics/metrics_test.go coverage asserting the two gauges hold DIFFERENT values when primary != total.
6. Bump the file header (version + last-edited: 2026-08-21) on internal/metrics/metrics.go, internal/metrics/metrics_test.go, internal/server/server_lifecycle.go and internal/server/server_ops_store.go.
7. Add changelog fragment changelog.d/20260821_server_131.md (no file header).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_131.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If CountAllBooks() and CountPrimaryBooks() ever return an error independently, each metric update must be independently guarded (matching the existing `if err == nil` pattern) so a failure in one does not suppress the other.

## Tests

- internal/metrics/metrics_test.go: assert both gauges reflect the correct, DIFFERENT values when primary and total book counts differ in a test fixture.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/metrics/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] A /metrics scrape shows both the (corrected-Help) primary-books gauge and the new true-total gauge, with the true-total gauge's value equal to a direct CountAllBooks() call in a test.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/metrics/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_131.md`.

## Commit message

```
fix(server): Fix audiobook_organizer_books_total to report the true total (TODO L3443)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run sed -n '495,509p' internal/server/server_lifecycle.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Do together with todo_line 3433/3384 if practical — all three touch internal/metrics/metrics.go's registration block and internal/server/server_lifecycle.go's same periodic metrics-gathering loop.
