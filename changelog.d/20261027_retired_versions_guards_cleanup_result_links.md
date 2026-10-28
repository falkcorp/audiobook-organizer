### Fixed

#### The trash cleanup's kept and needs-attention counts click through

Review follow-up for the retired-version guards (every count shown to the user must lead to the items it counts).

- Opening a finished `maintenance.trash-cleanup` run on the Activity page now shows what it stored: the count of versions purged with files kept on disk and the count that need attention each link to the Version Management page narrowed to exactly those versions (`/versions?op=<operation id>&set=kept|needs_attention`, which opens the Purged or Trash tab and reads the ids from the run's stored result). The books behind each count link to the book, and the kept file paths are behind a button that carries their count. A run that stopped at a busy merge lock says so in one plain line (nothing failed).
- The Book ID column of the Version Management page is a link to the book.
- The purge dialog's "holds retired versions" reason no longer says "retired versions of another book's files"; like the delete refusal it reads "file rows kept for another version of the book (retired versions)".
