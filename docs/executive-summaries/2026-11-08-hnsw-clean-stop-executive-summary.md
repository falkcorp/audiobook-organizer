<!-- file: docs/executive-summaries/2026-11-08-hnsw-clean-stop-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 42dae9c5-6b36-4000-91ca-af79a8cacdbf -->
<!-- last-edited: 2026-11-08 -->

# The duplicate finder's index no longer stays wrong after a crash

**Date:** 8 November 2026
**Pull request:** [#4476](https://github.com/falkcorp/audiobook-organizer/pull/4476) (merge commit: pending), a follow-up to [#4467](https://github.com/falkcorp/audiobook-organizer/pull/4467)

## Executive Summary

- The duplicate finder keeps a similarity index: a structure that finds books whose text is close in meaning. To avoid rebuilding it at every startup, the server saves a copy of it to disk and loads that copy next time.
- Startup decided whether the saved copy was still current by comparing one number, how many book fingerprints the database held. A crash could change the fingerprints without changing that number, and the index then stayed wrong until something rebuilt it. It said nothing, and duplicate checks used the stale index meanwhile.
- A clean shutdown now leaves a small "stopped cleanly" note beside the saved copy. A startup that finds no note checks the copy against the database and repairs what differs, whatever the count says.
- The note is written only when the saved copy matches the index in memory **and** nothing during the run changed the database records the index is built from. The first version of this change checked only the first half. A reviewer showed that a merge saved to the database but never applied to the index still left a note, so the next startup trusted a wrong index. A test now reproduces that case and fails without the fix.
- The note is the very last step of a shutdown, and is skipped when any shutdown step was still running or failed.
- Any program that opens the database removes the note, so a maintenance tool run between a shutdown and the next start cannot leave it vouching for records it changed.
- The note records which fingerprint model the index was built with. A startup with a different model repairs the copy.
- Each safeguard added in the second round has a test that was confirmed to fail when the safeguard is removed: 72 of 72. The first round reported "13 of 13", but three of its safeguards had no such test; they have one now and are part of the 72. One change has no such test, because reaching it needs a disk failure: the clean-up after a note that could not be fully written.
- Not covered: a tool that edits those records without going through the server's database layer (none does today), changes made by a separate worker process (no task runs in one today), and the index holding secondary editions that a startup leaves out (logged as a to-do, with a recommendation).

## A crash could leave the index wrong

**What it was.** Each book's text is turned into a numeric fingerprint, called an embedding, and stored in the database. The similarity index is built from those fingerprints, and it leaves out some of them: secondary editions of a book, books that no longer exist, fingerprints from an old model. When the server stopped, or every ten minutes while it ran, the index was saved to disk together with the number of fingerprints the database held at that moment. At startup, if the database still held that many, the saved index was taken as current.

**Why it mattered.** A crash can land after the database has changed and before the index on disk catches up. Some of those changes leave the count exactly where it was: one book fingerprinted and another deleted, a book fingerprinted again after its text changed, or two editions merged so that a different one becomes the main copy. The saved index then loaded as current and was never corrected. The duplicate finder would miss real duplicates, offer books that had been deleted, or compare against an outdated fingerprint, with no warning anywhere.

**The fix.** At the end of a clean shutdown, the server writes a "stopped cleanly" note and forces it onto the disk. Every startup removes the note before loading anything. Without the note, startup checks the saved index against the database and repairs what differs. With the note, the count is still checked, as a second safeguard.

## The first version of the note could vouch for a wrong index

**What it was.** The first version wrote the note whenever the saved copy matched the index in memory. But the index in memory can itself fall behind the database. A merge that makes a different edition the main copy changes the database and never touches the index. So does a cleanup task that deletes old fingerprints. The note then promised that the copy was current when it was not.

**Why it mattered.** This is the exact failure the note was meant to prevent, now with a note saying all was well. A reviewer reproduced it: after such a merge, the next startup trusted the copy and kept offering the merged edition as a main copy.

**The fix.** Every change to the database passes through one layer, and that layer now notices a change to any record the index is built from: a fingerprint, or a book's existence, its main-copy flag or its series details. Editing a book's other details does not count. If any such change happened since the server started, no note is written, and a change made after the note was written removes it at once. The note also records the fingerprint model, and the index is marked as out of step when it refuses a fingerprint or fails to apply one, which also stops the note.

The cost: any run that fingerprinted a book, merged editions or cleaned up fingerprints leaves no note, so its next startup repairs the copy. The note saves that repair only after a quiet run. The repair runs in the background and does not delay startup.

## Shutdown wrote the note too early

**What it was.** The note was written partway through shutdown, before the server had waited for its scheduled tasks to finish, and even when an earlier step had been abandoned for running too long.

**Why it mattered.** A task still running could change the database after the note was written.

**The fix.** The note is now the last step, after every wait. If any step was abandoned or failed (background tasks, file transfers, scheduled tasks, the operations queue, the shutdown of the server's services), no note is written, and the log names the steps.

## Maintenance tools did not remove the note

**What it was.** Only the server's own startup removed the note. A command-line tool that opened the database between a shutdown and the next start left the note in place.

**Why it mattered.** Such a tool can change the records the index is built from, and the server would not see those changes.

**The fix.** Any program that opens the database removes the note. Running a maintenance tool between a shutdown and the next start therefore costs that start one repair. A note that cannot be deleted is emptied instead, and an empty note counts as no note.

## A half-finished save counted as a good one

**What it was.** Saving the index replaces several files one after another. If a later step failed, some files were already new while the rest were still old, yet the server still treated the last good save as the one on disk.

**Why it mattered.** The server could then report that nothing needed saving and skip the save that would have repaired the files, and the note could have vouched for a copy that no longer matched anything.

**The fix.** A save that fails after it has begun replacing files now marks the copy on disk as not trusted. The next save writes a complete copy, and no note is written until it does.

## Worker processes

**What it was.** Some long tasks can run in a separate, short-lived worker process, which starts up like a small server and loads the saved index. The main server removes the note at its own startup, so a worker would never find one.

**Why it mattered.** Requiring the note would make every worker check the whole index against the database before doing its task.

**The fix.** A worker leaves the note alone and checks only the count, so a copy whose count moved is still repaired in the worker. A worker can never save the index, so anything its copy lacks disappears with it. No task runs in a worker process today, so this guards a path that is not in use.

## The shutdown time limit was documented wrong

**What it was.** Three code comments and the sample service settings said the system gives the server 30 seconds to shut down before force-stopping it. The service file in the repository sets no limit, and production's own settings give it 90 seconds.

**Why it mattered.** Shutdown timing decisions were being justified against a limit that does not exist. One of them was a hand-off to the work on shutdown settings, which concluded that the server overran the limit. Against the real 90 seconds it does not.

**The fix.** The comments now say where the limit comes from without giving a number. The sample settings file uses 90 seconds and explains what that time has to cover.
