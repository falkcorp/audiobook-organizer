<!-- file: docs/agent-tasks/todo-completion/database/TASK-039-omnibus-anthology-book-type-field-part-1-of-the-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6a0201f2-229d-4b69-9878-74922ea06efa -->
<!-- last-edited: 2026-08-21 -->

# TASK-039 — Omnibus/anthology book_type field — Part 1 of the omnibus-detection-and-dedup spec (TODO.md L10523)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · database subagent · **Why:** schema migration + cross-layer (DB/API/FE) field threading on a prod-data path; needs careful review · **Depends on:** none · **Wave:** 3 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10523 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Omnibus detection + dedup** — spec-only" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-14.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-039-omnibus-anthology-book-type-field-part-1-of-the-" -b agent/database-039-omnibus-anthology-book-type-field-part-1-of-the- origin/main
cd "$REPO/.worktrees/database-039-omnibus-anthology-book-type-field-part-1-of-the-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Implement Part 1 of docs/superpowers/specs/2026-05-31-omnibus-detection-and-dedup.md ONLY: add a book_type column (enum: standard/omnibus/anthology/single_file, default 'standard') to the books table via a new migration, expose it on GET/PATCH /api/v1/audiobooks/:id, and add a dropdown editor in the book detail panel plus an 'Omnibuses' quick-filter preset on the Library page. Parts 2 (omnibus↔individual detection algorithm) and 3 (relationship representation without destroying metadata) are explicitly OUT of scope for this item — they remain design-stage per the spec and are not briefed here.

## Background (verify before editing)

- Spec section 'Part 1: book_type Field' (docs/superpowers/specs/2026-05-31-omnibus-detection-and-dedup.md, ~lines 13-30) fully specifies the migration, enum values, and API/UI surface.
- No existing code references book_type anywhere in the repo.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn "book_type\|BookType" internal/database/bookcore.go internal/database/migrations.go   # 0 hits — book_type is not yet a Book field or DB column anywhere
  ```

### Reuse — don't invent

- Use `existing migration numbering/pattern to copy` in `internal/database/migrations.go` (verify: `grep -n "Description:" internal/database/migrations.go | tail -5`) — do NOT write a parallel helper.

## Step-by-step

1. Add a new migration in internal/database/migrations.go following the existing numbered-migration pattern (find the next number via `grep -n "Description:" internal/database/migrations.go | tail -5`) adding `book_type TEXT NOT NULL DEFAULT 'standard'` to the books table.
2. Add `BookType string \`json:"book_type"\`` to the database.Book struct in internal/database/bookcore.go, defaulting to "standard" wherever a new Book is constructed.
3. Thread BookType through CreateBook/UpdateBook/UpsertBook write paths in internal/database/pebble_store.go (mirror how an existing simple string field like Language is handled as a template).
4. Expose book_type as a settable field on PATCH /api/v1/audiobooks/:id in internal/server/handlers/audiobooks/handler.go (mirror the existing metadata-field PATCH handling).
5. Add a book_type dropdown (Standard / Omnibus / Anthology / Single file) to the book detail panel in the web frontend, mirroring the existing pattern for an editable metadata field like Language or Genre.
6. Add an 'Omnibuses' quick-filter preset to the Library page filter UI, following the existing quick-filter-preset pattern (find how another cached-count quick filter is implemented and mirror it).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_039.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Existing books with no book_type set must read back as 'standard', not empty string or null.
- An invalid enum value in a PATCH request must be rejected with a 400, not silently stored.

## Tests

- internal/database migration test: assert a fresh DB has book_type='standard' on a new book and the migration is idempotent on re-run.
- internal/server/handlers/audiobooks handler test: PATCH book_type to 'omnibus' and assert GET reflects it.
- web/src frontend test: dropdown renders all 4 enum values and calls onChange with the selected value.

Anti-over-suppression: N/A

## How to test

```bash
make ci && npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `grep -n "book_type" internal/database/bookcore.go` shows the new field.
- [ ] A PATCH /api/v1/audiobooks/:id with {"book_type":"omnibus"} followed by GET returns book_type="omnibus".
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_039.md`.

## Commit message

```
feat(database): Omnibus/anthology book_type field — Part 1 of the omnibus-de (TODO L10523)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (``grep -n "book_type" internal/database/bookcore.go` shows the new field.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Deliberately scoped to Part 1 only. Parts 2/3 of the spec (detection + non-destructive relationship representation) are large, undesigned-in-detail follow-ons — do not let a Haiku/Sonnet agent try to build the whole spec in one PR.
