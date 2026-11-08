### Added

- Internal: core's one journaled swap for restores and factory resets, applied at P0a (`core/internal/restoreswap`), and the boot crash-matrix kit (`coretest/crashmatrix`). The kit kills the boot before, and fails, every durable write from P0a to readiness, then kills the boot after each broken one again. `TestBootCrashMatrix` runs it over hello, and `TestBootCrashMatrixCoversEveryWriter` requires every writer-table label to be faulted.
