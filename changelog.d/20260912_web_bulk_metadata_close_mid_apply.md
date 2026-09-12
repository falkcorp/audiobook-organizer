### Fixed

#### Bulk metadata search: closing mid-apply no longer leaks into the next session

The per-book metadata wizard stays mounted when it is closed, so a metadata
apply still in flight when the user pressed Close used to land in the closed
dialog's state. The next time the wizard opened, it showed an "Undo Last" for
a book from the earlier session, hid that book as already applied if it was
selected again, and reported "1 applied" for nothing done in the new session. The
library list was also not reloaded, so it kept showing the book's old metadata
even though the apply had succeeded.

A request that settles after the dialog was closed now leaves the wizard alone:
no toast, no status change, no Undo entry. A late success (apply or undo) still
reloads the library list, because the book did change on the server. It
reloads only the list and leaves the book selection alone, so a late write
cannot empty the books of a wizard the user has since reopened on a new
selection. The same holds for an Undo clicked on a toast that outlived its
dialog. A late
failure shows no error, since the user already left the dialog. The same guard
covers the metadata search, "No match", and both undo buttons. Once the page
itself is gone, a late request updates nothing and reloads nothing.
