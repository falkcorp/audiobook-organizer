<!-- file: docs/agent-tasks/platform-core/PC-16-06-s7-flip-plugin-deleted-notify-routes.md -->
<!-- version: 1.0.0 -->
<!-- guid: bb8296a1-c3b1-49b2-9fb0-06aec4f82370 -->
<!-- last-edited: 2026-11-04 -->

# PC-16-06 — S7 flip: `internal/plugin` deleted; publishers go through `Deps.Notify`; the root store's field-merge conversion; generic integration routes and aliases

| Field | Value |
|---|---|
| ID | PC-16-06 |
| Title | S7 flip: internal/plugin deleted; publishers go through Deps.Notify; the root store's field-merge conversion; generic integration routes and aliases |
| Tier | **T3 — Opus / high.** Deletes a dependency imported by 20 files in 10 packages, moves webhook delivery onto the outbox in the same batch as each state change, and changes the settings home of every integration. |
| Wave | W16 (stage S7, release `S7`) — **the flip PR**, last in the wave |
| Depends on | PC-16-05 |
| Trailer | `Stage-Release: S7 flip` (sets `shipped_stage: S7` and `release: S7` together) |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §1, §3 (S7 books-side), §5 rules 9, 10, 14–17, §6 rows 7–8, §7 S7 |
| Owning spec | 09 S7 (all rows: "Flip PR", "Moves", "Data/keys", "Notify", "Config", "Endpoints", "Rollback", "Done when"); 05-notify §10; 05-library §5L.10; 05-metadata M1.3 HTTP surface; 09 §9.2.1 rows 7–8, "Soaks"; 09 §9.3 "Staging" |

**Gate.** A flip PR changes `release:`: `check_soak.py` check (a) requires a current `S6` row at least 14 days old
(OWNER-19). Required checks on this PR: `compat-rehearsal` and `unit-lifecycle` (every flip PR) besides the usual set.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-16-06" -b feat/pc-16-06-s7-flip origin/main
cd "$REPO/../abo-pc-16-06"
git worktree list
make web-deps
```

## Goal

Make S7 truth in one PR:
1. **Delete `internal/plugin`** with the webhook plugin. Its registry, route mount and handlers go; books publishes
   its 12 legacy event types through `module.Deps.Notify` (`Publish(ctx, b, n)`, outbox-backed, staged in the batch
   of the state change, refused for topics books does not own). `plugin.DownloadClient` (0 implementors) goes with it.
2. **Field-merge conversion.** Books' scanner, autoscan and filesystem handlers update a root's
   `LastScan`/`BookCount` through the root store's field-merge method (`GetForUpdate` + `RetryOnConflict`, only those
   two fields), never a whole-struct rewrite; the root store keeps books' memdb `import_paths` table current.
3. **Generic integration routes** in `core/internal/integrationshttp` (05-metadata M1.3 HTTP surface) with window
   aliases for the generic parts of `/api/v1/ai`, `/api/v1/deluge` and `/api/v1/metadata-sources`; books' Deluge
   behaviour routes stay on the legacy mount.
4. **Config aliases** for the integration keys, ledger rows, `core.json`, the staging cases, and the marker.

## Files

Create: `core/internal/integrationshttp/`.

Modify:
- `internal/plugin/` — deleted.
- `internal/server/plugins_init.go` — deleted or emptied of the plugin registry and blank imports.
- `internal/server/` — every importer of `internal/plugin` in this package; `folder_autoscan_op.go` (field merge).
- `internal/scanner/` — `service.go`'s `UpdateImportPath` call (field merge).
- `internal/books/module.go` — publishers via `Deps.Notify`; `Manifest.Requires` where needed.
- `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` — alias rows (C7) and config/env alias rows.
- `core/openapi/core.json` — regenerated with the integration routes.
- `scripts/tests/test_staging_scrub.py` — the S7 cases.
- `ci/stage.txt` — `shipped_stage: S7`, `release: S7`.

See Planner notes: the importer list of `internal/plugin` spans more packages than the row names.

## Re-verify before editing

Counts at `748fd787a` (unchanged at this plan's HEAD); informative (09 §9.3): record the value generated at your
base in the PR description and in the stage record.

```bash
M=github.com/falkcorp/audiobook-organizer
git grep -l "\"$M/internal/plugin\"" -- '*.go' | grep -v _test.go | wc -l     # 20 files
git grep -l "\"$M/internal/plugin\"" -- '*.go' | grep -v _test.go | xargs -n1 dirname | sort -u | wc -l   # 10 packages:
#   internal/dedup internal/itunes/service internal/plugins/webhook internal/quarantine internal/server
#   internal/server/handlers internal/server/handlers/audiobooks internal/server/handlers/dedup
#   internal/server/handlers/metadata internal/server/handlers/mocks (generated: regenerate or delete)
git grep -c 'EventType = ' -- internal/plugin/events.go                       # 12
git grep -n 'type DownloadClient interface' -- internal/plugin/plugin.go      # 1
git grep -n 'UpdateImportPath(' -- '*.go' | grep -v _test | grep -v 'func \|interface\|UpdateImportPath(id int' # 5 call sites:
#   internal/importer/path_service.go:51, internal/scanner/service.go:677, internal/server/folder_autoscan_op.go:147,
#   internal/server/handlers/filesystem.go:247, internal/server/handlers/filesystem.go:319
git grep -n 'dg := protected.Group("/deluge")' -- internal/server/deluge_integration.go     # 1 (:139)
git grep -n 'POST("/metadata-sources/test"' -- internal/server/wire_media_routes.go        # 1

