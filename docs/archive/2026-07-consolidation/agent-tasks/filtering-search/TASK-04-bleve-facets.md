<!-- file: docs/agent-tasks/filtering-search/TASK-04-bleve-facets.md -->
<!-- version: 1.0.0 -->
<!-- guid: 26630397-94ab-4f77-bfc3-0828bfdbf10c -->
<!-- last-edited: 2026-07-10 -->

# TASK-04 — Bleve facet counts with DB-distinct fallback (INIT-4 T4)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). T1/T2 are user-visible correctness fixes — ship first.
**File-ownership:** none

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · feature-additive subagent · **Why:** spans four files across three packages with a response-shape compatibility constraint (handler + warmer lockstep) · **Depends on:** TASK-01 (shares `internal/search/bleve_index.go` — start only after TASK-01's PR merges, branch from fresh origin/main)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/filtering-search-bleve-facets" -b agent/filtering-search-bleve-facets origin/main
cd "$REPO/.worktrees/filtering-search-bleve-facets"
git rebase origin/main
```

## Goal

`GET /audiobooks/facets` today returns only DB-distinct string lists
(`{"genres":[...],"languages":[...]}`) with no counts and no tags. Add real Bleve facet
aggregation: a new `BleveIndex.FacetCounts` method (bleve `FacetRequest` over the
already-keyword-indexed `genre`/`language`/`tags` fields), a thin
`AudiobookService.FacetCounts` wrapper, and ADDITIVE response keys
`genre_counts`/`language_counts`/`tag_counts`. The existing keys stay byte-identical (the
frontend dropdowns read them). REUSE the existing `SearchNative` request-building style and
the `ErrSearchIndexUnavailable` sentinel pattern from `internal/playlist/evaluator.go`
(verify: `grep -n "ErrSearchIndexUnavailable" internal/playlist/evaluator.go`) — define the
service's own sentinel in the new file rather than importing the playlist's. Fail-open: any
Bleve error → today's DB-distinct-only response, never a 500 from facets.

## Background (verify before editing)

- Handler: `AudiobookFacets` in `internal/server/handlers/audiobooks/handler.go` — cache
  check on `facetsCacheKey`, then `store.GetDistinctGenres()` / `GetDistinctLanguages()`,
  then `gin.H{"genres": genres, "languages": languages}` into the cache. DB-distinct only,
  no Bleve anywhere in the function (confirmed at planning time).
- **Warmer twin:** `internal/server/audiobooks_helpers.go` pre-warms the SAME
  `facetsCacheKey` with the SAME shape at startup. If the handler's response gains keys and
  the warmer doesn't, the first-request-after-boot shape flickers. Both MUST build the
  response through one shared helper.
- Facet fields are already keyword-indexed in the Bleve mapping (`genre`, `language`,
  `tags`) — no mapping change, no index rebuild.
- The service already holds the index as `svc.searchIndex` (used by `searchWithBleve` in
  `internal/audiobooks/service_query.go`); it is nil until startup indexing opens it.
- Does the handler reach the service? Check how `h.audiobookService` methods are called
  (e.g. `CountAudiobooks` in the same handler file) and mirror that.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func (h \*Handler) AudiobookFacets' internal/server/handlers/audiobooks/handler.go  # edit target, ~:639, 1 hit
  grep -n "facetsCacheKey" internal/server/audiobooks_helpers.go                               # warmer twin, >=2 hits
  grep -n 'AddFieldMappingsAt("genre"' internal/search/bleve_index.go                          # facet field indexed, 1 hit
  grep -n "func (b \*BleveIndex) SearchNative" internal/search/bleve_index.go                  # request-building style to mirror, 1 hit
  grep -n "searchIndex" internal/audiobooks/service_query.go                                   # service's index handle, >=1 hit
  grep -n "ErrSearchIndexUnavailable" internal/playlist/evaluator.go                           # sentinel pattern to mirror, >=1 hit
  ```
  Zero hits on any grep = STOP and report; the file has drifted.

## Step-by-step

1. In `internal/search/bleve_index.go`, add (mirroring `SearchNative`'s lock/nil-check
   preamble — use the grep above):
   ```go
   // FacetCounts returns value->count maps for the genre, language, and
   // tags keyword fields via a MatchAll facet search. size caps distinct
   // values per facet; <=0 defaults to 200.
   func (b *BleveIndex) FacetCounts(size int) (genres, languages, tags map[string]int, err error)
   ```
   Implementation: `bleve.NewSearchRequestOptions(bleve.NewMatchAllQuery(), 0, 0, false)`,
   then `req.AddFacet("genres", bleve.NewFacetRequest("genre", size))` (and `"language"`,
   `"tags"`), run `b.idx.Search(req)`, convert each `res.Facets[...]` terms list to a
   `map[string]int`. Missing facet in the result → empty map, not nil.
2. Create `internal/audiobooks/service_facets.go` (NEW — do NOT add this to
   service_query.go; it is owned by sibling tasks): `func (svc *AudiobookService)
   FacetCounts() (genres, languages, tags map[string]int, err error)` returning a package
   sentinel error when `svc.searchIndex == nil` (mirror the playlist sentinel style).
3. In `internal/server/handlers/audiobooks/handler.go` AND
   `internal/server/audiobooks_helpers.go`: extract ONE shared response-builder used by
   both (the handler package boundary decides where it lives — put the builder beside
   whichever of the two currently owns the store fetch, and have the other call through;
   if the packages cannot share it cleanly, duplicate the gin.H construction is FORBIDDEN —
   instead have the warmer call the same service/handler helper). The builder: DB-distinct
   lists exactly as today; then attempt `FacetCounts()`; on success add
   `"genre_counts"`, `"language_counts"`, `"tag_counts"`; on ANY error (including nil-index
   sentinel) omit the count keys and log at debug. HTTP status stays 200 in all cases.
4. Purely additive: existing response keys, cache key, TTL, and warmup call order unchanged;
   `SearchNative` untouched; no new HTTP route.
5. Edge semantics (in tests too): nil index → response EXACTLY equals today's shape (no
   `*_counts` keys, not empty ones); empty library → 200 with empty lists/maps.
