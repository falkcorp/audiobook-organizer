<!-- file: docs/agent-tasks/platform-core/PC-02-07-head-metric-oracles.md -->
<!-- version: 1.0.0 -->
<!-- guid: ec1140e4-704b-4b38-819f-85028887bf89 -->
<!-- last-edited: 2026-11-04 -->

# PC-02-07 — HEAD metric oracles, test first: capture `head_series_identity.golden` from the HEAD build, commit `head_series.golden`, `TestHEADSeriesStillPresent`

| Field | Value |
|---|---|
| ID | PC-02-07 |
| Tier | **T2 Sonnet/high.** Two goldens that every later stage's series check compares against; they are captured once, from the pre-extraction metric set, with the contract test's fixed seeds, and a wrong capture cannot be repaired later. |
| Wave | W02 (S0, release `S0`) |
| Depends on | PC-01-04 |
| Blocks | PC-03-04 (moves this package to `internal/telemetrypolicy` and must keep these tests green) |
| Trailer | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §8 (last sentences) |
| Spec | 05-metrics §6 "Series-name checks" (the bullets `TestHEADSeriesStillPresent` and `TestABOMetricsGatherNoDuplicateOrMissing`, which names `head_series_identity.golden` and its content), §4 Resource row (`target_info`'s labels belong in the identity golden); 09 S0 "Done when" (the S0 PR captures it); 09 §9.2.7 |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-02-07" -b platform-core/pc-02-07-head-series origin/main
cd "$REPO/../audiobook-organizer-pc-02-07"
git rebase origin/main
git worktree list
make web-deps
```

Run this task before any PR that adds or moves a metric family. At your base,
`git log origin/main --oneline -- internal/telemetry internal/metrics internal/opsmetrics internal/aidispatch | head -5`
must show no extraction PR (no `Stage-Release:` trailer other than `none` on those paths); if one exists, STOP.

## Exact files

Create: `internal/telemetry/contract/testdata/head_series.golden`,
`internal/telemetry/contract/testdata/head_series_identity.golden`,
`internal/telemetry/contract/head_series_test.go`.
Modify: none in the inventory; this brief also makes one helper path-aware in `series_contract_test.go` (Planner notes).

## Re-verify greps (expected counts at the planning checkout `51a2c2acd`)

```bash
grep -n 'goldenPath   = "testdata/series.golden"' internal/telemetry/contract/series_contract_test.go   # 1 hit (line 60)
grep -n '^func readGolden() (\[\]goldenRow, error)' internal/telemetry/contract/series_contract_test.go  # 1 hit (line 91)
grep -n '^func scrape(t \*testing.T)' internal/telemetry/contract/series_contract_test.go                # 1 hit (line 453)
grep -n '^func TestMain' internal/telemetry/contract/series_contract_test.go                              # 1 hit (line 422)
grep -n 'const targetInfo = "target_info"' internal/telemetry/contract/series_contract_test.go           # 1 hit
wc -l < internal/telemetry/contract/testdata/series.golden                                                # 87
ls internal/telemetry/contract/testdata/                                                                  # histogram_buckets.golden  series.golden  series_reserved.txt
grep -n 'var instanceID = ulid.Make().String()' internal/telemetry/resource.go                           # 1 hit
```

05-metrics cites `series_contract_test.go:15-18,451-456,1125-1150` at `748fd787a`; at `51a2c2acd` they are lines
15–18, 451–467 and 1125–1150. "HEAD's golden" is the file named `series.golden` at your base. A miss or a moved
count: STOP.

## Code contract

Test names: `TestHEADSeriesStillPresent` (spec), `TestHEADSeriesIdentityGoldenWellFormed` and
`TestCaptureHEADSeriesIdentity` (coined). Helper: `func readGoldenAt(path string) ([]goldenRow, error)`. File formats:
`head_series.golden` is `series.golden`'s format byte for byte; the identity format is step 4's.

## TDD tests to write first

Write `TestHEADSeriesStillPresent` first: it fails (missing file) until step 1 copies the golden. Write
`TestHEADSeriesIdentityGoldenWellFormed` before the capture: it fails until step 5 writes the file.

## Steps

1. **`head_series.golden`**: `cp internal/telemetry/contract/testdata/series.golden internal/telemetry/contract/testdata/head_series.golden`.
   Byte-identical; no header (the format allows no comments: `readGolden` refuses any non-row line). Verify with
   `cmp` (acceptance).
2. **Path-aware reader**: in `series_contract_test.go`, rename the body of `readGolden()` into
   `readGoldenAt(path string) ([]goldenRow, error)` (every `goldenPath` inside it becomes `path`) and make
   `readGolden()` return `readGoldenAt(goldenPath)`. No other change to that file; bump its header.
3. **`head_series_test.go`** (package `contract`, same test binary, so `TestMain`'s seeds apply):
   - `const headGoldenPath = "testdata/head_series.golden"` and
     `const headIdentityPath = "testdata/head_series_identity.golden"`.
   - `TestHEADSeriesStillPresent` (spec name): `rows, err := readGoldenAt(headGoldenPath)`; `fams := scrape(t)`; for
     every row, the family is present, its type equals the row's, and its label names equal the row's (the same
     comparison as part (A) of `TestSeriesContract`). Report all mismatches in one failure, listing each.
   - `TestHEADSeriesIdentityGoldenWellFormed` (name coined here): the identity file parses (format below), is sorted,
     has no duplicate line, holds one `target_info` line, and names only families of `head_series.golden` plus
     `target_info`.
   - `TestCaptureHEADSeriesIdentity` (name coined here): `t.Skip` unless `ABO_CAPTURE_HEAD_SERIES_IDENTITY=1` (env
     name coined here); otherwise writes `head_series_identity.golden` from `prometheus.DefaultGatherer.Gather()`.
     Its doc comment says: "Run once, in PC-02-07, against the pre-extraction metric set. Never re-run: the file is
     HEAD's oracle (05-metrics §6)."
4. **Identity format** (coined here): one line per series, `family<TAB>type<TAB>labels`, where `labels` is
   `name=value` pairs sorted by name and joined by `,`, values written as Go `strconv.Quote` strings; `le` and
   `quantile` are dropped (bucket bounds are pinned by `TestHistogramBucketsPinned`), so each histogram label set
   appears once. Include every in-scope family (`inScope(name)`) and `target_info`. Three resource values vary
   per process or per dependency bump and are written as fixed placeholders: `service_instance_id="<per-process>"`
   (HEAD draws a random ULID per process, `resource.go`), `service_version="<build>"`, `telemetry_sdk_version="<sdk>"`.
   Every other label value is written as gathered.
5. **Capture**: `ABO_CAPTURE_HEAD_SERIES_IDENTITY=1 go test ./internal/telemetry/contract/ -run TestCaptureHEADSeriesIdentity -count=1`.
   Run it twice and `cmp` the two outputs; they must be identical (fixed seeds). Commit the file.
6. Header the new test file; one `changelog.d/` fragment.

## Acceptance

```bash
cmp internal/telemetry/contract/testdata/series.golden internal/telemetry/contract/testdata/head_series.golden && echo same   # same
go test ./internal/telemetry/contract/ -count=1 -run 'TestHEADSeriesStillPresent|TestHEADSeriesIdentityGoldenWellFormed|TestSeriesContract|TestHistogramBucketsPinned' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
                     # --- PASS lines for all four; ok  github.com/falkcorp/audiobook-organizer/internal/telemetry/contract
grep -c 'target_info' internal/telemetry/contract/testdata/head_series_identity.golden   # 1
grep -c '<per-process>' internal/telemetry/contract/testdata/head_series_identity.golden # 1
go test ./internal/telemetry/contract/ -run TestCaptureHEADSeriesIdentity -count=1 -v 2>&1 | grep -c SKIP   # 1
make ci; echo "exit=$?"                                                                   # exit=0
```

## Out of scope

`TestABOMetricsGatherNoDuplicateOrMissing` (S5; it compares against this file), `TestContractScrapesServedHandler`
(S2 r3), the move to `internal/telemetrypolicy` (PC-03-04). Never edit `series.golden` here.

## Rollback

Revert the PR. No runtime code changes.

## Repo rules

- Worktree only; never `go work init`. Headers on new and changed Go files; the two goldens take none (their
  readers refuse any non-row line). One `changelog.d/` fragment.
- Every commit ends with `Stage-Release: S0 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase` after the required checks pass; never squash or `--admin`.

## Planner notes

- The inventory row lists no modified file; reusing `readGolden` for the second golden needs the one-line
  path-aware split in `series_contract_test.go` (step 2). No other W02 row touches that file.
- The spec says the identity golden holds "label values" but HEAD's `target_info` carries a random per-process
  `service_instance_id`; the three placeholders of step 4 are a plan choice so the golden is reproducible. S5's
  gather test must apply the same normalisation.
- Golden files carry no versioned header, by necessity of their format; this is an exception to the header rule.
