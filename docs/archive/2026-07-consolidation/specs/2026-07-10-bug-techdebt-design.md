<!-- file: docs/specs/2026-07-10-bug-techdebt-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 00935dd1-999e-4d0d-bae0-ae7a6d1a68e6 -->
<!-- last-edited: 2026-07-10 -->

# Bug + Tech-Debt Cluster (INIT-9) — Design Spec

**Status:** Approved — ready for implementation planning (locked by the 2026-07-10
remaining-work master plan, INIT-9)
**Scope:** Go backend (`internal/config`, `internal/operations/registry`,
`internal/dedup/unified`, `internal/models`, `internal/database`, `internal/server`,
`internal/organizer`), one CI workflow file (`.github/workflows/ci.yml`), one plan-only
document (repo-size history rewrite). No frontend code. No prod-data mutations.
**Parent task:** INIT-9 — Bug + tech-debt cluster
(`.claude/notes/2026-07-10-remaining-work-master-plan.md`)
**Package-weight note:** five of the seven items are one-to-few-file mechanical fixes;
the full per-task brief ceremony (worktree bootstrap, idempotency, rollback, locked
decisions) is driven by the /parallel-sweep autonomous execution model — which
requires standalone, self-contained briefs — not by item complexity. Do not
over-estimate risk from the package's weight.

---

## Motivation

Seven small, well-specified debt items are individually cheap but collectively make the
local gate (`make ci`) permanently red and leave known latent bugs unverified. All
evidence below was re-verified against HEAD `fce58498` on 2026-07-10:

