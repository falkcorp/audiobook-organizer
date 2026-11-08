<!-- file: docs/executive-summaries/2026-11-08-hnsw-clean-stop-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 42dae9c5-6b36-4000-91ca-af79a8cacdbf -->
<!-- last-edited: 2026-11-08 -->

# The duplicate finder's index no longer stays wrong after a crash

**Date:** 8 November 2026
**Pull request:** [#4476](https://github.com/falkcorp/audiobook-organizer/pull/4476) (merge commit: pending), a follow-up to [#4467](https://github.com/falkcorp/audiobook-organizer/pull/4467)

## Executive Summary

- The duplicate finder keeps a similarity index: a structure that finds books whose text is close in meaning. To avoid rebuilding it at every startup, the server saves a copy of it to disk and loads that copy next time.
- Startup decided whether the saved copy was still current by comparing one number, how many book fingerprints the database held. A crash could change the fingerprints without changing that number, and the index then stayed wrong until something rebuilt it. It said nothing, and duplicate checks used the stale index meanwhile.
- A clean shutdown now leaves a small "stopped cleanly" note beside the saved copy, and only when the copy matches the index exactly. Every startup removes the note. A startup that finds no note brings the copy up to date against the database, whatever the count says.
- A save that fails part way through no longer counts as a good save, so the note is never written for a copy that is half old and half new.
- The short-lived worker processes that run a single task load the saved copy as it is and leave the note alone. They no longer repeat the full check against the database for every task.
- The documented shutdown time limit was wrong (30 seconds). The real limit in production is 90 seconds, and the comments and the sample setup file now say so.
- Each new safeguard has a test that was confirmed to fail when that safeguard is removed: 13 of 13.

## A crash could leave the index wrong

**What it was.** Each book's text is turned into a numeric fingerprint, called an embedding, and stored in the database. The similarity index is built from those fingerprints. When the server stopped, or every ten minutes while it ran, the index was saved to disk together with the number of fingerprints the database held at that moment. At startup, if the database still held that many, the saved index was taken as current.

**Why it mattered.** A crash can land after the database has changed and before the index on disk catches up. Some of those changes leave the count exactly where it was: one book fingerprinted and another deleted, a book fingerprinted again after its text changed, or two editions merged so that a different one becomes the main copy. The saved index then loaded as current and was never corrected. The duplicate finder would miss real duplicates, offer books that had been deleted, or compare against an outdated fingerprint, with no warning anywhere.

**The fix.** At the very end of a clean shutdown, the server writes a "stopped cleanly" note, but only if the saved index is exactly the index it held in memory. It forces the note onto the disk before it counts it as written. If anything changes the index after that, the note is removed straight away. Every startup removes the note before loading anything. Without the note, startup checks the saved index row by row against the database and repairs what differs. With the note, the count is still checked as before, as a second safeguard. A startup that does not use this index at all also removes the note, so it cannot later vouch for a copy that fell behind.

## A half-finished save counted as a good one

**What it was.** Saving the index replaces several files one after another. If a later step failed, some files were already new while the rest were still old, yet the server still treated the last good save as the one on disk.

**Why it mattered.** The server could then report that nothing needed saving and skip the save that would have repaired the files. With the new note, it could also have vouched for a copy that no longer matched anything.

**The fix.** A save that fails after it has begun replacing files now marks the copy on disk as not trusted. The next save writes a complete copy, and no "stopped cleanly" note is written until it does.

## Worker processes would have repeated the check for every task

**What it was.** Some long tasks run in a separate, short-lived worker process. Each worker starts up like a small server and loads the saved index. The main server removes the note at its own startup, so a worker would never find one.

**Why it mattered.** Every worker would have checked the whole index against the database before doing its task. That repeats a full pass over the database for each task, while the main server is running.

**The fix.** A worker now loads the saved copy as it is and leaves the note alone. A worker can never save the index, so anything its copy lacks disappears with the worker. The main server's next startup still makes its own check.

## The shutdown time limit was documented wrong

**What it was.** Three code comments and the sample service settings said the system gives the server 30 seconds to shut down before force-stopping it. The service file in the repository sets no limit, and production's own settings give it 90 seconds.

**Why it mattered.** Shutdown timing decisions were being justified against a limit that does not exist. One of them was a hand-off to the work on shutdown settings, which concluded that the server overran the limit. Against the real 90 seconds it does not.

**The fix.** The comments now say where the limit comes from without giving a number. The sample settings file uses 90 seconds and explains what that time has to cover.
