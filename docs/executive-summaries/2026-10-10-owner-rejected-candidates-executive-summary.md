<!-- file: docs/executive-summaries/2026-10-10-owner-rejected-candidates-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 02f9b11f-058a-4b13-81a6-59ff338b29e3 -->
<!-- last-edited: 2026-10-10 -->

# Rejected metadata matches were still being applied (2026-10-10)

Branch: fix/owner-rejected-never-applied (pull request and merge commit to be
added when it merges).

## Executive Summary

- When you rejected a suggested metadata match for a book, the organizer
  remembered the "no", but most of the ways it applies metadata never checked
  it. A rejected match could still be written onto the book.
- A search of the code found nine routes that apply a match to a book. Each of
  them applied a rejected match before this change, and each refuses it now:
  1. the review page's apply buttons, for one row or in bulk, including a
     match you had approved there;
  2. scripted bulk applies;
  3. applying the results of an older search run, now or once the book's scan
     finishes;
  4. the automatic match for books identified from their audio;
  5. the nightly "upgrade to a better source" job;
  6. the older bulk metadata fetch;
  7. picking the match by hand in a book's metadata search window;
  8. the automatic "find and apply metadata" step: the iTunes import's
     enrichment, organizing with metadata fetch first, the book page's
     "Fetch metadata" button, and the jobs that look up the real author of
     books filed under a production company;
  9. the repair that copies a matched copy's metadata onto another copy of
     the same book.
- The nightly upgrade job was the worst case: it replaces fields that already
  have values, so a rejected match could overwrite a good title, description
  or publisher.
- No apply button lifts a rejection. To use a match you rejected, undo the
  rejection: a book's metadata search window marks the match "Rejected by
  you" and has an Undo button next to it.
- A rejection now refuses only the record you rejected. When a match carries
  an Audible ASIN (or an ISBN), rejecting it no longer also blocks a
  different record with the same title, such as the unabridged edition.
  Rejections made before this change still refuse every record of that
  title, as they did, until you undo them.
- Rejecting a match also moves it to the bottom of that book's suggestion
  list, so the review page shows the next-best suggestion instead. Books that
  were already in this state are fixed by a one-time background pass at the
  next start; it only re-orders suggestions and never deletes one.
- Verified with regression tests for each route: each test failed before the
  fix (the rejected match was applied) and passes after it.

## Applying a rejected match

**What it was.** Each book keeps a short list of suggested matches, best
first, and almost every apply route takes the first one. Rejecting a match
only wrote a note that the next search consulted; it did not move the
rejected match off the top of the list, and most apply routes never read
the note at all. The automatic "find and apply metadata" step did not read
it either: it searches the providers afresh and applies the best answer, so
a rejected record came straight back.

**Why it mattered.** A match you had explicitly turned down could be written
onto the book anyway, by any of the nine routes above, including ones that
run unattended (the iTunes import, organize, the nightly upgrade, the
production-company jobs). The book then carried a title, author or series
you had already said was wrong, and with automatic renaming and tag writing
switched on, the wrong data also reached the file names and the audio files'
tags.

**The fix.** The safety check every bulk apply runs gained a hard rule: a
match the owner rejected is refused, and if the list of rejections cannot be
read the book is left alone rather than assumed clear. No apply button,
single or bulk, lifts that rule. The routes that do not use the safety check
now check rejections themselves. The automatic "find and apply metadata"
step skips a rejected record and tries the provider's next answer, then the
next provider; when only rejected records match it applies nothing and says
so. The copy-between-copies repair holds the book for review instead of
applying a rejected record.

## Rejecting only the record you meant

**What it was.** A rejection was stored by provider and title only. Rejecting
one record refused every record from that provider with that title, and with
no way back from the search window, a single rejection could lock you out of
the correct edition everywhere.

**Why it mattered.** Audiobooks often have several records with the same
title (abridged and unabridged, different narrators). Turning down the wrong
one should not also turn down the right one.

**The fix.** A new rejection remembers the rejected record's ASIN, or its
ISBN when it has no ASIN. A record carrying a different identifier is not
refused. When either side has no identifier to compare, the rejection still
covers the whole title, as before. Undo removes the rejection of that one
record and leaves your other rejections of the same title in place.

## A rejected match staying at the top of the list

**What it was.** Rejecting a match left it first in the book's suggestion
list, and a later search could also put it straight back on top.

**Why it mattered.** The review page showed the rejected match as the book's
suggestion, so you kept being offered something you had already refused,
and the automatic routes kept trying to apply it.

**The fix.** Rejecting (or undoing a rejection) re-sorts that book's
suggestion list on the spot, and every search that rewrites the list sorts
rejected matches to the bottom. The sorting reads your rejections at the
moment it writes the list, so a rejection made while a search is running is
honoured. Matching ignores upper and lower case, the same way the list
already treats two suggestions as the same record. A one-time background
pass re-sorts the lists that were already affected; it records when it has
finished and retries at the next start if any book failed.

## Seeing and undoing a rejection

**What it was.** Nothing on the review page or in the search window said a
suggestion had been rejected, and undoing a rejection needed the search run
it was made in, which no screen offered.

**Why it mattered.** With the new rule, an apply would come back refused with
no visible reason and no way to change your mind.

**The fix.** The review page marks the row "Rejected". A book's metadata
search window marks the suggestion "Rejected by you", will not let you pick
it, and offers Undo, after which the suggestion can be picked and applied
again. Still to do, filed as follow-ups: an Undo on the review page's row
itself, putting the older bulk fetch through the full safety check (it only
checks rejections today), and re-sorting a book's list after the repair
copies another copy's suggestions onto it.
