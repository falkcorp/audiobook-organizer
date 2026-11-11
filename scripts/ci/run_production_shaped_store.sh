#!/usr/bin/env bash
# file: scripts/ci/run_production_shaped_store.sh
# version: 1.1.0
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
#      `go test -run` exit 0);
#   4. runs F2-15's cutover suite (internal/books/cutover_suite_test.go) on the
#      same store: the boot, the count, ID and byte-identity checks (the
#      referential pass is r0 and prefreeze-written only), each of whose
#      /shaped subtests must pass.
# The environment names below are stable:
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

# Not `go test ... | tee`: a pipe would hide go test's exit status. The boot
# takes seconds; the timeouts (here and the suite's below) are backstops.
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

# F2-15: the cutover suite's /shaped subtests. A store this size is scanned
# streaming (no rows kept); the drill and the selftest run in r2-cutover-suite.
tests='TestCutoverOnR0Corpus TestCutoverPreservesIDsAndReferences TestCutoverLeavesOtherFamiliesByteIdentical'
rc=0
out="$(go test ./internal/books/ -run "^($(tr ' ' '|' <<<"$tests"))\$/^shaped\$" -count=1 -v -timeout 15m 2>&1)" || rc=$?
grep -E -- '^\s*(--- |PASS|FAIL|ok)|_test\.go:[0-9]+: ' <<<"$out" || true
if [ "$rc" -ne 0 ]; then
  echo "$out"
  exit "$rc"
fi
for t in $tests; do
  if ! grep -q -- "--- PASS: ${t}/shaped " <<<"$out"; then
    echo "::error::${t}/shaped did not run and pass (skipped, renamed or removed?)"
    exit 1
  fi
done
