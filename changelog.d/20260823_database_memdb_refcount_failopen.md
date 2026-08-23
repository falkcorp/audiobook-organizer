### Fixed

#### The series delete guard could still report a series as unreferenced when it had simply failed to read it

The nightly `dedup.series-prune` job deletes a series only when nothing points
at it any more. The counter that answers that question was hardened in an
earlier fix so it would **abort** rather than return a short count — because a
short count reads as "referenced by nothing", which is the permissive answer
being handed to something that deletes on it. That hardening was real, and in
production it never ran.

`GetAllSeriesBookRefCounts` has two implementations: a scan of the on-disk
store, and a scan of the in-memory query layer. It prefers the in-memory layer
whenever that layer is available, and availability is hardcoded on. So every
production call took the in-memory path, and the hardening had been applied
only to the other one.

The in-memory layer is a **lossy** copy. It is rebuilt from disk at startup, and
that rebuild deliberately skips any row it cannot decode or cannot insert rather
than refusing to start at all — one bad row must not leave the service with no
index. But those rows were then dropped silently and the layer was published as
though it were complete. A dropped book row is a book whose series reference
nobody can see, so the series counts zero and becomes eligible for deletion
while the book is still on disk holding its ID. That is the exact failure the
guard exists to prevent, arriving through the door the guard was not watching.

The rebuild now records what it lost, and the counter refuses to answer from a
table it knows is short. Two details worth stating:

- **Only one of the two ways to lose a row was ever counted.** Rows rejected by
  an index rule were tallied; rows that failed to decode were not, so the
  `skipped_total` figure an operator would check after a restart could read zero
  through a rebuild that had dropped every unreadable row in the library. Both
  paths are counted now, and the startup log line is accurate for the first
  time.
- **The refusal does not stall the job.** When the in-memory layer is known to
  be short, the call falls through to the on-disk scan, which is authoritative
  and already hardened. So the answer is *correct*, not merely safe, and the
  nightly prune keeps running instead of waiting for the next restart.

The tracking is deliberately not private to startup, because startup is not the
only way the in-memory layer loses a row: a write that succeeds on disk but is
rejected by an in-memory index rule leaves the same divergence with no restart
involved. That path can report through the same mechanism.

Whether this ever actually stranded a series has **not** been measured, and the
number is not recoverable after the fact — a deletion that happened because a
row could not be read leaves no record of the row it could not read. What can be
checked going forward is the `skipped_total` figure in the startup log: it is
now accurate, and a non-zero value means the on-disk scan is being used instead.
