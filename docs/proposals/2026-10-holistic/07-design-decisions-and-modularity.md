<!-- file: docs/proposals/2026-10-holistic/07-design-decisions-and-modularity.md -->
<!-- version: 1.2.1 -->
<!-- guid: 4ee6ac26-168e-466d-8c91-11d955e1487d -->
<!-- last-edited: 2026-10-09 -->

# 07. Design decisions and modularity

- **Analyst:** `design`
- **HEAD:** `f7211eb39` (2026-10-08)
- **Status:** planning only; no code is changed.
- **Appendices:**
  - [A-measurements.md](07-design-decisions-and-modularity/A-measurements.md): every number in this doc and the command behind it.
  - [B-decisions.md](07-design-decisions-and-modularity/B-decisions.md): the decision register, D1 to D17. Each entry gives the current choice and why, the cost, the alternatives, the recommendation and the migration effort.
  - [C-server-state-library.md](07-design-decisions-and-modularity/C-server-state-library.md): the D48 pilot choice (TanStack Query v5), with the lanes' current server-state code measured, the candidates compared, and the pilot spec and acceptance bar.

### Round-2 review (r4, 2026-10-09)

Re-checked in the `aorg-review2` worktree at `ebda30d47`. Changes made in this pass:

- **455 re-measured** with a 60-line `go/ast` flattener (now printed in A.3, so the number is reproducible): `Store` 455, `catalogStore` 130, `operationsStore` 97, `mediaStore` 77, `enrichmentStore` 65, `accountStore` 44, `platformStore` 42; no role interface embeds a type from another package. The count stands.
- **New: a wave-0 CI-throughput set, 07-C1 to C4 (§4, Phase 0a), placed before G4.** Measured: the last 12 `ci.yml` runs all failed or were cancelled, including two docs-only PRs; a PR run is 22–24 min wall with 16 jobs, a `main` run 39–49 min because `auto-revert.yml` re-runs the failed jobs once ("one free re-run") before deciding; a docs-only PR fails on four causes that are all inherited from `main` (one unformatted test file, a two-way slog ratchet inside `go test`, errcheck down 779→770, one `interfacebloat` breach). 08's X4 covers two of the four; C1 absorbs X4. §6 asks 08 to move C1–C3 to the front of wave 0.
- **Simpler enforcement for G1–G3:** the three ratchets become Go tests (`go test ./...` runs on every runner already, so the "one gate list" goal is met for them without a manifest edit), modelled on the existing `internal/logger/slog_guard_test.go`; depguard and `.golangci.yml` are not needed for the three edges that matter today. §3.2 and the G rows updated.
- **D42–D51 mapped to rows:** S5 is now "D42, approved" (was "needs Q1"); S3 cites D43; R4 cites D44; M3 cites D45; F4 is "delete" per D46 with its exact files, and F1 names the generator (`tygo` v0.2.21, Go-side, `tool` directive); G4 cites D47 and gains the in-test slog ratchet; M6–M13 are cut and replaced by a pointer to 05 waves 12A–12E (D49); S6 cites D51; F3 points to appendix C (D48).
- **Duplicates cut:** R1 (now 01 P1/P2 per the coordinator note), M6–M13 (05), and the `openapi.json` remark in §6 (D46 settles it).
- **S2's site count corrected** to the coordinator's 10 (the row said "the 11 sites" while listing files).
- **Not changed:** the findings table beyond F23's cross-reference, the data-layer and config specs, the phase order for Phases 1–5.

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
- **Phasing:** *(r4)* CI throughput first (C1–C4: `main` green on its four inherited causes, path filters, one sharded short-test run; PR run 22–24 min → under 10 min), then gates (all S, as Go tests), then reliability (S), then the approved data-model work, then modularity and the frontend. Section 4 has 24 plan rows after cutting R1 (01's) and M6–M13 (05's): about 32 PRs once the per-package sweep (S1b, 4 groups) and the S6 sweeps (8) are counted.

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
| F23 | `main` CI: 1 of the last 30 runs succeeded; the latest failed on errcheck going down and on interface width. *(r4, 2026-10-09)* The last 12 runs: 0 successes; the 2026-10-09 docs-only PR run (`37865893192`, 22 min) failed on **four** inherited causes: `make fmt-check` (`internal/database/ops_v2_recent_test.go`, unformatted since `300d26dd3`), `TestGuard_NoDirectSlogCalls` (`internal/logger/slog_guard_test.go`, a two-way per-file ratchet: 10 files over or missing, 1 stale entry), errcheck 779→770, `interfacebloat` at `internal/database/iface_bookfile.go:54` (9 methods). Measurements in A.9 | `gh run list --workflow ci.yml --limit 12`; `gh run view 37865893192 --log-failed` | H | Red carries no information; merges rely on Woodpecker, which lacks these gates; every one of the roadmap's PRs pays 22 min and lands red | D16, C1–C4 |
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

