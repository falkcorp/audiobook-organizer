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
  `The Lord of the Rings` — and `parseFilenameForAuthor` then filed the **title as
  the author**. (An earlier draft of this note named `splitAuthorTitle` as the
  consumer. No such function exists anywhere in the tree; the real one is
  `parseFilenameForAuthor`, and naming the wrong consumer is what let a
  regression in it survive three review rounds.)
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

#### A sixth and seventh copy, and a regression the first fix introduced

Review of the fix above found that it shipped its own regression, for a reason
worth recording alongside the first one. The claim was that
`looksLikeAuthorName` had been "composed rather than replaced" — kept intact by
adding the shared predicate to the one rule it carried that the predicate lacks.
It carried **two** rules, and only one was preserved. The lost rule was "the last
word must start with an uppercase letter", and because the shared predicate
deliberately *permits* interior lowercase name particles, every particle of three
or more characters — `van`, `von`, `del`, `della`, `dos` — began qualifying as a
**surname**. Through the split scorer that does not merely add a candidate, it
makes a wrong answer beat the right one:

```
"Ludwig van Beethoven Wolfgang Amadeus Mozart"
   before  ["Ludwig van Beethoven"  "Wolfgang Amadeus Mozart"]
   after   ["Ludwig van"  "Beethoven Wolfgang"  "Amadeus Mozart"]
```

Fixing that exposed the same bug one character down: `Le` is a *capitalized*
particle, so a lowercase test does not catch it, and `Ursula Le Guin` split as
`["Ursula Le", "Guin …"]`. Both tests are needed, and the particle list is now
shared rather than copied a second time.

Four further things came out of the same review:

- **The slash branch was never gated**, and it is the first one tried. Its only
  check was that a part be longer than two characters — it did not even require a
  space. It was minting `Book 3`, `the quick brown`, `Ann Petry (DBY)` and
  `Unabridged` as authors. It was missed because the branches were found by
  reading rather than by running: the test's separator list contained no `/`, so
  nothing ever reached it.
- **The scanner and metadata copies called different predicates** at the same
  logical place — a divergence that predates this work and survived it, because
  the refactor faithfully preserved each call site's own predicate. They now
  agree. Recorded honestly at the call site: this does **not** restore the old
  behaviour and nothing can. The old code also rejected `Partners In Crime` and
  `Part-Time Job`, by the very same accident that rejected Booker T. Washington
  and Partha Chatterjee. Those strings are person-*shaped*; keeping the accident
  means keeping the bug.
- **Non-breaking spaces** were never normalized, because Go's `\s` is ASCII-only —
  so `John Smith` was being stored as an author that can never match
  `John Smith` in any index. Fixed by normalizing the space rather than refusing
  the name.
- **The "is this an initial?" rule was a byte count standing in for the question.**
  As bytes it was meaningless for CJK; rewritten as a character count it became
  actively wrong and rejected 村上 春樹 — a two-character Japanese surname — from
  the very change made to stop dropping Japanese authors. It now asks what it
  means, and as a side effect stops rejecting real two-letter surnames (Ng, Wu,
  Li, Ho) in any script.

Re-measured over 51,744 composites on a corpus built to include what the previous
one could not see — non-ASCII names, trailing particles, slashes, non-breaking
spaces and separator-free concatenations — the splitter now mints **392** distinct
author strings where the old code minted **637**. Of the 224 composites it stops
splitting, every one used the word "with", where the old code was producing
authors like `Volker Kutscher with Bob`; and it now correctly splits 440 strings
the old code could not split at all, because either name began with a non-ASCII
letter.

#### The two rules turned out to be entangled

A second review round found that the fix above had traded one bug for a quieter
version of itself. Relaxing the "is this an abbreviation?" rule from three
characters to two — done so that 村上 春樹, a two-character Japanese surname, would
stop being rejected — removed the accidental shield that had been covering the
particle list's incompleteness. `St`, `Zu` and `Ph` are not in that list, and at
three characters they could never reach it; at two they began qualifying as
surnames, and `Jane St Clair` split as `Jane St` / `Clair …`.

