<!-- file: docs/executive-summaries/2026-09-02-locked-fields-were-not-locked-executive-summary.md -->
<!-- version: 1.0.2 -->
<!-- guid: 8f58de07-0051-4369-b45c-21820ad8dd14 -->
<!-- last-edited: 2026-09-02 -->

# Locked fields were not locked

**Pull request:** https://github.com/falkcorp/audiobook-organizer/pull/3054

## Executive Summary

- When you correct a book's details by hand — fix the author, set the right series,
  retype the title — the edit screen tells you those fields are "automatically locked
  to prevent overwrites from future fetches". That is the deal: the app can keep
  pulling in details from Audible and the other sources, but it must not touch what
  you fixed yourself.
- The deal was only being kept in one of the eight places we first counted (the
  "fetch for selected books" action). Six others — the automatic lookup that runs on
  new books, the by-title lookup, the "Apply" button on a search result, the bulk apply
  job, the audio-transcription matcher and the metadata upgrade job — never checked the
  lock at all. Each one wrote the fetched value straight over yours. The eighth, the
  library rescan, checked but under the wrong names (below).
- It was hard to notice, because the detail page still showed your value: the page
  reads the lock separately and paints your correction back over the top. Every other
  view — the library list, search, the tags written into the audio files, the folder
  and file names the organizer builds — used the overwritten value. What you saw and
  what the app was actually storing had drifted apart.
- The rescan had a second, separate problem. Its lock check did exist, but it looked
  up the locks under different names than the ones the edit screen saves them under
  (`author` instead of `author_name`, and so on), so for author, series and series
  number it always found nothing locked. Its own test locked the names the check used
  rather than the names the edit screen writes, so it passed while proving nothing.
- Now there is one shared list of the thirteen lockable fields and one shared check,
  and every one of those write paths goes through it. When a fetch would have changed a
  locked field it now leaves the field alone and says so — the response and the bulk
  job's summary name the fields that were kept — rather than quietly skipping them.
  Locks set before the current storage format still count. And if the app cannot read
  the locks for a book at all, it refuses to write anything to that book rather than
  guessing that nothing is locked.
- The fix is tested by walking the same list the edit screen writes from: for every
  field, the test locks it, tries to apply a different value through both apply
  routines, and checks the value did not move — after first checking that it DOES
  move when unlocked, so a test that could never fail would be caught. Four deliberate
  breakages of the fix were each detected by the tests.
- A second review then counted the places the app writes to a book properly, rather
  than trusting the first count, and found twenty — not eight. Seven of them still
  never looked at a lock: the ISBN/ASIN top-up the app queues for itself after a
  fetch, the scanner's AI-suggestion apply, the diagnostics page's AI-suggestion
  apply, the iTunes reconcile, three kinds of duplicate merge, and undo/revert. All
  seven now check. The first count being short is the point worth remembering: the
  original claim that "every path" was covered was written from a list, not from a
  count of every place the code writes a book.
- Undo and "revert this fetch" needed a decision rather than a check. Both restore the
  values a book had before a fetch — but if you edited that field yourself since, the
  older value is not a restore, it is the exact overwrite the lock exists to prevent.
  They now leave those fields as you set them and say which ones they left, instead of
  reporting a completed undo that was actually partial.
- Three places write on **your** behalf, so they are allowed to overwrite — but they
  were not recording a lock, which meant the edit you had just made was unprotected
  against the very next fetch. Bulk edits, batch operations and the field choices you
  make when combining two books now record a lock for each field you set. Editing one
  book was also only recording locks for 9 of the 13 lockable fields; ASIN, genre,
  description and series number are now included, and a test now fails if that list
  and the lockable list ever drift apart again — in either direction.
- One inert bug is worth naming: unlocking every field on a book appeared to work and
  did not. Unlocking deleted the new-style lock records, and the app then fell back to
  a leftover copy in the old storage format that nothing ever deleted, and found the
  field locked again. The leftover copy is now removed when locks are saved.
- Still **not** covered, and deliberately left for separate work: about seventeen
  library-repair and regrouping jobs (title repair, junk-title cleanup, series
  renumbering, author-name repair and their siblings) write to a book without checking
  the lock. They are not part of the fetch flow — they run as maintenance — but a lock
  does not stop them today.
- What is also **not** known is how many books have already had a locked field
  overwritten. The lock records still hold your value, so those books can be found and
  the stored value restored from the lock — that repair is a separate piece of work and
  is not yet owned.
