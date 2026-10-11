<!-- file: docs/executive-summaries/2026-10-11-database-close-crash-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4b9e2d17-8a3c-4f61-b0d5-9c7e1a2f6b38 -->
<!-- last-edited: 2026-10-11 -->

# Shutting down during a long database read no longer crashes the app

Branch `fix/pebble-close-races-reconcile`. Found from a test that failed now and then on automated testing (CI).

## Executive Summary

- **What went wrong.** When the app shuts down or restarts, it closes its database. Background work may still be reading at that moment: a library scan, the start-up check of the operations history, a statistics refresh. The database engine (Pebble) does not wait for those reads before closing. When a read finished after the close, the engine's internal bookkeeping went negative and the whole process crashed.
- **Why it mattered.** A restart during any long read could end in a crash instead of a clean stop. The app checks its operations history at every start-up, so a quick restart right after a deploy was the most likely way to hit it. Because the crash happened during shutdown, the logs showed an engine panic rather than an orderly exit. That makes it look like something much worse than a timing problem.
- **The fix.** All database access now goes through one gatekeeper that counts what is in progress. Shutdown stops letting new work in, waits for the work already running to finish, and only then closes the database. New requests that arrive during shutdown get an ordinary "database is closing" error, not a crash.
- **Bounded wait, without losing saved changes.** Shutdown waits at most 10 seconds. If something is still reading after that, the app leaves the database open and exits instead of closing it under the read. Some changes are saved in a fast mode that keeps the newest journal entries in the app's memory until something forces them to disk, and closing the database is normally that something. So shutdown now forces the journal to disk itself: once as soon as it starts, and again when the wait ends, whether or not the wait timed out. Every change that finished saving before then survives the exit and is replayed from the journal the next time the database opens. One gap remains, and only when the wait times out: work that was still running at that moment can finish a fast-mode save after the last forced write, and that save can be lost if the app exits straight away.
- **Writers can still finish saving during shutdown.** Some writes save in the fast mode and then ask for one final "force to disk" step. That step is now allowed while shutdown waits, so a write that started before shutdown can still make itself durable. Before this, the step was refused and the write reported that its durability was unknown.
- **Long checks stop early.** The start-up history check now notices that shutdown has begun and stops, so a restart does not wait for it to finish.
- **One small leak fixed along the way.** Two listening-statistics lookups opened a database read and never closed it. Under the new gatekeeper an unclosed read would make shutdown wait the full 10 seconds, so both now close it.
- **Verified.** New tests hold each kind of database read open across a shutdown, and simulate a power cut right after a shutdown whose wait timed out, to check that a change saved just before it is still there. Before the fix, the database reported the read as "leaked" every time; after it, every case passes. The test that originally failed passed 200 runs in a row, and 600 more across one, two and eight processor cores.

## Details

### Shutdown closed the database under running reads

- **What it was.** Closing the database released memory that in-progress reads were still using. Each earlier fix for this kind of crash stopped one specific background task before closing: the start-up memory warm-up, the dashboard statistics refresh, the duplicate-detection backfill. Any other task reading at the wrong moment was still exposed.
- **Why it mattered.** Any shutdown or restart could crash the process if it landed during a long read. Writes that were partway through when the database closed were exposed too.
- **The fix.** Every read, write and maintenance call now registers with the gatekeeper for as long as it uses the database. The other stores that share the main database (duplicate candidates, activity log, metrics, AI scan results, catalog) use the same gatekeeper, so shutdown waits for them too. Registering never blocks, so a task that reads inside another read cannot deadlock against a shutdown that is waiting.

### Refused work returns an error instead of crashing

- **What it was.** Before, any use of a database that was already closed crashed the process, except in a few code paths that caught that crash.
- **Why it mattered.** Late background work during shutdown could still take the process down.
- **The fix.** Once shutdown begins, new database calls return a "database is closing" error that the app's existing error handling already logs. Reads already in progress run to completion; they are never cut short. Cutting a read short could have led code to act on an incomplete list.
