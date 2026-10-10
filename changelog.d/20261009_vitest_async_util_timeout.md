### Fixed

- Frontend tests no longer fail under CI load because a `findBy`/`waitFor` gave up after one second; the testing-library async timeout is now 10 s (the 30 s per-test cap is unchanged).
