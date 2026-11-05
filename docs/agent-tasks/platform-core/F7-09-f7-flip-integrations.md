<!-- file: docs/agent-tasks/platform-core/F7-09-f7-flip-integrations.md -->
<!-- version: 1.0.0 -->
<!-- guid: 92d3d884-998e-42de-94e1-451dd63836fb -->
<!-- last-edited: 2026-11-05 -->

# F7-09 — F7 flip, one PR: integrations, AI jobs, provider gates, library roots and notify wired, M6 registered, internal/plugin and the webhook plugin deleted, deliveries in core's format, the AI families renamed with their dashboards, the late-owner holds and poll on their native bodies

| Field | Value |
|---|---|
| ID | F7-09 |
| Release | R2, flip F7 (the flip PR) |
| Tier | **T4** (Opus / xhigh–max): every flip PR is T4; this one moves about fifteen packages and carries M6 |
| Wave | W34 (alone: the only PR of its wave touching `main.go`, `internal/server/` and books' registration). Runs inside an announced **move window** |
| Depends on | F6-03, F7-02, F7-03, F7-04, F7-05, F7-06, F7-07, F7-08 |
| Spec | 09 §9.4 R2 flips table row F7, §9.6 rows 5, 15 and 16, §9.2.3 (M6), §9.2.7 (the AI renames); 03 §3.8 F7; 05-notify §10; 05-ai §8, §10; 05-library §5L.10; 05-metadata M1.10, M2.10, M3.10; 05-download §10; 05-storage §5S.3.8; 05-metrics §13 item 3; 05-tasks §5T.8.2 (the late-owner kinds `core.integrations.release-holds` and `core.ai.batch-poll`); 05-http-api §8.1 (F7 row: `/api/v1/plugins` deleted); 04 §8.1 F7, §8.3 (the `internal/plugin` deletion gate); 06 §6.4.10 F7 row |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **One atomic PR** (09 §9.2.1): it wires the
subsystems, registers M6, deletes what they replace and switches every first-party caller in the same PR. It merges
alone in W34. The R2 freeze is on: do not touch `.github/workflows/prerelease.yml`.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-09-flip" -b agent/platform-core-f7-09-flip origin/main
cd "$REPO/.worktrees/pc-f7-09-flip"
git rebase origin/main
npm ci --prefix web
```

Announce the move window one working day ahead, naming every 04 row listed under "04 keys" below.

## Goal (09 §9.4 R2 flips table, row F7; 06 §6.4.10 F7 row)

1. **Wire** in the kernel and books: `Manifest.Requires`, `Manifest.Library`; `Registrar.Adapter`,
   `Registrar.AICapability`, `Registrar.DerivedIndex`; `Deps.Storage.Indexes`, `Deps.Storage.Derived`, `Deps.Library`,
   `Deps.HTTP`, `Deps.Integrations`, `Deps.Downloads`, `Deps.AI`, `Deps.Notify`; `app.Options.Adapters` in ABO's `main`
   (`deluge.Descriptor()`, `notifymail.Descriptor()`, `webhook.Descriptor()`, the `core.openai_compat` adapter); books'
   adapters `books.<id>` (metadata sources, whisper) and its 13 AI capabilities with the capability-ID map.
2. **Register M6** in `internal/books/cutover/steps.go`, raise M7's target in the same PR and register a new M8 whose
   step ID names M6 as the last step it compacts after (03 §3.8; IMPL-13).
3. **Delete** `internal/plugin` with no importer left (04 §8.3), the webhook plugin `internal/plugins/webhook`, and the
   unauthenticated `/api/v1/plugins` mount (05-http-api §8.1 F7 row); books publishes its 12 event types through
   `Deps.Notify` in the same batch as the change (05-notify §10's topic map); deliveries are in core's signed format
   (09 §9.6 row 5).
4. **AI**: `internal/aidispatch` and `internal/ai/aijobs` move into `core/integrations/ai`; `internal/ai`'s client half
   into the `openai` adapter (its feature methods stay books'); HEAD's `ai.*`/`ai_dispatch.*` families deleted and
   replaced by `<namespace>_ai_*` with every reference in `deploy/grafana/dashboards/*.json` and
   `deploy/prometheus/{alert-rules,recording-rules,alert-rules.test}.yml` updated in this PR (05-ai §8; 09 §9.2.7);
   `/api/v1/ai/capabilities`, `/ai/endpoints/status`, `/ai/test-connection` deleted, the SPA on the core routes.
5. **Provider gates**: HEAD's `internal/metadata/providerhttp`, `dailyquota`, `throttle*`, `circuitbreaker` replaced by
   core `providerhttp`; the metadata chain on `integrations/metadata` (the `switch src.ID` deleted, 05-metadata M3.10);
   `/api/v1/metadata/providers/throttles` deleted, the SPA and scripts on instance health and `DELETE …/hold`.
6. **Library roots**: books reads roots through `library.Scope`; `ImportPathStore` deleted; scanner, autoscan and the
   filesystem handlers write `rootscan:<root ID>`; the browse route under `core:library.browse` mounted, HEAD's browse
   API and the `fileops` `init()` registration deleted; `internal/watcher` → `Manager.Watch`.
7. **Search**: books' index on `storage/index` at `<db parent>/index/books/books.bleve`, rebuilt at cutover;
   `internal/search` moves to `core/internal/bleveindex` and `core/storage/index` (books keeps document building and
   query parsing); memdb, hnsw and chromem registered as `DerivedIndex`.
8. **Late-owner kinds** (05-tasks §5T.8.2): `integrations/providerhttp` and `integrations/ai` extend the registrations of
   `core.integrations.release-holds` and `core.ai.batch-poll` with their native bodies; `providerhttp.InterimHolds`,
   `ai.InterimPoll`, books' two interim backends and `InterimBackends`' holds and poll fields deleted (06 §6.4.10).
9. **Config**: the class-i `/api/v1/config` handlers deleted as the instance routes replace them.

04 keys this PR moves: `F7 move internal/ai/aijobs -> mediacore/integrations/ai`; `F7 move internal/aidispatch ->
mediacore/integrations/ai`; `F7 move internal/config -> mediacore/integrations/ai`; `F7 move internal/metadata ->
mediacore/integrations/providerhttp`; `F7 move internal/metadata/providerhttp -> mediacore/integrations/providerhttp`;
`F7 move internal/ai -> mediacore/integrations/ai/openai`; `F7 move internal/deluge ->
mediacore/integrations/download/deluge`; `F7 move internal/plugins/webhook -> mediacore/integrations/notify/webhook`;
`F7 move internal/search -> mediacore/internal/bleveindex`; `F7 move internal/search -> mediacore/storage/index`;
`F7 move internal/server -> mediacore/storage/index`; `F7 move internal/fileops -> mediacore/library`;
`F7 move internal/pathutil -> mediacore/library`; `F7 move internal/watcher -> mediacore/library`;
`F7 delete internal/plugin`.

## Invariants

- **I1.** The search oracle `TestBooksIndexRebuiltAtCutover` passes end to end (rebuild at the native path, answers
  equal R0's).
- **I2.** The webhook delivery test (`TestWebhook_JSONBodyGolden`, `TestWebhook_SignatureWithTimestamp`) and
  `TestWebhookMigration_SecretByValue` pass: a receiver keeps its secret and verifies core's signature.
- **I3.** `TestRootsCutover_FromHEADFixture`, `TestIntegrationsMigration_EveryInstanceAndID`,
  `TestAIJobMigration_EveryJobAndState` pass on the registered step; the R2 cutover suite (F2-15's count, ID,
  reference and byte-identity checks) is green with M6 registered.
- **I4.** Every series a shipped dashboard or rule names is served (the dashboard-reference test) and `promtool test
  rules` passes after the AI renames; a plain gather has no duplicate family (`TestABOMetricsGatherNoDuplicate`) and
  only `<namespace>_ai_*` AI families plus books' `ai.parse_results` (`TestNoHEADAIFamilies`).
- **I5.** `internal/plugin` is deleted with no importer left; nothing serves `/api/v1/plugins`.
- **I6.** `TestCutoverRefusedByOlderBuilds` still refuses with the pre-freeze binary on a store migrated through M6;
  the crash matrix covers M6 and the new M8.
- **I7.** Every first-party caller of a deleted route is switched in this PR (SPA, `scripts/`, skills, docs).

## Files

As the row lists: `main.go`, `internal/books/module.go`, `internal/books/cutover/steps.go`, `internal/books/store/`,
`internal/plugin/`, `internal/plugins/webhook/`, `internal/server/`, `internal/ai/`, `internal/aidispatch/`,
`internal/deluge/`, `internal/search/`, `internal/scanner/`, `internal/fileops/`, `internal/pathutil/`,
`internal/watcher/`, `internal/metadata/`, `internal/config/`, `core/app/options.go`, `web/src/`,
`deploy/grafana/dashboards/`, `deploy/prometheus/`, `.mockery.yaml`, `internal/arch/layering_test.go`,
`scripts/ci/short_test_weights.json`, and one `changelog.d/` fragment. **Plus the importers outside those paths that
the re-verify block finds** (see Planner notes): the PR does not build while any importer of a moved or deleted
package remains (04 §8.3).

## Re-verify before editing (expected results at your base)

Evidence counts are at HEAD `f7ac5b11e`; regenerate them and record both in the PR.

```bash
M=github.com/falkcorp/audiobook-organizer
# 1. internal/plugin importers (the deletion gate, 04 §8.3). [evidence: 20 non-test files in 10 packages, two of them
#    the generated mocks in internal/server/handlers/mocks]
git grep -l "\"$M/internal/plugin\"" -- '*.go' | grep -v _test.go
# 2. Importers of every moved package outside this row's paths. [evidence:
#    internal/plugin: internal/dedup, internal/itunes/service, internal/quarantine;
#    internal/ai/aijobs: internal/dedup; internal/aidispatch: internal/dedup, internal/telemetry/contract, internal/transcribe;
#    internal/deluge: internal/maintenance/jobs, internal/plugins/deluge; internal/search: internal/audiobooks, internal/playlist;
#    internal/metadata/providerhttp: internal/catalog, internal/metafetch; internal/metadata/dailyquota: internal/metafetch;
#    internal/fileops and internal/pathutil: about 25 packages, of which only the moved symbols' callers change]
for p in internal/plugin internal/ai/aijobs internal/aidispatch internal/deluge internal/search internal/metadata/providerhttp internal/metadata/dailyquota internal/fileops internal/pathutil internal/watcher; do
  echo "== $p"; git grep -l "\"$M/$p\"" -- '*.go' | grep -v "^$p/" | xargs -n1 dirname 2>/dev/null | sort -u; done
# 3. HEAD's AI metric families and the files that name them. [evidence: 19 non-test Go references;
#    deploy/grafana/dashboards/audiobook-organizer-ai.json, deploy/prometheus/alert-rules.yml, alert-rules.test.yml]
grep -rn 'ai_dispatch\|"ai\.\(requests\|failures\|tokens\|request\.duration\)' --include='*.go' internal/ | grep -v _test | wc -l
grep -l 'ai_dispatch\|ai_requests' deploy/grafana/dashboards/*.json deploy/prometheus/*.yml
# 4. Routes this PR deletes. [evidence: wire_metadata_routes.go:58-60 throttles; wire_media_routes.go:50 ai/test-connection;
#    handlers/aibackends ai/capabilities and ai/endpoints/status; handlers/plugins.go /api/v1/plugins*]
grep -rn '"/metadata/providers/throttles\|"/ai/test-connection\|"/ai/capabilities\|"/ai/endpoints/status\|plugins' \
  --include='*.go' internal/server/wire_*.go | grep -v _test
# 5. The dark packages and the M6 step. Expected: each present; M6 not yet in steps.go
ls core/integrations/integrations.go core/integrations/ai/openai/ core/integrations/download/deluge/ \
   core/integrations/notify/webhook/ core/library/transfer.go core/storage/index/ internal/books/cutover/m6/
grep -c 'm6' internal/books/cutover/steps.go
# 6. The interim late-owner backends to delete. Expected: InterimHolds, InterimPoll in core and ABO's main
grep -rn 'InterimHolds\|InterimPoll' --include='*.go' core/ internal/ main.go | grep -v _test
```

STOP if any dark package or M6 is missing (grep 5), or if grep 2 finds an importer you cannot re-point inside this PR
(report it; the PR must leave no importer of a deleted package).

## Steps

1. Run the re-verify block; paste grep 1 and 2 into the PR description as the importer checklist.
2. Write or extend the tests first (below).
3. Wire the kernel handles and books' registrations (Goal 1); ABO's `main` sets `app.Options.Adapters`.
4. Register M6, raise M7's target, register the new M8 (Goal 2); extend the crash matrix with the new M8 and
   `TestCutoverRefusedByOlderBuilds`'s cells.
5. Move or delete each package of the 04 keys with `git mv` where code moves; re-point every importer; delete
   `internal/plugin` last, once grep 1 is empty; regenerate or delete `internal/server/handlers/mocks` and update
   `.mockery.yaml`.
6. AI renames with the dashboards and rules (Goal 4); `promtool test rules`.
7. Switch the SPA and first-party scripts off every deleted route (Goals 4, 5, 6, 9).
8. Delete the interim late-owner backends (Goal 8).
9. Layering keys, shard weights, headers, fragment; `go build ./... && (cd core && go build ./...)`; acceptance.

## Code contract

No new public API beyond F7-01 … F7-08's. ABO's adapter list (05-metadata M1.3):

```go
app.Options{
	Adapters: []integrations.Descriptor{deluge.Descriptor(), notifymail.Descriptor(), webhook.Descriptor() /* , the openai_compat descriptor */},
}
```

## Tests (write first; spec names copied exactly)

- `TestBooksIndexRebuiltAtCutover` end to end (extend F7-08's test to the rebuild and the answers).
- `TestNoHEADAIFamilies` (05-ai §9).
- The dashboard-reference test and `promtool test rules` after the AI renames.
- `TestABOMetricsGatherNoDuplicate` (unchanged).
- `TestCutoverRefusedByOlderBuilds` and the R2 cutover suite with M6 registered.
- `TestInternalPluginDeleted` (name coined here): `go list ./...` contains no `internal/plugin` package and the router
  answers 404 for `GET /api/v1/plugins`.

## Acceptance (commands and expected output)

```bash
M=github.com/falkcorp/audiobook-organizer
# internal/plugin gone with no importer. Expected: nothing / 0
ls internal/plugin internal/plugins/webhook 2>/dev/null
git grep -l "\"$M/internal/plugin\"" -- '*.go' | wc -l

