<!-- file: docs/executive-summaries/2026-09-02-a-merge-can-no-longer-keep-the-wrong-book-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: b4d8e2f6-9a1c-4e37-8f5b-0c2d4e6a8b1f -->
<!-- last-edited: 2026-09-02 -->

# A merge can no longer keep the wrong book

**Pull request:** PENDING

## Executive Summary

- When the app finds two entries for the same audiobook, it "merges" them: one entry
  survives, the other is marked deleted and cleaned up a month later. Every merge — from
  the web UI, the duplicate finder, the review queue, the nightly maintenance — goes
  through one shared routine. This change hardens that routine in three ways.
- **It keeps the copy that can actually play.** Some entries in the library have no
  audio file on record at all (about 12,500 of them). The old rule for picking the
  survivor looked at format and quality first, so an empty "m4b" entry could beat the
  mp3 that really had the file — and the one playable copy went onto the delete clock.
  Now an entry with a file always beats one without; quality only breaks ties. If a
  person explicitly picks the file-less entry as the survivor, the app refuses and says
  why, instead of quietly doing it.
- **It refuses to merge something that is already deleted.** After a manual "keep this
  one", a stale internal pointer could make the next library scan hand the surviving
  entry its own deleted twin, and merge them again the other way round. The end state
  was a "book" whose only live member was a deleted row — and a month later the purge
  removed both. A deleted entry is now never chosen or forced as the survivor. Replaying
  a merge that already happened is harmless and does nothing.
- **A failed delete no longer becomes a harder delete.** If the "mark as deleted" write
  failed, the old code fell back to erasing the row outright — on the same store that had
  just failed to write. That fallback is gone in both places it existed, and the ability
  to call the erase from there was removed so it cannot return by accident. A merge whose
  loser could not be marked deleted now reports a failure that names the book, rather
  than reporting success while the loser stays live.
- Every guard was tested against a real database, and each was then deliberately
  removed or inverted to confirm the tests catch it — fourteen of fourteen were caught.
- Whether the old survivor rule already cost any library its playable copy is **not
  known**: the census of file-less entries exists, but no one has yet checked how many
  of them are the survivor of a merge whose loser had the file. That check is the next
  step, and the repair (re-pointing the survivor at the loser's file) is unowned.
