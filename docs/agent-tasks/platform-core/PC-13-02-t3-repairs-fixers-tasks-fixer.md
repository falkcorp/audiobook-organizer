<!-- file: docs/agent-tasks/platform-core/PC-13-02-t3-repairs-fixers-tasks-fixer.md -->
<!-- version: 1.0.0 -->
<!-- guid: db64cde2-c373-4d47-b289-7c4cb4f862c5 -->
<!-- last-edited: 2026-11-04 -->

# PC-13-02 — T3: the repairs fixers become tasks.Fixer; repairs.plan/apply removed

| Field | Value |
|---|---|
| ID | PC-13-02 |
| Tier | **T2 Sonnet/high** — fixer semantics (plan, re-plan, fingerprint check, owner grants, marks during quiesce) must hold exactly; see Planner notes on tier |
| Wave | W13 (stage S5, release `S5r2`) |
| Depends on | PC-13-01 |
| Trailer | `Stage-Release: S5 2` on every commit |
| Spec | 05-tasks §5T.8.2 row T3; §5T.3.1 (`FixerSpec`); §5T.3.7 (Approval, Permissions); §5T.8.1 rows `internal/repairs` and `internal/opsmetrics`; 05-metrics §6 and §13 (the `ops_abandoned_calls` row); digest S5 §5 rule 10 |

## Gate

