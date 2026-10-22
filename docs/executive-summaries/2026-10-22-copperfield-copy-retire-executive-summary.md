<!-- file: docs/executive-summaries/2026-10-22-copperfield-copy-retire-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 86a41d45-5e65-4458-bcb8-34a1762998c0 -->
<!-- last-edited: 2026-10-22 -->

# Retiring single-file copies of multi-file books, safely and undoably

PR: [#4107](https://github.com/falkcorp/audiobook-organizer/pull/4107). Merge
commit: not recorded yet (the PR was open when this was written).

## Executive Summary

- **A new clean-up proposal finds duplicate tracks.** Many multi-file books
  also exist in the library as separate one-file books, each one a copy of a
  single track. The existing clean-up only noticed those copies when their
  titles or file names looked like chapters. It now also recognizes a copy by
  its file fingerprint, whatever it is called, and proposes folding it into
  the multi-file book.
- **A fingerprint match alone is never trusted.** A copy is held back, with a
  stated reason, when a file is missing from disk, when the file is empty or
  the two files differ in size, when it is very short or looks like an intro
  or credits clip, when both entries point at the same physical file, or when
  two different books could be the parent. Held copies are listed but never
  changed.
- **Nothing the owner cares about is lost when a copy is folded in.** Listening
  progress, ratings, notes, and the book's places in collections and playlists
  move to the surviving book. Where the survivor already has a value, the
  survivor's value is kept.
- **Every step can be undone, in any order.** Undoing several clean-ups in any
  order, including the oldest first, puts each book back in the exact position
  it held in every collection and playlist.
- **If the owner rearranged a list by hand in the meantime, the undo says so.**
  It never quietly moves a book the owner placed.
- **A failure part-way cannot leave the records half-written.** The undo
  bookkeeping is written together with the list itself, and two undos running
  at once cannot trip over each other.
- Verified by running the real undo code against about 200,000 simulated
  histories, and by 15 deliberately injected faults that the tests caught.

## 1. Finding copies by file fingerprint

**What it was.** A one-file book that is really a copy of one track of a
multi-file book was only offered for clean-up when its name looked like a
chapter. Copies with ordinary titles stayed in the library as clutter and made
the same listening appear twice.

**Why it mattered.** Duplicates inflate the library, split listening progress
across two entries, and make the multi-file book look incomplete in search
and in the player.

**The fix.** The clean-up now matches a copy to a parent by file fingerprint.
It offers a copy only when exactly one other book owns a matching file and the
evidence holds up: both files exist, are not empty, are the same size, are not
the same physical file, and are not a short intro or credits clip. For very
large files the stored fingerprint covers only the start, the end and the size,
so the two files are compared in full before anything is proposed. Everything
that fails a check is shown as held, with its reason, and is never applied.

## 2. Carrying the owner's data onto the surviving book

**What it was.** Retiring a book moved its listening state but not its ratings,
notes, or its places in collections and playlists.

**Why it mattered.** A retire could silently drop a rating or remove a book
from a collection the owner had curated.

**The fix.** Every clean-up of this kind now carries ratings and notes (only
into fields the survivor left empty), and carries the book's place in each
collection and playlist onto the survivor. Each move is recorded so the undo
can reverse it, and the undo refuses to overwrite something the owner edited
since. A store that cannot reach collections or playlists refuses the retire
before writing anything.

## 3. Undo that restores exact positions, in any order

**What it was.** Putting a book back into a list by its old position number only
works when clean-ups are undone newest first. Any other order shifts the
numbers and the book lands in the wrong place. The situation gets harder when
several retired books share one surviving book, or when a surviving book is
itself retired later.

**Why it mattered.** A wrong position in a hand-ordered playlist, such as a
series in reading order, is a silent error: nothing fails, the order is simply
wrong afterward.

**The fix.** Each retire also records which books stood immediately before and
after the one it removed. The undo rebuilds the order from those records,
walking through books other clean-ups have taken out, so every book returns
beside its real neighbours whatever order the undos run in. The records of a
list are read from one dedicated index, so the undo always sees every record
for that list. Journal entries are stamped by a clock that never steps
backward, so a system clock correction between two clean-ups cannot reorder
the history. A surviving book that was removed from a list by hand and later
came back is treated as a fresh arrival, not as part of the earlier clean-up.

## 4. Never silently wrong when the owner edits lists by hand

**What it was.** If the owner moved, re-added or removed books in a list after
a clean-up, the old records no longer described the list.

**Why it mattered.** Acting on stale records could move a book the owner had
placed on purpose, with no sign that anything was overridden.

**The fix.** The undo checks the records against the list as it is now. A
surviving book found somewhere other than where the clean-up put it is left
where the owner placed it, and the undo says so. A position the records cannot
confirm, or records that contradict the list order, are reported, and the
book goes back by its recorded position number instead. A list the owner never
touched is never reported. A record that lacks the neighbour information is
refused with a reason and nothing is written.

## 5. Atomic bookkeeping and locking

**What it was.** Each undo wrote the list and then, separately, marked the
clean-up as undone. A crash or a second undo in between could leave the two
disagreeing, and a retry could act on a list that had changed.

**Why it mattered.** Disagreeing records are how a book gets restored twice or
left out for good.

**The fix.** The list and the "undone" mark are written in one step, each list
has its own lock while it is read and written, and a clean-up that is refused
takes back its record and its marker together.

## What the numbers show

The real undo code was run against a model of the journal, over random
histories with up to three surviving books sharing a list, crashes part-way
through an undo, and surviving books retired into other survivors:

- **140,000 histories** in the main suites: all 140,000 ended with exactly the
  original lists, none reported, none refused, none silent.
- **47,000 adversarial histories** (identical timestamps, a clock stepping
  backward, a record written before its marker, deep chains with crashes): all
  exact.
- **12,000 histories where the owner edited lists by hand afterward:** 5,716
  ended exact, 5,544 were restored with the deviation reported to the owner,
  740 were refused for manual review with a reason and wrote nothing, and
  **none ended wrong without being reported**. These suites report a lot by
  design, because a hand-edited list is exactly where the code refuses to
  guess.
- **15 of 15 injected faults** (for example, ignoring the order records,
  skipping the new index, letting the clock run backward, dropping a report)
  were caught by the tests. Two faults survived the first pass; tests were
  added for them and they are now caught as well.

## What the owner will see after deploy

- A **hash-copy cleanup preview** in the Repairs fragment-consolidation
  fixer: a plan listing the single-file copies proposed for retirement, each
  with the parent it would fold into, plus the held copies with their reasons.
  Nothing changes until the owner applies rows from that preview. The count is
  expected to be around 52 proposals; that figure is a prediction, and the
  preview will show the real number.
- After an apply, **Undo** on that operation restores everything it carried.
  If lists were changed by hand since, the undo result names exactly which
  positions were left where the owner put them or restored by recorded
  position.
- Retire operations (this fixer, duplicate copies, consolidation leftovers)
  now carry ratings, notes, collections and playlists from the retired book to
  the survivor.

## Limits worth knowing

- The index of list records is written for new clean-ups only. This feature is
  new in this release, so there is no earlier history to convert.
- The never-steps-backward clock fixes the order of entries at the moment they
  are written. It cannot repair entries already written out of order.
- The locking protects one running server process.
