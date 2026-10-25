<!-- file: docs/executive-summaries/2026-10-25-repairs-listening-state-exposure-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0fc47f42-dde9-4526-955a-4bab97c8800e -->
<!-- last-edited: 2026-10-25 -->

# Other people's listening history is private again

- Pull request: [#4148](https://github.com/falkcorp/audiobook-organizer/pull/4148) (merge commit to be filled in when it merges)
- Follow-up: [#4151](https://github.com/falkcorp/audiobook-organizer/pull/4151), stacked on #4148

## Executive Summary

- **What could be seen.** Several pages and data feeds showed other people's
  listening history to anyone who could browse the library: which books each
  person had finished or started, how far they were, where they had stopped,
  and some private playlists. This came from records the library-repair tools
  and book merges keep so that their work can be undone.
- **Who could see it.** Any signed-in account with "viewer" access, which is
  the access every guest gets when they accept an invitation. Editors could
  see some of it as well.
- **Was anyone exposed?** No. When the fix was made, every account on the
  production server was an admin account, and admins are allowed to see this
  information anyway. No guest or viewer account existed that could have
  looked.
- **What is closed now.** Other people's listening history is now visible only
  to admins (accounts that may manage users), wherever it appears: repair
  plans, operation results and logs, each book's change history, the undo
  records of book merges, and the activity log. Editors keep every repair tool
  they could run before, including Undo; they just no longer see the
  listening details inside those records.
- **What the follow-up closes.** #4151 makes the Audible reading-status
  import an admin-only tool, and hides repair runs and their activity-log
  lines from people who may not use the tool that made them.
- **Guard for the future.** Every kind of change record the undo system can
  write is now labelled either "personal data" or "library data", and an
  automatic check fails the build if someone adds a new kind without
  labelling it.

## The repair tools' plan pages

**What it was.** The library-repair page lets an admin preview a repair
before running it. That preview, and the list of each tool's last runs, was
readable by anyone who could view the library. Two of the tools work on
people's reading status (the Audible reading-status import and the
master-list import), so their previews list other users' reading state.

**Why it mattered.** A guest could open the preview of a reading-status
repair and read what other members of the household had read.

**The fix.** A tool's preview and results now require the same permissions
as running that tool. The repair page still lists every tool, but it hides
the run details of a tool you may not use and says you lack permission.

## Operation results, logs and change history

**What it was.** Every repair or merge is recorded as an "operation", with a
result, a log and a change journal: a before-and-after record of each thing
it changed, kept so it can be undone. These were reachable through the
general operations pages, through each book's change history, and after an
operation was dismissed, because dismissing one removes its summary but keeps
its change journal. When a repair retires a duplicate copy of a book, the
journal records every user's listening position and progress on both copies.
Thousands of these entries were written on 1 and 2 October, before the
journal recorded which tool wrote each entry, so the first version of this
fix still let them through.

**Why it mattered.** Anyone who could view the library could list the
retired books and read every user's progress on each one from its change
history.

**The fix.** Each journal entry is checked on its own. Entries that hold
personal data (listening state and positions, private playlist membership,
or the name of the user who applied a repair) are shown only to admins. An
entry that does not say which tool wrote it is judged by the operation it
belongs to, while that record still exists; when it does not, personal
entries stay admin-only. The undo preview, which lists entries without their
contents, still works for anyone allowed to run the tool.

## Book-merge undo records

**What it was.** When two copies of a book are merged, the system keeps an
undo record containing every user's progress on both copies, before and
after. Two diagnostic pages listed these records to anyone who could view the
library.

**Why it mattered.** The same listening history was readable there in full.

**The fix.** Those pages still list each merge, its books and its status,
but remove the per-person progress, and any warning that names a person,
unless the reader is an admin.

## Activity log

**What it was.** When an admin discards the listening progress on a book in
the trash and deletes it, the activity log records a summary of each user's
progress that was thrown away. The activity log is readable by anyone who
can view the library.

**Why it mattered.** The summary names each person and how far they were.

**The fix.** That summary is removed from the activity log for everyone but
admins, matching what the trash page already did.

## Still open

- Smart collections that filter on reading status ("finished", "in
  progress") are worked out from the reading history of the person who
  created the collection, and every account can see which books are in a
  collection. A viewer can therefore infer that person's reading status. No
  listening data is shown directly; the owner is deciding whether this should
  change.

Verified with automated tests: for each route, a viewer and an editor are
shown none of the personal data and an admin is shown all of it. Each test
was also run with its fix taken out, to confirm it then fails.
