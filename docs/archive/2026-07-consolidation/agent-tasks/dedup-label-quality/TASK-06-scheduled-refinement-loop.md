<!-- file: docs/agent-tasks/dedup-label-quality/TASK-06-scheduled-refinement-loop.md -->
<!-- version: 1.0.0 -->
<!-- guid: fd327f39-dc24-4fda-8dbc-e4f398d1c7fe -->
<!-- last-edited: 2026-07-10 -->

# TASK-06 — Scheduled label-refinement loop, built-in-DISABLED (INIT-1 T6)

**Gate:** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval. For THIS task that means: the scheduled chain runs DRY-RUN/report steps ONLY — there is no apply parameter on the scheduled path at all; enabling the schedule itself is an owner config decision.
**File-ownership:** none owned by another initiative. Do NOT touch `internal/dedup/engine.go` or `internal/database/embedding_store.go` (INIT-2-owned). NOTE: this task deliberately stays minimal because it aligns with — and awaits — INIT-6 WF-3 (persisted Workflow objects), which is expected to subsume these `scheduled.*` keys; do not build workflow abstractions here.

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · config + scheduler wiring subagent · **Why:** small but touches global config and the scheduler registry; must be provably inert by default · **Depends on:** TASK-05 (`dedup.calibrate-composite` op ID must exist) — ⛔ **DISPATCH HOLD:** as of 2026-07-10 TASK-05 is NOT merged, so this brief is not yet executable. Before dispatching, the coordinator must confirm `grep -rn '"dedup.calibrate-composite"' internal/plugins/dedup/` hits against origin/main; 0 hits = hold this brief, do not dispatch.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-label-quality-scheduled-refinement-loop" -b agent/dedup-label-quality-scheduled-refinement-loop origin/main
cd "$REPO/.worktrees/dedup-label-quality-scheduled-refinement-loop"
git rebase origin/main
# ⛔ DEPENDENCY GATE — this grep targets the op ADDED BY TASK-05 (file
# internal/plugins/dedup/calibrate_composite.go does not exist until TASK-05
# merges). 0 hits means TASK-05 is unmerged: STOP IMMEDIATELY and report
# exactly "BLOCKED: TASK-05 (dedup.calibrate-composite) not merged". This is a
# sequencing stop, NOT anchor drift — do not guess, do not stub the op ID.
grep -rn '"dedup.calibrate-composite"' internal/plugins/dedup/
```

## Goal

Add a scheduled task `label_refinement` that, when (and only when) an owner enables it, periodically runs the refinement chain in DRY-RUN mode: `dedup.rebuild-gold-labels` (dry-run) → `dedup.calibrate-composite` (dry-run) → log a drift summary (label-bucket deltas + recommended-vs-current config diff, from the two op reports). Built-in-DISABLED: `scheduled.label_refinement.enabled` defaults to `false`. MIRROR the existing `dedup_refresh` scheduled-task shape exactly — one `ScheduledTaskConfig` field, three viper defaults, one `TaskDefinition` — and nothing more (WF-3 will subsume this).

## Background (verify before editing)

- Config pattern to mirror: `ScheduledTaskConfig {Enabled bool; Interval int; OnStartup bool}` and `ScheduledTasksConfig` (fields like `DedupRefresh`) in `internal/config/config.go`, with viper defaults set nearby (`scheduled.dedup_refresh.enabled` → `false`, etc.).
- Scheduler pattern to mirror: `registerAllTasks` in `internal/scheduler/tasks.go` registers `TaskDefinition`s; the `dedup_refresh` entry shows the `IsEnabled`/`Interval`/`RunOnStart` closures reading `config.AppConfig.Scheduled.<Field>`.
- Ops to chain (both already dry-run by default — pass NO apply param): `dedup.rebuild-gold-labels` (see its `Apply bool` param — omit it) and `dedup.calibrate-composite` (TASK-05). Trigger them the way the scheduler's existing task bodies trigger ops (follow a sibling task's run function; do not invent a new op-invocation path).
- ⚠ The "scheduled loop never applies" guarantee rests ENTIRELY on both ops defaulting `Apply=false` when the key is absent. Verify this holds at your HEAD, not just historically: rebuild-gold-labels must default `Apply` to false and return before any delete/write when `!params.Apply` (the guarded dry-run return), and calibrate-composite must default `Apply=false` (TASK-05). If either default has changed, STOP and report — an empty-params chain would have silently become a timed prod mutation.
- Human labels are never at risk even if someone later mis-wires apply: the rebuild op preserves `source=human` rows (`case "human"` passthrough in `internal/plugins/dedup/rebuild_gold_labels.go`). Still: NO apply on the scheduled path, period.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "type ScheduledTaskConfig struct\|type ScheduledTasksConfig struct" internal/config/config.go   # mirror target, 2 hits
  grep -n 'scheduled.dedup_refresh.enabled' internal/config/config.go                                     # defaults block, 1 hit
  grep -n "func (ts \*TaskScheduler) registerAllTasks" internal/scheduler/tasks.go                        # edit target, 1 hit
  grep -n '"dedup_refresh"' internal/scheduler/tasks.go                                                   # mirror entry, >=1 hit
  grep -n 'case "human"' internal/plugins/dedup/rebuild_gold_labels.go
  grep -rn '"dedup.calibrate-composite"' internal/plugins/dedup/                                          # ⛔ DEPENDENCY GATE (TASK-05), >=1 hit once merged
  grep -n 'Apply bool\|if !params.Apply' internal/plugins/dedup/rebuild_gold_labels.go                    # dry-run default + guarded no-write return, 2 hits — the chain's safety rests on this
  grep -n 'Apply bool' internal/plugins/dedup/calibrate_composite.go                                      # ⛔ DEPENDENCY GATE (TASK-05): file is created by TASK-05; "No such file" or 0 hits = TASK-05 unmerged
  ```
  If any grep returns 0 hits, STOP and report — do not guess. Exception semantics for the two ⛔ DEPENDENCY GATE lines: 0 hits (or "No such file") there means TASK-05 is unmerged — report "BLOCKED: TASK-05 unmerged" (a sequencing stop), not anchor drift. All other lines resolve against origin/main today; 0 hits on any of THOSE is drift.

