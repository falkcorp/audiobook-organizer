<!-- file: docs/agent-tasks/platform-core/F2-11-late-owner-minimal-registrations.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6c13340d-6da2-494d-bec0-60353aaa03cf -->
<!-- last-edited: 2026-11-05 -->

# F2-11 — Minimal owner registrations of the three late-owner kinds over one-method interim interfaces: `providerhttp.InterimHolds`, `ai.InterimPoll`, `diagnostics.InterimExport`

> **SPEC-GATED (partly).** The spec fixes the three kinds, their owners and the interface names, but not the
> interfaces' one method, nor the `Resume`, `Timeout` and `Perms.Start` of two of the three kinds. Step 0 checks for
> that text; STOP if it is missing. Do not choose a resume policy yourself: 05-tasks A2 makes it a data-correctness
> decision that the spec must state.

| Field | Value |
|---|---|
| ID | F2-11 |
| Release / flip | R2, foundation PRs (F2), **dark** (registered on the executor only from F5, when ABO passes a backend) |
| Wave | W23 |
| Tier | **T2** (Sonnet / high): three `TaskSpec`s over one-method interfaces, once the spec states them |
| Depends on | F2-01 |
| Gate | Plan, then execute autonomously in a worktree with its own PR (after Step 0 passes) |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-11-late-owner-kinds
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

## Step 0 — spec gate

What the spec states (enough for `core.diagnostics.export`; not for the other two):

| Kind | Owner package | Stated | Missing |
|---|---|---|---|
| `core.diagnostics.export` | `health/diagnostics` | Shape Task, `ReadOnly`, `Resume: ResumeDrop`, `Timeout: 30m`, `Perms.Start: core:system.manage`, progress per section (05-health §5H.3.5) | the method of `diagnostics.InterimExport` |
| `core.integrations.release-holds` | `integrations/providerhttp` | Shape implied Task; Preview lists, Live releases; needs `core:integrations.manage`; no schedule; touches only `c/providerhttp/holds/` (05-metadata M2.4) | `Resume`, `Timeout`, the `Effects` resource ref, the method of `providerhttp.InterimHolds` |
| `core.ai.batch-poll` | `integrations/ai` | `Effects: tasks.Writes(tasks.Res("core", "ai"))`; `tasks.ScheduleFromConfig("core.ai.schedules.batch_poll")`, default `{enabled: true, every: 5m}`; stages each ready job's apply through `tasks.Stager.StartIn`, `ModeLive` (05-ai §4, §7) | `Resume`, `Timeout`, `Perms.Start`, the method of `ai.InterimPoll` |

05-tasks §5T.8.2 says F2 creates "the kind's final `TaskSpec` (name, shape, `Perms`, `Resume`, `Timeout`,
`Parallelism`, `Effects` on the owner's own core resource, and its schedule key …) over a one-method interim backend
interface", so all of these are required.

```bash
cd "$REPO/.worktrees/$SLUG"
S=docs/specs/platform-core
grep -c 'type InterimHolds interface' $S/05-metadata.md $S/05-tasks.md   # need >=1 in one file (0 at the plan commit)
grep -c 'type InterimPoll interface'  $S/05-ai.md $S/05-tasks.md         # need >=1 (0 at the plan commit)
grep -c 'type InterimExport interface' $S/05-health.md $S/05-tasks.md    # need >=1 (0 at the plan commit)
grep -n 'release-holds' $S/05-metadata.md | grep -c 'Resume'             # need >=1 (0 at the plan commit)
grep -n 'batch-poll' $S/05-ai.md | grep -c 'Resume'                      # need >=1 (0 at the plan commit)
```

If any check fails: STOP and report "F2-11 is spec-gated: the one-method signatures of providerhttp.InterimHolds,
ai.InterimPoll and diagnostics.InterimExport, and Resume/Timeout/Perms.Start of core.ai.batch-poll and Resume/Timeout/
Effects of core.integrations.release-holds, are not stated (05-tasks §5T.8.2 requires them)."

## Goal (once Step 0 passes)

In each owner's package, create a **minimal owner registration**: an exported constructor returning the kind's final
`tasks.Definition` over that package's one-method interim interface (05-tasks §5T.8.2 "Three core kinds whose owners
flip after F5"):

- `core/integrations/providerhttp/`: `type InterimHolds interface { … }` and the `core.integrations.release-holds`
  definition.
- `core/integrations/ai/`: `type InterimPoll interface { … }` and the `core.ai.batch-poll` definition, schedule
  `tasks.ScheduleFromConfig("core.ai.schedules.batch_poll").Live()`.
