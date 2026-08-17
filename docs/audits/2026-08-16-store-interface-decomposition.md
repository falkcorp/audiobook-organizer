<!-- file: docs/audits/2026-08-16-store-interface-decomposition.md -->
<!-- version: 1.1.0 -->
<!-- guid: 70654a6c-a4b1-42c7-a06f-ff48ba1783d7 -->
<!-- last-edited: 2026-08-16 -->

# Decomposing `database.Store`: what the evidence actually supports

**Measured at `8011a755` (main, 2026-08-16).** Every number below names the ref it was
taken at; `main` moved during this investigation and undated figures are not reproducible.

Two independent agents were commissioned — a Go architecture agent (decomposition design)
and a code-review agent (adversarial risk review) — with deliberately opposed briefs. This
document arbitrates between them. Where they disagreed, the disagreement is named and
adjudicated against a re-measurement run by hand, not split down the middle.

**Nothing here has been executed.** This is a proposal.

---

## 1. Verdict in one paragraph

**Do not run a third interface-segregation sweep.** The 2026-04-17/18 sweep hit its stated
target exactly and its output held for four months; 92% of today's wide-store debt is in
files that did not exist when it closed. A sweep addresses the other 8% and re-accrues on
the same growth curve. **Build the authoring-time gate instead**, land six one-line keystone
signature changes that unblock everything downstream, delete ~40,600 lines of dead generated
mocks, and narrow opportunistically thereafter. One question — whether to redesign the
`MaintenanceJob.Run` contract — is a genuine judgment call and is escalated in §9 rather
than decided here.

---

## 2. Retraction: three things I reported earlier were wrong

Stated plainly, because they were used to justify a conclusion.

| I said | Actual | Why I was wrong |
|---|---|---|
| `database.Store` regressed **6.5×**, "worse than before the sweep started" | Code-only: **179 occ / 64 files → 33/12 → 286/152**. Per 1k non-test LOC: **1.48 → 0.229 → 0.856** | My grep counted **doc-comment** mentions. At sweep-close, 24 of 36 flagged files held exactly one match and it was a comment — the sweep's own documentation of its work inflated the count. I also never normalized for growth (non-test LOC 121k → 144k → 334k). |
| The sweep "decayed" and its close-out was premature | It targeted `79 → ~12` files and **measured 12**. Of HEAD's 152 wide files, **140 never existed at close-out**, 5 held (all documented-intentional), **7 regressed** | Not decay. Outgrowth. |
| `GetGlobalStore()` at **308 sites** is the regression vector | **Zero production callers.** 301 of 305 are `_test.go`; the rest is the declaration and its own tombstone comments | The `SERVER-GLOBAL-STORE-AUDIT` already removed it from production. I counted comments again. |

The honest sentence about the trend is: **the sweep's gain has been about 60% eroded by
growth, and has not passed the pre-sweep baseline.** That is still a real trend worth acting
on — see §9 — but it is not the emergency the earlier framing implied.

### The instrument that works — for the *trend*

```bash
git grep -n "database\.Store\b" <ref> -- internal/ cmd/ \
  | grep -v "_test\.go" | grep -vE ':[0-9]+:[[:space:]]*//' | wc -l
```

Comparable across refs, so the three-point trend above is sound. **It is not a baseline.**
See §2b.

---

## 2b. The AST baseline: **338**, and grep undercounts it by 15%

Computed with `golang.org/x/tools/go/packages` at full type resolution (143 packages, 0 load
errors), counting declarations whose **resolved type is** `database.Store`, non-test, excluding
generated `/mocks/` packages. This is the number the gate keys on.

| category | count |
|---|---|
| function parameter | **280** |
| struct field | **35** |
| function result | 8 |
| package/local var | 6 |
| interface method **result** | 4 |
| interface method **param** | 3 |
| type alias | 2 |
| **TOTAL** | **338** across **35 packages** |

Top packages: `internal/database` **87** · `internal/maintenance/jobs` **59** ·
`internal/server` **44** · `internal/plugins/maintenance` **24** · `internal/dedup` 15.

