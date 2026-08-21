<!-- file: docs/agent-tasks/todo-completion/server/TASK-134-add-a-wiring-level-test-proving-the-server-actua.md -->
<!-- version: 1.0.0 -->
<!-- guid: bdfeed34-2f6c-4c62-8b28-6a6f65788be5 -->
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
  ```

### Reuse — don't invent

- Use `database.NewAIScanStore(path) — standalone AIScanStore backed by a temp Pebble file, usable in a test with t.TempDir()` in `internal/database/ai_scan_store.go` (verify: `grep -n 'func NewAIScanStore' internal/database/ai_scan_store.go`) — do NOT write a parallel helper.
- Use `AIScanStore.UpdateScanOperationID(id, operationID) — links a scan to the operation id CancelOperationV2 receives` in `internal/database/ai_scan_store.go` (verify: `grep -n 'func.*UpdateScanOperationID' internal/database/ai_scan_store.go`) — do NOT write a parallel helper.
- Use `&Server{router: gin.New(), store: mockStore, ...}; srv.setupRoutes() pattern for route-level wiring tests` in `internal/server/server_queue_test.go` (verify: `grep -n 'TestCancelOperationV2_NilRegistry' internal/server/server_queue_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. Create internal/server/wire_handlers_test.go (package server) with the standard file header (version 1.0.0, new guid, today's date).
2. Define a minimal fake implementing internal/aiscan.Store's 7 methods (CreateOperation, UpdateOperationStatus, UpdateOperationError, GetAllAuthors, GetAuthorByID, GetAllAuthorBookCounts, GetBooksByAuthorIDWithRoleCore) — a struct recording UpdateOperationStatus calls is enough since that is what CancelScan ultimately drives (internal/aiscan/pipeline.go:103).
3. In the test: `scanStore, err := database.NewAIScanStore(t.TempDir())`; create a scan via `scanStore.CreateScan(...)`; call `scanStore.UpdateScanOperationID(scan.ID, "op-under-test")`.
4. Construct `pm := aiscan.NewPipelineManager(scanStore, fakeStore, nil)` (parser can be nil if CancelScan does not dereference it — verify with `grep -n 'parser' internal/aiscan/pipeline.go` around CancelScan; if it does, pass a zero-value/no-op *ai.OpenAIParser).
5. Build `srv := &Server{router: gin.New(), store: mockStore, pipelineManager: pm, aiScanStore: scanStore, opRegistry: <a MockOperationsRegistry with no expectations, to prove the AI-scan branch is what handled the request>}`; call `srv.setupRoutes()`.
6. Issue `DELETE /api/v1/operations/v2/op-under-test` through `srv.router.ServeHTTP`.
7. Assert the response is 204, AND assert scanStore.GetScan(scan.ID) (or the fake mainStore's recorded UpdateOperationStatus call) shows the scan was actually acted on — the failure mode this test exists to catch is '204 returned but nothing was signalled'.
8. Bump internal/server/wire_handlers.go's version header only if you also add a defensive comment there pointing at the new test; otherwise leave it untouched since no production code changes.

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
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/... -run TestWireHandlers_CancelOperationV2ReachesAIScanPipeline` passes.
- [ ] Deleting `handlers.WithAIScanCancellation(v2Pipeline, v2ScanStore)` from wire_handlers.go:176 (as a manual sabotage check) makes the new test fail — confirming it actually guards the wiring line, not just the handler's internal logic (which internal/server/handlers/operations_v2_test.go already covers).
- [ ] Anti-over-suppression test: `N/A — this is a coverage-gap fix, not a filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
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

If the first acceptance check below already passes at HEAD (``go test ./internal/server/... -run TestWireHandlers_CancelOperationV2ReachesAIScanPipeline` passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The TODO item's stated root cause (concrete types blocking substitution) is already resolved; only reword this when folding into the assembled backlog so the next reader doesn't waste time re-narrowing interfaces that are already narrow.