| Item | Symptom (verified 2026-07-10) | Source |
|---|---|---|
| CFG-2 Phase D | Flat→nested config compat shim still live: `legacyRemapGroup`/`configRemapGroups` at `internal/config/update_service.go` (grep `legacyRemapGroup` → :70,:72,:80), applied at :228. Phases B+C shipped in PR #1514 on 2026-06-19 — the "1+ wk prod stability" gate is satisfied (3 weeks). TODO.md:481 cites a STALE path `internal/server/update_service.go` (file does not exist). | #1536 / CONS-13 |
| STATICCHECK-BURNDOWN | `staticcheck ./...` on HEAD: **41 findings — 37 U1000 (unused) + 4 SA1019 (deprecated SQLite sentinels)**. Issue #1796 said ~18 on 2026-07-03; the backlog grew. `make ci`'s staticcheck step fails on main. | #1796 |
| SDKGUARD-VIOLATION | `make sdkguard` FAILS on main with exactly two forbidden packages in the `pkg/plugin/sdk` dep tree: `internal/logger` (chain: sdk → internal/operations/registry → internal/logger; the ONLY use is `logger.WithOperation` at `internal/operations/registry/worker.go:157`) and `internal/dedup/unified` (chain: sdk → internal/database → internal/dedup/unified; the ONLY use is the `unified.UnifiedDedupScore` type in `internal/database/embedding_store.go` :120,:161,:845). | #1795 |
| MOCK-FRESHNESS-GLOB-GAP | `.github/workflows/ci.yml:89,:91` pass unquoted `internal/*/mocks/` to `git diff` — the SHELL expands it (one level deep, 6 dirs) before git sees it, so all 8 nested `internal/server/handlers/*/mocks/` dirs are invisible to the freshness gate (stale-mock escape that bit #1736→#1757). | #1797 |
| WARMERS-NOT-IN-BGWG | 4 fire-and-forget cache warmers launched untracked at `internal/server/server_lifecycle.go` :704 (`warmFacetsCache`), :709 (`warmLibrarySizes`), :724 (`warmAuthorsCache`), :725 (`warmSeriesCache`) — same lifecycle gap that produced the PEBBLE-CLOSED panic family; sibling `library-list-warmer` is enrolled in `s.bgWG` (:719-:723). | #1794 (follow-up to #1781) |
| REPO-SIZE-1 | Issue #1650 reports the repo at 1.69 GB (GitHub-side); local `git count-objects -vH` shows `size-pack: 228.41 MiB` — the delta itself needs auditing. History rewrite is destructive: **plan only, STOP for human.** | #1650 |
| STOREFID W5d-1 | `CreateOrganizedVersion` (`internal/organizer/service.go:817`) still writes the slim, page-derived original book straight back via `orgSvc.db.UpdateBook(book.ID, book)` (~:939) — the in-code comment says "Deliberately NOT fixed here". `PebbleStore.UpdateBook`'s STOR-1 guard (pebble_store.go:1419) preserves Description/VersionNotes/BookSig* but **NOT Author/Series** — the wipe is real and unverified by any test. | TODO.md:62-84 |

**Goal:** every item above is either fixed-and-merged autonomously (6 items) or has a
complete, human-reviewable migration plan and a hard stop (REPO-SIZE-1) — with
`make ci` fully green on main once TASK-02 and TASK-03 land.

## Goals

- Retire the CFG-2 flat-key compat shim (Phase D) without touching the blob (JSON
  round-trip) persistence path; correct the stale TODO.md path claims.
- Drain the 41-finding staticcheck backlog so the local `make ci` staticcheck step is
  green on main.
- Break both forbidden dependency chains so `make sdkguard` passes, WITHOUT adding
  `internal/logger` or `internal/dedup/unified` to the sdkguard allowlist.
- Close the Mock-Freshness CI gate's nested-dir blind spot, verified against a real
  nested mock path.
- Enroll all four fire-and-forget cache warmers in `s.bgWG` with a `-race`-verified test.
- Produce (not execute) the REPO-SIZE-1 history-rewrite migration plan; STOP.
- Prove — with a merged regression test — whether Author/Series survive the
  `CreateOrganizedVersion` original-book write-back (verification only; no product fix).

## Non-goals (v1)

- The two dedup stub getters (`GetFolderDuplicatesCore`, `GetDuplicateBooksByMetadataCore`,
  `internal/database/pebble_store.go:1047-1056`) — **owned by INIT-2 T1/T2**; this
  package only cross-references them. No duplicate task here.
- The product FIX for the W5d-1 Author/Series wipe (needs the fail-open hydrate design
  in TODO.md:75-83 — a decision-carrying change, deferred; this package only verifies).
- Retiring the frontend's dual nested+flat settings send (`useSettingsHandlers`) —
  harmless redundancy after Phase D (unmatched flat keys are dropped by
  `json.Unmarshal`); follow-up noted in TASK-01, not executed.
- `remapScheduledKeys` (`internal/config/persistence.go:538`) — the `scheduled_*`
  two-level remap is owned by INIT-6/WF-3 (workflow-state collapse); it STAYS.
- Executing the git-history rewrite (REPO-SIZE-1) — plan only, STOP-FOR-HUMAN.

## Decisions (locked during design)

1. **SDKGUARD chain 1 (registry → logger): dependency inversion via setter, NOT
   copy-into-sdk, NOT allowlist.** `pkg/plugin/sdk` never imports `internal/logger`
   directly, so copying a logger interface into the sdk fixes nothing; allowlisting
   `internal/logger` defeats the guard. Locked: add
   `Registry.SetRunContextDecorator(func(ctx context.Context, opID string) context.Context)`
   (default nil → no-op), replace the single `logger.WithOperation` call in
   `worker.go`, and wire `logger.WithOperation` in `internal/server/registry_wire.go`
   next to the existing `SetActivityRecorder` call (:337) — same precedent as
   `SetDepsScheduler`/`SetDepBookStore`. The op-ID log-correlation chain (SLOG) must be
   preserved by the prod wiring and asserted by a test.
2. **SDKGUARD chain 2 (database → dedup/unified): move the shared data types down to
   `internal/models` (already allowlisted), alias back.** `UnifiedDedupScore`, `Signal`,
   and `SignalKind` move to a new `internal/models/dedup_score.go`; `internal/dedup/unified/score.go`
   keeps `type UnifiedDedupScore = models.UnifiedDedupScore` (etc.) aliases so all
   existing consumers compile unchanged; only `internal/database/embedding_store.go`
   switches its import. `score.go` imports only `time`, so the move drags nothing.
   Losing alternative (allowlisting `internal/dedup/unified`) rejected: scoring logic
   is not SDK backplane.
3. **CFG-2 Phase D scope:** remove ONLY the generic shim (`legacyRemapGroup` type,
   `configRemapGroups` var, `applyLegacyRemaps` func + its call) and its tests. KEEP
   `remapScheduledKeys` (INIT-6/WF-3 owns it) and KEEP the JSON round-trip blob write
   (update_service.go ~:231-239). Backend-only; the frontend dual-send is untouched.
   Because the post-removal path is fail-OPEN (a flat-only POST is silently dropped by
   `json.Unmarshal` with zero observability), the removal ships with a
   **detection-only warn-log** for one release: a `retiredLegacyFlatKeys` list (the
   flat key names from the deleted `configRemapGroups` literal) drives a per-key
   warning (`legacy flat config key %q received; no longer remapped, dropped`) so a
   lost config write is detectable instead of silent — log only, no remapping, no
   mutation; a TODO.md follow-up removes the log after one stable release. No feature
   flag is needed because rollback is a **true inverse**: reverting the PR restores
   the shim with zero data-shape migration.
4. **W5d-1 is verification-only with a two-outcome protocol.** Planning-time evidence
   says the Author/Series-survival assertion will FAIL (STOR-1 guard omits
   Author/Series). Outcome A (test passes): merge, check off TODO.md:62. Outcome B
   (test fails = wipe confirmed): convert the failing assertion to
   `t.Skipf("W5d-1 known bug: ...")` with the evidence inline, merge the (now green)
   test as documentation + demotion regression coverage, and report the confirmation.
   No product code is touched either way. On outcome B, TASK-07 is NOT done until a
   tracked, severity-tagged follow-up for the fail-open hydrate fix (TODO.md:75-83)
   exists — a filed GitHub issue plus the annotated TODO.md item — and the
   confirmation is reported on a BLOCKED line; a `t.Skip` + TODO note alone is not
   sufficient closure for a confirmed live prod data-loss path.
5. **Staticcheck policy:** per-PR gating stays "Minimal CI green + scoped staticcheck
   on changed files" (the local `make ci` staticcheck step is red on main until
   TASK-02 completes). TASK-02 drains the FULL backlog in one PR, running LAST (wave 2)
   so newly-exposed findings from wave-1 merges are included. SA1019 findings that are
   deliberate compat keeps (e.g. `SQLiteTableStat` in the db-health API) use
   `//lint:ignore SA1019 <reason>` rather than deletion.
