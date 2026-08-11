### Fixed

#### The organizer wrote the literal word "narrator" into real filenames

Books with no narrator were organized to paths like
`.../Jerry Merritt/Time Pebbles/Time Pebbles/Time Pebbles - Jerry Merritt - read by narrator.mp3`.
Measured on production 2026-08-11: of 3,194 books failing organize with
`ErrTargetOccupied`, 2,611 (82%) had computed a path containing the literal
string `read by narrator`.

`internal/organizer/organizer.go` declared `defaultNarrator = "narrator"` and
substituted it whenever `book.Narrator` was blank, so the value never reached
the empty-placeholder handling that every other unset field goes through.

Deleting that default is necessary but not sufficient. The default pattern is
`{title} - {author} - read by {narrator}`, and the existing
`removeEmptySegment` only knows four shapes — ` - {placeholder}`,
`{placeholder} - `, `({placeholder}…)` and `(…{placeholder})`. Blanking the
narrator alone left the connector words behind: `cleanupPattern` trims trailing
` -/` characters but has no idea that "read by" is connective text. Measured
with the default removed and nothing else changed:

```
{title} - {author} - read by {narrator}  ->  "Time Pebbles - Jerry Merritt - read by"
```

Mid-string it was worse than a cosmetic dangle. With the pattern
`{title} - read by {narrator} - {author}`, the ` - {narrator}` rule consumed
the *wrong* dash and glued the connector onto the next field, producing a
filename that credits the author as the narrator:

```
{title} - read by {narrator} - {author}  ->  "Time Pebbles - read by Jerry Merritt"
```

The fix is a new `dropEmptyPatternSegments` pass that runs on the **raw**
pattern, before any substitution, and removes each ` - `-delimited segment
whose placeholders are *all* empty — connector words included. Three rules keep
it from over-deleting:

- A segment containing no placeholders is literal text the user asked for and
  is always kept.
- A segment where at least one placeholder has a value is kept, so
  `{title} - {series} {series_number}` still renders `Title - Series Name`
  for a book with a series but no series number.
- A placeholder that is not a known field at all (a typo) is *not* treated as
  empty, so it survives into the unresolved-placeholder check and a bad
  pattern still errors loudly instead of silently swallowing its segment.

Running before substitution is load-bearing: splitting on ` - ` after values
are in would tear apart a book titled `Foundation - Part 1`. That case is
pinned by a test.

Because a pattern can now legitimately expand to nothing, `generateTargetPath`
falls back to the book's own title (then `Unknown Title`) rather than emitting
a bare `.m4b` dotfile that every narrator-less book would collide on.

The config default is deliberately unchanged — deployed configs already contain
`read by {narrator}`, so fixing the expansion engine is the only layer that
helps them.

**Scope, honestly:** this makes computed filenames correct. It does **not** by
itself clear the 3,194 occupied-path organize failures, which are dominated by
books with duplicated or swapped author+title metadata — roughly 250 distinct
books compute one identical path. Note also that the next organize pass will
*rename* previously-organized narrator-less files, since their computed target
has changed. Known limitation: only ` - `-delimited and parenthesized segments
are dropped, so a custom pattern using another connector (`{title} by
{narrator}`) still leaves a dangling "by".
