<!-- file: docs/agent-tasks/todo-completion/server/TASK-134-add-a-wiring-level-test-proving-the-server-actua.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2fc5c716-482a-426b-b63c-8e779db9a808 -->
<!-- last-edited: 2026-08-21 -->

# TASK-134 — Add a wiring-level test proving the server actually constructs CancelOperationV2 with AI-scan cancellation attached (TODO.md L4449)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Requires constructing a real *aiscan.PipelineManager and *database.AIScanStore (not just interface fakes) and driving them through the actual wireHandlers/setupRoutes path — more setup than a typical handler unit test, though the pieces are all documented and reusable. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4449 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**The AI-scan cancel wiring is unverified, and it " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-08.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-134-add-a-wiring-level-test-proving-the-server-actua" -b agent/server-134-add-a-wiring-level-test-proving-the-server-actua origin/main
cd "$REPO/.worktrees/server-134-add-a-wiring-level-test-proving-the-server-actua"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a test in package server (alongside server_queue_test.go's pattern) that constructs a *Server with a real *aiscan.PipelineManager (backed by a fake internal aiscan.Store implementing its 7-method interface) and a real *database.AIScanStore (via database.NewAIScanStore(t.TempDir())), creates a scan linked to a known operation id via UpdateScanOperationID, calls srv.setupRoutes(), and issues DELETE /api/v1/operations/v2/<that-id> through the real router — then asserts the fake aiscan.Store's cancellation actually fired (not just that the response was 204). This closes the gap the TODO item correctly identifies even though its stated cause (concrete-type handler deps) is already fixed.

## Background (verify before editing)

- internal/server/handlers/operations_v2.go:282-301 (CancelOperationV2) already special-cases an AI scan: it lists scans via h.scanLister, matches OperationID against the requested id, and calls h.scanCanceler.CancelScan(scan.ID) before falling through to the ops registry.
- internal/server/wire_handlers.go:164-176 already boxes s.pipelineManager/s.aiScanStore into the narrow interfaces and passes them to handlers.NewOperationsV2Handler via WithAIScanCancellation — this is the exact line a future refactor could silently drop.
- internal/aiscan.PipelineManager.CancelScan (internal/aiscan/pipeline.go:57) cancels via a stored context.CancelFunc keyed by scan id (pm.cancels map), so a real PipelineManager needs an actual in-flight cancel func registered for the scan id under test — or the test can assert on the *aiscan.Store's UpdateOperationStatus call chain instead of a running goroutine, whichever is simpler to wire.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'type ScanCanceler interface\|type AIScanLister interface' internal/server/handlers/operations_v2.go   # 2 hits at L73 and L79 — ScanCanceler and AIScanLister interfaces already exist and are narrow
  grep -n 'v2Pipeline\|v2ScanStore\|WithAIScanCancellation' internal/server/wire_handlers.go   # hits at L164,166,168,170,176 — wire_handlers.go already narrows s.pipelineManager/s.aiScanStore into those interfaces before passing WithAIScanCancellation
  grep -rn 'pipelineManager\s*=\|aiScanStore\s*=' internal/server/*_test.go   # 0 hits — no existing test constructs a *Server with real pipelineManager/aiScanStore and drives the route through wireHandlers
  grep -n 'func NewPipelineManager' internal/aiscan/pipeline.go   # 1 hit at L47, params (scanStore *database.AIScanStore, mainStore Store, parser *ai.OpenAIParser) — a real *aiscan.PipelineManager can be constructed with a fake narrow Store and a real *database.AIScanStore for a test
  grep -n 'v2Pipeline\|v2ScanStore\|WithAIScanCancellation' internal/server/wire_handlers.go   # hits at L164,166,168,170,176 — the wiring line the new test must guard exists
  grep -n 'type ScanCanceler interface\|type AIScanLister interface' internal/server/handlers/operations_v2.go   # L73 and L79 — the narrow interfaces already exist
  ls internal/server/wire_handlers_test.go   # No such file or directory (exit 1) — no test file exists yet, so the -run acceptance must be replaced by a file-existence + named-test grep
  grep -rn 'pipelineManager\s*=\|aiScanStore\s*=' internal/server/*_test.go   # 0 hits — no existing test constructs a Server with a real pipelineManager/aiScanStore
  ```

### Reuse — don't invent

- Use `database.NewAIScanStore(path) — standalone AIScanStore backed by a temp Pebble file, usable in a test with t.TempDir()` in `internal/database/ai_scan_store.go` (verify: `grep -n 'func NewAIScanStore' internal/database/ai_scan_store.go`) — do NOT write a parallel helper.
- Use `AIScanStore.UpdateScanOperationID(id, operationID) — links a scan to the operation id CancelOperationV2 receives` in `internal/database/ai_scan_store.go` (verify: `grep -n 'func.*UpdateScanOperationID' internal/database/ai_scan_store.go`) — do NOT write a parallel helper.
- Use `&Server{router: gin.New(), store: mockStore, ...}; srv.setupRoutes() pattern for route-level wiring tests` in `internal/server/server_queue_test.go` (verify: `grep -n 'TestCancelOperationV2_NilRegistry' internal/server/server_queue_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. Create internal/server/wire_handlers_test.go (package server) with a fresh file header (version 1.0.0, new guid from `uuidgen | tr A-Z a-z`, last-edited: 2026-08-21).
2. Define a minimal fake implementing internal/aiscan.Store's 7 methods (CreateOperation, UpdateOperationStatus, UpdateOperationError, GetAllAuthors, GetAuthorByID, GetAllAuthorBookCounts, GetBooksByAuthorIDWithRoleCore); record UpdateOperationStatus calls.
3. scanStore, err := database.NewAIScanStore(t.TempDir()) (internal/database/ai_scan_store.go:91); create a scan; call scanStore.UpdateScanOperationID(scan.ID, "op-under-test") — note the signature is UpdateScanOperationID(id int, operationID string) (ai_scan_store.go:257), so scan.ID is an int, not a string.
4. pm := aiscan.NewPipelineManager(scanStore, fakeStore, nil) (internal/aiscan/pipeline.go:47); confirm CancelScan does not dereference the parser before passing nil.
5. Build srv := &Server{router: gin.New(), store: mockStore, pipelineManager: pm, aiScanStore: scanStore, opRegistry: <mock registry with no expectations>} and call srv.setupRoutes(), mirroring TestCancelOperationV2_NilRegistry (internal/server/server_queue_test.go:27).
6. Issue DELETE /api/v1/operations/v2/op-under-test via srv.router.ServeHTTP.
7. Assert BOTH the 204 status AND that the fake recorded the cancellation — the failure this test exists to catch is '204 returned but nothing was signalled'.
8. Do NOT edit internal/server/wire_handlers.go — this brief is test-only. Verify the test really guards the wiring by temporarily deleting handlers.WithAIScanCancellation(...) at wire_handlers.go:176, confirming the new test goes red, then restoring it.
9. Add changelog fragment changelog.d/20260821_server_134.md (no file header).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_134.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Unknown operation id (no matching scan) must fall through to the registry, not be swallowed — already covered by the existing handler-level test TestCancelOperationV2_FallsThroughToTheRegistryForAnOrdinaryOp; the new wiring test does not need to re-prove this, only that the AI-scan path is reachable at all through the real server.

## Tests

- {'file': 'internal/server/wire_handlers_test.go', 'name': 'TestWireHandlers_CancelOperationV2ReachesAIScanPipeline (new)', 'asserts': "with a real pipelineManager+aiScanStore wired into *Server exactly as wireHandlers does it, DELETE /operations/v2/:id for an id matching a scan's OperationID actually signals the scan (not just 204 blindly), proving the WithAIScanCancellation call site is still present and correctly fed"}

Anti-over-suppression test: `N/A — this is a coverage-gap fix, not a filter/guard.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/... -run TestWireHandlers_CancelOperationV2ReachesAIScanPipeline` passes.
- [ ] Deleting `handlers.WithAIScanCancellation(v2Pipeline, v2ScanStore)` from wire_handlers.go:176 (as a manual sabotage check) makes the new test fail — confirming it actually guards the wiring line, not just the handler's internal logic (which internal/server/handlers/operations_v2_test.go already covers).
- [ ] Anti-over-suppression test: `N/A — this is a coverage-gap fix, not a filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_134.md`.

## Commit message

```
feat(server): Add a wiring-level test proving the server actually construc (TODO L4449)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'type ScanCanceler interface\|type AIScanLister interface' internal/server/handlers/operations_v2.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The TODO item's stated root cause (concrete types blocking substitution) is already resolved; only reword this when folding into the assembled backlog so the next reader doesn't waste time re-narrowing interfaces that are already narrow.
