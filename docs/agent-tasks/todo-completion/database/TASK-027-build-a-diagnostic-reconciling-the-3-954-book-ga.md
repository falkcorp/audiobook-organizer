<!-- file: docs/agent-tasks/todo-completion/database/TASK-027-build-a-diagnostic-reconciling-the-3-954-book-ga.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6641cc46-f58b-4614-aa3a-1111b198104c -->
<!-- last-edited: 2026-08-21 -->

# TASK-027 — Build a diagnostic reconciling the 3,954-book gap between the store's live-book count and the API list endpoint's total (TODO.md L3414)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · database subagent · **Why:** Root cause genuinely unknown per the item's own text; requires building a small diagnostic tool and reading its output before any fix can even be scoped — investigative, not mechanical. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 3414 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**The store reports 67,824 live books; the API lis" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-027-build-a-diagnostic-reconciling-the-3-954-book-ga" -b agent/database-027-build-a-diagnostic-reconciling-the-3-954-book-ga origin/main
cd "$REPO/.worktrees/database-027-build-a-diagnostic-reconciling-the-3-954-book-ga"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build a small read-only diagnostic (tools/cmd/reconcile-book-counts/main.go, or a REPORT-only maintenance op per the owner's stated preference for report-over-mutate ops) that, against a live store, computes and prints: (1) len(ListBookIDs()) — the true live-book-ID-set size, (2) the count the /api/v1/audiobooks list endpoint reports when paged with library_state empty, and (3) the actual set difference (which IDs are in (1) but not (2)) — not just the count gap. FIRST CHECK: confirm whether the '67,824' figure originally cited in this item was actually a snapshot of Bleve's DocCount() (which the search_coverage.go history shows was polluted with 3,953 stale soft-deleted docs around the same date) rather than a true ListBookIDs() count — if so, this item's premise may be a measurement mix-up rather than a third invisible-books population, and the diagnostic should say so explicitly rather than assuming a real gap exists.

## Background (verify before editing)

- ListBookIDs already excludes MarkedForDeletion, so per the item's own reasoning these 3,954 are live rows that the API endpoint simply never serves.
- Paging the endpoint returns exactly 63,870 distinct IDs — internally consistent, so the API is not itself broken; the question is what population ListBookIDs sees that the endpoint's underlying query does not.
- A near-identical number (67,824 vs 63,871, off by only 3 from this item's 67,824 vs 63,870) shows up in search_coverage.go's own commit history as a Bleve-doc-count issue that has SINCE BEEN FIXED — strong enough coincidence to check first before assuming a new, unexplained population.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'ListBookIDs' internal/database/pebble_store.go | head -3   # comment: 'When memdb is available, delegates to the memdb fast path (which also filters MarkedForDeletion)' — ListBookIDs already excludes MarkedForDeletion
  grep -n '67,824' internal/server/search_coverage.go   # 1 hit in the file's header comment, describing Bleve DocCount()=67,824 vs live books=63,871 — a DIFFERENT axis of measurement than 'store reports 67,824 live books' — an identical 67,824 figure appears elsewhere, already explained as a stale-search-index-doc-count issue, not a store-book-count issue
  ```

### Reuse — don't invent

- Use `reconcileSearchIndexCoverage's set-comparison pattern (AllDocIDs vs ListBookIDs)` in `internal/server/search_coverage.go` (verify: `grep -n 'func (s \*Server) reconcileSearchIndexCoverage' internal/server/search_coverage.go`) — do NOT write a parallel helper.

## Step-by-step

1. Write tools/cmd/reconcile-book-counts/main.go: connect to the store, call ListBookIDs(), build a set.
2. Call the same code path GetBookSummaries/the audiobooks service uses for a library_state-empty, is_primary_version-unset list (bypass the HTTP layer, call the service function directly with paging disabled or a very high limit) to get the second set.
3. Print len(set1), len(set2), and the up-to-50 IDs present in set1 but absent from set2 (set difference), plus each such book's LibraryState, MarkedForDeletion, and IsPrimaryVersion fields.
4. Before running against prod, first run this same diagnostic's DocCount()-vs-ListBookIDs() comparison (the search_coverage.go set-comparison logic already does this — reuse it or call reconcileSearchIndexCoverage in dry-run/log-only mode) to rule in/out the Bleve-doc-count mix-up hypothesis from 'goal'.
5. Document findings; if a real gap is confirmed (not a measurement mix-up), file it as its own todo.d fragment with the root cause now identified, rather than fixing inline here.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_027.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If run against a live production store, must be strictly read-only — no write path in the diagnostic tool at all, to avoid any risk on a review-critical population question.

## Tests

- N/A for the diagnostic tool itself (read-only, ad-hoc).

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./tools/cmd/reconcile-book-counts/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] The diagnostic prints a definitive answer: either 'no real gap — the original 67,824 figure was a Bleve DocCount() snapshot, already explained/fixed' or a concrete list of book IDs and their differentiating field values explaining a genuine third population.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./tools/cmd/reconcile-book-counts/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_027.md`.

## Commit message

```
feat(database): Build a diagnostic reconciling the 3,954-book gap between th (TODO L3414)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'ListBookIDs' internal/database/pebble_store.go | head -3` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is a report/investigation task per the owner's PH-2b-style preference (report before mutate) — do not build a repair op until the cause is established, per the item's own explicit caution.