# Prerequisites (expect >= 1 each)
git grep -n 'func (s \*Service) For(module string) Publisher' -- core/integrations/notify | head -1   # PC-16-04
git grep -n 'func LibraryCoverID' -- core/legacy | head -1                                            # PC-16-05
grep -n '^shipped_stage: S6$' ci/stage.txt && grep -n '^release: S6$' ci/stage.txt                     # both 1
```

Generate the alias counts from the route manifest generator (PC-04-01) at your base: the generic `/api/v1/ai`
routes (09: 14 at the evidence commit), `/api/v1/deluge` (6) and `/api/v1/metadata-sources`. Classify each route
as **generic** (test connection, status, capabilities, endpoint health: becomes a core instance route plus alias) or
**books behaviour** (torrent listing, discovery, import, scans, parse: stays on the legacy mount). Write the table in
the PR description. A route you cannot classify from 09 S7 "Endpoints", 05-ai §10 and 05-download §10: STOP and ask.

## Steps

1. Read 09 S7 in full; 05-notify §4 and §10; 05-library §5L.10 (the `ImportPathStore` row); 05-metadata M1.3
   (HTTP surface table, `Put`/`Test` rules); 05-config §1.12.4 (alias registration) and C7/C11; 09 §9.2.9 (ledger
   rows); 09 §9.3 "Staging" (c); 09 §9.10 R-test.
2. Write the tests below first.
3. **Publishers.** For each of the 12 legacy event types, find every `plugin.EventBus` publish call (via the
   importer list) and replace it with `deps.Notify.Publish(ctx, b, n)` staged in the batch that commits the state
   change, using the topic mapping of 05-notify §10 verbatim (`book.imported` → `books.book.imported.v1`, …,
   `book.unquarantined` → `books.book.unquarantined.v1`). Where HEAD published after the commit with no batch, move
   the publish into the batch that writes the change; if a call site has no batch to join, STOP and report it.
4. Delete `internal/plugins/webhook`, the plugin registry, route mount and handlers, and `internal/plugin`.
   Regenerate (or delete) the mocks in `internal/server/handlers/mocks`. `go build ./...` must pass with no ABO import
   of `internal/plugin`.
5. **Field merge.** Convert the stats writers to a root-store field-merge method that reads the row with
   `GetForUpdate`, changes only `LastScan` and `BookCount`, and retries on conflict (`RetryOnConflict`), keeping the
   memdb `import_paths` table current in the same code: `internal/scanner/service.go:677`,
   `internal/server/folder_autoscan_op.go:147`, `internal/server/handlers/filesystem.go:247,319`. Classify
   `internal/importer/path_service.go:51`: if it writes only statistics, convert it the same way; if it is a library
   edit, route it through `legacy.LibraryRootSource.Update`. Un-skip PC-16-05's skipped half of
   `TestImportRootsStayLegacyFormat`.
6. **Routes.** Build `core/internal/integrationshttp` with the routes and permissions of 05-metadata M1.3's HTTP
   table: `GET /api/core/v1/integrations/adapters?kind=`, `GET, POST /api/core/v1/integrations/{kind}/instances`,
   `GET, PUT, DELETE .../instances/{id}`, `POST /api/core/v1/integrations/{kind}/test`, `.../instances/{id}/test`,
   `GET .../instances/{id}/health`, `DELETE .../instances/{id}/hold`. Map `ErrFieldNotStorable` to 422,
   `ErrFieldLocked` to 409, a ProtectAdmin refusal to 403, `ErrDisabled` on `Test` to 409. Register window aliases
   (`httpapi.Alias`) for the generic legacy routes classified above, each counted on the compat counter, and the
   `/api/v1/metadata/providers/throttles` alias (05-metadata M2.10). Regenerate `core/openapi/core.json`.
7. **Config.** `deluge_*`, `download_client`, `openai_*`, `ai_endpoints*`, `smtp_*`, `metadata_sources`,
   `plugins.webhook.*` stay in the blob as instance fields; register window aliases for their YAML and env names;
   `OPENAI_API_KEY`/`OPENAI_BASE_URL` are **permanent** env aliases; `deluge_move_enabled` etc. live under
   `modules.books.download.*` (verify PC-16-04 did it; else do it here).
8. **Ledger.** Add the C7 alias rows and the config/env alias rows to `docs/compat-surfaces.md`; refresh the embedded
   copy `internal/books/compat-surfaces.md` (`TestCompatLedgerCopyFresh`).
9. **Staging.** Add the S7 cases to `scripts/tests/test_staging_scrub.py`: `core.integrations.disabled` is set on
   every S7+ staging boot, and the scrubbed fixture booted with S6's `prev` attempts no outbound connection.
10. **Marker.** In `ci/stage.txt` set `shipped_stage: S7` and `release: S7` in this PR, nowhere else.
11. Draft the release-note lines for the coordinator (PR description), verbatim from 09 §9.2.1: row 7 "Webhooks
    queued at rollback are delivered after the upgrade, or dropped after 24 h." and row 8 "Integrations created on
    this release do nothing after a rollback; events during the rollback are not delivered to them."

## Code contract

Routes and permissions: 05-metadata M1.3 "HTTP surface" table (copy it). Publishing (05-notify §3):

```go
func (s *Service) For(module string) Publisher            // books gets For("books"); foreign topics → ErrForeignTopic
// Publisher.Publish(ctx context.Context, b *storage.Batch, n Notification) error
```

The 12 topics, verbatim (05-notify §10): `books.book.imported.v1`, `books.book.deleted.v1`,
`books.metadata.applied.v1`, `books.tags.written.v1`, `books.file.organized.v1`, `books.dedup.detected.v1`,
`books.dedup.merged.v1`, `books.cover.changed.v1`, `books.read-status.changed.v1`, `books.scan.completed.v1`,
`books.book.quarantined.v1`, `books.book.unquarantined.v1`.

## Tests to write first (09 S7 Done when; names from the spec unless marked)

- The build: `go build ./...` with `internal/plugin` gone; a check that no ABO file imports it.
- `test ! -e core/integrations/indexer && test ! -e core/arrfacade && test ! -e core/acquire`.
- Through the routes: `TestPut_LegacyBackedCreateNeedsProtectAdmin`, `TestAuthOff_LegacyBackedIntegrationWritableAsHEAD`,
  `TestPut_LegacyBackedDestinationNeedsProtectAdmin`, `TestPut_LegacyBackedAuthRefNeedsProtectAdmin`, plus an HTTP
  case "a legacy-backed instance at a private IP literal reaches its stored destination as at HEAD" (name coined
  here: `TestS7_LegacyBackedPrivateIPReachesDestination`) and the 422 case
  `TestIntegrationsProblem_FieldNotStorableIs422` (name coined here, unless PC-16-01 already added it).
- Publishing: `TestPublish_SameBatchAsCallerState` for one books event end to end (books state change → outbox
  entry in the same commit → `core.webhook` delivery with `legacy_headers` byte-equal to PC-16-04's golden), and
  `TestPublisher_ForeignTopicRefused` for books.
- Metrics: `TestABOMetricsGatherNoDuplicateOrMissing` (with books' AI recorder), `TestHistogramBucketsPinned`; the
  seeded series golden diff adds only `<namespace>_ai_*` relative to S6 (no line of HEAD's golden changes).
- Ledger: `TestCompatLedgerMatchesCode`, `TestCompatLedgerCopyFresh`, `TestCompatLedgerHoldsOnlyRows`,
  `TestNoContractBeforeSunset`, `TestLegacyEnvDecodeMatchesHEAD`, `TestLegacyFamiliesWrittenOnlyByLegacyStores`,
  `TestLegacyStoreSuites`, `TestLegacyRoleReachUnchanged`, `TestWorkerKey_NoAmbientScopes`.
- Staging: the S7 cases of `scripts/tests/test_staging_scrub.py`.
- R-test rows (in `make compat-rehearsal`): "a Deluge URL edited on prev is effective on roll-forward"; "a Deluge
  password saved on cur is used by prev; changed there, it is effective on roll-forward"; "a rename by `library`
  during a scan survives the scan's end"; "a webhook pending at rollback is delivered once on roll-forward or
  dead-lettered after the max age"; and the §9.2.1 row 8 cell (natively created integrations inert on `prev`).
- Crash matrix: if this PR adds a boot-time durable write, extend `TestBootCrashMatrix_ABO` (or core's
  `TestBootCrashMatrix`) and 06 §6.5.1's writer table in this PR.

## Acceptance

```bash
test ! -e internal/plugin && echo deleted                                       # deleted
git grep -n '/internal/plugin"' -- '*.go' | wc -l                               # 0
test ! -e core/integrations/indexer && test ! -e core/arrfacade && test ! -e core/acquire && echo none   # none
grep -c '^shipped_stage: S7$' ci/stage.txt; grep -c '^release: S7$' ci/stage.txt   # 1 and 1
go build ./... && (cd core && go build ./...)                                   # exit 0
go test ./internal/... -run 'TestABOMetricsGatherNoDuplicateOrMissing|TestHistogramBucketsPinned|TestCompatLedger|TestLegacyStoreSuites|TestLegacyFamiliesWrittenOnlyByLegacyStores|TestWorkerKey_NoAmbientScopes' -count=1 2>&1 | grep -E '^(FAIL|--- FAIL)'   # no output
python3 -m pytest scripts/tests/test_staging_scrub.py -q                        # all passed
make compat-rehearsal                                                           # exit 0 (both PREV_REF rows)
make ci                                                                         # exit 0
make -C core ci                                                                 # exit 0
```

On the PR: `extraction-gates`, `compat-rehearsal`, `unit-lifecycle`, `docker-image`, `vet-darwin`,
`vet-integration`, `core-ci` all green on an up-to-date branch.

## Out of scope

Deferred-build packages (`integrations/indexer`, `arrfacade`, `acquire`, grab/monitor code) — they must not exist;
`pkg/plugin/sdk` and `internal/serviceregistry` (S10); the `ItemRef` books migration (phase 2); any cutover of a
legacy integration family; UI changes (no UI call moves at S7 unless one does, in which case cells a/b apply).

## Rollback

Binary swap to S6 (09 S7 Rollback): S6 reads Deluge, AI, SMTP and webhook settings and secrets from the blob and
secret rows, which S7 writes in place in HEAD's format; webhook deliveries pending in `c/outbox/` are not sent by
S6 and are delivered once on roll-forward or dead-lettered past `core.notify.max_age` (row 7); natively created
instances do nothing on S6 (row 8). A revert of this flip is admitted only as the exact `git revert` of its commits.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Versioned headers on new or changed files (the ledger files included); one headerless `changelog.d/` fragment.
- Conventional commits, each with `Stage-Release: S7 flip` (never also `S7 1`) and
  `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- The flip has a wide blast radius: write the dated executive summary in this PR if
  `docs/process/executive-summaries.md`'s criteria hold.
- CLAUDE.md concurrency and "Fix it right": a call site that needs a different shape to join a batch is fixed
  properly, not wrapped in a fire-and-forget publish.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- The row's files omit most importers of `internal/plugin` (09 S7, 04 §8.3): `internal/dedup/`,
  `internal/itunes/service/`, `internal/quarantine/`, `internal/plugins/webhook/` (deleted),
  `internal/server/handlers/`, `internal/server/handlers/audiobooks/`, `internal/server/handlers/dedup/`,
  `internal/server/handlers/metadata/`, `internal/server/handlers/mocks/`; and the field-merge call sites
  `internal/server/handlers/filesystem.go` (05-library §5L.10 names it) and possibly
  `internal/importer/path_service.go:51` (not named by the spec; classify it). None of these is in another W16 row
  after PC-16-04, so adding them creates no collision.
- `scripts/staging/provision.sh` (and `scripts/boot_budget.py`'s documented preparation) must set
  `core.integrations.disabled=true` in the operator layer for every S7+ staging boot (09 §9.3 (c)); the row lists only
  the test file.
