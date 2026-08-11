### Fixed

- Multi-word searches containing a common word ("of", "the", "a", "in") returned
  **no results**. The query parser treats whitespace as AND, so `shards of
  oblivion` became a conjunction of three match queries — but the English
  analyzer strips stopwords at index time, so `of` exists in no document's term
  dictionary and made the whole conjunction unsatisfiable. Single-word searches
  worked, which made this look like "search breaks when there are spaces". A
  large share of book titles were unfindable by their own name. Stopword-only
  conjuncts are now dropped, but only when a real term survives, so searching for
  `of` alone keeps its previous behaviour instead of returning the whole library.
