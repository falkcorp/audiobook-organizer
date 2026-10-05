### Fixed

#### User-state lock and Audible read-status import: second review pass

- **ABS session sync** now takes the per-(user, book) user-state lock before
  it reads the stored position, and holds it until the write lands. Before
  this, the merge compared against a position read outside the lock.
- **The lock covers more writers.** The Repairs writer's `SetUserState` now
  takes the merge lock first (waiting without losing its scan stand-down
  lease) and the user-state stripe after it, the same order as the revert.
  `readstatus.SetManualStatus` (the web "mark as" endpoints) and the iTunes
  sync's "seed finished" check-then-write now hold the stripe too.
- **The lock documents its order:** merge lock, then stripe, then anything
  else. Still not covered: `readstatus` Recompute/Rebuild with the position
  writes before them, the iTunes position backfill, and the merge follow and
  merge combine paths. These are filed as ARS-LOCK.
- **The ABS PATCH path reads less under the stripe.** It reads the book's
  duration before taking the stripe, so only user state is read under it.
- **`maintenance.audible-read-status` tracks hits more strictly:**
  - On the title tier, only the hits that resolve to the chosen target count
    as copies of the book. Another author's book with the same title no
    longer blocks or skips the import.
  - Re-plan drops hits that have since been soft-deleted.
  - The fixer's clock is now a field, so tests can fix it without changing
    shared state.
- **New lock tests:** every ABS write path, the revert, the Repairs writer
  (merge lock first, cancellable wait) and `SetManualStatus`.
