<!-- file: docs/agent-tasks/todo-completion/docs/TASK-059-close-out-the-2026-05-01-re-audit-block-test-2-d.md -->
<!-- version: 1.0.0 -->
<!-- guid: f8378e74-b1b4-40c8-afde-2e4ce214c18f -->
<!-- last-edited: 2026-08-21 -->

# TASK-059 — Close out the 2026-05-01 re-audit block (TEST-2/DEP-1/DEAD-1/CTX-4/LOG-5/R-9/R-10) (TODO.md L10706)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · docs subagent · **Why:** editing a TODO.md prose bullet to record verified closure; no code change beyond the DEP-1e follow-up which is a separate, smaller task · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10706 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**2026-05-01 re-audit block close-out pass**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-059-close-out-the-2026-05-01-re-audit-block-test-2-d" -b agent/docs-059-close-out-the-2026-05-01-re-audit-block-test-2-d origin/main
cd "$REPO/.worktrees/docs-059-close-out-the-2026-05-01-re-audit-block-test-2-d"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Rewrite TODO.md's item 42 bullet (L10706-10709, '2026-05-01 re-audit block close-out pass') to record that TEST-2/DEP-1a-d/DEAD-1/CTX-4/LOG-5/R-9/R-10/PERF-1 are all confirmed resolved-or-moot with the grep evidence above, and spin DEP-1e (drop `Book.ITunesPath` / `books.itunes_path`) out as its own small forward-looking TODO line since it is the one genuinely unresolved sub-item.

## Background (verify before editing)

- docs/archive/codebase-evaluation.md's '2026-05-01 Re-Audit — New Findings' section (L30, TOC L16) is the original source of this finding set; docs/archive/todo-2026-H1.md:3185-3226 has the frozen original bullet text (non-checkbox format) for TEST-2/DEP-1/DEAD-1/CTX-4/LOG-5/R-9/R-10.
- PERF-1 (referenced in the same item text) was independently confirmed obsolete via commit 19e129d4 'future-proof remaining whole-library ops against fixed-limit truncation' (see scope item 45's own finding) — the two findings corroborate each other.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'legacySaveConfigToDatabase_REMOVED\|bookTagKeyspace\|bookSummarySelectColumnsQualified' --include=*.go .   # 0 hits — DEAD-1 symbols removed
  grep -rn 'func.*Summarize(ctx context.Context\|func.*CompactByDay(ctx context.Context' --include=*.go internal/database internal/activity   # ≥6 hits — CTX-4 done on all ActivityStore implementations
  find . -iname sqlite_store.go; grep -rn 'fmt.Printf\|log.Printf' internal/database internal/playlist internal/organizer --include=*.go   # 0 hits both — LOG-5 done, sqlite_store.go gone
  grep -rn 'errors.New("[A-Z]\|fmt.Errorf("[A-Z]' internal/metadata/audible.go internal/metadata/audnexus.go internal/metadata/googlebooks.go internal/metadata/hardcover.go internal/metadata/openlibrary.go internal/metadata/wikipedia.go   # 0 hits — R-10 done
  grep -rn 'book\.ITunesPath\b' --include=*.go internal/   # 0 hits outside _test.go — DEP-1a-d done (no non-test reads of the deprecated Book field)
  grep -n 'ITunesPath ' internal/database/store.go; grep -n 'ITunesPath: b.ITunesPath\|ITunesPath: c.ITunesPath' internal/database/bookcore.go   # 1 hit + 2 hits — DEP-1e still open — field still declared and copied
  grep -n 'func TestStoreAdditionalCoverageSQLite' -A3 internal/database/store_extra_test.go   # 1 hit, body calls setupTestDB (PebbleStore-backed) — TEST-2 test exists and is not SQLite-backed anymore
  grep -n 'ITunesPath: *b.ITunesPath\|ITunesPath: *c.ITunesPath' internal/database/bookcore.go   # 2 hits (L207, L321), tolerant of gofmt alignment spaces — DEP-1e still open — field still declared and copied
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In TODO.md, replace the L10706-10709 bullet body with a closure note: 'CLOSED 2026-08-21 — TEST-2/DEAD-1/CTX-4/LOG-5/R-9/R-10/DEP-1a-d all verified resolved or moot at HEAD (see grep evidence in scout package). DEP-1e (drop deprecated Book.ITunesPath field / books.itunes_path column) spun out separately below.'
2. Add a new todo.d/ fragment (per the repo's fragment system, headerless) describing DEP-1e as a standalone small task: remove the `ITunesPath *string` field from `internal/database/store.go`'s `Book` struct (~L220) and its two copy-sites in bookcore.go (L207, L321), after confirming (again, at execution time) that nothing else reads it.
3. Do NOT implement DEP-1e itself in this pass — it is scoped as future work; this item's job is the close-out + fragment, not the removal.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_059.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If a future grep at execution time finds a NEW non-test read of `book.ITunesPath` that didn't exist at this scout pass, DEP-1e's removal must be aborted and the field kept — the close-out note should say 're-grep before removing' explicitly.

## Tests

- n/a — docs-only edit for this pass; DEP-1e's own future task will need `go build ./...` + `make test` after the field removal to catch any remaining reference.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n 'CLOSED 2026-08-21' TODO.md` returns 1 hit at the rewritten L10706 area.
- [ ] A new fragment file exists under todo.d/ describing DEP-1e.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_059.md`.

## Commit message

```
refactor(docs): Close out the 2026-05-01 re-audit block (TEST-2/DEP-1/DEAD-1 (TODO L10706)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

This is a housekeeping pass, not new code — the only enduring deliverable is DEP-1e as a fresh small follow-up task, correctly separated out rather than left buried in a stale close-out bullet.
