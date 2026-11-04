<!-- file: docs/specs/platform-core/15-evidence-appendix.md -->
<!-- version: 1.12.1 -->
<!-- guid: 28e48d7f-7c8d-45af-90cd-14880b2b5988 -->
<!-- last-edited: 2026-11-04 -->

# 15 — Evidence appendix

**Evidence commit:** `748fd787a6d922c63dfb3bbb09da578a1418632e`. Every `path:line` anchor in this spec
refers to that commit unless it says otherwise (a few originally cited a docs-only descendant, the same code plus
documentation-only commits).

**Raw data:** [`docs/plans/platform-core/raw/00-index.md`](../../plans/platform-core/raw/00-index.md) —
17 files from read-only scouts and the module-layout experiments, each normalized to repo-relative paths
and scrubbed before commit.

**Review evidence:** round-1 reviews in [`docs/plans/platform-core/reviews/`](../../plans/platform-core/reviews/)
carry their own anchors and, for the modules/build lens, experiments X1–X8 run in scratch directories
(`spec-round-1-modules-build.md`, "Experiments run for this review"). Round-2 reviews
(`spec-round-2-*.md`) add experiments X9–X15 (`spec-round-2-modules-build.md`); X16 was run in a scratch
directory for the round-2 revision and is recorded in 03 §3.2.1. Round-3 reviews (`spec-round-3-*.md`) add
experiments X17–X22 (`spec-round-3-modules-build.md`, "Experiments run for this review": importer sets from
`go list -test -json`, `internal/startup` users, a split checker, root `go fix -diff`, root `go mod tidy -diff`,
an R5 scan of the R1 movers). Round-4 reviews (`spec-round-4-*.md`) add experiments Y1–Y9
(`spec-round-4-modules-build.md`, "Experiments run for this review": importer sets with `ForTest` variants
dropped, the imports of build-tagged files, `internal/telemetry`'s used identifiers, the `internal/logger`
split, `.dockerignore`, the release and CI entry points, the `core/go.mod` template, the npm workspace
layout, both `cockroachdb/errors` versions' genproto edge) and the data-migration lens's rollback
simulations (`spec-round-4-data-migration.md`, "Simulations"). Round-5 reviews (`spec-round-5-*.md`) add
experiments Z1–Z7 (`spec-round-5-modules-build.md`, "Experiments run for this review": importer sets split
into non-test, tagged-only and test-only users, the names crossing the planned taskstore split in both
directions, check (h)'s family-prefix scan over the spec's Go blocks and over the files 04 sends to core,
the `//go:embed` sites, every `npm ci` site, the toolchain pin copies) and the data-migration lens's
forward and rollback simulations (`spec-round-5-data-migration.md`, "Simulations"). Round-6 reviews
(`spec-round-6-*.md`) add the modules-build lens's experiments Y1–Y10 (`spec-round-6-modules-build.md`,
"Experiments": importer sets per root package, the imports of the files sent to the five new paths,
both directions of the taskstore split over test files, the names taskstore takes from
`internal/operations/state`, opsgen's inputs against `RunState`, every `npm run build` site, `.dockerignore`
against the embeds under `embed_frontend`, the toolchain pin copies, op IDs in core code; the round-6 IDs
reuse the letter Y, so cite them as "round-6 Y*n*") and the data-migration lens's simulations
(`spec-round-6-data-migration.md`, "Simulations"). Round-7 reviews (`spec-round-7-*.md`) add the
modules-build lens's experiments Z1–Z10 (`spec-round-7-modules-build.md`, "Experiments": all 213 `in/out`
cells of 04 §3 against `go list -deps -test -json`, the taskstore files' imports, both directions of the
taskstore split over test files, the HEAD-vocabulary `state.*` and `database.*` references in the core-bound
registry, `opsmetrics`, `opsv3` and `pkg/ops/opstest` files, check (h) re-run with setting-key call sites,
`internal/cfaccess`'s layers, every `COPY --from` in both Dockerfiles, every npm install and build site, the
toolchain pin copies; they reuse the letter Z, so cite them as "round-7 Z*n*") and the data-migration lens's
simulations (`spec-round-7-data-migration.md`, "Simulations"). Round-8 reviews (`spec-round-8-*.md`) add the
modules-build lens's experiments Y1–Y11 (`spec-round-8-modules-build.md`, "Experiments": all 213 `in/out` cells of
04 §3 again, the registry engine's files and references, 04 §2.1 (i)'s vocabulary over every root file, check (h)
over struct tags, the build-tagged twins of the *Split* cells, the ABO users of `childop` and `opsmetrics`,
`internal/cfaccess`'s users and layers, every `COPY --from`, an `all:` embed pattern in a throwaway module, the
toolchain pin copies and the `ForTest` variants of `internal/database`; they reuse the letter Y, so cite them as
"round-8 Y*n*") and the data-migration lens's simulations (`spec-round-8-data-migration.md`, "Simulations"). Round-9
reviews (`spec-round-9-*.md`) add the modules-build lens's experiments Z1–Z12 (`spec-round-9-modules-build.md`,
"Experiments": all 213 `in/out` and LOC cells of 04 §3 again, the *Split* cells' per-file LOC, nested-module
darwin builds importing ABO paths, the (GOOS, tag set) pairs a PR compiles, HEAD's `opsmetrics` recorder, the OTel
Prometheus exporter with two scopes of one name, the `http_server_*` golden rows, git-filter-repo 2.47.0's
path-file order (read, not run), the toolchain pin copies and the genproto requirer; they reuse the letter Z, so
cite them as "round-9 Z*n*"), the data-migration lens's simulations (`spec-round-9-data-migration.md`,
"Simulations") and the correctness lens's flow traces (`spec-round-9-correctness.md`, "B. Flow traces"). Round-10 reviews
(`spec-round-10-*.md`) add the modules-build lens's experiments E1–E8 (`spec-round-10-modules-build.md`,
"Experiments": otelhttp v0.72.0's route attribute against a request labeler with and without a request clone, HEAD's
otelgin wiring and position, the contract test's `http_server_*` seeding, HEAD's construction-time histogram views,
`go vet` and `go list` under `embed_frontend` with no built frontend, git-filter-repo 2.47.0's `--dry-run` and
filter-before-callback order, 04 §3's 213 rows again, and the `mediacore_*` family names against HEAD's golden; cite
them as "round-10 E*n*"), the data-migration lens's simulations (`spec-round-10-data-migration.md`, "Simulations")
and the correctness lens's flow traces (`spec-round-10-correctness.md`, "B. Flow traces"). Spec
files cite those experiments by ID where a decision rests on them.

