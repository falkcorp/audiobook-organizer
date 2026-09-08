### Fixed

- **Broad library searches took 13.8–28.0 s because every file lookup queued
  behind one process-global mutex held across an fsync.**
  `MintOrGetSyncFileID` took `syncFileMintMu` unconditionally at the top of the
  function — including for pairs that already had an ID and needed no mutual
  exclusion at all — and the lock is held across a `pebble.Sync` commit. The ABS
  mapper called it once per FILE, so a 12-result search page made ~480
  acquisitions of a single lock, serialized against each other and against
  whatever the metadata apply job was minting at the same moment. The book-level
  twin `MintOrGetSyncID` had the same shape.
- The lookup now happens **outside** the lock on both paths, with a re-check
  under the lock before minting (the double-checked pattern). Only a genuine
  first encounter reaches the mutex. Pebble is safe for concurrent reads against
  writes, so the unlocked point-get needs no coordination of its own.
- Added `MintOrGetSyncFileIDs(bookID, fileIDs)` — resolves a whole book's files
  in **one** lock acquisition, one Pebble batch and one fsync, instead of one of
  each per file — and switched the ABS item mapper to it. Per search page that
  is ~480 lock acquisitions down to 12.
- **This was diagnosed by measurement after the first hypothesis was wrong.**
  `os.Stat` on the book files, the obvious suspect on that path, measured 0.01 ms
  median / 2 ms for 61 calls against production storage — free. Parallelizing the
  stats, the fix that hypothesis implied, would have bought nothing: the ABS
  mapper already fans out over books at `runtime.NumCPU()`, and every one of those
  workers funnelled through the same global lock. The pool was decorative.
- The same-pair invariant is preserved and now tested from both entry points: the
  `Get`s that decide what to mint and the `Commit` that writes it happen inside a
  single hold of the mutex, because a Pebble batch is not a read-modify-write
  transaction and two concurrent batches could otherwise both miss the same pair
  and mint two durable IDs for it. New tests race the singular and batch paths
  against each other on one pair under `-race` and assert a single winner with
  exactly one persisted record.
- A dedupe bug in the first cut of the batch method was caught by its own test
  before it shipped: the duplicate check consulted the results map, which only
  holds pairs that were *found*, so a repeated **missing** file id was minted once
  per occurrence — three records for one file, one reachable through the lookup
  key and two live but orphaned. Uncommitted batch writes are invisible to the
  re-check inside the batch, so deduplication has to happen before the mint list
  is built, not during it.
