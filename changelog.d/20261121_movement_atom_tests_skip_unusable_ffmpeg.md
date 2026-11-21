### Fixed

- Three movement-atom tests in `internal/server` failed on a host whose `ffmpeg` is on `PATH` but cannot start (missing shared library, exit 127) instead of skipping. They now probe the binary with `ffmpeg -version` and skip when it does not run.
