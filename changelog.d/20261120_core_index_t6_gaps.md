### Added

- Core's full-text index contract can now express the books search that production runs, ahead of the F7-09 flip. A Spec declares its own analyzers from a closed set of core components (`index.Spec.Analyzers`), enough to declare production's stop-word-keeping, stemming, underscore-splitting analyzer. Analyzer edits are part of the mapping fingerprint, and a Spec without analyzers keeps its old one. Also added:
  - a built-in case-insensitive keyword analyzer (`keyword_lowercase`);
  - a per-field query-time weight on Match (`index.Field.Boost`);
  - an explicit `index.Wildcard` query node that allows a leading `*` or `?`. It costs 32 of a query's 1024 nodes, and an expansion past the term-searcher cap fails with the new `index.ErrTooBroad`;
  - `index.All()` (every document) and `index.MatchAnyField(text)` (an unweighted match over every text field, production's unfielded fallback);
  - `coretest.NewIndexEngine`, the real engine as an `index.Opener` for a module's tests under any owner.

  Term, Prefix and Wildcard text is lowercased on fields whose analyzer lowercases. Nothing in the deployed binary uses core's index yet, so no behaviour changes. `seams.d/F7-09-T6-core.md` lists what T6 switches to at the fold, plus four rulings; it covers the seven gaps T6's own seam note (`seams.d/F7-09-T6.md`) lists.
