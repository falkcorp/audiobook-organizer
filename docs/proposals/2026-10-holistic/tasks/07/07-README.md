<!-- file: docs/proposals/2026-10-holistic/tasks/07/07-README.md -->
<!-- version: 1.0.0 -->
<!-- guid: f1ed0a96-1c52-4ef9-8d4f-1a8e87cbcc76 -->
<!-- last-edited: 2026-10-09 -->

# Task briefs from doc 07 (design decisions and modularity) and X2/X3 from the coordinator

Nineteen briefs: seventeen in this directory, `08-X2.md` and `08-X3.md` in `tasks/08/`. Each follows `tasks/00-TEMPLATE.md`. Source: `07-design-decisions-and-modularity.md` §3-§4 with its appendices `A-measurements.md`, `B-decisions.md`, `C-server-state-library.md`; owner decisions in `09-owner-decisions.md`.

## Briefs in merge order
| # | ID | Title | Wave | Model | Size | Depends on |
|---:|---|---|---:|---|---|---|
| 1 | [C1](07-C1.md) | Make `main` green: four inherited failures fixed, ratchets made one-way | 0 | sonnet | M | none |
| 2 | [C2](07-C2.md) | Path filters: docs-only changes skip Go and web jobs | 0 | sonnet | S | C1 |
| 3 | [C3](07-C3.md) | 4-shard short-test run, intra-package `-run` split, coverage job | 0 | opus | M | C1, C2 |
| 4 | [C4](07-C4.md) | CI throughput measurement (`docs/ci/2026-10-ci-throughput.md`) | 0 | sonnet | S | C3 and the next ten merges |
| 5 | [X2](../08/08-X2.md) | Empty `Permissions` means `settings.manage` on `POST /operations/v2` | 0 | sonnet | S | C1 |
| 6 | [X3](../08/08-X3.md) | Refuse in-process decoding in three ops unless `ALLOW_SERVER_DECODE` | 0 | sonnet | S | C1 |
| 7 | [G1](07-G1.md) | Ratchet `Store` method count (455) and wide-type references (31) | 1 | sonnet | S | C1 |
| 8 | [G2](07-G2.md) | Layering test with a shrink-only violation map | 1 | sonnet | S | C1 |
| 9 | [G3](07-G3.md) | Ratchet direct `config.AppConfig` reads | 1 | sonnet | S | C1 |
| 10 | [R2](07-R2.md) | Readiness in `/health`, counted memdb fallback, ops wait for warmup | 1 | sonnet | M | C1 |
| 11 | [R3](07-R3.md) | Startup steps classified fatal or degraded in one table | 1 | opus | M | R2 |
| 12 | [R4](07-R4.md) | `Type=notify` and a deploy that waits for `ready` (the owner installs the unit) | 1 | opus | M | R2, R3 |
| 13 | [S3](07-S3.md) | Index from `ChangeObserver`; delete `indexedStore` | 1 | opus | L | R4, G1 |
| 14 | [G4](07-G4.md) | Gate manifest, one-way ratchets, baseline-lowering bot | 1 | sonnet | M | C3, G1, G2, G3 (and 01 P9 for `ci.yml`) |
| 15 | [S1](07-S1.md) | `dbtest.NewStore(t, opts...)` | 1 | sonnet | S | C1, R2 |
| 16 | [S6](07-S6.md) | `FirstAudioFile` helper over `book_file` rows | 1 | sonnet | S | C1 |
| 17 | [F3](07-F3.md) | TanStack Query v5 pilot on the Repairs lane (revert on a miss) | 2 | opus | M | C1 |
| 18 | [F1](07-F1.md) | tygo-generated TypeScript types | 3 | sonnet | M | C3, G4, T2 |
| 19 | [F4](07-F4.md) | Delete `docs/api/openapi.json` | 4 | sonnet | S | F1 |

Model split: opus 5 (C3, R3, R4, S3, F3), sonnet 14. R4 raised to opus by the verifier advisory (TLS/HTTP3 listener rework).

