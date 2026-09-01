### Changed

#### Three diverged copies of "does this string look like a person's name?" folded into one, fixing a Unicode bug and a title-as-author bug on the way

`looksLikePersonName` existed in three packages (`internal/scanner`,
`internal/metadata`, `internal/dedup`) and `isValidAuthor` in two. They had drifted
apart, and **no copy was correct** — each one had a bug the others did not:

- **scanner** and **metadata** tested capitalization with the ASCII range check
  `w[0] < 'A' || w[0] > 'Z'`, so a name beginning with a non-ASCII letter was
  rejected outright. `Émile Zola`, `村上 春樹` and `Достоевский Фёдор` were not
  names; `José Saramago` and `Søren Kierkegaard` were, because `J` and `S` are
  ASCII. Neither copy accepted lowercase particles, so `Simone de Beauvoir` failed
  on `de`.
- **scanner** additionally ended in a fallback that returned true for *any* string
  whose first two words start with ASCII capitals, bypassing the 2–4 word limit the
  same function declares. That is why it answered true for `A Game of Thrones` and
  `The Lord of the Rings` — and `splitAuthorTitle` then filed the **title as the
  author**.
- **dedup** handled Unicode and particles correctly but had no structural guard, so
  `Book 3`, `Chapter 1`, `Volume 2`, `Disc 1` and `Pratchett 036` were all names.

The replacement is a new leaf package, `internal/personname`, carrying the **union**
of the three copies' checks rather than any one of them. Capitalization is now
expressed as "the first rune must be a letter and must not be lowercase" — never
"must be uppercase", because `unicode.IsUpper` is false for every caseless script
(CJK, Hebrew, Arabic, Thai), and the `IsLetter` half is what keeps digits and
punctuation from passing as names.

The behaviour change is pinned by `TestDifferentialAgainstAllThreeLegacyCopies`,
which runs all three original implementations — extracted verbatim from git, not
re-typed — beside the unified one over a 29-case corpus and logs every
disagreement. The unified answer differs from at least one legacy copy on **18 of
29** inputs, and the test fails if that count ever reaches zero, since that would
mean the corpus no longer demonstrates why the copies were merged.

One existing scanner test changed its expectation as a result: `five_word_name`
asserted `true` for `Too Many Words Here Name` with the comment "Actually valid -
has proper capitalization". That expectation was derived from the buggy fallback,
not from a decision about names; it is now `false`, with the rationale recorded at
the test.

Books whose filename previously yielded a wrong author (the title) will now yield
no author instead. That is the better failure: an empty author field is refilled by
the AI parse and metadata paths, while a confidently wrong one is not.
