<!-- file: .claude/notes/feat-search-result-cache-progress.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4c8e2a17-9b3d-4f60-a5e1-2d7c0b9f3e48 -->
<!-- last-edited: 2026-09-25 -->

# feat/search-result-cache progress

## Done
- Review fixes for all real findings plus the 2 critic findings (commit "fix(search): close the result-cache review findings"), each with a regression test.
- Change ring raised from 8192 to 65536 records (owner decision).
- Rebased onto origin/proposed-main.
- Staticcheck: 22 known findings, none in touched files.
- Targeted `-race` tests pass: searchcache, server TestSearchResultCache*, abs TestSearch*, audiobooks, handlers/audiobooks, database scoped.
- vitest api.searchPoll: 8/8.

## Next
- Finish the `make test-all-short` + `coverage-check-short` run.
- Fast-forward push to proposed-main.
- Update the PR #3563 body.

## Pre-existing on proposed-main (not from this branch)
- `sdkguard`: internal/chaptershape is a new dependency.
- `fmt-check`: internal/plugins/maintenance/author_strip_merge.go is not gofmt-clean.
