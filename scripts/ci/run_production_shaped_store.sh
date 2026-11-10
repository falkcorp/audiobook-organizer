#!/usr/bin/env bash
# file: scripts/ci/run_production_shaped_store.sh
# version: 1.0.0
# guid: 3b1e7d52-6c4a-4f98-8e0d-5a9c2f7b1d64
# last-edited: 2026-11-10
#
# The body of the `production-shaped-store` job (F2-14; 09 §9.3 item 1, "Boot
# time"), shared by extraction-gates.yml (on unit-lifecycle's triggers) and
# nightly.yml (every night) so the two cannot drift. It:
#   1. generates the production-shaped store at the P1 counts (scale 1) from
#      F2-12's stage-1 corpus, with scripts/r2corpus/gen_production_shaped.py;
#   2. boots the current build on a copy of it with
#      TestProductionShapedStore_BootTime and writes boot_time.json;
#   3. fails unless that test ran and passed (a skipped or renamed test makes
#      `go test -run` exit 0).
# F2-15 extends the job to run the cutover suite over the same store, so the
# environment names below are stable:
#   R2_SHAPED_STORE  the generated store (set here)
#   R2_SHAPED_OUT    where boot_time.json and generator_report.json go; the
#                    job uploads that directory as an artifact
# generator_report.json holds counts only (never row values).
set -euo pipefail

work="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
export R2_SHAPED_STORE="${R2_SHAPED_STORE:-${work}/r2-shaped-store}"
export R2_SHAPED_OUT="${R2_SHAPED_OUT:-${work}/r2-shaped-out}"
mkdir -p "$R2_SHAPED_OUT"

df -h "$work" || true

python3 scripts/r2corpus/gen_production_shaped.py \
  --out "$R2_SHAPED_STORE" \
  --report "${R2_SHAPED_OUT}/generator_report.json"

df -h "$work" || true

# Not `go test ... | tee`: a pipe would hide go test's exit status. 20 minutes
# leaves the job's 30 for the generation, the artifact upload and the setup.
rc=0
out="$(go test ./internal/books/ -run '^TestProductionShapedStore_BootTime$' -count=1 -v -timeout 20m 2>&1)" || rc=$?
echo "$out"
if [ "$rc" -ne 0 ]; then
  exit "$rc"
fi
if ! grep -q -- '--- PASS: TestProductionShapedStore_BootTime ' <<<"$out"; then
  echo "::error::TestProductionShapedStore_BootTime did not run and pass (skipped, renamed or removed?)"
  exit 1
fi
test -s "${R2_SHAPED_OUT}/boot_time.json"
cat "${R2_SHAPED_OUT}/boot_time.json"