**Three things grep structurally cannot see**, all confirmed here:

1. **Uses inside `internal/database` itself.** Within that package the type is spelled `Store`,
   not `database.Store`. Grep finds **11** declarations there; the AST finds **87**. That single
   package hides **76** — and it is the largest holder in the codebase.
2. **The two type aliases' use sites.** `type Store = database.Store` and
   `type OrganizeStore = database.Store` mean every downstream use says `Store` / `OrganizeStore`.
   `go/types` collapses aliases to the same `*types.Named`, so they are counted automatically.
3. **The category.** Grep cannot tell a parameter (free to narrow, §8) from a struct field (not
   free). That distinction decides the migration order and grep is blind to it.

### Rule 1's population is **7**, not 6 — and the seventh explains the blind spot

Both hand-scanners in §3 reported 6. `go/types` finds a seventh:

```
internal/maintenance/job.go:56           MaintenanceJob.Run                    param
internal/plugins/maintenance/deps.go:22  ServerDeps.ExecuteSeriesPrune         param
internal/plugins/maintenance/deps.go:22  ServerDeps.ExecuteSeriesNormalizeCore param
internal/database/store_capability.go:68 StoreUnwrapper.Unwrap                 result   ← MISSED
internal/plugins/maintenance/deps.go:22  ServerDeps.Store                      result
internal/server/library_list_warmer.go:152 storeUnwrapper.Unwrap               result
internal/server/server.go:1177           unwrapper.Unwrap                      result
```

`StoreUnwrapper.Unwrap` is the *root* of the capability pattern — the one declaration §6 calls
the single genuinely structural blocker — and **both text scanners missed it**, because it lives
in `internal/database` and says `Store`. The instrument was blind to the most important member of
the population it was counting.

**Package globals, after excluding blank-identifier assertions and one function-local
(`store.go:1245`): three**, exactly as reported in §5 — `internal/database/store.go:1217`
`globalStore`, `internal/maintenance/job.go:73` `store`, `internal/scanner/scanner.go:171`
`pkgStore`.

### Reproducing it

The analyzer is ~200 lines and lives outside the repo (a standalone module; it never touches
`go.mod`). **It carries a self-test** asserting three hand-verified canary sites and exits
non-zero if the total is 0 or any canary is missing. That guard exists because **its first run
reported a confident `TOTAL = 0`** — `packages.NeedCompiledGoFiles` is not implied by
`NeedFiles`, so `CompiledGoFiles` was empty and every file was silently skipped. Zero looked
exactly like a measurement. Any gate built from this must keep the canaries; see §12.

---

## 3. Method, and the instruments that failed

**Six** measurements in this investigation returned confident, wrong answers. They are recorded
because the same traps apply to anyone re-running this — and because two of them were caught
only by a *second, differently-keyed* instrument disagreeing.

| Instrument | Wrong answer | Correct | Failure mode |
|---|---|---|---|
| `git grep 'database\.Store'` | 507 occ / 201 files | 286 / 152 | counts comments |
| the same, as a *baseline* | 286 | **338** (§2b) | blind to `internal/database`'s own 87 declarations, which say `Store` |
| `grep 'mocks\.MockX'` for mock usage | 8 of 45 used | **3 of 45** | `mocks.` also matches `handlersmocks.`, `dedupmocks.`, … |
| the same, restricted to importers of `database/mocks` | 2 of 45 used | **3 of 45** | package is imported under **three aliases** — `mocks` (33 files), `dbmocks` (20), `databasemocks` (4) |
| `gawk` block-scan for interface bodies | 8 sites | **6** | `type vgBackfiller interface{ … }` on one line — the scanner opened a block that never closed and swallowed the next 30 lines |
| the `go/packages` analyzer, first run | **`TOTAL = 0`** | 338 | `packages.NeedCompiledGoFiles` is not implied by `NeedFiles`; `CompiledGoFiles` was empty so every file was skipped |

