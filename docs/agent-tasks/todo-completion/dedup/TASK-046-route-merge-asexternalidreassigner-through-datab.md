<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-046-route-merge-asexternalidreassigner-through-datab.md -->
<!-- version: 1.1.0 -->
<!-- guid: 02ac1b2b-45da-4c39-9d22-e41194328161 -->
<!-- last-edited: 2026-09-02 -->

# TASK-046 — Route merge.AsExternalIDReassigner through database.AsCapability instead of a bare assertion (TODO.md L4698)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — Held 2026-08-23 and has NOT landed: merge/service.go:34 is still a bare assertion, called at L372/L513; no AsCapability in the file. Precedent: PR #2696. Recommendation: keep - still held for owner review, not dropped; either unblock it or close it explicitly rather than leaving it queued.

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · dedup subagent · **Why:** One-line body swap copying an existing sibling helper's exact pattern. · **Depends on:** none · **Wave:** 4 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4698 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`internal/merge/service.go:34-42` — `AsExternalIDR" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-046-route-merge-asexternalidreassigner-through-datab" -b agent/dedup-046-route-merge-asexternalidreassigner-through-datab origin/main
cd "$REPO/.worktrees/dedup-046-route-merge-asexternalidreassigner-through-datab"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change AsExternalIDReassigner in internal/merge/service.go to resolve through database.AsCapability so a merge running against a decorator-wrapped store still finds the capability instead of silently skipping iTunes-PID/ASIN reassignment on merge.

## Background (verify before editing)

- internal/database/store_capability.go documents the exact failure mode: 'Embedding an interface promotes only THAT interface's method set... a plain s.(SyncIdentityStore) returns nil once the wrap is installed — with no compile error.'
- Two prod jobs were already silently degraded this way for weeks per that file's comment; the fix pattern (AsCapability walking StoreUnwrapper) is established and reused at internal/plugins/acoustid/reset_all.go:223-228 (resolveFingerprintResetter) and wire_abs_routes.go's resolveWarmupWaiter.
- Today merge.NewService is always constructed with the bare store (internal/server/wire_handlers.go:387,655 pass s.storeForWiring(), internal/server/duplicates_ops.go:169 and handlers/diagnostics.go:526 pass a bare `store`), so this is latent, not a live incident — but the item is correct that any future call site that hands merge.NewService a wrapped store degrades silently.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func AsExternalIDReassigner" -A 8 internal/merge/service.go   # 1 hit ~L34, body is `s.(ExternalIDReassigner)` — AsExternalIDReassigner uses a bare type assertion
  grep -n "AsExternalIDReassigner(ms.db)" internal/merge/service.go   # 2 hits ~L236,377 — called on ms.db at two sites
  grep -n "func AsCapability" internal/database/store_capability.go   # 1 hit ~L86 — database.AsCapability exists and is the documented fix for exactly this shape
  grep -n "func resolveFingerprintResetter" -A 6 internal/plugins/acoustid/reset_all.go   # 1 hit ~L223, body calls database.AsCapability[fingerprintResetter](s) — the identical pattern is already fixed once, as a copyable reference
  grep -n "db  *Store" internal/merge/service.go   # 1 hit ~L51 `db Store` inside `type Service struct` — merge.Service.db is an interface (any-compatible), not a concrete type
  ```

### Reuse — don't invent

- Use `database.AsCapability[T]` in `internal/database/store_capability.go` (verify: `grep -n "func AsCapability" internal/database/store_capability.go`) — do NOT write a parallel helper.

## Step-by-step

1. Open internal/merge/service.go.
2. Replace the body of `func AsExternalIDReassigner(s any) ExternalIDReassigner` (currently `if s == nil { return nil }; if eid, ok := s.(ExternalIDReassigner); ok { return eid }; return nil`) with: `if s == nil { return nil }; c, _ := database.AsCapability[ExternalIDReassigner](s); return c`.
3. Confirm `github.com/falkcorp/audiobook-organizer/internal/database` is already imported (it is, for the `database` package alias used elsewhere in the file).
4. Bump the file's version header and last-edited date.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_046.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- s implements ExternalIDReassigner directly (no wrapper): AsCapability's outermost-check-first order returns it on the first iteration, same as today.
- s is a decorator that does NOT implement StoreUnwrapper: AsCapability returns (zero, false) immediately, same nil result as today's failed bare assertion — no behavior change for opaque decorators.

## Tests

- internal/merge/service_test.go: add TestAsExternalIDReassigner_ResolvesThroughDecorator — build a minimal fake decorator implementing database.StoreUnwrapper that wraps a fake store implementing ExternalIDReassigner, call AsExternalIDReassigner(decorator), assert it returns non-nil and its ReassignExternalIDs works.
- Keep/adapt the existing direct-store case: AsExternalIDReassigner(fakeStoreImplementingIt) still returns non-nil (anti-regression for the non-decorated path).
- AsExternalIDReassigner(nil) still returns nil (existing nil-guard, must survive the rewrite).

Anti-over-suppression test: `TestAsExternalIDReassigner_ResolvesThroughDecorator (must go red if AsExternalIDReassigner is reverted to a bare assertion)` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/merge/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./internal/merge/... exits 0.
- [ ] go test ./internal/merge/... -run TestAsExternalIDReassigner exits 0.
- [ ] grep -n "database.AsCapability\[ExternalIDReassigner\]" internal/merge/service.go returns 1 hit.
- [ ] Anti-over-suppression test: `TestAsExternalIDReassigner_ResolvesThroughDecorator (must go red if AsExternalIDReassigner is reverted to a bare assertion)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/merge/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_046.md`.

## Commit message

```
refactor(dedup): Route merge.AsExternalIDReassigner through database.AsCapabi (TODO L4698)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: a silent skip here means merge.MergeBooks completes 'successfully' while dropping the loser book's iTunes PID/ASIN reassignment — a prod-data-path defect class, even though today's wiring is unaffected. Correction to the TODO item's own citation: internal/plugins/acoustid/reset_all.go:69 (cited by the item as a live 'same shape' instance) is NOT a live bug — it is a comment describing the ALREADY-FIXED resolveFingerprintResetter helper (the fast path at reset_all.go:69-78 calls resolveFingerprintResetter, not a bare assertion). Only lsh_backfill.go:86 (this todo_line, part 2) is a live instance of the same shape.
