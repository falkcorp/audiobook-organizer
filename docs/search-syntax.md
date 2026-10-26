<!-- file: docs/search-syntax.md -->
<!-- version: 1.7.6 -->
<!-- guid: 9e7d007e-cca4-43e3-b1d5-777054e59d29 -->
<!-- last-edited: 2026-10-26 -->

# Search syntax

One syntax is used everywhere a search box takes a query: the library search,
dynamic collections and smart playlists. The in-app help (the question-mark
button in the search box) lists the same examples. The server is the authority:
the web page only pre-checks what you type so a mistake shows up before you
press Enter.

## Basics

- Plain words search the text of a book.
- `field:value` filters on one field, for example `author:kyle` or
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

### What a per-user term may say

A per-user term is compared against a viewer's listening state one book at a time, so
its value has to be something that comparison can read. These are refused when a
collection or smart playlist is saved or evaluated and when the library list runs a
search (`search=`, the `filters=` the search box sends, and the substring fallback used
while the index is rebuilding), with a message that names what is valid:

| Field | Accepted | Refused |
|---|---|---|
| `read_status` | exactly `unstarted`, `in_progress`, `finished` or `abandoned`, alone or in a list: `read_status:(finished\|in_progress)` | any other word (`completed`, `in-progress`), a comparison (`read_status:>finished`), a pattern (`read_status:fin*`, `*finished`, `finished~`), an empty list `read_status:()` |
| `progress_pct` | a number: `progress_pct:>50`, `progress_pct:[10 TO 90]` | text (`progress_pct:>half`, `progress_pct:<inf`), a pattern (`progress_pct:50*`), `!=` and `==`, an open end (`progress_pct:[* TO 50]`; write `progress_pct:<=50`), a comparison inside a list (`progress_pct:(>50\|<10)`) |
| `last_played` | a date as under Dates, or `*` (has been played at all) | a date that does not parse (`last_played:>30d` needs a sign), any other pattern (`last_played:2026-01-01*`), a comparison inside a list |

The names are lowercase: `READ_STATUS:finished` is refused (write `read_status:finished`),
because the matcher compares field names as written; any letter case counts, so a
look-alike letter that folds to one (`read_statu` followed by the long s, U+017F) is refused too.
Each of these used to be answered silently and wrongly: `read_status:completed` matched
nothing, and `-read_status:completed` matched every book, for every viewer. The old
matcher also ignored the pattern and comparison on a `read_status`, so `read_status:finished*`,
`read_status:*finished`, `read_status:finished~` and `read_status:>finished` all matched the
finished books, `progress_pct:50*` matched 50, `progress_pct:<inf` matched every book and
`last_played:2026-01-01*` matched that day; these now get the refusal above.

A stored collection or smart playlist saved before this check, whose query is one of
these, reads as having no members available, with the reason in the message, until it
is edited. A smart playlist also drops the member list its last evaluation cached, so
the Audiobookshelf app stops serving the old answer too. A rename that sends the stored
query back unchanged is not refused for a query it did not touch.

A per-user term is always required together with the rest of the query: the index
answers the other terms and the per-user part is applied afterwards to the books it
returned. That is right for a plain search, for a term negated on its own
(`-read_status:finished`) and for several states of one field
(`read_status:(finished|in_progress)`), which is one term that matches any of them.
It cannot express an `OR` between a per-user term and another term
(`title:Alpha OR read_status:finished`), or a `NOT` over a group that holds one
(`-(read_status:finished title:Alpha)`). Those used to be evaluated as an `AND`
without a word, so a collection or smart playlist showed far fewer books than its
query says. They are now refused with a 400 when a dynamic collection or a smart
playlist is created or its query is changed, with a message that points at the
`read_status:(finished|in_progress)` spelling, and a stored query of that shape
shows no members, with a message saying its query can no longer be evaluated, until
it is edited. The same shapes over indexed terms only (`title:Alpha OR title:Bravo`)
are unaffected. The library search refuses them too: a search with one of these shapes
answers 400 `invalid search: ...` with the same message, instead of the `AND` answer it
used to give. The search box cannot write such a shape into its `field:value` filters, so
when the text names a per-user field and has an `OR`, an explicit `AND`, a group or a `NOT` over a
group the web sends it as typed and shows that message; a per-user value the filters carry (a
status that is not one of the four, a pattern) is refused the same way with
`invalid filter value: ...`. Supporting `OR` and `NOT` over per-user terms properly, in
the library search and in the collection evaluator, is tracked in `todo.d`.

