<!-- file: docs/plans/platform-core/config-cow.md -->
<!-- version: 1.0.0 -->
<!-- guid: d0df1801-7af9-42d5-a558-c433a7c7cdb8 -->
<!-- last-edited: 2026-11-21 -->

# Copy-on-write config: replacing the mutable `config.AppConfig`

Status: design + prototype (branch `agent/config-cow`). Lands after the F3-07
config-flip stack (PR #4949), as one PR rebased onto it.

## 1. Problem

`internal/config` holds a package-global struct value, `var AppConfig Config`.

- **Writers** replace it whole or field by field under `mu.Lock`: every
  settings save (`update_service.go`, `Mutate(func(c){ ... *c = candidate })`
  on main; `config.Project` on every Facade rebuild after F3-07), boot, the
  scheduler admin, the dedup calibration apply, and about 1,600 test set-ups.
- **Readers** mostly read `config.AppConfig.Field` with no lock. On r10 that is
  551 field reads and 4 whole-struct copies in non-test code outside
  `internal/config` (an AST count that excludes comments; grep's 600 includes
  them). Another 100 call `config.Snapshot()`, an RLock copy.
- **Pointer hand-offs**: 23 `&config.AppConfig` and 2 `&config.AppConfig.Tools`
  pass the live global into objects that keep reading it.

Every unlocked read that runs while a save happens is a data race. The race
detector sees it whenever a test runs a background reader during a save. Go's
memory model does not promise a torn read is harmless: a string or slice
header read during a write can pair one write's pointer with another's length.
F3-07 converts about 110 background readers to `Snapshot()` and quiesces
TestFlip so its maintenance ops do not overlap settings saves. That covers
symptoms. It does not remove the class.

Two more members of the class turned up during this design:

- **`config.AppConfig.Validate()` is a write.** On main, `Validate` normalises
  its receiver: it derives the iTunes path shims, sets `vector_backend` `"" ->
  "hnsw"`, and replaces five zero-means-default fields. On r10, `Problems()`
  does the same. Both boot call sites, `cmd/root.go` (main 522, r10 481) and
  `internal/books/boot.go` (main 100, r10 78), therefore rewrite the live
  global field by field without the lock. See section 6.
- **`Engine.Mutate` on r10 makes a shallow copy.** It does
  `after := before; fn(&after)`, so `after` shares every map and slice with
  `e.cur`. When `fn` makes an in-place map or slice edit, it writes into the
  Engine's current projection before `diff` runs, and possibly into the
  projection readers hold. The F3-07 lead has been told. Section 4.4 has the
  fix.

## 2. API

New file `internal/config/cow.go`. The prototype adds it on main with a
one-line change to `Mutate`.

```go
// Get returns the published, immutable snapshot. One atomic load. Never nil.
func Get() *Config

// Source is what a long-lived object keeps instead of a *Config.
type Source func() *Config          // config.Get is the production Source
func Fixed(c *Config) Source         // a pinned snapshot, for tests and per-run pins

// Writers. The signatures are unchanged; the bodies become copy-on-write.
func Mutate(fn func(*Config))         // r10: Facade write, else local COW publish
func MutateChecked(fn func(*Config)) error
func Project(cfg Config)             // r10: publish the Facade's projection

// Pure. Neither changes its receiver.
func (c *Config) Normalized() *Config
func (c *Config) Validate() error     // validates c.Normalized()
func NormalizeAndValidate() error     // the boot step: publish Normalized(), return Validate's error

// Kept for its ~100 callers, now defined as *Get().
func Snapshot() Config

// Test-only. internal/arch refuses them outside _test.go and internal/testutil.
func SetForTest(fn func(*Config))     // in-memory COW publish, no Facade, cannot be refused
func ReplaceForTest(c Config)         // SetForTest(func(d){ *d = c })
func CheckSealed() error              // see section 7
```

**Name.** `Get`. `Current()` was the first choice, but r10's `install.go`
already defines `Current() Facade`, and the settings routes, the Basic gate and
telemetry call it. `Get` is free on both trees, and `config.Get().RootDir`
reads as what it is. `Load` was rejected because it suggests loading from disk,
and this package has `LoadConfigFromDatabase`.

**Storage.** `var current atomic.Pointer[Config]`. `init` stores an empty
`Config`, so `Get` never returns nil, even before `InitConfig`. That matches
the old zero-valued global, and no call site needs a nil check.

**Publish order.** The writer holds `mu.Lock` (the existing lock, which still
serialises writers) and does three things in order:

1. `next := Get().Clone()`.
2. `fn(next)`.
3. `publishLocked(next.Clone())`, followed by the deferred `generation.Add(1)`.

The pointer is stored before the generation moves. A reader that sees the new
`Generation()` therefore sees the new snapshot, the same promise `Mutate`
makes today.

**Why two copies per write.** The first copy keeps `fn`'s in-place edits
(`c.M[k] = v`, `c.S[i] = v`) out of the snapshot readers hold. The second
covers `*c = cfg`: without it, the published snapshot would share `cfg`'s maps
with a caller that may keep changing them. Writes are rare (a settings save, a
boot step, a test set-up), and `Clone` is the existing reflective deep copy
(`config_deepcopy.go`), already used on every PUT.

**Behaviour changes, all deliberate:**

- If `fn` panics, nothing is published. The old `Mutate` left whatever `fn`
  had written before the panic.
- A long-running operation that takes `Get()` once sees one consistent
  generation from start to finish. Before, it saw whatever the global held at
  each read, field by field.
- `Snapshot()` is no longer a lock plus a struct copy. It copies a snapshot
  that cannot change, so the value is internally consistent by construction.

## 3. What happens to `AppConfig`

It is **removed** in the sweep PR. The compiler then finds every reader, which
the codemod (section 8) has already rewritten. No deprecated alias is kept:
an alias would be a second copy the writers must keep in step, and any read of
it would be the old race again.

The prototype keeps it, **transitionally** and on this branch only, so that
packages the codemod has not reached still compile. In that mode:

- `publishLocked` also assigns `AppConfig = *next.Clone()`. It is a separate
  deep copy, so an unconverted test that writes into `AppConfig`'s maps can
  never reach a published snapshot.
- `mutateCOW` starts from `AppConfig.Clone()` rather than `Get().Clone()`, so
  a direct `AppConfig` write in an unconverted test is folded in at the next
  write.
- `Snapshot()` still copies `AppConfig` under `RLock`, unchanged, so the
  unconverted packages' tests behave exactly as before. It becomes `*Get()`
  when `AppConfig` is deleted.

Main never runs in this mode: the sweep lands as one PR.

## 4. Writers

### 4.1 `Mutate`, `MutateChecked`, `SetForTest`

On main, `Mutate` = lock, then `mutateCOW(fn)`. On r10, `MutateChecked`
delegates to the Facade when one is installed, else to `mutateLocal`.
`mutateLocal`'s body becomes `mutateCOW`. Nothing else changes.

### 4.2 `Project` (r10)

`Project(cfg Config)` becomes `mutateLocal(func(c *Config) { *c = cfg })`
unchanged. `mutateCOW`'s second `Clone` makes the published snapshot private,
so the Engine's later shallow copies of `e.cur` can never reach what readers
hold.

### 4.3 Validate and normalisation

See section 6.

### 4.4 `Engine.Mutate` (r10, `internal/books/appconfig/write.go:38`)

Replace `after := before` with `after := *before.Clone()`. One line in F3-07's
own file. It is listed here because the COW guarantee depends on it: until it
lands, an in-place edit in an `fn` passed through the Facade writes into
`e.cur`. The F3-07 lead may take it in the stack; if not, this lane carries
it.

## 5. Readers

| Shape | Rewrite | Who |
|---|---|---|
| `config.AppConfig.X...` (field read) | `config.Get().X...` | codemod |
| `config.AppConfig` as a value | `config.Snapshot()` | codemod |
| `config.AppConfig.M()` where M is in `READONLY_METHODS` | `config.Get().M()` | codemod |
| any other method call on a config chain | refused: a pointer receiver may write | human |
| `config.Snapshot()` (~100 sites) | unchanged | n/a |

The codemod rewrites one read per site, so each read is its own atomic load. A
function that reads three fields can see two generations. That is no worse
than today, and better: today a single string read can tear. Hoisting
`cfg := config.Get()` to the top of a run is the follow-up that gives
multi-field consistency. Do it where a run's correctness depends on it (the
scanner hand-off below already does). It is not done mechanically, because
moving a read earlier changes which generation a long run sees.