Merges only after PC-13-01 has merged (same release `S5r2`, whose predecessor row `S5r1` OWNER-13 records).
This PR does not change `ci/stage.txt`.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-02 -b pc-13-02-t3-repairs-fixer origin/main
cd ../audiobook-organizer-pc-13-02
git rebase origin/main
npm ci --prefix web
git worktree list
```

## Goal

Each Repairs-lane fixer (`internal/repairs` framework, registered by the maintenance plugin) becomes a native
`tasks.Fixer` definition registered by books; the two v2 ops `repairs.plan` and `repairs.apply` that drive every
fixer today are removed. The `/api/v1/repairs/*` routes keep HEAD's request and response bodies and start runs of
the fixer kinds through the tasks client. **No behaviour change** (05-tasks §5T.8.2 T3: binary swap).

The repairs source of `ops_abandoned_calls` **keeps calling `opsmetrics.RegisterAbandoned`** in this PR
(`internal/repairs/guards.go:209`); its owner changes only in PC-13-13 (05-tasks §5T.8.1 opsmetrics row; 05-metrics
§13). Do not touch `internal/opsmetrics` or `registerAbandonedGauge`.

## Exact files

Modify (inventory: `internal/repairs/`; the rest is listed under Planner notes):

- `internal/repairs/*.go` — the framework: `fixer.go` (the `Fixer` interface, `fixer.go:74`), `engine.go`
  (`RunItems` at `:130` guard pass and `:715` apply), `registry.go`, `planstore.go`, `guards.go` (keep line `:209`
  as is), `standdown.go`, `wire.go`, `writer*.go`, and their tests
- `internal/plugins/maintenance/plugin.go` (`Repairs()`, lines 46-127 at HEAD), `internal/plugins/maintenance/repairs_ops.go`
  (deleted) and the fixer files whose `Plan`/`Replan` signatures name `registry.Reporter`
- `internal/server/handlers/repairs/handler.go`, `internal/server/wire_repairs_routes.go`
- `internal/opscatalog/tombstones.go` (two tombstone rows)
- one `changelog.d/` fragment

## Re-verify greps (HEAD `f07f516be`; `748fd787a` identical for these files unless noted)

```bash
# 1. Fixers registered (expect 24 Register lines registering 25 fixers: the loop at plugin.go:52 registers 2)
grep -c 'repairsReg.Register(' internal/plugins/maintenance/plugin.go                         # 24
sed -n 46,127p internal/plugins/maintenance/plugin.go | grep -oE 'new[A-Za-z]+Fixer\(p\)' | sort -u | wc -l   # 25
# 2. The Fixer interface (expect 1 hit at fixer.go:74) and its reporter-typed methods (expect 2: Plan, Replan)
grep -n '^type Fixer interface' internal/repairs/fixer.go
grep -nE '^\s+(Plan|Replan)\(ctx context.Context, params json.RawMessage' internal/repairs/fixer.go
# 3. The two v2 ops (expect 2 defs in repairs_ops.go)
grep -n 'func (p \*Plugin) repairsPlanDef\|func (p \*Plugin) repairsApplyDef' internal/plugins/maintenance/repairs_ops.go
# 4. Both IDs are op-ledger lines and not tombstoned yet (expect 1, 1, 0, 0)
grep -cx 'repairs.plan' internal/server/testdata/op_ids.golden; grep -cx 'repairs.apply' internal/server/testdata/op_ids.golden
grep -c '"repairs.plan"' internal/opscatalog/tombstones.go; grep -c '"repairs.apply"' internal/opscatalog/tombstones.go
# 5. The engine's two worker pools and their defaults (expect engine.go:120 conc := deps.Concurrency (0 -> NumCPU)
#    and :711 conc := deps.Concurrency (0 -> defaultApplyConcurrency = 4, :489))
grep -n 'conc := deps.Concurrency\|const defaultApplyConcurrency' internal/repairs/engine.go
# 6. The abandoned-calls caller that must stay (expect 1 hit, guards.go:209)
grep -n 'RegisterAbandoned(opsmetrics.SourceRepairsResolver' internal/repairs/guards.go
# 7. The repairs routes (expect 6 lines, wire_repairs_routes.go:45-53)
grep -n 'protected\.\(GET\|POST\)("/repairs' internal/server/wire_repairs_routes.go
# 8. W12/PC-13-01 symbols (0 at HEAD; >= 1 at your base, else STOP)
grep -n '^func Fixer\[' core/tasks/*.go
grep -rn 'func Conformance' core/tasks/taskstest/*.go
```

The 25 fixers at HEAD, by `FixerID`: consolidation-leftovers, dedup.series-prune, dedup.split-books,
duplicate-copies, folder-books, fragment-consolidation, maintenance.audible-read-status,
maintenance.author-named-series, maintenance.normalize-letter-l-ordinals, maintenance.refetch-lost-candidates,
maintenance.relink-pre-fix-imports, maintenance.relink-stale-series, maintenance.repair-combined-author-credits,
maintenance.repair-junk-authors, maintenance.repair-junk-titles, maintenance.repair-swapped-title-author,
maintenance.reparse-folder-names, maintenance.scan-title-revert, maintenance.tag-franchise,
maintenance.version-twin-metadata, master-list-import, reconcile.missing-files, retired-versions,
stale-itunes-path, and the version-group-primary fixer (`newVGPrimaryFixer`). The spec's count (24 at
`748fd787a`) is informative; use the count at your base and say so in the PR.

## Design latitude and invariants

You may choose how the HEAD `Fixer` maps onto `tasks.FixerSpec`. The recommended shape is **one generic adapter in
`internal/repairs`** (e.g. `func Definition(fx Fixer, deps EngineDeps) tasks.Definition`, name coined here) so the
fixer files change only where their method signatures name `registry.Reporter`. Whatever you choose, these hold:

1. **Kinds.** Each fixer is one kind `books.repairs.<FixerID>` (name rule coined here: the `repairs` segment keeps
   fixer IDs such as `dedup.series-prune` from colliding with the T7 kind of the server op `dedup.series-prune`).
   `LegacyDefID` is `""`: no HEAD op ID belongs to one fixer (5T.3.1; a run of a kind an older build does not know
   is dropped by its resume sweep, 09 §9.2.1 row 5).
2. **Plan = Preview run, apply = Live run.** A Preview run's rows equal HEAD `Plan`'s rows for the same params and
   store; a Live run applies only rows whose fresh `Replan` fingerprint matches the stored plan, and reports
   `changed_since_plan` (`ErrChangedSincePlan`) and `partially_applied` (`ErrPartiallyApplied`, checked first) exactly
   as `engine.go` does. `Approval{PlanRequired: true}` (5T.3.7) carries HEAD's "apply names a stored plan"
   (`plan_op_id`).
3. **Owner grants.** Rows a plan lists as `owner_applicable` apply only under the one-shot owner grant minted by
   `POST /repairs/:fixer/owner-apply` for an interactive sign-in, never from params, a retry or a resume (HEAD's
   `repairs.apply` description). The existing `internal/server/handlers/repairs/owner_apply_test.go` cases stay green.
4. **Stand-down and marks.** Apply still pauses a running `library.scan` through the scan stand-down instead of
   refusing; a row's mark commits even while a stand-down quiesces the run (`TestFixerMarksLandDuringQuiesce` is
   core's; the repairs `standdown_*_test.go` cases stay green).
5. **Parallelism 1:1.** `FixerSpec.Parallelism` is `tasks.Fixed(conc, "legacy Concurrency")` where `conc` is the
   value `engine.go:711-713` computes today (`deps.Concurrency`, else `defaultApplyConcurrency` = 4); the guard pass
   (`engine.go:120`, `deps.Concurrency`, else `runtime.NumCPU()`) keeps its value through
   `tasks.RunItems(rc, tasks.Fixed(conc, "legacy Concurrency"), ...)`. No other parallelism change (CLAUDE.md;
   05-tasks §5T.8.2 T0+T1).
6. **Permissions.** `Perms.Start` and `Perms.Approve` keep the legacy atom the routes check today
   (`library.edit_metadata` for plan and apply, `wire_repairs_routes.go:47,49`) as `rbac.LegacyAtom(...)`;
   `Perms.View` keeps `library.view` (5T.3.7 "An adapted legacy def's `Perms.Start` is the one legacy-atom
   requirement value").
7. **Resume.** Apply keeps HEAD's restart-with-checkpoint behaviour (settled rows are not re-applied after a
   restart); plan keeps drop.
8. **Routes.** `GET /repairs`, `GET /repairs/owner-status`, `POST /repairs/:fixer/plan`,
   `GET /repairs/:fixer/plan/:op_id/rows`, `POST /repairs/:fixer/apply`, `POST /repairs/:fixer/owner-apply` keep
   their paths, permissions and bodies (the `op_id` in a path or body is the run ID; books' store keeps HEAD's op
   IDs as run IDs, 5T.3.3).
9. **Ledger.** `repairs.plan` and `repairs.apply` stop resolving, so add both to `opscatalog.Tombstones` with the
   reason "replaced by one tasks.Fixer kind per fixer at T3 (05-tasks §5T.8.2)". `op_ids.golden` itself does not
   change (append-only).

## Steps

1. Run the greps. STOP on any zero-hit edit target or moved count.
2. **Tests first.** Add `internal/repairs/fixer_conformance_test.go` running `taskstest.Conformance(t, defs...)`
   over every fixer definition (the "Fixer conformance suite", 05-tasks §5T.9 last row), and a parity test
   `TestFixerPreviewRowsMatchHEADPlan` (name coined here) that, on the existing repairs fixtures, compares a Preview
   run's rows to the rows HEAD's `Plan` returns for three fixers (folder-books, retired-versions,
   dedup.series-prune). Commit them failing-compile-free (skip with `t.Skip("lands with the adapter")` until Step 4,
   then remove the skip in the same PR).
3. Change the `Plan`/`Replan` reporter parameter from `registry.Reporter` to a repairs-local interface or
   `*tasks.RC` across `internal/repairs` and the fixer files (signature-only edits in the fixer files).
4. Write the adapter and register the definitions through books' seam (the one PC-13-01 used).
5. Delete `repairs_ops.go`, `repairsPlanDef`/`repairsApplyDef` and their two entries in the maintenance plugin's
   `Register` list; add the two tombstones.
6. Re-point `internal/server/handlers/repairs/handler.go` to start runs of `books.repairs.<FixerID>` through the
   tasks client, keeping bodies.
7. `make ci`; changelog; headers.

## Code contract (05-tasks §5T.3.1, quoted)

```go
type FixerSpec[P, C any] struct {
	Common // Effects must be Writes(...); Approval required
	Candidates  func(rc *RC, p P) (Source[C], error)
	Load        func(rc *RC, p P, rowID string) (C, bool, error)
	Evaluate    func(ic *ItemRC, p P, c C) (Row, error) // read-only; runs at plan AND apply
	Apply       func(ic *ItemRC, w Writer, p P, row Row) error
	Parallelism Parallelism
	PartitionBy func(Row) string // optional; Row.Subjects always act as ExclusiveRefs at apply
	AfterApply  func(rc *RC, applied []string) error
	Guards      []Guard
}
func Fixer[P, C any](kind Kind, s FixerSpec[P, C]) Definition
type Approval struct { PlanRequired bool; MaxPlanAge time.Duration }
```

## Acceptance

```bash
grep -rn 'repairs.PlanOpID\|repairs.ApplyOpID' --include=*.go . | grep -v _test | wc -l      # 0
test ! -f internal/plugins/maintenance/repairs_ops.go && echo gone                           # gone
grep -c '"repairs.plan"\|"repairs.apply"' internal/opscatalog/tombstones.go                  # 2
grep -n 'RegisterAbandoned(opsmetrics.SourceRepairsResolver' internal/repairs/guards.go      # still 1 hit
grep -rn 'registry.Reporter' --include=*.go internal/repairs | grep -v _test | wc -l          # 0
go test ./internal/repairs/... ./internal/server/handlers/repairs/... -count=1               # ok
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile' -count=1          # ok
go test ./internal/books/ -run TestABOMetricsGatherNoDuplicateOrMissing -count=1             # ok (both source values still present)
git diff --exit-code origin/main -- internal/server/testdata/op_ids.golden                  # no output
make ci                                                                                     # exit 0
```

## STOP conditions

- `tasks.Fixer` or `taskstest.Conformance` is missing at your base.
- A fixer's plan or apply result differs from HEAD's on the parity fixtures and you cannot make it equal without
  changing the fixer's own logic.
- `TestABOMetricsGatherNoDuplicateOrMissing` shows `ops_abandoned_calls{source="repairs_resolver"}` missing or
  doubled.
- The owner-grant path cannot be expressed without a new HTTP route.

## Out of scope

The `ops_abandoned_calls` owner change (PC-13-13); any parallelism change; the maintenance plugin's other
definitions (T6); `internal/opsmetrics`.

## Rollback

Revert the PR (binary swap). Fixer-kind runs are unknown to the previous build and are dropped by its resume sweep
(09 §9.2.1 row 5); plans made on this build must be re-made after a rollback. Release-note line for the
hand-off: "Repairs plans made on this release are not visible after a rollback; plan again."

## Repo rules

- File headers on every changed file; none on the changelog fragment.
- Conventional commits ending with `Stage-Release: S5 2` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- Every commit builds; `gh pr merge <n> --rebase` after the required checks; never squash, never `--admin`.
- CLAUDE.md concurrency: the fixers keep HEAD's worker counts 1:1.

## Planner notes

- Files missing from the inventory row (`internal/repairs/` only): `internal/plugins/maintenance/plugin.go`,
  `internal/plugins/maintenance/repairs_ops.go`, the fixer files under `internal/plugins/maintenance/` whose
  signatures name `registry.Reporter`, `internal/server/handlers/repairs/handler.go`,
  `internal/server/wire_repairs_routes.go` and `internal/opscatalog/tombstones.go`. PC-13-13 also edits
  `internal/plugins/maintenance/retired_versions_fixer.go` (ordered after this row, so no same-step collision).
- The row's notes say the repairs source keeps calling `RegisterAbandoned` "until PC-13-12"; the owner change is in
  PC-13-13 (inventory and 05-metrics §13). This brief uses PC-13-13.
- `retired_versions_fixer.go` holds a repairs fixer (converted here), not a v2 definition, so 05-tasks §5T.8.1's
  "the T6 sub-batch PR that converts `retired_versions_fixer.go`" names no T6 conversion at HEAD; PC-13-13 still
  owns the metric move, which keeps "exactly one PR".
- Fixer count at HEAD is 25 (spec: 24, informative). Kind naming `books.repairs.<FixerID>` is coined here because
  `dedup.series-prune` is both a fixer ID and a T7 op ID; the spec fixes no fixer kind names.
- Tier: owner grants, fingerprint re-plan and stand-down semantics across 25 fixers suggest T3 Opus/high.
