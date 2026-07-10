<!-- file: docs/agent-tasks/bug-techdebt/TASK-03-sdkguard-break-deps.md -->
<!-- version: 1.0.0 -->
<!-- guid: 260c0e89-cc5a-43a2-ae06-590d25304e97 -->
<!-- last-edited: 2026-07-10 -->

# TASK-03 — Break the two sdkguard dependency violations (SDKGUARD-VIOLATION, #1795) [⚠ review-critical]

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · cross-package refactor subagent · **Why:** touches the SDK backplane and the op-ID log-correlation chain — a silently-dropped wiring line breaks SLOG correlation without failing any test unless the new test is written correctly · **Depends on:** none

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY per item (worktree/PR/CI) EXCEPT REPO-SIZE-1 (#1650) which is STOP-FOR-HUMAN: a git-history rewrite is destructive and invalidates every clone/worktree — produce the migration plan (BFG/filter-repo vs LFS options, coordination checklist, backup strategy) as a TASK brief whose ONLY deliverable is the plan document, then STOP.
**Gate scope note:** the REPO-SIZE-1 exception above is the initiative-wide gate quoted verbatim; it does NOT apply to this task. TASK-03 is not REPO-SIZE-1 — execute this brief fully autonomously (worktree/PR/CI) with no stop-for-human step.
**File-ownership:** `internal/database/embedding_store.go` is INIT-2-owned for STRUCTURAL edits (its candidate-index task). Your change there is a 3-reference import/type swap only — check `gh pr list --search "embedding_store"` first; if an INIT-2 PR is open on that file, rebase on it after it merges rather than racing it. COORDINATOR HARD GATE: that grep is a point-in-time check and can miss an INIT-2 PR that opens mid-flight (the initiatives may run under different coordinators/sessions) — when this task runs under a coordinator, the coordinator must confirm no INIT-2 wave is ACTIVE (session/state-level check, per the plan's cross-initiative section) before dispatching you; solo executors keep the grep as the best available check. All other files are exclusively yours within INIT-9.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/bug-techdebt-sdkguard-break-deps" -b agent/bug-techdebt-sdkguard-break-deps origin/main
cd "$REPO/.worktrees/bug-techdebt-sdkguard-break-deps"
git rebase origin/main
```

## Goal

Make `make sdkguard` pass by breaking BOTH forbidden dependency chains — WITHOUT
adding `internal/logger` or `internal/dedup/unified` to the `allowedInternals` list in
`tools/cmd/sdkguard/main.go` (do not touch that file at all). Locked design (spec
Decisions 1-2): (chain 1) dependency inversion — a `SetRunContextDecorator` setter on
`Registry`, wired to `logger.WithOperation` in `internal/server/registry_wire.go`;
(chain 2) move `UnifiedDedupScore`/`Signal`/`SignalKind` down to a new
`internal/models/dedup_score.go` (models is already allowlisted) with type aliases
left behind in `internal/dedup/unified/score.go`.

## Background (verify before editing)

- `make sdkguard` FAILS on main today listing exactly two packages:
  `internal/logger` and `internal/dedup/unified` (verified 2026-07-10, HEAD fce58498).
- Chain 1: `pkg/plugin/sdk → internal/operations/registry → internal/logger`. The
  registry's `r.logger` field is `*slog.Logger` (NOT internal/logger); the ONLY
  `internal/logger` use in the whole registry package is the single call
  `runCtx = logger.WithOperation(runCtx, qr.opID)` in `worker.go`.
- Chain 2: `pkg/plugin/sdk → internal/database → internal/dedup/unified`. The ONLY
  `unified.` references in `internal/database` are the `*unified.UnifiedDedupScore`
  type in `embedding_store.go` (two struct fields + one method parameter).
- `internal/dedup/unified/score.go` imports ONLY `"time"` — the type move drags no
  further dependencies into `internal/models`.
- Constants of an aliased type stay valid: `SignalKind` constants (`SigExactFile`,
  etc.) can REMAIN declared in `unified` after `type SignalKind = models.SignalKind`.
- The registry already has setter precedents to mirror: `SetDepsScheduler`,
  `SetDepBookStore` (registry.go), and `SetActivityRecorder` (called from
  `registry_wire.go` right where you will add your wiring line).
- The prod registry is constructed INSIDE the registry package (a serviceregistry
  builder in `internal/operations/registry/register.go` returns a `RegistryWrapper`),
  so the wiring must happen post-construction in `internal/server/registry_wire.go` —
  the registry package itself must never import `internal/logger`.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'internal/logger' internal/operations/registry/*.go
  # Expected: exactly 1 hit — worker.go ~:18 (the import)
  grep -n 'logger.WithOperation' internal/operations/registry/worker.go
  # Expected: exactly 1 hit ~:157
  grep -n 'unified\.' internal/database/embedding_store.go
  # Expected: 3 hits ~:120, ~:161, ~:845 (ScoreBreakdown fields + UpdateCandidateScore param)
  grep -n 'type UnifiedDedupScore struct\|type Signal struct\|type SignalKind string' internal/dedup/unified/score.go
  # Expected: 3 hits (~:96, ~:70, ~:18) — the three types to move
  grep -n 'SetActivityRecorder\|wrapper.Registry' internal/server/registry_wire.go
  # Expected: hits ~:334-:338 — your wiring line goes in this block
  grep -n 'SetDepBookStore' internal/operations/registry/registry.go
  # Expected: 1 hit ~:175 — the setter shape to mirror
  grep -n 'func WithOperation' internal/logger/*.go
  # Expected: 1 hit — confirm the signature is func(ctx context.Context, opID string) context.Context
  ```

## Step-by-step

1. **Chain 2 first (mechanical).** Create `internal/models/dedup_score.go` with the
   repo-standard 4-line Go header. MOVE (cut-paste verbatim, comments included) from
   `internal/dedup/unified/score.go`: `SignalKind` (type only — NOT its constants),
   `Signal`, and `UnifiedDedupScore`. If `Signal` or `UnifiedDedupScore` reference any
   other unified-local type (check their field types line by line), move that type too
   and alias it identically.
2. In `internal/dedup/unified/score.go`, replace the moved declarations with aliases:
   `type SignalKind = models.SignalKind`, `type Signal = models.Signal`,
   `type UnifiedDedupScore = models.UnifiedDedupScore` (import
   `github.com/falkcorp/audiobook-organizer/internal/models`). The `SignalKind`
   constants stay where they are, unmodified. Nothing else in the package changes.
3. In `internal/database/embedding_store.go`, swap the 3 `unified.UnifiedDedupScore`
   references to `models.UnifiedDedupScore`; replace the `internal/dedup/unified`
   import with `internal/models`. Do NOT change json tags, method names, or logic —
   the persisted candidate encoding must stay byte-identical (the moved type's json
   tags are verbatim).
4. **Chain 1.** In `internal/operations/registry/registry.go`: add a private field
   `runContextDecorator func(ctx context.Context, opID string) context.Context` to
   `Registry` and a setter mirroring `SetDepBookStore`'s shape (mutex discipline
   included if the siblings use one):
   `func (r *Registry) SetRunContextDecorator(fn func(ctx context.Context, opID string) context.Context)`.
5. In `internal/operations/registry/worker.go`: replace
   `runCtx = logger.WithOperation(runCtx, qr.opID)` with a nil-guarded call to the
   decorator (`if r.runContextDecorator != nil { runCtx = r.runContextDecorator(runCtx, qr.opID) }`
   — or a tiny helper method); DELETE the `internal/logger` import.
6. In `internal/server/registry_wire.go`, inside the block that assigns
   `s.opRegistry = wrapper.Registry` (next to the existing `SetActivityRecorder`
   call — grep above), add:
   `s.opRegistry.SetRunContextDecorator(logger.WithOperation)` (that file may need the
   `internal/logger` import added — internal/server may import it freely).
7. Add `internal/operations/registry/context_decorator_test.go`: (a) decorator set →
   a dispatched run's ctx carries the decoration (assert via a decorator that stamps a
   sentinel value read back inside a fake op); (b) decorator nil → run executes
   normally, ctx undecorated, no panic. Mirror the existing worker/registry test
   fixtures in the package (find them: `grep -rn 'NewWithOptions' internal/operations/registry/*_test.go` —
   expected ≥1 hit to copy the construction shape from). Use a task-unique helper name
   (parallel-test-helper-collision rule).