Length cannot settle this, because two characters is also exactly what makes
`Wang Li` work. The discriminator is **script**: a two-character surname is
ordinary in Japanese, Chinese and Korean, and is almost always an abbreviation in
Latin, Cyrillic and Greek. The threshold is now conditional on script. The
accepted cost is that romanized two-letter surnames written in Latin letters —
`Wang Li`, `Ng`, `Wu`, `Ho` — are refused, so those composites are left unsplit.
That is a *missed* split rather than a *wrong* one, and this is the same rule the
rest of the file follows: refusing leaves a composite visibly wrong for repair,
while a confident wrong answer leaves nothing to notice.

Two smaller things from the same round. A guard written as "reject a lowercase
first letter **or** a known particle" was carrying a comment claiming both halves
were needed; the first half was **dead code** — by the time it runs, a lowercase
word has already been rejected unless it is a particle, which the second half
catches. That is the third time in this change that a comment asserted a control
which did not exist. And the review recommended reverting the stricter check on
the `Author - Title` directory pattern, on the grounds that no junk name arrives
that way. Measured, that turned out not to hold: `Discworld - Mort`,
`Bookends - Volume One` and `Chapterhouse - Dune` all do. Reverting would have
recovered six real single-name authors and introduced four junk ones the old code
never produced, so the stricter check stays — a book left without an author is
re-examined by the filename parser, and one given the wrong author is not.

#### The script rule was a deny-list, so every script nobody thought of failed open

A third review round found that the script-conditional threshold above was written
as the wrong kind of list. It named the scripts that *are* abbreviation-prone —
Latin, Cyrillic, Greek — and let everything else take the permissive branch. That
is fail-open: a script is admitted precisely because nobody enumerated it. Arabic,
Hebrew, Armenian and Devanagari were all falling through, and the two-character
words they fall through on are exactly the ones that must not be surnames:

```
"محمد بن سلمان أحمد"   ->  ["محمد بن"  "سلمان أحمد"]     (بن = "son of")
"דוד בן גוריון משה"    ->  ["דוד בן"  "גוריון משה"]      (David Ben-Gurion)
```

The list is now inverted to name the scripts where a short surname is *ordinary* —
Han, Hiragana, Katakana, Hangul — and everything else takes the strict branch. An
unenumerated script now fails closed, which is a missed split rather than a wrong
name, consistent with the rest of this file. Both strings above now return no split.

Three further things from the same round:

- **The test guard for this rule had the byte-versus-rune bug the production code
  had just been cured of.** The differential test that was supposed to catch a
  short surname escaping counted `len(lastWord)` — bytes. `بن` is two characters
  and four bytes, so it passed a `>= 3` check and the guard waved it through; the
  mutant that reverts the deny-list **survived**. The guard now counts runes and
  names the dangerous class directly rather than inferring it from a length.
- **A must-admit assertion of my own caught the floor was still too high.** `田中 翼`
  — a single-character Japanese given name — failed a test written to prove
  Japanese names are admitted. The length floor is now removed entirely for
  syllabic and logographic scripts, on the grounds that "a one-character word is a
  bare initial" is a Latin orthographic convention that does not exist in Han or
  Hangul.
- **The translator branch of `extractAuthorFromDirectory` was ungated** in both the
  scanner and metadata copies — the same defect as the slash branch, in the same
  shape, found the same way.

One known limit is recorded rather than fixed. Georgian is dropped by the shared
predicate: Mkhedruli letters are Unicode lowercase, so `გიორგი ბაქრაძე` is not a
name. This is not a regression — the ASCII test this package replaced dropped it
too — and the obvious remedy does not work: Go does map Mkhedruli to Mtavruli
(`unicode.ToUpper('გ') == 'Გ'`), so accepting runes with no uppercase mapping
rejects Georgian exactly as today. Measured, and filed with the disproof rather
than left as a plausible-sounding suggestion.

#### Measured at the second consumer too, not just the splitter

The differential above exercises `SplitCompositeAuthorName`. It does not touch
`extractAuthorFromDirectory`, which this change also altered in both the scanner
and metadata copies — and where a refused branch **falls through** to the next
branch rather than returning, which is the same amplifier shape that made the
first version of this change unshippable. Reasoning that the fall-through is
harmless was not enough, so it was measured the same way: 6,699 directory shapes,
both copies, old tree and new tree, exact comparison.

- **No input gets a different author.** 0 changed answers in either package: a
  gated branch's refusal never hands the decision to a branch that answers with
  some *other* name.
