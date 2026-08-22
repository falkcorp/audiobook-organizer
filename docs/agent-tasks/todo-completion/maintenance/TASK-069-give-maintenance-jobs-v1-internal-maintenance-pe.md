<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-069-give-maintenance-jobs-v1-internal-maintenance-pe.md -->
<!-- version: 1.0.0 -->
<!-- guid: 29370acb-3a33-4eb4-abe6-25e933f7ccfd -->
<!-- last-edited: 2026-08-21 -->

# TASK-069 — Give maintenance jobs (v1, internal/maintenance) per-job store interfaces instead of the shared JobStore (TODO.md L1009)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** mechanical per-job (37 jobs) but touches every job's Run signature plus both registry call sites -- large diff, well-suited to parallel-sweep, but not small · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 1009 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Decide whether maintenance jobs should take per-jo" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-02.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-069-give-maintenance-jobs-v1-internal-maintenance-pe" -b agent/maintenance-069-give-maintenance-jobs-v1-internal-maintenance-pe origin/main
cd "$REPO/.worktrees/maintenance-069-give-maintenance-jobs-v1-internal-maintenance-pe"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace the package-level All()/Register() registry and the shared 12-group JobStore with per-job store interfaces: each of the 37 jobs in internal/maintenance/jobs/ declares its own 2-5 method interface for exactly what its Run body calls (the same estimate-then-typecheck-proves-it move #2534/#2536 already used), and All(store) is constructed with the store in scope rather than at package-level init().

## Background (verify before editing)

- Plan doc Phase 2 item 1 (docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md): 'Each Run body is unchanged and already compiles against a bounded set, so each job can declare its own 2-5 method interface and the type checker verifies it... 37 jobs, mechanical, parallelisable.' The plan's 2026-08-18 update explicitly reaffirms this item is unaffected by the width-sweep correction and 'remains the next real step.'
- TODO.md's own reasoning for sequencing: maintenance_dispatcher.go is one of the two call sites requiring an All()->All(store) signature change, and Phase 1 step 3 (delete maintenance_dispatcher.go) removes that call site entirely -- doing this work before Phase 1 lands means touching a file that will shortly be deleted anyway.
- measured 2026-08-18 (per the TODO and the audit doc row 229): 23 of the 37 directly-called methods are used by exactly one job; only 5 are used by more than 4 jobs (GetAllBooksCore 18, GetBookByID 12, UpdateBook 10, GetAllBookFilesCore 10, GetBookFiles 8) -- so most of the shared JobStore contract is not actually shared.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'type JobStore interface' internal/maintenance/job.go   # 1 hit L293 — JobStore is still a single shared interface, 7 embedded groups
  grep -n 'func All(' internal/maintenance/registry.go   # 1 hit L51, signature `func All() []MaintenanceJob` — All() takes no store parameter, confirming jobs register with no store in scope
  find . -name 'maintenance_dispatcher.go' -not -path './.worktrees/*'   # 1 hit: internal/server/maintenance_dispatcher.go — maintenance_dispatcher.go (the call site Phase 1 would delete) still exists
  find . -name 'maintenance_job_op.go' -not -path './.worktrees/*'   # 1 hit: internal/server/maintenance_job_op.go — the second named call site also still exists
  ```

### Reuse — don't invent

- Use `internal/plugins/maintenance/deps.go's already-completed per-concern interface segregation (StoreProvider, OpsStore, etc.) as the pattern for per-job interfaces in the v1 package` in `internal/plugins/maintenance/deps.go` (verify: `grep -n 'type OpsStore interface' internal/plugins/maintenance/deps.go`) — do NOT write a parallel helper.
- Use `docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md Phase 2 item 1 -- the design already written for exactly this` in `docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md` (verify: `grep -n 'maintenance.JobStore.*187 methods' docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md`) — do NOT write a parallel helper.

## Step-by-step

1. Confirm whether Phase 1 (kill-v1) has progressed further since this scan (re-run `find . -name maintenance_dispatcher.go`); if it has been deleted, this item's ordering rationale is moot and the work can proceed against `internal/server/maintenance_job_op.go` alone.
2. If maintenance_dispatcher.go still exists, recommend to the coordinator that Phase 1 (a separate, smaller, already-planned effort) lands FIRST, since this item's own analysis says so -- flag as a sequencing dependency rather than building blind.
3. Once unblocked: for each of the 37 jobs in internal/maintenance/jobs/, use the same empty-interface compiler-probe methodology as internal/audiobooks/service.go's audiobookStore comment (-gcflags=-e) to enumerate exactly which JobStore methods that job's Run body calls.
4. Declare a per-job interface (or a small set of grouped narrow interfaces for jobs sharing real overlap, e.g. the 5 methods used by >4 jobs) in each job's own file.
5. Change MaintenanceJob's Run signature (or the registry's construction) so All(store) is called with a store in scope, updating both surviving call sites (maintenance_job_op.go, and maintenance_dispatcher.go if Phase 1 has not yet deleted it) and the package-level Register()/All() functions in internal/maintenance/registry.go.
6. This is a strong /parallel-sweep candidate given 37 mechanically-similar per-job tasks -- flag to the coordinator rather than doing all 37 serially.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_069.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A job using one of the 5 widely-shared methods (GetAllBooksCore, GetBookByID, UpdateBook, GetAllBookFilesCore, GetBookFiles) should embed a small shared interface for those rather than each independently re-declaring the same 1-5 methods 10+ times -- balance DRY against the per-job-narrow goal, following the same judgment internal/audiobooks/service.go's bookReader/bookWriter grouping already models.
- make ci currently cannot pass on main per the plan doc's own test-strategy note (10 pre-existing staticcheck findings, 0 introduced by this work) -- use go build + targeted go test as the real gate, not a red make ci, matching the plan's explicit guidance.

## Tests

- Each job's existing unit tests must stay green with the narrower interface -- the type checker is the primary proof (per the plan's stated test strategy: 'Type checker as the test wherever a signature changes').
- internal/maintenance/job_test.go and registry_test.go need updating for the new All(store) signature.

Anti-over-suppression test: `N/A -- this is an interface-narrowing refactor, not a filter/guard.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/maintenance/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./... succeeds with every job declaring its own narrow interface.
- [ ] go vet ./internal/maintenance/... passes.
- [ ] grep -n 'func All(' internal/maintenance/registry.go shows the new store-taking signature.
- [ ] Anti-over-suppression test: `N/A -- this is an interface-narrowing refactor, not a filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/maintenance/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_069.md`.

## Commit message

```
refactor(maintenance): Give maintenance jobs (v1, internal/maintenance) per-job sto (TODO L1009)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

L973 in this same scope is a decision-record pointer to this exact item -- close L973 as not_a_task and treat this line as the real, actionable deliverable. Strongly recommend /parallel-sweep given the 37-job mechanical shape, once the Phase-1 sequencing question in step 1-2 is resolved.