## Step-by-step

1. `internal/config/config.go` — add `LabelRefinement ScheduledTaskConfig \`json:"label_refinement" mapstructure:"label_refinement"\`` to `ScheduledTasksConfig` (alphabetical/nearby placement consistent with siblings), and three viper defaults next to the existing `scheduled.*` block: `scheduled.label_refinement.enabled` → **`false`**, `scheduled.label_refinement.interval` → `10080` (weekly, minutes), `scheduled.label_refinement.on_startup` → `false`.
2. `internal/scheduler/tasks.go` — in `registerAllTasks`, add a `TaskDefinition` named `label_refinement`, cloning the `dedup_refresh` entry's closure shape (`IsEnabled`, interval, `RunOnStart` reading `config.AppConfig.Scheduled.LabelRefinement`). Its run body: trigger `dedup.rebuild-gold-labels` with empty params (dry-run), wait for completion the way sibling tasks do (see `WaitForOperation` on the scheduler), then trigger `dedup.calibrate-composite` with empty params (dry-run), then log one summary line (info level) with both op IDs and headline counts pulled from their results if accessible — otherwise just the op IDs (the reports are inspectable in the ops UI; do not build report-parsing plumbing).
3. Edge-case semantics (also in acceptance): `Interval <= 0` with `Enabled=true` must not busy-loop — mirror however `dedup_refresh` guards a zero interval; default config keeps the task fully inert (never scheduled, never run on startup).
4. Purely additive: no changes to other task definitions, no new op-invocation abstraction, no workflow objects (WF-3's job), no Settings-UI work.
5. Tests — nearest scheduler test file (create `internal/scheduler/tasks_label_refinement_test.go` if none fits): `TestLabelRefinementRegistered` (task exists in the registry), `TestLabelRefinementDisabledByDefault` (`IsEnabled()` false under default config), `TestLabelRefinementChainPassesNoApply` (the params passed to both ops contain no `"apply"` key). ⚠ "No apply key" equals "dry-run" ONLY because both ops default `Apply=false` — tie the guarantee to that default so a future default-flip is caught: add a comment on the test stating the dependency, and a companion assertion that exercises the actual default (e.g. unmarshal empty params into `rebuildGoldLabelsParams`/`calibrateCompositeParams` and assert `Apply == false`; if the param structs aren't importable from the scheduler package, put that assertion in the respective plugin test files and reference it from the comment). Anti-over-suppression: N/A (this task adds no filter/guard/veto/skip/dedupe path — it adds a disabled scheduled chain).
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test ./internal/scheduler/... ./internal/config/... -race
go test ./... -short
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "label_refinement" internal/config/config.go` hits (struct field + 3 viper defaults, enabled default literally `false`)
- [ ] `grep -n "label_refinement" internal/scheduler/tasks.go` hits (task registered)
- [ ] `grep -n '"apply"' internal/scheduler/tasks.go` returns 0 hits inside the new task body (no apply on the scheduled path — verify via `TestLabelRefinementChainPassesNoApply`)
- [ ] `go test ./internal/scheduler/ -run TestLabelRefinement -v` — all three tests pass, including `TestLabelRefinementDisabledByDefault`
- [ ] the no-apply-key guarantee is tied to the verified `Apply=false` defaults: the anchor greps for `Apply bool` / `if !params.Apply` hit, and the empty-params → `Apply == false` assertion (step 5) exists
- [ ] Anti-over-suppression: N/A
- [ ] `go test ./... -short` green; `make ci` green
- [ ] File headers bumped on every changed file

## Commit message

```
feat(dedup): scheduled label-refinement loop (dry-run chain, built-in-disabled) (INIT-1 T6)

Adds scheduled.label_refinement (default enabled=false) chaining
dedup.rebuild-gold-labels and dedup.calibrate-composite in dry-run/report
mode only — applies remain operator AskUserQuestion decisions. Kept minimal
by design: INIT-6 WF-3 is expected to subsume these scheduled.* keys.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-label-quality-scheduled-refinement-loop
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "label_refinement" internal/config/config.go` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit, or operationally set `scheduled.label_refinement.enabled=false` (which is already the shipped default); the chain writes nothing when it does run (dry-run ops only), so no data rollback ever applies.