**Rule:** a claim about the current code without an anchor or a raw-file link is a review finding (see
`docs/plans/platform-core/reviews/`). External claims (Sonarr/Radarr behaviour, package versions) cite
public URLs in `raw/16-arr-domain-research.md` and `raw/15-dependencies.md` with check dates.

## Anchor census per spec file

Regenerated on 2026-11-04 **after the D10–D12 consistency pass**, with the round-1 method unchanged, over the revised tree (32 files, 36376 lines, 1083 line anchors). It replaces the round-10 integration census (32 files, 41075 lines, 1327 line anchors); the drop is the D10–D12 revision's deletion of the compatibility design and the evidence that only it needed. The same script reproduces the round-10 figures of every count the revision did not touch (for example 04's 70 anchors and 300 paths, 11's 2 and 2), so the method is unchanged. Method (exact, so the next run is comparable):

- every backticked span in the file is tokenized into path-shaped tokens
  (`[A-Za-z0-9_.-]` segments joined by `/`, optionally followed by `:<line>[-<line>][,<line>…]`);
- a token counts as a **repo path** only if it names a file or directory that exists in the tree of
  the evidence commit `748fd787a` and contains a `/` or a `.` (so `core/...` paths that do not exist
  yet, and bare words, are not counted);
- "Anchors with line" counts every occurrence of such a path followed by `:<line>`; "Distinct repo
  paths" counts distinct paths; "Raw files cited" lists every `raw/NN-<name>.md` mentioned anywhere in
  the file;
- 15 itself and README are excluded, as before.

The v1.0.0 table used a looser, unrecorded method, so its numbers are not comparable with these.

| Spec file | Lines | Anchors with line | Distinct repo paths | Raw files cited |
|---|---|---|---|---|
| [01-summary.md](01-summary.md) | 186 | 0 | 3 | 00-baseline-build, 01-package-inventory |
| [02-decision-record.md](02-decision-record.md) | 226 | 1 | 8 | 00-baseline-build, 05-storage, 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 10-auth-rbac, 11-integrations, 12-cross-cutting, 16-arr-domain-research |
| [03-architecture.md](03-architecture.md) | 1135 | 17 | 52 | 00-baseline-build, 00-index, 03-layering-guard, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting, 14-build-ci |
| [04-package-mapping.md](04-package-mapping.md) | 1996 | 70 | 300 | 01-package-inventory, 02-import-graph-hotspots, 03-layering-guard, 04-database-store, 05-storage, 06-tasks-ops, 07-plugin-registry-sdk, 09-http-api, 10-auth-rbac, 11-integrations, 12-cross-cutting |
| [05-ai.md](05-ai.md) | 975 | 33 | 30 | 11-integrations, 12-cross-cutting |
| [05-authn.md](05-authn.md) | 1863 | 78 | 58 | 09-http-api, 10-auth-rbac, 12-cross-cutting, 13-frontend |
| [05-backup.md](05-backup.md) | 1358 | 17 | 16 | 05-storage, 10-auth-rbac, 12-cross-cutting |
| [05-config.md](05-config.md) | 2324 | 64 | 49 | 02-import-graph-hotspots, 03-layering-guard, 10-auth-rbac, 12-cross-cutting, 15-dependencies |
| [05-download.md](05-download.md) | 821 | 15 | 14 | 06-tasks-ops, 11-integrations, 16-arr-domain-research |
| [05-events.md](05-events.md) | 810 | 12 | 11 | 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting |
| [05-health.md](05-health.md) | 798 | 29 | 29 | 09-http-api, 11-integrations, 12-cross-cutting |
| [05-http-api.md](05-http-api.md) | 1597 | 81 | 50 | 09-http-api, 10-auth-rbac, 12-cross-cutting, 13-frontend |
| [05-indexer.md](05-indexer.md) | 496 | 3 | 3 | 11-integrations, 16-arr-domain-research |
| [05-library.md](05-library.md) | 858 | 47 | 48 | 02-import-graph-hotspots, 05-storage, 08-prior-art, 12-cross-cutting, 16-arr-domain-research |
| [05-logging.md](05-logging.md) | 681 | 39 | 24 | 05-storage, 10-auth-rbac, 12-cross-cutting, 15-dependencies |
| [05-metadata.md](05-metadata.md) | 1594 | 59 | 44 | 11-integrations, 12-cross-cutting, 16-arr-domain-research |
| [05-metrics.md](05-metrics.md) | 690 | 28 | 34 | 12-cross-cutting, 15-dependencies |
| [05-notify.md](05-notify.md) | 524 | 20 | 15 | 10-auth-rbac, 11-integrations, 12-cross-cutting, 16-arr-domain-research |
| [05-rbac.md](05-rbac.md) | 1546 | 61 | 32 | 09-http-api, 10-auth-rbac, 12-cross-cutting, 13-frontend |
| [05-realtime.md](05-realtime.md) | 536 | 16 | 12 | 09-http-api, 12-cross-cutting, 13-frontend |
| [05-storage.md](05-storage.md) | 2930 | 61 | 49 | 02-import-graph-hotspots, 04-database-store, 05-storage, 08-prior-art, 12-cross-cutting |
| [05-tasks.md](05-tasks.md) | 2247 | 66 | 88 | 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting |
| [05-updater.md](05-updater.md) | 646 | 21 | 11 | 07-plugin-registry-sdk, 12-cross-cutting, 14-build-ci |
| [06-app-sdk.md](06-app-sdk.md) | 2893 | 60 | 67 | 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting |
| [07-ui-shell.md](07-ui-shell.md) | 1818 | 36 | 34 | 08-prior-art, 09-http-api, 13-frontend, 14-build-ci, 15-dependencies |
| [08-arr-facade.md](08-arr-facade.md) | 673 | 10 | 13 | 09-http-api, 10-auth-rbac, 16-arr-domain-research |
| [09-abo-on-core.md](09-abo-on-core.md) | 1241 | 54 | 78 | 00-index, 05-storage |
| [10-tv-movies-proof.md](10-tv-movies-proof.md) | 1100 | 8 | 7 | 08-prior-art, 11-integrations, 16-arr-domain-research |
| [11-multi-app.md](11-multi-app.md) | 147 | 2 | 2 | — |
| [12-repo-split.md](12-repo-split.md) | 819 | 10 | 35 | 08-prior-art, 14-build-ci, 15-dependencies |
| [13-toolchain.md](13-toolchain.md) | 395 | 58 | 49 | 00-baseline-build, 06-tasks-ops, 14-build-ci, 15-dependencies |
| [14-risks-open-questions.md](14-risks-open-questions.md) | 453 | 7 | 17 | 00-baseline-build, 04-database-store, 08-prior-art |
| **Total** | 36376 | 1083 | — | — |