**Simpler enforcement (r4).** The table above is the target; the mechanism does
not need depguard, a `.golangci.yml` change, a new CI job or a baseline file:

- `internal/arch/layering_test.go` (new, test-only) loads the module's import
  graph with `golang.org/x/tools/go/packages` (already an indirect dependency
  through the mocks tooling; else `go list -json` through `os/exec`) and checks
  one Go map `layerOf[pkg] = n` against the rule "a package imports only
  packages with `n` at most its own". The map is the table above, written once.
- Known violations are a second map, `allowed = map[edge]string{...: "reason"}`,
  that **may only shrink**: the test fails when an edge in `allowed` no longer
  exists, so a fixed edge is removed from the map in the PR that fixes it. That
  is the one-way ratchet D47 wants, with no bot.
- It runs in `go test ./...`, which every runner (GitHub, Woodpecker, `make ci`)
  already executes, so it is enforced everywhere on the day it lands.
- The pattern exists in the repo: `internal/logger/slog_guard_test.go` is a
  source-scanning guard test. The difference here is the one-way rule (its
  ratchet fails in both directions; see C1).
- G1 (store width) and G3 (`AppConfig` reads) take the same shape: a
  `_test.go` with a `const baseline = 455` (or 636) that the count may not
  exceed, lowered by hand in the PR that lowers the count. G4's manifest is then
  needed only for the gates that cannot be tests (coverage floor, errcheck,
  gofmt, mock freshness).

depguard stays as the fallback if the test turns out to be slow (the import
graph for 184 packages loads in a few seconds with `NeedImports` only).

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

- **Go DTO structs are the source.** The generator is **`tygo`** (`github.com/gzuidhof/tygo`, latest v0.2.21 by `go list -m -versions` on 2026-10-09; r4): it reads Go structs and `json` tags directly, needs no annotations or OpenAPI step, and is pinned as a `go.mod` `tool` directive so `go tool tygo generate` runs from the pinned toolchain with no separate install. Config in `tygo.yaml` at the repo root; output `web/src/types/generated/<package>.ts`, one file per Go package listed in the config. A CI step runs the generator and fails on a dirty tree (the same shape as Mock Freshness).
  - `api.ts` is split into `web/src/services/api/<domain>.ts`, and each module imports the generated types.
- **Contract test.** Go handler tests write golden JSON for each DTO into `testdata/contract/`, and a Vitest test type-checks the golden files against the generated types.
  - The ABS and AudioBooth responses get the same golden treatment.
