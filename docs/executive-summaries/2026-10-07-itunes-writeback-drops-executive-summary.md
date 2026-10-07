<!-- file: docs/executive-summaries/2026-10-07-itunes-writeback-drops-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4b9e7d21-3f6a-4c58-8e1b-a2d5c7f09e63 -->
<!-- last-edited: 2026-10-07 -->

# iTunes changes were silently thrown away (2026-10-07)

## What went wrong

When you edit a book, the organizer is supposed to copy the change (title,
author, file location, removals) into your iTunes library.

Since iTunes was switched to the organizer's own copy of the library, every one
of those updates had been refused by a safety check. That check mistook the
library's own music folder for a leftover temporary folder. After three refusals
the organizer simply threw the change away, and wrote only a log line nobody
sees.

- The logs that still exist (from 23 September on) show **662 batches thrown
  away**: about **4,300 book updates** and **one track removal**.
- The problem very likely started earlier, when the switch happened (iTunes last
  saved that library on 28 July), but older logs are gone, so the earlier losses
  cannot be counted.
- For removals it was worse: the organizer recorded the track as removed before
  even trying, so the database said "gone" while iTunes still had it.

## What changed

- The safety check now recognises the organizer's own library folder, and
  nothing else, so real updates go through and real mistakes are still caught.
- Updates waiting to go to iTunes are now saved in the database, so a restart no
  longer loses them.
- An update is **never thrown away** any more. If writing fails, it waits and
  tries again: first after a minute, then gradually less often, at least once an
  hour.
- A removal is recorded only after iTunes has really been changed.
- A new status endpoint in the API (`GET /api/v1/itunes/writeback/status`; it
  has no screen in the web app yet) shows how many updates are waiting, whether
  the last attempt failed and why, and when it will try again.
- If something asks to remove an unusually large number of tracks at once, those
  removals are put on hold for you to approve instead of being discarded.
- Backups of the iTunes library are now made only before a change that will
  really be written. Failed attempts were filling the five backup slots with
  identical copies and pushing out older, useful ones.
- A separate bug that stopped any large removal (more than about 27 tracks)
  from ever being written was fixed too.

## What you need to do

- **Close iTunes on Windows before this is deployed.** This fix turns iTunes
  writing back on for the first time since July. If you want a cautious start,
  deploy with `write_back_dry_run=true`, look at the status endpoint and the
  log, then turn dry-run off. Nothing is lost by doing this: dry-run now keeps
  the waiting updates.

- The lost updates cannot be recovered from the logs, because the logs never
  listed which books they were.
- They can be regenerated: after this is deployed and with iTunes closed on
  Windows, preview the full database-versus-iTunes difference
  (`POST /api/v1/itunes/rebuild?dry_run=true`) and re-send the changed books.
- The one lost removal came from merging two copies of a book on 6 October
  (book `01KXXVAF46CM0NWZMSA44X1XTB` into `01KNDBXRC0N6WPTVZ6RHPSF2HH`). That
  track is most likely still in iTunes.
