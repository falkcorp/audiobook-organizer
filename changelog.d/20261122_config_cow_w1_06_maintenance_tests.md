### Changed

- Tests in the maintenance plugin and maintenance jobs packages (config-cow sweep slice W1-06) now set and read the configuration through the copy-on-write API (config.Get, config.SetForTest, config.ReplaceForTest) instead of the package-global. Test set-up only; no production behaviour changes.
