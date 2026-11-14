<!-- file: docs/plans/platform-core/split-readiness.md -->
<!-- version: 1.0.1 -->
<!-- guid: 54041305-9707-4448-b5af-133d66d4696a -->
<!-- last-edited: 2026-11-14 -->

# Split readiness

The repository split happens when every criterion SC1-SC10 of
[12 §12.3](../../specs/platform-core/12-repo-split.md) holds and the trigger T holds.
This file is the running record of the measurements that are reports, not merge gates.
It is written by the nightly `split-readiness.yml` job through a pull request (never a
direct push): the job runs `make -C core readiness` (SC1, SC5, SC6, SC9), then SC3 and SC8
from the root, and `scripts/ci/fold_split_readiness.py` rewrites only the two regions
between the marker comments below. Each night's full JSON and Markdown report is also
uploaded as the workflow artifact `split-readiness`. The job and this file are dropped at
cutover step 6 (12 §12.8.2).

SC2, SC7 and SC10 are merge gates (`make -C core ci`), SC4(a) is the export run of that
gate, SC4(b) reads `core-ci.yml` on `main`, and SC4(c) is the root's `module-skew` check;
none of them is measured here. Thresholds are OWNER-01's Q-G answer, accepted as proposed
(D15): SC1 is three consecutive API-stable checkpoints over six weeks, counted from the
checkpoint of release R2 (`SC1_FIRST_CHECKPOINT` in `core/Makefile`; unset, SC1 cannot be
met yet), and SC5 is 70 % total coverage and 60 % for every SDK package.

Statuses: PASS (the criterion holds), FAIL (it does not hold yet), N/A (its subject does
not exist yet, so it is unmeasured, not passed) and ERROR (the measurement could not run).

## Latest

<!-- readiness:latest:start -->
Night of 2026-11-14, commit `337b855fae16`.

| Criterion | Status | Measured |
|---|---|---|
| SC1 Public API stable across 3 consecutive core checkpoints | FAIL | 1 checkpoint(s) recorded, 4 needed for 3 pairs |
| SC3 Second consumer builds against core with nothing but a dependency on it | PASS | builds, vets and tests in example.com/mediacore-smoke; internal import refused |
| SC5 Coverage: total and per SDK package | FAIL | total 82.7%, 9 of 61 SDK package(s) below 60% |
| SC6 Every core contribution point is documented | N/A | not applicable: the generator core/internal/cmd/coredoc has not landed yet |
| SC8 npm packages build and pack standalone | FAIL | npm run build failed against the packed tarballs |
| SC9 No known vulnerabilities in core's graph | FAIL | govulncheck ./... found vulnerabilities |
<!-- readiness:latest:end -->

## History

<!-- readiness:history:start -->
| Date | Commit | SC1 | SC3 | SC5 | SC6 | SC8 | SC9 |
|---|---|---|---|---|---|---|---|
| 2026-11-14 | `337b855fae16` | FAIL | PASS | FAIL | N/A | FAIL | FAIL |
<!-- readiness:history:end -->
