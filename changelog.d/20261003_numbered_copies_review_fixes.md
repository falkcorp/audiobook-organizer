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
