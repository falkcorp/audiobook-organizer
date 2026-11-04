### Changed

- `BookFile` and `BookFileCore` now expose their duration through `DurationSec()`, `DurationState()`, `SetMeasuredDuration()` and `ResetDuration()`, and production code reads and writes it through those accessors instead of the raw `Duration` field. Behaviour is unchanged (a duration is known when it is above zero); this prepares the type change that will distinguish an unmeasured duration from a measured one.
