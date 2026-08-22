<!-- file: docs/agent-tasks/todo-completion/server/TASK-137-fix-testorganizeservice-performorganize-nobookst.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3384ccc6-0a32-47db-bf7e-cf38c1dd2800 -->
<!-- last-edited: 2026-08-21 -->

# TASK-137 — Fix TestOrganizeService_PerformOrganize_NoBooksToOrganize to mock the method PerformOrganize actually calls (TODO.md L4732)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server subagent · **Why:** Swap one mock field name for the correct one and add a real assertion; mechanical once the right field is identified (already identified above). · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4732 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "⚠ `internal/server/organize_service_test.go:34` — " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-137-fix-testorganizeservice-performorganize-nobookst" -b agent/server-137-fix-testorganizeservice-performorganize-nobookst origin/main
cd "$REPO/.worktrees/server-137-fix-testorganizeservice-performorganize-nobookst"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Fix TestOrganizeService_PerformOrganize_NoBooksToOrganize to mock GetAllBooksCoreFunc (what PerformOrganize actually calls) instead of GetAllBooksFunc, and strengthen its assertions so a broken PerformOrganize would actually fail the test.

## Background (verify before editing)

- req.BookIDs is empty in the test (`req := &OrganizeRequest{}`), so PerformOrganize takes the `else` branch at service.go:257-278 which pages through GetAllBooksCore, not the per-ID GetBookByID branch.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "GetAllBooksFunc" internal/server/organize_service_test.go   # 1 hit ~L35 — the test sets the wrong mock field
  grep -n "orgSvc.db.GetAllBooksCore" internal/organizer/service.go   # 2 hits ~L259, L307 (initial fetch + metadata re-fetch) — PerformOrganize actually calls GetAllBooksCore, not GetAllBooks
  grep -n "func (m \*MockStore) GetAllBooksCore" -A 5 internal/database/mock_store.go   # 1 hit ~L724, body `if m.GetAllBooksCoreFunc != nil {...}; return nil, nil` — MockStore.GetAllBooksCore defaults to nil,nil when its own Func field is unset
  grep -n "type OrganizeService = organizer.Service\|func NewOrganizeService" internal/audiobooks/organize.go   # 2 hits ~L23, L43 — OrganizeService is a type alias to organizer.Service, and NewOrganizeService wraps organizer.NewService
  grep -n "func TestOrganizeService_PerformOrganize_NoBooksToOrganize" -A 15 internal/server/organize_service_test.go   # 1 hit; only `if err != nil { t.Errorf(...) }` present — the test asserts only err == nil, nothing about book count or organized results
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Open internal/server/organize_service_test.go.
2. In TestOrganizeService_PerformOrganize_NoBooksToOrganize (~L33-48), replace `GetAllBooksFunc: func(limit, offset int) ([]database.Book, error) { return []database.Book{}, nil }` with `GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) { return []database.BookCore{}, nil }` (matching MockStore's actual GetAllBooksCoreFunc signature — verify the exact param/return types via `grep -n "GetAllBooksCoreFunc " internal/database/mock_store.go` before writing this).
3. Add a second test, TestOrganizeService_PerformOrganize_WithBooks, that sets GetAllBooksCoreFunc to return a non-empty []database.BookCore slice (1-2 books with a FilePath needing organization) plus whatever other MockStore Funcs PerformOrganize's happy path touches (check organizer/service.go's other orgSvc.db.* calls in the non-BookIDs branch — e.g. UpdateBook, snapshot/backup calls — and stub each with a Func that records it was called), then assert PerformOrganize returns nil error AND that the recorded calls happened — this is the anti-over-suppression twin so the suite cannot silently regress back to a vacuous 'zero books, err==nil' pass.
4. Bump the file's version header and last-edited date.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_137.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- PerformOrganize's paging loop (service.go ~L257-278) breaks when `len(page) < fetchPageSize` (1000) — a 1-2 book fake page satisfies this on the first iteration, so the new WithBooks test does not need to simulate pagination across multiple pages.

## Tests

- TestOrganizeService_PerformOrganize_NoBooksToOrganize (fixed): mocks GetAllBooksCoreFunc, asserts err == nil.
- TestOrganizeService_PerformOrganize_WithBooks (new): mocks a non-empty GetAllBooksCoreFunc plus PerformOrganize's other store dependencies, asserts err == nil AND that book-processing side effects (via recorded Func calls) actually occurred.

Anti-over-suppression test: `TestOrganizeService_PerformOrganize_WithBooks` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/server/... -run TestOrganizeService_PerformOrganize -v exits 0 and both test names appear in the -v output.
- [ ] Manually verify the anti-over-suppression property: temporarily break PerformOrganize's book-fetch loop (e.g. force an early `return nil`) and confirm TestOrganizeService_PerformOrganize_WithBooks goes red while _NoBooksToOrganize stays green — then revert the temporary break.
- [ ] Anti-over-suppression test: `TestOrganizeService_PerformOrganize_WithBooks` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_137.md`.

## Commit message

```
fix(server): Fix TestOrganizeService_PerformOrganize_NoBooksToOrganize to (TODO L4732)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This scope's L4728 item (MockStore Func-field audit) is a prerequisite in spirit but not a hard dependency: GetAllBooksCoreFunc already exists on MockStore today, so this fix does not need L4728 to land first.
