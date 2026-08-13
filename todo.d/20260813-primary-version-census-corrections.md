### Search / version-group census corrections (2026-08-13)

Measured by a full 63,870-book census against production, correcting the figures in
`docs/handoffs/2026-08-13-web-search-returns-unrelated-books.md` v2.0.0.

- [ ] **765 books, not 6,157, are wrongly hidden by the primary-version filter** (1.20%,
      not 9.6%). Breakdown: 724 sit in a version group where no member is primary; 41
      have no `version_group_id` at all and are still hidden. The other 22,266 unreachable
      books are legitimately collapsed duplicates whose group *does* elect a primary.
- [ ] **Find the writer that creates a `vg-` group without electing a primary.** The lead
      is good: 472 of 7,154 `vg-` groups have no primary versus 7 of 17,635 unprefixed —
      a ~166x enrichment. Note `vg-` groups are NOT mostly singletons (12,877 books across
      7,154 groups; 1,905 singletons), so a repair that assumes singleton-ness is unsafe.
- [ ] **`is_primary_version` in the payload disagrees with the filter for 5,731 books.**
      Books with no `version_group_id` are returned by `is_primary_version=true` while
      their own serialized field says `false`. Nothing is hidden by this, but any client
      reading the field instead of calling the filter will disagree with the server about
      5,731 books. It is why two independent counts of "primary books" differed
      (40,839 vs 35,108).
- [ ] **41 ungrouped books are hidden anyway** and do not fit the rule above. Small
      concrete sample; unexplained.
- [ ] **`version_group_id` is silently ignored as a filter** on `/api/v1/audiobooks` —
      both `?filter=version_group_id:X` and `?version_group_id=X` return the entire
      library (count=63,870) rather than erroring. Same silent-filter family as the bare
      query-parameter rejection in ab04824e. This is what forced a full census instead of
      a targeted group lookup.
