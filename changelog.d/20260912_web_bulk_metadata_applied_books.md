### Fixed

#### Bulk metadata search: applied books now leave the list

The per-book metadata wizard opened from the audiobooks list kept a book in its
queue after metadata was applied to it: the "Skip applied" filter only read the
review status the list page passed in when the dialog opened, and ignored what
had been applied during the session. The filter now also drops books applied in
this session, and "Skip applied" is on by default. Turning it off still shows
every selected book, with applied ones marked "Applied".

The wizard now tracks the current book by id rather than list position, so
removing the applied book no longer skips the book after it. A book whose apply
fails stays current, with the error shown. Progress is measured against the
starting work set, so it no longer runs past 100% as the list shrinks, and the
"Close"/"Done" label no longer flips to "Done" early. When the last book is
applied, the empty screen keeps the "Undo Last" button, and undoing brings the
book back into the list.
