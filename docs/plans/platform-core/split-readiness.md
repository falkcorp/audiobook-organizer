<!-- file: docs/plans/platform-core/split-readiness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 54041305-9707-4448-b5af-133d66d4696a -->
<!-- last-edited: 2026-11-08 -->

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
No night has been folded in yet.
<!-- readiness:latest:end -->

## History

<!-- readiness:history:start -->
| Date | Commit | SC1 | SC3 | SC5 | SC6 | SC8 | SC9 |
|---|---|---|---|---|---|---|---|
<!-- readiness:history:end -->
