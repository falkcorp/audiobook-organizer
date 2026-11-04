<!-- file: docs/agent-tasks/platform-core/PC-04-06-s2-head-oracles.md -->
<!-- version: 1.0.0 -->
<!-- guid: 899b3fe7-b174-4c9a-8e99-b49ee69194f9 -->
<!-- last-edited: 2026-11-04 -->

# PC-04-06 — S2 HEAD oracles, test first: 20 wire goldens, the seeded series golden through the real chain, and ABS conformance cell c

| Field | Value |
|---|---|
| ID | PC-04-06 |
| Tier | **T2 Sonnet/high** — three oracle tests recorded against the HEAD build before the S2 code they guard; no production behaviour changes, but the selection and normalisation must be exact |
| Wave | W04 (S2, release `S2r1`), serialized step 1 |
| Depends on | OWNER-03 (the `S1` soak row) |
| Trailer | `Stage-Release: S2 1` |
| Spec | 09 §9.4 S2 "Done when" (wire goldens; ABS cell c); 09 §9.3 "Rollback rehearsal" (cell c in S2, S4, S6, S9); 09 §9.10 R-test row (cell (c) "ABS conformance against `prev` and `cur`"); 05-metrics §6 ("The `http_server_*` seeding rows go through production wiring", the stage golden-diff table, `TestHistogramBucketsPinned`, `TestHEADSeriesStillPresent`); raw/09 §1.1 (top-level groups) and §3.1 (route table) under `docs/plans/platform-core/raw/09-http-api.md` |
| Digest | `docs/plans/platform-core/digests/S2.md` §8 ("Wire goldens", "r1, r2 and r3") |

**Gate.** Open and make green any time; merge only after OWNER-03's `S1` row is in `ci/soak.txt`. Merge it before
PC-04-07 (the flip uses these oracles).

