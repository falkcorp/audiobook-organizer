### Fixed

#### Editing a book no longer overwrites its original publication year

The Edit Metadata dialog has a single "Year" box, which means the audiobook's
release year. Saving wrote that value into the *original publication year* as
well — so editing a 1937 novel with a 2010 audiobook could replace 1937 with
2010, silently. The dialog has no publication-year field, so nothing there
should ever have changed it; that write is removed.

The Year, ISBN-10 and ISBN-13 boxes also rendered empty whatever was stored,
and now show the current values.
