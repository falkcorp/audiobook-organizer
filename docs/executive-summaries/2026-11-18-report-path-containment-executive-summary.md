<!-- file: docs/executive-summaries/2026-11-18-report-path-containment-executive-summary.md -->
<!-- version: 1.3.0 -->
<!-- guid: 0b6f2c1e-7a34-4d58-9e21-c5a8d3f40b97 -->
<!-- last-edited: 2026-11-19 -->

# Maintenance reports can only be written into the report folder

**Date:** 18 November 2026. A fix to the maintenance operations that write a
report file, found while reviewing the shape-report operation, and tightened the
same day after its review.

## Executive Summary

- Many maintenance operations write a report of what they found or did. By
  default it goes into a hidden reports folder inside the library. Each
  operation also lets the person starting it name a different file for the
  report, and that name was used as given.
- That meant anyone allowed to start one of these operations could name any file
  the server is able to write, and the operation would write its report over it.
  The report is plain text, so the damage depends on which file was named, but
  an overwritten file the server relies on would be lost.
- Now a report may only be a file directly inside the reports folder. A plain
  file name is placed there; a full path is accepted only if its folder is the
  reports folder. A name with a folder in it is refused: there are no subfolders
  of the reports folder any more, so nothing is created inside it either. A path
  that climbs out with "..", one that goes through a shortcut (a symbolic link)
  leading elsewhere, a report file that is itself such a shortcut, and a path
  that names a folder instead of a file are all refused. A path refused for what
  it names creates nothing, not even a reports folder that was missing; one
  refused only because of what already sits at that name (another account's
  file, a shortcut) can have caused the reports folder to be made first. With
  no library folder configured there is nothing to keep a named
  report inside, so a named report is refused.
- The check at the start of a run was not enough on its own, because the run
  checks the name first and writes the report later, and a shortcut can be put at
  the name in between. Every report is now also written in a way that cannot be
  redirected at that moment: the file is opened inside the reports folder
  without following a shortcut, and is used only if it is an ordinary file owned
  by the account running the server and with no second name pointing at it. A
  file that fails that test is left exactly as it was, not emptied, and the
  operation reports the refusal. This covers all the operations that write a
  report, the extra "decisions" file one of them writes beside its report, and
  the ".applied" file the series de-numbering writes.
- The provenance export, which appends a ledger of file history to a file the
  person names, had the same flaw and an unusual one: it accepted any full path,
  and it adds to a file instead of replacing it. It follows the same safe open
  now, and it gets a folder of its own, ".ledger" inside the library, instead of
  sharing the reports folder. A review caught why: every report operation
  empties the file it is given, and the cleanup job deletes by name, so a ledger
  kept among the reports could be wiped by naming a report after it, and the
  export would never refill it. Nothing that writes a report can reach the
  ledger folder, and a report named after the ledger lands in the reports
  folder as a different file. The export also refuses to run, on a preview too,
  when the library volume does not look mounted, so an unmounted share's empty
  mountpoint never receives the ledger, where it would be hidden once the share
  came back.
- The file-hash backfill job wrote its report with an older open that followed a
  shortcut in any folder above the file and did not notice a second name
  pointing at the file. It now uses the same safe open as the rest.
- What changes for the person using them: a report named outside the reports
  folder, or with a folder in its name, is now refused with a message that says
  so. A plain file name, or a full path whose folder is the reports folder,
  works. Before this tightening, a name with a folder in it, such as
  "mine/chosen.tsv", was accepted and the folder created; that no longer works,
  so use "chosen.tsv". Anyone who has been exporting file history to a file
  elsewhere needs to start a new ledger file inside ".ledger", and to tell the
  export so once: it remembers how far it got, and a file that does not hold
  the lines up to that point is refused (see below) unless the run is given
  "start_new". The new file then begins at that point, the gap is written into
  the result and as a marker line in the file, and the earlier lines stay in the
  old file, which should be kept with it. Only the account the server runs as
  can create, delete or rename files inside the ".ledger" folder: it is made
  that way, and a folder that already let the group or everyone write into it
  has that taken away. That does not protect the folder itself. Anyone who can
  write in the library folder can rename ".ledger" and let the next export make
  a new one. The export now notices: before it appends, it reads the end of the
  ledger file and refuses, writing nothing, if that file is empty or stops
  before the point the export remembers (renamed aside and recreated, deleted,
  replaced, cut short), until "start_new" is passed. A file that is a copy or a
  restore holding all the lines passes. A file that runs past the point is
  fine: that is what a run that was cut off just before it noted its progress
  leaves, and the next run repeats those lines. A ledger
  copied in must be an ordinary file owned by the account the server runs as
  and with a single name; one copied in with administrator rights belongs to
  the administrator and is refused until its owner is changed. The same test is
  now made when a report name is chosen, so an old file at a report's fixed
  name that another account owns stops that operation up front, with a message
  naming the file.
- Tests cover climbing out with "..", an absolute path elsewhere, a path with a
  folder in it, a shortcut inside the reports folder that leads out of it
  (whether its target exists or not), a report file that is a shortcut, a
  second name pointing at an existing file, a file owned by another account, a
  folder where the file should be, and a path that is accepted, a report named after
  the ledger, and an unmounted library volume. They run against
  the shared helper, against every operation that writes a report, and against
  every report-writing function on its own. The checking logic was also broken on
  purpose 21 ways to confirm the tests notice each break; all 21 were caught.

## What it was

A report path supplied by the caller was trusted. Only the default path, built
by the program, had its folder checked. After the first fix, the name was
checked when the run started but the file was opened later by name, which left a
short window in which a shortcut could be swapped in.

## Why it mattered

An operation started with a chosen report path could overwrite a file outside
the library's report folder, with report text, and the provenance export could
append to one.

## The fix

The caller's path is resolved first and accepted only if it is a file directly
inside the reports folder, before anything is created or written. When the file
is written, it is opened relative to the reports folder itself, without following
shortcuts, and checked as an ordinary single-name file owned by the server's
account before a byte is written.
