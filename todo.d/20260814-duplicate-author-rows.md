## 56 duplicate-name author groups, and most of them are not authors

Found 2026-08-14 while checking whether the stranded-ampersand repair had left
duplicates behind. It had not — all 16 repaired names resolve to exactly one row
— but enumerating all 9,320 authors to prove that surfaced a separate problem.

**49 exact-duplicate name groups, 56 once case and whitespace are normalized.**

They fall into three kinds, and they want different fixes:

### 1. Book titles stored as authors

| name | rows |
|---|---|
| `Cthulhu Armageddon (Unabridged)` | **25** |
| `1 Ο Χάρι Πότερ και η Φιλοσοφική Λίθος` | 4 |
| `05_Rise of the Corinari` | 3 |
| `Sorcerer Ascendant` | 3 |
| `"Mind's Eye"` | 3 |

### 2. Disc labels stored as authors

`CD 13` ×10, `CD 06` ×10, `CD 05` ×7, `CD 15` ×5, `CD 18` ×4.

Both of these are the same disease as the `& Name` rows — a metadata parse
writing a non-author string into the author field — just a different input
shape. `-Dickens Short Stories` ×3 is the leading-delimiter variant, matching
the `- 3` / `- Legion` junk seen in the ABS series listing the same day.

### 3. Real authors, genuinely duplicated

| name | rows (id, book_count) |
|---|---|
| `Karen Joy Fowler` | 44479(1) 44480(0) 44481(1) 44482(1) 44483(1) 44484(3) |
| `Valery Starsky` | 46007(1) 46008(1) 46009(1) 46010(27) |
| `Raymond L. Weil` | 40775(39) 42117(27) **45616(0) spelled `Raymond  L.  Weil`** |
| `Time Pebbles` | 43574(0) 43575(0) 43576(29) |

`Raymond L. Weil` is the instructive one: two legitimate rows PLUS a third whose
only difference is doubled internal whitespace. Author matching is not
normalizing whitespace, so the dedupe that should have caught it never fires.

- [ ] Normalize whitespace (and probably case) in author lookup/creation, so a
      `Raymond  L.  Weil` can never be minted alongside `Raymond L. Weil`.
      ⚠️ Check `util.NormalizeAuthor` first — it is already used for the series
      name index (`pebble_store_series.go`), so the helper may exist and simply
      not be applied on the author path.
- [ ] Merge the type-3 real-author duplicates. The existing
      `maintenance.author-*` ops already know how to relink via the join slice —
      see `author_conjunction_repair.go`'s `mergeAuthorInto`, which handles the
      BookAuthor rewrite and the AuthorID hydration correctly.
- [ ] Decide what to DO with types 1 and 2 rather than merging them. Merging 25
      `Cthulhu Armageddon (Unabridged)` rows into one still leaves a book title
      masquerading as an author. These need the books re-parsed, or the rows
      retired and the books re-attributed — a different operation from dedupe.
- [ ] 🚨 Do NOT write a single op that treats all three kinds the same. Type 3
      wants a merge; types 1 and 2 want the author link removed entirely. An op
      that merges everything would consolidate the junk and make it look
      intentional — the laundering failure mode recorded in
      `feedback_stripping_without_corroboration_is_laundering`.

**Counts to re-measure before acting** — these are from the 2026-08-14 07:50
snapshot of a 9,320-row author table, taken via `/api/v1/authors` paged by
`limit`/`offset` (note: `page` is not a parameter this endpoint accepts).
