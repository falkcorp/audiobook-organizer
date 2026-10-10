<!-- file: docs/executive-summaries/2026-10-10-owner-rejected-candidates-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 02f9b11f-058a-4b13-81a6-59ff338b29e3 -->
<!-- last-edited: 2026-10-10 -->

# Rejected metadata matches were still being applied (2026-10-10)

Branch: fix/owner-rejected-never-applied (pull request and merge commit to be
added when it merges).

## Executive Summary

- When you rejected a suggested metadata match for a book, the organizer
  remembered the "no", but most of the ways it applies metadata never checked
  it. A rejected match could still be written onto the book.
- Seven different routes could do this: the review page's apply buttons
  (single row and bulk), scripted bulk applies, applying the results of an
  older search run, the automatic match for books identified from their audio,
  the nightly "upgrade to a better source" job, the older bulk metadata fetch,
  and picking the match by hand in a book's metadata search window.
- The nightly upgrade job was the worst case: it replaces fields that already
  have values, so a rejected match could overwrite a good title, description
  or publisher.
- Now a rejected match is never applied, by any route, and no apply button can
  override it. To use a match you rejected, you un-reject it first.
- Rejecting a match also moves it to the bottom of that book's suggestion list,
  so the review page shows the next-best suggestion instead. Books that were
  already in this state are fixed by a one-time background pass at the next
  start; it only re-orders suggestions and never deletes one.
- The review page and the metadata search windows now mark a suggestion you
  rejected, so you can see why it will not apply.
- Verified with regression tests that reproduce each of the seven routes:
  every one applied the rejected match before the fix and refuses it after.

## Applying a rejected match

**What it was.** Each book keeps a short list of suggested matches, best
first, and almost every apply route takes the first one. Rejecting a match
only wrote a note that the next search consulted; it did not move the
rejected match off the top of the list, and the safety check that every bulk
apply runs never read the note at all.

**Why it mattered.** A match you had explicitly turned down could be written
onto the book anyway: by a bulk apply from the review page, by a script, by
applying an older search run's results (the rejection only marked the run you
had open), or by the nightly upgrade, which overwrites filled-in fields. The
book then carried a title, author or series you had already said was wrong,
and with automatic renaming and tag writing switched on, the wrong data also
reached the file names and the audio files' tags.

**The fix.** The safety check gained a hard rule: a match the owner rejected
is refused, and if the list of rejections cannot be read the book is left
alone rather than assumed clear. No apply button, single or bulk, lifts that
rule. The routes that do not use the safety check (the audio-based automatic
match, the older bulk fetch, and picking a match by hand in a book's search
window) now check rejections themselves.

## A rejected match staying at the top of the list

**What it was.** Rejecting a match left it first in the book's suggestion
list, and a later search could also put it straight back on top.

**Why it mattered.** The review page showed the rejected match as the book's
suggestion, so the owner kept being offered something already refused, and
the automatic routes kept trying to apply it.

**The fix.** Rejecting (or un-rejecting) a match re-sorts that book's
suggestion list on the spot, and every search that rewrites the list sorts
rejected matches to the bottom. Matching ignores upper and lower case, the
same way the list already treats two suggestions as the same record. A
one-time background pass re-sorts the lists that were already affected; it
records when it has finished and retries at the next start if any book
failed.

## Seeing the rejection on the review page

**What it was.** Nothing on the review page said a suggestion had been
rejected.

**Why it mattered.** With the new rule, an apply would come back refused with
no visible reason.

**The fix.** The review page marks the row "Rejected", and a book's metadata
search window marks the suggestion "Rejected by you" and will not let you pick
it; both say it will not be applied until it is un-rejected. Un-rejecting today needs the
search run the rejection was made in; a simpler un-reject button on the row is
filed as a follow-up, and so is putting the older bulk fetch through the full
safety check, which it still skips apart from the rejection check added here.
