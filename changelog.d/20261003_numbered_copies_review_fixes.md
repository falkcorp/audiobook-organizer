### Fixed

- **Renamed chapter copies no longer make two books, whatever order they were
  imported in.** Each chapter keeps the file whose name the most files in the
  folder share, so a copy that holds the lower book id no longer pulls the
  set apart. When the kept files still disagree, the folder is held for review
  instead of being split into two books with the same audio. A disc folder
  that holds renamed copies is held as a whole for the same reason.
- **A copy with an iTunes id holds the row instead of stopping it halfway.**
  Copies are now checked for iTunes ids at plan time, and again before the
  first write, so a refusal writes nothing.
- **A numbered-set repair cut off after its retitle now resumes.** The book
  path and title are now set after every book is retired. A survivor that
  already carries the planned title is judged by the title it had when the
  plan was made.
- **Same-size copies must also have the same hash when both hashes are known.**
  The author, series, ASIN and title checks now cover copies too, and so does
  the co-owner check. An author such as "Jane Author (Narrator)" in a folder
  with that exact name is now treated as an author folder, as "Jane Author" is.
- **Copies whose names put them in another group no longer make a second
  book.** When two rows from one folder hold the same chapters (same position,
  same size, no conflicting hash), both rows are held and each names the other.
- **A numbered-set repair cut off partway now resumes when the books were
  organized.** The plan records each book's organized and primary flags, so
  books already retired are judged as they were planned. A book that something
  else retired into another book stops the row instead of being moved onto the
  survivor.
- **Three files at one chapter must all agree.** Every pair must match on size
  and on any known hash, not just each file against the first. Only a role in
  brackets, such as "(Narrator)", is set aside when testing whether an author
  name looks like a person; "(Unabridged)" or "[Book 3]" is not.
- **A cut-off numbered-set repair resumes whatever the apply changed on the
  way.** The plan now stores its survivor and which file it kept for each
  chapter, and a resume uses those instead of choosing again from the
  primary and organized flags that the apply changes as it retires books. A
  retired book counts as done only when this repair retired it into the
  survivor. A test cuts the repair at every single write and checks each
  resume reaches the same end state.
- **A resume still notices outside changes, and reads the job log once.** A
  book whose organized or primary flag changed since the plan stops the row,
  unless the repair's own earlier run made that change. Who retired a book is
  read from the repair's own job log, which is written before each change, so
  a crash right after a change cannot strand the row. That log is now read in
  one pass per resume instead of once per book (measured: 0.4 s instead of
  about 99 s for a 346-file set with 300 books already retired).
- **A cut-off repair whose job was cleared from the job list still resumes.**
  Each job-log entry a Repairs fixer writes now names the fixer itself.
  Clearing a failed or interrupted job deletes the job but keeps its log, so
  the repair used to stop recognising its own earlier work when the original
  plan was resumed. An entry from another fixer is never counted as this
  repair's, even if its job claims otherwise. Old entries without the name are
  still judged by their job; if that job is gone, the row is planned again.
- **A new plan made after a cut-off chapter repair now finishes that repair
  instead of splitting the book in two.** Before, a fresh plan saw the
  half-built book as an ordinary book and grouped the remaining chapters
  without it, around a different book; applying that row left two live books
  for one work (tested: 36 of 122 cut points with no version group, 10 of 122
  in the shape prod has). Each run of a chapter repair now writes its plan
  into the job log before its first change. A later plan finds that entry and
  continues the run with the same surviving book, the same chapters and the
  same plan time, so the end state is the same as a run that was never cut,
  and resuming the original plan afterwards changes nothing. When the run
  cannot be continued (two surviving books chosen for one folder, the
  surviving book retired or changed, a missing file), the row is held for
  review and names the books. A cut-off run that left no plan entry (older
  code) also holds every new row for that folder rather than guess. A test
  now cuts the repair at every single write and makes a fresh plan at each
  point.
