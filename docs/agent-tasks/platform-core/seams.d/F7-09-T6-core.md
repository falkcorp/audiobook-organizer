<!-- file: docs/agent-tasks/platform-core/seams.d/F7-09-T6-core.md -->
<!-- version: 0.1.0 -->
<!-- guid: 75a9fd71-b676-480d-926f-a24239fc212e -->
<!-- last-edited: 2026-11-20 -->

# F7-09 T6 core pre-PR: what books' search needs from `storage/index`

The T6 builder (`internal/books/searchindex/**`, merged as #4847) reported the places where core's full-text contract could not express the books search that runs in production (`internal/search`): five in the brief of this PR, and seven in `seams.d/F7-09-T6.md`'s "Switch at the fold" table (the same five plus a match-all and an `_all` field). This core-only PR closes all seven; the per-user `Mixed` shape in that table is books' own (F7-09 T7) and needs nothing from core. It is dark: nothing in the deployed binary opens core's index engine (ABO's `internal/search` keeps `library.bleve` until the fold), so no running code path changes. 05-storage §5S.3.8 carries the new Go block lines and a paragraph on analysis, weighting and wildcards.

## What it adds

| Gap | Surface | Where |
|---|---|---|
| 1. Analyzer | `index.Spec.Analyzers []index.Analyzer`: an index's own analyzers, built from core's closed component set. Char filter `CharUnderscoreToSpace`; tokenizers `TokenizeUnicodeWords`, `TokenizeWholeValue`; token filters `FilterLowercase`, `FilterEnglishPossessive`, `FilterPorterStem`. A Field names one in `Field.Analyzer`. `Spec.Validate` refuses unknown components, a missing tokenizer, a duplicate name, a built-in's name and a Field naming an analyzer that is neither built-in nor declared. The engine registers each under a prefixed bleve name (`core_spec_<name>`), so a module name never collides with bleve's global registry. | `core/storage/index/analyzer.go`, `core/internal/bleveindex/mapping.go` |
| 1. Fingerprint | The mapping marker's SHA-256 now covers the analyzer definitions after the field lines, so an analyzer edit under an unchanged `MappingVersion` recreates the index and lists it in `PendingRebuilds`. A Spec with no analyzers hashes exactly as before (`TestStampOfASpecWithoutAnalyzersIsUnchanged`), so no existing index is recreated by this PR. | `mapping.go` `mappingStamp` |
| 2. Case-insensitive keyword | Built-in analyzer `index.AnalyzerKeywordLowercase` = `"keyword_lowercase"`: the whole value as one lowercased term. Facet terms come back lowercased. | `analyzer.go`, `mapping.go` |
| 2. Folding | `Term`, `Prefix` and `Wildcard` match index terms without analysis, so the engine lowercases their text on a Text or Keyword field whose analyzer lowercases (`standard`, `keyword_lowercase`, or a Spec analyzer with `FilterLowercase`). This is HEAD's `patternTerm` moved into core. Nothing is stemmed and no char filter runs on that text. `keyword` stays case-sensitive. | `mapping.go` `foldText` |
| 3. Field boosts | `index.Field.Boost float64` (0 means 1; negative, NaN and infinite are refused; Text and Keyword only). It multiplies the score of every `Match` node naming the field. It does not weight Term, Prefix, Wildcard or Range, and it does not weight a field named inside a `QueryString`. It is not part of the fingerprint, so changing it needs no rebuild. | `index.go`, `mapping.go` |
| 3. Query-level boosts | `index.Boost(q, weight)` sets `Node.Boost` (finite, above 0; the last one wins) and weights any node: Match, Term, Prefix, Wildcard, Range, And, Or, Not, All, a QueryString (on top of any `^n` inside it). On a Match it **replaces** `Field.Boost`; the two never multiply, so `Boost(Match("title", x), 2)` scores like HEAD's `title:x^2` (pinned against `QueryString("title:x^2")`), not 3 x 2. This is the surface for T6's `field:term^3` and for a translator that carries every weight in its tree. | `query.go`, `mapping.go` `translateNode` |
| 4. Leading wildcards | `index.Wildcard(field, pattern)` / `index.OpWildcard`. `*` matches any run of characters, `?` matches one, and there is no escape. A pattern with no literal character (`*`, `*?*`, empty) is `ErrInvalidQuery`. `QueryString` still refuses a leading wildcard and every regexp, so `Wildcard` is the explicit opt-in. | `query.go`, `mapping.go` |
| 4. Bounded cost | A leading wildcard walks the field's whole term dictionary in every segment through the pattern's automaton (linear in the field's distinct terms), then searches every term that matched. Each matched term counts against the engine's term-searcher cap for the whole search (`Options.MaxTermSearchers`, default 1024). Past the cap, Search, Count and Facets fail with the new typed **`index.ErrTooBroad`** instead of running. Each Wildcard node also costs `wildcardCost` = 32 of the query's 1024-node budget, so a query holds at most 31 of them beside its own group node. | `index.go` `ErrTooBroad`, `bleveindex/index.go` `searchErr` |
| 6. Match-all | `index.All()` / `index.OpAll`: every document, for a listing with no search term. It counts 64 nodes (a Not's cost, `notCost`): it walks every document. T6's stand-in `Not(Term(format, <absent term>))` becomes `index.All()`. | `query.go`, `mapping.go` |
| 7. `_all` | `index.MatchAnyField(text)` / `index.OpMatchAny`: an unweighted analysed match against every Text and Keyword field at once (bleve's composite `_all` field, which every text field mapping feeds by default). The text is analysed with the **standard** analyzer and compared with the terms each field's own analyzer stored, so on a stemming field it matches only where the standard form equals the stem (pinned: `families` finds nothing, `famili` and `family` find both "Family" titles). This is HEAD's unfielded child of `translateFreeText` (`bleve.NewMatchQuery(text)` with no field), with the same analysis. | `query.go`, `mapping.go` |
| 5. Test support | `coretest.IndexOwner()` (the `c/index/` owner spec) and `coretest.NewIndexEngine(tb, st, coretest.IndexEngineOptions{Dir, MaxTermSearchers})` return the real engine over a `coretest.Store` booted with whatever owners and `storage.Spec.Derived` names the caller chose. Methods: `Opener(module)`, `Reconcile(ctx)`, `Rebuild(ctx, module, name) (docs, err)`, `PendingRebuilds()`, `Dir()`, `Path(module, name)` and an idempotent `Close()`. Nothing runs in the background. `bleveindex.Engine.Rebuild` is exported for it (it was `runRebuild`, the task's `Run`). It lives in `coretest`, not `indextest`, because bleveindex's in-package tests import `indextest` (an import cycle) and books' tests already import `coretest`. | `core/coretest/index.go`, `bleveindex/rebuild.go` |

The pinning tests are `core/internal/bleveindex/analysis_test.go` (owner `arkham`, index `cases`, on `coretest.IndexEngine`), `core/internal/bleveindex/stamp_test.go` and `core/storage/index/analyzer_test.go`. They pin these behaviours:

- stemming;
- the stop-word phrase: "A Death in the Family" against "Death of the Family", with the standard analyzer as the contrast that matches both;
- the underscore split;
- possessives;
- case-insensitive keyword terms, prefixes and facets, and case-sensitive `keyword`;
- the boost ordering, with an unweighted control that shows the reverse order, and `index.Boost` replacing the field's weight (against `QueryString("title:x^2")`) and weighting Prefix, Wildcard, groups and query strings;
- leading, double-ended and `?` wildcards, the no-literal refusal, the `QueryString` refusal, the 31/32 node budget, and `ErrTooBroad` from Search, Count and Facets;
- `All` (every document, with a term, under Not, and its 15/16 node budget) and `MatchAnyField` (keyword, author and title terms, its standard analysis against stemmed and plain fields);
- an analyzer edit recreating the index while a boost edit does not;
- the fixture refusing a store with no `c/index/`.

## What T6 should switch to at the fold

1. **Analyzer.** Declare HEAD's `book_en_nostop` (`internal/search/bleve_index.go` `bookIndexMapping`) in the Spec and name it on every free-text field (title, author, narrator, series, publisher, description, file_path):

   ```go
   Analyzers: []index.Analyzer{{
       Name:         "book_en_nostop",
       CharFilters:  []index.CharFilter{index.CharUnderscoreToSpace},
       Tokenizer:    index.TokenizeUnicodeWords,
       TokenFilters: []index.TokenFilter{index.FilterEnglishPossessive, index.FilterLowercase, index.FilterPorterStem},
   }},
   ```

   There is no fallback to the stock English analyzer, unlike HEAD's `bookIndexMapping`. Core refuses a component it cannot build at Open (`ErrInvalidSpec`) rather than indexing with a default.
2. **Boosts.** T6 translates HEAD's bleve tree, which already carries every weight (the free-text fan-out's and the user's `^n`), so the parity fold is: **declare no `Field.Boost`**, and emit `index.Boost(<node>, w)` for every node HEAD weighted. An unweighted fielded `title:x` then stays at 1, as in HEAD. That replaces T6's per-term escaped query strings for boosted clauses. Declaring HEAD's `textFieldBoosts` on the fields instead (title 3, author 2, series 1.5, narrator 1.2, publisher 1, description 0.5, file_path 0.5) is the long-term shape once books builds free text from the Spec rather than from HEAD's tree. It also weights a fielded `title:x` at 3, which HEAD does not (ruling 2). Either way, free text becomes an `index.Or` of one `index.Match(field, text)` per boosted field plus `index.MatchAnyField(text)`, HEAD's unweighted unfielded child. That replaces T6's current per-term escaped query strings for boosted clauses and its OR-over-every-text-field fan-out for an unfielded term. An unfielded term inside a `QueryString` also reaches `_all`, with the same standard analysis (pinned).
3. **Listing with no term.** Replace `MatchAllQuery()`'s `Not(Term(format, <absent term>))` with `index.All()`. A Not over a term costs the same 64 nodes but reads as a trick; `All` says what it means.
4. **Wildcards and prefixes.** Compile `*foo` and `*foo*` to `index.Wildcard(field, pattern)` (T6 now answers them `ErrNotExpressible`; core's answer to "does a leading wildcard stay refused" is no, it is allowed and bounded) and `foo*` to `index.Prefix`. Drop `patternTerm` for these: core lowercases on folding fields. Keep `underscorePatternQuery`'s split as books code (an `index.And` of Match, Prefix and Wildcard parts), because no char filter runs on pattern text. Fuzzy (`foo~`) and quoted phrases have no AST node. Route them through `QueryString` (`field:foo~`, `field:"..."`). A phrase gets the field's analyzer (pinned). A fuzzy term is not analysed, so books still lowercases it first, as HEAD's two `NewFuzzyQuery(patternTerm(...))` call sites do.
5. **Errors.** Map `index.ErrTooBroad` to the same 400-class "narrow your search" answer the handler gives a bad query. It is not a failure, and it must not trigger the substring fallback that `Degraded` does.
6. **Tests.** Delete `searchindextest/opener.go` (the bleve stand-in). `RunBooksIndexRebuiltAtCutover`, `TestBooksIndexRebuiltAtCutover` and T6's suites run on the real engine with these calls:
   - `st := coretest.NewStore(t, <books owner spec with Derived{books, memdb, hnsw, chromem}>, coretest.IndexOwner())`
   - `x := coretest.NewIndexEngine(t, st, coretest.IndexEngineOptions{})`
   - `x.Opener("books").Open(ctx, spec)`, then `x.Rebuild(ctx, "books", "books")` and `x.Reconcile(ctx)`
   - `x.PendingRebuilds()` to see what the boot would queue, and `x.Path("books", "books")` for `<db parent>/index/books/books.bleve`

   To simulate a restart: `x.Close()`, then `st.Reboot(...)`, then a new engine with `Dir: x.Dir()`.

## Rulings needed (none blocks this PR; it is dark)

1. **Keyword parity versus true keywords.** HEAD's "keyword" fields (`tags`, `format`, `genre`, `language`, `library_state`, `isbn10`, `isbn13`, `asin`, `_type`) are not keywords. They use the **standard** analyzer: word tokens, lowercased, English stop words dropped (`bleve_index.go` `keyword()` and its comment; `tag_terms.go`).
   - Parity: declare them `Kind: index.Keyword, Analyzer: index.AnalyzerStandard`. Core accepts this.
   - Long-term: `index.AnalyzerKeywordLowercase`. That changes answers. `genre:fantasy` would stop matching "Epic Fantasy", and a multi-word tag becomes one term. It is already tracked in `todo.d` (the `tag_terms.go` note).
   - **Recommendation:** parity at the flip, then the switch as its own change with its own `MappingVersion` bump.
2. **Where the boost lives at the fold.** Core now has both: `Field.Boost` (a field's default weight on Match) and `index.Boost` (a node's weight, which replaces the field's on a Match). A declared `Field.Boost` also weights a fielded `title:x OR author:y` at 3:2, which HEAD does not (HEAD weights only the free-text fan-out and the user's `^n`).
   - **Recommendation:** T6 declares no `Field.Boost` at the fold and emits `index.Boost` from HEAD's tree, so scores match HEAD exactly. Moving `textFieldBoosts` onto the fields is a later, deliberate ranking change.
3. **The term-searcher cap.** HEAD runs with no cap: `internal/search` never sets `search.MaxTermSearchersKey`. Core's default is 1024 per search. A prefix or wildcard that matches more than 1024 distinct terms across the fields it touches answers today and is `ErrTooBroad` after the flip. A short `*a*` over description or file_path is the likely case.
   - **Recommendation:** F7-09 exposes `Options.MaxTermSearchers` as a config key (the F7-07 seam already offers this) and measures the largest expansion on a production-sized index before choosing the default. T6 handles `ErrTooBroad` as in step 4.
4. **Term and Prefix now fold case.** Before this PR, `index.Term` and `index.Prefix` on a `standard` Text field were case-sensitive, so `Term("title", "Hush")` matched nothing. They now lowercase on folding fields. Nothing calls core's index today, so no answer changes in production. This is recorded in case a ruling wants the old raw semantics back.

## Not done here

- No phrase or fuzzy node. Each is reachable through `QueryString` today, and each is listed above where T6 would want it.
- `_all` is bleve's default composition (every text field mapping has `IncludeInAll`), not a per-field declaration: a Spec cannot keep a field out of `MatchAnyField`. A `Field.ExcludeFromAll` would be the surface if one is ever needed.
- No `Analyzer` components beyond what books needs and the tests pin: no stop filter, ASCII folding, n-grams or whitespace tokenizer. Each is one constant, one `Validate` case and one engine translation.
- `indextest.Fixture` still hard-codes owner `gotham`. `coretest.NewIndexEngine` is the fixture with caller-chosen owners. `indextest` stays the in-module store fixture for bleveindex's own in-package tests.
