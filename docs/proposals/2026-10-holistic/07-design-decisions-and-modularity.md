<!-- file: docs/proposals/2026-10-holistic/07-design-decisions-and-modularity.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4ee6ac26-168e-466d-8c91-11d955e1487d -->
<!-- last-edited: 2026-10-08 -->

# 07. Design decisions and modularity

- **Analyst:** `design`
- **HEAD:** `f7211eb39` (2026-10-08)
- **Status:** planning only; no code is changed.
- **Appendices:**
  - [A-measurements.md](07-design-decisions-and-modularity/A-measurements.md): every number in this doc and the command behind it.
  - [B-decisions.md](07-design-decisions-and-modularity/B-decisions.md): the decision register, D1 to D17. Each entry gives the current choice and why, the cost, the alternatives, the recommendation and the migration effort.

> **Coordinator note (08, 2026-10-08).** (1) R1 is folded into 01 P1, which flips the default, and 01 P2, which ports the NutsDB tests. R1's rollback is wrong: `ACTIVITY_BACKEND=sqlite` is not a symmetric rollback (01 Q2). (2) There are 10 `UpdateBook` call sites at HEAD, not 11 (`grep -rn '\.UpdateBook(' internal cmd`, excluding tests and mocks). S2 shrinks further once S3 deletes `indexed_store.go`, 01 Q4 deletes `rename.go`'s apply path and 01 P73 deletes `book_dedup.go:693`. (3) 01 handed the `Book.FilePath` migration (M6: 483 reference lines) to this doc, and nothing here picked it up. It is added as **07-S6**. (4) M6 to M13 are executed inside 05's port waves 12A to 12E, one domain per wave PR, so each op moves and ports once. They are not separate PRs.

**Approved decisions this builds on, without re-arguing them:**

- the storage efficiency redesign (`docs/design/2026-10-03-storage-efficiency-design.md`, approved 2026-10-03), in particular P1 (one write chokepoint), P2 (types cannot carry what they must not wipe), P3 (cut over with no backward compatibility), the section 8 key-family registry and Pebble settings, and the `RunCutover` mechanism;
- the ModifyBook migration with a row-version check (approved 2026-09-13);
- author and narrator as ordered credit lists (approved 2026-10-04);
- the Pebble-only activity backend in prod (2026-09-19).

## 1. Summary

- **`database.Store` keeps growing.**
  - It has 455 methods today, against 398 at the close of the August narrowing sweep (same AST instrument on both trees: 60 added, 3 removed).
  - One forcing function is the prod-only search decorator `indexedStore`. It embeds `Store`, so a capability an operation reaches through it has to be on `Store`. Assertions that pin a narrow interface to `Store` went from 3 to 14.
  - The store already has a post-commit `ChangeObserver` (`internal/database/change_observer.go`). Fix: drive all search indexing from it and delete the decorator (D1). Gate the flattened method count, because `interfacebloat` sees 6 entries, not 455 (D2).
- **Two invariants are kept by writer discipline, not by the data shape.**
  - "Exactly one primary per version group" depends on locks plus three repair ops. 10,780 groups once had two or more primaries. Fix: a version-group record (D6).
  - Full-row writes need the approved row-version check, which is still unbuilt. There are 10 `UpdateBook` call lines left (coordinator re-count; was 11). It should reuse the storage design's per-book version id inside the release B chokepoint (D5).
- **Readiness is invisible.**
  - The memdb warmup takes about 130 to 200 seconds. During it, 35 guarded read sites fall back silently to full scans.
  - `/health` does not report the warmup, ops do not wait for it, and systemd runs the service as `Type=simple`.
  - Fix: expose readiness, use `Type=notify`, and classify each startup step as fatal or degraded (D3, D11).
- **The code default for activity storage is the backend prod abandoned.** Empty `ACTIVITY_BACKEND` selects SQLite. NutsDB code is opened only by tests. Fix: flip the default and port the tests (D4); 01 deletes the dead code.
- **`internal/config` is not a leaf.**
  - It imports the store, auth and dedup, and 47 packages import it.
  - `AppConfig` is read directly at 636 sites.
  - The whole struct is persisted as one blob, so zeros override defaults; this is the incident that silently disabled chapter consolidation for 11 days.
  - Fix: a leaf config package, one declarative field table, sparse persistence and a direct-read ratchet (D8).
- **There is no enforced layering, and there are two god packages.**
  - `plugins/maintenance` is 78k lines with 103 op definitions and fan-out 57.
  - `internal/server` has fan-out 105.
  - Fix: depguard layering rules with a baseline ratchet, and a domain split of maintenance in the shape that 05's SDK sets (D9, D10). Splitting into multiple Go modules is rejected, because it needs the banned `go.work`.
- **The API contract is written by hand twice and checked nowhere.**
  - `api.ts` is 8,266 lines and has 260 types.
  - `openapi.json` has 305 operations against about 528 registered routes, and nothing reads it.
  - Fix: generate the TS types from the Go DTOs and add contract tests (D13).
- **Test speed is dominated by on-disk Pebble.**
  - 367 test call lines build on-disk stores.
  - The RAM disk measured 15.8 times faster for `internal/server`.
  - A 4,119-line hand-written `MockStore` ships in the production package beside the 32k-line generated mock.
  - Fix: one in-memory test-store helper, then fakes over mocks (D15).
- **CI on `main` is almost never green.**
  - Of the last 30 runs: 1 success, 9 failures, 20 cancelled.
  - The two-way ratchets fail `main` on an improvement (errcheck went from 779 to 770).
  - The GitHub and Woodpecker gate sets differ.
  - Fix: one gate manifest, and ratchets that fail only when the count goes up (D16).
- **Phasing:** gates first (all S), then reliability (S), then the approved data-model work, then modularity and the frontend. Section 4 has 28 plan rows: about 40 PRs once the per-package sweep (S1b) and the 8 maintenance-split PRs are counted.

## 2. Findings

Confidence: H is measured or compile-proven, M is a text grep or an inference from code reading, L is an estimate.

| ID | Finding | Evidence | Conf. | Impact | Decision |
|---|---|---|---|---|---|
| F01 | `database.Store` flattens to 455 methods; it was 398 on 2026-08-19 | The AST flattener run on HEAD and on `a0312c104` (A.3); `internal/database/store.go:46` | H | Every new store method is written three times (impl, `MockStore`, mockery). The interface-width gate is blind to it | D2 |
| F02 | The prod decorator promotes only `Store` methods, which is one forcing function on that growth. A store-side `ChangeObserver` already exists and covers the writes the decorator misses | `internal/server/indexed_store.go:41-54`; `internal/database/change_observer.go:8-36`; conformance assertions to `Store` went from 3 to 14 | H for the mechanism, M for its share of the growth | Assertions on capabilities fail only in prod; `AsCapability` has 189 call lines; two overlapping index mechanisms | D1 |
| F03 | The same grep for `database.Store` gives 9 lines on 2026-08-19 and 31 at HEAD. Classified: 11 conformance assertions, 13 wiring or unwrap lines, 2 test helpers, and about 5 genuine wide consumers | `grep -rn 'database\.Store\b' --include='*.go' internal cmd` minus tests, mocks and comment lines; the same with `git grep` on `a0312c104` | M | The growth is mostly decorator-driven assertions, not new wide consumers | D1, D2 |
| F04 | memdb fallback is silent; health and dispatcher ignore warmup | 35 `p.mem() != nil` guards; `IsMemReady` at `pebble_store.go:273` has callers only in `internal/server` and test helpers; `handlers/system/handler.go:169` | H | Ops run 100 times slower after a restart with no signal | D3 |
| F05 | The activity code default is SQLite while prod runs Pebble | `internal/activity/register.go:86-106`; memory note on the 2026-09-19 switch | H | Fresh installs and tests run the abandoned backend | D4 |
| F06 | NutsDB is linked but opened only by tests | `grep -rn 'NewNutsActivityStore('`: no non-test caller | H | Tests validate a store prod never uses | D4 |
| F07 | No row-version guard; 10 `UpdateBook` sites remain (coordinator re-count; was 11) | `grep -rn 'RowVersion\|StaleWrite' internal/database` returns nothing; A.6 site list | H | The approved lost-update fix is half done | D5 |
| F08 | No version-group record; the primary flag is tri-state, per book. Nil-as-not-primary reads went from 11 (`63a5eb807`, 2026-08-24) to 35, by the same grep | No `vg:`/`version_group:` key literal; `versionprimary/ensure.go:187-272` locks; 20 `EffectiveIsPrimaryVersion` calls; 407 `IsPrimaryVersion` lines | H for the structure and the counts, M for the classification of the 35 (most are election code) | The invariant broke at scale before (10,780 groups) | D6 |
| F09 | 236 key-prefix literals; 77 raw-KV calls outside `internal/database` | A.6 and D7 greps | M | No owner per family; schema docs are stale (SQLite and ULID text) | D7 |
| F10 | `internal/config` imports `database`, `auth`, `backup`, `dedup/unified` and others; fan-in 47 | `go list -f '{{join .Imports "\n"}}' ./internal/config` | H | Every config consumer depends on the data layer | D8, D9 |
| F11 | `AppConfig` is read directly at 636 sites in 204 files; only 4 `Mutate` callers | Greps (A.5); `config.go:1791-1816` | H | Races are tolerated by documentation, not by code | D8 |
| F12 | A config field is declared in up to 5 places; the whole struct is persisted | 250 `SetDefault`, 134 `BindEnv`, 133-case `applySetting` (`persistence.go:993`); `SaveConfigToDatabase` `persistence.go:1544` | H | The zero-overrides-default incident class (an 11-day silent disable) | D8 |
| F13 | `internal/metadata` imports `operations/registry` | `internal/metadata/enhanced.go:25` | H | A provider-client layer depends on the job runner | D9 |
| F14 | 37 non-test "import cycle" comments; scanner, organizer and metafetch are linked by server-set callbacks | `scanner/service.go:67,601`; `organizer/service.go:174-178`; `organizer/rename.go:35-39` | H | A missing hook is a silent no-op | D9, D12 |
| F15 | `plugins/maintenance`: 130 files, 78,102 lines, 103 `OperationDef` literals, fan-out 57 | A.1 and A.2 | H | Collision-prone; ops cannot be read or deleted alone | D10 |
| F16 | `internal/server` fan-out 105; `Server.Start` hand-starts 10 goroutines; 47 tickers in `internal/`; 4 container `Starter`s | `server_lifecycle.go:160-480` | H | Undeclared shutdown order; work outside the ops system (see 04) | D11 |
| F17 | The systemd unit is `Type=simple`; `Restart=on-failure` with `StartLimitBurst=5/60s` | `deploy/audiobook-organizer.service:55-59,83-84` | H | Readiness is found by grepping the journal; a fatal step means a crash loop (the 2026-10-03 outage) | D11 |
| F18 | `api.ts`: 8,266 lines, 314 exported functions, 260 exported types, hand-written | `web/src/services/api.ts` | H | Go JSON tag changes break the UI at runtime | D13 |
| F19 | `openapi.json`: 305 operations, hand-written, read by no script or test | `docs/api/openapi.json`; grep for consumers | H | A misleading contract document | D13 |
| F20 | Each Review lane caches server state its own way; the largest hook is 2,365 lines | `web/src/components/review/lanes/*.ts` | H | Four copies of fetch, stale and abort logic | D14 |
| F21 | 367 test lines build on-disk Pebble stores; 242 use in-memory | Greps (D15) | H | Write-bound tests; 15.8 times faster on a RAM disk (`Makefile:233-239`) | D15 |
| F22 | The hand-written `MockStore` (4,119 lines, header v1.139.0) is in a non-test file, used by 239 test files | `internal/database/mock_store.go` | H | It ships in the prod binary, and a zero-value 455-method mock hides wiring errors | D15 |
| F23 | `main` CI: 1 of the last 30 runs succeeded; the latest failed on errcheck going down and on interface width | `gh run list` / `gh run view 37843473291` | H | Red carries no information; merges rely on Woodpecker, which lacks these gates | D16 |
| F24 | The Go version is pinned in at least 12 tracked files; the mockery pin is v3.8.0 and the playbook memory's v3.7.1 is stale | `grep -rln 'go1\.27\.1'`; `scripts/setup-mockery.sh:8` | H | Upgrades are a hunt | D17 |
| F25 | Database docs are stale: `database-architecture.md` lists `SQLiteStore`; `AI-REFERENCE.md` says Go 1.24 and React 18 and lists `internal/server/config_update_service.go`, which does not exist (it is `internal/config/update_service.go`) | The files themselves; `go.mod` says `go 1.27.0`; `web/package.json` has React ^19.3.0 | H | New contributors are misled | D7 |
| F26 | The latest `main` CI job times: Go short tests 19.1 min, Coverage Floor 16.9 min, Fixture shards 6.3 to 8.6 min | `gh run view 37843473291 --json jobs` | H | Two jobs run the same suite twice | D15, D16 |
| F27 | Short tests without race on the Mac: `plugins/maintenance` 578 s, `server` 414 s; `scanner` hit the 10-minute default timeout | A.7 | H for the times, L for the scanner cause (not diagnosed) | The largest package is also the slowest; package size drives test latency | D10, D15 |

## 3. Proposed specification

### 3.1 Data layer contract (D1, D2, D5, D6, D7)

1. **The existing `database.ChangeObserver` is the only index trigger.**
   - Its hooks (`BooksChanged`, `BooksNeedReindex`, `AuthorRenamed`, `SeriesRenamed`) already fire post-commit at the memdb write-through points.
   - `searchChangeObserver.BooksChanged` starts enqueuing reindex or delete work; the decision is made off the hook goroutine, because hooks can fire under store locks.
   - `indexedStore` is deleted.
   - When release B's P1 book chokepoint lands (storage design section 4.5), the hook moves to after its single batch commit.
   - Per-path tests cover the five former decorator writers, the `BooksNeedReindex` sources (tags, aggregates, credits), the rename fan-outs and the coverage repair. A failed commit fires nothing.
2. **`database.Store` is a wiring type only.**
   - Packages outside the composition root (`cmd/`, `internal/server/wire*.go`, the `serviceregistry` builders) depend on role interfaces or on consumer-declared interfaces.
   - A ratchet gates two numbers: the flattened method count (baseline 455, may only fall) and the count of consumer references (baseline 31).
3. **The row-version check (approved), built on the storage design's version id.**
   - It adds no new field. A caller of `UpdateBook` presents the per-book version id it read (storage design section 4.2).
   - The release B chokepoint already reads the newest id under the stripe (section 4.5, step 1). It refuses a mismatch with `ErrStaleWrite`.
   - `ModifyBook` retries once.
   - The release B brief settles writes that touch only unversioned keys, which do not move the id.
4. **A version-group record (owner Q1).**
   - Key: `version_group:<id>`, holding `{primary_book_id, member_ids, rev}`.
   - It has one chokepoint. The book's `IsPrimaryVersion` is derived on read and is no longer stored as truth.
   - The conversion uses the approved cut-over. Groups with zero or several primaries go to the owner as a list.
   - `member_ids` holds live books only, which is consistent with section 6 of the storage design: group members are not archive-eligible, and a retired book leaves its group, handing off primary if needed, before it can be archived.
5. **Key-family ownership.**
   - Every family in the section 8 registry names one owning package.
   - A grep ratchet fails raw-KV calls on a family from any other package.
   - `docs/database-pebble-schema.md` and `docs/database-architecture.md` are generated from the registry.

### 3.2 Target layering, enforced by depguard (D9)

Layers, low to high. A package may import only its own layer or lower ones. The exceptions are listed in `.layering-baseline` and may only shrink.

| Layer | Packages (initial assignment) | May import |
|---|---|---|
| L0 leaf | `util`, `pathutil`, `personname`, `titleutil`, `authorname`, `audioext`, `httputil`, `logger`, `logging`, `metrics`, `cache`, `models`, `seqnum`, `querygrammar` | std lib and third-party only |
| L1 config | `config` (leaf after D8) | L0 |
| L2 storage | `database`, `openlibrary` store, `search` index | L0, L1. **Not** domain packages |
| L3 domain | `merge`, `versionprimary`, `versions`, `dedup`, `matcher`, `fingerprint`, `metadata` (provider clients), `organizer`, `scanner`, `itunes`, `audiobooks`, `metafetch`, `reconcile`, `repairs`, `undo`, … | L0 to L2 and other L3. **Not** `operations/*` |
| L4 jobs | `operations/registry`, `scheduler`, `plugins/*`, `maintenance/jobs` | L0 to L3 |
| L5 transport | `server`, `server/handlers/*`, `realtime`, `syncapi` | L0 to L4 |
| L6 entry | `cmd/*`, `tools/cmd/*` | anything |

Notes on the table:

- `internal/writeback/` is classed L3 and is not modified. The rule only reads its imports.
- Domain packages that `database` imports today (`matcher`, `fingerprint`, `chaptershape`, `metastate`, `syncapi/progress`) are either reclassified to L0, if they turn out to be pure, or entered in the baseline. The G2 PR decides each one with `go list` evidence.
- This layering fits the proposed pluggable-media-platform record (`docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md`, decisions 2, 3 and 6: compiled-in modules and capability adapters). It does not depend on that record being approved.

### 3.3 Config (D8)

- `internal/config` holds the `Config` struct, the declarative field table (`fields.go`) and `Snapshot`/`Mutate`. It imports nothing in the module.
- Each field-table entry has: key, Go path, type, default, env name, `secret`, `restart_required`, `kill_switch`, validation, and the owner-gated marker.
- `viper.SetDefault`, `BindEnv` and the DB apply are driven from this table. The 133-case `switch` is deleted.
- **Persistence:** one settings row per changed key (`config:<key>`). The blob is converted at startup (keys equal to the current default are dropped), then deleted.
- `GET /api/v1/config/fields` serves the table, so the Settings page and the kill-switch list come from one source.

### 3.4 Lifecycle and readiness (D3, D11)

- **Startup phases, in order:**
  1. open the store (refuse on a format mismatch, as release A already does);
  2. cut over;
  3. load config;
  4. start the container;
  5. bind the listener;
  6. become ready.
- Each step is tagged `fatal` or `degraded`. Degraded failures are listed in `/health` under `degraded: [...]`.
- `/health` adds `memdb_ready`, `warmup_ms` and `ready`.
- A counter, `memdb_fallback_reads_total{method}`, counts every fallback read.
- The service sends `READY=1` (systemd `Type=notify`) once the listener is up and the memdb is ready. Deploy waits on `systemctl is-active`.
- Every background loop is either a container service with `Stop`, or an operation or schedule. 04 and 05 decide which.

### 3.5 Frontend contract (D13, D14)

- **Go DTO structs are the source.** The generator (06 picks it) emits `web/src/types/generated/*.ts`.
  - `api.ts` is split into `web/src/services/api/<domain>.ts`, and each module imports the generated types.
- **Contract test.** Go handler tests write golden JSON for each DTO into `testdata/contract/`, and a Vitest test type-checks the golden files against the generated types.
  - The ABS and AudioBooth responses get the same golden treatment.
- **Server state.** Server state goes through one library. Lane descriptors stay, and a lane hook keeps only lane logic.

### 3.6 Tests and CI (D15, D16, D17)

- **Test stores.** `dbtest.NewStore(t, opts...)` defaults to in-memory vfs, with `dbtest.OnDisk()` for path-dependent tests.
  - `MockStore` moves to `internal/database/dbtest/mockstore` (test-only import) and shrinks as tests move to the real in-memory store.
- **One gate list.** `ci/gates.txt` is the single list of gates. `make ci`, the Woodpecker files and `ci.yml` read it.
- **Ratchets.** A ratchet fails a PR only when its count rises. A scheduled job opens a PR to lower a baseline after an improvement lands.
  - This needs owner sign-off, because the two-way design was deliberate (Q7).
- **Go version.** A `GO_VERSION` file is the single source of the Go pin.

## 4. Implementation plan

Size key: S is at most about 300 changed lines or mechanical work. M is one subsystem. L spans several subsystems.

Every PR adds a `changelog.d/` fragment (without a header) and bumps the file headers it touches.

### Phase 0: gates (land first; no runtime change)

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-G1 | Flattened `Store` method ratchet and consumer-reference ratchet | `tools/cmd/storewidth/main.go` (new), `scripts/check-interface-width.sh`, `.store-width-baseline` (new), `Makefile`, `.github/workflows/ci.yml`, `.woodpecker/checks-lint.yaml` | A mutation check: plant a method and expect a failure; remove one and expect a failure until the baseline is lowered | Revert the PR | S |
| 07-G2 | depguard layering rules plus a baseline | `.golangci.yml`, `.layering-baseline` (new), `scripts/check-layering.sh` (new), `Makefile`, `.github/workflows/ci.yml`, `.woodpecker/checks-lint.yaml`, `docs/architecture/layering.md` (new) | A planted `config` to `server` import fails | Revert | S |
| 07-G3 | Ratchet on direct `config.AppConfig.` reads (baseline 636) | `scripts/check-appconfig-reads.sh` (new), `.appconfig-read-baseline` (new), `Makefile`, `.github/workflows/ci.yml` | A planted read fails | Revert | S |
| 07-G4 | One gate manifest; ratchets fail only upward; a scheduled lowering job (needs Q7) | `ci/gates.txt` (new), `Makefile` (`ci`), `.woodpecker/checks-lint.yaml`, `.woodpecker/checks-build.yaml`, `.github/workflows/ci.yml`, `scripts/check-interface-width.sh`, `scripts/check-errcheck-ratchet.sh`, `.github/workflows/ratchet-lower.yml` (new) | Compare the runner gate lists against the manifest in CI | Revert | M |

### Phase 1: reliability (small, independent)

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-R1 | The activity default becomes `pebble`; the activity tests are ported off NutsDB | `internal/activity/register.go`, `internal/config/config.go` (default), `internal/activity/writer_test.go`, `internal/activity/service_test.go`, `internal/activity/writer_attrs_test.go`, `docs/configuration.md` | The existing activity suite on the Pebble store; a boot test with empty config logs "Pebble-only" | Set `ACTIVITY_BACKEND=sqlite` | S |
| 07-R2 | `/health` adds `memdb_ready`, `warmup_ms`, `ready` and `degraded[]`; add the fallback-read counter | `internal/server/handlers/system/handler.go`, `internal/database/pebble_store.go`, `internal/database/memdb_reads.go`, `internal/metrics/` (one file), `internal/server/server_lifecycle.go` | Handler tests before and after warmup; counter increments on the fallback path | Revert (additive fields) | S |
| 07-R3 | Startup step classification table; degraded steps never return an error from `serve` | `cmd/root.go`, `internal/server/server_lifecycle.go`, `internal/server/startup_steps.go` (new) | Table test: each degraded step with an injected failure still serves | Revert | S |
| 07-R4 | `Type=notify` plus `READY=1`; deploy waits on readiness | `deploy/audiobook-organizer.service`, `internal/server/sdnotify.go` (new), `internal/server/server_lifecycle.go`, `scripts/deploy-preflight.sh`, `Makefile` (deploy wait) | A unit test with a fake `NOTIFY_SOCKET` | Revert the unit to `Type=simple` (the owner installs the unit) | S |

### Phase 2: data model (approved items first)

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-S1 | `dbtest.NewStore(t)`, in-memory by default | `internal/database/dbtest/store.go` (new), `internal/database/dbtest/store_test.go` (new) | Helper tests | Revert | S |
| 07-S1b..n | Mechanical sweep of the 367 on-disk constructions, one PR per package group | The test files that `grep -rln 'NewPebbleStore(' --include='*_test.go' internal cmd` lists, grouped: `internal/server/**`; `internal/database`; `internal/plugins/**`; the rest | The package suites; record wall time before and after | Revert the per-package PR | M (parallel sweep) |
| 07-S2 | **Approved:** `UpdateBook` stale-write check, using the section 4.2 version id inside the release B chokepoint (**lands with or after release B**) | `internal/database/pebble_store.go` (the chokepoint), `internal/database/store.go` (`UpdateBook` signature or option), `internal/database/mock_store.go`, `internal/database/mocks/mock_store.go` (regenerate), the 11 sites: `internal/database/migrations.go`, `internal/merge/combine_journal.go`, `internal/organizer/service.go`, `internal/organizer/move.go`, `internal/organizer/rename.go`, `internal/server/indexed_store.go`, `internal/server/handlers/organize.go`, `internal/dedup/book_dedup.go` | Race test with two concurrent stale writes; one wins and one gets `ErrStaleWrite` | Revert (the field stays, unused) | M |
| 07-S3 | Index from `ChangeObserver.BooksChanged`; delete `indexedStore` (**independent of release B**) | `internal/server/search_result_cache.go` (`searchChangeObserver`), `internal/server/indexed_store.go` (delete; keep `enqueueIndex` and the worker in a new `internal/server/search_index_queue.go`), `internal/server/server.go` (`OpsStore`), `internal/server/server_lifecycle.go`, the 14 `= database.Store(nil)` assertion files listed in B-D2, and a new `internal/server/search_observer_paths_test.go` | A per-path test that each write path reaches the index queue; a failed commit fires nothing; the existing search coverage and reconciler tests | Revert (the decorator comes back) | M |
| 07-S4 | Key-family owner field plus a raw-KV ownership ratchet; generated schema docs (**after release A's registry**) | the release A registry file, `scripts/check-key-family-owners.sh` (new), `docs/database-pebble-schema.md`, `docs/database-architecture.md` | Ratchet mutation check | Revert | S |
| 07-S4b | Move `internal/merge`'s 23 raw-KV calls behind owner helpers | `internal/merge/*.go` (the files that `grep -lE '\.(SetRaw|GetRaw|ScanPrefix|DeleteRaw)' internal/merge` lists), `internal/database/` (helpers) | The merge suite | Revert | M |
| 07-S6 | *(Coordinator-added, from 01 M6.)* `Book.FilePath` → `book_file`: add one helper, "the book's real files" (primary/active), built on `GetBookFilesForIDsCore`. Then sweep the read sites one package per PR, largest first: `plugins/maintenance` 63, `organizer` 53, `metafetch` 53, `database` 40, `scanner` 37, `audiobooks` 34. Stop the 67 writes last, in a storage cut-over. | `internal/database/book_files_helper.go` (new), `internal/database/store.go`; then per-package read sites (01 appendix A census) | Helper tests on multi-file, missing-file and version-group books; per package, the existing suites plus one test that the stale `FilePath` is never read | Revert per package | L in total (helper S, about 8 sweep PRs of size M) |
| 07-S5 | **Needs Q1:** version-group record plus converter plus a derived flag | `internal/database/version_group_store.go` (new), `internal/database/store.go`, the release B/C cut-over converter file, `internal/versionprimary/ensure.go`, `internal/versionprimary/rank.go`, `internal/versionprimary/incumbent.go`, the reader sites of `IsPrimaryVersion` (one accessor) | Converter verification (P4: verify, then purge); invariant test that exactly one primary exists per group | The pre-migration checkpoint (P3, Q6 window) | L |

### Phase 3: modularity

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-M1 | Make config a leaf: move DB persistence and update into `internal/config/configstore` | `internal/config/persistence.go`, `internal/config/update_service.go`, `internal/config/register.go`, `internal/config/ai_endpoints.go` → `internal/config/configstore/*`; callers `cmd/root.go`, `cmd/child_mode.go`, `internal/server/server.go`, `internal/server/wire_handlers.go`, `internal/server/registry_wire.go`, `internal/server/handlers/system/handler.go`, `internal/server/handlers/ai.go`, `internal/server/handlers/scheduler_admin.go` (list by `grep -rln "config\.\(Save\|Load\)ConfigToDatabase\|UpdateService" internal cmd`) | `go list` shows no module imports in `internal/config`; the config suite | Revert | M |
| 07-M2 | Declarative field table; table-driven defaults, env and DB apply; delete the `applySetting` switch | `internal/config/fields.go` (new), `internal/config/config.go`, `internal/config/configstore/persistence.go` | Round-trip test for every field (default, env, DB); a missing table entry fails the build test | Revert | M |
| 07-M3 | Sparse config persistence plus a startup converter from the blob | `internal/config/configstore/persistence.go`, `internal/config/configstore/convert.go` (new) | Converter test: a blob with zeros gives no rows for default-equal keys | Restore the `config_blob` row from the backup (kept until the next release) | M |
| 07-M4 | Remove `metadata` → `operations/registry` | `internal/metadata/enhanced.go`, plus the caller that supplies the reporter | Build plus the layering ratchet goes down by one | Revert | S |
| 07-M5 | Pipeline hooks become constructor parameters | `internal/scanner/service.go`, `internal/scanner/ai_parse_async.go`, `internal/organizer/service.go`, `internal/organizer/rename.go`, `internal/server/wire_handlers.go` (and the wiring file that sets the hooks) | Constructing without a hook fails to compile; the existing scan and organize tests | Revert | M |
| 07-M6..M13 | Split `plugins/maintenance` into domain subpackages (`authors`, `versions`, `fragments`, `files`, `itunesread`, `metadata`, `activity`, `reports`), one PR each, **in 05's SDK shape** | `internal/plugins/maintenance/<prefix>_*.go` → `internal/plugins/maintenance/<domain>/`; `internal/plugins/maintenance/register.go`, `internal/plugins/maintenance/plugin.go` | A test asserts the registered op-ID set and ConcurrencyKeys are unchanged before and after | Revert the per-domain PR | M each |
| 07-M14 | Retire `database.Store` as a consumer type (**after S3**) | The about 5 genuine wide consumers (B-D2: `plugins/maintenance/deps.go`, `server/provider_throttle_wire.go`, `server/catalog_harvest_op.go`, `cmd/root.go`) and the `AsCapability` sites that become direct calls | The flattened ratchet and consumer-reference ratchet fall | Revert per package | M |

### Phase 4: frontend

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-F1 | Generate TS types from the Go DTOs, plus contract golden tests | the generator config (06's choice), `Makefile` (`gen-types`), `web/src/types/generated/*` (new), `internal/server/handlers/**/testdata/contract/*.json` (new), `web/src/services/contract.test.ts` (new) | Golden JSON type-checks; CI fails on a stale generation | Revert | M |
| 07-F2 | Split `api.ts` by domain | `web/src/services/api.ts` → `web/src/services/api/{books,review,operations,dedup,system,itunes,activity}.ts`, `web/src/services/api/index.ts` (re-export) | The existing `api.*.test.ts` suites | Revert | M |
| 07-F3 | Server-state library pilot on the Repairs lane | `web/package.json`, `web/src/main.tsx` (provider), `web/src/components/review/lanes/useRepairsLane.ts`, its tests | The lane suite; record lines removed | Revert | M |
| 07-F4 | **Needs Q6:** delete `docs/api/openapi.json`, or generate it | `docs/api/openapi.json` | — | Revert | S |

### Phase 5: upgradeability

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-U1 | `GO_VERSION` single source | `GO_VERSION` (new), `Makefile`, `.envrc`, `.woodpecker/*.yaml` (six files), `.github/workflows/ci.yml`, both Dockerfiles, `CLAUDE.md` | The CI step compares `go.mod` and `GO_VERSION` | Revert | S |
| 07-U2 | ABS / AudioBooth golden response fixtures | `internal/server/handlers/abs/testdata/golden/*.json` (new), `internal/server/handlers/abs/golden_test.go` (new) | Golden diff | Revert | S |

**Order and collisions.**

- G1 to G3 can run in parallel.
- G4 waits for Q7.
- R1 to R4 are independent.
- S2 waits for storage release B. S4 waits for release A's registry. S3 is independent of the storage releases.
- M6 to M13 wait for 05's SDK spec.
- M14 waits for S3.
- **Shared-file collisions:**
  - `internal/server/server_lifecycle.go`: R2, R3, R4, S3.
  - `.github/workflows/ci.yml`: G1 to G4, U1.
  - `internal/database/pebble_store.go`: R2, S2, and storage release B.

  Sequence them in the coordinator's matrix (08).

## 5. Risks and what must not break

- **`internal/writeback/` and iTunes.** Nothing here edits `internal/writeback/` or writes to iTunes. The Book facet idea (D5b) excludes every iTunes field until the owner says otherwise. The maintenance split moves only read-only iTunes repair files, into `itunesread`.
- **book_file rows.** No PR deletes `book_file` rows. S5 touches only book rows and the new group record; anomalies are repointed or listed, never deleted.
- **The scan ConcurrencyKey.** It stays one key. M6 to M13 assert the ConcurrencyKey set is unchanged.
- **The search index (S3).** The index must not miss writes after the decorator is gone. The per-path test, which covers every current caller of `enqueueIndex` and `markIndexDirty` including the rename fan-out, plus the existing dirty-set reconciler, cover it. Keep the reconciler.
- **The stale-write check (S2).** It can surface stale writers as errors in prod. Ship it with an `ErrStaleWrite` counter and a log line. The approved rule is to refuse, so do not downgrade it to a warning. It must not add a version mechanism beside the storage design's version id (section 4.2).
- **The activity default flip (R1).** It changes nothing in prod, which already runs Pebble. Rollback is the environment variable.
- **`Type=notify` (R4).** If `READY=1` is never sent, systemd kills the service at `TimeoutStartSec`. Set `TimeoutStartSec` above the measured warmup (300 seconds) until release C shortens it.
- **Config conversion (M3).** A wrong default comparison could drop a deliberate override that equals today's default. That is harmless by definition: the value is the same. The conversion is logged key by key.
- **Every count here is from HEAD on 2026-10-08.** Re-run the A-appendix commands before each PR.

## 6. Dependencies on other workstreams

- **05 (operations v3):**
  - the dispatcher gate on `memdb_ready` (D3);
  - the op-package shape for the maintenance split (M6 to M13);
  - where background loops go (D11).

  I assume the SDK keeps op IDs and ConcurrencyKeys stable.
- **04 (operations census):** which of the 47 tickers and 10 hand-started goroutines become operations (D11).
- **02 (search and identification):**
  - S3 changes how Bleve learns about writes: from the decorator to the existing `ChangeObserver`;
  - D12's orchestrator choice belongs to 02 and 05.
- **01 (dead code):**
  - deletes the NutsDB and SQLite activity stores after R1;
  - deletes `MockStore` once D15(c) lands;
  - the `openapi.json` deletion if Q6 says delete.
- **06 (bleeding edge):**
  - the generator choice for F1;
  - the server-state library for F3;
  - `go.mod` `tool` directives (D17).
- **03 (dedup page retirement):** none directly. The lane pattern D14 applies to its parity work.
- **Storage efficiency releases A, B and C (approved, outside this charter):** S2 lands inside B's chokepoint; S4 needs A; D3's final choice waits on C; S5 must agree with the section 6 archive rules.

## 7. Open questions for the owner

Each question carries a recommended answer.

1. **Q1. Should the version group become its own record holding the primary, replacing the per-book flag?**
   - **Recommended: yes (D6, PR 07-S5),** converted in a storage cut-over window, with zero- or multi-primary groups listed for your ruling.
   - The cheaper half-step is to make the flag non-nullable in the same cut-over.
2. **Q2. Should search indexing run entirely off the store's existing `ChangeObserver`, so the `indexedStore` decorator can be deleted?**
   - **Recommended: yes (D1, S3).** It removes the main reason methods are forced onto `database.Store`. The D2 ratchet (G1) caps growth either way.
3. **Q3. Should the server report readiness to systemd (`Type=notify`) and in `/health`, and should ops wait for the warm index?**
   - **Recommended: yes (R2, R4, and 05's gate).**
   - You install the unit change, because the deploy user cannot.
4. **Q4. Should config persistence store only changed keys instead of the whole struct?**
   - **Recommended: yes (M3).** It ends the "a saved zero beats the default" class of silent disables.
5. **Q5. Should `plugins/maintenance` be split by domain once 05's SDK is set?**
   - **Recommended: yes (M6 to M13),** one domain per PR, with op IDs unchanged.
6. **Q6. What should happen to `docs/api/openapi.json`: delete it, or generate it from code?**
   - **Recommended: delete it now (F4), and generate it later if a consumer appears.** A hand-written spec covering about 58% of routes misleads more than it helps.
7. **Q7. Should the two-way ratchets become fail-on-rise only, with a bot PR to lower the baseline?**
   - **Recommended: yes (G4).** `main` failed on an improvement today, 779 to 770 errcheck findings. Of its last 30 runs, 1 succeeded, 9 failed and 20 were cancelled; the cancellations are most likely superseded runs.
8. **Q8. Should a server-state library be adopted for the Review lanes, starting with the Repairs lane as a pilot?**
   - **Recommended: yes (F3).** Keep it only if the pilot removes code and does not slow the tests.