`READONLY_METHODS` (the codemod, `scripts/config_cow_codemod.py`) lists
`EffectiveEmbeddingMode`, `ResolveAIParseBatch`,
`ResolveChapterConsolidationThresholdMin`, `ResolveRepairChapterMaxMin` and
`ThresholdsForModel`. `internal/config`'s `TestReadOnlyMethodsDoNotMutate`
proves each one leaves its receiver alone, and
`TestReadOnlyMethodsMatchTheCodemod` fails if the two lists drift.

### 5.1 Slices and maps stay immutable

No API can freeze a Go map. Immutability is held by three layers:

1. **Writers never share.** Every publish is a deep copy (section 2), so no
   writer holds a reference into a published snapshot.
2. **Readers are refused at review time.**
   `internal/arch/config_cow_guard_test.go` (`TestConfigCOWNoWritesThroughGet`)
   fails on each of these when the target is rooted at `config.Get()` or at a
   local assigned from it, including inside closures:
   - an assignment, increment or range-assign into it;
   - `&` of one of its fields;
   - `append`, `copy`, `clear` or `delete` on it;
   - `sort.*`, or a `slices` mutator, on it;
   - `ITunesConfig.Resolve` on it.

   It also refuses `SetForTest` and `ReplaceForTest` outside tests.
   `TestConfigCOWGuardCatchesTheWrites` is its positive control. The codemod
   refuses the same shapes when it meets them on `AppConfig`, so none is
   introduced by the rewrite.
