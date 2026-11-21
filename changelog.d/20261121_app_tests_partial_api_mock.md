### Fixed

- The App-level web tests (28 tests in 5 files) were red on main because their hand-written `services/api` mock lacked exports that App started using. They now layer their stubs over the real module (`importOriginal` plus overrides) through the shared `createAppApiMock(actual)`, so a newly added export can no longer break them. `App.health` and `App.setupGate` drop their private copies of the same stub list.
