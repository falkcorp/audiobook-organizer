### Fixed

- `make deploy-release` has a test that a hash the host cannot produce (a garbled answer, or `sha256sum` failing) refuses the deploy before the unit or binary is touched and deletes the fetched file. Test-only.
