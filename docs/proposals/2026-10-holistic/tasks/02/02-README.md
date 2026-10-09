<!-- file: docs/proposals/2026-10-holistic/tasks/02/02-README.md -->
<!-- version: 1.0.1 -->
<!-- guid: 7d3b9e1a-52c4-4f08-9a6e-3c1f0b8d2e47 -->
<!-- last-edited: 2026-10-09 -->

# Workstream 02 task briefs: index

Source spec: `docs/proposals/2026-10-holistic/02-filter-identification-pipeline.md`. Wave placement: `08-integrated-roadmap.md` section 5. Each brief follows `../00-TEMPLATE.md`.

## Briefed (17)

| Brief | Title | Wave | Model | Size | Depends on |
|---|---|---|---|---|---|
| [02-PR1](02-PR1.md) | Benchmark the production filter path, guard against per-row compiles | 0 | sonnet | S | none |
| [02-PR2](02-PR2.md) | Parse duration filter once; pointer-pass compiled filters | 0 | sonnet | S | PR1 |
| [02-PR3](02-PR3.md) | Shared grammar conformance fixture for Go and vitest | 0 | sonnet | S | none |
| [02-PR5a](02-PR5a.md) | Stale flag on candidate-cache rows; identity-stale check | 0 | sonnet | S | none |
| [02-PR5b](02-PR5b.md) | Retitle stamps cache rows stale instead of deleting them | 0 | sonnet | S | PR5a |
| [02-PR15](02-PR15.md) | Review tab heap-snapshot audit note | 0 | sonnet | S | none (owner runs it) |
| [02-PR16](02-PR16.md) | Slim the index-view payload | 0 | sonnet | S | 07 C1 |
| [02-PR17](02-PR17.md) | Server-side Review query (`view=page`, `ids=all`, LRU) | 1 | opus | M | PR3, PR16, 07 C1 |
| [02-PR18](02-PR18.md) | Review lane page mode behind `review_metadata_server_query` | 1 | sonnet | M | PR17, PR3, PR15 findings |
| [02-PR4](02-PR4.md) | Identification state index, `ident:` filter, counts | 1 | opus | M | PR5a/5b, 07 R2, PR17 |
| [02-PR6](02-PR6.md) | Question-keyed provider cache with negative TTLs | 1 | opus | M | 07 C1-C3 |
| [02-PR9a](02-PR9a.md) | Unknown-signal recording in scoring; author-unknown gate check | 1 | sonnet | S | none |
| [02-PR4b](02-PR4b.md) | `author:` matches every credited author (D32) | 1 | sonnet | S | none |
| [02-PR7a](02-PR7a.md) | `catalog.coverage-report` op (read-only) | 1 | sonnet | S | PR4 |
| [02-PR7b](02-PR7b.md) | Catalog match stage (review-only candidates) behind a flag | 2 | opus | M | PR7a, PR6, PR4 |
| [02-PR8](02-PR8.md) | Window-print index, `SigWindowAcoustID`, fragment containment evidence | 2 | opus | M | 05 PR 3, PR9a |
| [02-PR14](02-PR14.md) | `identification.advance` driver in the v3 shape | 2 | opus | L | PR4, PR6, 04 P1/P4a/P9/P12 |
| [02-PR19](02-PR19.md) | Retire the full-index Review mode | 2 | sonnet | S | PR18 (one release), 03 PR 10 |

Model split: opus 6 (PR4, PR6, PR7b, PR8, PR14, PR17), sonnet 11.

## Not briefed (wave 3)

| PR | Wave | Source pointer |
|---|---|---|
| 9 | 3 | spec section 4, Phase 3 (scoring: consume unknown signals) |
| 10 | 3 | spec section 4, Phase 4 |
| 11 | 3 | spec section 4, Phase 4 |
| 12 | 3 | spec section 4 (PARKED by owner) |
| 13 | 3 | spec section 4, Phase 3; must land before the rescore is scheduled |

## Notes for the coordinator

- D32/F7 (`author:` matches any credit) has no 02 PR. Recommend an unassigned PR 4b.
- PR 13 must land before the corpus rescore is scheduled (D33), so it rides PR 8's `FormulaVersion` bump (`noisy-or-v2`).
- Deviations from the spec, each recorded in the brief: the Stale flag lives in `internal/database/iface_metadata.go` (PR5a); `unfetchedCandidateBookIDs` is in `metadata_candidate_unfetched.go` (PR5a); the coverage op and the advance adapter live in `internal/server`, not a plugin package, because plugins cannot import server (PR7a, PR14); the driver body is a pure package `internal/identification` (PR14).
- The "PR 11" reference in the spec's window-index sizing note is a typo for PR 8.
- Merge-order constraints: `pebble_store.go`: PR5b, 07 R2, PR4. `metadata_cache.go`: 07 C1, PR16, PR17, PR4, 06 P6, PR19. `scheduler/tasks.go`: 04 P1/P4a/P9/P12, then PR14. `ReviewWorkspace.tsx`: PR18 before 03 PR 2. `fragment_consolidation_fixer.go`: PR8 before 01 T5b.
