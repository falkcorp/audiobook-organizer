<!-- file: docs/executive-summaries/2026-09-11-the-cleanup-that-was-never-switched-on-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: d30afc65-4d03-431b-881b-c3c068c03422 -->
<!-- last-edited: 2026-09-11 -->

# The cleanup that was never switched on

**Pull request:** https://github.com/falkcorp/audiobook-organizer/pull/3221

## Executive Summary

When the app files a book onto disk it builds the folder name from a template
such as `{author}/{series}/{title}`. Early on, a bug let a book be filed before
every blank in that template had been filled in, so a handful of books ended
up with folder names that literally read `{series}` or `{author}`. Those paths
are wrong, and nothing downstream can make sense of them.

A one-time cleanup was written in July to find every book in that state and
put it in the **needs review** pile so a person could fix it. The cleanup was
never switched on. Two things went wrong at once:

- **The switch was wired to the wrong database.** The cleanup had a version for
  a database engine this app no longer uses, and a separate version for the one
  it actually runs on. Only the first was connected. The second sat in the code
  with a note saying "not yet hooked up" and a marker that told the code checker
  to stop complaining that nothing called it.
- **The app already believes the cleanup ran.** Every upgrade step is recorded
  once it finishes, and the app never repeats a recorded step. The disconnected
  step still ran — it just did nothing — and was recorded as done. So simply
  connecting the missing code would not have helped: the app would have skipped
  it as already applied.

The fix registers the cleanup as a brand-new upgrade step, one the app has not
seen before, so it runs on the next start. Step 14 stays exactly as it was,
with a note explaining why it must. The cleanup now announces how many books it
is checking and, at the end, how many it flagged, how many were already
flagged, and how many it could not read, so a long pass is visible in the logs
instead of looking like a hang. Books it has already flagged are left alone on
any later run, and the author and series attached to each flagged book survive
the change — both proven by tests that run the cleanup on its own and through
the same start-up path production uses.

What to expect on the next deploy: one pass over the whole library at start-up
(seconds, not minutes, on production's size), and any book whose folder name
still contains a `{...}` blank appears in the review queue. Books filed since
the original bug was fixed cannot get into this state, so the pass is a
one-time catch-up, not a recurring cost.
