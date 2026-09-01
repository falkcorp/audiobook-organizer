### Changed

#### Five diverged copies of "does this string look like a person's name?" folded into one, fixing a Unicode bug and a title-as-author bug on the way

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

#### Two more copies, and a safety claim that was measured at the wrong level

Review of the above found that the first version of this change was **not** safe to
ship, for a reason worth recording. The claim made for it was that the unified
predicate is monotonically more restrictive — it can only ever go `true → false`,
so it can never mint an author the deployed code would not have. That premise was
true. The conclusion did not follow, because it was measured on the **predicate**
and asserted about the **consumer**.

`SplitCompositeAuthorName`'s comma branch does not return when a part fails the
shape check — it `break`s, and falls through to weaker branches. So a newly-*false*
predicate does not stop a split, it changes **which branch wins**. Measured against
the real splitter, the first version minted **886 distinct author strings** that the
deployed code never minted, and sent **33,580 of 195,245** realistic composites from
a correct split to no split at all.

Two compounding causes, both now fixed:

- **The trigger.** `IsValidAuthor` matched the structural words (`book`, `chapter`,
  `part`, `vol`, `volume`, `disc`) with `strings.HasPrefix` and no word boundary, so
  it rejected every real author whose name merely begins with those letters — Booker
  T. Washington, Volker Kutscher, Volney Beckner, Volodymyr Zelensky, Voltaire,
  Partha Chatterjee, Partridge. Structural words are now matched as whole first
  words, with trailing punctuation and digits stripped so `Vol. 2`, `Book3` and
  `Disc 1` still match. Plurals are listed explicitly, so widening the match does not
  start admitting `Parts Unknown`, which the prefix test had caught by accident.
- **The amplifier.** The comma branch's own comment says every part must be
  person-shaped "or the whole split is refused — refusing leaves the composite
  VISIBLY wrong for repair rather than laundering a title fragment into a name." The
  bracket and semicolon branches it fell through to asked only whether a part was
  longer than two characters and contained a space — which is exactly the test that
  was removed from the comma branch for minting `and the Farm Boy (DBY)`. The comment
  described a control that did not exist, and those branches were still minting
  `Ann Petry (DBY), Ida Wells`, `the quick brown, Ida Wells` and
  `So Long, and Thanks for All the Fish` as author names. All branches now gate on
  the same shared predicate.

A **fifth** copy then turned up, caught by the new consumer test rather than by
reading: `looksLikeAuthorName`, used by the concatenated-name splitter, still carried
the same ASCII byte test this whole change exists to delete. It has been composed
with the shared predicate rather than replaced, because it also enforces one rule the
shared predicate does not — a surname must not be a bare initial — which is what
keeps `R.A. Mejia Charles Dean` splitting at the right boundary.

Re-measured at the consumer over 258,845 composites: **0** author strings are minted
that the deployed code did not mint, and the 13 strings no longer minted are all
structural labels (`Book 3`, `Chapter 1`, `Disc 1`, `Vol. 2`, `Parts Unknown`),
never a person.

Turning the `break` into a `return` was the other candidate fix and was rejected on
measurement, not taste: it also destroys legitimate last-first composites such as
`Smith, John; Doe, Jane`.
