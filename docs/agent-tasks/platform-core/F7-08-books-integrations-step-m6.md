<!-- file: docs/agent-tasks/platform-core/F7-08-books-integrations-step-m6.md -->
<!-- version: 1.1.2 -->
<!-- guid: 32e12309-4e58-4518-9d85-0a690231a661 -->
<!-- last-edited: 2026-11-05 -->

# F7-08 — Books' integrations step M6 built dark: instances with secret refs, the config blob deleted, aijob rows, the webhook instances and rule, import roots into core.library.roots with IDs kept, dirty set and holds deleted

| Field | Value |
|---|---|
| ID | F7-08 |
| Release | R2, flip label F7 (dark PR: the step is built and tested, **unregistered** until F7-09) |
| Tier | **T4** (Opus / xhigh–max): a one-way migration of instances, jobs, rules and roots; a dropped row or a changed ID is permanent after M7 |
| Wave | W34 (beside F4-08; file-disjoint; F4-08 merges first and this PR rebases after it) |
| Depends on | F7-01 (`integrations.Importer`), F7-03 (`ai.Importer`), F7-05 (`notify.Importer`), F7-06 (`library` roots as config entries), F7-07 (`storage/index` and the native dirty set), F3-06 (`internal/books/cutover` with its step shape, the rename table and M2's residual blob), F2-13 (the R2 corpus overlay) |
| Spec | 05-metadata M1.10 (the migration table, trust carried over, not carried over), M2.10 (holds and quotas dropped); 05-ai §10 (endpoints, `ai/openai`, `auth_ref` key copy, `aijob*` rows, the capability-ID map); 05-notify §10 (webhook instances, rule `webhooks`, the HMAC secret by value, the topic map); 05-library §5L.10 ("The roots cutover", IDs kept, `rootscan:` records, "M6's write outside its retired claims"); 05-download §10 (the Deluge instance); 05-storage §5S.3.6 (`DataMigration`), §5S.3.8 (dirty set dropped, index rebuilt), §5S.3.13 (the cutover step contract, quarantine), §5S.9.1 (families and claims M6 drains), §5S.3.16 (crash matrix cells); 03 §3.8 M6; 09 §9.2.3 (row 7 of the order table) |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** the step is not added to books'
cutover list (`internal/books/cutover/steps.go` is F7-09's); no ABO boot runs it. It runs in tests over the R0-written
corpus.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-08-m6" -b agent/platform-core-f7-08-m6 origin/main
cd "$REPO/.worktrees/pc-f7-08-m6"
git rebase origin/main
```

## Goal

Build books' cutover step M6 in `internal/books/cutover/m6/` as a `datamigrate.DataMigration` with `Cutover: true`,
`Compat: Breaking`, `Mode: BootBlocking`, `Source` = the retired claims it drains, a declared `Parallelism`, chunked
and resumable, **one batch per chunk** that writes the importers' rows and deletes the HEAD rows it decoded
(05-storage §5S.3.13). In order of 05-storage §5S.3.13's M6 row:

1. **Instances** (05-metadata M1.10 migration table): metadata sources → `metadata/<id>`; Deluge → `download/deluge`;
   `ai_endpoints` rows → `ai/<row id>` (`openai_compat` → `core.openai_compat`; whisper adapters → `books.whisper_*`;
   ticks rewritten through books' capability-ID map, an unknown tick kept); the synthesised `ai/openai`; each written
   through `integrations.Importer` with secret fields as `secrets.Ref`s to the names M2 used, Origin `migration`,
   `Local`/`LocalOrigin` and `OperatorSecretOrigin` as "Trust carried over" says, each `Local` grant audited as
   `core.integration.local_migrated` in the chunk's batch; an `auth_ref: openai_api_key` row gets its own key copy.
2. **AI jobs** (05-ai §10): every `aijob:`, `aijob_batch:`, `aijob_payload:` row, terminal ones included, through
   `ai.Importer` with HEAD's ID, payload and `apply_attempts`, state mapped by §10's table, open jobs with their
   `c/ai/ix/open/` row; a type with no catalog capability → `JobFailed`, reason recorded.
3. **Webhooks** (05-notify §10): one `notify/webhook-<n>` per non-empty `urls` entry (n = 1-based position among the
   entries HEAD's parser keeps), adapter `core.webhook`, method `POST`, `Enabled` from `plugins.webhook.enabled`; the
   family HMAC secret **by value** into each instance's `integrations.notify.webhook-<n>.secret`; one rule `webhooks`
   through `notify.Importer` with the mapped books topics (`all` or empty = HEAD's default list); an unknown event
   name reported and dropped.
4. **Roots** (05-library §5L.10): `root_dir` → entry `books-library`; each `import_path:<n>` → `import-<n>` (IDs kept by
   one pure function in books), with `Enabled`, `CreatedAt`, `LastScan`, `BookCount` into books' family
   `rootscan:<root ID>`; `itunes.path_mappings` → `core.library.mappings`; `protected_paths` →
   `core.library.protected_paths`; through `config.Importer`; rows, index rows and `counter:import_path` deleted; an
   unmappable iTunes mapping quarantined; a nested root registered and reported.
5. **Deleted by rule**: `provider_throttle:`/`provider_daily_budget:` rows (M2.10), HEAD's search dirty set
   `idx:sidx:dirty:` (the index is rebuilt at `<db parent>/index/books/books.bleve`, §5S.3.8), then the residual
   `setting:config_blob` row (it retires the claim).

HEAD's rows are decoded only here (books' read-only decoders); core never sees an ABO key name (03 R1).

## Invariants

- **I1. No row lost or doubled** (crash-matrix I1): every source row is either still in its range, or imported or
  quarantined and gone, at every kill point of every chunk.
- **I2. IDs kept**: every instance, import-root and job ID equals HEAD's (or the M1.10 mapping); every books
  reference resolves afterwards.
- **I3. No secret value in any row**; each ref resolves to the value HEAD sent; an undecryptable secret leaves the
  instance kept with `integration_secret_unreadable`, boot continuing.
- **I4. Byte identity elsewhere**: outside the sub-prefixes M6 drains and the `rootscan:` family it writes, every
  range is byte-identical (`TestCutoverLeavesOtherFamiliesByteIdentical`, F2-15's, which excludes exactly
  `rootscan:`).
- **I5. Re-running is a no-op** after completion; a dry run writes nothing and reports per kind.
- **I6. Dark**: the step is not registered in this PR.

## Files

| Action | Path |
|---|---|
| create | `internal/books/cutover/m6/` (the step, its decoders for HEAD's rows, the chunk logic, the dry-run report, tests) |
| create | `internal/books/testdata/r2corpus/m6/` (expected outputs for the corpus; any M6-specific overlay additions) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
# The cutover package and an earlier step to mirror (F3-06's M2, F4-06's M3). Expected: both present
ls internal/books/cutover/ internal/books/cutover/m3/
# The DataMigration type and the step writer. Expected: type DataMigration struct and StepWriter
grep -n 'type DataMigration struct\|type StepWriter' core/storage/datamigrate/*.go
# The four importers M6 calls. Expected: one hit each
grep -n 'type Importer interface' core/integrations/importer.go core/integrations/ai/*.go core/integrations/notify/*.go
grep -rn 'type Importer interface' core/config/*.go
# The R2 corpus and its manifest (F2-12, F2-13). Expected: manifest.json and the overlay
ls internal/books/testdata/r2corpus/manifest.json internal/books/testdata/r2corpus/overlay/
# HEAD's source families (their keys at your base). Expected: each prefix found once in books' key families
grep -n -e 'aijob' -e 'import_path' -e 'provider_throttle' -e 'provider_daily_budget' -e 'sidx:dirty' \
  internal/database/keyfamilies.go 2>/dev/null | head; grep -rn 'func .*rootscan\|"rootscan:' --include='*.go' internal/ | head -2
# M2 left the instance and library fields in the blob (F3-06's rename table marks them for M6). Expected: rows tagged M6
grep -rn 'M6' internal/books/cutover/*.go | head -5
```

STOP if an importer is missing, if F3-06's rename table marks no field for M6, or if `keyfamilies.go` (wherever R1
left it) lacks a family M6 drains.

## Steps

1. Read every spec section in the Spec row in full. 05-metadata M1.10's migration table, 05-notify §10's topic map
   and 05-library §5L.10's roots table are normative: copy them into Go tables verbatim, with a comment citing each.
2. Write the tests first (below) against the R0-written corpus and its overlay; record expected outputs under
   `internal/books/testdata/r2corpus/m6/`.
3. Implement the decoders (books' read-only HEAD row types; they are deleted in R3) and the step: `Source` = the
   retired claims of 05-storage §5S.9.1 that M6 drains; `RewritesInPlace` false; chunked by family, one batch per
   chunk; `Parallelism` declared (the instance and rule families are small: `tasks.Sequential(reason)`; the
   `aijob*` and `import_path:` families are bounded by the corpus size at P1: use `tasks.Fixed(n, reason)` if a chunk
   loop does real work per row, per CLAUDE.md and 09 §9.9); `ExtendStart` per chunk group.
4. Quarantine: an undecodable row, an unmappable iTunes mapping and an importer rejection are quarantined with raw
   bytes (sealed with the keyring for secret-bearing families), counted, and the step goes on.
5. Dry-run mode: per kind, the instances, IDs, carried trust and every row it would quarantine (05-metadata M1.10).
6. Register crash-point labels for every chunk and extend `TestCutoverCrashAtEveryChunk` and
   `TestBootCrashMatrix_ABO` with M6's cells (09 §9.3: a boot writer extends the matrix in its own PR).
7. Leave `internal/books/cutover/steps.go` untouched (F7-09 registers M6 and replaces the M7 and M8 entries). Headers,
   fragment.

## Code contract

Uses 05-storage §5S.3.6's `DataMigration` (copy the field set; a cutover step sets `Cutover`, `Compat: Breaking`,
`Mode: BootBlocking`, `Parallelism`, `Source`, `Step`, and `Sensitive` for the secret-bearing prefixes). Step ID: the
M6 constant F3-06 reserved in `internal/books/cutover/stepids.go` (the one place M2–M6's IDs live; R3-02's
`UpgradeFloor.Requires` reads the same constant). Never coin another ID; if `stepids.go` has no M6 constant, STOP.

## Tests (write first; spec names copied exactly)

Gate tests this row owns:

- **`TestRootsCutover_FromHEADFixture`** (05-library §5L.9).
- **`TestWebhookMigration_InstancesAndRule`** (05-notify §9).
- **`TestIntegrationsMigration_EveryInstanceAndID`** (05-metadata M1.9).
- **`TestAIJobMigration_EveryJobAndState`** (05-ai §9).

The dark half of **`TestBooksIndexRebuiltAtCutover`** (05-storage §5S.10, the search oracle; F7-09 owns the test and
adds the rebuild at the native path and the search-answer comparison, because books' index moves onto
`storage/index` only there): here, M6 drops `idx:sidx:dirty:` and leaves `library.bleve` untouched. Write it in
`internal/books/cutover/m6/` under the spec name, with the two assertions this PR can prove; F7-09 extends it.

The after-M6 clause of **`TestConfigCutover_RowsGone`** (05-config §1.12.2 test table; F3-06 owns the test in
`internal/books/cutover/`): after M6 no `setting:config_blob` remains, and every key range outside the listed keys is
byte-identical. Add it in `internal/books/cutover/m6/` as a test of the same spec name (a second package may declare
it; `go test -run TestConfigCutover_RowsGone ./internal/books/...` runs both halves); do not edit F3-06's file.

Also from the same spec sections: `TestIntegrationsMigration_SecretsByRef`, `TestIntegrationsMigration_TrustCarried`,
`TestIntegrationsMigration_UnreadableSecretReEnter`, `TestIntegrationsMigration_HEADRowsGone`,
`TestAIEndpointMigration_AuthRefKeyCopy`, `TestAIEndpointMigration_TicksRewritten`,
`TestAIJobMigration_UnknownTypeImportedFailed`, `TestWebhookMigration_SecretByValue`,
`TestDelugeMigration_InstanceAndPassword`; M6's cells of `TestCutoverCrashAtEveryChunk`; M6's quarantine cases in
the corpus' quarantine report (exactly the overlay's cases).

