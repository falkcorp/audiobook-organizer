<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-096-require-every-mutating-operation-to-declare-and-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 484b1922-1f4f-4dd6-97e9-aa7e5b1448ce -->
<!-- last-edited: 2026-08-21 -->

# TASK-096 — Require every mutating operation to declare and enforce dry_run support at the registry (TODO.md L7435)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** Cross-cutting registry contract change touching every mutating OperationDef; needs careful design of the shared param-embedding mechanism and a migration plan for existing ops that lack it. · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 7435 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Require every operation to support `dry_run`, an" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-11.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-096-require-every-mutating-operation-to-declare-and-" -b agent/missing-file-lane-096-require-every-mutating-operation-to-declare-and- origin/main
cd "$REPO/.worktrees/missing-file-lane-096-require-every-mutating-operation-to-declare-and-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a `SupportsDryRun bool` (or equivalent shared `DryRunParams` embed convention) to OperationDef in internal/operations/registry/types.go; extend ValidateOpDef (internal/operations/registry/registry.go) to reject registration of any op declaring CapLibraryWrite without SupportsDryRun=true, so the gap is caught at server boot rather than discovered ad hoc while deciding whether to hit apply. Default TRUE for destructive ops per the item's stated preference, matching the two existing correctly-built examples (missing-file-repoint, intro_transcribe reparse path).

## Background (verify before editing)

- Related existing machinery already following the 'declare what you do' philosophy the item cites: OperationDef.Writes []Resource (internal/operations/registry/types.go, 2026-08-07 owner design).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -c 'DryRun' internal/operations/registry/types.go   # 0 hits — OperationDef has no DryRun field today (grep -c prints "0" and exits 1 on no match) — OperationDef has no dry-run declaration field today
  grep -n 'func ValidateOpDef' internal/operations/registry/*.go   # 1 hit — ValidateOpDef exists as the place to add a registration-time check (extracted 2026-08-20 per commit 4e19d467)
  grep -n 'DryRun \*bool' internal/plugins/maintenance/intro_transcribe.go   # 1 hit ~L90 — the item's specific motivating example (transcribe-book-intros) already has dry_run, defaulting true
  ```

### Reuse — don't invent

- Use `ValidateOpDef (stateless registration-time checks, extracted 2026-08-20)` in `internal/operations/registry/registry.go` (verify: `grep -n 'func ValidateOpDef' internal/operations/registry/registry.go`) — do NOT write a parallel helper.
- Use `Writes []Resource (existing declare-what-you-do pattern to model this after)` in `internal/operations/registry/types.go` (verify: `grep -n 'Writes \[\]Resource' internal/operations/registry/types.go`) — do NOT write a parallel helper.
- Use `existing dry_run classify-then-branch pattern (missing_file_repoint.go, intro_transcribe.go)` in `internal/plugins/maintenance/missing_file_repoint.go` (verify: `grep -n 'if !params.Apply' internal/plugins/maintenance/missing_file_repoint.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/operations/registry/types.go, add a field to OperationDef, e.g. `SupportsDryRun bool // required true when Capabilities includes CapLibraryWrite; see ValidateOpDef`.
2. In internal/operations/registry/registry.go's ValidateOpDef (the function extracted 2026-08-20, `grep -n 'func ValidateOpDef'`), add a check: if the def's Capabilities include CapLibraryWrite (or similar write capability) and SupportsDryRun is false, return a registration error.
3. Audit every existing OperationDef with a write capability (`grep -rln 'CapLibraryWrite' internal/plugins/`) and set SupportsDryRun: true on those that already implement a dry_run/apply param (most maintenance ops per the existing classify-then-branch pattern), flagging any found WITHOUT dry_run support as a follow-up per-op task rather than silently forcing them.
4. Optionally introduce a shared `DryRunParams` struct (e.g. `type DryRunParams struct { DryRun *bool \`json:"dry_run,omitempty"\` }`) that op param structs can embed, to standardize the field name/semantics (some ops use `apply` inverted, some use `dry_run` — pick one canonical name and document the other as deprecated per-op).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_096.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- An op that mutates but is inherently idempotent/non-destructive (e.g., a cache warm) may reasonably not need dry_run — the check should key off CapLibraryWrite specifically, not 'any write', matching the item's own framing ('any op that mutates state').

## Tests

- internal/operations/registry/registry_test.go: TestValidateOpDef_RejectsWriteCapabilityWithoutDryRun — registering an OperationDef with CapLibraryWrite and SupportsDryRun:false must fail; the same def with SupportsDryRun:true must pass.
- A boot-time guard test (like the existing maintenance guard test mentioned in commit 4e19d467) asserting every currently-registered maintenance op with CapLibraryWrite has SupportsDryRun:true, so a future op can't silently regress.

Anti-over-suppression test: `TestValidateOpDef_RejectsWriteCapabilityWithoutDryRun must include a positive case (SupportsDryRun:true, CapLibraryWrite present) that passes registration, so the guard isn't accidentally rejecting everything.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/operations/registry/... ./internal/scheduler/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/operations/registry/... passes
- [ ] go build ./... succeeds after every existing write-capable OperationDef sets SupportsDryRun appropriately
- [ ] Anti-over-suppression test: `TestValidateOpDef_RejectsWriteCapabilityWithoutDryRun must include a positive case (SupportsDryRun:true, CapLibraryWrite present) that passes registration, so the guard isn't accidentally rejecting everything.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/operations/registry/... ./internal/scheduler/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_096.md`.

## Commit message

```
feat(missing-file-lane): Require every mutating operation to declare and enforce dry_ (TODO L7435)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`go test ./internal/operations/registry/... passes`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is a repo-wide contract change (large blast radius across every maintenance op) — per CLAUDE.md's Fix It Right rule, do the real registry-level enforcement rather than a narrower per-op-only fix, but expect this to be its own multi-PR effort given effort L / tier opus.
