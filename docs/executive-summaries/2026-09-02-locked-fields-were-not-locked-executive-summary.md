<!-- file: docs/executive-summaries/2026-09-02-locked-fields-were-not-locked-executive-summary.md -->
<!-- version: 1.0.1 -->
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
- The deal was only being kept in one of the eight places the app writes metadata (the
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
  and every one of the write paths goes through it. When a fetch would have changed a
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
- What is **not** known is how many books have already had a locked field overwritten.
  The lock rows still hold your value, so those books can be found and the stored
  value restored from the lock — that repair is a separate piece of work and is not
  yet owned.
