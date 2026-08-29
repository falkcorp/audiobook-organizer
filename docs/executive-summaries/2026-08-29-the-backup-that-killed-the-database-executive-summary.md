<!-- file: docs/executive-summaries/2026-08-29-the-backup-that-killed-the-database-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: ee4c032c-631c-46f1-b1ee-9ab49e5ce767 -->
<!-- last-edited: 2026-08-29 -->

# The backup that killed the database

**2026-08-29 — the safety copy was written onto the same disk as the thing it was
protecting, filled it, and took the app down every seventeen minutes for most of a
night**

**PRs:** `#2953` (the disk guard), `#2954` (the review queue losing books)

---

## The short version

- Every time the app tidies your library, it first takes a **safety copy of its
  database** — sensible, and it had worked for months.
- The safety copy was written **onto the same disk the database itself lives on**.
  In April a copy was 247 MB and nobody would have noticed. By August the database
  had grown and each copy was about **15 GB**.
- The app was told to keep the **last ten copies**. Ten copies at 15 GB each is
  **150 GB — on a 141 GB disk**. The rule had quietly become impossible to satisfy.
- So the disk filled completely. The database could not write, and the app shut
  itself down. The system restarted it, it tried the same backup again, and it died
  again — **every seventeen minutes, for hours**.
- **This is why new books were not showing up.** Not a broken scanner: the scan was
  being killed part-way through, over and over, before it could finish.
- Two saved passwords were also corrupted, because they were being written at the
  moment the disk ran out. They have to be typed in again.

## What was actually wrong

The rule said *keep ten copies*. It never said *don't fill the disk*. Those are the
same instruction only while the copies stay small, and nothing was watching the
size. The rule was followed exactly, and following it was what caused the outage.

Worse, the outage was self-sustaining. The app only skips a backup if a **recent
successful one** exists. A backup that fails leaves nothing behind, so every restart
concluded a backup was overdue and tried the same doomed 15 GB write again.

## What changed

- The app now **checks there is room before it starts**, and refuses the backup
  rather than filling the disk. A backup must never be able to destroy the database
  it exists to protect.
- Old copies are now **cleared out before the new one is written**, not after — the
  old order meant that when the disk was full the write failed, so the cleanup that
  would have freed the room never ran.
- There is now a **limit on total size**, not just on the number of files, so the
  rule cannot quietly become impossible again as the database grows.

## The second problem, found on the way

While looking into a separate complaint — *when I apply metadata to a batch of
books, the rows don't disappear* — a more serious bug turned up underneath it.

When you apply metadata to a batch, the books vanish from the review queue straight
away, and the actual work happens in the background. If that background work then
**failed** on some of those books, they were supposed to come back so you could deal
with them. They never did. The queue could only ever *add* notes about a row, never
correct one, and applying had already noted every book as done. So a book that
failed to apply **stayed hidden behind the default filter, permanently, with nothing
to tell you**. The queue simply looked finished. One click could strand hundreds of
books this way.

Fixing that turned up a third issue in the same area. The word "rejected" was being
used for two different things: *the server found no match* and *a person rejected
this*. They look identical but need opposite handling — the first should be
correctable, the second should be respected. The queue now keeps track of which
notes it wrote itself and which ones came from you, so it can correct its own
without overwriting your decisions.

That mattered in a place nobody would have guessed: the **Search** button, the only
way to fix a book the automatic matching gave up on. The apply worked, but the queue
then discarded the answer and the book disappeared with no sign it had succeeded.

## Where things stand

- The app is **stable again** and the library scan is running to completion rather
  than being killed part-way.
- The three oldest database copies — the deepest history, and the ones an
  oldest-first cleanup would have deleted first while freeing almost nothing — were
  **copied to separate storage** before any cleanup ran.
- **Two passwords still need re-entering in Settings** (`basic_auth_password` and
  `hardcover_api_token`). Nothing else can restore them; they were corrupted on
  disk, not merely forgotten.

## The lesson worth keeping

A limit expressed as *a number of things* cannot protect a *finite amount of space*
once the things can grow. `MaxBackups: 10` never malfunctioned, never logged a
warning, and never failed a test — it did exactly what it was told, right up to and
including taking production down. The bound has to be written in the same units as
the resource it is protecting.
