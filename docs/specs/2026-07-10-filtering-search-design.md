<!-- file: docs/specs/2026-07-10-filtering-search-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: ca9c28ea-dcfb-4d69-855e-0c1c8ddf9465 -->
<!-- last-edited: 2026-07-10 -->

# Filtering & Search Pipeline (INIT-4) — Design Spec

**Status:** Approved — ready for implementation planning, at Gate 2
**Scope:** Go backend only — `internal/search`, `internal/audiobooks`, `internal/playlist`,
`internal/database`, `internal/server/handlers/audiobooks`, `internal/dedup`, `internal/config`.
No frontend changes required (facet API is response-shape-compatible; UI opt-in is a follow-up).
**Parent task:** INIT-4 — Filtering & search pipeline
(`.claude/notes/2026-07-10-remaining-work-master-plan.md`)

---

## Motivation

Three concrete defects and three structural costs, all grep-verified at HEAD `fce58498`
on 2026-07-10 (anchors re-verified this session):

1. **Per-user filters are silently dropped (correctness — the top bug).**
   The DSL translator peels `read_status` / `progress_pct` / `last_played` off into a
   `[]PerUserFilter` (`internal/search/bleve_translator.go`, `translateField` — verify:
   `grep -n 'perUserFieldSet\[n.Field\]' internal/search/bleve_translator.go`), but
   `searchWithBleve` discards that return value with `_`
   (`internal/audiobooks/service_query.go` — verify:
   `grep -n 'search.Translate(ast)' internal/audiobooks/service_query.go`). A user searching
   `read_status:unread` gets **unfiltered** results. The doc comment on `searchWithBleve`
   admits it ("Per-user filters … are currently dropped here"). Meanwhile the smart-playlist
   evaluator applies the identical filters correctly
   (`internal/playlist/evaluator.go:applyPerUserFilters`).

2. **Field boosts are dead (relevance correctness).** `bookIndexMapping`'s `textAnalyzed`
   helper accepts a `boost float64` and never reads it (verify:
   `grep -n 'textAnalyzed := func' internal/search/bleve_index.go`); the intended
   title×3.0 / author×2.0 / series×1.5 / narrator×1.2 / publisher×1.0 / description×0.5 /
   file_path×0.5 weights (call sites:
   `grep -n 'textAnalyzed(' internal/search/bleve_index.go`) have never applied. Root cause:
   bleve v2 (go.mod pins `github.com/blevesearch/bleve/v2 v2.6.0`) removed index-time field
   boosts — `mapping.FieldMapping` has no boost field to set. Free-text queries
   (`translateFreeText`) emit a bare unfielded `MatchQuery` against `_all`, so a
   description-only match ranks equal to a title match.

3. **Per-hit rehydration.** Bleve hits are fetched one-by-one via `store.GetBookByID(h.BookID)`
   (verify: `grep -n 'GetBookByID(h.BookID)' internal/audiobooks/service_query.go`) — N point
   reads + N method dispatches per search page. No batch getter exists in the store
   (grep for `GetBooksByIDs` returns zero hits repo-wide).

4. **"Facets" are DB-distinct lists.** `AudiobookFacets` returns only
   `GetDistinctGenres()`/`GetDistinctLanguages()` (verify:
   `grep -n 'func (h \*Handler) AudiobookFacets' internal/server/handlers/audiobooks/handler.go`)
   — no counts, no tags, no interaction with the search query. Bleve keyword fields
   `genre`/`language`/`tags` are already indexed and facet-ready.

