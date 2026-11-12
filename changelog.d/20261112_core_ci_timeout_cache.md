### Changed

- ci: core-ci's job timeout is 45 minutes (was 30), which cancelled about 3 of 12 runs on cold runners. The Go module and build cache is now restored with a prefix fallback instead of setup-go's exact-key cache, so a go.sum change no longer starts from nothing, and a cancelled run no longer leaves the next one cold.
