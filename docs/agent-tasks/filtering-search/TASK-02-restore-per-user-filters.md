<!-- file: docs/agent-tasks/filtering-search/TASK-02-restore-per-user-filters.md -->
<!-- version: 1.0.0 -->
<!-- guid: b41bb050-c247-4cf7-a0ce-d8e04e337efb -->
<!-- last-edited: 2026-07-10 -->

# TASK-02 — Restore per-user filter application in searchWithBleve (INIT-4 T2) [⚠ review-critical]

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). T1/T2 are user-visible correctness fixes — ship first.
**File-ownership:** none

**Priority:** P0 · **Effort:** M · **Recommended subagent:** Sonnet-class · correctness-fix subagent · **Why:** changes which rows a user's search returns + pagination semantics; needs careful reuse of the playlist evaluator, then coordinator line-review · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/filtering-search-per-user-filters" -b agent/filtering-search-per-user-filters origin/main
cd "$REPO/.worktrees/filtering-search-per-user-filters"
git rebase origin/main
```

## Goal

`read_status:unread` (and `progress_pct:`/`last_played:` DSL filters) currently return
**unfiltered** search results: the translator peels them into a `[]PerUserFilter` but
`searchWithBleve` discards that slice with `_`. Fix by (a) lifting the playlist package's
already-correct per-user evaluator into `internal/search` as ONE exported source of truth,
(b) delegating the playlist to it, and (c) applying it in `searchWithBleve` with correct
pagination (over-fetch → filter → slice). REUSE the moved evaluator logic verbatim — do NOT
write a new matcher, do NOT duplicate it into `internal/audiobooks`, and do NOT touch the
separate `ListFilters.PerUserFilters` mechanism in `service_filtering.go` (that path already
works; this task is only about filters embedded in the search-DSL string).

## Background (verify before editing)

- Peel-off: `translateField` in `internal/search/bleve_translator.go` appends per-user fields
  (`read_status`, `progress_pct`, `last_played` — set defined as `perUserFieldSet`) to a
  `[]PerUserFilter` and returns nil query for them. `Translate` returns
  `(query.Query, []PerUserFilter, error)`.
- Discard: `searchWithBleve` in `internal/audiobooks/service_query.go` does
  `bleveQ, _, err := search.Translate(ast)`. Its own doc comment says the filters "are
  currently dropped here". The function signature is
  `searchWithBleve(query string, limit, offset int) ([]database.Book, error)`; its single
  caller inside `GetAudiobooks` (same file) has `f ListFilters` in scope with `f.UserID`
  already populated by the handler from the authenticated caller.
- Working evaluator to MOVE (not rewrite): `internal/playlist/evaluator.go` —
  `applyPerUserFilters` (walks IDs, `store.GetUserBookState(userID, id)`, AND semantics,
  `Negated` inversion) and `perUserFilterMatches` + `numericFieldMatches` +
  `timeFieldMatches` (nil state → zero-value `UserBookState`; `read_status` compares
  `strings.EqualFold(state.Status, node.Value)`; zero `LastActivityAt` never matches
  `last_played`). Its `defaultEvalPageSize = 10000` documents the over-fetch precedent.
- Import safety: `internal/search` ALREADY imports `internal/database`
  (see `internal/search/index_builder.go`), so the moved evaluator creates no import cycle.
- `internal/playlist` imports `internal/search` already — delegation is cycle-free too.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'perUserFieldSet\[n.Field\]' internal/search/bleve_translator.go     # peel-off, ~:145, 1 hit
  grep -n 'search.Translate(ast)' internal/audiobooks/service_query.go          # discard site, ~:532, 1 hit
  grep -n 'GetBookByID(h.BookID)' internal/audiobooks/service_query.go          # hit-hydration loop you slice AFTER, ~:542, 1 hit
  grep -n "func applyPerUserFilters" internal/playlist/evaluator.go             # move-from source, 1 hit
  grep -n "func perUserFilterMatches" internal/playlist/evaluator.go            # move-from source, 1 hit
  grep -n "func searchWithBleve\|func (svc \*AudiobookService) searchWithBleve" internal/audiobooks/service_query.go  # edit target, 1 hit
  grep -n "GetUserBookState" internal/database/iface_misc.go                    # store method to reuse, >=1 hit
  ```
  Zero hits on any grep = STOP and report; the file has drifted.

## Step-by-step

