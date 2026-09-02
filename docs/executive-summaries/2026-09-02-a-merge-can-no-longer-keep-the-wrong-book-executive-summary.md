<!-- file: docs/executive-summaries/2026-09-02-a-merge-can-no-longer-keep-the-wrong-book-executive-summary.md -->
<!-- version: 1.3.0 -->
<!-- guid: b4d8e2f6-9a1c-4e37-8f5b-0c2d4e6a8b1f -->
<!-- last-edited: 2026-09-02 -->

# A merge can no longer keep the wrong book

**Pull requests:** https://github.com/falkcorp/audiobook-organizer/pull/3047 (merged),
and https://github.com/falkcorp/audiobook-organizer/pull/3053 correcting a fault in it,
described at the end.

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

## The follow-up: the new rule was reading the wrong evidence

**Pull request:** https://github.com/falkcorp/audiobook-organizer/pull/3053

- The "keeps the copy that can actually play" rule above decided whether an entry had a
  file by looking at the entry's list of **per-file records**. But the library records a
  book's audio in two places: an older, single "file path" on the book itself, and the
  newer per-file list. The roughly 12,500 entries described above as having "no audio
  file on record" are exactly the ones that have the **old kind** of record and not the
  new kind — they do have a playable file; the app just hadn't written the per-file rows
  for them yet.
- So the guard that shipped in the change above got those 12,500 backwards. In any merge
  between one of them and a twin that *did* have per-file rows, the guard called the
  twin "the playable one" and put the other — one in five books in the library — on the
  delete clock. It was protecting the wrong side. The wording in this summary that
  called them file-less was itself repeating the mistake.
- Now either kind of record counts as having a file, and the comparison is simply
  "has one / doesn't" — an entry with three per-file rows does not outrank one with a
  single path.
- Two smaller faults fixed alongside it. Asking the app to merge the same pair twice,
  with the two books named in the opposite order, could flip which one survived; the
  survivor choice is now the same regardless of order (an existing survivor is kept,
  then the older entry). And when the app *refused* a merge for one of the reasons
  above, the web interface reported it as a generic server error instead of showing the
  reason — the reason now comes through on every merge screen, including the duplicate
  candidates page and the "wait for this book's deep scan first" refusal.
- A second review of this correction found two more faults and fixed them here. A
  temporary database hiccup while loading a book was being reported as "book not found",
  and the duplicates page treats "not found" as "already merged" — so a flaky read could
  have quietly retired a real duplicate pair. And if the step that moves a merged-away
  book's iTunes identifiers to the survivor failed, the app used to hide the book anyway
  and report success, leaving those identifiers pointing at a hidden entry with no way
  back. Now the book stays visible, the merge reports the failure, and retrying it
  finishes the job.
- Checked the same way: each new rule was deliberately broken and the tests caught it,
  twelve of twelve. The backwards guard was live on the production server from 10:44 to the
  deploy of this correction. The server's activity log for that window was read: no merge
  ran, the automatic duplicate resolver is switched off, and the 6-hourly duplicate
  refresh had not yet fired. No library entry was demoted by it.
