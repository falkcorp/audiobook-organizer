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
func CheckSealed() error              // see section 5.1
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

At `2be168931`, `Project(cfg Config)` is `cfg.Normalize()` followed by
`mutateLocal(func(c *Config) { *c = cfg })`. It stays as it is: `cfg` is
`Project`'s own argument, so normalising it in place writes nothing a reader
holds. `mutateCOW`'s second `Clone` makes the published snapshot private, so
the Engine's later shallow copies of `e.cur` can never reach what readers
hold.

### 4.3 Validate and normalisation

See section 6.

### 4.4 `Engine.Mutate` (r10, `internal/books/appconfig/write.go:38`)

**Done upstream.** r10 `795b798e5` ("Mutate applies its fn to a deep copy")
makes `Engine.Mutate` copy with `Config.Clone`, pinned by
`TestEngine_MutateInPlaceWriteIsSavedAndLeavesSnapshotsAlone` under `-race`.
The COW guarantee depended on it: with the shallow copy, an in-place edit in
an `fn` passed through the Facade wrote into `e.cur`. This lane has nothing
left to do here.

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
  `n := c.Clone(); n.Normalize(); return n`.
- **r10 gap (for the F3-07 lead).** At `2be168931`, `Normalize` covers
  `ITunes.Resolve`, `vector_backend`, `min_book_size_bytes` and
  `min_rescan_age_hours`. `chapter_consolidation_threshold_min` and
  `log_retention_days` are still normalised inside `Problems()`
  (`normalizeChapterConsolidationThreshold` and `normalizeLogRetentionDays`,
  at `config.go:2166` and `:2169`), and `Project` calls `Normalize`, not
  `Problems`. So after a save, a stored 0 in either field is published as 0.
  Readers go through the `Resolve*` helpers, so behaviour holds, but `GET
  /config` shows the 0 that the comment at `:2160` says it must never show.
  Moving both calls into `Normalize` closes the gap and leaves one body. This
  design assumes that move.
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
- **The boot step must not write through the Facade.** On r10,
  `books.Register` binds the Facade (`installAppConfig`, `module.go:361`)
  *before* `bootSettings` (`:376`), so `internal/books/boot.go`'s step runs
  with a Facade installed. There, `Mutate` routes to `Engine.Mutate`, which
  persists the diff as the internal actor. A `NormalizeAndValidate` built on
  `Mutate` would therefore make boot a stored write whenever the projection
  is not normalised (today: the two fields in the gap above), or a logged
  refusal. So on r10 it branches:

  ```go
  func NormalizeAndValidate() error {
      if Current() != nil { // Project already published a normalised projection
          return Get().Validate()
      }
      var err error
      mutateLocal(func(c *Config) { *c = *c.Normalized(); err = c.validateNormalized() })
      return err
  }
  ```

  `cmd/root.go`'s call runs in `cobra.OnInitialize`, before any Facade
  exists, and takes the local branch. The books boot step takes the Facade
  branch and only reads. Neither writes into a snapshot that readers hold.
  Today's `config.AppConfig.Validate()` on r10 still writes the live
  projection in place, even when every value written is unchanged; under
  `-race` that is still a write.

## 7. Pointer hand-offs

Measured at r10 `2be168931`: 24 non-test sites. These are 20
`&config.AppConfig`, 2 `&config.AppConfig.Tools`, and the 2 boot `Validate()`
calls covered in section 6. Since `344e033e2`, r10 itself removed the
`appdirs.go:73` and `plugins/maintenance/dedup_ops.go:155` hand-offs. The rule: **per-request or per-run objects take
`config.Get()` once at the start and keep that snapshot. Long-lived objects
take a `config.Source` and call it once per operation.**