3. **Readers are caught at run time in tests.** Under `go test`
   (`testing.Testing()`), every publish records a deep copy of what it
   published, and the next publish compares the outgoing snapshot against it.
   An in-place write panics, naming the field, for example
   `Config.SupportedExtensions[1]`. A suite that runs background readers calls
   `config.CheckSealed()` in `t.Cleanup` to attribute a violation to itself.
   This catches the aliases the syntax guard cannot follow, such as
   `exts := config.Get().SupportedExtensions; sort.Strings(exts)`. Production
   builds skip it: `testing.Testing()` is false there.

The remaining hole is a reader in production code that aliases a snapshot
slice through a variable and mutates it, on a path no test executes. That is
the same exposure every Go program with shared slices has. The seal makes it
visible the first time any test runs the path.

## 6. `Validate()` stops writing its receiver

**Today.** On main, `Config.Validate` normalises in place: it calls
`ITunes.Resolve()` (the legacy path shims), sets `vector_backend` `"" ->
"hnsw"`, and replaces zero values of `chapter_consolidation_threshold_min`,
`log_retention_days`, `min_book_size_bytes` and `min_rescan_age_hours`. On r10
the same happens through `Problems()`. Called as
`config.AppConfig.Validate()`, that is a field-by-field write to the live
global, outside `mu`.

**Fix (prototype on main).**

- `func (c *Config) Normalized() *Config`: `Clone`, then `normalizeInPlace` on
  the clone. It is pure.
- `Validate()` = `c.Normalized().validateNormalized()`. It is pure, and a zero
  that means "the default" is valid.
- `NormalizeAndValidate()`: `Mutate(func(c){ *c = *c.Normalized(); err = c.validateNormalized() })`.
  It publishes the normalised config in one swap, so a reader sees the config
  before normalisation or after it, never half of it, and it returns the
  error. The normalised config is published even when it is invalid, as the
  old in-place normalisation happened before the error was returned. Both boot
  sites call it.
- The settings write path (`update_service.go` on main) used `Validate`'s side
  effect to normalise the candidate it stores. It now does
  `candidate = candidate.Normalized()` and `priorCheck := prior.Normalized()`
  explicitly. The behaviour is the same, and the write is now visible.