## Key evidence by decision

| Decision / claim | Evidence |
|---|---|
| `go vet` and `go list` under `embed_frontend` fail with `pattern web/dist: no matching files found` on a tree with no built frontend, which root `make ci` never builds, so `vet-darwin` runs without the tag and the tag-union guards use `go list -e` (R10-9) | `web_embed.go:11`, `Makefile:785`; round-10 E5; MOD10-8, OPS10-8; 03 §3.5.1 item 4 |
| git-filter-repo's `--dry-run` changes nothing, and path filtering runs before the commit callback with a per-filename cache, so the history assertion runs on a real rewrite and re-created origins get a callback pass with no path filter (R10-9) | git-filter-repo 2.47.0 manual and script, read only; round-10 E6; MOD10-9; 12 §12.8.1 |
| No PR job compiles `GOOS=darwin` (goreleaser builds it at release, `CGO_ENABLED=0`, tag `embed_frontend`) or the `integration` tag (`vet-integration` is called by neither `make ci` nor a workflow), so the guards and a vet job must cover both (R9-10) | `.goreleaser.yml:23-36`, `Makefile:362-369`, `Makefile:785`; round-9 modules-build Z3–Z6; MOD9-3; 03 §3.5.1 item 4, 13 §13.3 |
| git-filter-repo applies `--paths-from-file` directives in file order, tests a filter line against the path as renamed so far, and drops every unwanted path once any filter line exists, so the history file lists the pre-move renames before the `core/` line (R9-10) | git-filter-repo 2.47.0 source, read only; round-9 modules-build Z10; MOD9-4; 12 §12.8.1 |
| HEAD's factory reset deletes the Open Library data and every entry of the library `RootDir` after wiping the store and before resetting settings, so a staged reset must place those side effects after the restart, where they are not undoable (R7-2, owner question Q-K); each entry it cannot remove is only a warning, so the post-reset hook completes with a reported remainder instead of failing (R8-1) | `internal/server/handlers/system/handler.go:551` (`store.Reset()`), `:507-536`, `:558-560` (`deleteOLDataLocked`), `:562-580` (`RootDir` wipe, dangerous-root refusal `:565-569`, per-entry `slog.Warn` `:571-577`), `:583-588` (settings reset); COR7-1, DATA7-6, COR8-4, OPS8-5; 14 Q-K |
| HEAD has a second whole-store wipe, `ResetSystem` (`POST /system/reset`), which wipes the store and resets settings but deletes no file, so the reset has two kinds, `ResetStore` and `ResetFactory` (R8-1) | `internal/server/handlers/system/handler.go:463-491` (`store.Reset()` `:479`, `config.ResetToDefaults()` `:485`); COR8-1; 03 §3.3 `internal/storagehttp`, 14 Q-K |
| The only `legacy_op_id` in core-bound scheduler code is a struct tag, so R1(c) matches struct tags per key | `internal/scheduler/maintenance.go:285`; round-8 modules-build review Y4; MOD8-4; 03 R1(c) |
| `go list -e` reports an unbuilt `//go:embed all:web/dist` as a package-relative `pattern all:web/dist`, so the embed check normalises the pattern before comparing it with a module-relative list | round-8 modules-build review Y9; MOD8-11; 03 §3.2 item 4 |
| Both Dockerfiles carry a comment naming `golang:1.27.2-alpine` above the pinned `FROM` line, a prose copy of the pin | `Dockerfile:25`, `Dockerfile.build-cgo:21`; round-8 modules-build review Y10; MOD8-12; 13 §13.2.2 |
| Both Dockerfiles copy the binary into the runtime stage with `COPY --from=go-builder`, so the embed cross-check reads only copies into a `go build` stage under its `WORKDIR`; the image build's tag set is the combined `embed_frontend fts5 native_taglib` | `Dockerfile:68`, `:74-75`, `:90`; `Dockerfile.build-cgo:65`, `:75`; round-7 modules-build review Z8; MOD7-5, MOD7-14; 03 §3.2 item 4, §3.5.1 item 4 |
| `internal/plugins/maintenance` is layer 4 and calls the owner check that moves into `internal/cfaccess`, which imports `internal/httputil` (layer 5 from R1), so books injects the check as a func | `internal/arch/layering_test.go:227`; round-7 modules-build review Z7; MOD7-4; 03 §3.5.1 item 1 |
| The scheduler persists `setting:maintenance_window_last_run` as a `YYYY-MM-DD` string, so the R2 tasks migration converts that kind's clock with HEAD's date codec rather than resetting it (R7-8; 14 E16) | `internal/scheduler/maintenance.go:53`, `:71`; round-7 modules-build review Z6; MOD7-3; 03 §3.3 store table |
| The core-bound registry, `childop`, `opsmetrics`, `opsv3` and `opstest` files use about 90 HEAD-vocabulary `state.*` references and v2 row types, so no import-path change compiles them: they are re-implementations, not moves (R7-8) | round-7 modules-build review Z4, Z5; MOD7-1, MOD7-2; 03 R1(c), §3.5.1; 12 §12.8.1 |
| 23 of the 35 shell- and ui-kit-bound files import `@mui/icons-material`, so it is a peer of both packages | `web/src/components/layout/TopBar.tsx` and others; `web/package.json:29`; FE7-11; 13 §13.4 |
| HEAD's `SetAPIKeyStatus` read-modify-writes the key row without the `apiKeyMu` that `TouchAPIKeyLastUsed` holds for its own whole-row write-back, so a request with a key can overwrite that key's revocation; the sixth early fix makes every `apikey:` writer take the lock (R6-5) | `internal/database/pebble_store_auth.go:1226-1247` (`SetAPIKeyStatus`, no lock), `:1284-1302` (`TouchAPIKeyLastUsed` under `apiKeyMu`); SEC6-3; 02 N13, 05-authn §11.1, 14 E17 |
| An install that ever saved settings with auth off holds `enable_auth=false` in its blob, because HEAD's save marshals the whole effective config, and that value differs from the default `true`; so the R2 config migration carries it, and for the auth keys the more restrictive of that stored value and an operator value wins and is reported (14 E5) | `internal/config/config.go:1558` (`enable_auth` blob field), `:2801` (default `true`), `internal/config/persistence.go:1932-1977` (`SaveConfigToDatabase`); SEC6-1; 03 §3.3 `config`, 14 K15 |
| `web/dist` is embedded under `embed_frontend` but excluded from the Docker context and produced in the image by an earlier stage, so the embed rule must allow listed in-image outputs | `web_embed.go:11`, `.dockerignore:20`, `Dockerfile:68`; round-6 modules-build review Y8; MOD6-3; 03 §3.2 item 4 |
| On Docker and launchd the default backup directory and the fallback state directory sit beside the database, so a whole-volume snapshot rollback reverts store, journal and markers together | `internal/backup/backup.go:145`, `internal/config/state_dir.go:143-154`; SEC6-10; 14 §14.2, §14.5 |
| HEAD resolves permissions by exact string over the role's stored list, so an unknown string grants nothing; but worker confinement is a negative "nothing else" test, so an extra string beside `fingerprint.worker` un-confines a custom worker role, and an empty API-key scope list widens to every role permission. The R2 identity migration therefore maps atoms to native scopes by one table that keeps a worker role confined, and a key never has empty scopes | `internal/server/middleware/auth.go:430-455` (`EffectivePermissionsFor`), `:271-297` (`workerOnly`, `abortWorkerOnlyOutsideWorkerAPI`), `:405-408` (`intersectPermissions`, empty scopes); SEC4-3; 02 decision 13 |
| An enabled Basic gate with an empty stored password admits `<username>:` with an empty password, so early fix 5 and the core Basic gate refuse a gate enabled with an empty password, and the R2 secrets migration never writes an empty Basic password | `internal/server/middleware/basicauth.go:81-87` (`subtle.ConstantTimeCompare` of two empty slices is 1); SEC4-1, SEC5-4; 02 decision 13 |
| HEAD already stores four secrets encrypted in its own setting rows (OpenAI key, Google Books key, Hardcover token, Basic password) and blanks them in the blob, so the R2 secrets migration decrypts them with `.encryption_key` and re-encrypts them under the keyring, and `.encryption_key` stays until R3 so that a rollback restore still reads them (14 E4) | `internal/config/persistence.go:1955-1958` (blanked in the blob), `:1989-2003` (`SetSetting(…, true)`); OPS5-2; 02 decision 13, 03 §3.3 `secrets` |
| The shipped compose file always sets `BASIC_AUTH_ENABLED` (default `false`), so an env value that locked its key would switch off a gate enabled in the UI; from R2 compose passes the Basic variables only when they are set, and for the Basic-gate keys the more restrictive value wins (R5-1; 14 E5) | `docker-compose.yml:20-22`; OPS5-1; 03 §3.3 `config`, 14 K15 |
| `library:` is an ABO key family and `KeyFamilies()` is exported, so check (h) / R1(c) reads the claim generator's output and treats a prefix followed by a space as a Go error string, not a key | `internal/database/keyfamilies.go:126`, `:330`; round-5 modules-build review Z3, Z4; MOD5-3, MOD5-4; 03 R1(c) |
| Every `//go:embed` today sits in a `*.go` file, so a path filter cannot catch an embed input outside the Docker context; `check_embed_context.py` reads `EmbedFiles` instead | round-5 modules-build review Z5; MOD5-7; 03 §3.2 item 4 |
| `make build` runs `npm run build` directly, so the UI build ID must be generated by the npm build itself (R5-7) | `Makefile:154` (`build: web-build`), `:198-201`; FE5-1; 03 §3.6 "Build identity" |
| HEAD restores by extracting the archive into the live DB directory, so the rollback restore runs offline, and a DB directory copied in place keeps its directory identity and needs a content marker to be classified as replaced | `internal/backup/backup.go:777-850`; SEC5-6; 14 K16, 05-backup §5B.3.6 |
| A default-tag `go list` lists build-tagged files only under `IgnoredGoFiles`, without imports, so a tool that needs every file's imports, such as the embed check, takes the union of every CI tag set | round-4 modules-build review Y2; 03 §3.5.1 item 4 |
| Dependabot gomod joins the top-level `multi-ecosystem-groups` entry; the repo uses no `groups` key, so `applies-to` cannot be set there | `.github/dependabot.yml:8-20,59-64`; MOD4-15; 03 §3.2 item 6 |
| HEAD's updater swaps any newer binary with no store check, so an upgrade constraint is a boot refusal of the new binary (exit 78), and automatic promotion is paused during the R2 flips (D12 (a)) | `internal/updater/updater.go:214-280` (`DownloadAndReplace`); DATA4-18; 05-updater §5U.1, 14 E15 |
| `cmd` is a non-test user of `internal/startup`, so `internal/startup` stays in ABO until `Registrar.StartupSteps` exists (R3-3) | round-3 modules-build review X17, X18; `cmd/root.go:274`; 03 §3.4 visibility rule |
| The root has pending `go fix` changes in 245 files, so the root gains no `go fix` gate; a move PR applies `go fix` to the files it moves in a commit after its move commit, so rename detection keeps their history (A-4) | round-3 modules-build review X20; 03 §3.2 item 4, 13 §13.3, 12 §12.8.1 |
| Root `go mod tidy -diff` is clean at HEAD, so per-module `tidy-check` can be enabled in R1 | round-3 modules-build review X21 |
| `internal/telemetry` installs OTel process globals and `logtest` calls `slog.SetDefault`, so neither moves into core verbatim (R5) | round-3 modules-build review X22; `internal/logger/logtest/logtest.go:83`; 03 R5 |
| An excluded module version never appears in `go mod graph`, so the genproto exclude check cannot probe the live graph | round-2 modules-build review X10; 03 §3.2 item 1 |
| `go mod graph` works in workspace mode and shows the requirer edge once the exclude is dropped; a requirer edge alone does not make the build fail | X16 (round-2 revision, 03 §3.2.1) |
| A core-only `go mod tidy` leaves the root red (`missing go.sum entry`): the tidy-coupling rule | round-2 modules-build review X11; 03 §3.2 item 4 |
| `go mod tidy` writes no `go.sum` for a module without requirements; an empty committed one is kept | round-2 modules-build review X12; 03 §3.2 item 4 |
| filter-repo aborts on colliding renamed paths: history paths come from real renames only | round-2 modules-build review X13; 12 §12.8.1 |
| ABO imports react-router in 122 files (52 source, 70 test) | `git grep -lE "from ['\"]react-router(-dom)?['\"]" 748fd787a -- web`; 13 E4 |
| `go.work` breaks the build; root cause; one-line `exclude` fixes it | `raw/00-baseline-build.md` E1, E7, E8–E10 |
| The exclude belongs in `core/go.mod`, not the root (a root-only exclude fails for workspaces inside core once core holds Pebble + OTLP; a core-only exclude is green) | modules-build review X2, X3; 03 §3.2 item 1 |
| `go install pkg@version` refuses a providing module with `replace` **or** `exclude` | modules-build review X4 (`cmd/go/internal/load/pkg.go:3313-3400` in the Go source) |
| Docker module-download layer fails once the root `replace`s `./core` | modules-build review X1; `Dockerfile:54-55`, `Dockerfile.build-cgo:52-53` |
| Go's `internal/` rule is path-based: a module under the core path may import core internals | modules-build review X5 |
| A tag on the release commit takes over `git describe` (the reported version) | modules-build review X6; `Makefile:9` |
| apidiff module-mode output forms used by the API-stability gate's filter | modules-build review X7 |
| Nested module + `replace` builds and vets in both modules | `raw/00-baseline-build.md` E2c |
| `database.Store` = 445 methods in 6 composites; 591 narrowed consumer interfaces | `raw/04-database-store.md` §1–§4 |
| 258 Pebble key families, 256 prefixed; none under `m/` or `c/` | `raw/05-storage.md` §3; [05-storage](05-storage.md) |
| 213 packages; 59 generic / 104 audiobook / 50 mixed | `raw/01-package-inventory.md` |
| `database` drags 7 audiobook packages into every importer; `config` imports 5 domain packages | `raw/02-import-graph-hotspots.md` |
| Layering guard assumptions (floor 150, control edge, `layerOf` completeness, `allowed`) | `raw/03-layering-guard.md`, `raw/00-baseline-build.md` E4 |
| Four job generations; v3 SDK has zero native defs; `RunItems` semantics | `raw/06-tasks-ops.md` §0, §2 |
| 111 non-test `init()`; serviceregistry global factory map | `raw/07-plugin-registry-sdk.md` §1, §5 |
| 497 + 75 routes; 36 authenticated routes without permission; unauthenticated plugin mount | `raw/09-http-api.md` §1, §4, §8 |
| 13 hard-coded permission atoms; admin checked by role name; gaps G1–G15 | `raw/10-auth-rbac.md` §8 |
| Download clients concrete-only; `plugin.DownloadClient` has no implementors | `raw/11-integrations.md` §4 |
| Process globals that collide in one binary | `raw/12-cross-cutting.md` §9 |
| Static nav and routes; hand-written 8.7k-line API client | `raw/13-frontend.md` |
| `make ci` steps; build tags; ~40 Go-pin copies | `raw/14-build-ci.md`; `make ci` has 13 prerequisites (`Makefile:785`) |
| Task-kind grammar `[a-z][a-z0-9-]*` segments fits every existing op ID (269 IDs, none with `_`) | `internal/server/testdata/op_ids.golden` |
| ABO exports 74 `audiobook_organizer_*` metric families, 87 series rows with the 10 `ai_*` and 3 `http_server_*` rows (the round-1 figure 73 was one short, MOD9-8); names may change in R2, with the dashboards and rules that reference them (14 E13) | `internal/telemetry/contract/testdata/series.golden` (`grep -c '^audiobook_organizer_'` prints 74); 03 §3.6, 05-metrics §2 |
| Layering guard floor, control edge | `internal/arch/layering_test.go:38,40`; new in-module control `. → cmd` (`main.go:12`) |
| Latest versions (Go go1.27.3; npm majors; `openapi-typescript` 7.13.0 and `openapi-fetch` 0.17.0, both published 2026-02-11) | `raw/15-dependencies.md`, [13](13-toolchain.md) §13.4 (`npm view … time`, checked 2026-11-04) |
| Acquisition machinery is media-neutral; *arr v3 endpoints used by ecosystem clients | `raw/16-arr-domain-research.md` |
| Prior decisions and gates | `raw/08-prior-art.md` |

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- MOD-1, MOD-3, MOD-4, MOD-5, MOD-13 — key-evidence rows for review experiments X1–X7 (Docker layer, exclude placement, `go install` refusal, path-based `internal/`, `git describe`, apidiff output).
- MOD-14, COR-26, SIMP-13, MOD-8 — key-evidence rows for the new layering control edge, op-ID grammar, the 73 metric families, and `make ci`'s 13 prerequisites.
- (all) — anchor census regenerated with a recorded method.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: anchor census regenerated with the documented method once every file had settled (32 files, 21998 lines, 842 line anchors).

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- MOD2-5, MOD2-6, MOD2-15, MOD2-12 — key-evidence rows for X10, X16, X11, X12, X13 (exclude check, tidy coupling, empty `go.sum`, history paths).
- FE2-19 — key-evidence row for the react-router count (122; 52 source, 70 test).
- (all) — anchor census regenerated with the unchanged method during the round-2 revision (32 files, 23903 lines, 867 line anchors; snapshot for files still under revision).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): Anchor census regenerated with the unchanged method after the round-2 integration pass (32 files, 25683 lines, 912 line anchors).

