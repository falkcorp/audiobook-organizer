### Fixed

- Three checks that were red on main are green: the deadcode ratchet (the expires_in/expires_at "not both" check is now written once in `KeyCreate.validate`, which `keySpec` also runs, instead of a separate unreachable helper), the core errcheck ratchet (`buildid.fileSHA256` and `typedload.readExportData` now return a failing `Close` instead of dropping it, and `core/.errcheck-baseline` drops to 0), and the shell storage-key test (the operations indicator frame kept its collapse state under the pre-shell literal key; it now uses a key under the app ID).