**Can the two boot calls race?**

- `cmd/root.go`'s call is in `initConfig`, the `cobra.OnInitialize` hook. It
  runs on the main goroutine before any command body, so it is boot-only and
  single-goroutine.
- `internal/books/boot.go`'s call runs inside the books module's kernel
  registration (`module.go`, `bootSettings`), before the HTTP server or any
  books goroutine starts. No books goroutine is running there. That is **not
  guaranteed by construction**: the kernel has already started the modules
  registered before books, and a core goroutine that read config would race.
  None does today: core does not import `internal/config`.
- In tests, boot harnesses run inside test binaries that may already have
  background goroutines. So "boot-only" holds in production and is a
  convention in tests.

The new path is safe in every case, which is the point of the change.

**r10 port: one normalisation, not two.** r10 has since moved the four in-place
fixups into `Config.Normalize()` (`2be168931`). `Normalize` is idempotent and
in place. Both `Problems()` and `Project` call it, so every projection is
published normalised; `TestEngine_ANormalisedDefaultSurvivesASave` pins that.
This fixes the flaw where the next rebuild overwrote the boot normalisation.
On rebase:

- `Normalize()` stays the single normalisation body. The prototype's
  `normalizeInPlace` is deleted, and `Normalized()` becomes
  `n := c.Clone(); n.Normalize(); return n`. Main's two extra normalisers,
  `chapter_consolidation_threshold_min` and `log_retention_days`, go wherever
  r10 put them; they are not reintroduced.
- `Normalize` keeps its in-place signature, because `Project` calls it on its
  own private argument before the swap. A guard rule is added: `Normalize` on
  a chain rooted at `config.Get()` is refused, the same way `Resolve` is.
- `Problems()` and `Validate()` become pure. Today `Problems` starts with
  `c.Normalize()` on its receiver. It changes to `n := c.Normalized()` and
  checks `n`.
- `appconfig.validateCandidate` (`write.go:650`) relied on `cand.Problems()`
  normalising `cand` before it is stored. It calls `cand.Normalize()`
  explicitly first: `cand` is the Engine's private candidate, so normalising
  in place is fine there.
- The boot step stays `config.NormalizeAndValidate()`. With a Facade
  installed, the published projection is already normalised, so the publish
  is an idempotent swap. Without one, the publish does the work. Either way,
  nothing writes into a snapshot that readers hold. Today's
  `config.AppConfig.Validate()` on r10 still writes the live projection in
  place, even when every value written is unchanged; under `-race` that is
  still a write.

## 7. Pointer hand-offs

Measured on r10: 26 non-test sites (23 `&config.AppConfig`, 2
`&config.AppConfig.Tools`, plus the 2 boot `Validate()` calls covered in
section 6). The rule: **per-request or per-run objects take
`config.Get()` once at the start and keep that snapshot. Long-lived objects
take a `config.Source` and call it once per operation.**

