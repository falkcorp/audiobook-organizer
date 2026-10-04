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
  the repair used to stop recognising its own earlier work. A fresh plan
  could then split one work into two live books. An entry from another
  fixer is never counted as this repair's, even if its job claims otherwise.
  Old entries without the name are still judged by their job; if that job is
  gone, the row is planned again.
- **A resume accepts a raised primary flag only when this repair raised it.**
  The flag must be on the member that the repair's own hand-off note names,
  or the hand-off must still be owed (its crown written, its note cut off).
  A lowered flag counts only when the repair lowered it after the plan was
  made. A plan stored without its flags or plan time is planned again.
