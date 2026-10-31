### Fixed

- `TestRun_EmptyNeverHarvestedAuthorsCompleteAfterTheWindow` judged "still due" against the wall clock while the runs it checked happened on a fixed 2026-10 test clock, so it started failing on every branch once the default reharvest interval had elapsed on the night of 2026-10-30. The check now uses the runs' own clock.