| Site (r10) | Lifetime | Rework |
|---|---|---|
| `scanner/ai_parse_async.go:633`, `ai_parse_journal.go:228` | parser built per scan run | `cfg := config.Get()` hoisted; the run keeps its snapshot (**done in the prototype**) |
| `organizer/service.go:235`, `organizer/preview.go:110` | organizer per op / request | `NewOrganizer(config.Get())` |
| `metafetch/file_pipeline.go:145` (`newPathOrganizer`) | per call (`ComputeTargetPaths`) | `NewOrganizer(config.Get())` |
| `metafetch/service_apply.go:669` | per op | `NewOrganizer(config.Get())` at op start |
| `server/batch_save_op.go:78`, `server/handlers/organize.go:314` | per op | `NewOrganizer(config.Get())` |
| `server/server.go:128` (`newAIParser`), `server/diagnostics_ai_ops.go:68`, `server/handlers/ai.go:68,262,297` | parser per op / request (`ai_ops.go:60`, `entities_ops.go:750` build it per call) | `ai.NewOpenAIParser(config.Get(), ...)`; read the key and enabled flag off the same snapshot |
| `server/handlers/aibackends/aibackends.go:103` | per request | `cfg := config.Get()` |
| `maintenance/jobs/bulk_deluge_import.go:134,163` | per file inside one run | `cfg := config.Get()` at run start, passed down |
| `server/deluge_discovery.go:325` | per file inside one run | same |
| `plugins/deluge/centralization.go:416`, `path_update.go:249` | env per run | `Cfg: config.Get()` |
| `server/server.go:976` `Override("config", &config.AppConfig)` | **long-lived**: the service registry. `config.GetConfig(c)` has 8 callers: `quarantine/register.go`, `activity/register.go`, `plugins/itunes/register.go`, `plugins/deluge/register.go`, and `server/registry_wire.go` ×4. Most read a few fields at Build time. `quarantine.QuarantineService`, `search`'s `cfg` field and the deluge importer adapter keep the pointer | register `config.Source(config.Get)`; `GetConfig(c)` returns a `Source`. A Build-time reader calls it once; a holder keeps the Source and calls it per operation |
| `server/server.go:1116` `tools.NewToolRegistry(&config.AppConfig.Tools)`, `server/wire_handlers.go:680` `toolshandler.New(..., &config.AppConfig.Tools)` | **long-lived** | `internal/tools` cannot import `internal/config` (config imports tools), so both take `func() *tools.ToolsConfig`, wired as `func() *tools.ToolsConfig { t := config.Get().Tools; return &t }`. That returns a pointer to a copy of the struct. `&config.Get().Tools` would compile, but the guard refuses it, rightly: it is an address into the snapshot. The copy is shallow, and the registry and handler only read it |
| `config/config.go` (`fn(&AppConfig)`) | n/a | gone: `mutateCOW` |