**The two text scanners that agreed on 6 were both wrong** — `go/types` found 7, and the one they
missed (`StoreUnwrapper.Unwrap`) is the single most important member of the population. Agreement
between two instruments that share a blind spot is not corroboration.

**And `TOTAL = 0` is the most dangerous result in the list**, because nothing about it looks like
a failure. It is why the analyzer now carries canary assertions and exits non-zero when they are
missing. This repo already has one gate — `check-mock-fresh` — that has looked like enforcement
and enforced nothing for months (§11). **Before trusting the §10 gate, feed it a deliberately bad
input and confirm it fails.**

Three separate greps gave three different mock counts (8 / 3 / 2). Only resolving each
importing file's actual local alias settled it. **The rule that held: when two differently-keyed
instruments disagree, neither is trusted until the disagreement is explained.** The gawk/Python
disagreement on the interface scan was resolved by reading the source; both now agree on 6.

Counts sourced from an agent and not re-measured by hand are marked ⚠ below.

---

## 4. Finding 1 — this was outgrowth, not decay

Decomposing HEAD's 152 code-only wide files against the tree at sweep-close `b9e8095f`:

| category | count | share |
|---|---|---|
| **did not exist at close-out — born wide** | **140** | **92%** |
| existed and was already wide — held | 5 | 3% |
| existed, was clean, **regressed** | 7 | 5% |

The 5 that held are exactly the ones the original design called legitimately wide:
`internal/server/server.go`, `internal/server/indexed_store.go`, `internal/server/undo_engine.go`,
`internal/operations/state.go`, `internal/testutil/integration.go`.

The 7 genuine regressions, in full: `cmd/diagnostics.go`, `internal/backup/backup.go`,
`internal/scanner/scanner.go`, `internal/server/external_id_backfill.go`,
`internal/server/file_io_pool.go`, `internal/server/metadata_batch_candidates.go`,
`internal/server/version_lifecycle.go`.

Where the 140 landed: `internal/maintenance/jobs` 37, `internal/server` 14,
`internal/plugins/maintenance` 13, `internal/metafetch` 7, `internal/dedup` 6. ⚠

