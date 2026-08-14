### Fixed

#### Fuzzy search (`~`) is case-insensitive again

`FuzzyQuery` bypasses the field analyser exactly as the wildcard/prefix
queries fixed on 2026-08-13 do, so a capitalised fuzzy term could never come
within edit distance of the lowercased index terms — `HYPERION~` found
nothing while `hyperion~` matched. Both construction sites (fielded and
free-text; the fragment named only one) now route through `patternTerm`.
Each site is pinned by its own mutation-verified subtest.
