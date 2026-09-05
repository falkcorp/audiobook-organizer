<!-- file: docs/executive-summaries/2026-09-05-books-that-would-not-download-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4d2a9e17-8c53-4b60-9f21-6e0a7c3d51b8 -->
<!-- last-edited: 2026-09-05 -->

# Books that would not download, mostly ones you'd edited

**Pull requests:**
https://github.com/falkcorp/audiobook-organizer/pull/3075 (merged),
https://github.com/falkcorp/audiobook-organizer/pull/3076 (merged), and
https://github.com/falkcorp/audiobook-organizer/pull/3077 (merged).

## Executive Summary

- **The problem you reported:** you'd go to download a book and the file wasn't there,
  so the download failed — and it kept happening on books you had recently corrected the
  metadata on. It was not random. Editing a book's details was quietly breaking its
  download.
- **Why it happened:** when you apply corrected metadata, the app tidies the audio file
  into a new folder and filename that match the new details. But the download link is
  built from a *separate* internal pointer to the file, and that pointer was being left
  behind at the file's **old** location. The bytes had moved; the pointer still aimed at
  where they used to be. So the download looked up an address that no longer held a file,
  and returned "not found."
- **It also created phantom duplicates.** Because the old pointer was stale, the next
  library scan noticed a file sitting at the new path with no pointer of its own and
  created a *second* book record for it. One real book turned into two entries — the
  broken original and a new shell — which is why some books also appeared twice.
- **The fix, in four parts:**
  1. When the app moves a single-file book, it now moves that book's file pointer to the
     new path in the same step, so the download link and the bytes never drift apart.
  2. The repair tool that fixes books with missing files can now recover a book by looking
     at the book's own known path, not only external clues — so the books already broken by
     this can be pointed back at their audio.
  3. When the app decides which author's folder to organize a book into, it now trusts the
     author **you applied** (kept in a durable record) rather than a copy that a later scan
     can silently overwrite with the author from the file's own tags. Previously an applied
     author could be reverted by a rescan and the book would be filed — and its file moved —
     under the wrong author, feeding right back into the missing-file problem.
  4. A new maintenance step can find and merge the phantom duplicate pairs — two book
     records that point at the exact same file — but only when it can confirm they really
     are the same file (identical stored fingerprint), and it reports what it would do
     before it does anything.
- **Data safety:** none of this deletes audio. The repair repoints; the merge only
  collapses records that provably share one identical file and runs in report-only mode
  first. The author fix also closed a related edge: editing a book's author *by picking an
  existing author* (rather than typing a name) used to update only half of the record,
  which could have re-misfiled the book — that now updates both halves.
- **How it was checked:** each safeguard was verified by deliberately breaking it and
  confirming a test catches the break — the download pointer move, the author preference,
  the duplicate-merge guards, and the author-by-ID edit were all confirmed this way.

## 1. What actually broke

Two facts had to be true at once for a book to become undownloadable, and applying
metadata made both true.

First, the download does not read the book record's own path. It resolves the actual bytes
through a lower-level file record — think of the book record as the catalogue card and the
file record as the exact shelf location written on a separate slip. When you applied
corrected metadata, the app renamed and moved the audio file to a tidy new location and
updated the catalogue card, but left the shelf slip pointing at the old, now-empty spot.
The download followed the slip, found nothing, and reported the file missing.

Second, the next routine library scan made it worse. Finding a real file at the new
location with no shelf slip of its own, the scan assumed it had discovered a brand-new
book and created a second record for it. Now there were two cards for one book: the
original (whose slip pointed at nothing) and a new one — which is why affected books both
failed to download and sometimes showed up twice.

## 2. Why editing was the trigger

This almost always happened on books you'd edited because editing is what moves the file.
A book left with its scanned-in details sits still; nothing renames it, so its shelf slip
stays correct. The moment you corrected the title, author, or series and the app tidied
the file into a matching folder, the slip was orphaned. The books you cared enough to fix
were exactly the ones that broke.

## 3. The author twist

There was a second, quieter way the same damage happened. The author you apply is stored
in a durable place, but the app also keeps a convenience copy of it — and a library scan,
seeing that the file's own embedded tags still name the old author, would quietly overwrite
that convenience copy back to the tag author. The part of the app that decides which
author folder to file a book into was reading the convenience copy. So a book you had
correctly re-authored could, after a scan, be moved into the *wrong* author's folder —
moving the file again, and orphaning the shelf slip again. It now reads the durable record
first, and only falls back to the convenience copy when the durable record has nothing
usable, so a normal scanned-only book is unaffected.

## 4. Cleaning up what already broke

The first three parts stop new breakage. The fourth helps undo the old: a maintenance
step can repoint books whose files went missing, and merge the phantom duplicate pairs the
stale slips created. Both are conservative — the merge only touches two records when it can
prove they share one identical file, and everything runs in a report-only pass first so the
planned changes can be reviewed before anything is applied.