6. **REPO-SIZE-1 is STOP-FOR-HUMAN.** The only deliverable is a migration-plan
   document (filter-repo vs BFG vs LFS, coordination checklist, backup strategy).
   No rewrite command is executed, ever, under this initiative.
7. **TODO.md/CHANGELOG.md are exempt from the collision matrix** (docs-ledger
   exception): every task updates them per post-task hygiene; conflicts are resolved
   keep-both-sides during the pre-merge rebase. Code files follow strict wave rules.

## Data model

No persisted-schema changes. Two Go surface changes:

```go
// internal/models/dedup_score.go (NEW — moved verbatim from
// internal/dedup/unified/score.go; json tags unchanged, so persisted
// candidate rows decode identically)

// SignalKind is the identifier for a single evidence signal in the dedup pipeline.
type SignalKind string

// Signal is one evidence signal contributing to a composite dedup score.
// (Fields moved verbatim from unified.Signal — copy the existing definition,
// do not redesign.)
type Signal struct { /* moved verbatim */ }

// UnifiedDedupScore is the persisted composite-score breakdown for a candidate
// pair. (Moved verbatim from unified.UnifiedDedupScore.)
type UnifiedDedupScore struct { /* moved verbatim */ }
```

```go
// internal/operations/registry/registry.go (NEW setter, same shape as
// SetActivityRecorder / SetDepBookStore)

// SetRunContextDecorator installs a hook that decorates each op run's context
// (e.g. stamping the op ID for log correlation). nil (the default) is a no-op.
func (r *Registry) SetRunContextDecorator(fn func(ctx context.Context, opID string) context.Context)
```

