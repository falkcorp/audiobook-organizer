### Fixed

#### Triggering "transcode" from the tasks page now says why it failed

The `transcode` scheduled task cannot run unattended — it needs a `book_id`, and
the scheduler's trigger signature has no way to carry one. It handled that by
creating an operations row, immediately stamping an error onto it, and returning
it. Because the run endpoint answers `202 Accepted` for any operation it gets
back, the caller was told the task had been accepted, with the actual reason
("transcode requires parameters — use the operations API directly") buried
inside a row nobody was looking at.

It now returns that message as an error, which the endpoint reports as a `400`
with the text in the response body, and writes no operations row at all.

### Changed

#### Scheduler uses the real dedup parameter types

Three scheduled dedup tasks declared their own local copies of parameter structs
that live in `internal/dedup`. A copy is coupled to the real type only by its
JSON field names, so the two drift apart silently — and all three had:
they still carried a `legacy_op_id` the operations stopped reading, and the
series-prune copy had additionally never gained the `detail` field the real type
has. They now use the real types, so the compiler catches any future divergence.