19 test-file hand-offs (`organizer/*_test.go` ×12, `bulk_deluge_import_test.go`
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
  `config.AppConfig = v` becomes `ReplaceForTest(v)`. The closure parameter
  is `c` unless the statement already uses that name (then `cfg`, `cc`,
  and so on).
- **A right-hand side that calls something is hoisted.** The closure runs
  under the config write lock. A callee that reaches `Mutate`, `SetForTest`
  or a lock-taking getter (`Snapshot` and the RLock getters, on this branch)
  would deadlock there, and neither vet nor the build can see that. So
  `config.AppConfig.RootDir = t.TempDir()` becomes
  `cowV1 := t.TempDir()` followed by
  `config.SetForTest(func(c *config.Config) { c.RootDir = cowV1 })`. The call
  still runs unlocked, as before. Temporaries are named to be unique in the
  file. A call on the left-hand side, or a statement that cannot be split
  (an `if`, `for` or `switch` header), is refused. On r10: 73 statements are
  hoisted and none are refused; the callees are `filepath.Join` (41),
  `t.TempDir` (22) and a handful of others.
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
| non-test | 441 (437 reads, 4 value copies) | 24 (20 `&AppConfig`, 2 `&AppConfig.Tools`, 2 `Validate()`) |
| test | 2,493 (683 reads, 138 value copies, 1,455 write closures, 217 restores) | 19 (18 `&AppConfig`, 1 field address) |
| **total** | **2,934 sites in 524 files** | **43 sites in 30 files** |

These counts are at r10 `2be168931` (2026-11-21). At `344e033e2` the totals
were 3,048 automatic and 45 manual; r10's own work removed the difference.
Re-measure with `--summary` at the merge base.

**Whole-tree proof on r10 (2026-11-21, scratch checkout, not pushed).** It
was run at `344e033e2` and again at `2be168931`, with the hoisting codemod.
The steps were:

1. Copy `cow.go` in.
2. Route `mutateLocal` through `mutateCOW`.
3. Run `--write --test-writes` over `cmd/`, `internal/` and `main.go`.

At `344e033e2` that rewrote 3,048 sites in 548 files, with `AppConfig` still
present (transitional), and `go build ./...` and `go vet ./...` are both
green. The `2be168931` result is in section 14.
`go vet ./...` type-checks every test file, so the rewrite compiles across the
whole module.

The first run had exactly one vet error: a loop variable `c`, read inside a
rewritten test write, was shadowed by the closure's `c`. The codemod now picks
a parameter name the statement does not use (`c`, `cfg`, `cc`, `conf`, `c2`,
and so on), and a test pins this. The same run found that an in-package
`return AppConfig` would have become a self-calling `Snapshot()`. Inside
package config a value copy is now `*Get()`, also pinned by a test.

What remains after the codemod is therefore the manual list, deleting
`AppConfig`, and the test suites. No compile repair across the rewritten
files is needed. **Test triage is not measured.** Build and vet prove that
the rewrite compiles. They do not prove that every suite passes: a test can
depend on a timing the copy-on-write config changes. Only the four
prototype packages' suites have been run (section 14).

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

- **TestFlip quiesce.** r10 `929c3d74c` (pushed) adds
  `quiesceBackgroundForFlipPUT`. `runFlipped` calls it before `atReady`; it
  shuts the operations registry down so no maintenance op runs during the
  fixture's saves, and its comment names this lane. This lane **deletes the
  helper and its call site**. The acceptance test is TestFlip*
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
| Rebase churn: the F3-07 stack is still moving and rewrites `internal/config` (r10 deletes `update_service.go` and most of `persistence.go`) | certain | Core API in a new file (`cow.go`); the codemod is re-run, never hand-merged; the main-only `update_service.go` edits disappear with the file. On r10 the only hand edits are `mutateLocal`, `Normalized` on top of `Normalize`, pure `Problems`/`Validate`, the branching `NormalizeAndValidate` and `validateCandidate` (`Engine.Mutate` is already fixed upstream, `795b798e5`) |
| A reader writes through `Get()` (compiles) | medium | arch guard (syntax) and seal (runtime, tests); the codemod refuses such sites |
| Two normalisation bodies after the rebase (main's `normalizeInPlace` and r10's `Normalize`) | certain without care | section 6: `Normalized()` is defined on `Normalize()`; `normalizeInPlace` is deleted on rebase |
| Per-run pinning changes behaviour for long runs | low, intended | listed per site in section 7; long-lived services use a Source |
| Write cost: two reflective `Clone`s per write, three under `go test` | low | writes are saves, boot and test set-ups; measure `BenchmarkMutate` in the sweep PR. If a hot test loop shows it, the seal can skip unchanged generations |
| `testing` imported by non-test `internal/config` | low | precedent: `internal/books/store/pebble_store_open.go`; `testing.Testing()` registers no flags |
| Test semantics: `SetForTest` is in-memory only, like the direct assignment it replaces. A test that relied on a write reaching the Facade or store was already broken | low | tests that mean "save" call `Mutate` |
| A test reads `config.Get()` while another test's cleanup `ReplaceForTest`s | low | same exposure as today's shared global; packages that run `t.Parallel` with config writes need a per-test Source (none found in the prototype packages) |
| The prototype's transitional mode diverges (direct `AppConfig` test writes are seen by `Get()` only at the next write) | **measured: certain** on this branch (section 14) | main never runs it; the sweep is one PR. This branch's CI is red in unconverted packages that call the converted ones, by design |
| A test-file pointer hand-off (`cfg(&config.AppConfig)`) silently writes the mirror instead of the snapshot | certain until fixed by hand | it is on the manual list; `apikey_credential_guard_test.go:81` alone turns 10 `internal/server` route tests red once the package is converted (section 14). Each becomes `config.SetForTest(cfg)` |
| Codemod moves a call under the config write lock | removed | right-hand sides that call are hoisted (section 8) |

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

The codemod does 98.6% of the sites (2,934 of 2,977 at `2be168931`) in
seconds. The work is the 43 manual sites, the signature changes behind the
hand-offs, and running and triaging the suites.

**Order.** `AppConfig` is deleted **last**, not first. Slice A lands the new
API alongside the transitional mirror, so `AppConfig` keeps compiling while
B–E run in parallel worktrees off A. Slice F deletes the mirror once nothing
references it. Every slice's files are disjoint from every other slice's.

| Slice | Files (disjoint) | Work | Worker-hours |
|---|---|---|---|
| A. core and every shared signature | `internal/config/*` (incl. `register.go`), `internal/books/appconfig/write.go` (`validateCandidate` only), `internal/books/boot.go`, `cmd/root.go`, `internal/arch/*`; the registry: `internal/server/server.go` (all three of its hand-offs: `:128`, `:976`, `:1116`), `internal/server/registry_wire.go`, `internal/quarantine/*`, `internal/search/register.go`, `internal/activity/register.go`, `internal/plugins/itunes/register.go`, `internal/plugins/deluge/register.go`; the tools Source: `internal/tools/registry.go`, `internal/server/handlers/tools/*`, `internal/server/wire_handlers.go` | port `cow.go`, `Normalized`, pure `Problems`/`Validate`, the branching `NormalizeAndValidate` (section 6) onto r10, with the `AppConfig` mirror **kept**; `validateCandidate` normalises explicitly; `GetConfig` returns a `Source` and its 8 callers adapt; `func() *tools.ToolsConfig`; guard and ratchet; in-package codemod | 4 |
| B. mechanical | every remaining file the codemod touches, **except** those in A and C–E | `--write --test-writes` with `--exclude` on A and C–E files, gofmt, build and vet, then the package suites under `-race`; triage any test that depended on seeing a direct write before the next publish | 2, plus triage (unmeasured) |
| C. AI parser | `internal/ai/*`, `internal/server/{diagnostics_ai_ops.go,ai_ops.go,entities_ops.go}`, `internal/server/handlers/ai.go`, `internal/server/handlers/aibackends/*`, `internal/scanner/ai_parse_{async,journal}.go` | per-op `config.Get()`; the parser reads the key and the enabled flag off one snapshot | 2 |
| D. organizer + metafetch | `internal/organizer/*` (incl. its tests), `internal/metafetch/{file_pipeline,service_apply}.go`, `internal/server/{batch_save_op.go,handlers/organize.go}`, `internal/audiobooks/organize_inplace_undo_test.go`, `internal/server/{apikey_credential_guard,scan_integration}_test.go` | `NewOrganizer(config.Get())`; tests use `Fixed` | 2 |
| E. deluge + rest | `internal/deluge/*`, `internal/plugins/deluge/*` (except `register.go`), `internal/maintenance/jobs/bulk_deluge_import*.go`, `internal/server/deluge_discovery.go`, `internal/appdirs/database_dir_test.go`, `internal/scheduler/on_startup_dropped_test.go` | per-run pin; the deluge importer adapter keeps a Source | 1.5 |
| F. delete + acceptance | `internal/config/*` (mirror only), `internal/books` TestFlip fixture | delete `AppConfig` and the transitional lines in `cow.go`; then delete the quiesce and run `-race -count=20` with `CheckSealed` in cleanup | 1.5, plus suite time |

About **13 worker-hours, plus B's test triage, which is not measured yet**
(section 8; section 14 has the first data point). A lands first. B–E run in
parallel off A, then F. The coordinator stacks them into **one PR**
(section 12). Wall-clock is about 5–6 hours with 4 workers, dominated by A
and by the `-race` suite runs on the remote builders.

## 14. Status of the prototype (this branch)

- Core API: `internal/config/cow.go`; `Mutate` routed through it;
  `Normalized`, pure `Validate`, `NormalizeAndValidate`; both boot call sites
  switched; `update_service.go` normalises explicitly.
- Codemod applied to `internal/reconcile`, `internal/dedup` and
  `internal/scanner`: 508 sites, all tests included. The 2 scanner hand-offs
  were reworked by hand.
- Guards: `internal/config/cow_test.go` and
  `internal/arch/config_cow_guard_test.go`.
- Evidence at `34f7ad97e`, run on the remote builder:
  - Green: `go build ./...` and `go vet ./...`.
  - Green under `-race`: `internal/config`, `internal/scanner`,
    `internal/dedup` and `internal/reconcile`.
  - Green without `-race`: the arch guard and ratchet tests, `./cmd/...`, and
    (at `4946aacf6`) the `internal/books` Boot, Settings and Validate tests.
  - The codemod has 15 unittest cases.
- **Transitional mode, measured.** These are 8 unconverted packages whose
  tests write `config.AppConfig` directly and call the converted packages.
  - 5 pass: `server/handlers/system`, `audiobooks`, `decisions`, `importer`
    and `metafetch`.
  - 3 fail: `plugins/dedup` (1 test), `plugins/maintenance` (5) and
    `internal/server` (5). This is a **lower bound**. Only packages that
    import the three converted packages directly and write `AppConfig` in
    tests were run; transitive callers were not, and `internal/books` ran
    only its Boot, Settings and Validate tests.
  - Causal check: running the codemod over those three packages (not
    committed) turns `plugins/dedup` and `plugins/maintenance` fully green.
  - In `internal/server`, 4 of the 5 failures clear and 10 new route tests
    fail. All 10 are in the four files that build their server with
    `setupCredGuardServer`/`setupCredGuardServerWith` (`admin_routes`,
    `legacy_manifest`, `master_list_handler`, `owner_routes`; checked by grep,
    not re-run). That fixture's
    `cfg(&config.AppConfig)` (`apikey_credential_guard_test.go:81`) is one
    of the 19 manual test hand-offs. It writes the mirror, which `Get()`
    never reads.
  - `TestCalculateLibrarySizes_ExcludesAppDirs` still fails and is not
    diagnosed by conversion. **It is not the prototype's.** On `origin/main`
    `368be5b83`, on the same builder, it fails 3 of 3 runs at the same
    assertion (`"2048" is not greater than "500000"`). The builder's
    filesystem likely stores the test's 500 kB zero-filled file in about 2 kB
    on disk, and the walker counts disk usage. It is outside this lane.
  - This is the first data point for slice B's test triage: mechanical
    conversion fixes the transitional failures, and the manual test
    hand-offs are load-bearing.
- Base: `origin/main`. On r10 the hand edits differ (sections 4 and 6), and
  the codemod output is regenerated, not merged. A whole-tree r10 run builds
  and vets clean (section 8).
