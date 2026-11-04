<!-- file: docs/specs/platform-core/15-evidence-appendix.md -->
<!-- version: 1.5.0 -->
<!-- guid: 28e48d7f-7c8d-45af-90cd-14880b2b5988 -->
<!-- last-edited: 2026-11-04 -->

# 15 — Evidence appendix

**Evidence commit:** `748fd787a6d922c63dfb3bbb09da578a1418632e`. Every `path:line` anchor in this spec
refers to that commit unless it says otherwise (a few cite `57fd77ffc`, which is the same code plus
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
an R5 scan of the S1/S2 movers). Round-4 reviews (`spec-round-4-*.md`) add experiments Y1–Y9
(`spec-round-4-modules-build.md`, "Experiments run for this review": importer sets with `ForTest` variants
dropped, the imports of build-tagged files, `internal/telemetry`'s used identifiers, the `internal/logger`
split, `.dockerignore`, the release and CI entry points, the `core/go.mod` template, the npm workspace
layout, both `cockroachdb/errors` versions' genproto edge) and the data-migration lens's rollback
simulations (`spec-round-4-data-migration.md`, "Simulations"). Round-5 reviews (`spec-round-5-*.md`) add
experiments Z1–Z7 (`spec-round-5-modules-build.md`, "Experiments run for this review": importer sets split
into non-test, tagged-only and test-only users, the names crossing the planned taskstore split in both
directions, check (h)'s family-prefix scan over the spec's Go blocks and over the files 04 sends to core,
the `//go:embed` sites, every `npm ci` site, the toolchain pin copies) and the data-migration lens's
forward and rollback simulations (`spec-round-5-data-migration.md`, "Simulations"). Spec files cite those
experiments by ID where a decision rests on them.

**Rule:** a claim about the current code without an anchor or a raw-file link is a review finding (see
`docs/plans/platform-core/reviews/`). External claims (Sonarr/Radarr behaviour, package versions) cite
public URLs in `raw/16-arr-domain-research.md` and `raw/15-dependencies.md` with check dates.

## Anchor census per spec file

Regenerated on 2026-11-04 **during the round-5 revision**, from the working tree with the round-1 method
unchanged (32 files, 31994 lines, 1077 line anchors), while other round-5 revisers were still editing, so
rows for files under revision are a snapshot that the integration pass re-takes. Run on the round-4 tree,
the same script reproduces that census's rows (e.g. 01, 02, 05-ai and 13), which confirms the method is
unchanged. Method (exact, so the next run is comparable):

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
| [01-summary.md](01-summary.md) | 165 | 0 | 3 | 00-baseline-build, 01-package-inventory |
| [02-decision-record.md](02-decision-record.md) | 148 | 6 | 12 | 00-baseline-build, 05-storage, 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 10-auth-rbac, 11-integrations, 12-cross-cutting, 16-arr-domain-research |
| [03-architecture.md](03-architecture.md) | 841 | 16 | 43 | 00-baseline-build, 00-index, 03-layering-guard, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting, 14-build-ci |
| [04-package-mapping.md](04-package-mapping.md) | 1496 | 64 | 287 | 01-package-inventory, 02-import-graph-hotspots, 03-layering-guard, 04-database-store, 05-storage, 06-tasks-ops, 07-plugin-registry-sdk, 09-http-api, 10-auth-rbac, 11-integrations, 12-cross-cutting |
| [05-ai.md](05-ai.md) | 903 | 30 | 26 | 11-integrations, 12-cross-cutting |
| [05-authn.md](05-authn.md) | 1784 | 71 | 57 | 09-http-api, 10-auth-rbac, 12-cross-cutting, 13-frontend |
| [05-backup.md](05-backup.md) | 1124 | 16 | 15 | 05-storage, 10-auth-rbac, 12-cross-cutting |
| [05-config.md](05-config.md) | 2160 | 68 | 52 | 02-import-graph-hotspots, 03-layering-guard, 10-auth-rbac, 12-cross-cutting, 15-dependencies |
| [05-download.md](05-download.md) | 765 | 16 | 14 | 06-tasks-ops, 11-integrations, 16-arr-domain-research |
| [05-events.md](05-events.md) | 748 | 12 | 8 | 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting |
| [05-health.md](05-health.md) | 649 | 24 | 25 | 09-http-api, 11-integrations, 12-cross-cutting |
| [05-http-api.md](05-http-api.md) | 1382 | 77 | 46 | 09-http-api, 10-auth-rbac, 12-cross-cutting, 13-frontend |
| [05-indexer.md](05-indexer.md) | 463 | 3 | 3 | 11-integrations, 16-arr-domain-research |
| [05-library.md](05-library.md) | 788 | 44 | 47 | 02-import-graph-hotspots, 05-storage, 08-prior-art, 12-cross-cutting, 16-arr-domain-research |
| [05-logging.md](05-logging.md) | 620 | 37 | 23 | 05-storage, 10-auth-rbac, 12-cross-cutting, 15-dependencies |
| [05-metadata.md](05-metadata.md) | 1556 | 57 | 42 | 11-integrations, 12-cross-cutting, 16-arr-domain-research |
| [05-metrics.md](05-metrics.md) | 480 | 19 | 26 | 12-cross-cutting, 15-dependencies |
| [05-notify.md](05-notify.md) | 442 | 19 | 14 | 10-auth-rbac, 11-integrations, 12-cross-cutting, 16-arr-domain-research |
| [05-rbac.md](05-rbac.md) | 1521 | 49 | 26 | 09-http-api, 10-auth-rbac, 12-cross-cutting, 13-frontend |
| [05-realtime.md](05-realtime.md) | 589 | 15 | 11 | 09-http-api, 12-cross-cutting, 13-frontend |
| [05-storage.md](05-storage.md) | 2142 | 59 | 49 | 02-import-graph-hotspots, 04-database-store, 05-storage, 08-prior-art, 12-cross-cutting |
| [05-tasks.md](05-tasks.md) | 1891 | 84 | 90 | 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting |
| [05-updater.md](05-updater.md) | 781 | 18 | 12 | 07-plugin-registry-sdk, 12-cross-cutting, 14-build-ci |
| [06-app-sdk.md](06-app-sdk.md) | 2562 | 60 | 67 | 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting |
| [07-ui-shell.md](07-ui-shell.md) | 1505 | 33 | 31 | 08-prior-art, 09-http-api, 13-frontend, 14-build-ci, 15-dependencies |
| [08-arr-facade.md](08-arr-facade.md) | 653 | 10 | 13 | 09-http-api, 10-auth-rbac, 16-arr-domain-research |
| [09-abo-on-core.md](09-abo-on-core.md) | 1330 | 86 | 137 | 00-index, 05-storage |
| [10-tv-movies-proof.md](10-tv-movies-proof.md) | 1070 | 8 | 7 | 08-prior-art, 11-integrations, 16-arr-domain-research |
| [11-multi-app.md](11-multi-app.md) | 115 | 2 | 2 | — |
| [12-repo-split.md](12-repo-split.md) | 706 | 10 | 32 | 08-prior-art, 14-build-ci, 15-dependencies |
| [13-toolchain.md](13-toolchain.md) | 365 | 48 | 43 | 00-baseline-build, 06-tasks-ops, 14-build-ci, 15-dependencies |
| [14-risks-open-questions.md](14-risks-open-questions.md) | 250 | 16 | 14 | 00-baseline-build, 04-database-store, 08-prior-art |
| **Total** | 31994 | 1077 | — | — |

## Key evidence by decision

| Decision / claim | Evidence |
|---|---|
| HEAD resolves permissions by exact string over the role's stored list, so an unknown string grants nothing; but worker confinement is a negative "nothing else" test, so an extra namespaced string beside `fingerprint.worker` un-confines a custom worker role on an older build; an empty API-key scope list widens to every role permission. R3-1's "additive strings are safe" premise is therefore false, and R4-1 keeps native-only data out of legacy rows (books' extension family) | `internal/server/middleware/auth.go:430-455` (`EffectivePermissionsFor`), `:271-297` (`workerOnly`, `abortWorkerOnlyOutsideWorkerAPI`), `:405-408` (`intersectPermissions`, empty scopes); SEC4-3; 02 decision 13 |
| An enabled Basic gate with an empty stored password admits `<username>:` with an empty password, so a moved secret must leave a never-matching sentinel, not an empty value (R4-2) | `internal/server/middleware/basicauth.go:81-87` (`subtle.ConstantTimeCompare` of two empty slices is 1); SEC4-1, DATA4-1, OPS4-1; 02 decision 13 |
| HEAD already stores four secrets encrypted in its own setting rows (OpenAI key, Google Books key, Hardcover token, Basic password) and blanks them in the blob, so they are not moved during the extraction and a rollback never strands the Basic gate (R5-1) | `internal/config/persistence.go:1955-1958` (blanked in the blob), `:1989-2003` (`SetSetting(…, true)`); OPS5-2; 02 decision 13, 03 §3.3 `secrets` |
| The shipped compose file always sets `BASIC_AUTH_ENABLED` (default `false`), so an env value that locked its key would switch off a gate enabled in the UI; HEAD lets the stored value win (R5-1) | `docker-compose.yml:20-22`; OPS5-1; 03 §3.3 `config`, 14 K15 |
| `library:` is an ABO key family and `KeyFamilies()` is exported, so check (h) / R1(c) reads the claim generator's output and treats a prefix followed by a space as a Go error string, not a key | `internal/database/keyfamilies.go:126`, `:330`; round-5 modules-build review Z3, Z4; MOD5-3, MOD5-4; 03 R1(c) |
| `internal/config` imports `internal/database`, so books' config-blob-backed legacy stores need their own package above both (R5-2) | `internal/config/persistence.go:915` (`LoadConfigFromDatabase(store database.SettingsStore)`); MOD5-2; 03 §3.5.1 |
| The planned taskstore split is bidirectional (residue → taskstore 23 names, taskstore → residue 13), so it must import in one direction only (R5-2) | round-5 modules-build review Z2; MOD5-1; 03 §3.5.1 item 1 |
| Every `//go:embed` today sits in a `*.go` file, so a path filter cannot catch an embed input outside the Docker context; `check_embed_context.py` reads `EmbedFiles` instead | round-5 modules-build review Z5; MOD5-7; 03 §3.2 item 4 |
| `make build` runs `npm run build` directly, so the UI build ID must be generated by the npm build itself (R5-7) | `Makefile:154` (`build: web-build`), `:198-201`; FE5-1; 03 §3.6 "Build identity" |
| HEAD restores by extracting the archive into the live DB directory, so an older build's in-place restore keeps the directory identity and needs a content marker to be classified as replaced | `internal/backup/backup.go:777-850`; SEC5-6; 14 K22 |
| The Docker build context excludes `docs` and root-level `*.md`, so a `//go:embed` of `docs/compat-surfaces.md` fails every image build; a Markdown file below the root stays in the context (R4-4) | `.dockerignore:11` (`docs`), `:30` (`*.md`); round-4 modules-build review Y5; 03 §3.2 item 4 |
| A default-tag `go list` lists build-tagged files only under `IgnoredGoFiles`, without imports, so the stage-visibility gate must union every CI tag set | round-4 modules-build review Y2; 03 §3.5.1 item 4 |
| Dependabot gomod joins the top-level `multi-ecosystem-groups` entry; the repo uses no `groups` key, so `applies-to` cannot be set there | `.github/dependabot.yml:8-20,59-64`; MOD4-15; 03 §3.2 item 6 |
| A pre-S0 updater swaps any newer binary with no store check, so a pre-S0 install can meet the floor release's boot refusal | `internal/updater/updater.go:214-280` (`DownloadAndReplace`); DATA4-18; 02 N14, 14 K18 |
| The legacy SPA opens an invite only on the path form `/invite/<token>`, so invite links keep it until the shell stage | `web/src/App.tsx:201-206`; FE4-2; 03 §3.6 |
| `cmd` is a non-test user of `internal/startup`, so the stage-visibility gate must read non-test imports of every package, including `cmd/*` and the root `main` (R3-3) | round-3 modules-build review X17, X18; `cmd/root.go:274`; 03 §3.5.1 item 4 |
| The root has pending `go fix` changes in 245 files, so the root gains no `go fix` gate; a move PR applies `go fix` to the files it moves and records the move in `core/scripts/split/origins.txt` | round-3 modules-build review X20; 03 §3.2 item 4, 13 §13.3, 12 §12.8.1 (MOD4-14) |
| Root `go mod tidy -diff` is clean at HEAD, so per-module `tidy-check` can be enabled at S0 | round-3 modules-build review X21 |
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
| apidiff module-mode output forms used by the S1 filter | modules-build review X7 |
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
| ABO exports 73 `audiobook_organizer_*` metric families (kept unchanged) | `internal/telemetry/contract/testdata/series.golden` |
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
