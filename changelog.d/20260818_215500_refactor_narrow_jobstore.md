### Changed

#### `maintenance.JobStore` narrowed from 187 methods to 52

`JobStore` was twelve `database.*` embeds. #2534 had already brought `Run`'s parameter
down from `database.Store` (398 methods, 40 embeds) to this, and the arbitration
deliberately chose a shared store over per-job interfaces. What it could not know is
how little of the 187 the jobs touch.

Measured by emptying `JobStore` and reading the compiler's enumeration across all 37
jobs: **37 methods called directly, plus 15 more reached only through the narrow
slices in `jobs/store_slices.go` — 52 of 187.** No job body changed.

It is kept as a composition of seven focused interfaces rather than a flat list of 52,
because `interfacebloat` counts declared entries: the flat form trades a smaller method
set for a wider declaration. Seven leaves one slot of headroom under the limit of
eight, so the next job needing a new capability adds a method to a group instead of
restructuring the type.

`.interface-width-baseline` drops 3 → 2. Its own note had said this item was settled
by the #2534 arbitration and that the number was "expected to hold until those
decisions are taken" — that decision was taken. #2534's choice of a shared store over
per-job interfaces still stands; what changed is that the shared store no longer
carries 135 methods nobody calls.

`database.MockStore` still satisfies the narrower interface, so the job tests that
build one compile unchanged.