Until F7-09 registers the step, these tests run M6 directly through the `storagemigrate` runner on a store already
migrated through the cutover steps registered at your base (M2, and M3 once F4-08 has merged). M4 and M5 are
registered only by F5-18, which is not among this row's dependencies: when F5-18 has not merged, run the M4 and M5
constructors (F5-16, F5-15) directly through `storagemigrate` before M6, and the M3 constructor (F4-06) the same way
if F4-08 has not merged. A constructor whose package is not on your base either is skipped: M6's inputs (the
residual blob, the `aijob*` rows, the webhook settings and the roots) need only M2. M7's target is raised in the test
fixture.

## Acceptance (commands and expected output)

```bash
# The step's tests. Expected: ok, no FAIL
go test -count=1 -race ./internal/books/cutover/m6/...

# The four gate tests, the oracle's dark half and RowsGone's after-M6 clause. Expected: ok
go test -count=1 -run 'TestRootsCutover_FromHEADFixture|TestWebhookMigration_InstancesAndRule|TestIntegrationsMigration_EveryInstanceAndID|TestAIJobMigration_EveryJobAndState|TestBooksIndexRebuiltAtCutover|TestConfigCutover_RowsGone' ./internal/books/...

# M6's step ID comes from stepids.go. Expected: >= 1, and 0 string-literal step IDs in m6/
grep -rn 'stepids\.\|cutover\.StepM6\|M6' internal/books/cutover/m6/*.go | grep -v _test | head -3
grep -rn '"books\.cutover' internal/books/cutover/m6/ | wc -l

# Crash cells. Expected: ok
go test -count=1 -run 'TestCutoverCrashAtEveryChunk|TestBootCrashMatrix_ABO' ./internal/books/...

# Dark: M6 is not in the registered list. Expected: 0
grep -c 'm6\.' internal/books/cutover/steps.go

# Core still names no ABO key. Expected: ok
(cd core && go test -count=1 -run TestCoreHoldsNoAppKeyNames ./archtest/...)

# Gates (the R2 cutover suite runs in extraction-gates). Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- Registering M6 and replacing the M7 and M8 entries with newly named ones (F7-09; the mechanism is order M2's, in
  F3-04 and F3-06).
- The full `TestBooksIndexRebuiltAtCutover` oracle: the rebuild at the native path and the search answers (F7-09).
- Wiring the subsystems and deleting HEAD's code (F7-09).
- The download `ItemRef` books migration (phase 2, 05-download §1).

## Rollback

Revert the PR; the step was never registered, so no store ran it.

## Repo rules

- Worktree off fresh `origin/main`; headers on every file.
- Conventional commit, for example `feat(books): cutover step M6 for integrations, AI jobs, webhooks and roots (dark)`,
  ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never
  `--admin`.
- 09 §9.9 and CLAUDE.md: a library-scale cutover step is a `DataMigration` with its own `tasks.Parallelism` over
  disjoint key ranges; no unbounded goroutines; partition chunks so two workers never touch the same row.

