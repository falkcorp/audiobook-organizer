### Changed

- Folder-books: listening progress on a folder-book no longer blocks the owner's apply (owner decision 2026-11-05: "ignore the progress, reset them all to 0"). A plain apply still refuses such a row; the owner's click retires the folder-book with the progress left on it, untouched and recoverable through the operation's revert, and the new books start at 0. An iTunes id on any member is still a hard hold, checked before progress is reported.
