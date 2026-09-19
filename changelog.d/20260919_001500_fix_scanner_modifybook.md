- Stopped library scans from silently undoing other work. The scanner read a book, changed the fields
  it owns, and wrote the whole row back, so anything committed in between — a metadata apply, an AI
  parse write-back, a maintenance repair — was reverted. Nothing failed and nothing was logged, because
  the write itself succeeded. Every scanner write now changes only the columns the scanner owns, on the
  row as it actually stands at write time. This affected the rescan of every already-imported book, the
  file-path normalization of single-file books, and the automatic version-linking of duplicates.
- Fixed duplicate detection creating version groups containing a single book. When two copies of a book
  were linked into a version group, a group already established by something else was overwritten, and a
  failed link still stamped the new copy with a group of its own. Either way a book advertised other
  versions that could not be listed. A copy now joins the existing group, and is left ungrouped when it
  cannot be linked at all.
