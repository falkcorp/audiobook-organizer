<!-- file: docs/agent-tasks/todo-completion/server/TASK-208-migrate-internal-server-test-fixtures-to-setupte.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c56c35e-39d9-41da-af66-010464e3ef24 -->
<!-- last-edited: 2026-08-21 -->

# TASK-208 — Migrate internal/server test fixtures to setupTestServerWithStore — itunes_error_test.go, version_lifecycle_test.go (DEC-6)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Mechanical fixture consolidation across two files, but itunes_error_test.go's 11 sites each hand-roll manual opRegistry.Start/Shutdown boilerplate that must be judged, not blindly deleted — a haiku-class run is likely to either strip needed opRegistry wiring or leave true duplicates behind. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 90006 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90006p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-19.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-208-migrate-internal-server-test-fixtures-to-setupte" -b agent/server-208-migrate-internal-server-test-fixtures-to-setupte origin/main
cd "$REPO/.worktrees/server-208-migrate-internal-server-test-fixtures-to-setupte"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace every ad-hoc `server := NewServer(env.Store)` / `srv := NewServer(store)` construction in itunes_error_test.go and version_lifecycle_test.go with a call to the already-existing shared fixture `setupTestServerWithStore(t, store)` (internal/server/server_test.go:151), removing any now-redundant manual boilerplate it already performs (gin.SetMode(gin.TestMode), database.SetGlobalStore(store), allowOpDefinitionUpserts(store) for mock stores) while preserving anything setupTestServerWithStore does NOT do (most notably: it does not start the operations registry — see server_test.go:100-102's comment that only setupTestServerFS does that — so itunes_error_test.go's manual `server.opRegistry.Start(context.Background())` / deferred Shutdown pairs must be kept as-is).

## Background (verify before editing)

- internal/server/server_test.go:151 defines setupTestServerWithStore(t, store) (*Server, func()) — sets gin.TestMode, calls allowOpDefinitionUpserts(store), database.SetGlobalStore(store), NewServer(store), and returns a cleanup that stops fileIOPool/writeBackBatcher and restores config.AppConfig.
- itunes_error_test.go builds its env via testutil.SetupIntegration(t) (grep -n "testutil.SetupIntegration" internal/server/itunes_error_test.go), then at 11 separate TestITunesImport_* functions calls `server := NewServer(env.Store)` directly followed by a manual opRegistry.Start/Shutdown pair — that manual pair is NOT something setupTestServerWithStore performs, so it must be kept.
- version_lifecycle_test.go's single call site at L37 constructs from a plain store with no such extra wiring, per a quick read of its surrounding function (verify before editing: grep -n -B5 -A5 "NewServer(" internal/server/version_lifecycle_test.go).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn "func newTestServer" internal/server   # 0 hits — no function literally named newTestServer exists
  grep -n "func setupTestServerWithStore" internal/server/server_test.go   # 1 hit at L151 — setupTestServerWithStore is the real, already-heavily-reused shared fixture
  grep -n "NewServer(" internal/server/itunes_error_test.go   # 11 hits at L34,66,83,130,162,175,201,217,233,259,283 — itunes_error_test.go has 11 direct NewServer(env.Store) construction sites
  grep -n "NewServer(" internal/server/version_lifecycle_test.go   # 1 hit at L37 — version_lifecycle_test.go has 1 direct NewServer(store) construction site
  ```

### Reuse — don't invent

- Use `setupTestServerWithStore(t, store) (*Server, func())` in `internal/server/server_test.go` (verify: `grep -n "func setupTestServerWithStore" internal/server/server_test.go`) — do NOT write a parallel helper.
- Use `allowOpDefinitionUpserts(store) — already called internally by setupTestServerWithStore for MockStore` in `internal/server/server_test.go` (verify: `grep -n "func allowOpDefinitionUpserts" internal/server/server_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. Open internal/server/itunes_error_test.go. For each of the 11 lines (34,66,83,130,162,175,201,217,233,259,283), replace `server := NewServer(env.Store)` with `server, cleanup := setupTestServerWithStore(t, env.Store)` followed immediately by `defer cleanup()`.
2. Immediately after each replacement, keep the existing `if server.opRegistry != nil { server.opRegistry.Start(context.Background()); defer func() { _ = server.opRegistry.Shutdown(context.Background()) }() }` block UNCHANGED — setupTestServerWithStore does not start the registry, so this remains necessary. Do not delete it.
3. Confirm no line in the same test function duplicates gin.SetMode/database.SetGlobalStore/allowOpDefinitionUpserts already covered by setupTestServerWithStore — remove only exact duplicates, nothing else (grep -n -A3 -B3 each construction site before editing to check).
4. Repeat for internal/server/version_lifecycle_test.go:37 — replace the construction with setupTestServerWithStore(t, store), add defer cleanup(), and verify (by reading the surrounding ~15 lines) whether any manual opRegistry wiring exists there too; if none, no extra step is needed.
5. Bump the version header and last-edited date on both files (per CLAUDE.md file-header rule).
6. Run: go build ./... && go vet ./... && go test ./internal/server/... -run 'TestITunesImport|TestServerVersionLifecycle' -count=1 (adjust the -run pattern to the actual Test function names in these two files if they differ — list them first with grep -n "^func Test" internal/server/itunes_error_test.go internal/server/version_lifecycle_test.go).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_208.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A construction site inside a subtest table loop (none in these two files per the anchors above, but verify before editing) would need its defer cleanup() inside the loop body, not once outside — check the immediate surrounding braces of each site before applying the mechanical replacement.
- If env.Store is ever nil in a given test (unlikely given testutil.SetupIntegration's contract, but verify), setupTestServerWithStore must be given the same nil-safety it already has for MockStore-typed stores — the type assertion in allowOpDefinitionUpserts (`ms, ok := store.(*mocks.MockStore)`) simply fails the assertion and is a no-op for a non-mock, non-nil store, which is the expected path here.

## Tests

- No new test is required — this is a pure fixture-construction refactor. Existing tests in itunes_error_test.go and version_lifecycle_test.go must pass unchanged: run go test ./internal/server/... -run 'TestITunesImport' -count=1 and confirm identical pass/fail outcomes to a pre-change baseline run.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -c "NewServer(" internal/server/itunes_error_test.go returns 0 (all 11 sites migrated; the only remaining NewServer( reference, if any, would be inside setupTestServerWithStore itself, which is not in this file).
- [ ] grep -c "NewServer(" internal/server/version_lifecycle_test.go returns 0.
- [ ] go build ./... && go vet ./... && go test ./internal/server/... -count=1 exits 0.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_208.md`.

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

Part 1 of 4 (DEC-6 split by disjoint file sets for collision-free parallel waves). Total measured hand-built *Server call sites across internal/server: 46, across 23 files — split as Part1=12 (itunes_error_test.go, version_lifecycle_test.go), Part2=12, Part3=12, Part4=10. fingerprint_rescan_test.go's newRescanTestServer is deliberately EXCLUDED from all 4 parts (it hand-builds a bare &Server{store: mockStore} on purpose, per its own doc comment, to avoid wiring the operations queue — migrating it would change TestFingerprintRescan_* behavior).
