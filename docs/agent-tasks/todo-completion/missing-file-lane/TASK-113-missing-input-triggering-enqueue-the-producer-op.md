<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-113-missing-input-triggering-enqueue-the-producer-op.md -->
<!-- version: 1.0.0 -->
<!-- guid: 26e7c2ea-914b-4c9d-9d5e-2616741bf9ba -->
<!-- last-edited: 2026-08-21 -->

# TASK-113 — Missing-input triggering: enqueue the producer op when a waiting_deps requirement's input has never run (TODO.md L8890)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** modifies core operations-registry scheduling logic (shipped flag-OFF and dormant per the item's own note, with only one real consumer today) — wide blast radius across every op using ReqOpCompleted · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 8890 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**\"First Aid\" — one sequenced library validate + r" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-113-missing-input-triggering-enqueue-the-producer-op" -b agent/missing-file-lane-113-missing-input-triggering-enqueue-the-producer-op origin/main
cd "$REPO/.worktrees/missing-file-lane-113-missing-input-triggering-enqueue-the-producer-op"
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
  grep -n 'func (r \*Registry) EnqueueOp' internal/operations/registry/registry.go   # 1 hit at L573 — the real op-submission path the producer must reuse is Registry.EnqueueOp
  grep -n 'reg    \*Registry' internal/operations/registry/deps_scheduler.go   # 1 hit at L55 — DepsScheduler already holds a *Registry, so it can call EnqueueOp without a new interface
  grep -n 'waiting_deps' internal/operations/registry/registry.go   # hits at L701 (status assignment) and L754-755 (parked log) — the park-into-waiting_deps site that must call the new method lives in registry.go, not deps*.go
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
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_113.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A
-  
- p
- r
- o
- d
- u
- c
- e
- r
-  
- o
- p
-  
- t
- h
- a
- t
-  
- I
- T
- S
- E
- L
- F
-  
- p
- a
- r
- k
- s
-  
- i
- n
-  
- w
- a
- i
- t
- i
- n
- g
- _
- d
- e
- p
- s
-  
- m
- u
- s
- t
-  
- n
- o
- t
-  
- r
- e
- -
- t
- r
- i
- g
- g
- e
- r
-  
- e
- n
- q
- u
- e
- u
- e
- -
- t
- h
- e
- -
- p
- r
- o
- d
- u
- c
- e
- r
-  
- r
- e
- c
- u
- r
- s
- i
- v
- e
- l
- y
-  
- -
-  
- b
- o
- u
- n
- d
-  
- t
- h
- e
-  
- c
- h
- a
- i
- n
-  
- (
- d
- o
-  
- n
- o
- t
-  
- e
- n
- q
- u
- e
- u
- e
-  
- a
-  
- p
- r
- o
- d
- u
- c
- e
- r
-  
- f
- r
- o
- m
-  
- w
- i
- t
- h
- i
- n
-  
- a
-  
- p
- r
- o
- d
- u
- c
- e
- r
- -
- t
- r
- i
- g
- g
- e
- r
- e
- d
-  
- p
- a
- r
- k
- )
-  
- a
- n
- d
-  
- a
- s
- s
- e
- r
- t
-  
- i
- t
-  
- w
- i
- t
- h
-  
- a
-  
- t
- e
- s
- t
- .
-  
- A
-  
- p
- r
- o
- d
- u
- c
- e
- r
-  
- o
- p
-  
- t
- y
- p
- e
-  
- t
- h
- a
- t
-  
- i
- s
-  
- n
- o
- t
-  
- r
- e
- g
- i
- s
- t
- e
- r
- e
- d
-  
- m
- u
- s
- t
-  
- f
- a
- i
- l
-  
- l
- o
- u
- d
- l
- y
-  
- (
- l
- o
- g
-  
- +
-  
- l
- e
- a
- v
- e
-  
- t
- h
- e
-  
- d
- e
- p
- e
- n
- d
- e
- n
- t
-  
- p
- a
- r
- k
- e
- d
-  
- w
- i
- t
- h
-  
- a
-  
- c
- l
- e
- a
- r
-  
- r
- e
- a
- s
- o
- n
- )
- ,
-  
- n
- o
- t
-  
- s
- i
- l
- e
- n
- t
- l
- y
-  
- e
- n
- q
- u
- e
- u
- e
-  
- n
- o
- t
- h
- i
- n
- g
- .
-  
- T
- h
- e
-  
- '
- h
- a
- s
-  
- t
- h
- i
- s
-  
- o
- p
- T
- y
- p
- e
-  
- A
- N
- Y
-  
- r
- u
- n
-  
- f
- o
- r
-  
- t
- h
- e
-  
- s
- u
- b
- j
- e
- c
- t
- '
-  
- l
- o
- o
- k
- u
- p
-  
- n
- e
- e
- d
- s
-  
- a
-  
- n
- a
- m
- e
- d
-  
- s
- t
- o
- r
- e
-  
- m
- e
- t
- h
- o
- d
-  
- -
-  
- a
- d
- d
-  
- i
- t
-  
- t
- o
-  
- S
- c
- h
- e
- d
- u
- l
- e
- r
- S
- t
- o
- r
- e
-  
- e
- x
- p
- l
- i
- c
- i
- t
- l
- y
-  
- r
- a
- t
- h
- e
- r
-  
- t
- h
- a
- n
-  
- r
- e
- a
- c
- h
- i
- n
- g
-  
- f
- o
- r
-  
- d
- a
- t
- a
- b
- a
- s
- e
- .
- S
- t
- o
- r
- e
- .

## Tests

- internal/operations/registry/deps_scheduler_test.go: TestDepsScheduler_EnqueuesMissingProducer — an op parks in waiting_deps for a producer that has never run; assert the producer gets enqueued.
- TestDepsScheduler_DoesNotDoubleEnqueueAlreadyRunningProducer — anti-over-suppression: if the producer is already queued/running, a second waiting op for the same subject/producer must NOT enqueue a duplicate.

Anti-over-suppression test: `TestDepsScheduler_DoesNotDoubleEnqueueAlreadyRunningProducer` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/operations/registry/... -run DepsScheduler_Enqueues passes both cases.
- [ ] Anti-over-suppression test: `TestDepsScheduler_DoesNotDoubleEnqueueAlreadyRunningProducer` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_113.md`.

## Commit message

```
feat(missing-file-lane): Missing-input triggering: enqueue the producer op when a wai (TODO L8890)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'ReqOpCompleted\|waiting_deps' internal/operations/registry/deps.go internal/operations/registry/deps_scheduler.go | wc -l` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is core operations-registry infrastructure, not First Aid-specific — First Aid (part 3) is its first real consumer, but review this change with the same care the item says its one prior review needed.