### Persistence

- Dedup candidate rows already persist `ScoreBreakdown` via json tags on
  `UnifiedDedupScore`; the type move keeps tags byte-identical → zero migration.

## Components

### C1. CFG-2 Phase D shim retirement (`internal/config/update_service.go`)

Delete `legacyRemapGroup` (type), `configRemapGroups` (var), `applyLegacyRemaps`
(func + call). Post-condition: a POST with ONLY flat legacy keys no longer remaps
(keys are dropped by `json.Unmarshal` — no matching top-level tags — and each dropped
retired key is warn-logged via the one-release `retiredLegacyFlatKeys` detection list,
Decision 3); nested keys keep working via the untouched JSON round-trip. Fix
TODO.md:481 stale path.

### C2. Staticcheck burndown (repo-wide, wave 2)

37 U1000: delete genuinely dead symbols (verify each with `grep -rn <symbol>` — a
symbol referenced only from its own declaration is dead). 4 SA1019: update callers off
the deprecated SQLite sentinels or `//lint:ignore` deliberate API-compat keeps.
Post-condition: `staticcheck ./...` exits 0.

### C3. SDK guard (registry + unified + database + models + server wiring)

Per Decisions 1-2. Post-condition: `make sdkguard` exits 0; op-ID context decoration
still happens in prod wiring (test-asserted).

### C4. Mock-freshness glob (`.github/workflows/ci.yml`)

Replace the shell-expanded `internal/*/mocks/` (lines 89, 91) with the quoted git
pathspec `':(glob)internal/**/mocks/**'` (verified this session:
`git ls-files ':(glob)internal/**/mocks/*'` matches the nested
`internal/server/handlers/*/mocks/` dirs the shell glob misses). Push via git, never
the MCP contents API (workflow-file corruption rule). **Mandatory pre-merge freshness
check of the newly-covered surface:** regenerate mocks with the CI-pinned mockery
(v3.7.1 — exactly, per the version-drift gotcha) and confirm
`git status --porcelain -- ':(glob)internal/**/mocks/**'` is empty BEFORE merging; if
any previously-invisible nested mock is already stale on main, regenerate it in the
SAME PR so the tightened gate lands green-covering-green — merging the glob fix over
a stale nested mock would turn Minimal CI red on origin/main and halt every other
wave-1 merge under the coordinator protocol.

### C5. Warmer bgWG enrollment (`internal/server/server_lifecycle.go`)

Wrap the four untracked `go s.warmX()` launches in named `s.bgWG.Add/Done` pairs with
a `s.bgCtx.Err()` early-exit inside the goroutine (no warmer signature changes).
Rewrite the now-false comment block (~:697-701) that says "Do not promote the
untracked ones here". `-race` test proves enrollment and the skip-on-shutdown path,
plus an anti-over-suppression check that warmers still run under a live context.

### C6. REPO-SIZE-1 migration plan (docs only)

Audit largest blobs (`git rev-list --objects --all | git cat-file --batch-check ... | sort -k3 -n -r`),
reconcile 1.69 GB (GitHub) vs 228.41 MiB (local pack), compare git-filter-repo vs BFG
vs LFS, produce coordination checklist (clones/worktrees/forks/open-PRs/CI/GitHub
support gc) + backup strategy. **Then STOP.**

### C7. W5d-1 write-back verification (`internal/organizer/` test only)

