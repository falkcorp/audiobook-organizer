<!-- file: docs/executive-summaries/2026-10-10-owner-rejected-candidates-executive-summary.md -->
<!-- version: 1.4.0 -->
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
- Two more jobs could carry a rejected match onto a book and no longer do:
  the job that fills in missing Audible IDs (it could stamp the rejected
  edition's ID and ISBN onto the book), and the repair that fixes junk
  titles (it could propose the rejected match's title).
- A rejected Audible ID or ISBN now counts for every provider. The provider
  tried after Audible (Audnexus) serves the same Audible product, so before
  this a match you rejected from Audible could still be applied from there.
  Rejecting an Audible match also rejects its ISBN, so another provider's
  record of that same audiobook edition is refused too.
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
so. Every route also checks your rejections again at the moment it writes,
and rejecting a match waits for any write to that book already in progress,
so a rejection made while a search is running is never missed. That
includes the older bulk metadata fetch, which until this round checked only
when it picked the match. The copy-between-copies repair holds the book for
review instead of applying a rejected record.

When a write is refused at that last moment, the author it had already added
to the book is taken off again. Before, that author stayed on the book with
no record of where it came from, so "undo last apply" could not remove it.

## Rejecting only the record you meant

**What it was.** A rejection was stored by provider and title only. Rejecting
one record refused every record from that provider with that title, and with
no way back from the search window, a single rejection could lock you out of
the correct edition everywhere.

**Why it mattered.** Audiobooks often have several records with the same
title (abridged and unabridged, different narrators). Turning down the wrong
one should not also turn down the right one.

**The fix.** A new rejection remembers the rejected record's ASIN and its
ISBNs. A record carrying a different identifier is not refused, including a
record of the same title that has a different ISBN and no ASIN. When either
side has no identifier to compare, the rejection still covers the whole
title, as before.

An ASIN or ISBN you reject is rejected for that book from every provider,
not only the one you rejected it from: Audible and Audnexus serve the same
Audible product under one ASIN, and an ISBN names one edition whoever
reports it. An ISBN-10 and its ISBN-13 count as the same ISBN, and a value
written as "ISBN 0-306-40615-2" counts as the bare number.

Undo removes the rejection of that one record. Undoing it from the Audnexus
row also removes the Audible rejection of the same product, since the two are
one product. Your other rejections stay in place: other records of the same
title, and a rejection made from another provider that only shares an ISBN
with this record. If one of those still covers the match, it stays refused;
the search window clears its "Rejected by you" mark on Undo today and shows
it again on the next search, and you undo that other rejection too. When the match carries no
ASIN or ISBN, Undo removes the rejection of that provider and title as a
whole, since there is nothing to tell the records apart.

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
again. Still to do, in the to-do list: an Undo on the review page's row
itself (UNREJECT-FROM-REVIEW), putting the older bulk fetch through the full
safety check (BULK-FETCH-NO-GATE; of that check it applies only the
rejections rule today, at the pick and again at the write), and
re-sorting a book's list after the repair copies another copy's suggestions
onto it (VT-COPY-RERANK). None of these can apply a rejected match: every
route refuses it.
