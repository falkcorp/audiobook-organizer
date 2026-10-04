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