New test file exercising `CreateOrganizedVersion` against a real
`NewPebbleStore(t.TempDir())` (+ mandatory `WaitForWarmup()`, pebble_store.go:159)
with a slim (heavy-field-nil) book input; asserts demotion invariants (must pass) and
Author/Series survival (two-outcome protocol, Decision 4).

## Migration / integration

- Type move (C3): consumers of `unified.UnifiedDedupScore`/`unified.Signal` compile
  unchanged via aliases; `SignalKind` constants stay declared in `unified` (constants
  of an aliased type remain valid). Only `embedding_store.go` changes imports.
- Registry hook (C3): all existing `New`/`NewWithOptions` callers (including tests)
  need no change — nil decorator is a no-op; prod wiring adds one
  `SetRunContextDecorator(logger.WithOperation)` line in `registry_wire.go`.
- CI glob (C4): behavior-tightening only — previously-invisible stale nested mocks now
  fail the gate (intended).

## Milestones

- **M1 (wave 1) — six independent items.** TASK-01 (CFG-2-D), TASK-03 (SDKGUARD),
  TASK-04 (MOCK-GLOB), TASK-05 (WARMERS), TASK-06 (REPO-SIZE plan, STOP-FOR-HUMAN),
  TASK-07 (W5D1-VERIFY). Disjoint code files; each its own worktree/PR.
- **M2 (wave 2) — TASK-02 (STATICCHECK-BURNDOWN).** Runs after wave-1's Go-touching
  tasks (TASK-01, TASK-03, TASK-05, TASK-07) merge: its file set is derived from a
  fresh `staticcheck ./...` at execution time, and those merges can expose, remove, or
  relocate findings — and may overlap its run-time-derived file set. TASK-04
  (ci.yml-only) cannot alter any Go finding and does not block M2; neither does
  TASK-06's STOP-FOR-HUMAN review.

Each milestone is independently shippable. No behavior-changing flag is needed: C1 is
a compat removal gated on the already-satisfied Phase B+C stability window whose
revert is a true inverse (no data-shape migration) and whose fail-open drop path is
made observable by the one-release detection warn-log (Decision 3); everything
else is additive or dead-code removal.

## Files modified

| File | Change |
|---|---|
| `internal/config/update_service.go` | remove `legacyRemapGroup`/`configRemapGroups`/`applyLegacyRemaps` + call; add `retiredLegacyFlatKeys` detection warn-log (Decision 3) |
| `internal/config/persistence_test.go` | remove `applyLegacyRemaps` tests |
| `internal/config/update_service_test.go` | add flat-key-dropped + nested-still-applied tests |
| `TODO.md` | fix stale `internal/server/update_service.go` path (:481); check off completed items |
| `internal/operations/registry/registry.go` | `runContextDecorator` field + `SetRunContextDecorator` |
| `internal/operations/registry/worker.go` | use decorator; drop `internal/logger` import |
| `internal/server/registry_wire.go` | wire `logger.WithOperation` into the registry |
| `internal/operations/registry/context_decorator_test.go` | NEW — decorator-applied + nil-decorator tests |
| `internal/dedup/unified/score.go` | move 3 types out; leave aliases |
| `internal/models/dedup_score.go` | NEW — the moved types |
| `internal/database/embedding_store.go` | import swap `unified` → `models` (INIT-2 owns structural edits — this is a 3-reference swap; land before INIT-2 T4 or rebase after) |
| `.github/workflows/ci.yml` | quoted recursive pathspec on lines 89/91 |
| `internal/server/server_lifecycle.go` | bgWG-enroll 4 warmers; rewrite stale comment |
| `internal/server/cache_warmers_bgwg_test.go` | NEW — enrollment + skip-path `-race` test |
| `internal/organizer/organized_version_writeback_test.go` | NEW — W5d-1 verification tests |
| `docs/plans/2026-07-10-repo-size-history-rewrite-plan.md` | NEW — REPO-SIZE-1 plan (STOP deliverable) |
| ~30 files repo-wide (wave 2) | staticcheck U1000 deletions + SA1019 caller updates/ignores |

