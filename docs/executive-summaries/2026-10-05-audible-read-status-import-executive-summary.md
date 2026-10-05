<!-- file: docs/executive-summaries/2026-10-05-audible-read-status-import-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3ed99468-f010-4872-a4be-20545a39dccf -->
<!-- last-edited: 2026-10-05 -->

# Bringing Audible's "finished" marks into the library

PR: https://github.com/falkcorp/audiobook-organizer/pull/3746

## Executive Summary

- The library can now read an Audible library export and copy what it says
  about each book (finished, partly listened, or not started) onto the
  matching books, for one named listener.
- You have to name the listener on every run; it is never assumed. The
  account that syncs with iTunes is refused, so nothing reaches the iTunes
  library.
- Matching is careful. A book is used only when exactly one copy in the
  library fits, and its length is within 10% of Audible's. Anything doubtful
  is listed for review and never applied: two candidates, a length that
  differs, a junk title, or two Audible books pointing at one library book.
- Newer listening in the library always wins. A book that is already
  finished, one the listener abandoned, and one played more recently than
  Audible's date are all left alone. Nothing is ever marked unfinished.
- Finish dates come from Audible, never from the day the import runs, so a
  phone that syncs later still wins with its real, later progress.
- It works like the other repairs. It first lists what it would do, then
  writes only the rows you pick. Every change is recorded, and one undo puts
  it back. The undo leaves alone any book the listener has played since the
  import.

## The import

**What it was.** Years of listening history lived only in Audible. In the
library, those books looked unread, so "Continue listening" and the finished
shelf were wrong.

**Why it mattered.** Marking hundreds of books by hand is not practical.
Writing them blindly risks two kinds of damage. Old Audible progress could
overwrite newer listening from a phone. Or the import could stamp every
finish with today's date, which makes it beat the phone's real progress the
next time it syncs.

**The fix.** A new repair matches each Audible title to the library: first
by Audible's product id, then by title and author, always checking that the
length agrees. It marks a finished book finished, with Audible's own finish
date, and locks the mark so the next playback sync does not undo it. It
copies a partial position only onto a book that has never been played here.
Before each write it records the listener's previous state. Undo restores
that state only while nothing newer has happened.