**Why the original design could not prevent this.** Its §6 was a migration catalog of 79
*named files*. Every package that produced the regression — `internal/maintenance/jobs`,
`internal/plugins/maintenance`, `internal/audiobooks`, `internal/metafetch`, `internal/dedup` —
did not exist to be listed. Its §9 success criteria ("all 9 PRs land, `make test` green,
`mocks-check` green") are all satisfiable at a single commit and none is falsifiable a month
later. A file list cannot bind code written after it.

**The consequence for planning:** a gate that watched the files the sweep touched would have
caught **7 of 192 — 3.6%**.

---

## 5. Finding 2 — the amplifier is interface method signatures, and there are six

An interface method that takes `database.Store` dictates the width of every future
implementer, and implementers are written by people who never open the interface file.

`internal/maintenance/job.go:70`:

```go
Run(ctx context.Context, store database.Store, reporter ProgressReporter, dryRun bool) error
```

**One line, created 2026-05-01 — 13 days after the sweep closed — minted 31 wide files**, one
per `MaintenanceJob` implementation. Go cannot dispatch an interface method on a
per-implementation parameter type, so no signature sweep can narrow these.
`internal/maintenance/` + `internal/plugins/maintenance/` together hold **88 of the 286
code-only occurrences (31%)**. ⚠

The complete population today is six, hand-verified by two independently-keyed scanners:

| Site | Verdict |
|---|---|
| `internal/server/server.go:1178` — `Unwrap() database.Store` | **Standing exemption** — required by `StoreUnwrapper` (`internal/database/store_capability.go:68-70`) |
| `internal/server/library_list_warmer.go:153` — `Unwrap() database.Store` | **Standing exemption**, same reason |
| `internal/maintenance/job.go:70` — `MaintenanceJob.Run` | **Allowlist with an expiry note** — see §9 |
| `internal/plugins/maintenance/deps.go:23` — `Store() database.Store` | **Fix** — `ServerDeps` has 42 other methods, all behavior delegates |
| `internal/plugins/maintenance/deps.go:36` — `ExecuteSeriesPrune(…, store database.Store, …)` | **Fix** |
| `internal/plugins/maintenance/deps.go:39` — `ExecuteSeriesNormalizeCore(…, store database.Store, …)` | **Fix** |

**Four to fix, two permanent exemptions.** This is the cheapest high-leverage rule available
and it is the one thing that would have prevented 31 of the 140.

A second half of the same rule: **no new package-level `database.Store` variable or accessor.**
Two were created after the audit that removed the last one —
`internal/maintenance/job.go:73-76` (`InjectStore`/`GetStore`) and
`internal/scanner/scanner.go:171-186` (`SetStore`/`getStore`, 41 non-test call sites ⚠).
A completed cleanup with no gate has a half-life of roughly four weeks in this repo.

---

## 6. Finding 3 — the claw-back ceiling: 11 runtime escapes grep cannot see

This is the finding neither agent fully surfaced, and it bounds how far narrowing can go.

Eleven sites hold a **narrow** interface and reach back to the wide store **at runtime**:

```
internal/audiobooks/service_filtering.go:775, 839, 862, 1130
internal/server/handlers/audiobooks/handler.go:363, 432, 487
internal/server/handlers/system/handler.go:694, 893, 929
internal/plugins/maintenance/intro_transcribe.go:201
```

All of the shape `if uw, ok := svc.store.(interface{ Unwrap() database.Store }); ok {`.

`internal/audiobooks/service.go:69` holds `audiobookStore` — a *correctly* narrowed named
composite, with a comment explaining its dependency surface. And then
`service_filtering.go:775` unwraps straight back to `database.Store` to reach
`GetAllBookSummariesFiltered`.

**Narrowing the parameter moved the wide dependency from compile time to a type assertion,
where no static gate can see it.** These are not decorator plumbing; they are consumers
routing around their own declared interface.

Two consequences:

1. **Rule 1's cost is 17 sites, not 6.** The 11 need individual adjudication, not a blanket
   `Unwrap()` exemption. Any gate must catch inline anonymous interface literals.
2. **The narrowing program has a nameable ceiling.** Consumers that need `Unwrap()` to reach
   capabilities cannot be narrowed past that point. The capability surface is ~120 sites ⚠
   (`AsSyncIdentityStore` 38, `AsSyncFileStore` 27, `AsPebbleStore` 19, `AsBookmarkStore` 10,
   14 distinct `AsCapability[T]` instantiations). `StoreUnwrapper.Unwrap() database.Store`
   returns the full interface by necessity — **leave it, allowlist it, say why.**

---

## 7. Finding 4 — the crux: the existing sub-interfaces are not narrow

This is where the two agents genuinely disagreed, and it is the most decision-relevant
finding in the document.

Measured across the 40 interfaces `Store` embeds (398 methods total):

| interface | methods |
|---|---|
| **BookStore** | **51** (BookReader 35 + BookWriter 16) |
| OpsV2Store | 32 |
| OperationStore | 30 |
| TagStore | 27 |
| BookFileStore | 27 |
| AuthorStore | 21 |

**median 7 · mean 9.9 · six interfaces exceed 20 methods · the top five hold 41% of all 398.**

Now cross that with consumer arity. Across ~275 non-test parameter sites, **162 of 175 leaf
consumers call ≤3 distinct methods**, and no leaf site anywhere calls more than 11. ⚠ There is
no 40-method consumer. **The god object is not being used as one.**

So a typical consumer migrating `database.Store` → `database.BookStore` goes from 398 methods
to 51. **It is still ~17× wider than what it uses.** The headline "398 → 3" that motivates
decomposition is not what the existing interfaces deliver. They deliver **398 → 51**.

### This is visible at the exemplar

`internal/sweep/sweeper.go:27` — `SweepTombstones(store database.BookStore)` — is the file most
often cited as proof the pattern works. Its test does **not** use the generated mock;
`internal/sweep/sweeper_test.go:18` hand-rolls a `MockBookStore` implementing all **51** methods
to serve a function that calls two of them.

The same shape at `internal/reconcile/reconcile.go:31`: a composite of 4 `database`
sub-interfaces = **115 methods declared, 11 used** — 90% dead surface, and its test double still
embeds the full `database.Store` to compile.

**Adjudication.** The go-specialist is right and the reviewer's "opportunistic narrowing captures
most of the value" is wrong: retargeting the existing 40 buys far less than advertised. The
correct pattern is the one at `internal/quarantine/service.go:25` — **7 methods declared, 7
used** — an explicit method list in the consuming package, unexported unless it must cross a
package boundary, with a `var _ Store = (*database.PebbleStore)(nil)` assertion (which
`internal/organizer/service.go:44` has and `internal/quarantine` lacks).

**Do not compose `database` sub-interfaces.** That pattern produced reconcile's 115/11.

**The honest counter, which is why §9 exists:** doing this properly means ~150 newly-defined
per-consumer interfaces across 42 packages, each an independent decision about what its consumer
may touch. That is categorically larger than "a mechanical sweep," and it is the version of the
work anyone proposing "per-consumer interfaces" is actually proposing.

---

## 8. Finding 5 — the mock objection is dead, and it was the main perceived cost

Both agents concluded independently that narrowing does not collide with the permissive
hand-written mock. **I measured it rather than accept the argument.**

**The reasoning:** `internal/database/store.go:19-58` composes `Store` *purely* by embedding 40
sub-interfaces, declaring no methods of its own. `var _ Store = (*MockStore)(nil)`
(`mock_store.go:30`) therefore guarantees `*MockStore`'s method set is a superset of every
embedded interface's. Narrowing a parameter is **monotone** — it can only shrink the required
method set.

**The experiment**, at `8011a755` in a clean worktree:

```
internal/database/tag_helpers.go:39
-  func EnsureSingletonBookTag(store Store, …)
+  func EnsureSingletonBookTag(store TagStore, …)
```

| check | result |
|---|---|
| files changed | **1** (`1 insertion(+), 1 deletion(-)`) |
| call sites requiring edits | **0** — 12 call sites across `dedup`, `metafetch` |
| `go build ./...` | **exit 0** |
| `go vet ./...` (full tree, never scoped) | **exit 0** |
| test compilation in `database`, `dedup`, `metafetch`, `server` | **exit 0** |

Change reverted; nothing but this document is committed.

**Two caveats that survive.** (a) This is exact for **parameters and results**; narrowing a
**struct field** is *not* free, because code that reads the field and passes it somewhere
still-wide fails to compile. Parameters first, fields second, per-struct. (b) n=1. It is one
measurement of a claim that is sound by Go's type rules, not a survey.

### The free win is 2× what I previously reported

My merged audit (`docs/audits/2026-08-16-manual-mock-inventory.md`, PR #2499) says **37 of 45
database mocks unused / 22,001 dead lines**. That is **wrong** — it used the colliding bare
`mocks.` grep. Corrected by alias resolution:

| | |
|---|---|
| generated types in `internal/database/mocks` | **45** |
| referenced anywhere | **3** — `MockStore` (54 files), `MockImportPathStore` (2), `MockOpsV2Store` (2) |
| **unused** | **42** |
| dead lines in `mock_store.go` | **40,569 of 52,752 — 76%** |

Plus two fully-dead generated packages: `internal/scanner/mocks` (442 lines, **0** importers) and
`internal/operations/mocks` (206 lines). Repo-wide generated mock footprint: 90,171 lines.

`internal/scanner/mocks` is the sharpest illustration: it is generated, unused, and
`internal/scanner`'s own tests hand-roll `fullMockScanner` (`scanner_coverage_test.go:655`)
instead — because 21 of 22 scanner test files are `package scanner` and importing the mocks
package would cycle. **Delete the `Scanner:` entry; keep the hand-written double.**

Deleting the 42 config entries is a config-only change, guarded by the real `mocks-check` gate.
**No behavioral surface. This is the single largest, safest item in the plan.**

> A correction fragment for the merged audit's 37/8 figure is included in this branch.

---

## 9. Arbitration: where the agents disagreed

| Question | Go agent | Review agent | Adjudication |
|---|---|---|---|
| Run a third sweep now? | implied yes, phased | **no** | **Reviewer.** 140 of 152 born wide — a sweep fixes 8% and re-accrues. |
| Is there remediation short of a sweep? | **yes — 6 keystone signatures** | rejects sweeps generally | **Go agent.** The keystones are 6 one-line changes and are not the thing the reviewer rejected. |
| Compose sub-interfaces, or declare explicitly? | **declare explicitly** | cites `internal/sweep` as the model | **Go agent, on evidence** — reconcile 115/11, sweep 4-of-51. §7. |
| Does narrowing break the mocks? | no | no | **Both, and now measured.** §8. |
| `GetGlobalStore` the vector? | no (self-corrected) | no | **Both.** Zero production callers. |
| Metric to gate on | AST/`go/types` | AST/`go/types` | **Both.** Grep is 28% comments and blind to aliases. |

### The one open question — yours to call

**Should `MaintenanceJob.Run` be redesigned?** The agents take opposite positions and both
arguments are good.

- **Go agent — no.** It is a real framework boundary; re-typing it is a **31-file atomic edit**
  that cannot be waved. But the interface dictates `Run` and *nothing else*: the ~35 free helper
  functions beneath it (`vgUnlinkOutliers`, `csMergeSeriesGroup`, `ddSoftDeleteBook`,
  `deleteOldOperations`, …) chose `database.Store` with nothing compelling them, and measure 1-4
  methods each. ⚠ Narrow the layer below; allowlist `Run`. Fully mechanical, no framework change,
  and it is where the volume is.
- **Review agent — yes, or don't bother.** `internal/maintenance/jobs` is the densest debt (37
  new wide files in four months, 39 leaf + 17 propagating sites) and it is exactly where
  "narrow it when you touch it anyway" is weakest. If a sweep happens at all, that package
  *with* the `Run` redesign in scope is the only version it would defend.

**My recommendation: the go agent's.** Same volume captured, no 31-file atomic edit, and it
leaves the redesign available later. But the reviewer's objection is not answered by my
recommendation — it is deferred by it, and you should know that.

### The strongest argument against this whole document, kept verbatim

> A gate without remediation freezes the debt instead of paying it. It does nothing about the
> existing 286 occurrences across 152 files, and at that size the allowlist stops being a
> transition mechanism and becomes the permanent state — which is materially worse than a clean
> sweep followed by a gate, because it *legitimizes* the wide type. There is also a real chance
> nobody funds remediation once the metric stops getting worse; "trend flat" reads as "solved"
> on a dashboard.

---

## 10. Recommended plan

Ordered so each step is independently valuable and none blocks on the next.

| # | Step | Size | Risk |
|---|---|---|---|
| 1 | **Delete the 42 unused mockery entries** + `internal/scanner/mocks` + `internal/operations/mocks` | ~41,200 lines removed, config-only | none — gated by `mocks-check` |
| 2 | **Fix or delete `check-mock-fresh`** (`Makefile`) — it runs `go generate` where the repo has **zero** `//go:generate` directives, so it can never fail, and it runs in `make ci` | one target | none |
| 3 | **Land the gate.** Rule 1 (no `database.Store` in interface method signatures, incl. inline literals — **population 7**, §2b), Rule 2 (shrink-only ratchet keyed on `package:Symbol`, **baseline 338**, §2b), Rule 3 (no `type X = database.Store`) | CI only | **must be tested with a deliberately-bad input before merge** — see §12 |
| 4 | **Six keystone signatures** — one line each, unblocks every "blocked by callee" site found | 6 lines | measured: §8 |
| 5 | **Write the missing design doc** at the path `internal/database/store.go:17` already cites | docs | none |
| 6 | **Split `iface_misc.go`** — 25 of the 40 sub-interfaces live in one 18.6 KB file | pure move | `go build` verifies |
| 7 | **Narrow the ~35 helpers beneath `MaintenanceJob.Run`** — pending §9 | ~35 funcs | mechanical |
| 8 | Opportunistic narrowing thereafter — **demoted**, per §7 it buys 398→51, not 398→3 | ongoing | low |

### The six keystone signatures (step 4)

```
internal/database/tag_helpers.go:39    EnsureSingletonBookTag(store Store,…)   → TagStore   ✅ MEASURED
internal/database/tag_helpers.go:91    EnsureSingletonAuthorTag                → TagStore
internal/database/tag_helpers.go:134   EnsureSingletonSeriesTag                → TagStore
internal/database/storecap.go:43       GetAIJobs(store Store)                  → AIJobsStore
internal/database/metadata_fetch_cache.go:168  CountCachedMetadataFetches(+2)   → MetadataCacheStore
internal/versions/ingest.go:46         CreateIngestVersion                     → BookVersionStore+BookFileStore
```

Payoff: unblocks the `internal/dedup.Engine` field (`engine.go:66`), and deletes both false
type aliases (§11). Verification is `go build ./... && make ci`.

### Sequencing hazards

- **7 active worktrees.** `main` moved `6afed45e → 8011a755` during this review. Overlap with the
  152 wide files is currently **1 file** (`fix/actionable-warning-logs`); `perf/test-memfs-pebble`
  touches 26 Go files, none wide. **That is a snapshot, not a property.**
- **Never scope `go vet`.** The April 18 / PR #394 post-mortem records that scoped vet missed
  test-file breakage. Full-tree, always.
- **Do not touch concurrently with any of this:** `internal/server/server_lifecycle.go:355-365`
  and `internal/server/indexed_store.go` (the wrap install — a capability regression could not be
  attributed); `internal/maintenance/job.go:70`; `internal/testutil/integration.go`.
- **`Store` stays intact forever.** All value comes from consumers ceasing to *name* it. Shrinking
  `Store` changes what `indexedStore` promotes — the exact shape of the 2026-07-30 incident.

---

## 11. Out of scope — file these separately, do not bundle

Bundling these into the refactor is how the refactor becomes unfundable. Each has a `todo.d/`
fragment in this branch.

**Concurrency / correctness**

1. **`globalStore` unsynchronized access.** `store.go:1217` declares `globalStoreMu sync.RWMutex`,
   but `InitializeStore` (`:1261`) writes bare, and `CloseStore` reads (`:1275`) and writes
   (`:1276`) bare — racing the `RLock`-protected accessors. `:1280` is a
   `time.Sleep(100 * time.Millisecond)` commented "brief pause to let in-flight goroutines notice
   the nil" — a race workaround, not a fix. Blast radius is test-only today (zero production
   readers), which is exactly why it will become production-critical if anyone reintroduces a
   `GetGlobalStore()` call.
