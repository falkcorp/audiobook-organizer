<!-- file: docs/agent-tasks/filtering-search/TASK-01-freetext-field-boosts.md -->
<!-- version: 1.0.0 -->
<!-- guid: 401efac1-0e75-4114-ae52-f8d164ec7fa5 -->
<!-- last-edited: 2026-07-10 -->

# TASK-01 — Apply free-text field boosts at query time (INIT-4 T1)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). T1/T2 are user-visible correctness fixes — ship first.
**File-ownership:** none

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · search/ranking subagent · **Why:** needs bleve v2 query-API judgment, but scope is two files with exact instructions · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/filtering-search-freetext-boosts" -b agent/filtering-search-freetext-boosts origin/main
cd "$REPO/.worktrees/filtering-search-freetext-boosts"
git rebase origin/main
```

## Goal

Make the intended field weights (title 3.0, author 2.0, series 1.5, narrator 1.2,
publisher 1.0, description 0.5, file_path 0.5) actually affect free-text search ranking.
Today they are passed to a helper that never reads them: bleve v2.6.0 (see `go.mod`) removed
index-time field boosts, so `mapping.FieldMapping` has nothing to set. The fix is
query-time: the translator's free-text path fans out into a `DisjunctionQuery` of per-field
boosted `MatchQuery` children. REUSE the existing `SetBoost` pattern already used in
`translateField` for explicit `field:value^boost` queries (verify:
`grep -n "SetBoost" internal/search/bleve_translator.go`). Do NOT invent a new boost config
type, do NOT touch the index mapping's field list, and do NOT change `SearchNative`.

## Background (verify before editing)

- `bookIndexMapping` in `internal/search/bleve_index.go` defines
  `textAnalyzed := func(boost float64) *mapping.FieldMapping {...}` whose body never reads
  `boost` — a dead parameter. Its 7 call sites carry the intended weights.
- `translateFreeText` in `internal/search/bleve_translator.go` returns a bare unfielded
  `bleve.NewMatchQuery(n.Value)` in its default branch — this searches the `_all` composite
  field with no per-field weighting. It has three other branches (Prefix, Fuzzy, Quoted)
  which are OUT OF SCOPE — leave them byte-identical (documented non-goal, spec Non-goals).
- Changing the query construction requires NO index rebuild — this is purely query-time.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'textAnalyzed := func' internal/search/bleve_index.go        # edit target, ~line 224, 1 hit
  grep -n 'textAnalyzed(' internal/search/bleve_index.go               # 8 hits: 1 decl + 7 call sites (title 3.0 ... file_path 0.5)
  grep -n "func translateFreeText" internal/search/bleve_translator.go # edit target, 1 hit
  grep -n "SetBoost" internal/search/bleve_translator.go               # copy-from pattern, >=4 hits
  ```
  Zero hits on any grep = STOP and report; the file has drifted.

## Step-by-step

1. Open `internal/search/bleve_index.go`, locate `textAnalyzed := func` (use the grep above —
   never trust line numbers from this brief).
2. Add a package-level ordered table next to `bookIndexMapping` (ordered slice, NOT a map —
   query construction must be deterministic for tests):
   ```go
   // textFieldBoosts is the query-time boost table for free-text search.
   // bleve v2 has no index-time field boost, so translateFreeText fans a
   // free-text term out across these fields with these weights. Keep in
   // sync with the analyzed-text fields registered in bookIndexMapping.
   var textFieldBoosts = []struct {
   	Field string
   	Boost float64
   }{
   	{"title", 3.0}, {"author", 2.0}, {"series", 1.5}, {"narrator", 1.2},
   	{"publisher", 1.0}, {"description", 0.5}, {"file_path", 0.5},
   }
   ```
3. Remove the dead `boost float64` parameter from `textAnalyzed` and drop the numeric
   arguments at its 7 call sites (`textAnalyzed(3.0)` → `textAnalyzed()`), leaving a comment
   at the decl pointing to `textFieldBoosts`. Change nothing else about the mapping.
4. Open `internal/search/bleve_translator.go`, locate `translateFreeText`. Replace ONLY the
   final default `return bleve.NewMatchQuery(n.Value)` with a fan-out:
   ```go
   children := make([]query.Query, 0, len(textFieldBoosts)+1)
   for _, fb := range textFieldBoosts {
   	mq := bleve.NewMatchQuery(n.Value)
   	mq.SetField(fb.Field)
   	mq.SetBoost(fb.Boost)
   	children = append(children, mq)
   }
   // Unfielded child preserves recall: anything that matched via _all
   // before (tags, genre, isbn text) still matches, at neutral weight.
   children = append(children, bleve.NewMatchQuery(n.Value))
   return bleve.NewDisjunctionQuery(children...)
   ```
   The Prefix/Fuzzy/Quoted branches above it stay untouched.
5. Keep the change purely additive beyond the dead-param removal — do not touch
   `translateField`, `SearchNative`, analyzers, or any keyword/numeric mappings; do not
   change any function signature other than `textAnalyzed`'s (an unexported closure).
6. Add/extend tests:
   - `internal/search/bleve_translator_test.go`: default free text yields a
     `*query.DisjunctionQuery` with 8 children; the title child has Boost 3.0 (bleve queries
     expose `Boost()`/`BoostVal` — assert via type switch); Quoted/Fuzzy/Prefix free text
     still yields the same query types as before (regression pins).
   - `internal/search/bleve_index_test.go` (end-to-end, mirror an existing index+search test
     in that file — find one: `grep -n "func Test" internal/search/bleve_index_test.go`):
     index two fixture docs, term in doc A's title and doc B's description only → A ranks
     first. Anti-over-suppression: a doc matching ONLY on a non-boosted `_all` field (e.g.
     tags) is still returned for that term.
7. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

Focused loop while iterating: `go test ./internal/search/... -run 'FreeText|Facet|Bleve' -race -count=1`

## Acceptance criteria

- [ ] `grep -n "textFieldBoosts" internal/search/bleve_index.go internal/search/bleve_translator.go` hits in BOTH files
- [ ] `grep -n "boost float64" internal/search/bleve_index.go` returns 0 hits (dead param removed)
- [ ] Translator test asserts DisjunctionQuery with title boost 3.0 + one unfielded child
- [ ] Anti-over-suppression: tag-only match still returned (test name contains `Recall` or `TagOnly`)
- [ ] Quoted/Fuzzy/Prefix free-text behavior unchanged (regression tests green)
- [ ] Tests green; vet/lint clean (`make ci`)
- [ ] File headers bumped on every changed file

## Commit message

```
fix(search): apply free-text field boosts at query time (INIT-4 T1)

bleve v2 removed index-time field boosts, so textAnalyzed's boost arg
was dead and title/author/series weights never applied. Fan free text
out into a boosted DisjunctionQuery (plus an unfielded child to keep
recall) in translateFreeText — the one place all free-text queries pass
through.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/filtering-search-freetext-boosts
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "textFieldBoosts" internal/search/bleve_translator.go` hits AND
`grep -n "boost float64" internal/search/bleve_index.go` returns 0 hits, this task is already
applied (transform polarity: new symbol present + dead param absent) — run the acceptance
checks instead of re-applying. Rollback = revert the commit; ranking returns to unweighted
`_all` matching, no index rebuild required either way (query-time only).