# HEAD's AI families gone from code and deploy files. Expected: 0 and 0
grep -rn 'ai_dispatch\|"ai\.\(requests\|failures\|tokens\|request\.duration\)' --include='*.go' internal/ core/ | grep -v _test | wc -l
grep -l 'ai_dispatch' deploy/grafana/dashboards/*.json deploy/prometheus/*.yml | wc -l
promtool test rules deploy/prometheus/alert-rules.test.yml

# Interim backends for holds and poll gone. Expected: 0
grep -rn 'InterimHolds\|InterimPoll' --include='*.go' core/ internal/ main.go | wc -l

# Gate tests. Expected: ok
go test -count=1 -run 'TestBooksIndexRebuiltAtCutover|TestRootsCutover_FromHEADFixture|TestWebhookMigration_InstancesAndRule|TestIntegrationsMigration_EveryInstanceAndID|TestAIJobMigration_EveryJobAndState|TestNoHEADAIFamilies|TestABOMetricsGatherNoDuplicate|TestCutoverRefusedByOlderBuilds|TestDeployReferencedSeriesServed' ./...
(cd core && go test -count=1 -run 'TestWebhook_JSONBodyGolden|TestWebhook_SignatureWithTimestamp' ./integrations/notify/...)

# Builds, frontend and e2e. Expected: exit 0; green
go build ./... && (cd core && go build ./...)
make web-build && make web-test && make test-e2e

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

The required checks of 09 §9.2.1, the R2 cutover suite and `unit-lifecycle` included (this PR changes `main.go`),
must be green on the PR.

## Out of scope

- Moving organizer, versions, quarantine, Deluge import and iTunes transfer onto `library.Transfer` (after R2).
- The download phase 2, the AI usage ledger, the notify inbox (deferred-build).
- Backup, health, the updater and the kernel `/metrics` mount (F8-04).

## Rollback

Before R2 ships: revert the PR (atomic). After R2 ships: `make rollback-restore` (09 §9.2.1).

## Repo rules

- Worktree off fresh `origin/main`; `npm ci --prefix web`; headers bumped on every file; moved files keep their guid.
- The PR description lists every `old ==> new` package path (09 §9.2.10) and the importer checklist.
- One PR per rename: the AI series, their dashboards, rules and rule tests change together (09 §9.2.7).
- Conventional commits ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
  never squash; never `--admin` past a required check. No release or flip trailer.
- Push workflow changes, if any, with `git push`.

## Planner notes

- **Importers outside the row's files.** At the evidence checkout the packages this flip moves or deletes are imported
  from paths the row does not list: `internal/plugin` from `internal/dedup`, `internal/itunes/service` and
  `internal/quarantine` (the event publishers that must switch to `Deps.Notify`); `internal/aidispatch` from
  `internal/dedup`, `internal/transcribe` and `internal/telemetry/contract`; `internal/ai/aijobs` from `internal/dedup`;
  `internal/deluge` from `internal/maintenance/jobs` and `internal/plugins/deluge`; `internal/search` from
  `internal/audiobooks` and `internal/playlist`; `internal/metadata/providerhttp` and `dailyquota` from
  `internal/metafetch` and `internal/catalog`; and `internal/fileops`/`internal/pathutil` from about 25 packages (only
  the callers of the moved symbols change). The flip is alone in W34, so adding them collides with nothing, but the
  row's file list understates the PR. Some of these importers may have moved or gone by F7's base (F5-18 and F6-03 run
  earlier); grep 2 decides.
- **Integrations HTTP surface.** This flip deletes the class-i `/api/v1/config` handlers "as the instance routes replace
  them"; no row builds those instance routes (see F7-01's Planner notes).
