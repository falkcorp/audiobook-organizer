<!-- file: docs/agent-tasks/platform-core/F2-11-late-owner-minimal-registrations.md -->
<!-- version: 1.1.1 -->
<!-- guid: 6c13340d-6da2-494d-bec0-60353aaa03cf -->
<!-- last-edited: 2026-11-05 -->

# F2-11 — Minimal owner registrations of the three late-owner kinds over one-method interim interfaces: `providerhttp.InterimHolds`, `ai.InterimPoll`, `diagnostics.InterimExport`

| Field | Value |
|---|---|
| ID | F2-11 |
| Release / flip | R2, foundation PRs (F2), **dark** (registered on the executor only from F5, when ABO passes a backend) |
| Wave | W29 |
| Tier | **T2** (Sonnet / high): three `TaskSpec`s over one-method interfaces, every field stated by 05-tasks §5T.8.2 |
| Depends on | F2-01, F2-09 (the `core.ai` section), F2-04 (the batch-poll schedule key `core.ai.schedules.batch_poll`) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-11-late-owner-kinds
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

No `go work init` (CLAUDE.md). Core tests run from inside `core/`.

## What the spec fixes (copy exactly)

05-tasks §5T.8.2 "Three core kinds whose owners flip after F5" states every field of the three `TaskSpec`s and the
one method of each interim interface (copied from 05-metadata M2.4, 05-ai §4 and 05-health §5H.3.5, which match):

```go
package providerhttp // F2 to F7
type InterimHolds interface {
	ReleaseHolds(rc *tasks.RC, p ReleaseHoldsParams) (ReleaseHoldsResult, error)
}

package ai // F2 to F7
type InterimPoll interface {
	PollBatches(rc *tasks.RC, p BatchPollParams) (BatchPollResult, error)
}

package diagnostics // F2 to F8
type InterimExport interface {
	Export(rc *tasks.RC, p ExportParams) (ExportResult, error)
}
```

| Kind | Shape | `Effects` | `Resume` | `Timeout` | `Perms` | `Schedule` (key) | Other |
|---|---|---|---|---|---|---|---|
| `core.integrations.release-holds` | Task | `tasks.Writes(tasks.Res("core", "providerhttp"))` | `ResumeDrop` | 1 m | `Start: rbac.Need("core:integrations.manage")` | none | `Priority: PriorityHigh` |
| `core.ai.batch-poll` | Task | `tasks.Writes(tasks.Res("core", "ai"))` | `ResumeFromZero` | 10 m | `Start: rbac.Need("core:integrations.manage")` | `tasks.ScheduleFromConfig("core.ai.schedules.batch_poll").Live()` | takes `batch_poller`'s clock through M4 |
| `core.diagnostics.export` | Task | `tasks.ReadOnly()` | `ResumeDrop` | 30 m | `Start: rbac.Need("core:system.manage")` | none | progress per section |

The params and result types are the owner package's, declared here with the registration (05-tasks §5T.8.2: "the kind's
final params and result types, which the owner package declares with the minimal registration at F2"):
`providerhttp.ReleaseHoldsParams` (empty) and `ReleaseHoldsResult` (`Listed`, `Released` and each listed hold, 05-metadata
M2.4); `ai.BatchPollParams` (empty) and `BatchPollResult` (05-ai §4 item list); `diagnostics.ExportParams` and
`ExportResult` (05-health §5H.3.5). Copy their fields from those sections. The interim registration's `Run` is the
backend's method value.

Re-verify the spec text at your base (a zero means the spec moved: STOP and report):

```bash
S=docs/specs/platform-core
grep -c 'type InterimHolds interface\|type InterimPoll interface\|type InterimExport interface' $S/05-tasks.md   # 3
grep -c 'ReleaseHoldsParams' $S/05-metadata.md      # >=1
grep -c 'BatchPollParams' $S/05-ai.md               # >=1
grep -c 'ExportParams' $S/05-health.md              # >=1
```

## Goal

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
  by F2-04 (a merged dependency); check `grep -n 'batch_poll' core/kernel/core_sections.go` → ≥1 hit, else STOP.

## Base state

```bash
grep -rn 'func Task\[' core/tasks/                     # 1 hit (F2-01), else STOP
grep -rn 'func Conformance' core/tasks/taskstest/      # 1 hit, else STOP
test -d core/health && echo ok                          # "ok" (R1-09, F2-10)
test ! -e core/integrations && echo absent              # "absent" (this PR creates the two leaf packages)
ls core/health/diagnostics 2>/dev/null | wc -l          # 0
grep -n 'batch_poll' core/kernel/core_sections.go | wc -l   # >=1 (F2-04), else STOP
grep -rn '"core:integrations.manage"\|"core:system.manage"' core/rbac/catalog.go | wc -l   # >=2 (F2-08's catalog), else STOP
```

## Files

Create `core/integrations/providerhttp/` (one file + test), `core/integrations/ai/` (one file + test),
`core/health/diagnostics/` (one file + test). Nothing else: no `core/integrations/integrations.go` (F7-01), no
`app.Options.InterimBackends` (F5-18), no kernel change.

## Tests

`TestReleaseHoldsTask_Conformance`, `TestBatchPollTask_Conformance`, `TestExportTask_Conformance` (names coined here):
`taskstest.Conformance(t, def)` passes; the kind string is exactly `core.integrations.release-holds`,
`core.ai.batch-poll`, `core.diagnostics.export`; `Effects`, `Resume`, `Timeout`, `Perms.Start`, the schedule and
`Priority` are exactly the table above; `Run` delegates to the fake backend's method (a fake records the call and its
params).

## Acceptance

```bash
cd core
go build ./... && go vet ./integrations/... ./health/diagnostics/...                       # exit 0
go test ./integrations/... ./health/diagnostics/... -race -count=1                         # ok
go test ./integrations/... ./health/diagnostics/... -run 'Conformance' -count=1 -v | grep -c '^--- PASS'   # 3
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
