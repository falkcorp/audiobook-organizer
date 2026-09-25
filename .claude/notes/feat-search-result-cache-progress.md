<!-- file: .claude/notes/feat-search-result-cache-progress.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4c8e2a17-9b3d-4f60-a5e1-2d7c0b9f3e48 -->
<!-- last-edited: 2026-09-25 -->

# feat/search-result-cache progress

## State: review fixes delivered and integrated into proposed-main
- Every real review finding and both critic findings are fixed. Every one has a regression test except the ABS min(gen, hitsGen) stamp in browse.go: that is a guard its callers cannot reach, and the cache-level fix is tested by TestCache_JoinedOlderBuildIsBroughtForward.
- Mutation probes are caught for:
  - the ABS bad-row completeness flag;
  - exact-caller options;
  - the coverage-sweep record;
  - the indexBookChunk record;
  - the hydrateIDsOnly selection;
  - ABS withSig=false.
- The change ring was raised from 8192 to 65536 records (owner decision). A full ring holds about 3.5 MiB.
- The slog ratchet for service_query.go was lowered from 16 to 14.

## make ci at integration
- staticcheck: 22 known findings, none in this branch's files.
- sdkguard fails, and fmt-check fails on author_strip_merge.go. Both are pre-existing on proposed-main.
- test-short: the database and abs packages hit the 25m timeout at load average ~150. abs passes when run alone (1314s). database was run scoped.
