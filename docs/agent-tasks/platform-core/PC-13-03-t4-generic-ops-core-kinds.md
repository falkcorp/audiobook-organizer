<!-- file: docs/agent-tasks/platform-core/PC-13-03-t4-generic-ops-core-kinds.md -->
<!-- version: 1.0.0 -->
<!-- guid: f43709e8-4dce-43fd-bc8f-6f13247b37e2 -->
<!-- last-edited: 2026-11-04 -->

# PC-13-03 — T4: generic ops become core.* kinds with app.Options.LegacyTaskAliases

> **SPEC-GATED.** The spec fixes the alias mechanism (`app.Options.LegacyTaskAliases`, the P7 merge,
> `ProblemLegacyTaskAlias`, `TestLegacyTaskAliases`) but not **which** generic ops become `core.*` kinds, **what
> each core kind is called**, or **which core package declares and runs each one**. Step 0 checks for that text
> and STOPs if it is missing. Do not invent kind names, owners or core bodies.

| Field | Value |
|---|---|
| ID | PC-13-03 |
| Tier | **T2 Sonnet/high** — kind renames that must not strand stored rows |
| Wave | W13 (stage S5, release `S5r3`; first PR of `S5r3`) |
| Depends on | OWNER-14 (the `S5r2` soak row) |
| Trailer | `Stage-Release: S5 3` on every commit |
| Spec | 05-tasks §5T.8.2 (the paragraph before the batch table, and row T4); 06 §6.4.7 (`ProblemLegacyTaskAlias`), the `app.Options.LegacyTaskAliases` field comment, the P7 refusal table row and the `TestLegacyTaskAliases` test row; 03 R1(c) and 04 §2.1 (h) (`TestCoreHoldsNoAppKeyNames`); 05-tasks §5T.9 (`TestCoreRun_NeverCallsModuleWriter`) |

## Gate

Merges only after OWNER-14 has merged a current `S5r2` row. This PR changes `release:` (`S5r2` → `S5r3`), so
`check_soak.py` refuses it until then and `compat-rehearsal` runs on it.

## Step 0 — the spec gate (run first; STOP if it fails)