2. **`wire_abs_routes.go:494` bare assertion in a goroutine.** `if ps, ok := s.Store().(*database.PebbleStore)`
   — the literal form `store_capability.go:44` forbids. `Server.Store()` (`server.go:331-333`) reads
   `s.store` with no lock while `server_lifecycle.go:362` writes `s.store = wrapped`; the goroutine
   is launched from `setupRoutes()` inside `NewServer`, i.e. before `Start()`. The data race is
   certain; **which side wins is not**, so this is not a claim that warmup is skipped in prod.

**Capability-pattern defects — the historically-realized class**

3. **`iface_assert.go`'s guarantee is false for 4 interfaces.** Its comment claims compile-time
   proof that `PebbleStore` satisfies *every* sub-interface; it asserts 36 of 40. Missing:
   `OAuthIdentityStore`, `MetadataCacheStore`, `RejectedMetadataStore`, `ReviewStore`. One line each.
4. **`AsExternalIDReassigner` uses a bare assertion** (`internal/merge/service.go:34-42`), called on
   `ms.db` at `:236, :377`. Latent — `merge.Service` is registry-built, which holds the bare store —
   but a single wiring change turns it into silent skipping of iTunes-PID/ASIN reassignment on merge.
   Same shape at `internal/plugins/acoustid/reset_all.go:69` and `lsh_backfill.go:86`.
