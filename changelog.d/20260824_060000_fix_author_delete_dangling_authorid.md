### Fixed

#### Splitting an author, or turning one into a narrator, no longer breaks the book's author link

Two actions in the authors screen — splitting a combined author like
`"Author1 / Author2"` into separate people, and reclassifying an author as a
narrator — relinked the affected books correctly in one place and forgot them
in another. Books whose main author was the one being changed kept an internal
pointer to an author record that had just been deleted.

The effect depended on which screen you were looking at. In the library list
those books showed **no author at all**; open one directly and it still showed
the old name. The same book gave two different answers, and nothing reported an
error either way. Because the duplicate-cleanup screen runs these actions in
bulk, a single click could affect many books at once.

Now the split moves the book to the correct individual author, and the
reclassify promotes whichever author remains — or, if the reclassified person
was the only author listed, leaves the book with no author link rather than a
broken one.

One remnant is not fixed: a book left with no author can still show the old
name on its detail page, because that stored copy of the name cannot currently
be cleared. The library list is unaffected. **Books already affected are not
repaired by this change** — it stops new ones from being created.

Two other copies of this logic (the scheduled author-split job and the
maintenance author-split scan) were already correct; the screen actions were
the ones that had drifted.
