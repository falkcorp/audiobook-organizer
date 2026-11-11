#!/usr/bin/env bash
# file: scripts/ci/run_cutover_suite.sh
# version: 1.1.1
# guid: cb3cc064-170c-4661-b9ad-f3d45d18b2b2
# last-edited: 2026-11-10
#
# The body of extraction-gates' `r2-cutover-suite` job (F2-15; 05-storage
# §5S.10, 09 §9.3). `run_cutover_suite.sh <shard>` runs the cutover suite and
# the restore drill on one corpus, then fails unless every expected subtest
# printed a PASS line (a skipped or renamed test makes `go test -run` exit 0):
#   r0                 the committed R0 corpus, the selftest (both corpora),
#                      the pre-freeze tag reader and the shared-family golden
#   prefreeze-written  the corpus the pre-freeze binary wrote into (rule 10)
# The shaped corpus runs in production-shaped-store
# (scripts/ci/run_production_shaped_store.sh). Each recording boots the
# pre-freeze binary for about five minutes (the overlay's deps sweep), so the
# two shards run as two jobs.
#
# The pre-freeze tag ($PREFREEZE_TAG, else resolved here; `--print-tag` prints
# it): ci/prefreeze_release.txt (F3-08) when it exists, read by
# fetch_prefreeze_binary.sh --resolve; until then the latest published
# release that is not a pre-release. R2_PREFREEZE_BIN set skips the fetch.
set -euo pipefail

# prefreeze_tag prints the pre-freeze release tag.
prefreeze_tag() {
  local tag
  if [ -f ci/prefreeze_release.txt ]; then
    tag="$(scripts/ci/fetch_prefreeze_binary.sh --resolve ci/prefreeze_release.txt)"
  else
    tag="$(gh release list --exclude-pre-releases --exclude-drafts --limit 1 --json tagName -q '.[0].tagName')"
  fi
  [ -n "$tag" ] || { echo "::error::no pre-freeze release tag" >&2; return 1; }
  echo "$tag"
}

# `--print-tag`: the job keys its binary cache on the tag.
if [ "${1:-}" = "--print-tag" ]; then
  prefreeze_tag
  exit 0
fi

shard="${1:?usage: run_cutover_suite.sh r0|prefreeze-written | --print-tag}"
case "$shard" in
  r0) sub='r0|selftest' ;;
  prefreeze-written) sub='prefreeze-written' ;;
  *) echo "unknown shard $shard" >&2; exit 2 ;;
esac

if [ -z "${R2_PREFREEZE_BIN:-}" ]; then
  tag="${PREFREEZE_TAG:-$(prefreeze_tag)}"
  echo "pre-freeze release: $tag"
  R2_PREFREEZE_BIN="$(scripts/ci/fetch_prefreeze_binary.sh "$tag")"
  export R2_PREFREEZE_BIN
fi

# Every corpus is required here: a test that cannot build one fails, never
# skips (cutover_suite_test.go strictCutover).
export R2_CUTOVER_STRICT=1

tests='TestCutoverOnR0Corpus TestCutoverPreservesIDsAndReferences TestCutoverLeavesOtherFamiliesByteIdentical TestRollbackRestoreBootsPreviousBuild'
want=()
for t in $tests; do want+=("$t/$shard"); done
run="^($(tr ' ' '|' <<<"$tests"))\$/^(${sub})\$"
# The drill's halves. The checkpoint half on prefreeze-written skips while no
# step or legacy migration runs on that corpus (nothing to checkpoint; the
# archive half restores it), so only r0's is required.
want+=("TestRollbackRestoreBootsPreviousBuild/$shard/archive")
if [ "$shard" = r0 ]; then
  want+=(TestRollbackRestoreBootsPreviousBuild/r0/checkpoint
    TestCutoverOnR0Corpus/selftest/r0 TestCutoverOnR0Corpus/selftest/prefreeze-written
    TestCutoverOnR0Corpus/selftest/answers TestCutoverOnR0Corpus/selftest/manifest
    TestCutoverOnR0Corpus/selftest/references TestCutoverOnR0Corpus/selftest/checkpoint-guard
    TestCutoverOnR0Corpus/selftest/id-resolution
    TestPrefreezeTagResolver TestPrefreezeFetchVerifiesChecksum)
  python3 scripts/ci/gen_shared_family_lists.py --check
fi

# Not `go test ... | tee`: a pipe would hide go test's exit status.
rc=0
out="$(go test ./internal/books/ -run "$run" -count=1 -v -timeout 22m 2>&1)" || rc=$?
if [ "$shard" = r0 ] && [ "$rc" -eq 0 ]; then
  out+=$'\n'"$(go test ./internal/books/ -run '^(TestPrefreezeTagResolver|TestPrefreezeFetchVerifiesChecksum)$' -count=1 -v 2>&1)" || rc=$?
fi
grep -E -- '^\s*(--- |PASS|FAIL|ok)|_test\.go:[0-9]+: ' <<<"$out" || true
if [ "$rc" -ne 0 ]; then
  echo "$out"
  exit "$rc"
fi
for t in "${want[@]}"; do
  if ! grep -qE -- "--- PASS: ${t} " <<<"$out"; then
    echo "::error::${t} did not run and pass (skipped, renamed or removed?)"
    exit 1
  fi
done