```bash
# The T4 kind table: for each generic op, its core kind, its HEAD op ID and its core owner package.
# 05-tasks §5T.8.2 T4's gate is "owner per kind recorded in 04". At HEAD f07f516be this prints nothing.
grep -nE '`core\.[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+`[^|]*\|[^|]*`(maintenance|diagnostics)\.[a-z0-9.-]+`' docs/specs/platform-core/04-package-mapping.md
grep -n 'T4' docs/specs/platform-core/04-package-mapping.md
```

If both print nothing, **STOP** and report:

> PC-13-03 is SPEC-GATED. Missing spec text: (1) the list of T4 kinds — 05-tasks §5T.8.2 names a family
> ("activity-log cleanup/compaction family, backups prune, db optimize/census, temp/trash/reports cleanup, throttle
> reset, tombstone cleanup, retention-and-hygiene, metrics TTL sweep", "≤ 24", "the `generic` class rows of
> `raw/06` §6, each re-checked in 04") but no row list; (2) each kind's `core.*` name; (3) the core package that
> declares and runs each kind (04 records no owner per kind; the gate "owner per kind recorded in 04" is unmet);
> (4) how a core kind's body reaches data that is a books family at S5 (the activity log, ABO's trash, reports and
> tombstones) without breaking R1 and `TestCoreRun_NeverCallsModuleWriter` ("a `core.` kind's body reaches no
> module-registered write callback"); (5) for the backup prune, whether the kind may land before S8's
> `core/backup` (05-backup names `core.backup.prune` at S8).

Only continue when a spec edit supplies (1)-(5). The rest of this brief is ready for that case.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-03 -b pc-13-03-t4-core-kinds origin/main
cd ../audiobook-organizer-pc-13-03
git rebase origin/main
npm ci --prefix web
git worktree list
```

## What is specified (implement exactly this once Step 0 passes)

1. **The option** (06, `app.Options` field comment, quoted):
   ```go
   // LegacyTaskAliases are the app's former task kinds of CORE-owned kinds
   // (ABO: {"core.diagnostics.export": ["diagnostics.export"]}, filled by
   // ABO's main from books' op-ID ledger, internal/server/testdata/
   // op_ids.golden). ... At P7 the kernel adds each value to that core
   // kind's former kinds, before the op-ledger gate, so HEAD's stored runs
   // resolve; a key that is not a registered core.* kind, or a value that a
   // module registers or lists in its TaskLedger, records
   // ProblemLegacyTaskAlias (MOD6-6). nil = none. Lands at S5, in T4 ...
   LegacyTaskAliases map[tasks.Kind][]tasks.Kind
   ```
   in `core/app/options.go`.
2. **The problem kind**, appended (never renumbering) to `module.ProblemKind` in `core/module`:
   `ProblemLegacyTaskAlias // an app.Options.LegacyTaskAliases key that is not a registered core.* kind, or a value a module registers or lists in its TaskLedger (P7, MOD6-6); appended at S5 (T4)`.
3. **The P7 merge** in `core/kernel`: after every module's `Register` and before the op-ledger gate, for each key
   `k`: refuse (`ProblemLegacyTaskAlias`, boot fails closed) when `k` is not a registered kind whose module is
   `core`; refuse when any value is a kind a module registers or lists in its `Manifest.TaskLedger`; otherwise add
   the values to `k`'s former kinds, so a run HEAD stored under the former ID resolves to `k`.
4. **No core file spells an ABO op ID.** A core kind declares no `FormerKinds` outside `core.`;
   `TestCoreHoldsNoAppKeyNames` (03 R1(c), 04 §2.1 (h)) fails a non-test `core/` file that does.
5. **ABO supplies the aliases** in its main/composition root (inventory: `internal/books/module.go`,
   `internal/server/`), one entry per T4 kind, from the op-ID ledger. ABO also stops registering the v2 def of each
   T4 op (its run now resolves to the core kind).
6. **`ci/stage.txt`**: `release: S5r2` → `release: S5r3`.

## Exact files (inventory)

Modify: `core/app/options.go`, `core/kernel/`, `internal/books/module.go`, `internal/server/`, `ci/stage.txt`; plus
`core/module/` (see Planner notes) and whatever core packages the Step 0 spec text names as owners.

## Re-verify greps (HEAD `f07f516be`: `core/` does not exist; every core grep must hit at your base)

```bash
grep -n 'LegacyTaskAliases' core/app/options.go            # 0 before, 1 after
grep -rn 'ProblemLegacyTaskAlias' core/module/*.go          # 0 before, 1 after (non-test)
grep -rn 'type Options struct' core/app/options.go          # 1 (PC-04-02)
grep -n 'func TestCoreHoldsNoAppKeyNames' internal/arch/*_test.go   # 1 (PC-01-03)
grep -cx 'diagnostics.export' internal/server/testdata/op_ids.golden  # 0 at HEAD: diagnostics.export is the
                                                                      # S8 example, not a T4 kind
```

## Tests to write first (names from the spec)

- `TestLegacyTaskAliases` (06 owns it; 05-tasks §5T.9): "a run HEAD stored as `diagnostics.export` resolves to
  `core.diagnostics.export` through `app.Options.LegacyTaskAliases`; a key that is not a registered `core.*` kind,
  or a value a module registers or lists in its `TaskLedger`, fails boot with `ProblemLegacyTaskAlias`", **extended
  to one T4 kind** (05-tasks §5T.9). Until S8 there is no `core.diagnostics.export`, so the T4 kind is the positive
  case and a test-only core kind stands in for the S8 example.
- The op-ID ledger (`internal/server/op_id_aliases_test.go`): every former T4 op ID still resolves (now through
  the alias), and `op_ids.golden` is unchanged.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                   # exit 0
(cd core && go test ./app/... ./kernel/... ./module/... -run 'TestLegacyTaskAliases' -count=1)   # ok
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile' -count=1                # ok
go test ./internal/arch/ -run TestCoreHoldsNoAppKeyNames -count=1                                 # ok
git diff --exit-code origin/main -- internal/server/testdata/op_ids.golden                       # no output
grep -n '^release:' ci/stage.txt                                                                  # release: S5r3
make ci && make -C core ci                                                                        # both exit 0
```

## Out of scope

The S8 `core.diagnostics.export` alias (05-health, S8); any parallelism or schedule change; books-bound ops of the
`generic` class (05-tasks names `opchange-book-index-rebuild` as one), which stay `books.*` kinds in T6.

## Rollback

Revert (binary swap). Runs stored under a former ID are still stored under it (the alias only resolves reads), so
the previous build resumes them.

## Repo rules

File headers; conventional commits ending with `Stage-Release: S5 3` and
`Co-Authored-By: Claude <noreply@anthropic.com>`; every commit builds; `make ci` and `make -C core ci`; required
checks including `core-ci` and `compat-rehearsal`; `gh pr merge <n> --rebase`, never squash, never `--admin`.

## Planner notes

- SPEC-GATED: 05-tasks §5T.8.2 T4 and 04 give no T4 kind list, no core kind names and no owner per kind (the
  batch's own gate, "owner per kind recorded in 04", is unmet at HEAD), and give no way for a core kind to clean
  books-owned data without breaking `TestCoreRun_NeverCallsModuleWriter`. Suggest splitting the row: PC-13-03a,
  the mechanism (items 1-4 and `TestLegacyTaskAliases` with a test-only core kind), briefable now; PC-13-03b, the
  conversions, after a spec edit.
- This row is also the PR that sets `release: S5r3`, and PC-13-04 to PC-13-06 depend on it, so while it is gated no
  T5 PR can merge. If the split above is not taken, move the `release: S5r3` change and the T5 rows' dependency to
  the mechanism PR (or to PC-13-04).
- PC-13-14 may need this row's mechanism: 05-tasks §5T.3.8 runs the maintenance window as the core kind
  `core.window.<name>`, whose HEAD op ID `maintenance.window` can only resolve through `LegacyTaskAliases`.
- `ProblemLegacyTaskAlias` belongs to `module.ProblemKind` (06 §6.4.7), so `core/module/` is missing from the
  row's files.
- The `generic` rows of `raw/06` §6 overlap other batches: `repairs.plan`/`repairs.apply` (T3) and four
  `internal/maintenance/jobs` jobs (T2, PC-13-01, as `books.maintenance.*`). A T4 conversion of a T2 job is a
  second rename of the same kind (rides `FormerKinds`/the alias).
