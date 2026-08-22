<!-- file: docs/agent-tasks/todo-completion/server/TASK-210-migrate-internal-server-test-fixtures-to-setupte.md -->
<!-- version: 1.0.0 -->
<!-- guid: c00ac26c-e6fd-4a9d-acd1-6692bb6139ce -->
<!-- last-edited: 2026-08-21 -->

# TASK-210 — Migrate internal/server test fixtures to setupTestServerWithStore — server_coverage_phase2_test.go, deluge_integration_test.go, search_reconciler_test.go, maintenance_window_handlers_test.go, user_tags_authz_test.go, playlist_handlers_test.go, handlers_integration_test.go (DEC-6)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server subagent · **Why:** server_coverage_phase2_test.go's 4 sites live inside `for _, tt := range ...` subtest loops (confirm with the surrounding braces before editing) and already have their own manual allowOpDefinitionUpserts call that becomes redundant post-migration — a purely mechanical find-replace would leave a harmless but sloppy duplicate call; a Sonnet-tier read of the surrounding ~10 lines per site is warranted. The 4 wrapper-helper files (maintenance/user_tags_authz/playlist/handlers_integration) require deciding whether to migrate the WRAPPER's internals (keep the file's distinct helper name and signature, delegate its body to setupTestServerWithStore) or retire the wrapper and repoint its call sites directly — the wrapper's own return signature (user_tags_authz_test.go's five-tuple return, for instance) makes 'delegate internals, keep signature' the lower-risk choice; document that choice explicitly per file. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 90006 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90006p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-19.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-210-migrate-internal-server-test-fixtures-to-setupte" -b agent/server-210-migrate-internal-server-test-fixtures-to-setupte origin/main
cd "$REPO/.worktrees/server-210-migrate-internal-server-test-fixtures-to-setupte"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For server_coverage_phase2_test.go, deluge_integration_test.go, and search_reconciler_test.go, replace each direct NewServer(...) construction with setupTestServerWithStore(t, store) the same way as Parts 1-2, removing the now-redundant manual allowOpDefinitionUpserts(mockStore) calls that precede each of server_coverage_phase2_test.go's 4 sites (setupTestServerWithStore performs this call internally — see server_test.go:167). For the 4 files with their own duplicate 'setup*TestServer' wrapper helper (setupMaintenanceTestServer, setupUserTagsAuthzTestServer, setupPlaylistTestServer, setupHandlerTestServer), keep each wrapper's existing name and return signature (its call sites elsewhere in the same file must not change) but rewrite its INTERNAL body to construct via setupTestServerWithStore instead of hand-rolling NewServer + manual global-state wiring, so the file has exactly one place performing raw construction.

## Background (verify before editing)

