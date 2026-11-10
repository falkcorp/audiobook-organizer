### Fixed

#### Task item loop: nested ref locks are per item and fail closed, and a nested panic is logged once

In `core/tasks`, an item that holds refs now keeps one lock table for everything nested under it. Nested loops and `ic.LockRefs` calls under one item can no longer hold the same ref at once. A nested lock may name only refs the item holds. Any other ref is refused with the new `ErrNestedLockNotHeld` instead of risking a deadlock against a sibling item. `RC.LockRefs` now documents that `ic.LockRefs` is the one to use inside an item. A panic inside a nested loop logs its stack once instead of twice. Nothing outside `core/` uses `core/tasks` yet.
