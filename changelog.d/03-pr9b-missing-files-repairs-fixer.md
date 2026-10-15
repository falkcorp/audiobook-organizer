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

Only a hash match keeps the book's current-file hashes. For an original-hash or filename match, `file_hash` and `organized_file_hash` are cleared in the same write as the path, and the row's hash is cleared too. The original-file hash is kept. The cleared book hashes are not refilled automatically: `backfill-file-hashes` (manual) refills only the file row's hash, and the book's come back only when a scan re-reads the file or the book is organized (todo `MISSING-FILES-BOOK-HASH-REFILL`).

If an apply is stopped between the two writes (cancel, restart), resuming it or a new apply of the row finishes the second write. Step 1 counts as done only when the fixer's own journal shows it: the latest path change on the book (newest by time, then by id) must be an unreverted `book_path_update` from `reconcile.missing-files` with the planned old and new paths. The book's hashes must also still be as that write left them: for a hash match, that write cleared none; for any other match, both are still empty. A book moved by anything else, including the legacy Reconcile Apply (which writes no file row), is shown as `path_changed` and refused.

A new op that finishes an earlier op's row journals the path change and hash clears again as journal-only rows. Once its step 2 is written, it takes the move over: every live row of each earlier op with a step 1 of the same move (the path change, the hash clears, and any repoint row for a write that op never made) is marked reverted without a write (`Writer.SupersedeChanges`, through the journal's optional `repairs.ChangeReverter` capability). The finishing op's revert then undoes the whole move. Reverting an earlier op restores nothing for that book; when that op touched no other book, the revert says it is already reverted. The book and its file row always end up together, in either order. The hand-off is best-effort: if the journal cannot mark the rows, or the server stops between step 2 and the mark, both ops keep their rows (logged).

When the op that wrote step 1 resumes and its step 2 is refused, the row is `partially_applied`. When a different op's step 2 is refused, it wrote nothing, so the row is `changed_since_plan` and its rows are voided.

`RepointBookFile` now voids its journal row when the compare-and-set write is refused, and its refusal names the field that changed (the path, or the Missing flag).

While the file index is still warming up after a restart, the trial fails with "file index warming up; retry in a few minutes", and an apply holds the row as "retry later".

#### Repairs fixers can require a stronger permission

A fixer can now require more than the lane's `library.edit_metadata` (`repairs.PermissionScoped`). Starting a plan, an apply or an owner apply checks it, with the same rules as the route middleware: allowed when auth is disabled and on a first run with no users. Missing files requires `settings.manage`.

### Changed

#### "Match missing files" points at Repairs

The command's description now says its matches are reviewed in Repairs, under Missing files.
