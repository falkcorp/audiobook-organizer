<!-- file: docs/executive-summaries/2026-08-24-the-id-that-belonged-to-nobody-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 97c364c3-1b69-4dc6-950e-112d84ca6329 -->
<!-- last-edited: 2026-08-24 -->

# The ID that belonged to nobody

**2026-08-24 — handing an iTunes ID from one file to another let go with one hand
before the other hand had closed**

**PR:** `fix/pid-transfer-atomicity` — the fix, four tests, and this report.
**Follow-up to:** the batch-create work merged earlier the same day, which is where
the flaw was introduced into a second code path and where the incorrect claim of
safety was written down.

---

## The short version

- Every audio file we track can carry an **iTunes persistent ID** — the identifier
  iTunes itself uses to recognise a track. It is the thread that lets the app and
  iTunes agree they are talking about the same recording.
- That ID has to belong to exactly one file at a time. When the app creates a new
  file record that should own an ID currently held by an older record, it **hands
  the ID over**: clear it from the old record, set it on the new one.
- The hand-over was done in two separate saves. Clear the old record and save.
  Then write the new record and save. Two saves, not one.
- If anything went wrong between those two saves, the first had already stuck. The
  old record had given the ID up, and the new record was never written. **The ID
  now belonged to nobody**, and nothing reported a problem.
- Both saves are now a single save. The ID moves across, or it does not move at
  all. There is no longer a moment in between.

## What was actually at risk

Losing that ID does not delete a book or a file. What it breaks is the app's
ability to recognise the track in iTunes. A track whose ID has gone missing looks
like a track we have never seen, so the two-way sync can no longer match it up,
and a later pass may treat it as new.

Being straight about the odds: the gap only opens if a save fails partway — a full
disk, a disk error. That is not an everyday event, and **we have no evidence it
happened**. This is a fix to a window that should never have been open, not a
cleanup after known damage.

## The part worth being uncomfortable about

The flaw existed in two places, and the ordering was the opposite of what we
assumed.

- The **older** path, which creates one file record at a time, is the one that
  genuinely runs with these IDs. Its callers copy an ID onto a new record on
  purpose — that is the whole reason the hand-over exists. This was the live one.
- The **newer** path, added the same morning, carried a comment stating plainly
  that "on any error nothing is committed." That was **not true when it was
  written**. It was only harmless because the single feature that uses that path
  builds its records without iTunes IDs at all, so the hand-over never ran.

So the code that looked safe was the one with the reassuring comment, and the
comment was wrong. Nothing checked whether the sentence was true; it was simply
believed, including by the person who wrote it. The comment now records what it
used to claim and why that claim was false, so the next reader inherits the
correction rather than the confidence.

## Two tests that were not testing anything

Both of these passed the entire time the flaws were present.

- A test named for checking that a rejected batch **"must leave nothing behind"**
  was set up with no older record holding the ID in the first place. With nothing
  to hand over, the hand-over never ran, so the test confirmed cleanliness in the
  one situation that was already clean. It has been given a proper setup, and its
  comment now says out loud what it does *not* cover.
- A test named **"...AndAggregatesOnce"** checked that a book's total size came out
  correct. It never checked the "once". Doing the work three times produces exactly
  the same total as doing it once, so the wasteful version passed too — measured at
  three, green. It now counts.

Both were confirmed the only way that counts: the flaws were deliberately put back,
and each test was watched to fail. Every other test in those files stayed green
while it did, which is precisely the point — they could not see it.

## What changed

| Before | After |
| --- | --- |
| ID hand-over saved separately from the record claiming it | One save, all or nothing |
| A safety claim in a comment that was untrue | The claim is true, and its history is recorded |
| A rollback test with nothing to roll back | A setup that has something to lose |
| A "did it once" test that only checked "was it correct" | It counts |