| Site (r10) | Lifetime | Rework |
|---|---|---|
| `scanner/ai_parse_async.go:633`, `ai_parse_journal.go:228` | parser built per scan run | `cfg := config.Get()` hoisted; the run keeps its snapshot (**done in the prototype**) |
| `organizer/service.go:235`, `organizer/preview.go:110` | organizer per op / request | `NewOrganizer(config.Get())` |
| `metafetch/file_pipeline.go:145` (`newPathOrganizer`) | per call (`ComputeTargetPaths`) | `NewOrganizer(config.Get())` |
| `metafetch/service_apply.go:669` | per op | `NewOrganizer(config.Get())` at op start |
| `server/batch_save_op.go:78`, `server/handlers/organize.go:314` | per op | `NewOrganizer(config.Get())` |
| `server/server.go:128` (`newAIParser`), `server/diagnostics_ai_ops.go:68`, `server/handlers/ai.go:68,262,297`, `plugins/maintenance/dedup_ops.go:155` | parser per op / request (`ai_ops.go:60`, `entities_ops.go:750` build it per call) | `ai.NewOpenAIParser(config.Get(), ...)`; read the key and enabled flag off the same snapshot |
| `server/handlers/aibackends/aibackends.go:103` | per request | `cfg := config.Get()` |
| `appdirs/appdirs.go:73` (`Current()`) | per call | `FromConfig(config.Get())` |
| `maintenance/jobs/bulk_deluge_import.go:134,163` | per file inside one run | `cfg := config.Get()` at run start, passed down |
| `server/deluge_discovery.go:325` | per file inside one run | same |
| `plugins/deluge/centralization.go:416`, `path_update.go:249` | env per run | `Cfg: config.Get()` |
| `server/server.go:976` `Override("config", &config.AppConfig)` | **long-lived**: the service registry. Consumers: `quarantine.QuarantineService.cfg`, `search.IndexService.cfg`, `plugins/deluge`, `plugins/itunes` | register `config.Source(config.Get)`; `config.GetConfig(c)` returns a `Source`; each consumer keeps the Source and calls it per operation. 4 consumer packages |
| `server/server.go:1116` `tools.NewToolRegistry(&config.AppConfig.Tools)`, `server/wire_handlers.go:680` `toolshandler.New(..., &config.AppConfig.Tools)` | **long-lived** | `internal/tools` cannot import `internal/config` (config imports tools), so both take `func() *tools.ToolsConfig`, wired as `func() *tools.ToolsConfig { return &config.Get().Tools }`. The registry and handler only read it; the guard refuses any write |
| `config/config.go` (`fn(&AppConfig)`) | n/a | gone: `mutateCOW` |

19 test-file hand-offs (`organizer/*_test.go` ×10, `bulk_deluge_import_test.go`
×2, `scheduler/on_startup_dropped_test.go`'s `&config.AppConfig.Scheduled`, and
others) become `config.Get()`, or `config.Fixed(&cfg)` where the test wants its
own config. They follow mechanically once the production signatures settle.

**Semantics note.** For per-run objects this is a deliberate change. A scan,
an organize run or a Deluge import that starts before a settings save
finishes with the settings it started with. Before, it switched mid-run, one
field at a time. Long-lived services keep seeing saves, through the Source,
at their next operation.

## 8. The codemod

`scripts/config_cow_codemod.py` is a Python driver with unittest tests in
`scripts/tests/test_config_cow_codemod.py`. Its analysis half is
`scripts/config_cow_codemod/main.go`, a `go/ast` program behind the build tag
`config_cow_codemod`, so `go build ./...` and `go vet ./...` never see it.
`scripts/r2corpus` uses the same pattern. The analysis is syntax-only on
purpose: a type-checked pass needs every package to build, and the codemod
must run on a tree mid-sweep or mid-rebase.

```
scripts/config_cow_codemod.py [--write] [--test-writes] [--summary|--json] PATH...
```

- **Automatic (exact)**: field reads become `Get()`; whole values become
  `Snapshot()`; calls to `READONLY_METHODS` are rewritten. With
  `--test-writes`, in `_test.go` files only, every write statement moves into
  one `SetForTest` closure with each root replaced by `c` (or `*c`):
  `A = B`, `N += 2` and `x, A = 1, 2` all become exact. Adjacent statements
  fold into one closure. A root inside a function literal in such a statement
  becomes `Get()`, the live read the literal made whenever it ran.
  `config.AppConfig = v` becomes `ReplaceForTest(v)`.
- **Refused, printed as the manual list**: any write outside a test;
  `&config.AppConfig`; `&` of a field; a method call not in
  `READONLY_METHODS`; `append`, `copy`, `clear` or `delete`; a `sort` or
  `slices` mutator; re-slicing; a range loop assigning into the config.
- **Re-runnable**: a rewritten site no longer mentions `AppConfig`, so a
  second run proposes nothing (asserted by a test). Run it again after every
  rebase onto the moving F3-07 stack.

**Coverage, r10 tree (`cmd`, `internal`, `main.go`, including
`internal/config`):**

