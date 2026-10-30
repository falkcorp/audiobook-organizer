<!-- file: docs/decisions/2026-10-30-franchise-matchall-prefilter.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4ce859d5-c995-47bc-ba75-fafacd62910d -->
<!-- last-edited: 2026-10-30 -->

# franchise.MatchAll prefilter

## Decision log

- Chose an explicit `anchors []string` field on each rule (and package-level
  anchor lists for the core, UNIT, Counter-Measures and download rules), checked
  against the lowercased text before a rule's regexp runs. Rejected: deriving
  literals from the pattern source at init (regexp/syntax can yield required
  literals, but through (?i) folding and the `[\s._-]*` gaps they come out as
  case-folded fragments too short to filter on, and the derivation is harder to
  read than a list); a single Aho-Corasick pass (a new dependency for a
  30-rule table).
- Anchors cannot drift silently: `TestAnchorsCoverEveryRule` needs a sample
  per rule that the rule matches and that contains an anchor, and every anchor
  in some sample; `TestMatchAllMatchesOracle` compares the filtered result to
  the unfiltered one on the corpus, the samples and non-ASCII edge spellings.
- The filter applies to pure-ASCII text only. A (?i) regexp folds the long s
  and the Kelvin sign onto ASCII letters, so a lowercased literal check could
  miss them; non-ASCII text takes the full regexp path.
- The unfiltered matcher lives in the test file as `oracleMatchAll` and is
  kept, not deleted: it is the reference a new rule's anchors are checked
  against.
- Patterns were already compiled once at package init (no per-call
  MustCompile or string building), so nothing changed there.
- `Detect` ran `MatchAll` twice on a title that only a weak rule names
  (once inside `MatchTitle`, once for the weak signal). `matchTitleHits`
  hands the first result to `Detect`. Rejected: a per-`Detect` memo map
  across fields (author and narrator are often the same string, but the map
  costs about what a prefiltered `MatchAll` does).