8. Bump file headers (version + last-edited) on every touched file; keep guids.
9. Run the gate (below). `make sdkguard` MUST pass now.

Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added; the
nil-guard's covered-by-test "decorator nil" case is the no-op default, not a filter).

## How to test

```bash
make sdkguard
# Expected: "✅ SDK guard passed" (exit 0) — the whole point of this task
go build ./... && go test ./internal/operations/registry/ ./internal/database/ ./internal/dedup/... ./internal/models/ -race
# Expected: green, including the two new decorator tests
go test ./... -short
# Expected: green (type-alias moves ripple; full-suite rule applies)
make ci
# Expected: the sdkguard step PASSES (that is this task's fix — its failure before your change was the bug itself). The staticcheck step may still FAIL on pre-existing backlog files (#1796) — that failure is expected-and-ignorable IF AND ONLY IF the flagged files are not ones you touched. Pass condition for this line: sdkguard green + scoped staticcheck clean on every file listed in Acceptance criteria; a staticcheck failure confined to files you did not touch is NOT a regression and does not block the PR (merge gate is Minimal CI green, see below).
```

staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
you changed; the merge gate is Minimal CI green. After THIS task merges, the sdkguard
step of `make ci` is green again; until then its failure is the bug you are fixing.

## Acceptance criteria

