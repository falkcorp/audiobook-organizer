<!-- file: docs/agent-tasks/todo-completion/server/TASK-211-migrate-internal-server-test-fixtures-to-setupte.md -->
<!-- version: 1.0.0 -->
<!-- guid: 57edfb88-2092-421a-af0b-626cc28ea80a -->
<!-- last-edited: 2026-08-21 -->

# TASK-211 — Migrate internal/server test fixtures to setupTestServerWithStore — cover_history_test.go, server_middleware_test.go, ai_jobs_handlers_test.go, entity_tag_handlers_test.go, import_collision_test.go, reading_handlers_test.go, user_handlers_test.go, organize_integration_test.go, server_op_registration_test.go, metadata_handlers_test.go (DEC-6)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server subagent · **Why:** 10 files, 1 site each — individually trivial, but 3 of them (ai_jobs, reading, rating) route through their own single-use wrapper helper whose internals must be migrated the same way as Part 3's wrapper files, requiring the same judgment about how to thread cleanup. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 90006 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90006p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-19.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-211-migrate-internal-server-test-fixtures-to-setupte" -b agent/server-211-migrate-internal-server-test-fixtures-to-setupte origin/main
cd "$REPO/.worktrees/server-211-migrate-internal-server-test-fixtures-to-setupte"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace the single direct NewServer(...) construction in each of the 10 files with setupTestServerWithStore(t, store) + defer cleanup() (for the 7 files that construct inline) or migrate the wrapper's internals the same way as Part 3 (for ai_jobs_handlers_test.go's setupAIJobsTestServer, reading_handlers_test.go's setupReadingTestServer, and metadata_handlers_test.go's setupRatingTestServer), removing only exactly-duplicated boilerplate.

## Background (verify before editing)

