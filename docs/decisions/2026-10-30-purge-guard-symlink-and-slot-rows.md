<!-- file: docs/decisions/2026-10-30-purge-guard-symlink-and-slot-rows.md -->
<!-- version: 1.0.0 -->
<!-- guid: c4da87e0-3a2d-4af1-b514-0697208afe92 -->
<!-- last-edited: 2026-10-30 -->

# Purge guard: links that resolve to a trashed version's file, and slot rows in the timeline

## Decision log

TODO-VERSION-PURGE-GUARD-FOLLOWUPS had two parts. The owner was not asked
(autonomy instruction of 2026-10-30); each open choice below took the
recommended option.

### 1. Another book's symlink (internal/versions/serve_guard.go, serve_guard_links.go)

Chosen: a link index. The first time a path reaches the "another live book"
rules, the guard resolves every non-retired book_file row and every Book.FilePath
of the other live books once and keys each by what it resolves to, link hops
included. A purge target is looked up by its folder-resolved spelling
(leaf kept) and by its fully resolved spelling. A FilePath that resolves to a
folder serves the files under it, except through a .versions/ slot (the existing
pathAndAncestors rule).

- Scope: all rows, not only OrganizeMethod "symlink". Nothing writes that field
  (grep: it is read and copied, never assigned), and links also come from
  scans, imports, hand-made links and repoints (repoint_book_file_rows accepts a
  link). Rejected: scoping by OrganizeMethod (incomplete), by same parent folder
  (a link can be anywhere), or by "symlink-strategy books" (the strategy is a
  global setting, not a per-book fact).
- Cost: one os.Lstat per row, one EvalSymlinks per distinct folder (cached),
  workers = runtime.NumCPU() over chunks of 256 rows (errgroup SetLimit); the
  index is merged serially and the liveness lookups (servedGuard.others, a plain
  map) are serial. Built lazily per guard, so a purge whose files the own book
  serves never pays for it. The guard rebuilt after the claim builds its own
  index: a second O(rows) pass per purged version, accepted because it is what
  lets a link made while the version was being claimed be seen. Rejected:
  carrying the first index into the rebuilt guard (it would miss exactly that).
- Hops: every link on the way is a key, not only the end. A link to a link in
  the slot breaks if the middle link is removed.
- Fail closed: any resolution error other than "not there" (fs.ErrNotExist,
  ENOTDIR) fails the whole index, so the purge deletes nothing; a link loop
  wraps errSymlinkLoop; a store without database.BookCompletenessReader fails
  the same way. Consequence the owner should know: one unreadable or looping path
  held by ANY other book blocks every trash purge until it is fixed (the
  warning in the log names the path and book). Rejected: skipping the unreadable
  row, which is the silent fail-open this removes.
- Store access: database.AsCapability[database.BookCompletenessReader] with the
  Complete getters (GetAllBookFilesCoreComplete, GetAllBooksCoreComplete(0, 0)),
  the readers documented for a caller that treats an ABSENT row as permission to
  destroy. Rejected: adding methods to versionPurger (interface-width ratchet).
- The version's own book is skipped by the index: its links are the own-book
  rules' business (inVersionSlot, hard-link comparison), unchanged.
- Not done: the same shape exists for a trashed OTHER book (otherBooksNamingPath
  names a trashed book's FilePath by exact path only). Out of scope.

### 2. Slot rows in SplitOwnFolderFiles (internal/database/book_own_folder.go)

Chosen: a new partition, OwnFolderSplit.Slots, that Counted drops, instead of
only removing slot rows from Own. A row that is not inside Dir fell into
Others, which Counted keeps, so removing it from Own alone would have changed
nothing the player sees.

- Slot rows are neither Own, Others nor counted in HasOutside (a book with an
  alt must not start to look like it has rows outside its folder, which would
  change the zero_rows_only skip).
- They are left out of the copy clustering entirely. Before, an alt slot row
  with the same hash as the live row could win the keeper contest on row order
  and mark the LIVE row a copy, which would have dropped the track from the
  timeline once slot rows were dropped. Pinned by a test with the slot row first.
- Recognition: database cannot import internal/versions (cycle), so the name and
  the predicates moved to internal/pathutil (VersionsDirName, UnderVersionsDir,
  HasSlotPair); versions.VersionsDirName is now an alias of the pathutil constant,
  hasSlotPair delegates, and ownBookServes uses UnderVersionsDir in place of its
  hand loop. A row is a slot row when its path is under a .versions/ directory
  below the book's folder, OR when its path carries .versions/<its own
  VersionID>. The second test does not depend on Book.FilePath, which is known
  to lag, so a stale FilePath no longer sends slot rows into Others.
- Callers (findReferences/grep of SplitOwnFolderFiles and OwnFolderFiles): the
  ABS mapper and countedBookFiles (items, userdata, progress), RecomputeBookAggregates,
  and duration_backfill (zero-rows and normal mode). Only duration_backfill's normal
  mode depended on the old shape: it filtered only when there were copies, so with
  slots and no copies it measured slot rows against a total that no longer counts
  them and the book read as "would change" on every run. It now filters when there
  are slots too (test added).
- Visible change: RecomputeBookAggregates lowers the stored Duration and
  FileSize of any book that has an alternate or trashed version in a slot, the
  next time it runs for that book. That is the intended correction.
- Rejected: filtering on the version's status (needs a store read per book in
  a pure function and is wrong for a stale status); skipping any row with a
  .versions component anywhere (a book folder that itself sits under a .versions
  tree would lose every row).

### Not touched, noted

- internal/plugins/deluge/path_update.go and internal/deluge/integration.go
  hard-code ".versions"; they could use pathutil.VersionsDirName.
- A row of the ACTIVE version that sits in its own slot (a half-done swap) is
  also treated as a slot row now and is not played; ResumeVersionSwap is the
  repair path for that state.
