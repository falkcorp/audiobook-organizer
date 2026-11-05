<!-- file: docs/agent-tasks/platform-core/F5-05-other-plugins-definitions-become-kinds.md -->
<!-- version: 1.1.0 -->
<!-- guid: e41a9e17-4a2d-46f5-bee3-76a82f236e8e -->
<!-- last-edited: 2026-11-05 -->

# F5-05 — Conversion group T5, the other plugins: acoustid 8, metafetch 3, itunes 1 and Deluge 4 definitions become books kinds, dark

| Field | Value |
|---|---|
| ID | F5-05 |
| Release / flip | R2, tasks flip F5 (dark conversion PR) |
| Wave | W29 |
| Tier | **T3** (Opus / high): 16 definitions across four plugins, each shared body rerouted through its shell (05-tasks conversion rule 3) |
| Depends on | F2-05 (transitively F2-01..F2-04), F2-16 (`Registrar.Tasks`, `coretest`'s task cells), F2-17 (`tasklint` and this row's exemption entry file) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-05-plugin-kinds
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` in the worktree (CLAUDE.md: it breaks the build). `core/` is a nested Go module
(module path `github.com/falkcorp/mediacore`); the root module requires it, so root packages import
`github.com/falkcorp/mediacore/tasks` directly. Run core's own tests from inside `core/`.

## Goal

Give each of the **16** v2 definitions of the acoustid, metafetch, iTunes and Deluge plugins a native
`core/tasks` definition (kind `books.<HEAD ID>`) that lands **dark** (05-tasks §5T.8.2, T5 row; gate "the native
shells do not import `pkg/plugin/sdk` (the HEAD shells keep it until F5)"). HEAD's
definitions keep running until the tasks flip F5-18 registers the native kinds.

## Spec to read first

1. 05-tasks §5T.8.2 (T5 row; the three body changes; conversion rules 1–4; kind-rename map rule 4), §5T.8.4
   (`tasklint` and the dark-conversion ratchet), §5T.3.1–§5T.3.7.
2. 05-download §10 (the Deluge rows: what the four Deluge definitions become **after** the integrations flip F7).
3. 05-rbac §6. CLAUDE.md "Concurrency" (the acoustid backfill's `Concurrency` history is in CLAUDE.md itself).

## The definitions (at `f7ac5b11e`; regenerate at your base)

**`internal/plugins/acoustid`** (8)

| File | HEAD def IDs to convert |
|---|---|
| `internal/plugins/acoustid/backfill.go` | `acoustid.backfill` |
| `internal/plugins/acoustid/duration_backfill.go` | `acoustid.fingerprint-duration-repair` |
| `internal/plugins/acoustid/fingerprint_rescan.go` | `acoustid.fingerprint-rescan` |
| `internal/plugins/acoustid/lsh_backfill.go` | `acoustid.lsh-backfill` |
| `internal/plugins/acoustid/online_lookup.go` | `acoustid.lookup-online` |
| `internal/plugins/acoustid/reset_all.go` | `acoustid.reset-all` |
| `internal/plugins/acoustid/scan.go` | `acoustid.scan` |
| `internal/plugins/acoustid/window_backfill.go` | `acoustid.window-backfill` |

**`internal/plugins/metafetch`** (3)

| File | HEAD def IDs to convert |
|---|---|
| `internal/plugins/metafetch/asin_backfill.go` | `metafetch.asin-backfill` |
| `internal/plugins/metafetch/calibrate_scoring.go` | `metafetch.calibrate-scoring` |
| `internal/plugins/metafetch/pcache_sweep.go` | `metafetch.pcache-sweep` |

**`internal/plugins/itunes`** (1)

| File | HEAD def IDs to convert |
|---|---|
| `internal/plugins/itunes/position_sync.go` | `itunes.position-sync` |

**`internal/plugins/deluge`** (4)

| File | HEAD def IDs to convert |
|---|---|
| `internal/plugins/deluge/centralization.go` | `deluge.centralize` |
| `internal/plugins/deluge/link_backfill.go` | `deluge.link-backfill` |
| `internal/plugins/deluge/path_update.go` | `deluge.path-update` |
| `internal/plugins/deluge/protected_paths.go` | `deluge.protected-paths-sync` |

**Total: 16.** `internal/plugins/itunes/import.go` is an excluded stub (`plugin.go` says its real
implementation is `internal/server`'s `itunes.import`, which is F5-13's); it is not converted.

## Base state: re-verify before editing

```bash
# F2 outputs this PR builds on. Each must hit at your base; 0 hits on any line = STOP and report.
test -f core/go.mod && grep -c '^module github.com/falkcorp/mediacore$' core/go.mod      # 1
grep -c 'github.com/falkcorp/mediacore' go.mod                                            # >=1 (root requires core)
grep -rn 'func Task\[P, R any\]' core/tasks/*.go | grep -v _test | wc -l                  # 1 (F2-01)
grep -rn 'func Batch\[P, T, R any\]\|func Fixer\[P, C any\]' core/tasks/*.go | grep -v _test | wc -l   # 2
grep -rn 'func Fixed(n int, reason string) Parallelism\|func Sequential(reason string) Parallelism' core/tasks/*.go | wc -l   # 2
grep -rn 'func RunItems\[T any\](rc \*RC' core/tasks/*.go | wc -l                         # 1
grep -rn 'func (rc \*RC) Mode() Mode\|func (rc \*RC) Checkpoint(v any) error\|func (rc \*RC) Restore(v any) (bool, error)' core/tasks/*.go | wc -l   # 3
grep -rn 'func Conformance(' core/tasks/taskstest/*.go | wc -l                             # 1 (F2-01)
test -f core/coretest/tasks.go && echo present                                            # present (F2-16: coretest's task cells)
grep -c 'Tasks(' core/module/registrar.go                                                 # >=1 (F2-16: Registrar.Tasks)
test -d core/archtest/tasklint && test -f internal/books/tasklint_test.go && echo tasklint   # tasklint (F2-17)
test -f internal/books/testdata/tasklint_exempt.d/F5-05.txt && echo entry                 # entry (F2-17 created it), else STOP
grep -v '^#' internal/books/testdata/tasklint_exempt.d/F5-05.txt | grep -v '^$' | grep -v '^internal/plugins/\(acoustid\|metafetch\|itunes\|deluge\)/' | wc -l   # 0 (the entry names only files of the four plugin packages)
```

A grep that misses only because of formatting (a wrapped signature, an extra space) is not a STOP: grep the bare
symbol name (`func Task[`, `func Conformance(`) first, and STOP only if the symbol itself is absent.

```bash
# HEAD's engine is still the production engine (dark rule R1): each must still hit
grep -rn 'func RunItems\[T any\](ctx context.Context, r Reporter' internal/operations/registry/run_items.go | wc -l   # 1
grep -n 'Empty Writes = declares nothing' internal/operations/registry/types.go | wc -l                               # 1
grep -n 'ResumeRestart *ResumePolicy\|ResumeRestart  *// reload' internal/operations/registry/types.go | wc -l        # >=1
# No converted kind registered in production yet (must stay 0 after your PR too)
git grep -n 'mediacore/tasks"' -- main.go internal/opscatalog/ | wc -l                                         # 0
```

```bash
for d in acoustid metafetch itunes deluge; do grep -rn 'OperationDef{' internal/plugins/$d/*.go | grep -v _test.go | grep -vc plugin.go; done   # 8, 3, 2, 4 (itunes 2 includes the excluded stub)
grep -n 'Triggers:' internal/plugins/deluge/path_update.go | wc -l                 # 1 (book.relocated; not carried, rule R5)
grep -n 'Schedule:' internal/plugins/deluge/protected_paths.go | wc -l             # 1 (the */30 cron that never runs; not carried)
grep -n 'Concurrency:' internal/plugins/acoustid/backfill.go | wc -l               # >=1 (CLAUDE.md: copy the field, rule R4)
grep -rln 'mediacore/tasks"' internal/plugins/acoustid internal/plugins/metafetch internal/plugins/itunes internal/plugins/deluge | wc -l   # 0
```

## The conversion rules (every definition this PR converts)

05-tasks §5T.8.2 fixes the three body changes (`rc.Mode()`, `rc.Checkpoint`, the returned result), conversion
rules 1–4 (here R7, R2, R10 and R9) and the naming rule (R3); R4 is its parallelism rule. R5, R6 and R8 are this
plan's mechanical mapping of HEAD's fields onto the spec's `tasks.Common` (05-tasks §5T.3.1), with the source of
each line. Apply every rule the same way to every definition; never improvise a field.

**R1, dark (05-tasks §5T.8.2).** The converted definition compiles, is exercised by
`taskstest.Conformance`, and is **not registered anywhere in production**: HEAD's v2 definition keeps its ID,
its registration and its behaviour, and the production build still runs HEAD's engine. Nothing in `main.go`,
`internal/server` wiring, `internal/opscatalog` or any `Register`/`RegisterOp` call changes in this PR. The
tasks flip F5-18 registers the native kinds and deletes HEAD's definitions.

**R2, shared core, two shells (05-tasks §5T.8.2 conversion rule 2; helper names coined here).** One body serves
both engines during the dark window, so the parity suites test the code production runs and HEAD's behaviour is
the oracle. Rule R10 adds two fields to the env below (`Writer`, `Store`) and is part of this rule:

1. Move the body of HEAD's `Run` func into an unexported function in the **same file**, named
   `<runFuncName>Core` (name coined here), that takes `ctx context.Context`, the decoded params struct, and a
   per-definition env struct declared in the same file (`<runFuncName>Env`, name coined here) holding only
   what the body needs from its engine: `Live bool`; `Log func(level slog.Level, msg string)`;
   `Progress func(done, total int, msg string)`; and, only when the body uses them, `SaveCheckpoint func(v any) error`,
   `LoadCheckpoint func(v any) (bool, error)` and `Items func(n int, fn func(ctx context.Context, i int) error) error`
   (the in-body loop). It **returns** the result value instead of writing it (`(Result, error)`).
2. HEAD's `Run` becomes the **v2 shell**: decode params exactly as before (including the `dry_run`/`dryRun`
   reading through `opmode`), fill the env from the `Reporter` (`Live` = not dry-run; `Items` = HEAD's
   `registry.RunItems` call with its **unchanged** `RunItemsOptions`), call the core, then write the result
   exactly as before (`registry.ReporterSetResult` / `maintenance.SetResult` / the summary row). Every
   existing test of the definition must still pass **unchanged**: they are the proof the shell kept HEAD's
   behaviour.
3. The **native shell** is a `tasks.Task[P, R]` (or `tasks.Batch` where HEAD's def is already a per-item
   runner and you keep the item function verbatim) whose `Run(rc, p)` fills the env from `rc`:
   `Live` = `rc.Mode() == tasks.ModeLive`; `Log`/`Progress` = `rc.Log()`/`rc.Progress()`;
   `SaveCheckpoint`/`LoadCheckpoint` = `rc.Checkpoint`/`rc.Restore` **with HEAD's checkpoint struct type
   unchanged**, so `Restore` decodes the bytes HEAD wrote (05-tasks §5T.3.6 "An imported run carries HEAD's
   checkpoint bytes"; `TestCutover_CheckpointsResume` in the flip depends on it); `Items` = `tasks.RunItems`
   with the parallelism of rule R4. It returns the core's result. It never reads a `dry_run` param
   (05-tasks §5T.3.7 mode contract item 3: params structs carry no `DryRun` field on the native side; if
   HEAD's params struct has one, the native shell uses a copy of the struct without it, declared in the
   same file).
4. Do **not** rename, re-sign or delete any existing function, type, constant or exported symbol: other
   files (and other same-wave PRs) call them. Only add.

**R3, kind name (05-tasks §5T.8.2 "kind-rename map", rule 4).** `tasks.Kind("books." + <HEAD def ID>)`,
the ID verbatim (dots and hyphens kept; `tasks.ParseKind` must accept it). Declare it as an unexported
constant next to HEAD's ID constant. A HEAD `FormerIDs` list is **not** carried (there is no alias field,
05-tasks §5T.3.1; M4's map consumes aliases, rule 3).

**R4, parallelism 1:1 (05-tasks §5T.8.2).** Every in-body loop keeps HEAD's worker count: HEAD
`RunItemsOptions.Concurrency` = n > 0 → `tasks.Fixed(n, "<reason>")`; zero or omitted → `tasks.Sequential("<reason>")`
(zero is sequential at HEAD, `internal/operations/registry/run_items.go` clamps it to 1). `Concurrency:
runtime.NumCPU()` → `tasks.CPU()`. The reason is one sentence copied from HEAD's comment on that call, or
"HEAD declared no concurrency (sequential)". Any change of a definition's parallelism is a **separate PR**
(05-tasks §5T.8.2; CLAUDE.md), never this one.

**R5, field map** (HEAD `registry.OperationDef`, `internal/operations/registry/types.go`, → `tasks.Common`):

| HEAD field | Native | Source |
|---|---|---|
| `DisplayName`, `Description` | `Title`, `Help` (verbatim) | 05-tasks §5T.3.1 |
| `ResumePolicy` `ResumeRestart` / `ResumeRequeue` / `ResumeDrop` / `ResumeAsk` | `tasks.ResumeContinue` / `tasks.ResumeFromZero` / `tasks.ResumeDrop` / `tasks.ResumeAsk` | 05-tasks §5T.3.6 |
| `DefaultPriority` `PriorityLow` / `PriorityNormal` / `PriorityHigh` (0/1/2) | `tasks.PriorityLow` / `PriorityNormal` / `PriorityHigh` | 05-tasks §5T.3.1; §5T.8.3 ("Priority 0/1/2 → Low/Normal/High") |
| `Timeout`, `ProgressTimeout` | `Timeout`, `ProgressTimeout` (0 stays 0: the shape default) | 05-tasks §5T.3.6 |
| `ConcurrencyKey` | `""` → leave zero (`Key(Kind)`); `v` → `tasks.Key("books." + v)` (the same HEAD key maps to the same native key in every PR) | 05-tasks §5T.3.4 (keys are `<module>.<name>`) |
| `DependsOn` | `NotWhile`, each ID mapped by R3 (or by 05-tasks §5T.8.2's core table for the six T4 IDs) | 05-tasks §5T.3.1 ("v2 DependsOn") |
| `DedupeQueuedRuns`, `DedupeSameActorOnly`, `MergeQueuedParams` | `Dedupe{AnyParams, SameActorOnly, Merge}` | 05-tasks §5T.3.1 `DedupePolicy` |
| `Batchable`, `BatchWindow`, `BatchMaxWait` | `Debounce` | 05-tasks §5T.3.1 |
| `NotifyLevel` | `Notify` | 05-tasks §5T.3.1 |
| `NoWarmupWait` | `false` → `Ready` = the readiness component `books.memdb`; `true` → `Ready` nil | 05-tasks §5T.3.1 `Ready`; `TestDispatch_WaitsForReadySignal` (COR4-8) |
| `Permissions` | `Perms` by rule R6 | 05-tasks §5T.3.7 |
| `Writes`, `Reads`, `Preview`, `Capabilities` | `Effects` by rule R7 | 05-tasks §5T.3.4 |
| `Schedule` (a v2 cron string) | **none**: never evaluated at HEAD (05-tasks §5T.2); dropped and listed for the release note (05-tasks §5T.3.8; 14 Q-C). Schedules are F5-14's | 05-tasks §5T.3.8 |
| `Triggers` | none in this PR (the one trigger, `book.relocated`, becomes an outbox topic at F6) | 05-tasks §5T.2; 05-events §5E.13 |
| `Plugin`, `FormerIDs`, `Cancellable`, `ParamsSchema`, `SummarizeQueued`, `Liveness`, `MinCheckpointInterval`, `ProgressFlushInterval`, `Synchronous`, `Isolate`, `RunsAs` | not carried; list each non-zero value per kind in the PR description under "Dropped HEAD fields". `Cancellable` is never read at HEAD (05-tasks §5T.2); `Isolate: true` or `RunsAs: ActorSystem` has 0 hits at `f7ac5b11e`: a hit at your base is a STOP | 05-tasks §5T.2, §5T.3.6 |
| `Requires` | the `Requires` field `core/tasks` landed (grep `Requires` in `core/tasks/*.go`); absent → STOP for that one definition and report | 05-tasks §5T.4 (`waiting_deps`) |
| `Phases` | `rc.Phase(name)` around the same code | 05-tasks §5T.3.3 |

**R6, permissions (05-tasks §5T.3.7, "start" row; 05-rbac §6 atom table).** `Perms.Start` is the native
requirement the atom table maps HEAD's `Permissions` to (the "Route requirement" column; all of HEAD's atoms
are required, so combine them with `rbac.AllOf`); HEAD's empty list means HEAD's default `settings.manage`.
A definition whose atoms map only to view-class scopes (`library.view`) converts to
`rbac.AllOf(books:scan, books:view)`, which keeps the scan floor (`TestStart_ConvertedViewDefKeepsScanFloor`).
Read the table from the books atom-table package the identity flip landed (`grep -rln 'TestAtomTable_Golden'
internal/` at your base; 0 hits = STOP). Never set `StartableByViewers` in a conversion. `Perms.Approve`
is set only where `Approval` is (Fixers, F5-02).

**R7, effects (05-tasks §5T.8.2 conversion rule 1; §5T.3.4).** HEAD's write set is table-level and optional
("Empty Writes = declares nothing = the gate skips this op", `internal/operations/registry/types.go`); core
requires `Effects` (05-tasks §5T.3.1). Map with `res(s) = tasks.Res("books", s)` for HEAD's resource strings
(`books`, `book_files`, `authors`, `series`, `review_items`, `embeddings`, `operations`, `catalog`,
`fingerprint_index`):

| HEAD | Native `Effects` | PR description list |
|---|---|---|
| `Writes` non-empty | `tasks.Writes(res(w)...)`, plus `.AlsoReads(res(r)...)` for `Reads` | — |
| `Writes` empty, the body writes | `tasks.Writes(res(x)...)` for every books resource the shared core **and every function it calls** writes, found by reading them (05-tasks conversion rule 1). Rule R10 makes this a reading of the core's `kindWriter` calls: `ModifyBook` → `books`; `ModifyBookFile`, `RepointBookFile` → `book_files`; `ModifyBookAuthors` → `authors`; any other method → the resource of the HEAD store method it wraps | "Writes found": per definition, each resource and the call that writes it |
| `Writes` empty, the body writes nothing | `tasks.ReadOnly()`, with no declared reads | "Declared ReadOnly" |

A definition HEAD runs only in an explicit live mode (`Writes` empty, `Preview` zero, a `library.write`/
`files.write`/`db.migrate` capability; `internal/operations/registry/preview.go`, `PreviewSpec` comment) keeps that
behaviour (rule 3): its native shell returns an error when `rc.Mode() != tasks.ModeLive` ("<kind>: preview not
supported; start with mode live"). List those under "Live-only writer".

**R9, the schedule hook (05-tasks §5T.8.2 conversion rule 4; list-function names coined here).** `tasks.Definition` is opaque and `Common.Schedule` is set
at construction (05-tasks §5T.3.1, §5T.3.8), while the 38 schedules are declared later, in
`internal/books/schedules.go` (F5-14), and attached by the flip. So every list function this PR adds takes
its owner-declared dependencies (rule R10) and `sched func(tasks.Kind) *tasks.Schedule` and sets `Common.Schedule: sched(<kind>)` on every kind it builds
(a nil `sched` means no schedule; tests pass nil). No conversion declares a schedule itself.

**R8, tests (05-tasks §5T.8.2 "Gate" column; §5T.9 last row).** One new test file per PR (name in the
Files table) that builds every native kind of the PR and runs `taskstest.Conformance(t, defs...)` over
them, plus one table test that asserts each kind's name, `Title`, `Resume`, parallelism `String()` and
`Effects` against the HEAD definition it came from (built from HEAD's `xxxDef()` in the same test, so a
drift fails). HEAD's existing tests stay unchanged and green.

**Plugin specifics.**

- **Deluge (05-download §10).** At F5 the four Deluge definitions keep calling HEAD's `internal/deluge`
  client exactly as today: the shared core is HEAD's body. Their move onto core's download `Pool`
  (`Pool.Relocate`, `Pool.Items`, reached as `module.Deps.Downloads`) belongs to the integrations flip F7,
  when that contract is live; do not import `core/integrations/download` here (F7-04 builds the adapter dark and
  F7-09 switches HEAD's client to it). Rule R10's `kindWriter` therefore wraps HEAD's store writes only; the
  Deluge client calls are not store writes and stay in the shared core unchanged. Kind names follow 05-tasks §5T.8.2 rule 4: `books.deluge.centralize`,
  `books.deluge.link-backfill`, `books.deluge.path-update`, `books.deluge.protected-paths-sync`.
- **`deluge.path-update`'s trigger** (`book.relocated`, never published at HEAD, 05-tasks §5T.2) is not
  carried; the outbox topic and its consumer arrive with the events flip F6 (05-events §5E.13).
- **`deluge.protected-paths-sync`'s `*/30` cron** is not carried (rule R5 `Schedule` row; 14 Q-C). Whether it
  gets a real schedule is F5-14's, per the owner's Q-C answer.
- **acoustid** keeps HEAD's `Concurrency` values verbatim (rule R4; the `Label` closure rules of CLAUDE.md
  apply to `tasks.RunItems` too: anything `Label` reads is shared state).
- **The T5 gate (05-tasks §5T.8.2: "the native shells do not import `pkg/plugin/sdk`").** Go imports are per
  file, so each package's native shells live in its `kinds.go`, not beside their HEAD definitions (this replaces
  rule R2 item 3's "same file" for this PR), and no `kinds.go` imports `pkg/plugin/sdk`. The shared cores, their
  env structs and the HEAD v2 shells stay in the definition files, which keep their `sdk` import until F5-18. Each
  package declares its own `kindWriter`, `kindStore` and `KindDeps` (rule R10) in its `kinds.go`.

**R10, writer and stores only through the shell (05-tasks §5T.8.2 conversion rule 3; §5T.8.4 "The
dark-conversion ratchet"; plan §(l) items 4 and 5).** A dark conversion keeps HEAD's behaviour, and the shared
core reaches its writer and its stores **only through what its shell hands it**. That is what lets `tasklint`'s
`writer-bypass` and `store-capture` pass over the converted files once this PR deletes its exemption entry file.
For every core this PR creates:

1. **No captured store.** The core reads no store, database handle, HEAD writer or service from a receiver field,
   a package variable or a closure it captures (`p.db`, `p.store`, `s.store`, a global store, a dependency reached
   through the receiver). Everything it reads or writes arrives through its arguments: the params, and two env
   fields added to rule R2's env struct, `Writer kindWriter` and `Store kindStore`. A helper the core calls that
   touches a store gets a new variant taking the env's `Writer`/`Store` (name coined per helper: `<helper>Via`);
   the existing helper is not re-signed (rule R2 item 4).
2. **The typed-writer interface** `kindWriter` (name coined here; plan §(l) item 4: a consumer interface declared in
   the converted package, with a fake in its tests) holds exactly the write methods this PR's cores call. Where
   HEAD's body already wrote through `pkg/ops.Writer`, copy those methods with their signatures (`ModifyBook`,
   `ModifyBookFile`, `RepointBookFile`, `ModifyBookAuthors`, `Effect`); a write HEAD's body made directly on a store
   becomes a method with the name and signature of the HEAD store method it calls. One interface per package for
   this PR; methods are only added, never re-signed.
3. **The store interface** `kindStore` (name coined here) holds exactly the **read** methods the cores call, with
   HEAD's signatures. A method that writes is never on it.
4. **The native shell** sets `env.Writer = deps.NewWriter(rc.Writer())` and `env.Store = deps.Store`, where
   `deps` is the owner-declared dependency value this PR's list function takes (rule R9; 05-tasks §5T.8.4
   `store-capture`: "through `tasks.Writer` (`rc.Writer()`) or the dependencies its owner declared for it"), with
   fields `Store kindStore` and `NewWriter func(tasks.Writer) kindWriter`. Nothing passes it in production before
   F5-18, which wires `internal/books/writer` behind `NewWriter` (plan §(l) item 4); tests pass a recording fake.
5. **HEAD's shell** sets `env.Writer` to an adapter of the same interface over HEAD's writer (name coined here:
   `head` + the interface name): each method calls exactly what HEAD's body called, in the same order with the same
   arguments (the reporter's v3 writer where HEAD used one, else the HEAD store method), and `env.Store` to the
   store HEAD's body read. HEAD's existing tests, unchanged and green, prove the adapter kept HEAD's behaviour.
6. The other body changes stay the three of 05-tasks §5T.8.2. Rule R10 changes where a write is reached from,
   never what is written or in which order.

STOP and report (never an allow directive, a `//nolint`, or a line added to any entry file) if a HEAD write cannot
be expressed as an interface method without changing what HEAD writes or its order; if `tasklint` still reports
`writer-bypass` or `store-capture` in a file this PR converts; or if it reports one in a file this row does not
own (that file is another row's; name it in the report).

**The exemption entry (05-tasks §5T.8.4; plan §(l) item 5).** F2-17 created
`internal/books/testdata/tasklint_exempt.d/F5-05.txt`, listing the books files this row converts. `tasklint`
reads the directory as one union whose ratchet only shrinks (a PR that adds a file or a line fails). This PR
deletes **that file only**, in the same PR as the conversion, and touches no other entry file: its same-wave
siblings delete their own.

## Files

| Action | Path |
|---|---|
| modify | the 16 definition files above (shared core + v2 shell, rule R2; the native shells are in `kinds.go`) |
| create | `kinds.go` and `kinds_test.go` (names coined here) in each of the four packages: the package's native shells, `kindWriter`, `kindStore`, `KindDeps{Store kindStore; NewWriter func(tasks.Writer) kindWriter}` (rule R10) and `func (p *Plugin) Kinds(deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition` (rule R9), no `pkg/plugin/sdk` import; rule R8's and rule R10's tests |
| delete | `internal/books/testdata/tasklint_exempt.d/F5-05.txt` (this row's exemption entry file; rule R10) |
| create | one `changelog.d/` fragment |

Do not modify any `plugin.go` registration, any other file under `internal/books/testdata/tasklint_exempt.d/`,
`internal/deluge`, `internal/server`, `internal/opscatalog` or `main.go`.

## TDD: tests to write first

Per package (names coined here): `Test<Pkg>Kinds_Conformance` (`taskstest.Conformance` over
`p.Kinds(fakeKindDeps(t), nil)`), `Test<Pkg>Kinds_MatchHEADDefs` (rule R8) and `Test<Pkg>Kinds_WritesThroughShell`
(for every kind whose `Effects` declare writes, one `ModeLive` run on a `taskstest` fake `RC` shows `NewWriter`
called with that run's `rc.Writer()` and every write reaching the recording `kindWriter`; rule R10), with
`<Pkg>` = `Acoustid`, `Metafetch`, `ITunes`, `Deluge`. A package whose kinds are all `ReadOnly` keeps the third
test as one case asserting that.

## Steps

1. Base checks; STOP on a mismatch.
2. Convert package by package, file by file (rule R2, then R3–R7); existing tests green after each file.
3. `kinds.go`/`kinds_test.go` per package.
4. `git rm internal/books/testdata/tasklint_exempt.d/F5-05.txt`, then run the acceptance block's `tasklint` line: it
   must pass with the four packages no longer exempt (rule R10's STOP if not).
5. PR description (`old ==> new`; the rule R5/R7 lists; the deleted entry file; `converted: 16/16`).

## Acceptance (commands and expected output)

```bash
go build ./...                                                                   # exit 0
go vet ./internal/plugins/acoustid/ ./internal/plugins/metafetch/ ./internal/plugins/itunes/ ./internal/plugins/deluge/   # exit 0
go test ./internal/plugins/acoustid/ ./internal/plugins/metafetch/ ./internal/plugins/itunes/ ./internal/plugins/deluge/ -count=1 -short   # ok x4
go test ./internal/plugins/acoustid/ ./internal/plugins/metafetch/ ./internal/plugins/itunes/ ./internal/plugins/deluge/ -run 'Kinds_' -count=1 -v | grep -c '^--- PASS'   # 12
grep -rho '"books\.\(acoustid\|metafetch\|itunes\|deluge\)\.[a-z0-9.-]*"' internal/plugins/acoustid internal/plugins/metafetch internal/plugins/itunes internal/plugins/deluge | sort -u | wc -l   # 16
grep -rln 'mediacore/integrations/download' internal/plugins/deluge | wc -l       # 0
grep -c 'pkg/plugin/sdk' internal/plugins/acoustid/kinds.go internal/plugins/metafetch/kinds.go internal/plugins/itunes/kinds.go internal/plugins/deluge/kinds.go | awk -F: '{s+=$2} END {print s}'   # 0 (the T5 gate)
test ! -e internal/books/testdata/tasklint_exempt.d/F5-05.txt && echo deleted       # deleted
git diff --name-only --diff-filter=D origin/main -- internal/books/testdata/tasklint_exempt.d/   # internal/books/testdata/tasklint_exempt.d/F5-05.txt (only)
git diff --name-only --diff-filter=AMR origin/main -- internal/books/testdata/tasklint_exempt.d/ | wc -l   # 0
go test ./internal/books/ -count=1 -run "$(grep -o '^func Test[A-Za-z0-9_]*' internal/books/tasklint_test.go | sed 's/^func //' | paste -sd'|')"   # ok (tasklint over the union; the four packages no longer exempt)
git grep -n 'mediacore/tasks"' -- main.go internal/opscatalog/ | wc -l           # 0
make ci                                                                          # exit 0
```

`make ci` runs `tasklint` (F2-17). With this row's entry file deleted, a `writer-bypass` or `store-capture` finding
in the four packages is a failure: fix it by rule R10, never with an allow directive or an entry line. A finding
in another row's file is rule R10's STOP.

## Out of scope

Registration (F5-18); schedules (`acoustid_backfill`, `acoustid_online_lookup`, `asin_backfill`: F5-14);
Deluge on core's download contract (F7); the `book.relocated` outbox topic (F6); `itunes.import` (F5-13).

## Idempotency / rollback

If the four `kinds.go` files exist and their tests pass, run the acceptance instead. Rollback = revert.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task (CLAUDE.md "Worktree Discipline").
- File headers on every created or modified file, version bumped, `last-edited` set to the day you edit
  (`.standards/instructions/file-headers.md`); keep existing `guid`s; new files get a fresh `guid`
  (`python3 -c 'import uuid;print(uuid.uuid4())'`). `changelog.d/` fragments carry **no** header.
- One `changelog.d/` fragment (`scriv create`, or a hand-written Markdown file); no hand edit of `CHANGELOG.md`.
- Conventional commit; the body ends with `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no
  release or flip trailer (09 §9.2.1: nothing records a flip), so add none.
- `gh pr create`, then `gh pr merge <n> --rebase` once every required check is green on an up-to-date
  branch; never squash, never `--admin` past a required check.
- CLAUDE.md concurrency rules: a loop over a library-scale collection with per-item work uses a bounded
  worker pool (`tasks.RunItems` / `tasks.ForEach` with an explicit `tasks.Parallelism`, or `errgroup` with
  `SetLimit`); `registry.RunItems` without `Concurrency` is sequential.
- Status report at the end: exact counts, then `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`,
  `BLOCKED: <n> — <list>` (CLAUDE.md).

Commit message: `refactor(plugins): native books kinds for acoustid, metafetch, itunes and deluge (dark)`.