- **The two copies now agree exactly.** They disagreed on **128** of the 6,699
  inputs before — the same folder yielding `Ludwig van Beethoven` from one path
  and nothing from the other — and on **0** after.
- Scanner: 88 folders newly yield an author (all of them the near-miss-prefix
  class this change exists to fix — `Booker …`, `Volker …`, `Partha …` — the
  particle class, or non-ASCII names); 36 stop yielding one, being 14 bare
  single-word folder names, 11 `Pratchett 036` placeholder variants and 11
  `the quick brown` title fragments. Metadata: 180 newly yield an author, 0 stop.

Nothing in either list is unaccounted for.

The mutation matrix was re-run at the final commit rather than trusted from the
round it was first measured in, since a later fix can defuse an earlier test —
and one of these mutants had already survived a guard written to kill it once.
Ten mutants, each verified to match exactly one anchor, to actually change the
file, and to still compile before the tests were run: **10 killed, 0 survived.**

#### A stricter predicate made one caller WORSE, because it was choosing a side

A fourth review round found the most serious defect in this change, and it is a
different shape from the three before it. Those were all "a closed set fails open
and admits junk." This one is a gate that fails **closed** and, because the caller
uses the answer to choose an **orientation** rather than to accept or reject, a
false does not cause a miss — it causes an **inversion**.

`parseFilenameForAuthor` splits `"X - Y"` and asks which side is the author. The
old per-package predicate ended in a fallback that approved almost anything
capitalized, so a decorated author string on the right was recognised as a name.
The shared predicate is strict, and it answers **false** for
`"Neil Gaiman (Unabridged)"` — because of the trailing parenthesis, not because
Neil Gaiman is not a person. The caller read that false as "so the right side must
be the title", took the `"Author - Title"` branch, and stored:

```
"Good Omens - Neil Gaiman (Unabridged)"        Author = "Good Omens"
"Good Omens - Neil Gaiman and Terry Pratchett" Author = "Good Omens"
"The Hobbit - Neil Gaiman [Unabridged]"        Author = "The Hobbit"
```

The title, filed as the author. Nothing downstream can catch it: the minted string
passed the person check on the left by construction, so it is person-shaped, it is
not the placeholder, and re-running the predicate on it returns true. `(Unabridged)`
is native to this library — `internal/titleutil` exists to strip it — so this is a
common shape, not an exotic one. And it directly contradicts the rule this change
argues for sixty lines earlier, that a wrong author is strictly worse than an absent
one.

The cause is that one bit was being asked a two-way question. `LooksLikePersonName`
answers "is this one bare human name?", and "not a person" and "a person carrying an
edition marker" both come back false. Callers deciding an orientation now ask
`LooksLikeAuthorCredit` instead — one name, a name with an edition marker, or several
names joined by a conjunction — while callers deciding whether to *accept* a single
string keep the strict predicate, because a credit list is not a person.

Fixing it by making the branch refuse was rejected: that branch is right far more
often than it is wrong, and refusing would have thrown away every correct
`"Author - Title"` reading with it.

Measured at the consumer against a corpus where the author side is known, 1,792
filenames, scanner:

| | correct | wrong | empty |
|---|---|---|---|
| before this change (`origin/main`) | 512 | 1,152 | 128 |
| with the strict predicate (the bug) | 448 | 438 | 906 |
| with `LooksLikeAuthorCredit` | **1,120** | 384 | 288 |

**Zero** filenames go from a correct author to a wrong one, 552 go from wrong to
correct, and 36 go from correct to empty — and empty is the outcome this file
treats as safe, because it routes to AI nomination.

The metadata twin had the same inversion **before** this change as well as after,
because its own older predicate also rejected a decorated name. So it is fixed
rather than repaired: 620 correct before, 1,144 after, at the cost of 101 filenames
in a class that is genuinely ambiguous — a two-to-four-word capitalized title with
no leading article is not distinguishable from a person's name by structure, and
whichever side you prefer, some corpus makes you wrong. Preferring the right-hand
side is what both copies' own comments already said they did; the metadata copy was
only doing otherwise by accident, because decoration made its check fail.

Four comment claims from the same review are corrected rather than left standing,
the first being the one that mattered: two places named `splitAuthorTitle` as the
function that filed titles as authors. **No such function exists in the tree.** The
real consumer is `parseFilenameForAuthor` — the one that then carried this
regression through three review rounds without being named once.
