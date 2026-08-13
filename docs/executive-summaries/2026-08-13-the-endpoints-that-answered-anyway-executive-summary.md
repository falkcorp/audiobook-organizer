<!-- file: docs/executive-summaries/2026-08-13-the-endpoints-that-answered-anyway-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8d0a1513-e21f-4853-8db2-c536773c0bb8 -->
<!-- last-edited: 2026-08-13 -->

# Executive Summary: The Endpoints That Answered Anyway

**Date:** 2026-08-13
**Changes:** PRs #2375, #2376, #2377, #2378
**Written for:** anyone who uses the audiobook organiser, not the people who build it

---

## In one paragraph

Opening a series in the audiobook app showed a pile of books that had nothing to do with
that series, while the series itself insisted it contained zero books. Opening a playlist
showed nothing at all. None of this was a display glitch — the app was asking the right
questions and the organiser was answering the wrong ones, confidently. When the app asked
"which books are in this series?", the organiser ignored the "which series" part of the
question and handed back an arbitrary slice of the entire 34,280-book library. When the
app asked for a playlist, the organiser quietly forwarded the request to a different
system that speaks a different language, and the app got back something it could not
read. Three separate faults, one shape: a request that was accepted, understood by
nobody, and answered with a straight face.

---

## What you would have noticed

Series pages full of strangers. You tap a series you know has four books in it, and you
get "Space Carrier Avalon" and a dozen other unrelated titles, with a count underneath
reading **0 books**. Tap a different series and you get a *different* random assortment —
which is what made it look like the app was broken rather than the library. Playlists
opened empty every time, including playlists you had just created and could see on the
web interface. Long series lists were slow to load, because the organiser was sending the
complete list of all 14,625 series on every single request, no matter how few the app
asked for.

---

## What was actually wrong

**The series filter was read by nothing.** The app requests a filtered list of books —
"only the ones in series 14792". The organiser's code accepted that filter, stored it
nowhere, consulted it never, and returned page one of everything. This is worse than an
error, because an error would have shown the app that something was wrong. Instead the
app received a perfectly valid list of perfectly real books and displayed them. The
"0 books" count came from a different, correct calculation — so the two halves of the
screen were disagreeing, and only one of them was lying.

**Playlists were never actually built for the app.** The address the app uses to open a
playlist had no implementation behind it. Requests to it were being redirected to the
organiser's own internal interface, which returned the playlist in a format the app has
no idea how to read. From the app's point of view the playlist was empty.

**Series lists ignored "give me 50."** Page and page-size instructions were discarded, so
every request for a page returned all 14,625 series, unsorted and always from the
beginning. Fixing the series-contents bug above would have made each of those responses
roughly ten megabytes — so this had to be fixed first, before the other fix could ship
safely.

**And one we caused ourselves, same day.** The playlist fix reserved a slightly wider
area than it needed. That broke six working operations — editing a playlist, deleting it,
adding and removing books, reordering it, rebuilding it — which began returning "not
found" on a live server. Caught within the hour by re-testing the routes rather than
trusting the fix, and repaired in the same day's work.

---

## How we know it is fixed

Each fault was verified against the live server before and after, not just in tests:

| What the app asks for | Before | After |
|---|---|---|
| Books in one specific series | 34,280 results, first one unrelated | **2 results, both correct** |
| A series' book list and running time | empty, `0` — on all 14,625 series | **populated, correct total** |
| 3 series, please | all 14,625 (3.4 MB) | **3 results, 2 KB, count still accurate** |
| Open a playlist | redirected, unreadable | **opens, 77 items** |
| The six playlist operations | broken by our own fix | **working again** |

Every fix also carries a test that was proven capable of failing — each safeguard was
deliberately broken to confirm it noticed, then restored. One of those checks turned out
to be silently useless and was strengthened before it shipped.

---

## Why it went unnoticed for so long

The organiser has an automated conformance suite that compares its answers against 28
recorded examples of what the real AudiobookShelf server sends. All three faults sailed
through it, because **not one of those 28 recorded examples contains a search, filter, or
page instruction.** The suite could only ever check the plain, unfiltered version of each
question. A request that carries an instruction nobody reads is invisible to a test
corpus in which no request carries an instruction at all. Re-recording those examples
with real-world instructions attached is now on the work list.

---

## Still outstanding

- **Author and series detail pages** in the app are not implemented yet and currently
  return "not found". The app degrades quietly rather than showing an error, so these
  pages simply render blank. Documented with the routing analysis needed to add them
  safely.
- **Collections** are empty in the app because the organiser has no concept of a
  collection at all — this is an unbuilt feature, not a fault.