- `core/health/diagnostics/`: `type InterimExport interface { … }` and the `core.diagnostics.export` definition.

Each package holds **only** that interface and its definition until its owner's flip (04 §8.1 F2: "`integrations/
providerhttp` and `integrations/ai` only the kind's `TaskSpec` and the interim interface until F7, `health` only its
readiness core until F8"). Nothing registers the kinds: the kernel does at P6 from F5, only when ABO sets the matching
`app.Options.InterimBackends` field (06 §6.4.5), and that field is the F5 flip's, not this PR's.

Constructor names (coined here; one per package, so F5 and the owner flips extend the same function):
`providerhttp.ReleaseHoldsTask(b InterimHolds) tasks.Definition`, `ai.BatchPollTask(b InterimPoll) tasks.Definition`,
`diagnostics.ExportTask(b InterimExport) tasks.Definition`.

## Invariants

- The kind name, its stored runs, the poll's clock and the schedule key never change at F5, F7 or F8 (no second rename;
  05-tasks §5T.8.2).
- No backend writes a module entity under a core kind (COR2-11): `InterimPoll`'s apply staging goes through
  `tasks.Stager.StartIn` as a books run.
- `Effects` names only the owner's own core resource; `Perms.Start` is a `core:` scope from F2-08's catalog.
- Each definition passes `taskstest.Conformance` (F2-01).
- The schedule key `core.ai.schedules.batch_poll` is declared with type `tasks.ScheduleConfig` in the `core.ai` section
  (F2-09); check `grep -n 'batch_poll' core/kernel/core_sections.go` → ≥1 hit, else STOP.

## Base state

```bash
grep -rn 'func Task\[' core/tasks/                     # 1 hit (F2-01), else STOP
grep -rn 'func Conformance' core/tasks/taskstest/      # 1 hit, else STOP
test -d core/health && echo ok                          # "ok" (R1-09, F2-10)
test ! -e core/integrations && echo absent              # "absent" (this PR creates the two leaf packages)
ls core/health/diagnostics 2>/dev/null | wc -l          # 0
```

## Files

Create `core/integrations/providerhttp/` (one file + test), `core/integrations/ai/` (one file + test),
`core/health/diagnostics/` (one file + test). Nothing else: no `core/integrations/integrations.go` (F7-01), no
`app.Options.InterimBackends` (F5-18), no kernel change.

## Tests

`TestReleaseHoldsTask_Conformance`, `TestBatchPollTask_Conformance`, `TestExportTask_Conformance` (names coined here):
`taskstest.Conformance(t, def)` passes; the kind string is exactly `core.integrations.release-holds`,
`core.ai.batch-poll`, `core.diagnostics.export`; `Effects` and `Perms.Start` are those Step 0's spec text states; a
Preview run of release-holds calls the backend in list-only mode (a fake backend records the call).

## Acceptance

```bash
cd core
go build ./... && go vet ./integrations/... ./health/diagnostics/...                       # exit 0
go test ./integrations/... ./health/diagnostics/... -race -count=1                         # ok
grep -rn '"core.integrations.release-holds"\|"core.ai.batch-poll"\|"core.diagnostics.export"' integrations/ health/diagnostics/ | grep -v _test | wc -l   # 3
grep -rn 'audiobook-organizer' integrations/ health/diagnostics/ | wc -l                   # 0 (no ABO import)
cd ..
make -C core ci                                                                            # exit 0
make ci                                                                                    # exit 0
```

## Out of scope

Registering the kinds, `app.Options.InterimBackends` and the books backends (F5-18); the native bodies (F7 for holds and
poll, F8 for export); the kind-rename map entries (F5-16/F5-18).

## Rollback

Revert; nothing registers or imports these packages before F5.

## Repo rules

File headers; conventional commit (`feat(core): minimal owner registrations of the three late-owner kinds (dark)`) with
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; `changelog.d/` fragment.

## Planner notes

- SPEC-GATED: the three interim interfaces' one method is unstated anywhere (06 §6.4.5 names only the types), and
  `core.ai.batch-poll` lacks `Resume`, `Timeout` and `Perms.Start`, and `core.integrations.release-holds` lacks `Resume`,
  `Timeout` and its `Effects` resource ref. `core.diagnostics.export` is fully stated except its interface method.
- Missing dependency: the batch poll's schedule key must be declared in the `core.ai` section by F2-09 (W22). The wave
  order satisfies it, but the row lists only F2-01.
