### Added

- `tasks.Progress.Update(done, total, msg)` reports a position and a status line in one call, and the built-in kinds' progress closures use it. A Note-only run now writes a progress row (and the ops bell's `op.updated.message`) up to once per progress interval, with a trailing flush at the end of each window so the last update of a burst is not lost; this corrects the earlier "no extra store writes" claim.
