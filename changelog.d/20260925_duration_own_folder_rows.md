### Fixed

- A book whose file rows span its own library folder and other folders (the iTunes copy, old chapter files) now counts only its own-folder rows toward its duration and size: in ABS (item, play timeline, progress/finished math) and in the stored book aggregate. Summing every copy inflated totals (e.g. 17.6 h read as 43.7 h). No rows are deleted or rewritten; the others are simply not summed while the book has a present row in its own folder.
- `maintenance.duration-backfill` with `zero_rows_only` no longer skips these books: it fills the zero rows in the book's own folder, reports the others as stray and leaves them untouched, still skips a book with no own-folder row, and never writes a book whose counted rows lie in the iTunes tree.
