<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-080-new-maintenance-op-merge-an-operator-confirmed-l.md -->
<!-- version: 1.0.0 -->
<!-- guid: fefc7322-e8af-4dd2-a773-83879d3ced2d -->
<!-- last-edited: 2026-08-21 -->

# TASK-080 — New maintenance op: merge an operator-confirmed list of duplicate real-author rows (TODO.md L3795)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · maintenance subagent · **Why:** Deletes author rows and rewrites book links on a prod data path; needs a deliberately narrow, explicit-allowlist design (see notes) plus full dry-run/canonical-selection test coverage, not a mechanical change. · **Depends on:** TASK-095 · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 3795 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Merge the type-3 real-author duplicates. The exist" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-06.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-080-new-maintenance-op-merge-an-operator-confirmed-l" -b agent/maintenance-080-new-maintenance-op-merge-an-operator-confirmed-l origin/main
cd "$REPO/.worktrees/maintenance-080-new-maintenance-op-merge-an-operator-confirmed-l"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new maintenance op (e.g. 'maintenance.author-duplicate-merge') that merges ONLY author-name groups the operator explicitly lists (a Names []string param, matched after util.NormalizeAuthor), never an auto-detected 'looks like a real name' heuristic. For each listed group, group live authors by normalized name, pick the canonical row (most books via GetAllAuthorBookCounts, tie-break lowest ID), and call p.mergeAuthorInto for every other row in the group into it. Dry-run true by default, per the repo's own precedent in author_conjunction_repair.go.

## Background (verify before editing)

- TODO.md's 2026-08-14 snapshot names 4 known type-3 groups needing merge: Karen Joy Fowler, Valery Starsky, Raymond L. Weil, Time Pebbles -- but the owner's decision list explicitly warns 'counts to re-measure before acting', so the op must not hardcode these names; it must accept them as a runtime param.
- L3799 (types 1/2 -- book-title/disc-label rows masquerading as authors) is UNRESOLVED (needs_design, see that item) and L3803 warns against a single op that treats all three kinds alike. Requiring an explicit Names allowlist (rather than any automatic 'is this a real name' classifier) sidesteps that ambiguity entirely: the op can never accidentally merge a junk group because it only acts on names the operator names.
- mergeAuthorInto's doc comment (author_conjunction_repair.go:280-287) already states it moves BookAuthor join-slice links, not just Book.AuthorID -- the correct mechanism, already used and tested by author-conjunction-repair.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func (p \*Plugin) mergeAuthorInto" internal/plugins/maintenance/author_conjunction_repair.go   # 1 hit L288 — mergeAuthorInto moves every BookAuthor link and deletes the losing row
  grep -n "func (p \*PebbleStore) GetAllAuthors" internal/database/pebble_store_authors.go   # 1 hit L21 — GetAllAuthors exists for enumerating the 9,320-row table
  grep -rn 'author-duplicate-merge\|author-merge\|merge-author\|MergeAuthors\b' internal/plugins/maintenance   # 0 hits — no existing op already merges general name-duplicate authors (maintenance.author-dedup-scan exists but only refreshes the duplicate-group cache; it does not merge)
  ```

### Reuse — don't invent

- Use `mergeAuthorInto` in `internal/plugins/maintenance/author_conjunction_repair.go` (verify: `grep -n "func (p \*Plugin) mergeAuthorInto" internal/plugins/maintenance/author_conjunction_repair.go`) — do NOT write a parallel helper.
- Use `util.NormalizeAuthor (post-L3790 fix)` in `internal/util/normalize.go` (verify: `grep -n "func NormalizeAuthor" internal/util/normalize.go`) — do NOT write a parallel helper.
- Use `sdk.NewProgress / dryRun-default param pattern` in `internal/plugins/maintenance/author_conjunction_repair.go` (verify: `grep -n "DryRun \*bool" internal/plugins/maintenance/author_conjunction_repair.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add `func displayOrNone(s string) string { if s == "" { return "(none)" }; return s }` directly in internal/metafetch/service_apply.go, next to RecordChangeHistory (do NOT add it to service.go — keep the change confined to the one file already in scope).
2. In service_apply.go's RecordChangeHistory, change the Summary line (currently L242) to: `title := book.Title; if title == "" { title = book.ID }; Summary: fmt.Sprintf("%s: Applied %s: %s → %s", title, c.field, displayOrNone(truncateActivity(c.oldVal, 50)), truncateActivity(c.newVal, 50))`.
3. Bump the version header on internal/metafetch/service_apply.go only.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_080.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A listed name normalizes to a group of exactly 1 live row -- no-op, log 'no duplicate found'.
- A listed name matches zero live authors (already merged or typo) -- log and continue, not an error.
- Two listed names normalize to the SAME group -- dedupe the group list before processing so it isn't processed twice.
- Canonical row has a tombstone entry from a prior merge -- reuse CreateAuthorTombstone/GetAuthorTombstone if applicable (see pebble_store_authors.go:832-840); confirm this doesn't already happen inside mergeAuthorInto before adding it again.

## Tests

- internal/plugins/maintenance/author_duplicate_merge_test.go: TestAuthorDuplicateMerge_MergesListedGroupOnly -- 2 groups exist (one listed, one not), assert only the listed group's rows merge.
- TestAuthorDuplicateMerge_CanonicalIsHighestBookCount -- 3-row group, assert the row with the most books survives.
- TestAuthorDuplicateMerge_DryRunDefaultTrue -- no DryRun param supplied, assert no rows are deleted.
- TestAuthorDuplicateMerge_EmptyNamesIsNoop -- Names=[] or nil, assert zero merges even if duplicate groups exist in the fixture (anti-over-suppression / anti-laundering check).

Anti-over-suppression test: `TestAuthorDuplicateMerge_EmptyNamesIsNoop and TestAuthorDuplicateMerge_MergesListedGroupOnly -- together they prove the op only acts on what it is explicitly told to, which is the anti-laundering guard for this task (see feedback_stripping_without_corroboration_is_laundering, cited directly by L3803).` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TestAuthorDuplicateMerge -v exits 0.
- [ ] grep -n 'maintenance.author-duplicate-merge' internal/plugins/maintenance/author_duplicate_merge.go returns >=1 hit (op ID registered).
- [ ] Anti-over-suppression test: `TestAuthorDuplicateMerge_EmptyNamesIsNoop and TestAuthorDuplicateMerge_MergesListedGroupOnly -- together they prove the op only acts on what it is explicitly told to, which is the anti-laundering guard for this task (see feedback_stripping_without_corroboration_is_laundering, cited directly by L3803).` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_080.md`.

## Commit message

```
refactor(maintenance): New maintenance op: merge an operator-confirmed list of dupl (TODO L3795)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

Soft dependency on L3790 (whitespace-normalization fix) only in the sense that re-measuring duplicate groups after L3790 ships will show fewer/different groups; this op works correctly either way since it groups by util.NormalizeAuthor at call time.
