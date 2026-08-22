<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-045-build-a-dry-run-report-only-classifier-for-serie.md -->
<!-- version: 1.0.0 -->
<!-- guid: aa6df42b-8bcf-47e9-ae26-fee95c833487 -->
<!-- last-edited: 2026-08-21 -->

# TASK-045 — Build a dry-run report-only classifier for series that look like they were minted from a book title (TODO.md L4304)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · dedup subagent · **Why:** a new whole-series-table maintenance op with a two-bucket fuzzy-match classifier (exact-equals vs contains) plus emitting a bounded sample list for hand-audit · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4304 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**~2,270 series look like they were created from a" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-045-build-a-dry-run-report-only-classifier-for-serie" -b agent/dedup-045-build-a-dry-run-report-only-classifier-for-serie origin/main
cd "$REPO/.worktrees/dedup-045-build-a-dry-run-report-only-classifier-for-serie"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a report-only maintenance op (e.g. maintenance.series-title-leak-audit) that walks the series table, computes each series' single associated book (where ref count == 1) and its title, classifies as 'exact' (series name == book title, case/whitespace-normalized) or 'near' (one string contains the other) or neither, and emits counts plus a bounded sample (e.g. first 40 of the 'near' bucket) for the hand-audit this TODO item explicitly calls for — no delete/merge logic in this task; that stays parked pending the hand-audit and its own apply gate.

## Background (verify before editing)

- The TODO item explicitly warns against a blunt 'book-count == 1' filter: 2,322 legitimate single-book series exist (named examples: Arliss Cutter, The Spiderwick Chronicles, Star Runners) that must NOT be caught by this classifier — only series-name/title correlation should flag a candidate, not book count alone.
- The two buckets to report (990 exact-equals, 1,280 'near'/contains) are cited directly in the TODO item as the expected current population sizes — the classifier's output should be checked against these approximate numbers as a sanity check, not treated as exact targets (the live count will have drifted since the TODO was written).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rln "title-derived.*series\|near.*bucket" internal --include=*.go   # 0 hits — no existing classifier for title-derived series
  grep -n "Apply bool" internal/plugins/maintenance/dedup_triage.go   # 1 hit ~L288, comment describing the report-only contract — reusable report-only op pattern with Apply bool default-false
  ```

### Reuse — don't invent

- Use `sdk.OperationDef report-only pattern (Apply bool, default false, dry-run emits counts/list)` in `internal/plugins/maintenance/dedup_triage.go` (verify: `grep -n "ID:.*maintenance.dedup-exact-triage" internal/plugins/maintenance/dedup_triage.go`) — do NOT write a parallel helper.
- Use `registry.RunItems bounded worker pool for the whole-series-table scan` in `internal/operations/registry/run_items.go` (verify: `grep -n "func RunItems" internal/operations/registry/run_items.go`) — do NOT write a parallel helper.
- Use `GetAllSeriesBookRefCounts (needed to find single-book series, the classifier's core signal)` in `internal/database` (verify: `grep -rn "func.*GetAllSeriesBookRefCounts" internal/database/*.go`) — do NOT write a parallel helper.

## Step-by-step

1. Create internal/plugins/maintenance/series_title_leak_audit.go modeled on dedup_triage.go: an sdk.OperationDef with ID 'maintenance.series-title-leak-audit', report-only (no Apply field, no mutation path at all in this task), Capabilities: []sdk.Capability{sdk.CapLibraryRead}.
2. In Run: get all series with a ref count of exactly 1 via GetAllSeriesBookRefCounts + a series listing call (check internal/database for the series-listing method to reuse rather than reinventing), for each fetch its single book's title, normalize both strings (reuse internal/util.NormalizeAuthor-style normalization if a title-normalization helper already exists — grep internal/titleutil first), and classify as ExactMatch (normalized equal) or NearMatch (one contains the other, case-insensitive) or Neither.
3. Shard the per-series book lookup across a bounded worker pool via registry.RunItems.
4. Report: total single-book series scanned, ExactMatch count, NearMatch count, and (bounded to e.g. 40) a sample of NearMatch entries (series ID, series name, book ID, book title) for the human hand-audit the TODO explicitly requests.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_045.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A series with a nil or empty name must not crash the normalize/compare step — classify as Neither.
- A single-book series whose one book is soft-deleted should still count via GetAllSeriesBookRefCounts (which includes trashed books per its own contract) — this classifier is about the series/title relationship, not about live-vs-trashed status.

## Tests

- internal/plugins/maintenance/series_title_leak_audit_test.go: TestSeriesTitleLeakAudit_ClassifiesExactAndNear — seed 3 series: one where name==book title (exact), one where name is a substring of the title (near), one legitimate real-series name unrelated to its book's title (neither); assert counts 1/1/1.
- TestSeriesTitleLeakAudit_NeverMutates — assert no DeleteSeries/UpdateBook call occurs during the run.

Anti-over-suppression test: `TestSeriesTitleLeakAudit_ClassifiesExactAndNear (must include a genuine single-book-series-that-is-real case classified as Neither, so the classifier isn't just flagging every single-book series)` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TestSeriesTitleLeakAudit passes.
- [ ] POST /api/v1/operations/v2 {"def_id":"maintenance.series-title-leak-audit"} on a running dev/sandbox server returns a report with ExactMatch/NearMatch counts roughly consistent with the TODO's cited 990/1,280 (allowing for drift since the TODO was written) and mutates nothing.
- [ ] Anti-over-suppression test: `TestSeriesTitleLeakAudit_ClassifiesExactAndNear (must include a genuine single-book-series-that-is-real case classified as Neither, so the classifier isn't just flagging every single-book series)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_045.md`.

## Commit message

```
feat(dedup): Build a dry-run report-only classifier for series that look  (TODO L4304)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `POST /api/v1/operations/v2 {"def_id":"maintenance.series-title-leak-audit"} on a running dev/sandbox server returns a report with ExactMatch/NearMatch counts roughly consistent with the TODO's cited 990/1,280 (allowing for drift since the TODO was written) and mutates nothing.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This satisfies the 'dry-run that emits the list' half of the TODO item. The '~40 hand-audit' and 'its own apply gate' halves are explicitly NOT part of this task — see part 2 (parked).
