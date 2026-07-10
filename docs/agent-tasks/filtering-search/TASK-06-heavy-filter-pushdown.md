<!-- file: docs/agent-tasks/filtering-search/TASK-06-heavy-filter-pushdown.md -->
<!-- version: 1.0.0 -->
<!-- guid: b126c7b2-5c40-486c-9b54-27c57c8b817c -->
<!-- last-edited: 2026-07-10 -->

# TASK-06 — Parity-lock the shipped heavy-filter pushdown + narrow its fetch-all fallback (INIT-4 T6) [⚠ review-critical]

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). T1/T2 are user-visible correctness fixes — ship first.
**File-ownership:** none

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · test-hardening subagent · **Why:** the pushdown itself is ALREADY SHIPPED — this task locks it with parity tests and touches one error-path fallback; regressions here surface as missing books, so it keeps coordinator line-review · **Depends on:** TASK-03 (shares `internal/audiobooks/service_query.go` — start only after TASK-03's PR merges, branch from fresh origin/main)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/filtering-search-filter-pushdown" -b agent/filtering-search-filter-pushdown origin/main
cd "$REPO/.worktrees/filtering-search-filter-pushdown"
git rebase origin/main
```

## Goal

**RESCOPED in review — the pushdown the original brief asked you to build ALREADY EXISTS at
HEAD.** `GetAudiobooks`' heavy-filter branch routes through
`svc.buildBookSummaryFilterWithLookupCount(f, sortAsc)` +
`svc.summariesPushdownFiltered(pdLimit, pdOffset, bsf)` with REAL limit/offset, covering
LibraryState, Tag/Tags (→ `RestrictToIDs`), FieldFilters, per-user
(`ListFilters.PerUserFilters`), FingerprintStatus/CoveragePercent (denormalized in-loop
predicates), and non-title sorts (predicates pushed, `applySorting` on the smaller slice).
Do NOT build routing, do NOT add a filter-construction helper, and do NOT narrow the shipped
predicates — an executor doing so would duplicate or REGRESS live code (spec Decision 9,
§C6). This task delivers the genuine residual:

1. **Parity/regression tests** (core deliverable) that LOCK the shipped pushdown so no
   future change silently narrows it.
2. **Narrow the residual fetch-all fallback:** the `pushdownOK == false` branch (reached
   when `GetBooksByTag` errors during tag→ID resolution) still calls
   `summariesPushdownFiltered(0, 0, database.BookSummaryFilter{})` — a full-corpus fetch.
   Either surface the tag-resolution error to the caller or keep the fallback and add a
   `slog.Warn` making the fetch-all visible; decide from evidence, state the choice + reason
   in the PR. If narrowing proves risky, ship the tests only and say so.

## Background (verify before editing)

- Shipped heavy-pushdown branch in `GetAudiobooks` (`internal/audiobooks/service_query.go`,
  the `else` of `if !hasHeavyPostFilters`): builds the filter via
  `buildBookSummaryFilterWithLookupCount`, fetches with real limit/offset (zeroed only for
  `heavySorting`, which still fetches only the FILTERED subset), skips the post-filter pass
  when `didPushdown && !heavySorting`. The `storeLimit = 0` zeroing above it still greps but
  only feeds the legacy in-memory path and the fallback branch.
- Shipped filter construction: `buildBookSummaryFilterWithLookupCount` in
  `internal/audiobooks/service_filtering.go` — tag intersection → `RestrictToIDs` (returns
  `ok=false` when `GetBooksByTag` errors); LibraryState (+ plucked `library_state`/review
  FieldFilters); remaining FieldFilters/per-user/fingerprint via a read-only `Predicate`
  closure with a stripped-field Pebble fallback. READ its doc comments before writing tests.
- The residual fetch-all: the `else` arm after `pushdownOK` —
  `summariesPushdownFiltered(storeLimit, storeOffset, database.BookSummaryFilter{})` with
  `storeLimit == 0` (fetch-all). Only reachable on tag-resolution error.
- Pushdown machinery under test: `MemStore.GetBookSummaries(limit, offset, f
  BookSummaryFilter)` in `internal/database/memdb_summaries.go` (`LibraryState`,
  `RestrictToIDs` — empty set = fast empty return, nil = no restriction — and read-only
  `Predicate`).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "buildBookSummaryFilterWithLookupCount" internal/audiobooks/service_query.go internal/audiobooks/service_filtering.go  # shipped pushdown, hits BOTH files
  grep -n 'func.*AudiobookService.*summariesPushdownFiltered' internal/audiobooks/service_filtering.go  # shipped wrapper, 1 hit (countSummariesPushdownFiltered has a capital S, so it does not double-match)
  grep -n 'storeLimit = 0' internal/audiobooks/service_query.go                                  # legacy zeroing (feeds fallback), ~:59, 1 hit
  grep -n 'Predicate func..Book. bool' internal/database/memdb_summaries.go                      # pushdown hook, 1 hit
  grep -n "RestrictToIDs" internal/database/memdb_summaries.go                                   # tag-set hook, >=2 hits
  grep -n 'NewPebbleStore' internal/audiobooks/transcribed_title_pushdown_test.go                # test harness to mirror (Step 2), 1 hit
  grep -n 'filteredSummaryStore' internal/audiobooks/service_filtering.go                        # spy seam: type assertion the pin hooks (Step 2), >=2 hits
  grep -n 'GetAllBookSummariesFiltered' internal/database/pebble_store.go                        # real impl the spy delegates to, >=2 hits
  grep -rn "PushdownParity" internal/audiobooks --include='*_test.go'                            # MUST be 0 hits before you start (see Idempotency)
  ```
  Zero hits on the first grep = the pushdown is NOT shipped in your checkout — STOP and
  report (the rescope premise has drifted). Zero hits on any other verify-grep = STOP and
  report. The patterns are deliberately paren-free (`.` wildcards where the source has
  `(`, `)`, or `*`) so the SAME string resolves under both POSIX bash grep (BRE) and
  ripgrep/PCRE-style Grep tools — do NOT "tighten" them with literal or backslash-escaped
  parens; run them exactly as written, in either tool.

## Step-by-step

1. Investigation (read-only, ~30 min budget): trace the shipped heavy-pushdown branch
   end-to-end (`buildBookSummaryFilterWithLookupCount` → `summariesPushdownFiltered` →
   `bookSummariesToBooks` → post-filter skip logic) and the `pushdownOK == false` fallback.
   Write the trace into the PR description — it is the review evidence for the parity tests.
2. Tests — `internal/audiobooks/service_filtering_pushdown_test.go` (NEW). **Harness:**
   there is NO MemStore/mock-based service harness in this package — mirror the sibling
   pushdown test `internal/audiobooks/transcribed_title_pushdown_test.go` (anchor grep
   above), which exercises the ACTUAL service path with a real store:

   ```go
   ps, err := database.NewPebbleStore(t.TempDir())
   require.NoError(t, err)
   t.Cleanup(func() { _ = ps.Close() })
   ps.WaitForWarmup() // memdb must be warm or the pushdown path is not live
   // seed fixtures via ps.CreateBook(&database.Book{...})
   svc := NewAudiobookService(ps)
   ```

   **parity tests** are the core deliverable — seed ~50 books, run library_state-only,
   tag-only, tags-multi, FieldFilters, fingerprint-filter, and non-title-sort (`SortBy:
   "rating"`) queries through `GetAudiobooks` and assert the pages (IDs + order) equal a
   reference evaluation computed by filtering/sorting the seeded fixtures directly in the
   test, for several limit/offset combos (name the suite `TestLibraryStatePushdownParity` /
   `TestPushdownParityFingerprintAndSort` per the spec Testing table).
   Anti-over-suppression: a filter matching everything returns the full page.
   **Anti-narrowing pins (Decision 9 guard):** fingerprint and non-title-sort queries must
   provably go through the pushdown. There is NO generated mock for the summary getters
   (`grep -rn "GetAllBookSummariesFiltered" internal/database/mocks/` → 0 hits — do not go
   hunting for one), so detect "no full-corpus unfiltered fetch" with a spy wrapper defined
   in the test file:

   ```go
   type pushdownSpyStore struct {
   	*database.PebbleStore // promotes the full store surface → satisfies NewAudiobookService

   	mu              sync.Mutex
   	filteredCalls   []database.BookSummaryFilter
   	unfilteredCalls int
   }

   func (s *pushdownSpyStore) GetAllBookSummariesFiltered(limit, offset int, f database.BookSummaryFilter) ([]database.BookSummary, error) {
   	s.mu.Lock()
   	s.filteredCalls = append(s.filteredCalls, f)
   	s.mu.Unlock()
   	return s.PebbleStore.GetAllBookSummariesFiltered(limit, offset, f)
   }

   func (s *pushdownSpyStore) GetAllBookSummaries(limit, offset int) ([]database.BookSummary, error) {
   	s.mu.Lock()
   	s.unfilteredCalls++
   	s.mu.Unlock()
   	return s.PebbleStore.GetAllBookSummaries(limit, offset)
   }
   ```

   Why this works: `summariesPushdownFiltered` (`internal/audiobooks/service_filtering.go`)
   type-asserts `svc.store` against a local `filteredSummaryStore` interface (anchor grep
   above); the spy's own `GetAllBookSummariesFiltered` shadows the promoted PebbleStore
   method, so the assertion resolves to the spy and the real pushdown still runs
   underneath. For the pinned tests construct the service as
   `svc := NewAudiobookService(&pushdownSpyStore{PebbleStore: ps})`. For each pinned query
   (fingerprint filter; non-title sort paired WITH the fingerprint filter, matching the
   spec name `TestPushdownParityFingerprintAndSort`): reset both counters, run the query,
   then assert (a) `unfilteredCalls == 0`, and (b) at least one recorded `filteredCalls`
   entry has `Predicate != nil` (fingerprint and FieldFilters push through the
   `BookSummaryFilter.Predicate` closure; tag queries set `RestrictToIDs` instead), and
   (c) NO recorded entry is the zero-value `database.BookSummaryFilter{}` — a zero-value
   filter is exactly the fetch-all fallback arm
   (`summariesPushdownFiltered(storeLimit, storeOffset, database.BookSummaryFilter{})`)
   that a future narrowing would silently reroute these queries through.
3. Fallback narrowing in `internal/audiobooks/service_query.go` — ONLY the
   `pushdownOK == false` branch: surface the tag-resolution error OR keep the fallback and
   add `slog.Warn("GetAudiobooks: pushdown filter construction failed; falling back to full fetch", ...)`.
   No other edit to this file. Edge case in tests: tag with zero books → empty page via the
   pushdown (empty `RestrictToIDs` set short-circuits), NOT the fetch-all fallback.
4. Purely additive elsewhere: do not modify `GetBookSummaries`,
   `buildBookSummaryFilterWithLookupCount`, `summariesPushdownFiltered`, the shipped
   predicate closures, `GetAudiobooks`' signature/response shape, or the light-filter branch.
5. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
make ci
go test ./... -short   # FULL suite: the routing change sits under every library-list consumer
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "TestLibraryStatePushdownParity" internal/audiobooks/service_filtering_pushdown_test.go` hits (parity suite present)
- [ ] Parity tests green: pages (IDs + order) match the reference evaluation for library_state / tag / tags / FieldFilters / fingerprint / non-title-sort combos across limit/offset combos
- [ ] Anti-narrowing pins green: fingerprint + non-title-sort queries provably go through the shipped pushdown via the `pushdownSpyStore` wrapper — `unfilteredCalls == 0`, ≥1 recorded filter with `Predicate != nil`, no zero-value `BookSummaryFilter{}` recorded (Decision 9 guard)
- [ ] Anti-over-suppression: match-everything filter returns the full page
- [ ] Shipped pushdown untouched: `git diff origin/main -- internal/audiobooks/service_filtering.go` is EMPTY, and the only `service_query.go` change is the `pushdownOK == false` fallback branch (or none, if tests-only was chosen — say so in the PR)
- [ ] Fallback decision (surface error vs warn-and-fetch-all) stated + justified in the PR description
- [ ] Tests green (`make ci` + full `go test ./... -short`); vet/lint clean
- [ ] File headers bumped on every changed file

## Commit message

```
test(audiobooks): parity-lock the shipped heavy-filter pushdown (INIT-4 T6)

The library heavy-filter pushdown (LibraryState/tag/FieldFilters/
per-user/fingerprint/non-title-sort via buildBookSummaryFilterWith-
LookupCount + summariesPushdownFiltered) shipped without parity tests.
Add a parity suite locking it against narrowing, plus anti-narrowing
pins for the fingerprint/sort predicates, and make the pushdownOK-false
tag-resolution fallback loud instead of a silent full-corpus fetch.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/filtering-search-filter-pushdown
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -rn "TestLibraryStatePushdownParity" internal/audiobooks --include='*_test.go'`
hits, this task is already applied (additive polarity: presence of the parity suite) — run
the acceptance checks instead of re-applying. Rollback = revert the commit; the tests are
pure additions and the shipped pushdown was never modified, so behavior is unchanged either
way (the only behavioral surface is the fallback branch's error/warn, itself a one-line
revert). No data or schema is touched.