1. Create `internal/search/per_user_match.go` (new file, 4-line version header per repo
   standard). Move `perUserFilterMatches`, `numericFieldMatches`, and `timeFieldMatches`
   from `internal/playlist/evaluator.go` into it VERBATIM (same logic, adjusted receivers/
   names only), plus one new exported wrapper:
   ```go
   // MatchPerUserFilters reports whether state satisfies EVERY filter
   // (AND semantics; Negated inverts per filter). state==nil is evaluated
   // as the zero-value UserBookState — read_status:finished rejects an
   // unstarted book, negated filters can still succeed.
   func MatchPerUserFilters(state *database.UserBookState, filters []PerUserFilter) bool
   ```
   The AND-over-filters + Negated-inversion loop body comes from `applyPerUserFilters`'s
   inner loop (move-from grep above).
2. In `internal/playlist/evaluator.go`: delete the three moved private functions and rewrite
   `applyPerUserFilters`'s inner match block to call `search.MatchPerUserFilters(state,
   filters)`. Do not change its signature, its ID-walking shape, or `sortBookIDs`.
3. In `internal/audiobooks/service_query.go`:
   - Change the signature to `searchWithBleve(query string, limit, offset int, userID string)`
     and update the single call site to pass `f.UserID`. List every call site first:
     `grep -rn "searchWithBleve(" internal/ --include='*.go'` (expect the definition + 1 call;
     if more appear, update them all and say so in the PR).
   - Capture the filters: `bleveQ, perUser, err := search.Translate(ast)`.
   - Add `const searchPostFilterWindow = 10000` (mirrors the playlist's
     `defaultEvalPageSize`; do not import it — it is private and package-specific).
   - When `len(perUser) > 0 && userID != "" && !config.AppConfig.DisablePerUserSearchFilters`:
     call `svc.searchIndex.SearchNative(bleveQ, 0, searchPostFilterWindow)`, hydrate hits
     (keep the existing per-hit `GetBookByID` loop — TASK-03 batches it later; do NOT batch
     here), then filter each hit with the state read captured — NEVER the banned silent
     `state, _ :=` discard (spec Decision 5):
     ```go
     state, stateErr := svc.store.GetUserBookState(userID, b.ID)
     if stateErr != nil {
     	// FAIL-OPEN (Decision 5): evaluate the zero-value state, loudly.
     	slog.Warn("search: per-user state read failed; evaluating zero-value state",
     		"book_id", b.ID, "err", stateErr)
     	state = nil
     }
     if !search.MatchPerUserFilters(state, perUser) { continue }
     ```
     (If the store's not-found signal is a sentinel error rather than `(nil, nil)` — check
     `internal/database/pebble_store_playback.go` first — exclude it from the warn with
     `errors.Is`; not-found is normal, not degradation.) THEN apply `offset`/`limit` slicing
     on the filtered slice (offset beyond len → empty slice, not error — mirror the slicing
     shape used later in `GetAudiobooks`, verify:
     `grep -n "offset >= len(filtered)" internal/audiobooks/service_query.go`).
   - Window-exhaustion warn (Decision 4 contract): after `SearchNative`, when the pre-filter
     hit count reaches `searchPostFilterWindow`, emit
     `slog.Warn("search: post-filter window exhausted; results beyond it are truncated", "window", searchPostFilterWindow)`.
   - When `len(perUser) > 0 && userID == ""`: keep today's behavior (no per-user filtering,
     original limit/offset passed to Bleve) but emit
     `slog.Warn("search: per-user filters dropped, no user context", "filters", len(perUser))`.
   - Kill switch (spec Decision 11): when `len(perUser) > 0` and
     `config.AppConfig.DisablePerUserSearchFilters` is true, take the same skip-and-warn
     branch as empty userID (`"reason", "disabled_by_config"`). `internal/audiobooks`
     already imports `internal/config` (see `service_single.go`) — no cycle.
   - When `len(perUser) == 0`: existing fast path byte-identical (Bleve does the paging).
   - Update the now-stale doc comment above `searchWithBleve` (it currently says filters are
     dropped).
4. In `internal/config/config.go`: add the kill-switch field to `type Config struct`
   (mirror a neighboring bool's style):
   ```go
   // DisablePerUserSearchFilters, when true, makes searchWithBleve skip
   // per-user DSL post-filtering (read_status/progress_pct/last_played)
   // and warn — the pre-fix drop-and-warn behavior. Ops escape hatch for
   // the up-to-10K sequential state reads per per-user-filtered request
   // (spec Decision 11); NOT a feature flag. Default false = filters ON.
   DisablePerUserSearchFilters bool `json:"disable_per_user_search_filters"`
   ```
   Zero-value default (false) means no default-block wiring is needed; verify existing
   config files without the key still unmarshal with the feature ON.
5. Purely additive beyond the listed edits: do not modify `translateField`, `Translate`'s
   signature, the fallback `SearchBooks` paths, or `service_filtering.go`.
6. Edge semantics (spell in tests too): nil `UserBookState` = zero-value state;
   `read_status:finished` does NOT match a book with no state; a NEGATED filter
   (`NOT read_status:finished`) DOES match a book with no state; empty `userID` must never
   produce an empty result page when matches exist; a state-read ERROR on one hit keeps the
   page serving (zero-value eval + warn), never a silent drop and never a request failure.
7. Tests:
   - `internal/search/per_user_match_test.go` (NEW): nil-state semantics, negation, numeric
     range (`progress_pct:50..100`), AND-across-filters.
   - `internal/audiobooks` (extend the existing service query tests — find them:
     `grep -rln "searchWithBleve\|SearchBooks" internal/audiobooks/*_test.go`): with a mock/
     fixture store + real Bleve index: (a) `read_status:finished` returns only the finished
     book; (b) pagination: 3 matching books, limit=2 offset=2 → 1 book (slice applied AFTER
     filtering); (c) empty userID → all matches returned (anti-over-suppression) — and the
     positive case proving a plain no-per-user-filter query still returns hits unchanged;
     (d) `TestSearchWithBleveStateErrorFailsOpen` (spec Testing table): erroring
     `GetUserBookState` mock → hit evaluated as zero-value state AND the warn is logged
     (capture via a test `slog` handler) — never silently dropped;
     (e) `TestSearchWithBleveWindowExhaustionWarns` (spec Testing table): pre-filter hits ==
     `searchPostFilterWindow` → truncation warn logged;
     (f) `TestSearchWithBleveKillSwitchDrops` (spec Decision 11): with
     `DisablePerUserSearchFilters=true`, per-user filters are skipped + warn logged, results
     match today's unfiltered behavior (restore the config value with `t.Cleanup`).
   - Playlist regression: existing `internal/playlist` tests must pass UNMODIFIED (they pin
     the moved evaluator's behavior).
8. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
make ci
go test ./... -short   # FULL suite mandatory: evaluator moved across packages (store-getter/consumer rule)
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "func MatchPerUserFilters" internal/search/per_user_match.go` hits
- [ ] `grep -n "func perUserFilterMatches" internal/playlist/evaluator.go` returns 0 hits (moved, not copied)
- [ ] `grep -n "bleveQ, _, err" internal/audiobooks/service_query.go` returns 0 hits (filters no longer discarded)
- [ ] `read_status:finished` test drops unfinished books; pagination-after-filter test green
- [ ] `grep -n "state, _ := svc.store.GetUserBookState\|state, _ := store.GetUserBookState" internal/audiobooks/service_query.go` returns 0 hits (no silent `_` discard on the search path — spec Decision 5)
- [ ] `TestSearchWithBleveStateErrorFailsOpen` green (zero-value eval + warn logged)
- [ ] `TestSearchWithBleveWindowExhaustionWarns` green (truncation warn at window)
- [ ] `TestSearchWithBleveKillSwitchDrops` green + `grep -n "DisablePerUserSearchFilters" internal/config/config.go internal/audiobooks/service_query.go` hits both
- [ ] Anti-over-suppression: empty-userID query still returns all Bleve matches; no-filter query byte-identical
- [ ] Existing `internal/playlist` tests pass unmodified
- [ ] Tests green (`make ci` + full `go test ./... -short`); vet/lint clean
- [ ] File headers bumped on every changed file

## Commit message

```
fix(search): apply per-user DSL filters in searchWithBleve (INIT-4 T2)

read_status/progress_pct/last_played filters were peeled off by the
translator and then discarded with `_` — read_status:unread returned
unfiltered results. Lift the playlist evaluator into internal/search as
the single source of truth, delegate the playlist to it, and apply it in
searchWithBleve with over-fetch -> filter -> offset/limit slicing.
State-read errors fail open (zero-value eval + warn); window exhaustion
warns; disable_per_user_search_filters config bool is the ops kill
switch for the per-request state-read amplification.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/filtering-search-per-user-filters
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "MatchPerUserFilters" internal/search/per_user_match.go` hits AND
`grep -n "func perUserFilterMatches" internal/playlist/evaluator.go` returns 0 hits, the move
is already done (transform polarity: symbol at new location + absent at old) — run acceptance
instead of re-applying. Rollback = revert the commit; the playlist regains its private
evaluator and search returns to the documented drop-the-filters behavior. Fast ops
mitigation WITHOUT a revert: set `disable_per_user_search_filters: true` + restart (spec
Decision 11) — restores the drop-and-warn behavior for the amplifying path only. No data or
schema is touched.
