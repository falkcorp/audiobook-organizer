<!-- file: docs/search-syntax.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e7d007e-cca4-43e3-b1d5-777054e59d29 -->
<!-- last-edited: 2026-10-18 -->

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
rows in one evaluation share the same "now".

### Per-user fields and dynamic collections

`last_played`, `read_status` and `progress_pct` describe one listener. A smart
playlist evaluates them as the playlist's user. A dynamic collection is shared
by the whole server, so it evaluates them as the collection's owner (the user
who created it; if that user is gone, the first active admin). A user with no
listening history for a book counts as "unstarted" for that book.