- **`docs/api/openapi.json` is deleted (D46).** No script, test or build step reads it (F19); the only non-archived references are `CHANGELOG.md`, `TODO.md` and `.claude/skills/api-doc/SKILL.md`, which is updated to point at the generated types. If a consumer appears later, OpenAPI is generated from the same DTOs, never hand-written again.
- **Server state.** Server state goes through one library: **TanStack Query v5**, chosen in [appendix C](07-design-decisions-and-modularity/C-server-state-library.md) (D48). Lane descriptors stay, and a lane hook keeps only lane logic. Appendix C has the pilot spec, the acceptance bar (at least 150 of the Repairs lane's 215 server-state lines removed; 22 tests still green) and the follow-on order.

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

### Phase 0a: CI throughput (r4; lands before everything else in wave 0)

**Why first.** The roadmap plans about 191 PRs. Measured on 2026-10-09 (A.9):
a PR run of `ci.yml` is 22–24 min wall across 16 jobs; a `main` run is 39–49 min
because `auto-revert.yml` grants one free re-run of the failed jobs before it
decides, and `main` fails deterministically today, so every merge pays twice.
The last 12 runs, two of them docs-only, all failed or were cancelled. At those
numbers the program's critical path is CI time: 191 × 22 min ≈ 70 h of PR runs
plus about 45 min per merge, all landing red, with Woodpecker also serialized
(one run at a time, `docs/ci/woodpecker.md`). The target after C1–C3: a
docs-only PR runs nothing but the header and fragment lints (under 2 min); a
Go PR runs in **under 10 min** wall; a merge to `main` costs one run, not two.

| PR | Change | Files | Tests / verification | Rollback | Size |
|---|---|---|---|---|---|
| 07-C1 | **`main` green on all four inherited causes** (absorbs 08's X4, which covered two). (1) `gofmt -w internal/database/ops_v2_recent_test.go` (unformatted since `300d26dd3`; `gofmt -l` lists it locally too, so it is not a version drift). (2) The slog ratchet: fix or ratchet-list the 10 files `TestGuard_NoDirectSlogCalls` reports and drop the stale `library_watcher.go` entry; make that test **one-way** (a file below its allowance passes; the entry is lowered by hand). (3) `.errcheck-baseline` 779 → the measured count. (4) `internal/database/iface_bookfile.go:54` (`BookFileUpserter`, 9 methods): split the one method out or set `.interface-width-baseline` to 1 with a comment; 08 X4 chose "fix the breach". Also fix the gate scripts themselves to fail only on a rise (D47), so the next improvement does not re-redden `main` | `internal/database/ops_v2_recent_test.go`, `internal/logger/slog_guard_ratchet_test.go`, `internal/logger/slog_guard_test.go` (one-way), the 10 files it names (`internal/audiobooks/filter_compiled.go`, `internal/config/update_service.go`, `internal/database/book_listing_fields.go`, `internal/database/soft_deleted_count.go`, `internal/server/apikey_expiry_stamp.go`, `internal/server/candidate_feedback.go`, `internal/server/handlers/metadata_cache.go`, `internal/server/middleware/credential_guard.go`, `internal/server/middleware/owner_guard.go`, `internal/itunes/library_watcher.go` entry only), `.errcheck-baseline`, `scripts/check-errcheck-ratchet.sh`, `.interface-width-baseline`, `scripts/check-interface-width.sh`, `internal/database/iface_bookfile.go` | `make fmt-check`; `go test ./internal/logger/`; `make lint-errcheck-ratchet`; `scripts/check-interface-width.sh`; then one full `ci.yml` run on the PR is green, and the `main` run after merge is green **on attempt 1** (no re-run in `auto-revert.yml`'s log) | Revert | S |
| 07-C2 | **Path filtering.** `ci.yml` `on.push.paths-ignore` and `on.pull_request.paths-ignore`: `docs/**`, `**.md`, `changelog.d/**`, `todo.d/**`, `.claude/**`, `agents/**`, `skills/**`. A docs-only PR then runs only `changelog-check.yml` and the TODO fragment header lint (moved into its own 1-minute workflow, since `ci.yml` no longer runs for it). Inside `ci.yml`, a first job `changes` (a 15-line `git diff --name-only origin/main...HEAD` step; no third-party action) outputs `go=true/false` and `web=true/false`; every project job gets `if: needs.changes.outputs.go == 'true'`, and the reusable `ci` job receives `run-frontend: ${{ needs.changes.outputs.web == 'true' }}`. The reusable workflow has no input to skip its Go jobs (`reusable-ci-minimal.yml` inputs: `go-version`, `node-version`, `go-experiment`, `system-packages`, `frontend-working-dir`, `run-frontend`, `go-lint`, `golangci-lint-version`, `golangci-lint-args`, `super-linter`; read via `gh api` on 2026-10-09), so a web-only PR still pays its Go vet, build, lint and test jobs until C3 | `.github/workflows/ci.yml`, `.github/workflows/todo-header-lint.yml` (new, from the job at `ci.yml:408-427`), `.github/workflows/auto-revert.yml` (it must treat "no run" on a docs-only push as not-a-failure: `workflow_run` never fires, so nothing to do; verify, do not assume) | Three PRs against a scratch branch: docs-only (no `ci.yml` run), web-only (frontend jobs only plus the reusable Go jobs), Go-only (no frontend jobs). Record each wall time in the PR body | Revert | S |
| 07-C3 | **One sharded short-test run instead of two full ones.** Today the PR runs the full `-short -race` suite **twice** in parallel: `Minimal CI / Go Tests (short, race)` (19.3 min, in the reusable workflow) and `Coverage Floor (PR gate)` (22.7 min, `make test-short` with `-coverprofile`). Replace both with one matrix job `go-test-short` of **4 shards**, balanced by measured package time the way `scripts/ci/fixture_test_packages.py` already balances the fixture shards (its `_WEIGHTS` map). The two packages over 5 min are split **inside the package**: `go test -list '^Test' ./internal/plugins/maintenance` (1,589 tests, 578 s on the Mac without `-race`; 182 test files) and `./internal/server` (1,397 tests, 414 s) are each divided into 2 `-run '^(Name1\|Name2\|…)$'` groups by the same script, so no shard carries more than about 5 min of tests plus 2 min of setup. Each shard writes `coverage-<n>.out`; a final `coverage` job merges them (`go tool covdata` with `GOCOVERDIR`, or line-concatenation of `-coverprofile` files with the mode line kept once) and runs `make coverage-check-short` against `.ci/coverage-floor.txt`. The duplicate in the reusable workflow is switched off by a **one-line input** added upstream (`run-go-tests`, default true) in `falkcorp/github-common/.github/workflows/reusable-ci-minimal.yml`, then pinned here; if that PR is refused, the fallback is to inline the reusable workflow's `go-vet-build` and `go-lint` jobs into `ci.yml` and keep only the frontend half of it. **Before:** PR wall 22–24 min, longest job 22.7 min. **Target:** PR wall under 10 min, longest shard under 8 min. **After 07-S1b** (in-memory test stores, 15.8× on `internal/server` per `Makefile:233-239`) the shard count can drop back to 2 | `.github/workflows/ci.yml` (`coverage-gate` → `go-test-short` matrix + `coverage` merge job), `scripts/ci/short_test_shards.py` (new; sibling of `fixture_test_packages.py`; emits the package list or the `-run` regex for shard `k/N`), `scripts/ci/tests/test_short_test_shards.py` (new), `Makefile` (`test-short-shard SHARD=k/N`; `test-short` keeps running everything locally), `.ci/coverage-floor.txt` unchanged; upstream: `falkcorp/github-common` (the `run-go-tests` input) | Every test name appears in exactly one shard (the script's self-check: union of `-list` output across shards equals the full list, no duplicates); the merged coverage total equals the single-run total within 0.1 points on one PR; PR wall time before and after recorded in the PR body | Revert the workflow; the Makefile target is additive | M |
| 07-C4 | **Stop paying twice on `main`.** After C1, `main` fails only on real regressions or flakes, so `auto-revert.yml`'s free re-run (`auto-revert.yml:85-97`) becomes rare and `main`'s cost falls to one PR-sized run (≈10 min after C3). No workflow change is proposed here; C4 is the measurement: the first 10 merges after C1–C3 land are tabled (wall time, attempt count) in a short note under `docs/ci/`, and if any job still needs a re-run on more than 2 of 10, its mechanism is found (the standing flake rule), not re-run | `docs/ci/2026-10-ci-throughput.md` (new) | The table itself | — | S |