### Merge-order constraints worth knowing
- `ci.yml`: C2 -> C3 -> 06 P2 -> 01 P9 -> G4 -> F1 -> U1.
- `internal/server/server_lifecycle.go`: R2 (touches `pebble_store.go` and the health handler), then R3, R4, S3, in that order.
- `internal/database/pebble_store.go` is shared by R2, 07-S2 and storage release B: rebase, do not reorder.
- G1 baselines are lowered by S3 (reference count); G2 entries are deleted by 07-M4; G3 is lowered by 07-M2, M3, M5.
- X2 merges before 07-F1 and 05 PR 9 (all touch `operations_v2.go` or its types).
- F3 goes after 03 PR 10 and before 05 PR 10 and 12D.

## Not briefed (waves 3 and 4): listed only
Source pointers are rows of the table in `07-design-decisions-and-modularity.md` §4 (line numbers at HEAD `93a9b745f`). Waves are from the doc's ordering and `08-integrated-roadmap.md` §5; not re-verified here.
| ID | What | Wave | Source |
|---|---|---:|---|
| S1b..n | Mechanical sweep of the 367 on-disk test-store constructions, 4 package groups, after freeze window F | 3 | doc 07 §4 line 281; 08 §5 |
| S2 | `UpdateBook` stale-write check inside the release B chokepoint | 3 | doc 07 §4 line 282 (lands with or after storage release B) |
| S4 | Key-family owner field and raw-KV ownership ratchet | 3 | doc 07 §4 line 284 (after release A's registry) |
| S4b | Move `internal/merge`'s 23 raw-KV calls behind owner helpers | 3 | doc 07 §4 line 285 |
| S5 | D42 version-group record, converter, derived flag | 3 | doc 07 §4 line 287 (storage cut-over window) |
| M1 | Make config a leaf (`internal/config/configstore`) | 3 | doc 07 §4 line 293 |
| M2 | Declarative config field table | 3 | doc 07 §4 line 294 |
| M3 | Sparse config persistence plus converter | 3 | doc 07 §4 line 295 |
| M4 | Remove `metadata` -> `operations/registry` | 3 | doc 07 §4 line 296 (lowers G2) |
| M5 | Pipeline hooks become constructor parameters | 3 | doc 07 §4 line 297 |
| M14 | Retire `database.Store` as a consumer type | 3 | doc 07 §4 line 299 (after S3) |
| F2 | Split `api.ts` by domain | 4 | doc 07 §4 line 306 (only if F3 met its bar) |
| U1 | `GO_VERSION` single source | 3 | doc 07 §4 line 314 (`ci.yml` order: after F1) |
| U2 | ABS / AudioBooth golden response fixtures | 3 | doc 07 §4 line 315 |
Also not briefed here: the S6 read-site sweeps (8 PRs, wave 3, doc 07 row 07-S6); this directory has only the helper.

## Discrepancies found while writing (doc versus HEAD)
- C1: doc says S and 10 slog files; HEAD has 9 offending files plus 1 stale entry (about 33 call sites), so the brief says M.
- C3: the doc proposes a new shard script; `scripts/ci/go_test_shards.py` already exists and is reused.
- G3: the doc's 636 AppConfig reads in 204 files is not reproducible (598 and 611 lines by two greps); the brief defines its own AST instrument and baseline.
- F3: `operation.status` carries the operation id as `ev.id` / `ev.data.operation_id`, not `ev.data.id`; the lane file is under `web/src/components/review/lanes/`, not `hooks/`.
- F4: the file is `docs/api/openapi.json` (281 `operationId` entries counted, the doc says 305 operations).
- S3: `indexedStore` also carries the authorcredit authority capability; the brief relocates it. A third mechanism (`SetBookChangeObserver`) exists and is left alone. The observer has a single slot already used by the search result cache, so S3 composes observers.
- X2: the consequence is wider than "library-optimize" (seven defs found without `Permissions`; the PR must enumerate at runtime).
- R4: listeners are bound inside goroutines, so `READY=1` needs an explicit `net.Listen`; READY is sent after bind, not after warmup (150 s warmup versus the start timeout).

## Unverified
- `.errcheck-baseline` is 779; the 2026-10 audit says 770; golangci-lint was not run.
- The 30-versus-34 count of memdb read guards (R2 tells the agent to re-count).
- Wave placement of the non-briefed rows.
- Nothing was built or tested; all facts were read from the tree or measured with grep and `go list`.
