### Fixed

- Substring search (`SearchBooks`, the filtered variants and the scoped
  fallback) now normalizes the query the same way the stored author name is
  normalized, so an author is found when the query carries a tab, a non-breaking
  space or other non-ASCII whitespace. The query as typed is still tried first,
  so matches only widen. See
  `docs/decisions/2026-10-30-searchbooks-author-normalization.md`.
