### Added

- The logging pipeline can now apply its live settings to a running process. `core.log.level`, `core.log.levels`, `core.log.add_source` and `core.log.redact_keys` are applied by one new call, `Logging.ApplySettings`, with no restart and no lock on the logging path: levels go through `SetLevels`, `add_source` is a per-record switch, and `redact_keys` replaces the redactor's key table atomically, so the single redactor the metrics and HTTP layers also hold changes with it. This is dark until the config flip calls it: the kernel gains `watchLogConfig`, which subscribes to the `core.log` section, but boot does not build the layered config store yet, so nothing changes in a running server and the server behaves as it did (the log level is read once at start).

### Fixed

- `core.log` now refuses values it cannot apply. A per-package level that is not trace, debug, info, warn or error, an empty package name, and a `redact_keys` entry made only of separators (which would never match, leaving a key the operator believed protected in the clear) are refused by the config layer, so a change is never half-applied.
