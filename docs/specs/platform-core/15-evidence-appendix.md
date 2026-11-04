<!-- file: docs/specs/platform-core/15-evidence-appendix.md -->
<!-- version: 1.1.1 -->
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
(`spec-round-1-modules-build.md`, "Experiments run for this review"). Spec files cite those experiments
by ID where a decision rests on them.

**Rule:** a claim about the current code without an anchor or a raw-file link is a review finding (see
`docs/plans/platform-core/reviews/`). External claims (Sonarr/Radarr behaviour, package versions) cite
public URLs in `raw/16-arr-domain-research.md` and `raw/15-dependencies.md` with check dates.

## Anchor census per spec file

Regenerated on 2026-11-04 at the end of the round-1 **integration pass**, from the working tree, after
every file of round 1 had settled (09 and 04 at v1.1.0 included), so all rows are from one moment and
replace the R-A snapshot. Method (exact, so the next run is comparable; a re-run of it reproduces the
unchanged rows of the R-A snapshot, e.g. 01, 02 and 05-ai):

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
| [01-summary.md](01-summary.md) | 118 | 0 | 3 | 00-baseline-build, 01-package-inventory |
| [02-decision-record.md](02-decision-record.md) | 101 | 0 | 6 | 00-baseline-build, 05-storage, 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 10-auth-rbac, 11-integrations, 12-cross-cutting, 16-arr-domain-research |
| [03-architecture.md](03-architecture.md) | 410 | 6 | 24 | 00-baseline-build, 00-index, 03-layering-guard, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting, 14-build-ci |
| [04-package-mapping.md](04-package-mapping.md) | 1104 | 53 | 271 | 01-package-inventory, 02-import-graph-hotspots, 03-layering-guard, 04-database-store, 05-storage, 06-tasks-ops, 07-plugin-registry-sdk, 09-http-api, 10-auth-rbac, 11-integrations, 12-cross-cutting |
| [05-ai.md](05-ai.md) | 615 | 24 | 19 | 11-integrations, 12-cross-cutting |
| [05-authn.md](05-authn.md) | 1025 | 54 | 48 | 09-http-api, 10-auth-rbac, 12-cross-cutting, 13-frontend |
| [05-backup.md](05-backup.md) | 770 | 12 | 10 | 05-storage, 10-auth-rbac, 12-cross-cutting |
| [05-config.md](05-config.md) | 1274 | 42 | 41 | 02-import-graph-hotspots, 03-layering-guard, 10-auth-rbac, 12-cross-cutting, 15-dependencies |
| [05-download.md](05-download.md) | 617 | 15 | 14 | 06-tasks-ops, 11-integrations, 16-arr-domain-research |
| [05-events.md](05-events.md) | 594 | 12 | 9 | 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting |
| [05-health.md](05-health.md) | 476 | 15 | 21 | 09-http-api, 11-integrations, 12-cross-cutting |
| [05-http-api.md](05-http-api.md) | 1041 | 73 | 37 | 09-http-api, 10-auth-rbac, 12-cross-cutting, 13-frontend |
| [05-indexer.md](05-indexer.md) | 428 | 3 | 3 | 11-integrations, 16-arr-domain-research |
| [05-library.md](05-library.md) | 680 | 40 | 42 | 02-import-graph-hotspots, 05-storage, 08-prior-art, 12-cross-cutting, 16-arr-domain-research |
| [05-logging.md](05-logging.md) | 455 | 32 | 18 | 05-storage, 10-auth-rbac, 12-cross-cutting, 15-dependencies |
| [05-metadata.md](05-metadata.md) | 1019 | 53 | 41 | 11-integrations, 12-cross-cutting, 16-arr-domain-research |
| [05-metrics.md](05-metrics.md) | 380 | 17 | 24 | 12-cross-cutting, 15-dependencies |
| [05-notify.md](05-notify.md) | 281 | 18 | 14 | 10-auth-rbac, 11-integrations, 12-cross-cutting, 16-arr-domain-research |
| [05-rbac.md](05-rbac.md) | 1039 | 38 | 24 | 09-http-api, 10-auth-rbac, 12-cross-cutting, 13-frontend |
| [05-realtime.md](05-realtime.md) | 472 | 13 | 10 | 09-http-api, 12-cross-cutting, 13-frontend |
| [05-storage.md](05-storage.md) | 1419 | 38 | 36 | 02-import-graph-hotspots, 04-database-store, 05-storage, 08-prior-art, 12-cross-cutting |
| [05-tasks.md](05-tasks.md) | 1224 | 63 | 78 | 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting |
| [05-updater.md](05-updater.md) | 462 | 12 | 9 | 07-plugin-registry-sdk, 12-cross-cutting, 14-build-ci |
| [06-app-sdk.md](06-app-sdk.md) | 1551 | 52 | 57 | 06-tasks-ops, 07-plugin-registry-sdk, 08-prior-art, 12-cross-cutting |
| [07-ui-shell.md](07-ui-shell.md) | 1005 | 29 | 27 | 08-prior-art, 09-http-api, 13-frontend, 14-build-ci, 15-dependencies |
| [08-arr-facade.md](08-arr-facade.md) | 606 | 9 | 12 | 09-http-api, 10-auth-rbac, 16-arr-domain-research |
| [09-abo-on-core.md](09-abo-on-core.md) | 816 | 49 | 101 | 00-index, 05-storage |
| [10-tv-movies-proof.md](10-tv-movies-proof.md) | 922 | 8 | 7 | 08-prior-art, 11-integrations, 16-arr-domain-research |
| [11-multi-app.md](11-multi-app.md) | 99 | 2 | 2 | — |
| [12-repo-split.md](12-repo-split.md) | 531 | 10 | 24 | 08-prior-art, 14-build-ci, 15-dependencies |
| [13-toolchain.md](13-toolchain.md) | 313 | 47 | 42 | 00-baseline-build, 06-tasks-ops, 14-build-ci, 15-dependencies |
| [14-risks-open-questions.md](14-risks-open-questions.md) | 151 | 3 | 6 | 00-baseline-build, 04-database-store, 08-prior-art |
| **Total** | 21998 | 842 | — | — |

## Key evidence by decision

| Decision / claim | Evidence |
|---|---|
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
| Latest versions (Go go1.27.3; npm majors) | `raw/15-dependencies.md`, [13](13-toolchain.md) |
| Acquisition machinery is media-neutral; *arr v3 endpoints used by ecosystem clients | `raw/16-arr-domain-research.md` |
| Prior decisions and gates | `raw/08-prior-art.md` |

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- MOD-1, MOD-3, MOD-4, MOD-5, MOD-13 — key-evidence rows for review experiments X1–X7 (Docker layer, exclude placement, `go install` refusal, path-based `internal/`, `git describe`, apidiff output).
- MOD-14, COR-26, SIMP-13, MOD-8 — key-evidence rows for the new layering control edge, op-ID grammar, the 73 metric families, and `make ci`'s 13 prerequisites.
- (all) — anchor census regenerated with a recorded method.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: anchor census regenerated with the documented method once every file had settled (32 files, 21998 lines, 842 line anchors).
