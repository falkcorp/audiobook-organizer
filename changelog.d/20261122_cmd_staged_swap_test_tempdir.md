### Fixed

- `TestRunOperationRunner_StagedSwapRefusesTheOpen` no longer fails for a user whose working directory is not writable. Its seeding boot opened the log file under the default state directory, which resolved to a `logs/` folder in the working directory; the test now points `ABK_STATE_DIR` at its own temporary directory before seeding. Test only.