**Why first.** The oracles are recorded against the build as it is before any S2 code lands, so they can catch a
change S2 makes (advisor G1 risk 6: "Use HEAD as the test oracle... Brief them before the code they guard").

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-04-06 -b test/pc-04-06-s2-oracles origin/main
cd ../audiobook-organizer-pc-04-06 && git rebase origin/main && git worktree list
make web-deps
```

Never run `go work init` (CLAUDE.md).

## Exact files

| Action | Path |
|---|---|
| create | `internal/wirecontract/s2_goldens_test.go` |
| create | `internal/wirecontract/testdata/s2/` (20 golden files, one per sampled route) |
| modify | the seeded series contract test's seeding file: `series_contract_test.go` under `internal/telemetrypolicy/` (moved there from `internal/telemetry/contract/` by PC-03-04 at S1) |
| must NOT change | the series golden `series.golden` (now under `internal/telemetrypolicy/testdata/`): the done-check is an empty diff |
| modify | the R-test harness's cell (c) (files located in Step 0) |
| create | `changelog.d/<date>_pc_04_06_s2_oracles.md` (headerless) |

## Re-verify (rerun at your base)

```bash
git ls-files | grep -E '(^|/)series\.golden$'                         # 1 path; expect internal/telemetrypolicy/testdata/series.golden
git ls-files | grep -E 'series_contract_test\.go$'                    # 1 path, in internal/telemetrypolicy/
grep -n 'func seedOtelgin' $(git ls-files | grep -E 'series_contract_test\.go$')   # 1 (HEAD's test-local engine seed)
grep -c '^http_server_' $(git ls-files | grep -E '(^|/)series\.golden$')            # 3 (source column otelgin)
grep -n 'UPDATE_WIRE_GOLDEN' internal/wirecontract/contract_test.go                # 1 (the regenerate convention)
grep -n 'func (s \*Server) Engine()' internal/server/*.go | wc -l                   # 1 if PC-04-01 merged, else 0 (Step 0)
grep -rln 'compat-rehearsal' Makefile .github/workflows/ scripts/ 2>/dev/null       # the R-test harness (PC-01-04)
grep -rn -i 'cell.\{0,3\}c\b\|abs conformance\|abs_conformance' Makefile scripts/ .github/workflows/compat-rehearsal.yml 2>/dev/null | head
ls internal/syncapi/conformance/fixture.go                                          # exists (the ABS fixture loader)
```

## Steps

0. **Preconditions.**
   - The production gin engine must be reachable from a test outside `internal/server`. Use `(*server.Server).Engine()`
     if PC-04-01 has merged. If not, add `internal/server/engine_export.go` (coined) holding exactly
     `func (s *Server) Engine() http.Handler { return s.router }` with its header; PC-04-01 then drops its own copy
     (same name and signature, so the second PR to merge removes the duplicate on rebase).
   - The R-test harness from PC-01-04 must exist and name a cell (c) slot. If `compat-rehearsal` exists but has no
     cell-(c) slot, add one in the harness's own structure. If no harness exists at all, **STOP and report**: cell c
     cannot be written without PC-01-04's `prev`/`cur` runner.
1. **Wire goldens (`internal/wirecontract/s2_goldens_test.go`).** Test name `TestS2WireGoldens` (coined). Sample
   (selection coined here, deterministic): the first 20 groups of raw/09 §1.1's table, and in each the first `GET`
   route of raw/09 §3.1 (alphabetical) under that prefix with no path parameter, else the first with one:

   | # | Group | Route |
   |---|---|---|
   | 1 | `/api/v1/audiobooks` | `GET /api/v1/audiobooks` |
   | 2 | `/api/v1/operations` | `GET /api/v1/operations/active` |
   | 3 | `/api/v1/dedup` | `GET /api/v1/dedup/candidates` |
   | 4 | `/api/v1/auth` | `GET /api/v1/auth/api-keys` |
   | 5 | `/api/v1/metadata` | `GET /api/v1/metadata/candidate-feedback` |
   | 6 | `/api/v1/authors` | `GET /api/v1/authors` |
   | 7 | `/api/v1/series` | `GET /api/v1/series` |
   | 8 | `/api/v1/users` | `GET /api/v1/users` |
   | 9 | `/api/v1/ai` | `GET /api/v1/ai/backends/status` |
   | 10 | `/api/v3/ops` | `GET /api/v3/ops/counts` |
   | 11 | `/api/v1/admin` | `GET /api/v1/admin/debug/edits` |
   | 12 | `/api/v1/itunes` | `GET /api/v1/itunes/books` |
   | 13 | `/api/v1/diagnostics` | `GET /api/v1/diagnostics/db-census` |
   | 14 | `/api/v1/maintenance` | `GET /api/v1/maintenance/acoustid-stats` |
   | 15 | `/api/v1/playlists` | `GET /api/v1/playlists` |
   | 16 | `/api/v1/books` | `GET /api/v1/books/:id/position` (seeded book ID) |
   | 17 | `/api/v1/collections` | `GET /api/v1/collections` |
   | 18 | `/api/v1/review` | `GET /api/v1/review/count` |
   | 19 | `/api/v1/system` | `GET /api/v1/system/activity-log` |
   | 20 | `/api/v1/deluge` | `GET /api/v1/deluge/discover` |

   Substitution rule: a route absent at your base, or one that attempts outbound network I/O in the test (run the
   test with an `http.DefaultTransport` that fails every dial and with no integration configured), is replaced by the
   next `GET` of the same group in §3.1 order; record each substitution in the test's table comment and the PR body.
   For each route, build the production server over a fresh store seeded by one deterministic fixture function in the
   test (a few books, authors, series, one playlist, one collection, one user; fixed IDs and times), serve the request
   through `srv.Engine()` with `EnableAuth=false`, and compare **status and JSON body** with
   `testdata/s2/<NN>-<slug>.json`. Normalise volatile fields before comparing and before writing a golden: RFC 3339
   timestamps and epoch-millisecond fields, durations/uptime, version and build strings, request IDs, generated
   ULIDs/UUIDs → fixed placeholders (09 S2: "volatile fields (timestamps, uptime, versions, request IDs) normalised —
   not byte equality across builds"). Regenerate with `UPDATE_WIRE_GOLDEN=1 go test ./internal/wirecontract/ -run TestS2WireGoldens`.
2. **Series test through production wiring.** In the contract test's seeding, replace `seedOtelgin`'s test-local gin
   engine (`gin.New()` + `otelgin.Middleware`) with one request through ABO's production engine (`srv.Engine()` of a
   server built over an in-memory store) to a sampled `:id` legacy route (`GET /api/v1/audiobooks/:id` with a seeded
   ID), with a `host:port` target as HEAD's seed uses (`http://example.test:8484/...`), so `server_port` stays pinned.
   Update the seeding table's description text for the three `http_server_*` rows to say "one GET through the
   production gin engine". The golden's source column stays `otelgin`. The golden must not change: run the test and
   confirm `git diff --exit-code` on the golden. `TestHistogramBucketsPinned` and `TestHEADSeriesStillPresent` stay
   green. (From S2 r2 the seed moves onto a `coretest` boot with books' `MountLegacy` engine: PC-05-02.)
3. **ABS cell c.** Wire cell (c) of the R-test to run the ABS conformance fixtures (`internal/syncapi/conformance`
   loader, the fixtures the `internal/server/handlers/abs` conformance tests use) over HTTP against a running
   binary with the ABS surface enabled, once against `prev` and once against `cur` (09 §9.10). In this PR run it with
   both rows built from your base (`prev = cur = HEAD`) and make it pass; it becomes a required part of
   `compat-rehearsal` on every S2 flip PR (S2 is an ABS-path stage, 09 §9.3).
4. Header on every new or changed file; changelog fragment: "Tests: S2 oracles (wire goldens, production-wired HTTP
   series seed, ABS conformance cell c)."

## Code contract

No production API. The one accessor this PR may add (Step 0) is exactly
`func (s *Server) Engine() http.Handler` (name coined in PC-04-01). Golden files are JSON documents
`{"status": <int>, "body": <normalised JSON>}` (format coined here), one per sampled route.

## TDD

This task is test-only: write each oracle, run it against your base, and commit the recorded goldens. A golden that
does not reproduce on a second run (flaky normalisation) is a failure: run every new test with `-count=3`.

## Acceptance

```bash
go test ./internal/wirecontract/ -run TestS2WireGoldens -count=3                 # ok
ls internal/wirecontract/testdata/s2/ | wc -l                                     # 20
POL=$(dirname $(git ls-files | grep -E 'series_contract_test\.go$'))
go test ./$POL/ -count=1                                                          # ok (contract test, TestHistogramBucketsPinned, TestHEADSeriesStillPresent)
git diff --exit-code origin/main -- $(git ls-files | grep -E '(^|/)series\.golden$') && echo GOLDEN-UNCHANGED
#   expect: GOLDEN-UNCHANGED
grep -n 'otelgin.Middleware' $POL/series_contract_test.go | wc -l                 # 0 (no test-local otelgin engine)
make compat-rehearsal PREV_REF=HEAD 2>&1 | tail -5                                # cell (c) reported PASS (use the harness's documented invocation)
make ci                                                                           # exit 0
```

## Out of scope

Any production code change (except the one-line `Engine()` accessor of Step 0 when PC-04-01 has not merged); the
`coretest`/`MountLegacy` seeding (PC-05-02); `TestLegacySeriesLabelValuesMatchHEAD` (its HEAD-captured golden is
05-http-api §12's and runs from S2 through the real chain, which needs PC-04-01/PC-04-02/PC-04-05 merged — see
Planner notes).

## Rollback

Revert the PR. Only tests, goldens and the harness cell change.

## Repo rules

Worktree per task off fresh `origin/main`; 4-line headers on new or changed hand-written files (JSON goldens carry
none, like `web/src/types/contract/testdata/`); headerless changelog fragment; conventional commit
`test(s2): HEAD oracles for wire shapes, HTTP series and ABS conformance` ending with `Stage-Release: S2 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin` past a
required check. The fixture seed builds a handful of rows; no library-scale loop (CLAUDE.md concurrency).

## Planner notes

- Path drift: the row names `internal/telemetry/contract/testdata/series.golden`, but PC-03-04 (S1) renames that
  suite to `internal/telemetrypolicy/` (04 §2.4). This brief locates the file with `git ls-files`.
- The row lists `series.golden` as "modify", but 05-metrics §6 says no HEAD golden line changes and the S2 r1 diff is
  empty; the file that changes is the contract test's seeding code, which the row does not list.
- Cell (c) lives in the R-test harness of PC-01-04, whose files the row does not list.
- `TestLegacySeriesLabelValuesMatchHEAD` (05-http-api §12, "every stage from S2") needs a golden captured from the
  HEAD build; no row of W04 names it. If it must be captured from HEAD, this PR is the natural place, but it also
  needs the `coretest` boot with `MountLegacy` (PC-04-01, PC-04-02, PC-04-05), which this row does not depend on.
  Recommend assigning its HEAD capture here and its real-chain run to PC-05-02.
- The 20-route selection rule is coined here (09 fixes only "one per top-level group"); a reviewer may prefer a
  different sample.
