### Fixed

#### Corrected a book count that was ~7.5× too high, and the decision it distorted

A number had been circulating through this codebase for months — written
variously as 392,962, 366,922, 366,916, "392K-book", "393K", "367K-book" — as
the size of the production library. It was never a book count. It was the
number of PebbleDB *keys* under the `book:` prefix, and that prefix is shared
with roughly seven secondary-index families (`book:path:`, `book:hash:`,
`book:versiongroup:`, `book:work:` and others), so it ran about 7.5 keys per
actual row.

The real figure is **~48,900 books**, from the organizer's own complete paging
enumeration, and independently consistent with system-status readings of 46,221
and 54,734.

**Why this was worth chasing down rather than quietly editing.** One decision
had already been made on the inflated number. The memory cost of the nine
optional sort indexes was measured properly — 3,750 bytes per book, benchmarked
at 100,000 books — and that measurement was and remains correct. But it was
then multiplied by 366,916 instead of ~48,900:

| | as recorded | corrected |
|---|---|---|
| all nine sort keys | +1,312 MB | **~+175 MB** |
| per sort key | ~146 MB | **~19 MB** |

"+1.3 GB on a server already holding 1.25 GB" reads as prohibitive. "+175 MB"
does not. The indexes shipped switched off on the strength of the first
reading; whether to turn some on is worth revisiting against the second. That
is an owner decision, and it is flagged in the code rather than decided here.

**How it spread, since the pattern is the point.** The error survived three
chances to be caught. A design document noticed that 392K disagreed with the
~44,888 books a person actually sees, correctly flagged the discrepancy, and
then resolved it the wrong way — trusting the bigger number and inventing
"non-primary versions" to account for the gap. A later pass "corrected" 392K to
366,916, propagating the error while looking like a fix. And a backfill audit
concluded the version-group scan covered only 13.3% of the library by dividing
a genuine scan count by the inflated one.

Every affected comment and document is now corrected or annotated in place —
kept and struck through rather than deleted, so nobody re-derives the same
conclusion from the same evidence. The per-row analysis in the May heap audit
is unaffected: it comes from the Go struct layout, not from the population, so
it needs re-multiplying, not re-deriving.

Row counts for the other tables (`book_files`, `works`, `book_authors`,
`series`, `authors`) came from the same counter but sit under different
prefixes with different index families, so they are **not** assumed wrong and
**not** assumed right. Re-verifying them is tracked as `ROWCOUNT-REVERIFY`.