5. **`prodSchedulerStore` is a decorator without `Unwrap`** (`internal/operations/registry/register.go:40-42`).
   Defect-*shaped*, not live — no capability lookup currently runs through it.

> **Why this class matters more than it looks.** `internal/server/server_lifecycle.go:1737-1766`
> documents the **third** capability lost to the same decorator, measured in production
> 2026-08-10 23:07:40: the version-group index backfill "had NEVER ONCE RUN, silently" since the
> decorator was installed, and is the likely origin of the under-reporting reproduced in #2277.
> This is the #1-ranked risk of any careless narrowing, and it is not hypothetical here.

**Documentation defects — three comments that are false at HEAD**

6. `internal/importer/service.go:27-31` — `type Store = database.Store`, justified by
   "`versions.CreateIngestVersion` requires the full Store interface." It uses **4 methods**.
7. `internal/server/handlers/organize.go:57-62` — `type OrganizeStore = database.Store`, justified by
   `organizer.SetStore` and `deluge.NotifyDelugeAfterOrganize`. At HEAD those take a **4-method**
   `OrganizerStore` and an anonymous `interface{ database.BookVersionStore }` respectively.
8. `internal/dedup/collectors_metadata.go:51` — "`database.EnsureSingletonBookTag` (which requires
   the full Store interface)." It uses **3**.
