### Changed

- core: while the path redactor is the one F8-03b replaces, `diagnostics.New` and `Exporter.Build` return `diagnostics.ErrRedesignPendingF8_03b` outside a test binary and write nothing, from any module or constructor (the build-time archtest guard scans only core). The guard also parses files that no build tag compiles. F8-04 now lists F8-03b under `Depends on`.

### Fixed

- core: the test for the linear naming of colliding bundle keys counts lookups instead of timing them, so a loaded CI runner cannot fail it (two wall-clock bounds had failed core-ci for a scan that was linear).
