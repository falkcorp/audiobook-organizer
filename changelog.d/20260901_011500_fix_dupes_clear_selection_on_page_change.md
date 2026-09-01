### Fixed

- **Dupes review: a selection no longer survives a page turn.** Selecting rows on
  one page of the duplicate-candidate lane and then paginating left those rows
  armed for "Merge Selected" while they were off screen, so the reviewer could
  irreversibly merge pairs they could not see — this lane has no undo. Changing
  the page or the page size now clears the selection and says so with a toast.
  The shift-click anchor is cleared with it: it is an index into the visible
  rows, so carrying it across a page turn let the next shift-click extend a
  range from an unrelated row.
