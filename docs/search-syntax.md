<!-- file: docs/search-syntax.md -->
<!-- version: 1.5.0 -->
<!-- guid: 9e7d007e-cca4-43e3-b1d5-777054e59d29 -->
<!-- last-edited: 2026-10-25 -->

# Search syntax

One syntax is used everywhere a search box takes a query: the library search,
dynamic collections and smart playlists. The in-app help (the question-mark
button in the search box) lists the same examples. The server is the authority:
the web page only pre-checks what you type so a mistake shows up before you
press Enter.

## Basics

- Plain words search the text of a book.
- `field:value` filters on one field, for example `author:sanderson` or
  `year:>2020`. Negate any filter with `-` or `NOT`, for example
  `-read_status:finished`.
- `*` is a wildcard in a text value (`title:a*`); `/.../` is a regex (RE2,
  case-insensitive); quotes mean literal text (`title:"a*"`).
- Numbers take `>`, `>=`, `<`, `<=` and ranges: `year:[2015 TO 2020]`.

## Dates

These fields take a date:

| Field | Meaning | Scope |
|---|---|---|
| `last_played` | When a listener last played the book | Per user |
| `date_added` (alias `added`) | When the book entered the library | Library-wide |
| `date_modified` (alias `modified`) | When the book record last changed | Library-wide |

`itunes_last_played` can be sorted on but not searched.

A date is one of:

- `YYYY-MM-DD`, for example `2026-03-31`. With no operator a bare day matches
  that whole day (UTC).
- A full timestamp such as `2026-03-31T08:00:00Z`.
- `now`.
- A rolling offset from now: a sign, a whole number and a unit.

| Unit | Meaning |
|---|---|
| `d` | days |
| `w` | weeks (7 days) |
| `m` | calendar months |
| `y` | calendar years |

Months and years keep the day of the month, and a day that does not exist in the
target month rolls forward into the next one, so `-1m` on 31 March is 3 March
(there is no 31 February, so it becomes 3 March), not 28 February. Use days (`-30d`)
when you need an exact length.

A list of values in parentheses, `field:(a|b)`, matches when ANY of them does, and
`-field:(a|b)` when NONE does. This holds for the per-user fields as well as the
indexed ones, so `read_status:(finished|in_progress)` finds books in either state.
Each value in a list is a value on its own, not a comparison: `last_played:(2026-03-31|-2d)`
works, `last_played:(>-7d|...)` does not.

A date with no time of day names a whole UTC day, and the operator says what you mean
by it:

| Query | Finds |
|---|---|
| `date_added:2026-03-31` | Added at any time on 31 March |
| `date_added:>2026-03-31` | Added after the end of 31 March |
| `date_added:>=2026-03-31` | Added from the start of 31 March |
| `date_added:<2026-03-31` | Added before 31 March began |
| `date_added:<=2026-03-31` | Added up to the end of 31 March |
| `date_added:2026-03-01..2026-03-31` | Added on any of those days, including all of 31 March |

A full timestamp or an offset is an exact moment instead: `<=` includes it and `>`
starts just after it. A book added at 15:00 UTC on 31 March matches
`date_added:<=2026-03-31` and `date_added:2026-03-01..2026-03-31`, in the library
search and in a dynamic collection alike.

The sign is required: `-30d` is 30 days ago and `+2w` is two weeks from now.
`30d` on its own is refused, with a message showing `-30d`, rather than
guessed at. The number can be at most 100000.

Examples:

| Query | Finds |
|---|---|
| `last_played:>-30d` | Played in the last 30 days |
| `date_added:>-1y` | Added to the library within the last year |
| `last_played:-30d..now` | The same window written as a range (inclusive) |
| `date_added:2026-01-01..2026-03-31` | Added in the first quarter of 2026 |
| `-last_played:>-6m` | Not played in the last six months |
| `last_played:*` | Has been played at all |

Offsets are resolved when the search runs, not when you type it. A saved
search, a dynamic collection or a smart playlist that says `last_played:>-30d`
therefore keeps sliding: it means the last 30 days as of each evaluation. All
rows in one evaluation share the same "now". A dynamic collection is evaluated
when someone opens it, and the Audiobookshelf surface serves its stored members, so
a scheduled task refreshes every dynamic collection with a relative date in its query
hourly; its members are at most an hour behind the clock. A collection that also uses
a per-user field (such as `last_played:>-30d`) is evaluated for each viewer on every
read instead, so it is always current and nothing is stored.

### Per-user fields and dynamic collections

`last_played`, `read_status` and `progress_pct` describe one listener. A smart
playlist evaluates them as the playlist's user. A dynamic collection is shared by the
whole server but is still evaluated for whoever is looking: "unfinished" means
unfinished for you. Two users see different members of the same collection, the user
who created it never decides what anyone else sees, and nobody can work out another
user's listening from a collection's members. That includes administrators: they get
their own state too (reading another user's listening state needs the `users.manage`
permission and its own tooling). A user with no listening history for a book counts as
"unstarted" for that book. With authentication off there is one local user and it is
the viewer. Anything that evaluates a collection with no viewer, such as a background
job, matches nothing for these fields; it is never answered with the creator's state.

Because the answer is always the viewer's own, anyone allowed to manage collections may
write these fields into a query, and their members are not stored. A query without them
is unchanged: it is the same for everyone and is stored.

A query that cannot be parsed or translated, such as a malformed date on a per-user
field (for example `last_played:>30d`, which needs a sign), is refused with a 400 when
the collection is created or its query is changed, with the same message the library
search gives, instead of being stored as a collection that matches nothing. A closed
search index does not stop a good query from being saved.

A stored collection whose query has since stopped parsing is treated as per-viewer:
it shows no members to anyone, with a message saying its query can no longer be
evaluated, and never the list it may have held. Fix its query to bring it back.

One known limit: an `OR` between a per-user term and an indexed term
(`title:Alpha OR read_status:finished`) is evaluated as an `AND`, because the per-user
part is applied afterwards to the books the index returned. Write such a search as two
searches, or avoid mixing them, until that is fixed.
