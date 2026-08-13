### 6,157 books are invisible in the web UI — `vg-` singleton groups elect no primary

Confirmed against production 2026-08-13. Reported as "search is broken"; search is fine.

Books whose `version_group_id` carries a **`vg-` prefix** sit one-per-group with
`is_primary_version=false` and **no primary elected anywhere in the group**. The web
Library page filters to primary versions by default, so a singleton group with no
primary can never satisfy it and the book cannot be seen — in search, in browsing, or
in any count. The AudiobookShelf app applies no such filter, which is why the same book
is visible there and was reported as a search discrepancy.

| | |
|---|---|
| total books | 63,870 |
| `is_primary_version=true` | 40,839 |
| `is_primary_version=false` | 23,031 |
| **`vg-` singletons with no primary** | **6,157 (9.6% of the library)** |

Counted exhaustively — all 24 pages of the non-primary set, not sampled. Every affected
book was created between **2026-04-04 and 2026-08-11**; zero `vg-` rows appear in the
older 11,031 non-primary books, which bounds the writer to that window.

Healthy groups use an unprefixed id and elect exactly one primary (verified on a real
two-member group). The `vg-` prefix is the tell — find what mints it.

Work:

1. Find the writer that mints a `vg-`-prefixed `version_group_id` without electing a
   primary. That is the defect. Everything else is cleanup.
2. Backfill the existing 6,157. The sole member of a single-member group should be its
   primary — but **verify the group is genuinely a singleton before flipping the flag**,
   or a real duplicate pair ends up with two primaries and the dedup UI inherits a new
   class of bug.
3. Add a data invariant: **no version group may have zero primaries.** This is the shape
   of defect an invariant suite catches and no unit test will — see the existing
   data-loss invariant suite (#1930–#1942) for the pattern.
4. Check whether anything else keys off the `vg-` prefix before renaming or normalising
   it.

Related but independent, found in the same investigation and recorded in
`docs/handoffs/2026-08-13-web-search-returns-unrelated-books.md`: search silently drops
English stopwords (`all jobs` searches only `jobs`, returning 283 results), and quoted
phrases never become a `MatchPhraseQuery` because the parser leaves the quote characters
attached to the terms.