## Testing

| Test | Asserts |
|---|---|
| `TestUpdateService_FlatKeysDropped` | POST with only flat legacy keys → nested config fields unchanged (shim gone); detection warn-log fires if capturable |
| `TestUpdateService_NestedKeysStillApply` | nested-key POST still mutates config (blob path intact — anti-regression) |
| `TestRegistry_RunContextDecoratorApplied` | decorator set → run ctx carries the decoration; nil decorator → no panic, ctx unchanged |
| `TestStartCacheWarmers_EnrolledInBgWG` | all 4 warmer names appear via `bgWG` enrollment; `Wait()` returns after completion |
| `TestStartCacheWarmers_SkipOnCanceledCtx` | canceled `bgCtx` → warmers skip, `Wait()` returns, no panic |
| warmers anti-over-suppression | live ctx → warmers actually execute (not permanently skipped) |
| `TestCreateOrganizedVersion_OriginalDemotedToNonPrimary` | original gets VersionGroupID + IsPrimaryVersion=false + LibraryState=organized_source even with slim input |
| `TestCreateOrganizedVersion_AuthorSeriesSurviveOriginalWriteback` | Author/Series survive slim write-back (two-outcome protocol per Decision 4) |
| Mock-glob manual probe | mutate a nested mock file → the NEW pathspec `git diff` command detects it; revert |

## Rollback

**Initiative gate (verbatim):** PLAN -> EXECUTE AUTONOMOUSLY per item (worktree/PR/CI)
EXCEPT REPO-SIZE-1 (#1650) which is STOP-FOR-HUMAN: a git-history rewrite is
destructive and invalidates every clone/worktree — produce the migration plan
(BFG/filter-repo vs LFS options, coordination checklist, backup strategy) as a TASK
brief whose ONLY deliverable is the plan document, then STOP.

- Every task is a single revertable PR; none mutates prod data or schema.
- CFG-2-D: revert restores the shim with zero data-shape migration (true inverse —
  the reason no flag gates the removal); until reverted, clients sending ONLY flat
  keys (none known — the frontend has sent nested since PR #1514) lose those writes,
  now observably: each dropped retired key is warn-logged (Decision 3).
- SDKGUARD: revert restores the direct import; type aliases mean the move is
  transparently reversible; persisted json is byte-identical either way.
- MOCK-GLOB: revert restores the narrower gate (only loosens CI, never breaks builds).
- WARMERS: revert restores fire-and-forget launches (the pre-#1794 behavior).
- W5D1 / REPO-SIZE plan: docs/tests only — revert deletes them, product untouched.

## Open questions (resolved — recorded for the plan)

1. ~~Is the sdk's logger import direct or transitive?~~ → Transitive only:
   `sdk → internal/operations/registry → internal/logger` (single use,
   `worker.go:157`) and `sdk → internal/database → internal/dedup/unified`. Verified
   via `go list -deps` BFS this session; fix per Decisions 1-2. NOTE: the master-plan
   research JSON's SDKGUARD anchor (`internal/organizer/service.go:22`, whose own note
   admits it could not confirm the chain) is SUPERSEDED by this BFS — do not "correct"
   the spec back to it.
2. ~~Is the staticcheck backlog still ~18?~~ → No: 41 findings (37 U1000 + 4 SA1019)
   at HEAD `fce58498`.
3. ~~Does UpdateBook's STOR-1 guard protect Author/Series?~~ → No (pebble_store.go
   :1419-:1447 preserves Description/VersionNotes/BookSig* only) — W5d-1 verification
   is expected to confirm the wipe (Decision 4, outcome B).
4. ~~Is the Phase B+C stability gate satisfied?~~ → Yes: PR #1514 merged 2026-06-19,
   3 weeks > the 1-week bar; TASK-01 still re-verifies at execution time.
