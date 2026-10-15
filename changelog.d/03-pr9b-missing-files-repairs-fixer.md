### Added

#### Repairs fixer "Missing files" (`reconcile.missing-files`) replaces the Reconcile tab

The fixer appears in `/review?lane=repairs` and takes over from the old Reconcile tab, which 03-PR10 retires. Its trial reads the latest saved reconcile scan, chosen by the same rule as `GET /operations/reconcile/scan/latest`: the newest completed scan whose result decodes, including nightly runs recorded under the retired `maintenance.reconcile-scan` id. It lists one row per book:

- each match from a book to an untracked file. The row proposes the new path; its class is the match type, and the match type and confidence are in its reason and evidence;
- each book the scan found no file for, as a skipped row with the reason "no candidate file".

A match is offered only while all of these hold:

- the book still points at the path the scan found missing;
- that path does not exist. An existing folder counts as present, and a path that cannot be read is an error row, never "missing";
- the book has exactly one file row at that path and none at the matched file;
- the matched file is a regular file on disk, and its size and modification time are part of the check;
- no other book holds the matched file, by path or by a file row;
- the scan did not match that file to a second book. If it did, every row naming the file is skipped (`shared_match`).

iTunes books, including a book with an iTunes path, and matches onto files in the iTunes library are skipped with the reason "iTunes".

#### Missing files applies by writing the repoint itself

Owner decision 2026-10-14. Right before writing, an apply re-reads:

- who holds the matched file;
- the book;
- whether the old file is still missing;
- the iTunes check.

It then makes two compare-and-set writes, each journaled and revertible from the Operations page:

1. It points the book at the matched file.
2. It repoints the book's file row there.

Nothing moves on disk, no file row is created or deleted, and the fixer no longer hands its picks to Reconcile Apply.

Only a hash match keeps the book's current-file hashes. For an original-hash or filename match, `file_hash` and `organized_file_hash` are cleared in the same write as the path, and the row's hash is cleared too. The original-file hash is kept. If the apply is stopped between the two writes (cancel, restart), resuming it finishes the second write.

While the file index is still warming up after a restart, the trial fails with "file index warming up; retry in a few minutes", and an apply holds the row as "retry later".

#### Repairs fixers can require a stronger permission

A fixer can now require more than the lane's `library.edit_metadata` (`repairs.PermissionScoped`). Starting a plan, an apply or an owner apply checks it, with the same rules as the route middleware: allowed when auth is disabled and on a first run with no users. Missing files requires `settings.manage`.

### Changed

#### "Match missing files" points at Repairs

The command's description now says its matches are reviewed in Repairs, under Missing files.
