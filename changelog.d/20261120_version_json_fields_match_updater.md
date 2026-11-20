### Added

- A root test (`main_versioninfo_test.go`) pins that the field names `--version-json` prints (the json tags of `versionInfo`) are exactly the set, in the same order, that the updater's step-3 self-test requires (`updater.VersionFields()`, a new exported copy-returning accessor in `core/updater/selftest.go`). Before, the two lists were kept equal by hand and nothing failed if they drifted. They match today.
