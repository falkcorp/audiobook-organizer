### Fixed

- Tests that need `ffmpeg`, `ffprobe` or `fpcalc` now skip when the tool is on `PATH` but cannot start (for example a build with a missing shared library) instead of failing. The guards share one helper, `internal/testutil/tooltest`, which probes the binary with `-version`. This replaces the `exec.LookPath`-only checks in 31 test files and the integration synthetic-audio helper.
