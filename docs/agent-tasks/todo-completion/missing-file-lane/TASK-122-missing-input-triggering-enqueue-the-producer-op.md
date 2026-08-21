<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-122-missing-input-triggering-enqueue-the-producer-op.md -->
<!-- version: 1.0.0 -->
<!-- guid: b67b42f4-b4bd-4107-bc6d-fcf3c7b1c00d -->
<!-- last-edited: 2026-08-21 -->

# TASK-122 — Missing-input triggering: enqueue the producer op when a waiting_deps requirement's input has never run (TODO.md L8890)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** modifies core operations-registry scheduling logic (shipped flag-OFF and dormant per the item's own note, with only one real consumer today) — wide blast radius across every op using ReqOpCompleted · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 8890 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**\"First Aid\" — one sequenced library validate + r" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-122-missing-input-triggering-enqueue-the-producer-op" -b agent/missing-file-lane-122-missing-input-triggering-enqueue-the-producer-op origin/main
cd "$REPO/.worktrees/missing-file-lane-122-missing-input-triggering-enqueue-the-producer-op"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

When a waiting_deps op's ReqOpCompleted requirement names a producer op type that has never run for the subject, ENQUEUE that producer op instead of leaving the dependent op parked forever — closing the gap the item's own text identifies: 'parking WAITS and never enqueues the producer.'

## Background (verify before editing)

- deps.go's evalOpCompleted (L196) and deps_scheduler.go's DepsScheduler already implement requirement evaluation and promotion-on-completion — real, shipped infrastructure, just missing this one behavior.
- This subsystem shipped flag-OFF and dormant (#1442) with dedup.check-book as its only consumer; its one review already caught three real bugs including a promote path that never dispatched — treat this area as fragile and test thoroughly.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'ReqOpCompleted\|waiting_deps' internal/operations/registry/deps.go internal/operations/registry/deps_scheduler.go | wc -l   # >10 hits across both files — ReqOpCompleted/waiting_deps evaluation and scheduling already exist
  grep -n 'PromoteToQueued' internal/operations/registry/deps_scheduler.go   # 4 hits (interface decl, comment, call site) in deps_scheduler.go — the scheduler has PromoteToQueued
  grep -n 'func.*Enqueue' internal/operations/registry/deps_scheduler.go   # 0 hits — no Enqueue-producer function exists in deps_scheduler.go today — the scheduler never enqueues a missing producer (no Enqueue-producer function in this file)
  ```

### Reuse — don't invent

- Use `DepsScheduler / ListWaitingDepsOps / PromoteToQueued` in `internal/operations/registry/deps_scheduler.go` (verify: `grep -n 'type DepsScheduler' internal/operations/registry/deps_scheduler.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read internal/operations/registry/deps.go and deps_scheduler.go in full before changing anything.
2. Add a new method (e.g. EnsureProducerEnqueued(subjectType, subjectID, opType string)) that, given a ReqOpCompleted requirement not yet satisfied, checks whether opType has ANY run (queued/running/completed) for the subject and, if not, enqueues it via the same path normal op submission uses.
3. Call this from wherever a new op transitions into waiting_deps, instead of only recording the wait.
4. Guard against enqueue storms: if the producer op is ALREADY queued/running for the subject, do not enqueue a second one.
5. Keep the existing promotion-on-completion behavior unchanged — this only adds the missing enqueue-the-producer step.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_122.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A producer op type that doesn't exist/isn't registered must fail loudly (log + leave the dependent parked with a clear reason), not silently enqueue nothing and pretend it tried.

## Tests

- internal/operations/registry/deps_scheduler_test.go: TestDepsScheduler_EnqueuesMissingProducer — an op parks in waiting_deps for a producer that has never run; assert the producer gets enqueued.
- TestDepsScheduler_DoesNotDoubleEnqueueAlreadyRunningProducer — anti-over-suppression: if the producer is already queued/running, a second waiting op for the same subject/producer must NOT enqueue a duplicate.

Anti-over-suppression test: `TestDepsScheduler_DoesNotDoubleEnqueueAlreadyRunningProducer` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/operations/registry/... -run DepsScheduler_Enqueues passes both cases.
- [ ] Anti-over-suppression test: `TestDepsScheduler_DoesNotDoubleEnqueueAlreadyRunningProducer` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_122.md`.

## Commit message

```
feat(missing-file-lane): Missing-input triggering: enqueue the producer op when a wai (TODO L8890)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/operations/registry/... -run DepsScheduler_Enqueues passes both cases.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is core operations-registry infrastructure, not First Aid-specific — First Aid (part 3) is its first real consumer, but review this change with the same care the item says its one prior review needed.