`tag:` is an exact tag filter: it finds the books that carry that whole tag, ignoring
case, exactly as the tag chips and the `tags[]` parameter do (`tag:"to-read"` does not find
a book tagged only "read"; `tag:knight` does not find "dark-knight"). The search index holds
a tag as words, so the filter is applied outside it, ANDed with the rest of the query. It
therefore works only where it applies to the whole query: at the top level, or in a group
that is only ANDed (`read_status:finished AND tag:noir`, `(read_status:finished tag:noir)`),
and several `tag:` terms are all required. Inside an `OR`, inside an alternation
(`tag:(noir|gotham)`), under `NOT` or `-`, or with `*`, `~` or `^` in an unquoted value (a
wildcard, a fuzzy marker, a boost: `tag:noir*`, `tag:noir~1`, `tag:"noir"^2`) it is refused
with a message; pick the tag chip, or write it at the top level. Quote a tag that contains
one of those characters (`tag:"c++*"` is that literal tag), but a boost or fuzzy marker
after the closing quote is still refused. A word that is only a stop word or punctuation
next to a tag term (`the tag:noir`, `- tag:noir`, `(tag:noir OR)`) is dropped, as it is
next to any other term, and the tag filter applies to the rest. `tags:` is the index field
itself and still matches by word.

The Library search box sends a query with boolean structure whole as `search=`, with no
filters or `tags[]` of its own for it, so the server judges the whole query and the page
shows its message. Structure is decided from the terms the box cuts the text into, never
from the raw characters: a bare `OR`, `AND`, `NOT`, `||`, `&&` or `|`, the opening of a group
(`(a`, `-(a`), a `-` or `NOT` before a `tag:` term, a `tag:` value that is not one whole name
(`*`, `~` or `^` in it, a comparison, a range), or a boost after a tag (`tag:noir ^2`, with or
without a space). A field's VALUE is never structure: the parentheses, pipes and spaces
inside a `/regex/` (`title:/^(the|a) /`, `title:/hush|knight/`), a quoted value
(`title:"Year (One)"`) or a plain value (`title:Batman(1989)`) are text, and such a term
stays a filter. `Batman -tag:"to-read"`, `-tag:noir`, `tag:noir OR tag:gotham` and
`tag:noir*` therefore answer 400 with the tag message instead of quietly dropping the
negation.

A negated field other than `tag` (`-title:x`, `NOT narrator:y`, `-metadata:applied`) stays a
filter. The reason is the matching, not whether the index holds the field (`title`,
`narrator` and `library_state` are indexed; `metadata`, `ident`, `codec` and about twenty
more are not): a filter matches by substring, glob or regex, the index by whole words. Sent
whole, `-title:bat` would stop excluding "Batman", a stop-word value would exclude nothing
or everything, and a field only the filter layer knows would be refused in `search=`.

A `/regex/` value is refused in `search=` with a 400 (`invalid search: a /regex/ value
cannot be used in the search parameter ...`), also in a dynamic collection or smart
playlist query, whether or not the text parses. The search index has no regular
expressions: `title:/hush|knight/` was read as the words "hush" and "knight" and found one
book of two, and `title:/^(the|a) /` (which the parser cannot read) fell to a substring
match that found nothing, both without a message. A regex works as a field filter on its
own, which is how the Library sends it; to combine it with an `OR` or a group, use plain
words in the group and keep the regex term outside it. A quoted value that starts with a
slash (`title:"/x/"`) is plain text.

How the search reads the viewer's listening state for a `read_status`, `progress_pct` or
`last_played` term: the candidate ids from the index are judged first and only the page that
survives is loaded. With 256 candidates or fewer each id is one point read of the viewer's
state row; with more, the viewer's rows are listed in one scan (measured: 1.6 ms for 1,000
rows, 30 ms for 20,000, against a few microseconds per point read, so a narrow search does
not pay for the scan). A row that cannot be read or decoded is judged as an unstarted book
and logged with the book id: the search does not fail for it. The collection evaluator is
the opposite on purpose and fails closed (the collection reads as `members_unavailable`),
because a collection is a saved definition other screens and the Audiobookshelf endpoints
show as the truth about its members, while a search is a filter the viewer reads at once
and can change. A request that has gone away stops reading.

Text the parser cannot read (an unclosed group, a trailing `OR`) that writes a per-user
term is refused with `could not be read`, not matched as plain text; unreadable text with
no per-user term still falls back to a plain match on title and author.

A query that names a per-user field anywhere in its text, even where the grammar reads
it as something else (`author:read_status`, a quoted `"read_status:finished"`), is
treated as per-viewer too: it shows each viewer their own answer and stores no list.
A query the evaluator accepts is unaffected by that choice.

A collection or smart playlist lists at most 10,000 books. A query that matches more
(`read_status:unstarted` on a large library) is cut to the first 10,000 in its sort
order and says how many it really matches (`materialized_total` on a collection,
`book_ids_total` on a smart playlist, `totalBooks` on the Audiobookshelf collection
row). With no sort the order is ascending book id, which is oldest first because ids
are ULIDs, so a broad unsorted query keeps the oldest 10,000 books. Add a sort and a
limit to choose which books those are. For a collection the server also logs the cut,
once a minute per collection (and, for a per-viewer collection, without the count, which
is one viewer's listening); a smart playlist's cut is reported in its answer and is not
logged.

Materializing a smart playlist makes a static copy, and that copy is cut the same way:
it holds the first 10,000 books, `materialized_total` on the new playlist says how many
the query matched, and its description says so.

Two more things are checked when a collection or smart playlist is saved: `sort_json`
must be a JSON array of `{"field", "direction"}` objects (a spec that does not parse
fails every evaluation of two or more books, for every viewer), and a changed query is
checked as above.
