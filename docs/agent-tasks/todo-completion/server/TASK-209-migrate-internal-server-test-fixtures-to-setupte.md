<!-- file: docs/agent-tasks/todo-completion/server/TASK-209-migrate-internal-server-test-fixtures-to-setupte.md -->
<!-- version: 1.0.0 -->
<!-- guid: 528954e9-22bb-4d65-8268-8db90a3afe0d -->
<!-- last-edited: 2026-08-21 -->

# TASK-209 — Migrate internal/server test fixtures to setupTestServerWithStore — itunes_integration_test.go, indexed_store_test.go, similar_books_test.go, e2e_workflow_test.go (DEC-6)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Mechanical, but indexed_store_test.go and similar_books_test.go have multiple sites per file (possibly in table-driven subtests) that need per-site defer placement judged correctly. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 90006 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90006p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-19.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-209-migrate-internal-server-test-fixtures-to-setupte" -b agent/server-209-migrate-internal-server-test-fixtures-to-setupte origin/main
cd "$REPO/.worktrees/server-209-migrate-internal-server-test-fixtures-to-setupte"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Same transformation as Part 1, applied to itunes_integration_test.go (5 sites), indexed_store_test.go (4 sites), similar_books_test.go (2 sites), e2e_workflow_test.go (1 site): replace each direct NewServer(store)/NewServer(env.Store) with setupTestServerWithStore(t, store) + defer cleanup(), deleting only exactly-duplicated boilerplate (gin.SetMode/database.SetGlobalStore/allowOpDefinitionUpserts) that setupTestServerWithStore already performs, and preserving anything it does not (e.g. manual opRegistry start/shutdown, if present).

## Background (verify before editing)

- Same base facts as Part 1's background — setupTestServerWithStore (server_test.go:151) is the real shared fixture; it does not start the operations registry.
- Before editing each site, read the ~15 surrounding lines (grep -n -B5 -A10 "NewServer(" <file>) to check for a manual opRegistry.Start/Shutdown pair (as seen in itunes_error_test.go in Part 1) or other custom wiring that must be preserved rather than deleted.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "NewServer(" internal/server/itunes_integration_test.go   # 5 hits at L56,124,172,222,256 — itunes_integration_test.go has 5 direct NewServer(env.Store) sites
  grep -n "NewServer(" internal/server/indexed_store_test.go   # 4 hits at L51,99,144,183 — indexed_store_test.go has 4 direct NewServer(store) sites
  grep -n "NewServer(" internal/server/similar_books_test.go   # 2 hits at L42,136 — similar_books_test.go has 2 direct NewServer(store) sites
  grep -n "NewServer(" internal/server/e2e_workflow_test.go   # 1 hit at L49 — e2e_workflow_test.go has 1 direct NewServer(env.Store) site
  ```

### Reuse — don't invent

- Use `setupTestServerWithStore(t, store) (*Server, func())` in `internal/server/server_test.go` (verify: `grep -n "func setupTestServerWithStore" internal/server/server_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. For each of the 12 cited line numbers across the 4 files, replace the NewServer(...) construction with `<var>, cleanup := setupTestServerWithStore(t, <store-var>)` followed by `defer cleanup()`, keeping the original result variable name (srv/server).
2. Before deleting any surrounding line, confirm it duplicates exactly what setupTestServerWithStore already does (gin.SetMode(gin.TestMode), database.SetGlobalStore(store), allowOpDefinitionUpserts(store)) — delete only those exact duplicates.
3. If indexed_store_test.go's or similar_books_test.go's sites are inside a `for _, tt := range ...` or `t.Run(...)` subtest body, ensure `defer cleanup()` is placed INSIDE that body (each subtest/iteration gets its own cleanup), not once at the outer function scope.
4. Bump version headers on all 4 files.
5. List each file's Test function names first (grep -n "^func Test" <file>) and run: go build ./... && go vet ./... && go test ./internal/server/... -run '<the discovered Test names joined with |>' -count=1.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_209.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- indexed_store_test.go's 4 sites (L51,99,144,183) are likely 4 separate Test functions, not one table loop — verify with grep -n "^func Test" internal/server/indexed_store_test.go before assuming a shared-loop pattern; if they are indeed 4 separate functions, each gets its own straightforward defer cleanup() with no loop-scoping concern.
- e2e_workflow_test.go likely exercises a longer end-to-end flow — confirm its single site's surrounding context doesn't rely on a specific NewServer(...) side effect (e.g. a particular initial config.AppConfig.RootDir) that setupTestServerWithStore's config-pinning (RootDir="", server_test.go:161) would change; if the test needs a non-empty RootDir, keep a manual override line after the setupTestServerWithStore call rather than silently dropping the requirement.

## Tests

- No new test required — pure fixture refactor. Run the existing tests in all 4 files and confirm identical pass/fail outcomes to a pre-change baseline: go test ./internal/server/... -count=1 (or scoped -run to just these files' Test funcs).

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -c "NewServer(" internal/server/itunes_integration_test.go internal/server/indexed_store_test.go internal/server/similar_books_test.go internal/server/e2e_workflow_test.go each returns 0.
- [ ] go build ./... && go vet ./... && go test ./internal/server/... -count=1 exits 0.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_209.md`.

## Commit message

```
fix(server): Migrate internal/server test fixtures to setupTestServerWith (DEC-6)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Part 2 of 4. See Part 1's notes for the full 46-site/23-file breakdown and the fingerprint_rescan_test.go exclusion.
