<!-- file: docs/executive-summaries/2026-10-21-never-authorless-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b7e2c94-1d3a-4f68-9a0e-c2d84b61f7a3 -->
<!-- last-edited: 2026-10-21 -->

# No book is ever left without an author

## Executive Summary

Two of the library's clean-up tools used to remove a bad author credit even
when it was the only author a book had. Their reasoning was that a credit like
"Track 01" or "Book 2" is not a person, so a book with no author was better
than a book with a fake one. The owner decided on 21 October that this is
wrong: **a book must never be left without an author.** Both tools now keep
the bad credit until a real author has been found, and then swap it.

## What was wrong

- **The junk-author repair** could "unlink" a credit with no replacement. For
  names that are only a label, such as "Book 2 (Unabridged)", "Part 2",
  "Disc 1", "Vol. 01" or "[Track 003] 2", it did that whenever it found no
  evidence of the real author.
- **The author clean-up that deletes junk author records** removed those
  records outright, and every book that depended on one lost its only author
  with it. It has hundreds of "Track 01" to "Track 42" style records in its
  reach, crediting 343 books.

## What changed

- A credit that is only a part, track or book label is now handled as "swap or
  keep". It is swapped only when the metadata provider, or two independent
  kinds of evidence, name a real author. A single file tag is not enough. With
  nothing to go on, the credit stays.
- For every kind of junk credit, the repair now holds a book whose junk credit
  is its only author credit. It is shown as "held" instead of being removed,
  and it is checked again right before anything is written, so a book that
  became author-less since the plan was made is still protected.
- A junk credit that sits next to a real co-author is still removed, because
  the book keeps its author.
- The clean-up that deletes junk author records no longer removes a credit that
  is a book's only author, and it leaves a record in place while any book still
  depends on it. Its report now says how many books and records were held.
- Label detection now covers number ranges ("Books 1-3"), decimals
  ("Book 2.5"), letter labels ("Track A"), front and back matter words
  ("Prologue Chapter 1") and letterless disc or track codes ("1-02"). Names
  that merely contain such words next to a real name are not treated as labels.

## What users will notice

Some books keep a placeholder author such as "Track 01" for longer, and the
repair lists show them as held rather than as removed. Nothing is deleted or
left blank; those books are replaced with a real author once one is found.
Every change stays undoable.