Order: C1 → C2 → C3 (C3's upstream input PR can be opened the same day as C1) → C4 runs alongside the next ten merges. **G4's manifest comes after C3**, because C3 changes which jobs exist.

### Phase 0: gates (land first; no runtime change)

*(r4)* G1–G3 are Go tests, not scripts (see §3.2 "Simpler enforcement"): they
run under `go test ./...` on all three runners with no manifest change, and they
are one-way by construction (D47), lowered by hand in the PR that lowers the
count.

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-G1 | Flattened `Store` method ratchet (baseline 455, may only fall) and consumer-reference ratchet (baseline 31) as a test | `internal/database/store_width_test.go` (new; the A.3 flattener as a test over its own package directory), `internal/database/store_consumers_test.go` (new; the §A.3 grep over `internal` and `cmd`, with the 14 assertion and 13 wiring lines classified in a table the test owns) | A mutation check: plant a method and expect a failure; remove one and expect a failure until the constant is lowered | Revert the PR | S |
| 07-G2 | Layering rule as a test, with a shrink-only `allowed` edge map | `internal/arch/layering_test.go` (new), `docs/architecture/layering.md` (new, the §3.2 table) | A planted `config` → `server` import fails; removing a listed violation without removing its `allowed` entry fails | Revert | S |
| 07-G3 | Ratchet on direct `config.AppConfig.` reads (baseline 636) as a test | `internal/config/appconfig_reads_test.go` (new) | A planted read fails | Revert | S |
| 07-G4 | One gate manifest for the gates that cannot be tests; every remaining script ratchet fails only upward; a scheduled lowering job (**D47, approved**) | `ci/gates.txt` (new: coverage floor, errcheck ratchet, interface width, gofmt, mock freshness, tygo freshness once F1 lands), `Makefile` (`ci`), `.woodpecker/checks-lint.yaml`, `.woodpecker/checks-build.yaml`, `.github/workflows/ci.yml` (after C3), `scripts/check-interface-width.sh`, `scripts/check-errcheck-ratchet.sh` (both already one-way after C1; G4 only wires them to the manifest), `.github/workflows/ratchet-lower.yml` (new, weekly: lowers `.errcheck-baseline` and `.interface-width-baseline` to the measured count and opens a PR) | Compare the runner gate lists against the manifest in CI; the lowering job's PR is dry-run against a planted improvement | Revert | M |

### Phase 1: reliability (small, independent)

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| ~~07-R1~~ | *(cut, r4)* The activity default flip and the NutsDB test port are **01 P1 and 01 P2** (coordinator note above; 01's own note confirms "P1 absorbs 07 R1"). Nothing remains here. | — | — | — | — |
| 07-R2 | `/health` adds `memdb_ready`, `warmup_ms`, `ready` and `degraded[]`; add the fallback-read counter | `internal/server/handlers/system/handler.go`, `internal/database/pebble_store.go`, `internal/database/memdb_reads.go`, `internal/metrics/` (one file), `internal/server/server_lifecycle.go` | Handler tests before and after warmup; counter increments on the fallback path | Revert (additive fields) | S |
| 07-R3 | Startup step classification table; degraded steps never return an error from `serve` | `cmd/root.go`, `internal/server/server_lifecycle.go`, `internal/server/startup_steps.go` (new) | Table test: each degraded step with an injected failure still serves | Revert | S |
| 07-R4 | `Type=notify` plus `READY=1`; deploy waits on readiness (**D44, approved**; the owner installs the unit; 05 PR 5's `NeedsReady` gate reads the same readiness flag) | `deploy/audiobook-organizer.service`, `internal/server/sdnotify.go` (new), `internal/server/server_lifecycle.go`, `scripts/deploy-preflight.sh`, `Makefile` (deploy wait) | A unit test with a fake `NOTIFY_SOCKET` | Revert the unit to `Type=simple` (the owner installs the unit) | S |

### Phase 2: data model (approved items first)

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-S1 | `dbtest.NewStore(t)`, in-memory by default | `internal/database/dbtest/store.go` (new), `internal/database/dbtest/store_test.go` (new) | Helper tests | Revert | S |
| 07-S1b..n | Mechanical sweep of the 367 on-disk constructions, one PR per package group | The test files that `grep -rln 'NewPebbleStore(' --include='*_test.go' internal cmd` lists, grouped: `internal/server/**`; `internal/database`; `internal/plugins/**`; the rest | The package suites; record wall time before and after | Revert the per-package PR | M (parallel sweep) |
| 07-S2 | **Approved:** `UpdateBook` stale-write check, using the section 4.2 version id inside the release B chokepoint (**lands with or after release B**) | `internal/database/pebble_store.go` (the chokepoint), `internal/database/store.go` (`UpdateBook` signature or option), `internal/database/mock_store.go`, `internal/database/mocks/mock_store.go` (regenerate), the 10 call lines (coordinator re-count; one is the store's own `pebble_store.go:3654`) in: `internal/database/migrations.go`, `internal/merge/combine_journal.go`, `internal/organizer/service.go`, `internal/organizer/move.go`, `internal/organizer/rename.go`, `internal/server/indexed_store.go`, `internal/server/handlers/organize.go`, `internal/dedup/book_dedup.go` | Race test with two concurrent stale writes; one wins and one gets `ErrStaleWrite` | Revert (the field stays, unused) | M |
| 07-S3 | Index from `ChangeObserver.BooksChanged`; delete `indexedStore` (**D43, approved; independent of release B; before 05 PR 7** per 08 §5) | `internal/server/search_result_cache.go` (`searchChangeObserver`), `internal/server/indexed_store.go` (delete; keep `enqueueIndex` and the worker in a new `internal/server/search_index_queue.go`), `internal/server/server.go` (`OpsStore`), `internal/server/server_lifecycle.go`, the 14 `= database.Store(nil)` assertion files listed in B-D2, and a new `internal/server/search_observer_paths_test.go` | A per-path test that each write path reaches the index queue; a failed commit fires nothing; the existing search coverage and reconciler tests | Revert (the decorator comes back) | M |
| 07-S4 | Key-family owner field plus a raw-KV ownership ratchet; generated schema docs (**after release A's registry**) | the release A registry file, `scripts/check-key-family-owners.sh` (new), `docs/database-pebble-schema.md`, `docs/database-architecture.md` | Ratchet mutation check | Revert | S |
| 07-S4b | Move `internal/merge`'s 23 raw-KV calls behind owner helpers | `internal/merge/*.go` (the files that `grep -lE '\.(SetRaw|GetRaw|ScanPrefix|DeleteRaw)' internal/merge` lists), `internal/database/` (helpers) | The merge suite | Revert | M |
| 07-S6 | *(Coordinator-added, from 01 M6; **D51, approved**: helper now, read-site sweeps in wave 3, writes stop at a storage cut-over.)* `Book.FilePath` → `book_file`: add one helper, "the book's real files" (primary/active), built on `GetBookFilesForIDsCore`. Then sweep the read sites one package per PR, largest first: `plugins/maintenance` 63, `organizer` 53, `metafetch` 53, `database` 40, `scanner` 37, `audiobooks` 34. Stop the 67 writes last, in a storage cut-over. | `internal/database/book_files_helper.go` (new), `internal/database/store.go`; then per-package read sites (01 appendix A census) | Helper tests on multi-file, missing-file and version-group books; per package, the existing suites plus one test that the stale `FilePath` is never read | Revert per package | L in total (helper S, about 8 sweep PRs of size M) |
| 07-S5 | **D42, approved** (in a storage cut-over window): version-group record plus converter plus a derived flag; groups with zero or several primaries go to the owner as a list, never auto-resolved | `internal/database/version_group_store.go` (new), `internal/database/store.go`, the release B/C cut-over converter file, `internal/versionprimary/ensure.go`, `internal/versionprimary/rank.go`, `internal/versionprimary/incumbent.go`, the reader sites of `IsPrimaryVersion` (one accessor) | Converter verification (P4: verify, then purge); invariant test that exactly one primary exists per group | The pre-migration checkpoint (P3, Q6 window) | L |

### Phase 3: modularity

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-M1 | Make config a leaf: move DB persistence and update into `internal/config/configstore` | `internal/config/persistence.go`, `internal/config/update_service.go`, `internal/config/register.go`, `internal/config/ai_endpoints.go` → `internal/config/configstore/*`; callers `cmd/root.go`, `cmd/child_mode.go`, `internal/server/server.go`, `internal/server/wire_handlers.go`, `internal/server/registry_wire.go`, `internal/server/handlers/system/handler.go`, `internal/server/handlers/ai.go`, `internal/server/handlers/scheduler_admin.go` (list by `grep -rln "config\.\(Save\|Load\)ConfigToDatabase\|UpdateService" internal cmd`) | `go list` shows no module imports in `internal/config`; the config suite | Revert | M |
| 07-M2 | Declarative field table; table-driven defaults, env and DB apply; delete the `applySetting` switch | `internal/config/fields.go` (new), `internal/config/config.go`, `internal/config/configstore/persistence.go` | Round-trip test for every field (default, env, DB); a missing table entry fails the build test | Revert | M |
| 07-M3 | Sparse config persistence plus a startup converter from the blob (**D45, approved**) | `internal/config/configstore/persistence.go`, `internal/config/configstore/convert.go` (new) | Converter test: a blob with zeros gives no rows for default-equal keys | Restore the `config_blob` row from the backup (kept until the next release) | M |
| 07-M4 | Remove `metadata` → `operations/registry` | `internal/metadata/enhanced.go`, plus the caller that supplies the reporter | Build plus the layering ratchet goes down by one | Revert | S |
| 07-M5 | Pipeline hooks become constructor parameters | `internal/scanner/service.go`, `internal/scanner/ai_parse_async.go`, `internal/organizer/service.go`, `internal/organizer/rename.go`, `internal/server/wire_handlers.go` (and the wiring file that sets the hooks) | Constructing without a hook fails to compile; the existing scan and organize tests | Revert | M |
| ~~07-M6..M13~~ | *(cut, r4; **D49**)* The domain split of `plugins/maintenance` happens **inside 05's port waves 12A–12E**, one domain per wave PR, so each op moves and ports once. The domain list (`authors`, `versions`, `fragments`, `files`, `itunesread`, `metadata`, `activity`, `reports`), the rule that subpackages import only `internal/repairs` and domain services, and the invariant test (registered op-ID set and ConcurrencyKey set unchanged before and after) are **requirements on 05's wave briefs**, not PRs here. | — (05 `implementation-briefs.md`, waves 12A–12E) | the invariant test, owned by 05 | per 05 wave | — |
| 07-M14 | Retire `database.Store` as a consumer type (**after S3**) | The about 5 genuine wide consumers (B-D2: `plugins/maintenance/deps.go`, `server/provider_throttle_wire.go`, `server/catalog_harvest_op.go`, `cmd/root.go`) and the `AsCapability` sites that become direct calls | The flattened ratchet and consumer-reference ratchet fall | Revert per package | M |

### Phase 4: frontend

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-F1 | Generate TS types from the Go DTOs with **tygo** (§3.5), plus contract golden tests | `go.mod` / `go.sum` (`tool github.com/gzuidhof/tygo` directive, v0.2.21), `tygo.yaml` (new), `Makefile` (`gen-types`, `gen-types-check`), `web/src/types/generated/*.ts` (new), `internal/server/handlers/**/testdata/contract/*.json` (new), `web/src/services/contract.test.ts` (new), `.github/workflows/ci.yml` (a `gen-types-check` step beside Mock Freshness; after C3), `ci/gates.txt` (after G4) | Golden JSON type-checks; CI fails on a stale generation (dirty tree after `go tool tygo generate`) | Revert | M |
| 07-F2 | Split `api.ts` by domain | `web/src/services/api.ts` → `web/src/services/api/{books,review,operations,dedup,system,itunes,activity}.ts`, `web/src/services/api/index.ts` (re-export) | The existing `api.*.test.ts` suites | Revert | M |
| 07-F3 | **D48 pilot: TanStack Query v5 on the Repairs lane.** Full spec, defaults, key conventions, SSE invalidation and the acceptance bar in [appendix C §4](07-design-decisions-and-modularity/C-server-state-library.md). **Must land before 05 wave 12D** (which re-points the lane to `/api/v3/ops/*` and 05 PR 14 retires `/api/v1/repairs`), or inside that re-point, so the lane is not rewritten twice | `web/package.json`, `web/package-lock.json`, `web/src/lib/queryClient.ts` (new), `web/src/test/queryWrapper.tsx` (new), `web/src/main.tsx` (provider; dev-only devtools), `web/src/components/review/lanes/useRepairsLane.ts`, `web/src/components/review/lanes/useRepairsLane.test.ts` | The lane's 22 tests unchanged and green; at least 150 of the 215 server-state lines removed; test file wall time at most 3.5 s (2.72 s today); gzipped JS at most 650 KB (636 KB today); React Compiler bailouts in the lane not higher. Miss the bar → revert and close D48 as "no" | Revert (one dependency, one lane, no server or data change) | M |
| 07-F4 | **D46, approved: delete** `docs/api/openapi.json` | `docs/api/openapi.json` (delete), `.claude/skills/api-doc/SKILL.md` (point at `web/src/types/generated/` once F1 lands; until then at the Go DTOs), `changelog.d/<new>.md`. `CHANGELOG.md` and `TODO.md` mentions are history and stay | `grep -rn openapi.json --exclude-dir=archive` finds only history | Revert | S |

### Phase 5: upgradeability

| PR | Change | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| 07-U1 | `GO_VERSION` single source | `GO_VERSION` (new), `Makefile`, `.envrc`, `.woodpecker/*.yaml` (six files), `.github/workflows/ci.yml`, both Dockerfiles, `CLAUDE.md` | The CI step compares `go.mod` and `GO_VERSION` | Revert | S |
| 07-U2 | ABS / AudioBooth golden response fixtures | `internal/server/handlers/abs/testdata/golden/*.json` (new), `internal/server/handlers/abs/golden_test.go` (new) | Golden diff | Revert | S |

**Order and collisions.**

- *(r4)* **C1 → C2 → C3 first**, before any other PR in this document; C4 is a measurement alongside the next ten merges.
- G1 to G3 can run in parallel (they are tests; no shared files).
- G4 is approved (D47) and waits only for C3, because C3 changes the job set the manifest lists.
- R2 to R4 are independent (R1 is 01's).
- S2 waits for storage release B. S4 waits for release A's registry. S3 is independent of the storage releases.
- The maintenance domain split waits for 05's SDK spec (it happens inside the 05 port waves 12A to 12E; M6 to M13 were cut in §4).
- M14 waits for S3.
- **Shared-file collisions:**
  - `internal/server/server_lifecycle.go`: R2, R3, R4, S3.
  - `.github/workflows/ci.yml`: **C2, C3**, G4, F1, U1, and 06 P2 (the `go-version` patch pin). Order: C2 → C3 → 06 P2 → G4 → F1 → U1. G1–G3 no longer touch it.
  - `internal/database/pebble_store.go`: R2, S2, and storage release B.
  - `internal/logger/slog_guard*_test.go`: C1 only.

  Sequence them in the coordinator's matrix (08).

## 5. Risks and what must not break

- **`internal/writeback/` and iTunes.** Nothing here edits `internal/writeback/` or writes to iTunes. The Book facet idea (D5b) excludes every iTunes field until the owner says otherwise. The maintenance split moves only read-only iTunes repair files, into `itunesread`.
- **book_file rows.** No PR deletes `book_file` rows. S5 touches only book rows and the new group record; anomalies are repointed or listed, never deleted.
- **The scan ConcurrencyKey.** It stays one key. each 05 port wave PR that moves ops asserts the ConcurrencyKey set is unchanged.
- **The search index (S3).** The index must not miss writes after the decorator is gone. The per-path test, which covers every current caller of `enqueueIndex` and `markIndexDirty` including the rename fan-out, plus the existing dirty-set reconciler, cover it. Keep the reconciler.
- **The stale-write check (S2).** It can surface stale writers as errors in prod. Ship it with an `ErrStaleWrite` counter and a log line. The approved rule is to refuse, so do not downgrade it to a warning. It must not add a version mechanism beside the storage design's version id (section 4.2).
- **The activity default flip (R1).** It changes nothing in prod, which already runs Pebble. Rollback is the environment variable.
- **`Type=notify` (R4).** If `READY=1` is never sent, systemd kills the service at `TimeoutStartSec`. Set `TimeoutStartSec` above the measured warmup (300 seconds) until release C shortens it.
- **Config conversion (M3).** A wrong default comparison could drop a deliberate override that equals today's default. That is harmless by definition: the value is the same. The conversion is logged key by key.
- **Every count here is from HEAD on 2026-10-08.** Re-run the A-appendix commands before each PR.

## 6. Dependencies on other workstreams

- **05 (operations v3):**
  - the dispatcher gate on `memdb_ready` (D3);
  - the op-package shape for the maintenance split (inside the 05 port waves 12A to 12E);
  - where background loops go (D11).

  I assume the SDK keeps op IDs and ConcurrencyKeys stable.
- **04 (operations census):** which of the 47 tickers and 10 hand-started goroutines become operations (D11).
- **02 (search and identification):**
  - S3 changes how Bleve learns about writes: from the decorator to the existing `ChangeObserver`;
  - D12's orchestrator choice belongs to 02 and 05.
- **01 (dead code):**
  - owns the activity default flip and the NutsDB test port (01 P1, P2; was 07 R1) and deletes the NutsDB and SQLite stores after them;
  - deletes `MockStore` once D15(c) lands.
- **06 (bleeding edge):**
  - 06 P2 edits `.github/workflows/ci.yml` (the `go-version` patch pin) and goes after C3;
  - `go.mod` `tool` directives (D17): F1's `tygo` directive is the first one, so 06's tool-directive PR follows F1 or lands in it;
  - the generator (tygo) and the server-state library (TanStack Query v5) are chosen here (§3.5, appendix C); 06 §6 agrees.
- **08 (coordinator), action required:** move **07-C1, C2, C3 to the front of wave 0**, before X4 (which C1 absorbs), 06 P1 and every other PR. Every later PR's cost and every "main is green" claim depend on them. Re-run the wave-0 duration estimate with a 10-minute PR run.
- **03 (dedup page retirement):** none directly. The lane pattern D14 applies to its parity work.
- **Storage efficiency releases A, B and C (approved, outside this charter):** S2 lands inside B's chokepoint; S4 needs A; D3's final choice waits on C; S5 must agree with the section 6 archive rules.

## 7. Open questions for the owner

*(r4)* All eight were answered in `09-owner-decisions.md` (Q1 = D42, Q2 = D43,
Q3 = D44, Q4 = D45, Q5 = D49 as merged into 05, Q6 = D46 delete, Q7 = D47,
Q8 = D48 pilot). They are kept below for the record; the plan rows above carry
the D numbers. One new question from this round:

9. **Q9. CI throughput first?** Should 07-C1 to C3 (§4, Phase 0a) go to the
   front of wave 0, ahead of X4 and the Go 1.27.2 bump, with the reusable
   workflow's `run-go-tests` input requested from `falkcorp/github-common`?
   - **Recommended: yes.** Measured: 22–24 min per PR run, 39–49 min per merge,
     0 green runs in the last 12, and about 191 PRs planned. Every later item
     is cheaper and every "green" claim is meaningful only after this.
   - If the upstream input is refused, inline the two Go jobs (C3's fallback).

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
   - **Recommended: yes (inside the 05 port waves 12A to 12E),** one domain per PR, with op IDs unchanged.
6. **Q6. What should happen to `docs/api/openapi.json`: delete it, or generate it from code?**
   - **Recommended: delete it now (F4), and generate it later if a consumer appears.** A hand-written spec covering about 58% of routes misleads more than it helps.
7. **Q7. Should the two-way ratchets become fail-on-rise only, with a bot PR to lower the baseline?**
   - **Recommended: yes (G4).** `main` failed on an improvement today, 779 to 770 errcheck findings. Of its last 30 runs, 1 succeeded, 9 failed and 20 were cancelled; the cancellations are most likely superseded runs.
8. **Q8. Should a server-state library be adopted for the Review lanes, starting with the Repairs lane as a pilot?**
   - **Recommended: yes (F3).** Keep it only if the pilot removes code and does not slow the tests.
