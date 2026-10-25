Fixed the second and third re-review findings on the retired-version readers.
When a combine plans a single-file book's file and other books already hold a row
at that path, it now takes the row of a trashed book first, then the row of the
one live book holding it, and a row the same combine already moves onto the
survivor is left to that move (known from the rows the combine holds, not from a
lookup, so it also holds while the in-memory index is warming up). With two or
more live holders, or a holder whose book no longer exists, it plans nothing:
the absorbed book is still retired, but the combine's result and its undo journal
both say "path held by N live book(s) (M row(s))" and the other books keep their
rows. Taking another book's row now needs the complete holder list: while the
index is still warming up after a restart the combine plans nothing for that file
and warns, instead of picking whichever single row the fallback index names. A
holder that cannot be read refuses the combine before it writes anything, rather
than failing after its journal. Taking a trashed book's row is noted on the
journal. Undoing a combine no longer refuses outright when a row it took from
another book has since lost that book: the row stays on the survivor and the undo
warns, as it already did for a row the combine created. A combine whose rowless
primary book has the same file path as a row of a book it absorbs no longer stops
after writing its journal with "file not found". Two merge tests that combined a
book before the in-memory index finished warming up failed on a loaded runner; the
test store now waits for the warmup, so no test starts without it.
