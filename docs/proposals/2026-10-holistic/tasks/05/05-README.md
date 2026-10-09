<!-- file: docs/proposals/2026-10-holistic/tasks/05/05-README.md -->
<!-- version: 1.0.1 -->
<!-- guid: 7d3e9b14-5c2a-4f68-a1e0-9b4c6d8f2a73 -->
<!-- last-edited: 2026-10-09 -->

# 05 Ops v3 platform: task briefs (wave 1)

Briefs follow `../00-TEMPLATE.md`. Listed in the 08 section 5 order. Line numbers in the
briefs were verified at repo HEAD `93a9b745f`; re-grep before editing.

## Briefs

| ID | Title | Wave | Model | Size | Depends on |
|---|---|---|---|---|---|
| [05-PR0](05-PR0.md) | Docs truth pass (AI-REFERENCE, types.go comment, writing-a-plugin) | 1 | sonnet | S | none |
| [05-PR1](05-PR1.md) | `state` package, `opsgen`, generated `ops.ts` | 1 | opus | M | PR0 |
| [05-PR3](05-PR3.md) | Fence, zombie handle held until goroutine exit, 10-min WARN | 1 | opus | M | none |
| [05-PR4](05-PR4.md) | `OpsV3Store`, `opv3:` keys, migration 065, v2 dual-write | 1 | opus | L | PR1, PR3 |
| [05-PR2](05-PR2.md) | OTel ops instruments, outcome set, zombie gauge | 1 | sonnet | S | PR1, PR4, 11 PR4, PR3 |
| [05-PR5](05-PR5.md) | `pkg/ops` SDK, chunk-leased runner and ledger, Writer, v2 adapter | 1 | opus | L | PR3, PR4, 08 X2, 07 R2-R4 |
| [05-PR6](05-PR6.md) | `opstest` harness, fault injection, conformance, mutation tests | 1 | opus | M | PR5 |
| [05-PR7](05-PR7.md) | `opscatalog`, `oplint` as go/analysis, startup gate, ledger embed | 1 | opus | M | PR5, PR6, 07 S3 |
| [05-PR8](05-PR8.md) | `ops.Schedule` in TaskScheduler plus cron evaluator | 1 | sonnet | M | PR5, PR7, 04 P1 (and 04 P4a-P4f, P9, P12; 02 PR14 is a `tasks.go` rebase note only, this PR does not edit that file) |
| [05-PR9](05-PR9.md) | `/api/v3/ops/*` census, timeline, SSE | 1 | sonnet | M | PR4, PR5, PR8 |

Model split: opus 6 (PR1, 3, 4, 5, 6, 7), sonnet 4 (PR0, 2, 8, 9). PR7 was moved from the
requested sonnet to opus: the `go/analysis` writer-bypass rule needs cross-package judgment.

## Not briefed here (see `implementation-briefs.md`)

| Item | Wave | Pointer |
|---|---|---|
| Port waves 12A-12H | 2 onward, per wave | "Port waves" section; each wave gets its brief when its predecessors land |
| PR 10 (operations UI) | after PR 9 | "PR 10" |
| PR 11 (alerts and schedule lag/missed) | after PR 8 and 11 PR 4 | "PR 11" |
| PR 13 | after port waves | "PR 13" |
| PR 14 | after port waves | "PR 14" |
| PR 15 (v2compat removal) | late | "PR 15". Note D53 supersedes the "allowlist of 4": the four frozen ops are wrapped as native Tasks so v2compat is deleted outright |
| PR 16 (drop `opv2:` mirror) | at least 30 days after the last wave (D25) | "PR 16" |

## Landing order
PR0, PR1, PR3 (parallel-safe), PR4, PR2 (after 11 PR4), PR5, PR6, PR7, PR8, PR9. `go.mod`
order: 01 P2, 11 PR 2, PR7 (x/tools), PR8 (if any dependency), 01 P75, 07 F1. `tasks.go`
order: 04 P1, P4a, P9, P12, 02 PR14, PR8, 12B, PR15.

## Spec corrections made inside the briefs
- PR2 instruments follow doc 11 (`internal/opsmetrics`, `audiobook_organizer.ops.*`, label `def_id`), not the upstream 05 brief.
- Progress across a resume is monotone: counters fold only at chunk completion.
- `opv3:` families are registered by PR4, not PR2. No new `opv2:` census is needed (existing census counts it).
- PR1 pollers keep stopping on every `interrupted*` status.
- A v3 zombie stays `stopping`, so EnqueueOp dedupe must skip it.
- PR8 does not enable the D23 checks; 04 P1 does. PR8 adds `Schedule.Preview()`, absent from sdk-api.

## Unverified
- No registry endpoint lists zombies; PR3 adds registry methods only.
- 11 PR4 had not landed: zombie-gauge hook shape and test provider injection unconfirmed.
- 07 R2-R4 readiness API shape unknown.
- Whether any plugin beyond the five has a static `OperationDefs()`.
- Real credits and tag types for the Writer `CreditsFn`/`TagsFn`.
- Whether `testing/synctest` works with the runner.
- Migration 065 cost on prod unmeasured; 065 may be taken by implementation time.