- [ ] `make sdkguard` exits 0
- [ ] `grep -rn 'internal/logger' internal/operations/registry/ --include='*.go'` returns 0 hits
- [ ] `grep -n 'internal/dedup/unified' internal/database/embedding_store.go` returns 0 hits
- [ ] `grep -n 'SetRunContextDecorator(logger.WithOperation)' internal/server/registry_wire.go` returns 1 hit (op-ID correlation preserved in prod wiring)
- [ ] `grep -n 'type UnifiedDedupScore = models.UnifiedDedupScore' internal/dedup/unified/score.go` returns 1 hit (aliases in place)
- [ ] `tools/cmd/sdkguard/main.go` untouched (`git diff --stat origin/main -- tools/cmd/sdkguard/` empty)
- [ ] Anti-over-suppression: N/A
- [ ] `go test ./... -short` green; vet clean; scoped staticcheck on changed files clean
- [ ] File headers bumped on every changed file

## Commit message

```
refactor(sdk): break sdkguard violations via decorator inversion + type move (#1795)

pkg/plugin/sdk's dep tree pulled internal/logger (registry worker's single
logger.WithOperation call) and internal/dedup/unified (UnifiedDedupScore in
embedding_store). Inverts the first behind Registry.SetRunContextDecorator
(wired to logger.WithOperation in registry_wire.go, preserving SLOG op-ID
correlation) and moves UnifiedDedupScore/Signal/SignalKind to internal/models
with aliases left in unified. make sdkguard green; allowlist untouched.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/bug-techdebt-sdkguard-break-deps
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "SetRunContextDecorator" internal/operations/registry/registry.go` hits
AND `grep -rn "internal/logger" internal/operations/registry/ --include='*.go'`
returns 0 hits AND `grep -n "type UnifiedDedupScore struct" internal/models/dedup_score.go`
hits while `grep -n "type UnifiedDedupScore struct" internal/dedup/unified/score.go`
returns 0 hits, the transform is already done — run the acceptance checks instead of
re-applying. Rollback = revert the single commit; the direct import and original type
locations are restored, persisted candidate rows are unaffected either way (json tags
identical), and op-ID correlation returns to the hardcoded call.
