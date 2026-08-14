## Author table: copyright text and HTML entities leaked into artist tags

Found while fixing the stranded-`&` author rows (`dedup.NormalizeAuthorName`,
2026-08-14). These are a **different** defect and were deliberately left alone —
the leading-conjunction strip requires trailing whitespace precisely so it does
not mangle them.

- [ ] Author id 46583 is named `&#169` — an HTML entity for `©` with its
      trailing `;` already lost somewhere upstream. 1 book attached.
- [ ] Author id 51870 is named `&#169;2013 by HarperCollinsPublishers` — a whole
      copyright line stored as an author. 0 books attached, so it can likely just
      be deleted.
- [ ] Find where the entity loses its `;`. `SplitCompositeAuthorName`'s semicolon
      branch splits `&#169;2013 by HarperCollinsPublishers` into `["&#169",
      "2013 by HarperCollinsPublishers"]` but then discards the result because
      `&#169` has no space and only one part survives — so the branch returns
      nothing and is *not* the culprit. The truncation happens somewhere else.
- [ ] Decide whether author-name ingest should HTML-unescape at all. If it should,
      `html.UnescapeString` belongs at the same chokepoint, but note it would turn
      `&#169;2013 by HarperCollinsPublishers` into `©2013 by
      HarperCollinsPublishers` — still not an author, so entity decoding alone
      does not fix the real problem, which is copyright text in an artist tag.
- [ ] Consider a `isDirtyAuthorName` rule for names starting with `©`/`&#`/a
      4-digit year, so these are rejected at creation instead of repaired later.

## Author table: book titles are being comma-split into author rows

Also found on 2026-08-14, and deliberately excluded from the leading-conjunction
data repair. Three rows begin with `and ` but are **not** stranded conjunctions
from a credit list — they are fragments of book titles that reached the artist
tag and were then split on the comma:

- [ ] id 46595 `and Thanks for All the Fish` (2 books) — from *So Long, and
      Thanks for All the Fish*
- [ ] id 46989 `and the Farm Boy (DBY)` (5 books)
- [ ] id 47193 `and Make Better Decisions` (16 books)

Stripping the leading `and` from these produces `Thanks for All the Fish`, which
is still not an author — it just stops *looking* broken. The repair op therefore
matches `&` only, and these three are left visibly wrong on purpose.

- [ ] The real defect is that `SplitCompositeAuthorName`'s comma branch has no
      notion of person-vs-title: its only per-part test is "contains a space".
      A title clause passes as readily as a name.
- [ ] Consider requiring a part to look like a personal name (2-4 words, no
      leading lowercase function word, no trailing parenthetical like `(DBY)`)
      before accepting a comma split, or refusing to split when the source
      string also carries title-ish punctuation.
- [ ] Check how many other author rows are title fragments without the `and`
      giveaway — the 57 rows beginning with `-` are the next place to look.

## Author table: misspelling shared by both rows of a duplicate pair

- [ ] `Sylverster McCoy` (2 books) and `& Sylverster McCoy` (1 book) are *both*
      misspelled — the actor is Sylvester McCoy. Merging the `&` row into its twin
      leaves the misspelling intact in the survivor. Worth a targeted rename after
      the conjunction repair lands.
