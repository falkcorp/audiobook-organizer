<!-- file: docs/agent-tasks/todo-completion/server/TASK-209-migrate-internal-server-test-fixtures-to-setupte.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7d3ad532-83b7-4d90-835d-5794c91235d6 -->
<!-- last-edited: 2026-09-02 -->

# TASK-209 — Migrate internal/server test fixtures to setupTestServerWithStore — itunes_integration_test.go, indexed_store_test.go, similar_books_test.go, e2e_workflow_test.go (DEC-6)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — NewServer( still at itunes_integration_test.go:56,124,172,222,256; indexed_store_test.go now 6 sites (51,99,153,234,297,334); similar_books_test.go:42,136; e2e_workflow_test.go:49. Recommendation: keep - indexed_store grew from 4 to 6 sites.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Mechanical, but indexed_store_test.go and similar_books_test.go have multiple sites per file (possibly in table-driven subtests) that need per-site defer placement judged correctly. · **Depends on:** none · **Wave:** 3

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

- setupTestServerWithStore (internal/server/server_test.go:151) sets gin.TestMode, PINS config.AppConfig.RootDir="" (L160-161), calls allowOpDefinitionUpserts, database.SetGlobalStore(store) and NewServer(store); it does NOT start the operations registry and does NOT restore the previous global store.
- itunes_integration_test.go AND e2e_workflow_test.go both build their env via testutil.SetupIntegration(t), which deliberately sets a non-empty RootDir. Both must re-pin config.AppConfig.RootDir after each setupTestServerWithStore call, or be left unmigrated — an empty RootDir also nils the iTunes plugin (internal/plugins/itunes/register.go:52).
- indexed_store_test.go (4 sites) and similar_books_test.go (2 sites) use a plain store with no SetupIntegration and no RootDir dependence — these are the safe, purely mechanical sites in this part.
- Before editing each site, read the ~15 surrounding lines (grep -n -B5 -A10 'NewServer(' <file>) to check for a manual opRegistry.Start/Shutdown pair or other custom wiring that must be preserved rather than deleted.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "NewServer(" internal/server/itunes_integration_test.go   # 5 hits at L56,124,172,222,256 — itunes_integration_test.go has 5 direct NewServer(env.Store) sites
  grep -n "NewServer(" internal/server/indexed_store_test.go   # 4 hits at L51,99,144,183 — indexed_store_test.go has 4 direct NewServer(store) sites
  grep -n "NewServer(" internal/server/similar_books_test.go   # 2 hits at L42,136 — similar_books_test.go has 2 direct NewServer(store) sites
  grep -n "NewServer(" internal/server/e2e_workflow_test.go   # 1 hit at L49 — e2e_workflow_test.go has 1 direct NewServer(env.Store) site
  grep -n 'NewServer(' internal/server/itunes_integration_test.go   # 5 hits at L56,124,172,222,256 — 5 sites in itunes_integration_test.go
  grep -n 'NewServer(' internal/server/indexed_store_test.go   # 4 hits at L51,99,144,183 — 4 sites in indexed_store_test.go
  grep -n 'NewServer(' internal/server/similar_books_test.go   # 2 hits at L42,136 — 2 sites in similar_books_test.go
  grep -n 'NewServer(' internal/server/e2e_workflow_test.go   # 1 hit at L49 — 1 site in e2e_workflow_test.go
  grep -ln 'SetupIntegration' internal/server/*_test.go   # includes e2e_workflow_test.go, itunes_integration_test.go, itunes_error_test.go, organize_integration_test.go — itunes_integration_test.go and e2e_workflow_test.go both come from SetupIntegration, which sets a non-empty RootDir that the fixture blanks
  grep -n 'origCfg := config.AppConfig\|config.AppConfig.RootDir = ""' internal/server/server_test.go   # hits at L160 `origCfg := config.AppConfig` and L161 `config.AppConfig.RootDir = ""` inside setupTestServerWithStore (plus the other fixture's origCfg capture) — the fixture's RootDir pin
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

- Any site fed by testutil.SetupIntegration (itunes_integration_test.go x5, e2e_workflow_test.go x1) loses its RootDir to setupTestServerWithStore's pin. Re-pin it immediately after the call rather than silently dropping the requirement.
- setupTestServerWithStore calls database.SetGlobalStore(store) and its cleanup never restores the prior global store — do not migrate a test that manages the global store itself.
- indexed_store_test.go's 4 sites are 4 separate Test functions, not a table loop (verify with grep -n '^func Test'), so each gets a straightforward defer cleanup().
- Capture a per-test PASS/FAIL baseline before editing and diff it after — 'the package still exits 0' is not the same as 'the same tests still pass'.

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
