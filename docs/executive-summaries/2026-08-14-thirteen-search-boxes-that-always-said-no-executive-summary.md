<!-- file: docs/executive-summaries/2026-08-14-thirteen-search-boxes-that-always-said-no-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: d05d0c86-3b27-4065-8d1c-a25999af3968 -->
<!-- last-edited: 2026-08-14 -->

# Executive Summary: Thirteen Search Boxes That Always Said No

**Date:** 2026-08-14
**In one line:** Thirteen of the things you can search your library by — release year,
ISBN, length, file size, and nine more — never worked, and always answered "no books
found" instead of admitting they didn't understand the question.

---

## What you saw

You typed `year:2019` into the library search box. It came back: no books found.

So you concluded you have no audiobooks from 2019. Or you assumed you'd typed it wrong,
tried `year:2019 ` with a space, tried `Year:2019`, gave up, and searched some other way.

The same thing happened with `duration:`, `file_size:`, `bitrate:`, `isbn13:`,
`isbn10:`, `work_id:`, `series_number:`, `channels:`, `bit_depth:`, `sample_rate:`,
`created_at:` and `updated_at:`. Thirteen different ways to narrow down your library,
every one of them silently useless.

Nothing was broken-looking. No error, no red box, no "unknown field" warning. Just an
empty result and a count of zero, which reads exactly like a fact about your library.

## What was actually happening

The search box and the server keep separate lists of what you're allowed to search by.
The search box's list is what it offers you. The server's list is what it can actually
do. Nobody had ever checked the two lists against each other, and they didn't match.

When you typed `year:2019`, the search box recognised `year` as a real search term,
packaged it up correctly, and sent it off. The server received a perfectly well-formed
request asking about a field it had never heard of — and its response to "I don't know
what that is" was to report that no books matched.

Four of the thirteen are almost funny. The server *could* search by length, file size,
bitrate and sample rate the whole time. It just called them different names —
`duration_seconds` instead of `duration`, `file_size_bytes` instead of `file_size`. The
search box offered you the short name; the server only answered to the long one. On your
library, `duration:1` returned zero books and `duration_seconds:1` returned **25,090** —
the same books, the same data, one name apart.

There is a comment in the code above those four names reading *"Aliases for frontend
field names."* Whoever wrote it had the direction backwards: those are the server's own
names, not the ones the search box sends. The comment was written, believed, and never
tested against the actual search box.

## Why "zero results" is the wrong answer

This is the part worth keeping. A count of zero is not a neutral answer. It is a
confident claim that your library contains nothing matching what you asked for.

When the server didn't recognise a search term, it made that claim anyway. A typo, a
renamed field, and a genuine "you own no 2019 audiobooks" all produced the identical
response — and only one of those three was true. The search results couldn't tell you
which situation you were in, so the honest reading of "0" and the wrong reading of "0"
looked exactly alike.

The same shape showed up in a second place today. Asking the library to list your deleted
books returned zero, while 3,953 of them sat in the trash perfectly intact. Same cause:
the server didn't recognise the request and reported emptiness rather than confusion.

## What changed

All thirteen search terms now work. The short and long names both work, so whichever one
you type finds your books.

`year:` searches both years a book can carry — the year the book was printed and the year
the audiobook was released. Those two often differ, and someone typing `year:2019` means
either one, not whichever the code happened to pick.

Most importantly: **when the server doesn't recognise a search term, it now says so.** It
names the term it didn't understand and lists the ones it does understand, instead of
answering with a zero that means something else entirely.

## Also fixed today

Books you had deleted were still being offered up for writing metadata back into your
iTunes library. Deleting a book here is reversible — it goes to a trash you can restore
from — so a deleted book keeps its link to the iTunes entry it came from. The preview
that decides "here's what we're about to write into iTunes" was including those deleted
books, with no way to show you they were deleted, because that list has no column for it.

Your library holds 3,953 deleted-but-restorable books. Any of them still linked to iTunes
were eligible to have their metadata pushed back out.

## What keeps it from happening again

A test now reads the search box's own list of search terms and checks every single one
against what the server can actually do. If someone adds a term to the search box that the
server can't handle — or renames one on the server — the build fails, with a message
naming the specific term.

That test had to be written this way round. A test of just the search box would have
passed: its list was correct *for the search box*. A test of just the server would have
passed too. The mismatch only exists in the gap between them, so only a test that holds
the two against each other can see it.

---

**Related:** [The Books Search Could Not See](2026-08-13-the-books-search-could-not-see-executive-summary.md)
and [Deleted But Not Gone](2026-08-13-deleted-but-not-gone-executive-summary.md) — the
same "zero results is not a neutral answer" problem, found in two other places this week.