- cover_history_test.go, server_middleware_test.go, entity_tag_handlers_test.go, import_collision_test.go, user_handlers_test.go, organize_integration_test.go, and server_op_registration_test.go construct their single *Server inline within their one Test function (no wrapper helper) — these are the simplest of the 46 sites to migrate.
- ai_jobs_handlers_test.go's setupAIJobsTestServer (L25) returns (*Server, *database.PebbleStore) — a two-value, non-cleanup-returning signature; its 5 call sites elsewhere in the file must keep receiving those same two values, so t.Cleanup(cleanup) inside the wrapper (rather than changing its signature) is the lower-risk migration.
- reading_handlers_test.go's setupReadingTestServer and metadata_handlers_test.go's setupRatingTestServer are structurally identical single-purpose wrappers, each with a handful of call sites in-file (5 and 2 respectively, per the DEC-6 call-count grep run earlier in this scope's investigation).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "NewServer(" internal/server/cover_history_test.go internal/server/server_middleware_test.go internal/server/ai_jobs_handlers_test.go internal/server/entity_tag_handlers_test.go internal/server/import_collision_test.go internal/server/reading_handlers_test.go internal/server/user_handlers_test.go internal/server/organize_integration_test.go internal/server/server_op_registration_test.go internal/server/metadata_handlers_test.go   # 10 hits total, 1 per file: cover_history_test.go:44, server_middleware_test.go:38, ai_jobs_handlers_test.go:40, entity_tag_handlers_test.go:36, import_collision_test.go:38, reading_handlers_test.go:35, user_handlers_test.go:35, organize_integration_test.go:53, server_op_registration_test.go:46, metadata_handlers_test.go:32 — each of the 10 files has exactly 1 direct NewServer(...) construction site
  grep -n "^func setupAIJobsTestServer\|^func setupReadingTestServer\|^func setupRatingTestServer" internal/server/ai_jobs_handlers_test.go internal/server/reading_handlers_test.go internal/server/metadata_handlers_test.go   # 3 hits: ai_jobs_handlers_test.go:25, reading_handlers_test.go:19, metadata_handlers_test.go:19 — ai_jobs_handlers_test.go, reading_handlers_test.go, and metadata_handlers_test.go each define their own single-use wrapper helper (setupAIJobsTestServer, setupReadingTestServer, setupRatingTestServer) around their site
  grep -n 'config.AppConfig.RootDir = ""\|origStore := database.GetGlobalStore()\|srv := NewServer(store)' internal/server/server_op_registration_test.go   # 3 hits: L34 RootDir pin, L42 origStore := database.GetGlobalStore(), L46 srv := NewServer(store) — each RootDir/store change paired with its own t.Cleanup restore — server_op_registration_test.go deliberately hand-builds to pin the empty-RootDir op-registration regression, and restores the global store, which the fixture does not
  grep -n 'database.SetGlobalStore' internal/server/server_test.go   # 3 hits: L84 and L123 (the OTHER fixture sets, then nils, the global store); L170 in setupTestServerWithStore, whose cleanup restores only fileIOPool, writeBackBatcher and config.AppConfig and never the global store — the fixture pins RootDir empty and never restores the global store
  grep -n 'RootDir' internal/server/cover_history_test.go   # 1 hit at L29: config.AppConfig.RootDir = rootDir — cover_history_test.go sets its own RootDir before constructing
  grep -n 'NewServer(env.Store)\|library.organize' internal/server/organize_integration_test.go   # `server := NewServer(env.Store)` at L53 and the `"def_id":"library.organize"` POST body at L60 — a real-RootDir organize run with its own opRegistry Start/Shutdown — organize_integration_test.go runs library.organize against a real RootDir from SetupIntegration and keeps its own opRegistry start/shutdown
  grep -n 'NewServer(' internal/server/cover_history_test.go internal/server/server_middleware_test.go internal/server/ai_jobs_handlers_test.go internal/server/entity_tag_handlers_test.go internal/server/import_collision_test.go internal/server/reading_handlers_test.go internal/server/user_handlers_test.go internal/server/organize_integration_test.go internal/server/server_op_registration_test.go internal/server/metadata_handlers_test.go   # exactly 10 hits, one per file, at L44,38,40,36,38,35,35,53,46,32 — the 10 claimed single sites, one per file
  grep -n '^func setupAIJobsTestServer\|^func setupReadingTestServer\|^func setupRatingTestServer' internal/server/ai_jobs_handlers_test.go internal/server/reading_handlers_test.go internal/server/metadata_handlers_test.go   # ai_jobs_handlers_test.go:25, reading_handlers_test.go:19, metadata_handlers_test.go:19 — the 3 wrapper definitions
  ```

### Reuse — don't invent

- Use `setupTestServerWithStore(t, store) (*Server, func())` in `internal/server/server_test.go` (verify: `grep -n "func setupTestServerWithStore" internal/server/server_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. EXCLUDE internal/server/server_op_registration_test.go from this migration entirely and record the exclusion in the Coordinator notes alongside fingerprint_rescan_test.go. Its TestNewServer_RegistersOpsWithEmptyRootDir hand-builds on purpose: it pins config.AppConfig.RootDir="" with its own restore (L33-35), saves and restores database.GetGlobalStore() (L41-44) which setupTestServerWithStore never does, and asserts srv.opRegistry.ActiveDefs() != 0. Routing it through the shared fixture would make the empty RootDir incidental rather than asserted.
2. For the 6 remaining inline-construction files (cover_history_test.go:44, server_middleware_test.go:38, entity_tag_handlers_test.go:36, import_collision_test.go:38, user_handlers_test.go:35, organize_integration_test.go:53), replace `<var> := NewServer(<store>)` with `<var>, cleanup := setupTestServerWithStore(t, <store>)` + `defer cleanup()`, deleting ONLY lines that exactly duplicate gin.SetMode / database.SetGlobalStore / allowOpDefinitionUpserts.
3. BEFORE migrating cover_history_test.go and organize_integration_test.go, handle the RootDir pin: setupTestServerWithStore sets config.AppConfig.RootDir="" (internal/server/server_test.go:160-161). cover_history_test.go:29 sets `config.AppConfig.RootDir = rootDir` itself, and organize_integration_test.go uses testutil.SetupIntegration (non-empty RootDir) and then POSTs def_id library.organize, asserting 2 books exist afterwards — an empty RootDir gives the organize op no destination. Re-pin RootDir immediately after the setupTestServerWithStore call in both, and never delete their RootDir assignment as a duplicate.
4. organize_integration_test.go:54-58 also has a manual `if server.opRegistry != nil { Start(...); defer Shutdown }` pair. setupTestServerWithStore does not start the registry, so keep that block verbatim.
5. For ai_jobs_handlers_test.go's setupAIJobsTestServer (L25, NewServer at L40), reading_handlers_test.go's setupReadingTestServer (L19, NewServer at L35) and metadata_handlers_test.go's setupRatingTestServer (L19, NewServer at L32): replace the internal construction with setupTestServerWithStore(t, store) and add t.Cleanup(cleanup) inside the wrapper so the existing signatures and all call sites are untouched.
6. Bump the version header and last-edited: 2026-08-21 on every file actually changed (9 files, not 10).
7. Capture a per-test PASS/FAIL baseline with `go test ./internal/server/... -count=1 -v` before and after, and diff the lists.
8. Add changelog fragment changelog.d/20260821_server_211.md (no file header).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_211.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- setupTestServerWithStore's cleanup restores config.AppConfig but NEVER restores the previous database global store. Any test that saves/restores the global store itself (server_op_registration_test.go) must not be migrated.
- setupRatingTestServer's callers are both inside metadata_handlers_test.go (L38, L86). reset_handler_test.go calls setupHandlerTestServer (L26, L81, L108), which is Part 3's file, not this part's — no cross-file coupling exists here.
- cover_history_test.go and organize_integration_test.go depend on a real config.AppConfig.RootDir; the fixture blanks it.

## Tests

- No new test required — pure fixture refactor. Run every existing test in these 10 files (go test ./internal/server/... -count=1) and confirm identical pass/fail outcomes to a pre-change baseline.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -c "NewServer(" internal/server/cover_history_test.go internal/server/server_middleware_test.go internal/server/ai_jobs_handlers_test.go internal/server/entity_tag_handlers_test.go internal/server/import_collision_test.go internal/server/reading_handlers_test.go internal/server/user_handlers_test.go internal/server/organize_integration_test.go internal/server/server_op_registration_test.go internal/server/metadata_handlers_test.go each returns 0.
- [ ] go build ./... && go vet ./... && go test ./internal/server/... -count=1 exits 0.
- [ ] Combined with Parts 1-3: grep -rn "NewServer(" internal/server --include="*_test.go" | grep -v server_test.go | grep -v fingerprint_rescan_test.go | grep -v httptest.NewServer returns 0 hits across the whole package.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_211.md`.

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

Part 4 of 4 — final part. Combined with Parts 1-3, this closes out all 46 measured hand-built *Server call sites (23 files) except the deliberately-excluded fingerprint_rescan_test.go. After all 4 parts land, TODO-SRVTIMEOUT (internal/server test-package stall) has one fewer duplicated-fixture contributor, though the timeout problem itself is a separate, larger item not fully resolved by this migration alone.