5. **Heavy-filter pushdown is SHIPPED but unlocked by tests (re-grounded in review — the
   master plan's `service_query.go:28,45-58` anchor was stale).** The `storeLimit = 0`
   zeroing still greps (verify: `grep -n 'storeLimit = 0' internal/audiobooks/service_query.go`),
   but the branch immediately after it ALREADY routes `hasHeavyPostFilters` queries through
   `svc.buildBookSummaryFilterWithLookupCount(f, sortAsc)` +
   `summariesPushdownFiltered(pdLimit, pdOffset, bsf)` with REAL limit/offset
   (`service_query.go` ~:134-176 at HEAD `fce58498`). That helper
   (`service_filtering.go` ~:589) resolves `Tag`/`Tags` → `RestrictToIDs`, `LibraryState`,
   and `FieldFilters`/per-user/`FingerprintStatus`/`CoveragePercent` via a `Predicate`
   closure; non-title sorts push the filter predicates down and sort the smaller slice.
   The genuine residual is: (a) ZERO parity/regression tests pin this pushdown (grep for
   `PushdownParity` in `internal/audiobooks/*_test.go` = 0 hits), and (b) the
   `pushdownOK == false` fallback (`service_query.go` ~:171, triggered when tag→ID
   resolution errors) still calls `summariesPushdownFiltered(0, 0, BookSummaryFilter{})` —
   a fetch-all.

6. **Boilerplate title blocklist is hardcoded.** Two English/Audible-only pattern slices live
   as `var` blocks in `internal/dedup/engine.go` (verify:
   `grep -n 'var boilerplateTitlePatterns' internal/dedup/engine.go`) — not extendable per
   publisher/locale without a code change.

**Goal:** ship the two user-visible correctness fixes (per-user filters, field boosts) first,
then remove the O(n) request-time scans and make facets and the blocklist real, all as
independent additive PRs.

## Goals

- `read_status:unread` (and all per-user DSL filters) actually filter Bleve search results,
  with correct pagination after filtering.
- Free-text relevance honors the intended field weights (title beats description).
- Hit hydration consolidated behind ONE store call per search page (an API seam — the store
  impl still performs the same N `book:<id>` point reads internally; see C3. The genuine
  O(68K) scan removal is T6, not T3).
- Facet counts (genres/languages/tags) served from Bleve when the index is open, with the
  existing DB-distinct behavior as fallback — response shape backward-compatible.
- Boilerplate title blocklist extendable via config without recompiling; defaults unchanged.
- The SHIPPED heavy-filter pushdown (LibraryState/tag/FieldFilters/fingerprint/per-user/
  non-title-sort — see Motivation #5) is locked in by parity/regression tests, and the
  residual `pushdownOK == false` fetch-all fallback is narrowed or explicitly documented.
  No task may NARROW the existing pushdown coverage.

## Non-goals (v1)

- Frontend faceted-browse UI — deferred (API ships first; response shape is additive).
- Boosting the quoted/fuzzy/prefix free-text variants — only the default match fans out;
  the rare variants keep today's behavior (documented in T1).
- New memdb indexes or `GetBookSummaries` semantic changes for T6 — the shipped pushdown
  hooks are sufficient; T6 is parity tests + fallback narrowing (§C6). (An earlier draft
  said per-user filters and heavy sorts "stay Go-side" — that was stale: they are ALREADY
  pushed down via `Predicate` closures on the library-list path and must stay that way.)
- DSL grammar changes; index schema changes requiring a version-gated rebuild.
- i18n blocklist CONTENT (translated pattern lists) — v1 delivers the config mechanism only.

## Decisions (locked during design)

1. **Boost mechanism: query-time fan-out, not index-time mapping.** bleve v2.6.0 has no
   index-time field boost (the `textAnalyzed(boost)` param was dead on arrival). Spot-checked
   in review: `go doc github.com/blevesearch/bleve/v2/mapping FieldMapping` shows NO `Boost`
   field on the pinned version — the "v2 removed the capability" claim is accurate, not just
   "the param was never wired". The fix is a
   `DisjunctionQuery` in `translateFreeText` over per-field boosted `MatchQuery` children,
   NOT attempting to resurrect mapping-level boosts (losing alternative: fork/pin bleve v1
   semantics — rejected, index rebuild + dead-end API).
2. **Recall preservation in the fan-out.** The disjunction keeps one unfielded `MatchQuery`
   child at boost 1.0 so anything that matched via `_all` before (tags, genre, ISBN text)
   still matches — the change reorders ranking, it never shrinks the result set.
3. **Per-user evaluator moves to `internal/search`, single source of truth.** The playlist
   evaluator's `perUserFilterMatches`/`numericFieldMatches`/`timeFieldMatches` logic is lifted
   into `internal/search` (which already imports `internal/database` — see
   `internal/search/index_builder.go`) and the playlist package delegates to it. Losing
   alternative: a second copy in `internal/audiobooks` — rejected, divergent semantics risk.
4. **Post-filter pagination = over-fetch window, then slice.** When per-user filters are
   present, `searchWithBleve` fetches from Bleve at `from=0, size=searchPostFilterWindow`
   (10000, mirroring the playlist's `defaultEvalPageSize`), applies filters, then applies
   caller offset/limit. Losing alternative: iterative re-query until the page fills —
   rejected as needless complexity at 68K-book scale. **Truncation contract (explicit):**
   a query whose PRE-filter Bleve matches exceed the window sees only the top-10000
   relevance-ranked hits — post-filter pagination beyond that window is silently truncated.
   Deep pagination past the window is OUT OF SCOPE for v1 (same accepted tradeoff as the
   playlist evaluator's page size); executors must NOT treat truncated deep pages as a
   regression. For observability, `searchWithBleve` emits a `slog.Warn` whenever the
   pre-filter hit count reaches `searchPostFilterWindow`, so window exhaustion is visible
   in logs (see C2; day-one logging rule).
5. **Missing user context keeps today's behavior, loudly.** If per-user filters exist but
   `UserID == ""`, filters are skipped (as today) and a `slog.Warn` records the drop. The
   handler already sets `filters.UserID` from the authenticated caller, so the warn path is
   for un-authed internal callers only. Losing alternative: evaluate against a zero-value
   state — rejected, would silently empty results for anonymous calls.
   **Per-hit state-read error (explicit, FAIL-OPEN):** a non-nil `GetUserBookState` error on
   an individual hit (distinct from not-found — see
   `internal/database/pebble_store_playback.go`) evaluates that hit against the zero-value
   `UserBookState` — behavior parity with the playlist evaluator's `state, _ :=` call site —
   AND emits a `slog.Warn` with the book ID and error so the degradation is operator-visible
   (never a silent `_` discard on the search path). Precise behavior of this choice
   (stated exactly — "fail-open" is polarity-DEPENDENT, not uniform): on a state-read error
   the hit is evaluated as zero-value state, which KEEPS hits under negated filters
   (`NOT read_status:finished` still matches) and DROPS hits under positive filters
   (`read_status:finished` fails against zero-value) — and the warn makes either outcome
   visible. Losing alternative: fail-closed (drop the hit unconditionally) — rejected, it
   would drop ALL such hits regardless of filter polarity AND introduce a new
   silent-suppression mode for transient store errors.
6. **Batch getter is Full-fidelity `[]Book`, on the `BookReader` interface.**
   `GetBooksByIDs(ids []string) ([]Book, error)` point-gets full `book:<id>` JSON rows (same
   as `GetBookByID`) — NOT a memdb-slim projection. Missing IDs are skipped silently
   (today's `if b != nil` semantics); input order is preserved (Bleve relevance order must
   survive). Losing alternative (named in review): a service-local `hydrate(ids)` helper in
   `internal/audiobooks` looping the existing `GetBookByID` — byte-identical behavior today
   with no interface/mock churn. Kept on the interface anyway because the seam is the ONLY
   place a real Pebble snapshot/iterator multi-get can ever land without touching consumers,
   and the concrete near-term consumer is `searchWithBleve` itself: after C2 it hydrates up
   to `searchPostFilterWindow` (10K) hits per per-user-filtered request, exactly the shape a
   snapshot-consistent batch read improves. The mock cost is one hand-added method (scoped);
   the full-suite run is mandated by the store-getter rule either way once the interface
   changes — that cost is accepted, not accidental.
7. **Facets response shape is additive.** `{"genres":[…],"languages":[…]}` keys stay exactly
   as-is (string arrays — the startup warmer in `internal/server/audiobooks_helpers.go`
   writes the same cache key and must stay in lockstep); Bleve-backed
   `genre_counts`/`language_counts`/`tag_counts` maps are ADDED. Nil index → counts omitted,
   lists from DB-distinct as today.
8. **Blocklist: compiled-in defaults + config extension — extension-ONLY, no replace.** New
   `internal/dedup/boilerplate.go` owns the default slices + `isBoilerplateTitle`; config
   adds extra patterns that APPEND to the compiled-in defaults. Losing alternatives:
   DB-stored list with CRUD UI — rejected for v1, config file covers the per-publisher/i18n
   need without new surface; a `replace_defaults` escape hatch — rejected (cut in review):
   no named use case, and setting it would silently drop ALL compiled-in Audible/publisher
   suppression, re-opening the exact dedup-seeding bug the list exists to prevent. Add a
   replace path only when a concrete need is named.
9. **Pushdown is ALREADY SHIPPED and must be PRESERVED, not built (inverted in review —
   the original decision was regression-inducing).** At HEAD `fce58498`,
   `buildBookSummaryFilterWithLookupCount` (`service_filtering.go` ~:589) already pushes
   down LibraryState, Tag/Tags (`RestrictToIDs`), FieldFilters, per-user
   (`ListFilters.PerUserFilters`), and FingerprintStatus/CoveragePercent (denormalized
   in-loop predicates); non-title sorts push the filter predicates down and `applySorting`
   runs on the smaller filtered slice. An earlier draft of this decision locked fingerprint
   filters and non-title sorts OUT of pushdown ("stay on the fetch-all path") — implementing
   that faithfully would have NARROWED the shipped pushdown and reintroduced the ~68K
   fetch-all those queries already avoid. **No task may revert or narrow the
   `service_filtering.go` ~:590-596 predicates.** T6's scope is parity tests over the
   existing path + narrowing the residual `pushdownOK == false` fetch-all fallback
   (see §C6). No new memdb indexes in v1.
10. **File ownership:** `internal/dedup/engine.go` is INIT-2-OWNED. TASK-05 (the only INIT-4
    task touching it) is scheduled AFTER INIT-2's engine.go waves merge, rebased on top —
    same partition rule as INIT-1.
11. **Minimal kill switch for T2's per-user post-filter path (added in review).** T2 adds a
    per-request amplification: a per-user-filtered query over-fetches up to
    `searchPostFilterWindow` (10K) hits and issues up to 10K SEQUENTIAL `GetUserBookState`
    point reads. With no toggle, a prod load incident's only mitigation is revert-PR +
    Minimal CI + `make deploy` (minutes). So T2 ships ONE config bool,
    `DisablePerUserSearchFilters` (json `disable_per_user_search_filters`, default `false` =
    new correct behavior ON; `internal/audiobooks` already imports `internal/config`, so
    `config.AppConfig` is cycle-free). When `true`, `searchWithBleve` skips per-user
    post-filtering and emits the SAME drop-warn as the empty-userID path — i.e. today's
    documented behavior, bounded by a config flip + restart instead of a deploy cycle. This
    is NOT the general default-ON feature flag that was rejected for M1 (that rejection
    stands for T1, which is ranking-only and stays revert-only): it targets only the
    amplifying path, per the CLAUDE.md request-path concurrency guidance.

## Data model

```go
// internal/search/per_user_match.go (NEW — lifted verbatim-in-behavior from
// internal/playlist/evaluator.go). PerUserFilter itself already exists in
// bleve_translator.go and is unchanged.

// MatchPerUserFilters reports whether state satisfies EVERY filter
// (AND semantics, matching the playlist evaluator). A nil state is
// evaluated as the zero-value UserBookState (status="", progress=0):
// read_status:finished rejects an unstarted book; negated filters can
// still succeed against nil.
func MatchPerUserFilters(state *database.UserBookState, filters []PerUserFilter) bool
```

```go
// internal/database/iface_book.go — BookReader gains ONE method:

// GetBooksByIDs returns the full Book rows for ids, in input order,
// silently skipping IDs that do not resolve (mirrors GetBookByID's
// nil-on-ErrNotFound). Full fidelity: reads book:<id> JSON, never a
// slim projection.
GetBooksByIDs(ids []string) ([]Book, error)
```

```go
// internal/config/config.go — dedup boilerplate extension (defaults empty):

// DedupBoilerplateConfig extends the compiled-in boilerplate title
// blocklist (internal/dedup/boilerplate.go) without a code change.
// Extension-only: extras APPEND to the compiled-in defaults, which are
// always active (Decision 8 — no replace escape hatch in v1).
type DedupBoilerplateConfig struct {
	// ExtraTitlePatterns are additional exact-title patterns (normalized
	// with util.NormalizeTitle+CollapseSpaces at load time).
	ExtraTitlePatterns []string `json:"extra_title_patterns" mapstructure:"extra_title_patterns"`
	// ExtraPrefixPatterns are additional anchored-prefix patterns.
	ExtraPrefixPatterns []string `json:"extra_prefix_patterns" mapstructure:"extra_prefix_patterns"`
}
```

```go
// internal/config/config.go — T2 kill switch (Decision 11; default false = feature ON):

// DisablePerUserSearchFilters, when true, makes searchWithBleve skip
// per-user DSL post-filtering (read_status/progress_pct/last_played)
// and warn, restoring the pre-T2 drop-and-warn behavior. Ops escape
// hatch for the 10K-sequential-state-read amplification; not a
// feature flag.
DisablePerUserSearchFilters bool `json:"disable_per_user_search_filters"`
```

```go
// internal/search/bleve_index.go — facet API (additive; SearchNative unchanged):

// FacetCounts runs a MatchAll search with facet requests over the
// keyword fields and returns value→count maps. size caps distinct
// values per facet (default 200).
func (b *BleveIndex) FacetCounts(size int) (genres, languages, tags map[string]int, err error)
```

### Persistence

- No new keyspaces. No index schema change (facet fields `genre`/`language`/`tags` are
  already keyword-indexed). No data migration anywhere in this initiative.

## Components

### C1. Free-text boost fan-out (`internal/search/bleve_translator.go`, `bleve_index.go`)

`translateFreeText`'s default (unquoted/non-fuzzy/non-prefix) branch returns
`DisjunctionQuery( match(title)^3.0, match(author)^2.0, match(series)^1.5,
match(narrator)^1.2, match(publisher)^1.0, match(description)^0.5, match(file_path)^0.5,
match(_all fields / unfielded)^1.0 )` using the existing `mq.SetBoost` pattern already used
for explicit `field:value^boost` queries in `translateField`. The boost table lives as one
ordered package-level slice (`textFieldBoosts`) in `bleve_index.go` next to the mapping so
mapping and ranking stay in one file; `textAnalyzed` loses its dead `boost float64` param.
Fail-open: no error paths added — pure query construction.

### C2. Per-user filter application in search (`internal/search/per_user_match.go` NEW,
`internal/playlist/evaluator.go`, `internal/audiobooks/service_query.go`)

`searchWithBleve` gains a `userID string` param (call site passes `f.UserID`, already
populated from the authenticated caller in the handler). When `Translate` returns per-user
filters AND `userID != ""`: over-fetch (Decision 4), keep hits where
`search.MatchPerUserFilters(state, filters)` with `state, stateErr :=
store.GetUserBookState(userID, h.BookID)` — on a non-nil `stateErr` (non-not-found),
`slog.Warn` with book ID + error and evaluate the zero-value state (FAIL-OPEN, Decision 5;
never a silent `_` discard) — then slice offset/limit. When the pre-filter Bleve hit count
reaches `searchPostFilterWindow`, `slog.Warn` that the window is exhausted (results beyond
it are truncated — Decision 4 contract). When `userID == ""`: skip + `slog.Warn`
(Decision 5). When `config.AppConfig.DisablePerUserSearchFilters` is true: skip + the same
`slog.Warn` (kill switch, Decision 11 — restores today's documented drop behavior without a
deploy). Playlist's private evaluator functions are replaced by delegation to the new
exported helper — behavior-identical, covered by existing playlist tests.

### C3. Batch hydration (`internal/database/iface_book.go`, `pebble_store.go`,
`mocks/mock_store.go`, `internal/audiobooks/service_query.go`)

`PebbleStore.GetBooksByIDs` loops point-gets over `book:<id>` (the loop is bounded by the
request page size — ≤10K after C2's window, not whole-library scale, so no worker pool per
the CLAUDE.md concurrency rule's own threshold; stated in a comment). `searchWithBleve`
replaces its per-hit loop with one call. Mock updated by hand (scoped — mockery version
drift regenerates the world; see MOCKERY_GUIDE.md).
**Framing (explicit):** this is an API/readability consolidation, NOT an algorithmic win —
the store performs the SAME N sequential point reads; the value is one interface seam + one
call site (and a future home for a real snapshot/iterator multi-get). Do not add a worker
pool or a batching layer. **Error semantics (explicit):** per-item not-found is skipped
silently (today's `if b != nil` parity); a non-not-found read/unmarshal error is surfaced as
the getter's error ALONGSIDE the rows read so far, and `searchWithBleve` logs a `slog.Warn`
and serves the partial page (fail-open — parity with today's discarded-error loop, where the
page silently shrank; now it shrinks loudly).

### C4. Bleve facets (`internal/search/bleve_index.go`, `internal/audiobooks/service_facets.go`
NEW, `internal/server/handlers/audiobooks/handler.go`, `internal/server/audiobooks_helpers.go`)

`BleveIndex.FacetCounts` uses `bleve.NewFacetRequest("genre", size)` etc. on a MatchAll
request. New `AudiobookService.FacetCounts` wraps it (nil index → `ErrSearchIndexUnavailable`
sentinel, mirroring the playlist package's PATTERN — a deliberate reuse exception: the
sentinel VALUE is defined package-locally rather than imported, because importing an error
value from `internal/playlist` into `internal/audiobooks` would couple two unrelated domains;
only the concrete error value is duplicated, the pattern is mirrored). `AudiobookFacets`
handler and the
startup warmer both emit: lists as today (fallback + dropdown compatibility) + `*_counts`
maps when the index is open. Fail-open: any Bleve error → DB-distinct-only response
(today's behavior), never a 500 caused by facets.

### C5. Config-driven blocklist (`internal/dedup/boilerplate.go` NEW,
`internal/dedup/engine.go`, `internal/config/config.go`)

The two pattern slices + `isBoilerplateTitle` move verbatim to `boilerplate.go`; a
`sync.Once`-guarded loader merges `config.AppConfig` extras (normalized at load). All nine
existing call sites (`engine.go`, `drain_stale.go`) compile unchanged — same package, same
symbol. **Default behavior byte-identical** when config is empty.

### C6. Heavy-filter pushdown — RESCOPED to parity lock + residual fallback
(`internal/audiobooks/service_filtering_pushdown_test.go` NEW; optionally
`internal/audiobooks/service_query.go`)

**Re-grounded in review (Motivation #5): the pushdown this component originally asked an
executor to BUILD already exists at HEAD.** `GetAudiobooks`' heavy branch routes through
`buildBookSummaryFilterWithLookupCount` + `summariesPushdownFiltered` with real
limit/offset, covering LibraryState, Tag/Tags, FieldFilters, per-user, fingerprint, and
non-title sorts (Decision 9, inverted). Building it again would duplicate or conflict with
shipped code. T6's genuine residual scope:

1. **Parity/regression tests (the core deliverable).** Fixture tests on the same in-memory
   store comparing the pushdown path against a forced fetch-all evaluation for
   library_state-only, tag-only, tags-multi, FieldFilters, fingerprint-filter, and
   non-title-sort queries — identical pages (IDs + order) across limit/offset combos. These
   tests LOCK the shipped pushdown against future narrowing (Decision 9 guard).
2. **Narrow the residual fetch-all fallback (small, optional-if-risky).** The
   `pushdownOK == false` branch (`service_query.go` ~:171, reached when `GetBooksByTag`
   errors during tag→ID resolution) falls back to
   `summariesPushdownFiltered(0, 0, BookSummaryFilter{})` — a full fetch. Either surface the
   tag-resolution error to the caller instead of silently fetching everything, or keep the
   fallback and add a `slog.Warn` making the fetch-all visible; the executor decides from
   evidence and states the choice in the PR. If narrowing proves risky, ship tests only.

No routing predicate is added; no `hasHeavyPostFilters` split; the shipped
`service_filtering.go` ~:590-596 predicates are untouched.

## Migration / integration

No data migration. Two mechanical call-site changes:

**Before** (`internal/audiobooks/service_query.go`, verify:
`grep -n 'search.Translate(ast)' internal/audiobooks/service_query.go`):

```go
bleveQ, _, err := search.Translate(ast)
```

**After:**

```go
bleveQ, perUser, err := search.Translate(ast)
```

**Before** (same file, verify: `grep -n 'GetBookByID(h.BookID)' internal/audiobooks/service_query.go`):

```go
for _, h := range hits {
	b, _ := svc.store.GetBookByID(h.BookID)
	...
}
```

**After:** one `svc.store.GetBooksByIDs(ids)` call. Playlist integration: private evaluator
funcs replaced by `search.MatchPerUserFilters` delegation (exact IDs pinned in TASK-02).

## Milestones

- **M1 — Correctness (T1+T2).** Field boosts apply; per-user filters apply. User-visible
  ranking/result changes are the POINT of these fixes (not feature-flag-gated — they restore
  documented, spec'd behavior; rollback = revert PR). **Operator note:** T1 (ranking only)
  has NO in-process kill switch — rollback latency is one revert-PR + Minimal-CI cycle
  (minutes, not instant); deliberate, accepted tradeoff, stated again in Rollback below.
  T2 additionally ships the `DisablePerUserSearchFilters` ops kill switch (Decision 11)
  because its per-user path amplifies to up to 10K sequential state reads per request — a
  prod load incident there is bounded by a config flip + restart, not a deploy.
- **M2 — Perf + facets (T3+T4).** Batch hydration (behavior-invariant) and facet counts
  (additive response keys). No existing behavior changes.
- **M3 — Config + pushdown lock (T5+T6).** Blocklist externalized (byte-identical
  defaults); shipped heavy-filter pushdown parity-locked with tests + residual fetch-all
  fallback narrowed (§C6). T5 waits for INIT-2's engine.go waves.

Each milestone is independently shippable; every task is its own PR.

## Files modified

| File | Change | Task |
|---|---|---|
| `internal/search/bleve_translator.go` | free-text boost fan-out | T1 |
| `internal/search/bleve_index.go` | boost table; drop dead param; `FacetCounts` | T1, T4 |
| `internal/search/per_user_match.go` | NEW: exported per-user evaluator | T2 |
| `internal/playlist/evaluator.go` | delegate to `search.MatchPerUserFilters` | T2 |
| `internal/audiobooks/service_query.go` | apply per-user filters; batch hydrate; (optional) narrow pushdown fetch-all fallback | T2, T3, T6 |
| `internal/database/iface_book.go` | `GetBooksByIDs` on BookReader | T3 |
| `internal/database/pebble_store.go` | `GetBooksByIDs` impl | T3 |
| `internal/database/mocks/mock_store.go` | hand-added mock method (scoped) | T3 |
| `internal/audiobooks/service_facets.go` | NEW: service facet wrapper | T4 |
| `internal/server/handlers/audiobooks/handler.go` | Bleve-backed facet counts + fallback | T4 |
| `internal/server/audiobooks_helpers.go` | warmer parity with new response shape | T4 |
| `internal/dedup/boilerplate.go` | NEW: blocklist + config merge (moved from engine.go) | T5 |
| `internal/dedup/engine.go` | REMOVE blocklist vars + `isBoilerplateTitle` (moved) | T5 |
| `internal/config/config.go` | `DisablePerUserSearchFilters` (T2); `DedupBoilerplateConfig` (T5) | T2, T5 |
| `internal/audiobooks/service_filtering_pushdown_test.go` | NEW: parity/regression tests locking the shipped pushdown | T6 |

*Table lists production code plus T6's load-bearing new test file; the remaining `_test.go`
targets (e.g. `internal/search/bleve_index_test.go`, shared T1/T4) are enumerated in each
brief's Exact-files list and the plan's collision matrix.*

## Testing

| Test | Asserts |
|---|---|
| `TestTranslateFreeTextBoostsFields` | disjunction contains title child with boost 3.0; unfielded child present (recall) |
| `TestFreeTextStillMatchesTagOnlyDoc` | doc matching only via a non-boosted field still returned (anti-over-suppression) |
| `TestSearchWithBleveAppliesReadStatus` | `read_status:finished` drops unfinished books; pagination applied post-filter |
| `TestSearchWithBleveNoUserIDKeepsResults` | empty userID → unfiltered results + warn (no silent empty page) |
| `TestSearchWithBleveStateErrorFailsOpen` | erroring `GetUserBookState` mock → hit evaluated as zero-value state + warn logged (never silently dropped) |
| `TestSearchWithBleveWindowExhaustionWarns` | pre-filter hits == `searchPostFilterWindow` → truncation warn logged (Decision 4 contract) |
| `TestSearchWithBleveKillSwitchDrops` | `DisablePerUserSearchFilters=true` → per-user filters skipped + warn (pre-T2 behavior; Decision 11) |
| `TestSearchWithBleveHydrationErrorPartialPage` | non-not-found `GetBooksByIDs` error → partial page served + warn (fail-open, §C3) |
| `TestMatchPerUserFiltersNilState` | nil state = zero-value semantics; negated filter matches nil |
| `TestGetBooksByIDsOrderAndSkips` | preserves input order; unknown ID skipped not errored; rows are Full (AcoustIDFingerprint intact) |
| `TestFacetCountsGenres` | indexed fixture docs produce correct genre→count map |
| `TestAudiobookFacetsFallbackNilIndex` | nil index → today's DB-distinct response, HTTP 200 |
| `TestIsBoilerplateTitleDefaultsUnchanged` | every pattern from the old engine.go lists still hits after the move |
| `TestBoilerplateConfigExtension` | config extra pattern hits; built-ins ALWAYS retained (extension-only, Decision 8); real title "Introduction to Algorithms" still passes (anti-over-suppression) |
| `TestLibraryStatePushdownParity` | shipped pushdown path and forced fetch-all evaluation return identical pages for the same filter |
| `TestPushdownParityFingerprintAndSort` | fingerprint-filter and non-title-sort queries STILL go through the shipped pushdown with identical results (Decision 9 anti-narrowing guard) |

## Rollback

**Gate (verbatim):** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). T1/T2 are
user-visible correctness fixes — ship first.

- Every task is a single revertible PR; no data, schema, or index-format migration anywhere.
- T1/T2 restore documented behavior; revert = back to dead boosts / dropped filters.
  **Rollback latency (explicit):** T1 (ranking only) has NO in-process kill switch — a bad
  ranking outcome in prod degrades every search until a revert PR clears Minimal CI
  (minutes, not instant). Deliberate: a general default-ON flag was considered and rejected —
  these PRs restore documented behavior, and the flag would be permanent dead surface after
  the first week. T2 is the exception (Decision 11): its per-user post-filter path can issue
  up to 10K sequential state reads per request, so `DisablePerUserSearchFilters=true` +
  restart restores the pre-T2 drop-and-warn behavior WITHOUT a deploy. Operators: T1 revert
  path is `gh pr revert` (or `git revert`) + Minimal CI + `make deploy`; T2 incidents flip
  the config first, revert second.
- T3 is behavior-invariant (order-preserving batch of the same point reads; a hydration
  error now serves a partial page + warn instead of silently shrinking — §C3).
- T4/T5 are additive/dormant by default: facets fall back on nil index or error; blocklist
  defaults are byte-identical with empty config.
- T6 is tests-first over an ALREADY-SHIPPED path (§C6): the parity tests are pure additions;
  the optional fallback-narrowing edit is a single revertible diff and the shipped pushdown
  itself is never modified.
- Nothing here mutates prod data; no AskUserQuestion apply gates are needed in this
  initiative (code-only PRs, Minimal CI gate per task).

## Open questions (resolved — recorded for the plan)

1. ~~Can bleve v2 apply mapping-level boosts?~~ → No (v2.6.0 removed index-time boost);
   query-time fan-out is the only mechanism (Decision 1).
2. ~~Where does the shared per-user evaluator live without an import cycle?~~ →
   `internal/search`, which already imports `internal/database` (Decision 3).
3. ~~Does the store need a new index for facets?~~ → No; `genre`/`language`/`tags` are
   already keyword-indexed in the Bleve mapping.
4. ~~Does T6 need new memdb indexes?~~ → No — and moreover the pushdown itself is already
   SHIPPED at HEAD (`buildBookSummaryFilterWithLookupCount` + `summariesPushdownFiltered`);
   T6 is rescoped to parity tests + residual fallback narrowing (Decision 9, §C6).