| | automatic | manual |
|---|---|---|
| non-test | 555 (551 reads, 4 value copies) | 27 (23 `&AppConfig`, 2 `&AppConfig.Tools`, 2 `Validate()`) |
| test | 2,493 (683 reads, 138 value copies, 1,455 write closures, 217 restores) | 19 (18 `&AppConfig`, 1 field address) |
| **total** | **3,048 sites in 548 files** | **46 sites in 33 files** |

Re-measure with `--summary` at the merge base. These counts are at r10's head
on 2026-11-21.

## 9. The ratchet (`internal/arch/appconfig_ratchet_test.go`, r10)

The ratchet counts, per package, uses of `AppConfig`, `Snapshot` and `Mutate`,
with ceilings that may only fall. End state:

- `appConfigAccessors` becomes `{"Get", "Snapshot", "Mutate"}`. `AppConfig` no
  longer exists to count. `Get` must be counted: otherwise the rewrite would
  silently zero every package's count, trip the "delete the entry" branch, and
  hand new code an uncounted global accessor, defeating the ratchet.
- The rewrite is 1:1: one `AppConfig` use becomes one `Get` or `Snapshot` use,
  and one `&config.AppConfig` becomes one `config.Get` (the Source). So every
  ceiling holds the day the sweep lands, and no entry needs changing.
- Hoisting `cfg := config.Get()` lowers counts, which the ratchet allows. Each
  PR that does it lowers its ceiling by hand, as the ratchet's own comment
  requires.
- When the map is empty, `Get`, `Snapshot` and `Mutate` go the way the
  ratchet's comment already plans for `AppConfig`. The design does not change
  that destination; it makes the road to it race-free.

## 10. Acceptance, and what this deletes

- **TestFlip quiesce.** The F3-07 lead is adding a test-fixture quiesce in the
  TestFlip fixture under `internal/books`, with a comment naming this lane. It
  is not pushed yet. This lane **deletes it**. The acceptance test is TestFlip*
  running its maintenance ops concurrently with settings saves under
  `go test -race -count=20 ./internal/books/ -run 'TestFlip'`, green, plus
  `config.CheckSealed()` clean at the end of each.
- `TestConcurrentReadersAndWriters` (`internal/config/cow_test.go`): 8 readers
  iterating slices and maps while 200 writes publish, under `-race`.
- The guard tests (section 5.1), the seal tests, and the `Normalized`/`Validate`
  purity tests.
- `go build ./...`, `go vet ./...` and `make ci` green on the rebased branch.

## 11. Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Rebase churn: the F3-07 stack is still moving and rewrites `internal/config` (r10 deletes `update_service.go` and most of `persistence.go`) | certain | Core API in a new file (`cow.go`); the codemod is re-run, never hand-merged; the main-only `update_service.go` edits disappear with the file. On r10 the only hand edits are `mutateLocal`, `Normalized` on top of `Normalize`, pure `Problems`/`Validate`, `Engine.Mutate` and `validateCandidate` |
| A reader writes through `Get()` (compiles) | medium | arch guard (syntax) and seal (runtime, tests); the codemod refuses such sites |
| Two normalisation bodies after the rebase (main's `normalizeInPlace` and r10's `Normalize`) | certain without care | section 6: `Normalized()` is defined on `Normalize()`; `normalizeInPlace` is deleted on rebase |
| Per-run pinning changes behaviour for long runs | low, intended | listed per site in section 7; long-lived services use a Source |
| Write cost: two reflective `Clone`s per write, three under `go test` | low | writes are saves, boot and test set-ups; measure `BenchmarkMutate` in the sweep PR. If a hot test loop shows it, the seal can skip unchanged generations |
| `testing` imported by non-test `internal/config` | low | precedent: `internal/books/store/pebble_store_open.go`; `testing.Testing()` registers no flags |
| Test semantics: `SetForTest` is in-memory only, like the direct assignment it replaces. A test that relied on a write reaching the Facade or store was already broken | low | tests that mean "save" call `Mutate` |
| A test reads `config.Get()` while another test's cleanup `ReplaceForTest`s | low | same exposure as today's shared global; packages that run `t.Parallel` with config writes need a per-test Source (none found in the prototype packages) |
| The prototype's transitional mode diverges (direct `AppConfig` test writes are seen by `Get()` only at the next write) | prototype only | main never runs it; the sweep is one PR |