9. `internal/database/store.go:17` cites `docs/superpowers/specs/2026-04-17-store-interface-segregation-design.md`,
   which is **not on main**. Recoverable via `git show 29e256ac:<path>`. A dangling reference in the
   canonical file is itself an authoring-discipline failure.

**Test-quality defects**

10. **~88 of `MockStore`'s 399 methods have no `Func` field** — hardwired to a zero return that no
    test can override. `GetAllAuthorBookCounts` (`mock_store.go:863`) returns `map[int]int{}, nil`
    unconditionally, so `TestListAuthors_Success` asserts against a response where every author has
    `BookCount: 0`. ⚠
11. **A vacuous test.** `internal/server/organize_service_test.go:34` sets `GetAllBooksFunc`; the code
    under test calls `GetAllBooksCore`, whose func field is unset → `nil, nil`. The test asserts only
    `err == nil` and passes against a mock wired to nothing. ⚠

---

## 12. Confidence

**Hand-verified by me at `8011a755`** (not agent-reported): the three-ref occurrence/file counts;
the 140/5/7 decomposition and its file lists; the 6-site Rule 1 population (two independently-keyed
scanners agreeing); the 11 inline claw-back sites; the 4 missing `iface_assert` entries; the 2 type
aliases; sub-interface method counts and the 398 total; the 45/3/42 mock census and 40,569 dead
lines; the build/vet/test-compile experiment.

**Agent-reported, marked ⚠, not re-measured:** per-package distribution of the 140; the 88/286
maintenance share; consumer arity (162 of 175 ≤3 methods); the ~120 capability sites; the ~35
narrowable helpers; the 41 scanner `getStore()` sites; findings 10 and 11.

**Known-unverified:**

- Which side wins the `wire_abs_routes.go:494` race. The race is certain; the outcome is not.
- Whether zero-method leaf sites propagate via plain field assignment — the analyzer was blind to
  `x.store = store`, 58 candidate sites, hand-verified n=1.
- ~~The AST/`go/types` baseline the gate needs.~~ **Computed: 338.** See §2b. My earlier estimate
  ("near 286") was low by 18% — I had not anticipated that `internal/database`'s own 87
  declarations are invisible to the grep.
- The §8 experiment is n=1 on parameters and says nothing about struct fields — and §2b now puts
  a number on that gap: **35 struct fields**, each needing its own `go build` check.

**One process requirement, from this repo's own history.** `check-mock-fresh` has looked like
enforcement and enforced nothing for months. Before trusting the §10 gate, **feed it a deliberately
bad input and confirm it fails.** A gate that cannot fail is worse than no gate: it manufactures
confidence.