6. Tests:
   - `internal/search/bleve_index_test.go`: index fixture docs with known genres/tags →
     assert exact count maps (mirror an existing index test's setup:
     `grep -n "func Test" internal/search/bleve_index_test.go`).
   - Handler-level test beside the existing audiobooks handler tests (find them:
     `grep -rln "AudiobookFacets" internal/server --include='*_test.go'`; if none exist,
     add one following the nearest handler test file's gin-test harness): nil-index
     fallback shape + with-index shape. Anti-over-suppression: N/A (no filter/guard added —
     additive read surface).
7. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

Focused loop: `go test ./internal/search/... ./internal/audiobooks/... ./internal/server/... -run 'Facet' -race -count=1`

## Acceptance criteria

- [ ] `grep -n "func (b \*BleveIndex) FacetCounts" internal/search/bleve_index.go` hits
- [ ] `grep -n "FacetCounts" internal/audiobooks/service_facets.go internal/server/handlers/audiobooks/handler.go` hits both
- [ ] Nil-index test: response deep-equals today's `{"genres":[...],"languages":[...]}` shape
- [ ] Handler + warmer share one response builder (grep the builder's name in both files, 2 hits total minimum)
- [ ] Anti-over-suppression: N/A
- [ ] Tests green; vet/lint clean (`make ci`)
- [ ] File headers bumped on every changed file

## Commit message

```
feat(search): Bleve facet counts for genres/languages/tags (INIT-4 T4)

The facets endpoint returned only DB-distinct string lists. Add
BleveIndex.FacetCounts over the already-indexed keyword fields, a
service wrapper with a nil-index sentinel, and additive *_counts
response keys — existing keys and the startup warmer stay in lockstep,
DB-distinct remains the fallback on any index error.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/filtering-search-bleve-facets
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "FacetCounts" internal/search/bleve_index.go` hits, this task is already applied
(additive polarity: presence of the new method) — run the acceptance checks instead of
re-applying. Rollback = revert the commit; the endpoint returns to DB-distinct-only lists,
the cache key and warmer behavior are unchanged, and no index or schema state is touched.