## 12. Rollback

- **Before the sweep lands**: nothing on main changes. The prototype branch is
  disposable.
- **After**: revert the single sweep PR. Because it is one PR (core, the
  codemod output, hand-offs, guards), the revert restores `AppConfig` and
  every reader together; there is no half-state to unwind. The F3-07 quiesce
  would need restoring with it. Keep its commit SHA in the PR body.
- There is no runtime flag. A flag would mean keeping both storage models in
  step, which is the transitional mode, and that mode is exactly the
  inconsistency this lane removes.

## 13. Full sweep: cost and slicing

The codemod does 98.5% of the sites (3,048 of 3,094) in seconds. The work is
the 46 manual sites, the signature changes behind the hand-offs, compile
fixes, and running the suites.

| Slice | Files (disjoint) | Work | Worker-hours |
|---|---|---|---|
| A. core | `internal/config/*`, `internal/books/appconfig/write.go`, `internal/books/boot.go`, `cmd/root.go`, `internal/arch/*` (ratchet, guard) | delete `AppConfig`; port `cow.go`, `Normalized` and `Problems` onto r10; fix `Engine.Mutate`; `NormalizeAndValidate` at both boot sites; ratchet edit; in-package codemod | 3 |
| B. mechanical | every remaining file the codemod touches **except** those in C–E | `--write --test-writes`, gofmt, build, fix the rare compile error (e.g. a value-copy site passed where a pointer was) | 2, plus suite time |
| C. AI parser + scanner | `internal/ai/*`, `internal/server/{server.go,diagnostics_ai_ops.go,ai_ops.go,entities_ops.go}`, `internal/server/handlers/ai.go`, `handlers/aibackends/*`, `internal/plugins/maintenance/dedup_ops.go` | per-op `config.Get()`; registry `Source` (server.go:976) and tools Source (server.go:1116) stay in this slice because they are in `server.go`; quarantine, search, deluge and itunes register consumers | 3 |
| D. organizer + metafetch | `internal/organizer/*`, `internal/metafetch/{file_pipeline,service_apply}.go`, `internal/server/{batch_save_op.go,handlers/organize.go}`, organizer tests | `NewOrganizer(config.Get())`; tests to `Fixed` | 2 |
| E. deluge + tools | `internal/deluge/*`, `internal/plugins/deluge/*`, `internal/maintenance/jobs/bulk_deluge_import*.go`, `internal/server/{deluge_discovery.go,wire_handlers.go}`, `internal/tools/registry.go`, `internal/server/handlers/tools/*`, `internal/appdirs/*` | per-run pin; `func() *tools.ToolsConfig` | 2 |
| F. acceptance | `internal/books` TestFlip fixture | delete the quiesce; `-race -count=20`; `CheckSealed` in cleanup | 1, plus suite time |

About **13 worker-hours**. Run B–E in parallel worktrees off the same base,
and A first, because the others compile against it. B runs the codemod with
`--exclude` on C–E's files, so no file is in two slices. The coordinator
stacks A, then B–E, then F, into **one PR** (section 12). Wall-clock is about
5–6 hours with 4 workers, dominated by A and by the `-race` suite runs on the
remote builders.

## 14. Status of the prototype (this branch)

- Core API: `internal/config/cow.go`; `Mutate` routed through it;
  `Normalized`, pure `Validate`, `NormalizeAndValidate`; both boot call sites
  switched; `update_service.go` normalises explicitly.
- Codemod applied to `internal/reconcile`, `internal/dedup` and
  `internal/scanner`: 508 sites, all tests included. The 2 scanner hand-offs
  were reworked by hand.
- Guards: `internal/config/cow_test.go` and
  `internal/arch/config_cow_guard_test.go`.
- Base: `origin/main`. On r10 the hand edits differ (sections 4 and 6), and
  the codemod output is regenerated, not merged.