Round 3 (dispositions in `docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- R3-1 — key-evidence row: exact-match permission resolution and the empty-scope widening (`internal/server/middleware/auth.go:430-455`, `:405-408`), the premise of one stored format per family.
- R3-3 (MOD3-1), MOD3-6, MOD3-3 — key-evidence rows for round-3 experiments X17/X18 (`cmd` is a non-test user of `internal/startup`), X20 (245 root files with pending `go fix`), X21 (root tidy clean) and X22 (R5 scan of the S1/S2 movers); review-evidence paragraph names X17–X22.
- FE3-20 — key-evidence row: `openapi-typescript` 7.13.0 and `openapi-fetch` 0.17.0 publish dates (13 §13.4).
- (all) — anchor census regenerated with the unchanged method during the round-3 revision (32 files, 26383 lines, 933 line anchors; snapshot for files still under revision).

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): anchor census regenerated with the unchanged method after every round-3 reviser had finished (32 files, 27754 lines, 969 line anchors).

Round 4 (dispositions in `docs/plans/platform-core/reviews/spec-round-4-dispositions.md`; R4-1 refines R3-1):

- SEC4-3 (R4-1) — the R3-1 key-evidence row is corrected: exact-match resolution, but worker confinement is a negative test (`auth.go:271-297`), so native-only scopes stay out of legacy rows.
- SEC4-1, DATA4-1, OPS4-1 (R4-2) — key-evidence row: an empty stored Basic password admits an empty presented one (`basicauth.go:81-87`).
- MOD4-1 (R4-4) — key-evidence row: `.dockerignore` excludes `docs` and root `*.md` (Y5).
- MOD4-7 — key-evidence row: build-tagged files are invisible to a default-tag `go list` (Y2).
- MOD4-15 — key-evidence row: Dependabot's `multi-ecosystem-groups`, no `groups` key.
- DATA4-18 — key-evidence row: pre-S0 updaters swap binaries unchecked (`updater.go:214-280`).
- FE4-2 (disposition note) — key-evidence row: the legacy SPA's `/invite/<token>` route (`App.tsx:201-206`).
- MOD4-14 — `go fix` key-evidence row: moves recorded in `core/scripts/split/origins.txt`.
- (all) — review-evidence paragraph names round-4 experiments Y1–Y9 and the data-migration simulations; anchor census regenerated with the unchanged method during the round-4 revision (32 files, 29791 lines, 1005 line anchors; snapshot while other files were still under revision).

Integration pass (round 4):

- integration pass (round 4): anchor census regenerated with the unchanged method after every round-4 reviser and the integration pass had finished (32 files, 30564 lines, 1033 line anchors).

Round 5 (dispositions in `docs/plans/platform-core/reviews/spec-round-5-dispositions.md`; resolutions R5-1…R5-7):

- R5-1 (OPS5-1, OPS5-2) — key-evidence rows: HEAD's four encrypted secret rows (`persistence.go:1955-1958`, `:1989-2003`) and compose's always-set `BASIC_AUTH_*` (`docker-compose.yml:20-22`).
- R5-2 (MOD5-1, MOD5-2) — key-evidence rows: the bidirectional taskstore split (Z2) and `internal/config` → `internal/database` (`persistence.go:915`).
- MOD5-3, MOD5-4 — key-evidence row: `library:` is an ABO family and `KeyFamilies()` is exported (`keyfamilies.go:126`, `:330`; Z3, Z4).
- MOD5-7 — key-evidence row: every embed sits in a `*.go` file (Z5).
- R5-7 (FE5-1) — key-evidence row: `make build` runs `npm run build` directly (`Makefile:154`, `:198-201`).
- SEC5-6 — key-evidence row: HEAD's in-place restore into the live DB directory (`backup.go:777-850`).
- (all) — review-evidence paragraph names round-5 experiments Z1–Z7 and the data-migration simulations; anchor census regenerated with the unchanged method during the round-5 revision (32 files, 31994 lines, 1077 line anchors; snapshot while other files were still under revision).
- integration pass (round 5): anchor census regenerated with the unchanged method after every round-5 reviser and this pass had finished (32 files, 32644 lines, 1098 line anchors); replaces the in-revision snapshot.

Round 6 (dispositions in `docs/plans/platform-core/reviews/spec-round-6-dispositions.md`; resolutions R6-1…R6-7):

- SEC6-3 (R6-5), SEC6-1 (R6-6), MOD6-3, MOD6-1 (R6-2), SEC6-4, SEC6-10 — key-evidence rows: the unlocked `SetAPIKeyStatus` against the locked `TouchAPIKeyLastUsed` (`pebble_store_auth.go:1226-1247`, `:1284-1302`); `enable_auth=false` persisted by the whole-config save (`config.go:1558`, `:2801`, `persistence.go:1932-1977`); `web/dist` embedded but produced in the image (`web_embed.go:11`, `.dockerignore:20`, `Dockerfile:68`); taskstore's import of `internal/operations/state` (round-6 Y2, Y5, Y6); Pebble's per-open `OPTIONS-<n>` file (`go.mod:7`); volume-default backup and state directories (`backup.go:145`, `state_dir.go:143-154`).
- (all) — review-evidence paragraph names the round-6 experiments (modules-build Y1–Y10, cited as "round-6 Y*n*") and the data-migration simulations; anchor census regenerated with the unchanged method during the round-6 revision (32 files, 33779 lines, 1118 line anchors; snapshot while other files were still under revision).
- integration pass (round 6): anchor census regenerated with the unchanged method after every round-6 reviser and this pass had finished (32 files, 34636 lines, 1135 line anchors); replaces the in-revision snapshot.

Round 7 (dispositions in `docs/plans/platform-core/reviews/spec-round-7-dispositions.md`; resolutions R7-1…R7-8):

- R7-2 (COR7-1, DATA7-6) — key-evidence row: HEAD's factory reset deletes the Open Library data and the library `RootDir` contents (`handler.go:551-588`), the basis of owner question Q-K.
- MOD7-5, MOD7-14 — key-evidence row: runtime-stage `COPY --from=go-builder` lines and the image's combined tag set (`Dockerfile:68`, `:74-75`, `:90`; `Dockerfile.build-cgo:65`, `:75`; round-7 Z8).
- MOD7-4 — key-evidence row: `internal/plugins/maintenance` is layer 4 (`layering_test.go:227`; round-7 Z7).
- MOD7-3 (R7-8) — key-evidence row: `maintenance_window_last_run` and its date format (`maintenance.go:53`, `:71`; round-7 Z6).
- MOD7-1, MOD7-2 (R7-8) — key-evidence row: the HEAD-vocabulary references that make the registry engine a re-implementation (round-7 Z4, Z5).
- FE7-11 — key-evidence row: the icons imports in the shell-bound files (`web/package.json:29`).
- (all) — review-evidence paragraph names the round-7 experiments (modules-build Z1–Z10, cited as "round-7 Z*n*") and the data-migration simulations; anchor census regenerated with the unchanged method during the round-7 revision (32 files, 35870 lines, 1153 line anchors; snapshot while other files were still under revision).
- integration pass (round 7): review-evidence paragraph names `pkg/ops/opstest`; anchor census regenerated with the round-1 method after the integration edits.

Round 8 (dispositions in `docs/plans/platform-core/reviews/spec-round-8-dispositions.md`; resolutions R8-1…R8-6):

- R8-1 (COR8-1, COR8-4, OPS8-5) — key-evidence rows: HEAD's second whole-store wipe `ResetSystem` deletes no file (`handler.go:463-491`), the basis of `ResetStore`; the factory reset's per-entry removal is warn-and-continue (`:571-577`).
- R8-6 (MOD8-2, MOD8-3), MOD8-4, MOD8-11, MOD8-12 — key-evidence rows for round-8 Y4 (struct-tag `legacy_op_id`, `maintenance.go:285`), Y5 (`reflink_darwin.go`), Y6 (ABO users of `childop` and `opsmetrics`), Y9 (`all:` embed pattern) and Y10 (Dockerfile pin comments); the re-implementation row names `childop`.
- (all) — review-evidence paragraph names the round-8 experiments (modules-build Y1–Y11, cited as "round-8 Y*n*") and the data-migration simulations; anchor census regenerated with the unchanged method during the round-8 revision (32 files, 37462 lines, 1203 line anchors; snapshot while other files were still under revision).
- integration pass (round 8): anchor census regenerated with the round-1 method after every round-8 edit (32 files, 37967 lines, 1209 line anchors); replaces the in-revision snapshot.

Advisor (dispositions in `docs/plans/platform-core/reviews/spec-advisor-dispositions.md`; resolutions A-1…A-17):

- A-2 (ADV-3, ADV-S1) — key-evidence rows for `enable_auth=false`, worker confinement, the empty Basic password and HEAD's four encrypted secrets restated for the representation-neutral extraction (no extension family, no sentinel, HEAD's auth precedence kept).
- A-4 (ADV-S3) — the build-tag-twin row deleted (the move taxonomy is gone); the tag-set union, `internal/startup` and `go fix` rows restated without the stage-visibility gate and `origins.txt`.
- (all) — anchor census regenerated with the round-1 method during the advisor revision (snapshot while other files were still under revision).
- integration pass (advisor): anchor census regenerated with the round-1 method after every advisor edit and this pass (32 files, 37725 lines, 1227 line anchors); replaces the in-revision snapshot; 05-config's precedence cutover cited as H3 (09 §9.13).

Round 9 (dispositions in `docs/plans/platform-core/reviews/spec-round-9-dispositions.md`; resolutions R9-1…R9-11):

- MOD9-8 — the metric-family row: 74 `audiobook_organizer_*` families (87 rows), not 73.
- R9-1 (COR9-1, DATA9-2), R9-3 (MOD9-1, MOD9-2), R9-10 (MOD9-3, MOD9-4) — key-evidence rows: HEAD's identity mutex held across its Pebble write and the per-instance lock fields; `opsmetrics.New`'s unconditional attribute-less `ops.zombies`; otelgin's `http_route`; no PR job for darwin or `integration`; filter-repo's path-file order.
- (all) — review-evidence paragraph names the round-9 experiments (modules-build Z1–Z12, cited as "round-9 Z*n*"), the data-migration simulations and the correctness flow traces; anchor census regenerated with the round-1 method during the round-9 revision (snapshot while other files were still under revision).
- integration pass (round 9): anchor census regenerated with the round-1 method over the final round-9 tree (32 files, 39730 lines, 1279 line anchors); replaces the in-revision snapshot.

Round 10 (dispositions in `docs/plans/platform-core/reviews/spec-round-10-dispositions.md`; resolutions R10-1…R10-11):

- R10-3 (MOD10-1, MOD10-2, MOD10-6) — key-evidence row: otelgin's `http_route`, constant `server_address`, position and HEAD's construction-time views; otelhttp's override (round-10 E1, E2, E4).
- R10-9 (MOD10-8, MOD10-9, OPS10-8) — key-evidence rows: the embed failure under `embed_frontend` with no built frontend (round-10 E5); filter-repo's `--dry-run` and filter-before-callback order (round-10 E6).
- (all) — review-evidence paragraph names the round-10 experiments (modules-build E1–E8, cited as "round-10 E*n*"), the data-migration simulations and the correctness flow traces; anchor census regenerated with the round-1 method during the round-10 revision (32 files, 40363 lines, 1299 line anchors; snapshot while other files were still under revision).
- integration pass (round 10): anchor census regenerated with the round-1 method over the final round-10 tree (32 files, 41075 lines, 1327 line anchors); replaces the in-revision snapshot.

- D10–D12 revision (2026-11-04): evidence rows that supported only deleted mechanisms are deleted, not marked
  (lock parity across core's commit, the `ops.zombies` and otelgin metric parity rows, opsbridge's v2 bodies,
  taskstore's imports and split, the Pebble `OPTIONS-<n>` open stamp, the legacy config store package, the
  compat-ledger embed, the SPA invite-link reader). Rows whose HEAD fact still holds keep it with the D10
  conclusion (the maintenance-window clock is converted by the R2 tasks migration; `enable_auth=false` under the
  fail-closed precedence; worker confinement in the atom-to-scope table; HEAD's encrypted rows re-encrypted in
  R2 with `.encryption_key` kept until R3; compose's Basic variables; HEAD's live restore and the offline
  rollback restore; HEAD's unchecked updater swap; metric names may change). Citations of deleted 14 rows
  (K18, K21, K22, K25) repointed; stage references re-keyed. The anchor census above is the round-10 one; the
  lead re-runs it after the D10–D12 revision. Consistency pass: the anchor census is re-taken over the revised tree (36376 lines, 1083 line anchors), one experiment description re-keyed from S1/S2 to R1, and D12's answers cited as D12 (a)–(d).

