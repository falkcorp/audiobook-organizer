### Fixed

#### Task item loop: an item takes all its ref locks in one call

In `core/tasks`, an item without `ExclusiveRefs` could lock refs in stages: `ic.LockRefs(X)`, then `ic.LockRefs(Y)` or a nested loop on Y. Two sibling items doing that in opposite orders deadlocked until the run deadline. Each item now has at most one `LockRefs` call outstanding. A second call before the first is released is refused with the new `ErrLockAlreadyHeld`. While the first call's refs are held, a nested loop may lock only refs that call covers. Release-then-relock still works. Nothing outside `core/` uses `core/tasks` yet.