- **A changed primary flag is credited only to this row's own runs.** A
  member's primary flag counts as this repair's change only when a run of
  THIS row made it: a run whose plan entry names this row and its plan time.
  A run of a different plan of the same folder no longer counts, even if it
  ran after this plan was made. A flag raised or lowered by a hand-off counts
  when a hand-off note names the crowned member. The note may come from this
  row's run, or from another repair that finished the hand-off this row's
  demote left owed. In the crash window, the crown is written but its note is
  not. There the flag counts only on the member that the hand-off would
  crown, worked out again from the group as it stood before the hand-off. A
  crown that anyone else gave to another member stops the row. A crown given
  to that same member is accepted, because it leaves the state the repair
  would leave. The surviving book's own flag is now judged the same way: it
  can be crowned or demoted by the row's own hand-off when it shares a
  version group with a retired member.
- **A failed primary hand-off now stops the row.** When retiring a group's
  primary could not hand the group to another member, the row used to carry
  on. The group was left without a primary and no note was written. The
  error is now returned, so the row reports partially applied and its resume
  makes the hand-off. This applies to the duplicate-copies repair as well.
- **A group whose only primary is an unset flag no longer skips an owed
  hand-off.** When a retire demoted a group's primary and was cut off before
  the hand-off, the resumed run counted a member with an unset primary flag
  as the group's primary and stopped there. An uninterrupted run would have
  chosen a primary explicitly, and sometimes a different member. The resume
  now checks the job log in that case and makes the same hand-off. This
  applies to the duplicate-copies repair too, and costs one job-log read for
  such groups only.
- **A cut-off chapter repair stays held when its surviving book is merged
  away.** If another repair, a dedup merge or a user merge folded the
  surviving book into some other book, a new plan used to regroup the
  remaining chapters around a new survivor and split the work again (tested:
  68 of 120 cut points in the shape prod has, 106 of 120 with no version
  group). The repair's plan entry now holds the folder in that case too. The
  held row says which book the survivor went into, by whom, and names the
  repair's own job(s) to resume or revert.
- **Finishing a cut-off repair by hand now clears its hold.** When every book
  of the cut-off repair ends up merged into one live book and every chapter
  file sits on a live book, the repair counts as done whoever did the
  merging. Reverting the cut-off repair afterwards no longer moves a file
  back onto a book that someone else retired since, where it would vanish
  from view. The revert refuses that file and says so; it stays on the book
  that holds it.
- **The plan entries are never aged out of the job log.** The 90-day job-log
  cleanup used to delete them, which would have let a still-unfinished repair
  be planned afresh. They are one entry per repair run.
- **A plan limited to some books still respects a cut-off repair of their
  folder.** The folder is held even when the cut-off repair's own books are
  outside the plan's scope.
- **Resuming a repair reads its books' job-log entries through the new
  per-book index** once that index is trusted, instead of reading the whole
  job log twice per row (once at the check, once under the merge lock).
- **The plan entry no longer shows in a book's change history.** It records
  no change to the book. It is still listed under the repair job's own
  changes. The version-group hand-off note now reads "Version group primary
  handed to <book>" instead of raw values.
- **Reverting a cut-off chapter repair no longer drops its hold while the
  revert is incomplete.** The repair's plan entry used to be marked undone by
  any revert of the job, even one that could not put the files back (another
  repair had moved them on). The next plan then split the work again (tested:
  22 of 24 cut points with organized books, 24 of 24 without). The plan entry
  is now marked undone only when every other change of the job was undone.
  A job whose only remaining entry is the plan (the rest aged out of the job
  log) is refused with an error that says nothing can be restored, instead of
  being reported as reverted.
- **A held row now offers only the actions that clear it, and names the
  right book.** When the surviving book was merged into another book, the
  row says to merge the remaining books into that other book, not into the
  retired survivor, and it no longer suggests a resume that cannot work. A
  survivor retired by a duplicate merge is followed to the book that won the
  merge, so finishing into that book clears the hold. A survivor deleted
  outright is offered a restore. Each offered action is carried out by a
  test.
- **A chapter file that ended up on an unrelated book keeps the folder
  held.** A run counted as done while one of its chapter files sat on a third
  live book, so the split went unnoticed. The row now names that book and
  says to move the file back first.
- **A cut-off repair whose other job-log entries aged out still finishes.**
  The repair's own earlier retires are recognised from its plan entry once
  their own entries are gone.
- **A revert error lists every refused change** (the first five, then a
  count) instead of only the first.