- server_coverage_phase2_test.go's 4 sites (L76,134,168,223) are the clearest evidence of the exact bug class DEC-6 targets: each is preceded one line earlier by a manual `allowOpDefinitionUpserts(mockStore)` call (L75,133,167,222) — this is precisely the boilerplate setupTestServerWithStore already performs internally (server_test.go:167), duplicated by hand at every call site instead of being reused.
- The 4 wrapper-helper files each define ONE function (setupMaintenanceTestServer, setupUserTagsAuthzTestServer, setupPlaylistTestServer, setupHandlerTestServer) that other test functions in the SAME file call repeatedly (call counts: 4, 2, 10, 13 respectively, per grep -rn '<fnName>(' <file> --include="*_test.go") — migrating the wrapper's internals (not its call sites) is a single edit that benefits every downstream caller in that file with no call-site churn.
- user_tags_authz_test.go's wrapper has a 5-value return signature (srv, adminToken, viewerToken, bookID, cleanup) — its body does more than construct a server (it also seeds sessions and a book); only the construction portion should delegate to setupTestServerWithStore, the seeding logic stays as-is.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "NewServer(\|allowOpDefinitionUpserts(" internal/server/server_coverage_phase2_test.go   # 8 hits: allowOpDefinitionUpserts at L75,133,167,222 each immediately preceding NewServer at L76,134,168,223 — server_coverage_phase2_test.go has 4 mockStore-based NewServer sites, each already manually calling allowOpDefinitionUpserts(mockStore) first — direct proof of the exact duplication DEC-6 targets
  grep -n "NewServer(store)" internal/server/deluge_integration_test.go   # 2 hits at L113,161 — deluge_integration_test.go has 2 real NewServer(store) sites (plus unrelated httptest.NewServer mock-backend sites, not counted)
  grep -n "NewServer(store)" internal/server/search_reconciler_test.go   # 2 hits at L46,242 — search_reconciler_test.go has 2 NewServer(store) sites
  grep -n "^func setupMaintenanceTestServer\|^func setupUserTagsAuthzTestServer\|^func setupPlaylistTestServer\|^func setupHandlerTestServer" internal/server/maintenance_window_handlers_test.go internal/server/user_tags_authz_test.go internal/server/playlist_handlers_test.go internal/server/handlers_integration_test.go   # 4 hits, one per file: maintenance_window_handlers_test.go:26, user_tags_authz_test.go:38, playlist_handlers_test.go:24, handlers_integration_test.go:190 — 4 files each define their own duplicate setup*TestServer wrapper that calls NewServer directly
  ```

### Reuse — don't invent

- Use `setupTestServerWithStore(t, store) (*Server, func())` in `internal/server/server_test.go` (verify: `grep -n "func setupTestServerWithStore" internal/server/server_test.go`) — do NOT write a parallel helper.
- Use `allowOpDefinitionUpserts(store) — already called internally by setupTestServerWithStore, making the manual calls in server_coverage_phase2_test.go redundant once migrated` in `internal/server/server_test.go` (verify: `grep -n "func allowOpDefinitionUpserts" internal/server/server_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. In server_coverage_phase2_test.go, at each of the 4 sites (L76,134,168,223), delete the preceding `allowOpDefinitionUpserts(mockStore)` line and replace `srv := NewServer(mockStore)` with `srv, cleanup := setupTestServerWithStore(t, mockStore); defer cleanup()`. Verify each site is inside its own subtest/loop iteration body first (grep -n -B15 'NewServer(mockStore)' internal/server/server_coverage_phase2_test.go) so defer cleanup() lands inside that scope, not once outside a shared loop.
2. In deluge_integration_test.go (L113,161) and search_reconciler_test.go (L46,242), apply the same NewServer(store) -> setupTestServerWithStore(t, store) + defer cleanup() replacement as Parts 1-2, deleting only exact-duplicate boilerplate.
3. In maintenance_window_handlers_test.go's setupMaintenanceTestServer (L26-...), find its internal `srv := NewServer(store)` (L41) and any manual config/global-store lines above it; replace the construction with `srv, cleanup := setupTestServerWithStore(t, store)`, thread `cleanup` into whatever this wrapper already returns to its callers (check its current signature and whether it currently returns a cleanup func at all — if it doesn't, either add one and update its ~4 call sites to `defer cleanup()`, or call `t.Cleanup(cleanup)` inside the wrapper itself so the signature does not need to change — prefer t.Cleanup(cleanup) to avoid touching call sites).
4. Repeat step 3's approach for setupUserTagsAuthzTestServer (user_tags_authz_test.go:38, NewServer at L79 — preserve the session/book seeding logic that follows construction unchanged, only replace the construction line and add t.Cleanup(cleanup) or thread the returned cleanup into the existing 5th return value slot), setupPlaylistTestServer (playlist_handlers_test.go:24, NewServer at L45), and setupHandlerTestServer (handlers_integration_test.go:190, NewServer at L220).
5. Bump version headers on all 7 files.
6. List Test function names in each file and run: go build ./... && go vet ./... && go test ./internal/server/... -run '<discovered Test names>' -count=1.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_210.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If t.Cleanup(cleanup) is used inside a wrapper helper instead of returning cleanup explicitly, verify the wrapper's existing callers that currently do `defer cleanup()` on a returned value are updated (removed) consistently — do not leave a double-cleanup (t.Cleanup registered inside the helper AND a stale `defer cleanup()` at the call site referencing a variable that no longer exists, which would simply fail to compile and surface immediately).
- handlers_integration_test.go's setupHandlerTestServer uses mockDB (grep -n 'NewServer(mockDB)' internal/server/handlers_integration_test.go) — confirm mockDB is the same *mocks.MockStore type allowOpDefinitionUpserts type-asserts against; if it is a different mock type, setupTestServerWithStore's allowOpDefinitionUpserts call becomes a silent no-op (safe) rather than an error, but note this explicitly rather than assuming.

## Tests

- No new test required — pure fixture refactor. Run every existing test in these 7 files (go test ./internal/server/... -count=1) and confirm identical pass/fail outcomes to a pre-change baseline, with special attention to server_coverage_phase2_test.go's mock-based subtests (a missed allowOpDefinitionUpserts equivalent would surface as a testify FailNow on an unexpected UpsertOpDefinitionV2 call).

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -c "NewServer(mockStore)" internal/server/server_coverage_phase2_test.go returns 0.
- [ ] grep -c "NewServer(store)" internal/server/deluge_integration_test.go internal/server/search_reconciler_test.go internal/server/maintenance_window_handlers_test.go internal/server/playlist_handlers_test.go each returns 0.
- [ ] grep -c "NewServer(" internal/server/user_tags_authz_test.go internal/server/handlers_integration_test.go each returns 0.
- [ ] go build ./... && go vet ./... && go test ./internal/server/... -count=1 exits 0.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_210.md`.

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

Part 3 of 4. See Part 1's notes for the full 46-site/23-file breakdown and the fingerprint_rescan_test.go exclusion. This part additionally covers 4 of the 8 duplicate wrapper-helper files identified at HEAD (the other 4 — ai_jobs_handlers_test.go, reading_handlers_test.go, metadata_handlers_test.go's setupRatingTestServer, and fingerprint_rescan_test.go's excluded newRescanTestServer — are in Part 4 or excluded).
